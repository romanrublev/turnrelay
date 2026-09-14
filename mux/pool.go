package mux

import (
	"context"
	"crypto/rand"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/romanrublev/turnrelay/credpool"
	"github.com/romanrublev/turnrelay/obfs"
)

// maxDatagram is the largest UDP payload a single Write may carry. A larger
// datagram could never leave a relay intact; rejecting it at the door keeps
// it out of the uplink queue, where a write that always fails would be a
// poison pill that kills every worker that steals it.
const maxDatagram = 65535

// ErrDatagramSize is returned by Write for an empty or oversize datagram.
var ErrDatagramSize = errors.New("mux: datagram size out of range")

type Acquirer interface {
	Acquire(ctx context.Context, worker int) (*credpool.Lease, error)
	Release(*credpool.Lease)
	Failed(*credpool.Lease, error)
}

// DefaultUplinkQueue is the uplink queue depth when Options.UplinkQueue is
// zero: how many datagrams Write accepts while no worker is draining them
// before it parks.
const DefaultUplinkQueue = 256

type Options struct {
	Workers           int
	Peer              *net.UDPAddr
	Wrapper           obfs.Wrapper
	Creds             Acquirer
	TURNUDP           bool
	TURNOverride      string
	ProbeInterval     time.Duration
	ZombieAfter       time.Duration
	StartPacing       time.Duration
	KeepaliveInterval time.Duration
	HandshakeSlots    int
	UplinkQueue       int
	DownlinkQueue     int
	BackoffMin        time.Duration
	BackoffMax        time.Duration
	Logf              func(string, ...any)
	// DialContext is handed to relay.Allocate for every worker's socket to
	// the relay; nil means the net package. See relay.Options.DialContext.
	DialContext func(ctx context.Context, network, address string) (net.Conn, error)
}

func (o *Options) defaults() {
	if o.Workers <= 0 {
		o.Workers = 30
	}
	if o.ProbeInterval == 0 {
		o.ProbeInterval = 30 * time.Second
	}
	if o.ZombieAfter == 0 {
		o.ZombieAfter = 120 * time.Second
	}
	if o.StartPacing == 0 {
		o.StartPacing = 100 * time.Millisecond
	}
	if o.KeepaliveInterval == 0 {
		o.KeepaliveInterval = 10 * time.Second
	}
	if o.HandshakeSlots == 0 {
		o.HandshakeSlots = 3
	}
	if o.UplinkQueue == 0 {
		o.UplinkQueue = DefaultUplinkQueue
	}
	if o.DownlinkQueue == 0 {
		o.DownlinkQueue = 2048
	}
	if o.BackoffMin == 0 {
		o.BackoffMin = 2 * time.Second
	}
	if o.BackoffMax == 0 {
		o.BackoffMax = 60 * time.Second
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
}

type Stats struct {
	Active     int
	Connecting int
	Restarts   int
	LastError  string
}

type Pool struct {
	o          Options
	session    [16]byte
	up         chan []byte
	down       chan []byte
	handshakes chan struct{}
	active     atomic.Int32
	connecting atomic.Int32
	restarts   atomic.Int32
	errMu      sync.Mutex
	lastErr    error
	readyMu    sync.Mutex
	readyCond  *sync.Cond
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	closeOnce  sync.Once
	closed     chan struct{}
}

func New(o Options) *Pool {
	o.defaults()
	p := &Pool{o: o, up: make(chan []byte, o.UplinkQueue), down: make(chan []byte, o.DownlinkQueue),
		handshakes: make(chan struct{}, o.HandshakeSlots), closed: make(chan struct{})}
	_, _ = rand.Read(p.session[:])
	p.readyCond = sync.NewCond(&p.readyMu)
	return p
}

func (p *Pool) Session() [16]byte { return p.session }

func (p *Pool) Start(ctx context.Context) {
	ctx, p.cancel = context.WithCancel(ctx)
	for i := 0; i < p.o.Workers; i++ {
		w := &worker{id: i, pool: p}
		p.wg.Add(1)
		go func(delay time.Duration) {
			defer p.wg.Done()
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			w.run(ctx)
		}(time.Duration(i) * p.o.StartPacing)
	}
}

func (p *Pool) Close() {
	p.closeOnce.Do(func() {
		if p.cancel != nil {
			p.cancel()
		}
		close(p.closed)
		p.broadcastReady()
		p.wg.Wait()
	})
}

// Write queues one datagram for the next free worker. It blocks while the
// uplink queue is full and gives up with ctx.Err() when ctx ends, or with
// net.ErrClosed once the pool is closed. Callers tie ctx to the lifetime of
// the conn doing the write so closing that conn releases a parked Write.
//
// The pool's closed channel is checked before the queue is offered the
// datagram: a plain three-way select would pick at random between a ready
// queue slot and the closed signal, so a Write after Close could still
// "succeed" into a queue nobody drains.
func (p *Pool) Write(ctx context.Context, b []byte) error {
	if len(b) == 0 || len(b) > maxDatagram {
		return ErrDatagramSize
	}
	select {
	case <-p.closed:
		return net.ErrClosed
	default:
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	pkt := make([]byte, len(b))
	copy(pkt, b)
	select {
	case p.up <- pkt:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-p.closed:
		return net.ErrClosed
	}
}

// requeue puts a datagram a dying worker could not carry back on the
// uplink queue so another worker sends it. It blocks while the queue is
// full: the queue is the only place a datagram may wait, never the floor.
// Only pool shutdown (ctx or Close) lets it give up.
func (p *Pool) requeue(ctx context.Context, pkt []byte) {
	select {
	case p.up <- pkt:
	case <-ctx.Done():
	case <-p.closed:
	}
}

func (p *Pool) Read(ctx context.Context) ([]byte, error) {
	select {
	case pkt := <-p.down:
		return pkt, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.closed:
		return nil, net.ErrClosed
	}
}

// broadcastReady wakes WaitReady. It takes readyMu so a broadcast cannot
// land between WaitReady's condition check and its Wait and be lost.
func (p *Pool) broadcastReady() {
	p.readyMu.Lock()
	p.readyCond.Broadcast()
	p.readyMu.Unlock()
}

// WaitReady blocks until at least n workers are active.
func (p *Pool) WaitReady(ctx context.Context, n int) error {
	stop := context.AfterFunc(ctx, p.broadcastReady)
	defer stop()
	p.readyMu.Lock()
	defer p.readyMu.Unlock()
	for int(p.active.Load()) < n {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		select {
		case <-p.closed:
			return net.ErrClosed
		default:
		}
		p.readyCond.Wait()
	}
	return nil
}

func (p *Pool) setErr(err error) {
	p.errMu.Lock()
	p.lastErr = err
	p.errMu.Unlock()
}

func (p *Pool) Stats() Stats {
	p.errMu.Lock()
	defer p.errMu.Unlock()
	st := Stats{Active: int(p.active.Load()), Connecting: int(p.connecting.Load()), Restarts: int(p.restarts.Load())}
	if p.lastErr != nil {
		st.LastError = p.lastErr.Error()
	}
	return st
}
