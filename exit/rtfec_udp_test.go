package exit

import (
	"math/rand/v2"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/pion/transport/v4/deadline"
)

// lossyConn is an in-memory PacketConn: WriteTo delivers to the peer's read
// queue, dropping a fraction to model a lossy relay path. It is one direction
// of a pair (see newLossyPair); reads come from its own queue.
type lossyConn struct {
	self net.Addr
	peer *lossyConn
	in   chan []byte
	loss float64
	dl   *deadline.Deadline

	mu   sync.Mutex
	rng  *rand.Rand
	done chan struct{}
	once sync.Once
}

type fakeAddr string

func (fakeAddr) Network() string  { return "mem" }
func (a fakeAddr) String() string { return string(a) }

func newLossyPair(loss float64, seed uint64) (*lossyConn, *lossyConn) {
	a := &lossyConn{self: fakeAddr("A"), in: make(chan []byte, 4096), loss: loss, dl: deadline.New(), rng: rand.New(rand.NewPCG(seed, 1)), done: make(chan struct{})}
	b := &lossyConn{self: fakeAddr("B"), in: make(chan []byte, 4096), loss: loss, dl: deadline.New(), rng: rand.New(rand.NewPCG(seed, 2)), done: make(chan struct{})}
	a.peer, b.peer = b, a
	return a, b
}

func (c *lossyConn) WriteTo(b []byte, _ net.Addr) (int, error) {
	c.mu.Lock()
	drop := c.rng.Float64() < c.loss
	c.mu.Unlock()
	if !drop {
		cp := make([]byte, len(b))
		copy(cp, b)
		select {
		case c.peer.in <- cp:
		default: // queue full: treat as loss
		}
	}
	return len(b), nil
}

func (c *lossyConn) ReadFrom(b []byte) (int, net.Addr, error) {
	select {
	case p := <-c.in:
		return copy(b, p), c.peer.self, nil
	case <-c.done:
		return 0, nil, net.ErrClosed
	case <-c.dl.Done():
		return 0, nil, os.ErrDeadlineExceeded
	}
}

func (c *lossyConn) Close() error                       { c.once.Do(func() { close(c.done) }); return nil }
func (c *lossyConn) LocalAddr() net.Addr                { return c.self }
func (c *lossyConn) SetDeadline(t time.Time) error      { c.dl.Set(t); return nil }
func (c *lossyConn) SetReadDeadline(t time.Time) error  { c.dl.Set(t); return nil }
func (c *lossyConn) SetWriteDeadline(t time.Time) error { return nil }

// TestRealtimeFECRecoversUDPLoss sends many datagrams through a realtime fecConn
// over a 5%-lossy pipe and checks that FEC recovers almost all of them (well
// above the ~95% that would survive raw), which is the exit-mode UDP fix.
func TestRealtimeFECRecoversUDPLoss(t *testing.T) {
	const (
		loss = 0.05
		n    = 400
	)
	cin, sin := newLossyPair(loss, 7)
	lossFn := func() float64 { return loss }
	client := newRealtimeFECConn(cin, lossFn)
	server := newRealtimeFECConn(sin, lossFn)
	t.Cleanup(func() { client.Close(); server.Close() })

	dst := fakeAddr("B")
	go func() {
		for i := 0; i < n; i++ {
			payload := []byte{byte(i), byte(i >> 8), 'x', 'y', 'z'}
			_, _ = client.WriteTo(payload, dst)
			time.Sleep(time.Millisecond) // a realistic real-time cadence
		}
	}()

	seen := map[uint16]bool{}
	deadlineAt := time.Now().Add(6 * time.Second)
	buf := make([]byte, 2048)
	for len(seen) < n && time.Now().Before(deadlineAt) {
		_ = server.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		m, _, err := server.ReadFrom(buf)
		if err != nil {
			continue
		}
		if m >= 2 {
			seen[uint16(buf[0])|uint16(buf[1])<<8] = true
		}
	}

	got := len(seen)
	t.Logf("delivered %d/%d (%.1f%%) at %.0f%% path loss", got, n, float64(got)/n*100, loss*100)
	if got < n*98/100 {
		t.Fatalf("FEC recovered only %d/%d; raw survival would be ~%d, want near-complete", got, n, int(n*(1-loss)))
	}
}

// TestRealtimeFECCleanPathDeliversAll: with no loss, every datagram arrives and
// nothing is duplicated.
func TestRealtimeFECCleanPathDeliversAll(t *testing.T) {
	const n = 120
	cin, sin := newLossyPair(0, 3)
	client := newRealtimeFECConn(cin, func() float64 { return 0 })
	server := newRealtimeFECConn(sin, func() float64 { return 0 })
	t.Cleanup(func() { client.Close(); server.Close() })

	go func() {
		for i := 0; i < n; i++ {
			_, _ = client.WriteTo([]byte{byte(i), byte(i >> 8)}, fakeAddr("B"))
			time.Sleep(time.Millisecond)
		}
	}()

	count := map[uint16]int{}
	deadlineAt := time.Now().Add(5 * time.Second)
	buf := make([]byte, 2048)
	for len(count) < n && time.Now().Before(deadlineAt) {
		_ = server.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		m, _, err := server.ReadFrom(buf)
		if err != nil {
			continue
		}
		if m >= 2 {
			count[uint16(buf[0])|uint16(buf[1])<<8]++
		}
	}
	if len(count) != n {
		t.Fatalf("clean path delivered %d/%d distinct datagrams", len(count), n)
	}
}
