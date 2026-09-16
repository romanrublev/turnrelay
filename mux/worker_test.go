package mux

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/transport/v4/deadline"

	"github.com/romanrublev/turnrelay/credpool"
	"github.com/romanrublev/turnrelay/provider"
	"github.com/romanrublev/turnrelay/relay/turntest"
)

func TestBackoffResetsAfterHealthySession(t *testing.T) {
	const minB, maxB = 50 * time.Millisecond, 200 * time.Millisecond
	var b time.Duration
	want := []time.Duration{minB, 2 * minB, maxB, maxB}
	for i, w := range want {
		if b = nextBackoff(b, false, minB, maxB); b != w {
			t.Fatalf("step %d: got %v want %v", i, b, w)
		}
	}
	// A healthy session starts the ladder over, and it climbs again from there.
	if b = nextBackoff(b, true, minB, maxB); b != minB {
		t.Fatalf("after healthy: got %v want %v", b, minB)
	}
	if b = nextBackoff(b, false, minB, maxB); b != 2*minB {
		t.Fatalf("after reset: got %v want %v", b, 2*minB)
	}
}

// staticCreds is an Acquirer that hands every worker the same credential.
type staticCreds struct{ cred provider.Credential }

func (s staticCreds) Acquire(_ context.Context, worker int) (*credpool.Lease, error) {
	return &credpool.Lease{Cred: s.cred, Index: worker}, nil
}
func (staticCreds) Release(*credpool.Lease)       {}
func (staticCreds) Failed(*credpool.Lease, error) {}
func (staticCreds) FailedRelay(*credpool.Lease)   {}

// echoConn stands in for an obfs conn talking to an echo VPS: hellos are
// consumed, probes and payload echoed back to Read. With failPayload set,
// every payload write fails, the way a conn over a dead allocation would.
type echoConn struct {
	failPayload bool
	failed      atomic.Int32
	in          chan []byte
	done        chan struct{}
	closeOnce   sync.Once
	dl          *deadline.Deadline
}

func newEchoConn(failPayload bool) *echoConn {
	return &echoConn{failPayload: failPayload, in: make(chan []byte, 64), done: make(chan struct{}), dl: deadline.New()}
}

func (c *echoConn) Write(b []byte) (int, error) {
	select {
	case <-c.done:
		return 0, net.ErrClosed
	default:
	}
	if _, ok := ParseHello(b); ok {
		return len(b), nil
	}
	if !IsControl(b) && c.failPayload {
		c.failed.Add(1)
		return 0, errors.New("echoConn: write failed")
	}
	pkt := make([]byte, len(b))
	copy(pkt, b)
	select {
	case c.in <- pkt:
	case <-c.done:
		return 0, net.ErrClosed
	}
	return len(b), nil
}

func (c *echoConn) Read(b []byte) (int, error) {
	select {
	case pkt := <-c.in:
		return copy(b, pkt), nil
	case <-c.done:
		return 0, net.ErrClosed
	case <-c.dl.Done():
		return 0, os.ErrDeadlineExceeded
	}
}

func (c *echoConn) Close() error {
	c.closeOnce.Do(func() { close(c.done) })
	return nil
}
func (c *echoConn) LocalAddr() net.Addr               { return &net.UDPAddr{} }
func (c *echoConn) RemoteAddr() net.Addr              { return &net.UDPAddr{} }
func (c *echoConn) SetDeadline(t time.Time) error     { return c.SetReadDeadline(t) }
func (c *echoConn) SetReadDeadline(t time.Time) error { c.dl.Set(t); return nil }
func (c *echoConn) SetWriteDeadline(time.Time) error  { return nil }

// echoWrapper skips the real handshake and returns echoConns; the first
// one it hands out fails every payload write.
type echoWrapper struct {
	mu    sync.Mutex
	conns []*echoConn
}

func (w *echoWrapper) Client(_ context.Context, underlay net.PacketConn, _ net.Addr) (net.Conn, error) {
	_ = underlay.Close()
	w.mu.Lock()
	defer w.mu.Unlock()
	c := newEchoConn(len(w.conns) == 0)
	w.conns = append(w.conns, c)
	return c, nil
}

func (w *echoWrapper) failedWrites() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, c := range w.conns {
		n += int(c.failed.Load())
	}
	return n
}

// TestUplinkPacketSurvivesWorkerDeath: a worker whose conn fails a payload
// write must hand that packet back to the queue (blocking while the queue
// is full, which UplinkQueue: 1 makes the common state) so the other
// worker carries it. Every packet written must come back.
func TestUplinkPacketSurvivesWorkerDeath(t *testing.T) {
	ts := turntest.Start(t)
	w := &echoWrapper{}
	p := New(Options{
		Workers: 2, Peer: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1}, Wrapper: w,
		Creds:   staticCreds{provider.Credential{Username: ts.Username, Password: ts.Password, Relays: []string{ts.Addr()}}},
		TURNUDP: true, UplinkQueue: 1,
		ProbeInterval: 200 * time.Millisecond, ZombieAfter: 2 * time.Second, StartPacing: 10 * time.Millisecond,
		BackoffMin: 50 * time.Millisecond, BackoffMax: 200 * time.Millisecond,
		Logf: t.Logf,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	t.Cleanup(p.Close)
	p.Start(ctx)
	if err := p.WaitReady(ctx, 2); err != nil {
		t.Fatal(err)
	}
	const n = 100
	go func() {
		for i := 0; i < n; i++ {
			if err := p.Write(ctx, []byte{1, byte(i)}); err != nil {
				return
			}
		}
	}()
	seen := make([]bool, n)
	for got := 0; got < n; got++ {
		b, err := p.Read(ctx)
		if err != nil {
			t.Fatalf("read after %d: %v (stats %+v)", got, err, p.Stats())
		}
		if len(b) != 2 || b[0] != 1 || seen[b[1]] {
			t.Fatalf("unexpected packet %x", b)
		}
		seen[b[1]] = true
	}
	if w.failedWrites() == 0 {
		t.Fatal("the failing conn never saw a payload write; put-back path not exercised")
	}
	// The failing worker's restart is asynchronous with the last Read; give
	// it a moment rather than asserting the instant the payload arrived.
	deadline := time.Now().Add(5 * time.Second)
	for p.Stats().Restarts < 1 {
		if time.Now().After(deadline) {
			t.Fatalf("expected the failing worker to restart: %+v", p.Stats())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// recordingCreds hands every worker the same credential and records how many
// times FailedRelay is called, for the eviction-wiring test.
type recordingCreds struct {
	cred provider.Credential
	mu   sync.Mutex
	n    int
}

func (r *recordingCreds) Acquire(_ context.Context, worker int) (*credpool.Lease, error) {
	return &credpool.Lease{Cred: r.cred, Index: worker}, nil
}
func (*recordingCreds) Release(*credpool.Lease)       {}
func (*recordingCreds) Failed(*credpool.Lease, error) {}
func (r *recordingCreds) FailedRelay(*credpool.Lease) {
	r.mu.Lock()
	r.n++
	r.mu.Unlock()
}
func (r *recordingCreds) failedRelays() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

// TestSupervisorEvictionRetiresRelay drives the eviction seam directly: a
// signal on a worker's evict channel must tear that worker down via
// FailedRelay (cooling its relay) and the pool must recover to full strength
// as the worker re-acquires. The supervisor's own ranking is unit-tested in
// pickEvict; here SuperviseInterval is set huge so only the manual signal fires.
func TestSupervisorEvictionRetiresRelay(t *testing.T) {
	ts := turntest.Start(t)
	w := &echoWrapper{}
	rc := &recordingCreds{cred: provider.Credential{Username: ts.Username, Password: ts.Password, Relays: []string{ts.Addr()}}}
	p := New(Options{
		Workers: 2, Peer: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1}, Wrapper: w,
		Creds: rc, TURNUDP: true,
		ProbeInterval: 200 * time.Millisecond, HealthProbeInterval: 50 * time.Millisecond,
		ZombieAfter: 2 * time.Second, StartPacing: 10 * time.Millisecond,
		BackoffMin: 20 * time.Millisecond, BackoffMax: 100 * time.Millisecond,
		SuperviseInterval: time.Hour, // no auto-eviction; the test drives evict[0]
		Logf:              t.Logf,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	t.Cleanup(p.Close)
	p.Start(ctx)
	if err := p.WaitReady(ctx, 2); err != nil {
		t.Fatal(err)
	}

	p.evict[0] <- struct{}{} // retire worker 0's relay

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if rc.failedRelays() >= 1 && int(p.active.Load()) >= 2 {
			return // relay cooled and the pool recovered to full strength
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("eviction not wired: failedRelays=%d active=%d", rc.failedRelays(), p.active.Load())
}
