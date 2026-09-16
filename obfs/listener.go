package obfs

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"os"
	"sync"
	"time"

	"github.com/pion/dtls/v3"
	"github.com/pion/dtls/v3/pkg/crypto/selfsign"
	"github.com/pion/transport/v4/deadline"
)

// Listener is the server side of one obfuscation mode: it accepts obfuscated
// sessions on one UDP socket and yields a net.Conn per allocation. It is the
// production counterpart of the upstream servers (cacggghp dtls, WDTT wrap,
// anton48 -srtp) and what obfstest wraps for tests.
type Listener struct {
	addr  net.Addr
	conns chan net.Conn
	close func()
	once  sync.Once
}

// ListenOptions carries the wrap key (or the password it is derived from);
// srtp and dtls need nothing.
type ListenOptions struct {
	Password string
	WrapKey  []byte
	Video    bool
	// Cert, when set, is the server's DTLS certificate. A stable certificate
	// (persisted across restarts) has a stable CertFingerprint, which is what
	// clients pin (see Options.ServerFingerprint). Nil generates a fresh
	// self-signed certificate each start, whose fingerprint changes on restart
	// and so cannot be pinned.
	Cert *tls.Certificate
}

func (l *Listener) Addr() net.Addr { return l.addr }

func (l *Listener) Accept(ctx context.Context) (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (l *Listener) Close() error {
	l.once.Do(l.close)
	return nil
}

func Listen(mode Mode, address string, o ListenOptions) (*Listener, error) {
	var (
		cert tls.Certificate
		err  error
	)
	if o.Cert != nil {
		cert = *o.Cert
	} else if cert, err = selfsign.GenerateSelfSigned(); err != nil {
		return nil, err
	}
	switch mode {
	case ModeDTLS:
		return listenDTLS(address, cert)
	case ModeWrap:
		key := o.WrapKey
		if key == nil {
			if o.Password == "" {
				return nil, errors.New("obfs: wrap needs a password or wrap key")
			}
			key, err = DeriveWrapKey(o.Password)
			if err != nil {
				return nil, err
			}
		}
		return listenWrap(address, cert, key, o.Video)
	case ModeSRTP:
		return listenSRTP(address, cert)
	default:
		return nil, errors.New("obfs: unknown mode " + string(mode))
	}
}

func serverOptions(cert tls.Certificate) []dtls.ServerOption {
	return []dtls.ServerOption{
		dtls.WithCertificates(cert),
		dtls.WithExtendedMasterSecret(dtls.RequireExtendedMasterSecret),
		dtls.WithCipherSuites(dtls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256),
		dtls.WithConnectionIDGenerator(dtls.RandomCIDGenerator(8)),
	}
}

func listenDTLS(address string, cert tls.Certificate) (*Listener, error) {
	laddr, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		return nil, err
	}
	ln, err := dtls.ListenWithOptions("udp", laddr, serverOptions(cert)...)
	if err != nil {
		return nil, err
	}
	l := &Listener{addr: ln.Addr(), conns: make(chan net.Conn, 64)}
	ctx, cancel := context.WithCancel(context.Background())
	l.close = func() { cancel(); _ = ln.Close() }
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				dc := c.(*dtls.Conn)
				hctx, cancel := context.WithTimeout(ctx, dtlsHandshakeTimeout)
				defer cancel()
				if err := dc.HandshakeContext(hctx); err != nil {
					_ = dc.Close()
					return
				}
				select {
				case l.conns <- dc:
				case <-ctx.Done():
					_ = dc.Close()
				}
			}()
		}
	}()
	return l, nil
}

func listenWrap(address string, cert tls.Certificate, key []byte, video bool) (*Listener, error) {
	laddr, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		return nil, err
	}
	raw, err := net.ListenUDP("udp", laddr)
	if err != nil {
		return nil, err
	}
	l := &Listener{addr: raw.LocalAddr(), conns: make(chan net.Conn, 64)}
	ctx, cancel := context.WithCancel(context.Background())
	l.close = func() { cancel(); _ = raw.Close() }
	go serveWrap(ctx, raw, cert, key, video, l.conns)
	return l, nil
}

// serveWrap is the WDTT server's per-source demux over one UDP socket, in the
// same shape as serveSRTP: it keys sessions by source address and hands each
// source's packets to a WRAP-then-DTLS handshake. It replaces pion's
// udp.Listener, whose internal WaitGroup is not safe against Accept racing
// Close.
func serveWrap(ctx context.Context, raw net.PacketConn, cert tls.Certificate, key []byte, video bool, out chan<- net.Conn) {
	ps := newPerSource(srcSessionTTL)
	go ps.reap(ctx)
	buf := make([]byte, 2048)
	for {
		n, addr, err := raw.ReadFrom(buf)
		if err != nil {
			return
		}
		if n == 0 {
			continue
		}
		pkt := make([]byte, n)
		copy(pkt, buf[:n])

		src := addr
		srcKey := addr.String()
		ps.dispatch(time.Now(), srcKey, func(_ []byte) (func([]byte), bool) {
			ch := make(chan []byte, 64)
			go acceptWrapSession(ctx, raw, src, cert, key, video, ch, out, ps, srcKey)
			return func(p []byte) { trySend(ch, p) }, true
		}, pkt)
	}
}

func acceptWrapSession(ctx context.Context, raw net.PacketConn, src net.Addr, cert tls.Certificate, key []byte, video bool, pkts chan []byte, out chan<- net.Conn, ps *perSource, srcKey string) {
	codec, err := NewWrapCodec(key, video)
	if err != nil {
		ps.remove(srcKey)
		return
	}
	// side delivers this source's raw packets (still WRAP-enveloped) and writes
	// back to the shared socket; NewWrapPacketConn unwraps on read and wraps on
	// write, and DTLS runs on top of that.
	side := &sessionConn{ctx: ctx, raw: raw, src: src, dtlsCh: pkts, dl: deadline.New()}
	pc := NewWrapPacketConn(side, codec)
	dc, err := dtls.ServerWithOptions(pc, src, serverOptions(cert)...)
	if err != nil {
		ps.remove(srcKey)
		return
	}
	hctx, cancel := context.WithTimeout(ctx, dtlsHandshakeTimeout)
	defer cancel()
	if err := dc.HandshakeContext(hctx); err != nil {
		_ = dc.Close()
		ps.remove(srcKey)
		return
	}
	select {
	case out <- dc:
	case <-ctx.Done():
		_ = dc.Close()
	}
}

func listenSRTP(address string, cert tls.Certificate) (*Listener, error) {
	laddr, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		return nil, err
	}
	raw, err := net.ListenUDP("udp", laddr)
	if err != nil {
		return nil, err
	}
	l := &Listener{addr: raw.LocalAddr(), conns: make(chan net.Conn, 64)}
	ctx, cancel := context.WithCancel(context.Background())
	l.close = func() { cancel(); _ = raw.Close() }
	opts := append(serverOptions(cert), dtls.WithSRTPProtectionProfiles(dtls.SRTP_AES128_CM_HMAC_SHA1_80))
	go serveSRTP(ctx, raw, opts, l.conns)
	return l, nil
}

// srcSessionTTL is how long a per-source demux entry lives with no packets
// before the reaper drops it. It is well above the client's zombie timeout
// (mux ZombieAfter, 120s), so only sessions already dead upstream are reaped,
// while roaming or NAT-rebinding sources cannot grow the map without bound.
var srcSessionTTL = 5 * time.Minute

// dtlsHandshakeTimeout bounds one server-side DTLS handshake. Without it a
// source that sends a first packet and then goes silent (a port scan, a spoofed
// address) would leave a goroutine and its DTLS state blocked on the listener's
// lifetime context until the server exits. pion/dtls has no handshake timeout
// of its own, so we impose one.
var dtlsHandshakeTimeout = 20 * time.Second

// trySend delivers a packet to a per-source channel without blocking: a full
// channel drops the packet (DTLS retransmits its handshake and media payload
// is best effort), so one slow or flooding source cannot stall the shared read
// loop for every other source.
func trySend(ch chan []byte, pkt []byte) {
	select {
	case ch <- pkt:
	default:
	}
}

type srcEntry struct {
	deliver  func(pkt []byte)
	lastSeen time.Time
}

// perSource is the shared per-source-address demux state for serveSRTP and
// serveWrap: it maps a source address to its delivery closure, refreshes a
// last-seen timestamp, and reaps idle entries so the map stays bounded.
type perSource struct {
	mu       sync.Mutex
	sessions map[string]*srcEntry
	ttl      time.Duration
}

func newPerSource(ttl time.Duration) *perSource {
	return &perSource{sessions: map[string]*srcEntry{}, ttl: ttl}
}

// dispatch routes one packet to its source's delivery closure, creating the
// source (via newEntry) on first sight. newEntry returns (deliver, ok); ok
// false means the source could not be set up, and the packet is dropped. The
// delivery closure runs outside the lock and must not block.
func (p *perSource) dispatch(now time.Time, key string, newEntry func(pkt []byte) (func([]byte), bool), pkt []byte) {
	p.mu.Lock()
	e, ok := p.sessions[key]
	if !ok {
		deliver, valid := newEntry(pkt)
		if !valid {
			p.mu.Unlock()
			return
		}
		e = &srcEntry{deliver: deliver}
		p.sessions[key] = e
	}
	e.lastSeen = now
	deliver := e.deliver
	p.mu.Unlock()
	deliver(pkt)
}

// remove drops a source entry, freeing its delivery channels. A handshake that
// fails calls this so a dead session's buffers are released at once rather than
// lingering until the reaper's TTL.
func (p *perSource) remove(key string) {
	p.mu.Lock()
	delete(p.sessions, key)
	p.mu.Unlock()
}

// reap drops sources with no packet for ttl until ctx ends.
func (p *perSource) reap(ctx context.Context) {
	t := time.NewTicker(p.ttl / 2)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			p.mu.Lock()
			for key, e := range p.sessions {
				if now.Sub(e.lastSeen) > p.ttl {
					delete(p.sessions, key)
				}
			}
			p.mu.Unlock()
		}
	}
}

func (p *perSource) len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.sessions)
}

// srtpSession is the per-source state serveSRTP demuxes packets into: a
// pending or completed DTLS-SRTP connection from one client address.
type srtpSession struct {
	dtlsCh chan []byte
	rtpCh  chan []byte
}

// serveSRTP is the server-side twin of obfs's demux, but shared across every
// source address on one UDP socket instead of owning a private one: the
// first packet from a new source spins up a DTLS server handshake with
// use_srtp on a session-scoped side conn, and once that completes the
// resulting net.Conn (built by NewSRTPServerConn) is delivered to out.
// Later packets from the same source are dispatched to that session's
// dtlsCh or rtpCh by the same first-byte rule obfs uses on the client side.
func serveSRTP(ctx context.Context, raw net.PacketConn, opts []dtls.ServerOption, out chan<- net.Conn) {
	ps := newPerSource(srcSessionTTL)
	go ps.reap(ctx)
	buf := make([]byte, 2048)
	for {
		n, addr, err := raw.ReadFrom(buf)
		if err != nil {
			return
		}
		if n == 0 {
			continue
		}
		pkt := make([]byte, n)
		copy(pkt, buf[:n])

		src := addr
		key := addr.String()
		ps.dispatch(time.Now(), key, func(first []byte) (func([]byte), bool) {
			// Only a DTLS ClientHello starts a session. A packet whose first
			// byte is not in the DTLS content-type range (a scan, a stray
			// datagram, a spoofed probe) must not spin up a handshake goroutine
			// and its channels.
			if len(first) == 0 || !isDTLSByte(first[0]) {
				return nil, false
			}
			sess := &srtpSession{dtlsCh: make(chan []byte, 64), rtpCh: make(chan []byte, 2048)}
			go acceptSRTPSession(ctx, raw, src, opts, sess, out, ps, key)
			return func(p []byte) {
				switch {
				case isDTLSByte(p[0]):
					trySend(sess.dtlsCh, p)
				case isRTPByte(p[0]):
					trySend(sess.rtpCh, p)
				}
			}, true
		}, pkt)
	}
}

func acceptSRTPSession(ctx context.Context, raw net.PacketConn, src net.Addr, opts []dtls.ServerOption, sess *srtpSession, out chan<- net.Conn, ps *perSource, key string) {
	side := &sessionConn{ctx: ctx, raw: raw, src: src, dtlsCh: sess.dtlsCh, dl: deadline.New()}
	dc, err := dtls.ServerWithOptions(side, src, opts...)
	if err != nil {
		ps.remove(key)
		return
	}
	hctx, cancel := context.WithTimeout(ctx, dtlsHandshakeTimeout)
	defer cancel()
	if err := dc.HandshakeContext(hctx); err != nil {
		_ = dc.Close()
		ps.remove(key)
		return
	}
	c, err := NewSRTPServerConn(raw, src, dc, sess.rtpCh)
	if err != nil {
		_ = dc.Close()
		ps.remove(key)
		return
	}
	select {
	case out <- c:
	case <-ctx.Done():
	}
}

// sessionConn is the server-side counterpart of obfs's unexported demuxSide:
// it feeds pion/dtls this session's demuxed dtlsCh and writes straight back
// to the shared socket at src. It is deliberately not shared with obfs since
// NewSRTPServerConn already covers the post-handshake data path.
//
// The deadline uses github.com/pion/transport/v4/deadline rather than a
// snapshot-a-channel-then-select timer: see obfs.demuxSide's doc comment for
// why a naive version is racy against a concurrent SetReadDeadline and would
// leave pion/dtls unable to cancel a blocked handshake read.
type sessionConn struct {
	ctx    context.Context
	raw    net.PacketConn
	src    net.Addr
	dtlsCh chan []byte
	dl     *deadline.Deadline
}

func (s *sessionConn) ReadFrom(b []byte) (int, net.Addr, error) {
	select {
	case pkt := <-s.dtlsCh:
		return copy(b, pkt), s.src, nil
	case <-s.ctx.Done():
		return 0, nil, net.ErrClosed
	case <-s.dl.Done():
		return 0, nil, os.ErrDeadlineExceeded
	}
}

func (s *sessionConn) WriteTo(b []byte, _ net.Addr) (int, error) { return s.raw.WriteTo(b, s.src) }
func (s *sessionConn) Close() error                              { return nil }
func (s *sessionConn) LocalAddr() net.Addr                       { return s.raw.LocalAddr() }
func (s *sessionConn) SetDeadline(t time.Time) error             { return s.SetReadDeadline(t) }
func (s *sessionConn) SetWriteDeadline(time.Time) error          { return nil }

func (s *sessionConn) SetReadDeadline(t time.Time) error {
	s.dl.Set(t)
	return nil
}
