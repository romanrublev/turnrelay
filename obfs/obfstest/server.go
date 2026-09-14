// Package obfstest provides in-process counterparts of the VPS server for
// each obfuscation mode, for tests. They are obfs.Listen bound to 127.0.0.1
// with t.Cleanup wired up.
package obfstest

import (
	"context"
	"net"
	"testing"

	"github.com/romanrublev/turnrelay/obfs"
)

type Server struct{ l *obfs.Listener }

func (s *Server) Addr() *net.UDPAddr                           { return s.l.Addr().(*net.UDPAddr) }
func (s *Server) Close()                                       { _ = s.l.Close() }
func (s *Server) Accept(ctx context.Context) (net.Conn, error) { return s.l.Accept(ctx) }

func listen(t *testing.T, mode obfs.Mode, o obfs.ListenOptions) *Server {
	t.Helper()
	l, err := obfs.Listen(mode, "127.0.0.1:0", o)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{l: l}
	t.Cleanup(s.Close)
	return s
}

// ListenDTLS is the legacy cacggghp server: a DTLS listener on 127.0.0.1.
func ListenDTLS(t *testing.T) *Server { return listen(t, obfs.ModeDTLS, obfs.ListenOptions{}) }

// ListenWrap mirrors the WDTT server: UDP socket, WRAP-v1 envelope, then DTLS.
func ListenWrap(t *testing.T, key []byte) *Server {
	return listen(t, obfs.ModeWrap, obfs.ListenOptions{WrapKey: key})
}

// ListenSRTP mirrors anton48's -srtp server: one UDP socket, sessions keyed
// by source address, DTLS with use_srtp, then RTP/SRTP framed datagrams.
func ListenSRTP(t *testing.T) *Server { return listen(t, obfs.ModeSRTP, obfs.ListenOptions{}) }
