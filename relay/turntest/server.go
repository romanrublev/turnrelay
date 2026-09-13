// Package turntest runs pion/turn as a stand-in for VK's relay.
package turntest

import (
	"net"
	"sync/atomic"
	"testing"

	"github.com/pion/logging"
	"github.com/pion/turn/v5"
)

type Server struct {
	addr     string
	Username string
	Password string
	Realm    string
	allocs   atomic.Int32
	quota    atomic.Int32
	srv      *turn.Server
}

func (s *Server) Addr() string     { return s.addr }
func (s *Server) Allocations() int { return int(s.allocs.Load()) }
func (s *Server) SetQuota(n int)   { s.quota.Store(int32(n)) }

func Start(t *testing.T) *Server {
	t.Helper()
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{addr: pc.LocalAddr().String(), Username: "user", Password: "pass", Realm: "turnrelay.test"}
	s.quota.Store(1 << 30)
	s.start(t, pc)
	return s
}

// start builds the pion server bound to pc and registers cleanup. It is
// factored out so Restart can rebuild the server on the same address.
func (s *Server) start(t *testing.T, pc net.PacketConn) {
	t.Helper()
	key := turn.GenerateAuthKey(s.Username, s.Realm, s.Password)
	srv, err := turn.NewServer(turn.ServerConfig{
		Realm:         s.Realm,
		LoggerFactory: logging.NewDefaultLoggerFactory(),
		AuthHandler: func(ra *turn.RequestAttributes) (string, []byte, bool) {
			if ra.Username == s.Username && ra.Realm == s.Realm {
				return ra.Username, key, true
			}
			return "", nil, false
		},
		QuotaHandler: func(string, string, net.Addr) bool {
			return s.allocs.Load() < s.quota.Load()
		},
		EventHandler: turn.EventHandler{
			OnAllocationCreated: func(_, _ net.Addr, _, _, _ string, _ net.Addr, _ int) { s.allocs.Add(1) },
			OnAllocationDeleted: func(_, _ net.Addr, _, _, _ string) { s.allocs.Add(-1) },
		},
		PacketConnConfigs: []turn.PacketConnConfig{{
			PacketConn: pc,
			RelayAddressGenerator: &turn.RelayAddressGeneratorStatic{
				RelayAddress: net.ParseIP("127.0.0.1"),
				Address:      "127.0.0.1",
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	s.srv = srv
	t.Cleanup(func() { _ = s.srv.Close() })
}

// Restart closes the running server and its socket, then re-listens on the
// same address and rebuilds the server with the same credentials. Callers
// use this to exercise reconnect behavior against a relay that reboots
// without changing address.
func (s *Server) Restart(t *testing.T) {
	t.Helper()
	if err := s.srv.Close(); err != nil {
		t.Fatal(err)
	}
	pc, err := net.ListenPacket("udp4", s.addr)
	if err != nil {
		t.Fatal(err)
	}
	s.allocs.Store(0)
	s.start(t, pc)
}
