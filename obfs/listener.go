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
	pionudp "github.com/pion/transport/v4/udp"
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
	cert, err := selfsign.GenerateSelfSigned()
	if err != nil {
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
				if err := dc.HandshakeContext(ctx); err != nil {
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

// udpConnPacketConn adapts a connection-oriented net.Conn (one per source
// address, as returned by pion/transport's udp.Listen) into a net.PacketConn:
// NewWrapPacketConn and dtls.ServerWithOptions both want ReadFrom/WriteTo,
// not Read/Write, and the peer address never changes for a given conn.
type udpConnPacketConn struct {
	net.Conn
}

func (c *udpConnPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	n, err := c.Conn.Read(b)
	return n, c.Conn.RemoteAddr(), err
}

func (c *udpConnPacketConn) WriteTo(b []byte, _ net.Addr) (int, error) {
	return c.Conn.Write(b)
}

func listenWrap(address string, cert tls.Certificate, key []byte, video bool) (*Listener, error) {
	laddr, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		return nil, err
	}
	pl, err := pionudp.Listen("udp", laddr)
	if err != nil {
		return nil, err
	}
	l := &Listener{addr: pl.Addr(), conns: make(chan net.Conn, 64)}
	ctx, cancel := context.WithCancel(context.Background())
	l.close = func() { cancel(); _ = pl.Close() }
	go func() {
		for {
			c, err := pl.Accept()
			if err != nil {
				return
			}
			go func() {
				raddr := c.RemoteAddr()
				codec, err := NewWrapCodec(key, video)
				if err != nil {
					return
				}
				pc := NewWrapPacketConn(&udpConnPacketConn{c}, codec)
				dc, err := dtls.ServerWithOptions(pc, raddr, serverOptions(cert)...)
				if err != nil {
					return
				}
				if err := dc.HandshakeContext(ctx); err != nil {
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
	var mu sync.Mutex
	sessions := make(map[string]*srtpSession)
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

		key := addr.String()
		mu.Lock()
		sess, ok := sessions[key]
		if !ok {
			sess = &srtpSession{dtlsCh: make(chan []byte, 64), rtpCh: make(chan []byte, 2048)}
			sessions[key] = sess
			go acceptSRTPSession(ctx, raw, addr, opts, sess, out)
		}
		mu.Unlock()

		var ch chan []byte
		switch {
		case isDTLSByte(pkt[0]):
			ch = sess.dtlsCh
		case isRTPByte(pkt[0]):
			ch = sess.rtpCh
		default:
			continue
		}
		select {
		case ch <- pkt:
		case <-ctx.Done():
			return
		}
	}
}

func acceptSRTPSession(ctx context.Context, raw net.PacketConn, src net.Addr, opts []dtls.ServerOption, sess *srtpSession, out chan<- net.Conn) {
	side := &sessionConn{ctx: ctx, raw: raw, src: src, dtlsCh: sess.dtlsCh, dl: deadline.New()}
	dc, err := dtls.ServerWithOptions(side, src, opts...)
	if err != nil {
		return
	}
	if err := dc.HandshakeContext(ctx); err != nil {
		_ = dc.Close()
		return
	}
	c, err := NewSRTPServerConn(raw, src, dc, sess.rtpCh)
	if err != nil {
		_ = dc.Close()
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
