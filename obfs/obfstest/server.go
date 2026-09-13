// Package obfstest provides in-process counterparts of the VPS server for
// each obfuscation mode. They mirror what cacggghp/anton48 servers accept.
package obfstest

import (
	"context"
	"crypto/tls"
	"net"
	"testing"

	"github.com/pion/dtls/v3"
	"github.com/pion/dtls/v3/pkg/crypto/selfsign"
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
