// Package obfstest provides in-process counterparts of the VPS server for
// each obfuscation mode. They mirror what cacggghp/anton48 servers accept.
package obfstest

import (
	"context"
	"crypto/tls"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/pion/dtls/v3"
	"github.com/pion/dtls/v3/pkg/crypto/selfsign"
	"github.com/pion/transport/v4/deadline"

	"github.com/romanrublev/turnrelay/obfs"
)

type Server struct {
	addr  *net.UDPAddr
	conns chan net.Conn
	close func()
}

func (s *Server) Addr() *net.UDPAddr { return s.addr }
func (s *Server) Close()             { s.close() }

func (s *Server) Accept(ctx context.Context) (net.Conn, error) {
	select {
	case c := <-s.conns:
		return c, nil
	case <-ctx.Done():
		return nil, ctx.Err()
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

// ListenDTLS is the legacy cacggghp server: a DTLS listener on 127.0.0.1.
func ListenDTLS(t *testing.T) *Server {
	t.Helper()
	cert, err := selfsign.GenerateSelfSigned()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := dtls.ListenWithOptions("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}, serverOptions(cert)...)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{addr: ln.Addr().(*net.UDPAddr), conns: make(chan net.Conn, 64)}
	ctx, cancel := context.WithCancel(context.Background())
	s.close = func() { cancel(); _ = ln.Close() }
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
				s.conns <- dc
			}()
		}
	}()
	t.Cleanup(s.Close)
	return s
}

// ListenSRTP mirrors anton48's -srtp server: one UDP socket, sessions keyed
// by source address, DTLS with use_srtp, then RTP/SRTP framed datagrams.
func ListenSRTP(t *testing.T) *Server {
	t.Helper()
	cert, err := selfsign.GenerateSelfSigned()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{addr: raw.LocalAddr().(*net.UDPAddr), conns: make(chan net.Conn, 64)}
	ctx, cancel := context.WithCancel(context.Background())
	s.close = func() { cancel(); _ = raw.Close() }
	opts := append(serverOptions(cert), dtls.WithSRTPProtectionProfiles(dtls.SRTP_AES128_CM_HMAC_SHA1_80))
	go serveSRTP(ctx, raw, opts, s.conns)
	t.Cleanup(s.Close)
	return s
}

func isSRTPDTLSByte(b byte) bool { return b >= 20 && b <= 63 }
func isSRTPRTPByte(b byte) bool  { return b >= 128 && b <= 191 }

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
// resulting net.Conn (built by obfs.NewSRTPServerConn) is delivered to out.
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
		case isSRTPDTLSByte(pkt[0]):
			ch = sess.dtlsCh
		case isSRTPRTPByte(pkt[0]):
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
	c, err := obfs.NewSRTPServerConn(raw, src, dc, sess.rtpCh)
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
// obfs.NewSRTPServerConn already covers the post-handshake data path.
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
