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
	// FailedRelay marks the relay this lease rode as degraded (lossy/slow) so
	// the pool avoids it for a cooldown, then releases the lease. The
	// supervisor uses it to retire a relay whose health probes show loss or
	// latency the credential-level Failed never sees.
	FailedRelay(*credpool.Lease)
}

// DefaultUplinkQueue is the uplink queue depth when Options.UplinkQueue is
// zero: how many datagrams Write accepts while no worker is draining them
// before it parks.
const DefaultUplinkQueue = 256

type Options struct {
	Workers       int
	Peer          *net.UDPAddr
	Wrapper       obfs.Wrapper
	Creds         Acquirer
	TURNUDP       bool
	TURNOverride  string
	ProbeInterval time.Duration
	// HealthProbeInterval is how often each worker sends a health probe to
	// measure its relay's RTT and loss (the server echoes probes). It is
	// faster than ProbeInterval, which also carries the hello and drives
	// zombie detection. Default 1s.
	HealthProbeInterval time.Duration
	// SuperviseInterval is how often the supervisor ranks worker health and
	// may evict the single worst relay. Default 5s. Zero disables eviction
	// only if negative; use a large value to effectively disable.
	SuperviseInterval time.Duration
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
	// Password, when set, makes every worker send an auth frame after its
	// hello so an exit server can verify the session. Empty sends nothing.
	Password string
}

func (o *Options) defaults() {
	if o.Workers <= 0 {
		o.Workers = 30
	}
	if o.ProbeInterval == 0 {
		o.ProbeInterval = 30 * time.Second
	}
	if o.HealthProbeInterval == 0 {
		o.HealthProbeInterval = time.Second
	}
	if o.SuperviseInterval == 0 {
		o.SuperviseInterval = 5 * time.Second
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
	authTag    []byte // nil when Options.Password is empty
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
	health     []*workerHealth // per-worker, indexed by worker id; set in Start
	evict      []chan struct{} // per-worker eviction signal; set in Start
	evictions  atomic.Int32    // cumulative relays retired by the supervisor
}

func New(o Options) *Pool {
	o.defaults()
	p := &Pool{o: o, up: make(chan []byte, o.UplinkQueue), down: make(chan []byte, o.DownlinkQueue),
		handshakes: make(chan struct{}, o.HandshakeSlots), closed: make(chan struct{})}
	_, _ = rand.Read(p.session[:])
	if o.Password != "" {
		p.authTag = AuthTag(o.Password, p.session)
	}
	p.readyCond = sync.NewCond(&p.readyMu)
	return p
}

func (p *Pool) Session() [16]byte { return p.session }

func (p *Pool) Start(ctx context.Context) {
	ctx, p.cancel = context.WithCancel(ctx)
	p.health = make([]*workerHealth, p.o.Workers)
	p.evict = make([]chan struct{}, p.o.Workers)
	for i := range p.health {
		p.health[i] = newWorkerHealth()
		p.evict[i] = make(chan struct{}, 1)
	}
	p.wg.Add(1)
	go func() { defer p.wg.Done(); p.supervise(ctx) }()
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

// supervise periodically ranks worker health and signals the single worst
// relay for eviction. It evicts at most one worker per interval, so a bad
// batch is retired gradually and the pipe is never gutted chasing marginal
// gains; pickEvict also refuses to drop below one active worker. An evicted
// worker's run loop re-acquires from the credential pool, which cools the bad
// relay and hands it a different one.
func (p *Pool) supervise(ctx context.Context) {
	t := time.NewTicker(p.o.SuperviseInterval)
	defer t.Stop()
	var cfg evictConfig
	cfg.defaults()
	// Emit a machine-parseable health gauge every gaugeEvery passes (~30s at
	// the default 5s interval) so the engine can surface loss/RTT/evictions.
	const gaugeEvery = 6
	pass := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.closed:
			return
		case <-t.C:
			snaps := make([]healthSnapshot, len(p.health))
			for i, h := range p.health {
				snaps[i] = h.snapshot(i)
			}
			if id := pickEvict(snaps, cfg); id >= 0 {
				select {
				case p.evict[id] <- struct{}{}:
					p.evictions.Add(1)
					p.o.Logf("mux: evict worker %d (loss %.1f%%, rtt %v) - retiring relay",
						id, snaps[id].loss*100, snaps[id].rtt)
				default: // a prior signal is still pending; leave it
				}
			}
			if pass%gaugeEvery == 0 {
				p.logHealthGauge(snaps)
			}
			pass++
		}
	}
}

// logHealthGauge emits one stable line summarising fleet health. Values are
// integers (loss in basis points, RTT in ms) so a log scanner parses them
// without locale or float-format surprises.
func (p *Pool) logHealthGauge(snaps []healthSnapshot) {
	active := 0
	var maxLoss float64
	var rttSum time.Duration
	rttN := 0
	for _, s := range snaps {
		if !s.active {
			continue
		}
		active++
		if s.loss > maxLoss {
			maxLoss = s.loss
		}
		if s.rtt > 0 {
			rttSum += s.rtt
			rttN++
		}
	}
	meanRTTMs := 0
	if rttN > 0 {
		meanRTTMs = int((rttSum / time.Duration(rttN)) / time.Millisecond)
	}
	p.o.Logf("mux: health active=%d evictions=%d max_loss_bp=%d mean_rtt_ms=%d",
		active, int(p.evictions.Load()), int(maxLoss*10000), meanRTTMs)
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
