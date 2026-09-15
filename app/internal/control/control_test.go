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

// startTestServerShort is like startTestServer but uses a short-prefixed
// temp dir instead of t.TempDir(): unix socket paths are capped at ~104
// bytes on macOS (sun_path), and t.TempDir() embeds the (long) test name
// in the path, which this test's longer name can overflow.
func startTestServerShort(t *testing.T, h Handler) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ctl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "s.sock")
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

// panicHandler panics on Up to simulate a handler bug (e.g. from
// engine.Start/box.New) reaching the control server.
type panicHandler struct{ fakeHandler }

func (p *panicHandler) Up(profile.Profile) error { panic("boom") }

// TestHandlerPanicDoesNotCrashServer proves the accept loop survives a
// panicking handler: the panicking request returns an error to its client
// instead of hanging or crashing the process, and a second, independent
// connection is still served afterward.
func TestHandlerPanicDoesNotCrashServer(t *testing.T) {
	h := &panicHandler{}
	path := startTestServerShort(t, h)

	p := profile.Defaults()
	p.Link, p.Server, p.WGPrivateKey, p.WGPeerPublicKey = "L", "S:1", "k", "pk"

	if err := (Client{Path: path}).Up(p); err == nil {
		t.Fatal("expected an error from the panicking handler, got nil")
	}

	// A fresh connection must still be served, proving the accept loop
	// (and the whole daemon process) survived the panic.
	st, err := (Client{Path: path}).Status()
	if err != nil {
		t.Fatalf("status after panic: %v", err)
	}
	if st.Workers != 7 {
		t.Fatalf("status wrong after panic: %+v", st)
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
