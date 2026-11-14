package mux_test

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/romanrublev/turnrelay/mux"
	"github.com/romanrublev/turnrelay/passwords"
)

func TestMatchPassword(t *testing.T) {
	var sess [16]byte
	copy(sess[:], "session-M-0000000")
	tag := mux.AuthTag("second", sess)
	got, ok := mux.MatchPassword([]string{"first", "second", "third"}, sess, tag)
	if !ok || got != "second" {
		t.Fatalf("got %q ok=%v", got, ok)
	}
	if _, ok := mux.MatchPassword([]string{"first", "third"}, sess, tag); ok {
		t.Fatal("tag matched a password that did not produce it")
	}
	if _, ok := mux.MatchPassword(nil, sess, tag); ok {
		t.Fatal("empty list accepted")
	}
}

func TestServerAcceptsAnyListedPassword(t *testing.T) {
	s := mux.NewServer(mux.ServerOptions{Passwords: passwords.Static{"alpha", "beta"}})
	defer s.Close()
	var a, b, c [16]byte
	copy(a[:], "session-A-0000000")
	copy(b[:], "session-B-0000000")
	copy(c[:], "session-C-0000000")
	mustAlloc(t, s, a, "alpha")
	mustAlloc(t, s, b, "beta")

	client, server := net.Pipe()
	defer client.Close()
	errc := make(chan error, 1)
	go func() { errc <- s.Handle(context.Background(), server) }()
	_, _ = client.Write(mux.EncodeHello(c))
	_, _ = client.Write(mux.EncodeAuth(mux.AuthTag("gamma", c)))
	select {
	case err := <-errc:
		if !errors.Is(err, mux.ErrAuth) {
			t.Fatalf("err = %v, want ErrAuth", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("unlisted password was not rejected")
	}
}

func TestServerDropsAllocationWhenPasswordRemoved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "passwords")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pf, err := passwords.OpenFile(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := mux.NewServer(mux.ServerOptions{Passwords: pf})
	defer s.Close()

	var sa, sb [16]byte
	copy(sa[:], "session-A-0000000")
	copy(sb[:], "session-B-0000000")
	alpha := mustAlloc(t, s, sa, "alpha")
	beta := mustAlloc(t, s, sb, "beta")

	// Remove "alpha" from the file and reload.
	if err := os.WriteFile(path, []byte("beta\ngamma\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if changed, err := pf.Reload(); !changed || err != nil {
		t.Fatalf("reload changed=%v err=%v", changed, err)
	}

	if !closesSoon(alpha) {
		t.Fatal("allocation of the removed password stayed open")
	}
	if closesSoon(beta) {
		t.Fatal("allocation of a still-listed password was closed")
	}

	// A newly added password works without a restart; the removed one no
	// longer authenticates.
	var sg [16]byte
	copy(sg[:], "session-G-0000000")
	mustAlloc(t, s, sg, "gamma")
}

// closesSoon reports whether the server closes the allocation within a few
// seconds, detected by writes failing once the pipe is closed.
func closesSoon(a *allocConn) bool {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_ = a.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
		if _, err := a.Write([]byte("x")); err != nil && !errors.Is(err, os.ErrDeadlineExceeded) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}
