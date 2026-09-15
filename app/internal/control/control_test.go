package control

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/romanrublev/turnrelay/app/internal/profile"
	"github.com/romanrublev/turnrelay/app/internal/proto"
)

type fakeHandler struct{ up bool }

func (f *fakeHandler) Up(profile.Profile) error { f.up = true; return nil }
func (f *fakeHandler) Down() error              { f.up = false; return nil }
func (f *fakeHandler) Status() proto.Status     { return proto.Status{Running: f.up, Workers: 7} }

func startTestServer(t *testing.T, h Handler) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	go Serve(ln, uint32(os.Getuid()), h)
	t.Cleanup(func() { ln.Close() })
	return path
}

func TestClientServerRoundTrip(t *testing.T) {
	h := &fakeHandler{}
	c := Client{Path: startTestServer(t, h)}
	p := profile.Defaults()
	p.Link, p.Server, p.WGPrivateKey, p.WGPeerPublicKey = "L", "S:1", "k", "pk"
	if err := c.Up(p); err != nil {
		t.Fatalf("up: %v", err)
	}
	st, err := c.Status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !st.Running || st.Workers != 7 {
		t.Fatalf("status wrong: %+v", st)
	}
}

func TestForeignUIDRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	go Serve(ln, uint32(os.Getuid())+99999, &fakeHandler{}) // owner nobody can match
	t.Cleanup(func() { ln.Close() })
	if err := (Client{Path: path}).Down(); err == nil {
		t.Fatal("expected rejection for mismatched uid")
	}
}
