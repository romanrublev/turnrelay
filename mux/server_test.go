package mux_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/romanrublev/turnrelay/mux"
)

// allocConn is the client end of one fake allocation: hello+auth already
// sent, inbound frames delivered on recv (probe echoes are filtered out).
type allocConn struct {
	net.Conn
	recv chan []byte
}

// newAlloc is safe to call from any goroutine: it reports failures as an
// error instead of t.Fatal. The caller closes the returned conn.
func newAlloc(ctx context.Context, s *mux.Server, session [16]byte, password string) (*allocConn, error) {
	client, server := net.Pipe()
	go func() { _ = s.Handle(ctx, server) }()
	a := &allocConn{Conn: client, recv: make(chan []byte, 64)}
	go func() {
		buf := make([]byte, 65535)
		for {
			n, err := client.Read(buf)
			if err != nil {
				return
			}
			if mux.IsControl(buf[:n]) {
				continue
			}
			pkt := make([]byte, n)
			copy(pkt, buf[:n])
			a.recv <- pkt
		}
	}()
	if _, err := client.Write(mux.EncodeHello(session)); err != nil {
		client.Close()
		return nil, err
	}
	if password != "" {
		if _, err := client.Write(mux.EncodeAuth(mux.AuthTag(password, session))); err != nil {
			client.Close()
			return nil, err
		}
	}
	return a, nil
}

func mustAlloc(t *testing.T, s *mux.Server, session [16]byte, password string) *allocConn {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	a, err := newAlloc(ctx, s, session, password)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); a.Close() })
	return a
}

func TestServerGroupsBySessionAndStripes(t *testing.T) {
	s := mux.NewServer(mux.ServerOptions{Password: "pw"})
	defer s.Close()
	var sess [16]byte
	copy(sess[:], "session-A-0000000")
	a1 := mustAlloc(t, s, sess, "pw")
	a2 := mustAlloc(t, s, sess, "pw")

	pc := s.PacketConn()
	if _, err := a1.Write([]byte("from-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := a2.Write([]byte("from-2")); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	buf := make([]byte, 64)
	_ = pc.SetReadDeadline(time.Now().Add(5 * time.Second))
	for i := 0; i < 2; i++ {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			t.Fatal(err)
		}
		if from.(mux.SessionAddr) != mux.SessionAddr(sess) {
			t.Fatalf("from %v", from)
		}
		got[string(buf[:n])] = true
	}
	if !got["from-1"] || !got["from-2"] {
		t.Fatalf("uplink %v", got)
	}

	// Downlink is striped over the session's allocations: every datagram
	// arrives on exactly one of them.
	for i := 0; i < 10; i++ {
		if _, err := pc.WriteTo([]byte{byte(i)}, mux.SessionAddr(sess)); err != nil {
			t.Fatal(err)
		}
	}
	seen := 0
	deadline := time.After(5 * time.Second)
	for seen < 10 {
		select {
		case <-a1.recv:
			seen++
		case <-a2.recv:
			seen++
		case <-deadline:
			t.Fatalf("downlink: got %d of 10", seen)
		}
	}
}

func TestServerRejectsBadAuth(t *testing.T) {
	s := mux.NewServer(mux.ServerOptions{Password: "pw"})
	defer s.Close()
	var sess [16]byte
	copy(sess[:], "session-B-0000000")
	client, server := net.Pipe()
	defer client.Close()
	errCh := make(chan error, 1)
	go func() { errCh <- s.Handle(context.Background(), server) }()
	go func() {
		_, _ = client.Write(mux.EncodeHello(sess))
		_, _ = client.Write(mux.EncodeAuth(mux.AuthTag("wrong", sess)))
	}()
	select {
	case err := <-errCh:
		if !errors.Is(err, mux.ErrAuth) {
			t.Fatalf("err %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Handle did not return")
	}
	// Nothing from an unauthenticated allocation reaches the pipe.
	pc := s.PacketConn()
	_ = pc.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if _, _, err := pc.ReadFrom(make([]byte, 16)); err == nil {
		t.Fatal("unauthenticated payload delivered")
	}
}

func TestServerEchoesProbes(t *testing.T) {
	s := mux.NewServer(mux.ServerOptions{Password: "pw"})
	defer s.Close()
	var sess [16]byte
	copy(sess[:], "session-C-0000000")
	client, server := net.Pipe()
	defer client.Close()
	go func() { _ = s.Handle(context.Background(), server) }()
	_, _ = client.Write(mux.EncodeHello(sess))
	_, _ = client.Write(mux.EncodeAuth(mux.AuthTag("pw", sess)))
	probe := mux.EncodeProbe(42)
	go func() { _, _ = client.Write(probe) }()
	buf := make([]byte, 64)
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := client.Read(buf)
	if err != nil || !bytes.Equal(buf[:n], probe) {
		t.Fatalf("echo %x err %v", buf[:n], err)
	}
}

func TestServerConcurrentJoinLeave(t *testing.T) {
	s := mux.NewServer(mux.ServerOptions{Password: "pw"})
	defer s.Close()
	var sess [16]byte
	copy(sess[:], "session-D-0000000")
	pc := s.PacketConn()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, err := newAlloc(ctx, s, sess, "pw")
			if err != nil {
				t.Error(err)
				return
			}
			_, _ = a.Write([]byte("x"))
			time.Sleep(10 * time.Millisecond)
			_ = a.Close()
		}()
	}
	wg.Wait()
	_ = pc.SetReadDeadline(time.Now().Add(2 * time.Second))
	n := 0
	for {
		if _, _, err := pc.ReadFrom(make([]byte, 16)); err != nil {
			break
		}
		n++
	}
	if n == 0 {
		t.Fatal("no uplink from concurrent allocations")
	}
}

// TestServerHandleReturnsOnContextCancel guards against Handle blocking
// forever when the uplink or downlink goroutine is parked inside a blocking
// conn.Read/conn.Write at the moment ctx is cancelled: without forcing the
// conn's deadline on cancel, that goroutine never reaches errCh and Handle
// (and its deferred conn.Close) never returns.
//
// To actually reproduce that deadlock, both goroutines must be parked in
// blocking I/O, not merely idling in a select: the client never writes
// another frame (so the uplink goroutine sits inside conn.Read), and the
// client never reads a queued downlink datagram (so, since net.Pipe is
// unbuffered, the downlink goroutine sits inside conn.Write).
func TestServerHandleReturnsOnContextCancel(t *testing.T) {
	s := mux.NewServer(mux.ServerOptions{Password: "pw"})
	defer s.Close()
	var sess [16]byte
	copy(sess[:], "session-E-0000000")
	client, server := net.Pipe()
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- s.Handle(ctx, server) }()
	if _, err := client.Write(mux.EncodeHello(sess)); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Write(mux.EncodeAuth(mux.AuthTag("pw", sess))); err != nil {
		t.Fatal(err)
	}

	// Queue downlink datagrams that nobody on the client side ever reads.
	// join() runs in the Handle goroutine right after the auth write above
	// unblocks, so WriteTo can briefly race it; retry until the session is
	// visible. net.Pipe is unbuffered, so a single unread datagram is
	// enough to park the downlink goroutine inside conn.Write; queue two to
	// be safe.
	pc := s.PacketConn()
	writeDeadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := pc.WriteTo([]byte("d1"), mux.SessionAddr(sess)); err == nil {
			break
		}
		if time.Now().After(writeDeadline) {
			t.Fatal("session never joined: WriteTo kept failing")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := pc.WriteTo([]byte("d2"), mux.SessionAddr(sess)); err != nil {
		t.Fatal(err)
	}

	// Give the downlink goroutine time to pull the first datagram off the
	// queue and block inside conn.Write; the uplink goroutine is already
	// blocked inside conn.Read since the client sent nothing more.
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("Handle did not return within 2s of context cancel")
	}
}
