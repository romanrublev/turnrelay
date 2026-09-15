// Package daemon wires the control handler to the in-process engine.
package daemon

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/romanrublev/turnrelay/app/internal/config"
	"github.com/romanrublev/turnrelay/app/internal/engine"
	"github.com/romanrublev/turnrelay/app/internal/profile"
	"github.com/romanrublev/turnrelay/app/internal/proto"
)

type Daemon struct {
	eng *engine.Engine

	// opMu serializes Up and Down as whole operations, so a concurrent
	// Up/Up or Up/Down pair cannot interleave and leak or race the poll
	// goroutine below.
	opMu sync.Mutex

	// mu guards the fields below, which are also read by Status and
	// written by the egress-poll goroutine.
	mu     sync.Mutex
	egress string
	poll   context.CancelFunc
	done   chan struct{}

	// fetchEgress is a seam over egressOnce so tests can drive the poll
	// loop without a real network call or a running engine.
	fetchEgress func(context.Context) string
}

func New() *Daemon {
	d := &Daemon{eng: engine.New()}
	d.fetchEgress = d.egressOnce
	return d
}

func (d *Daemon) Up(p profile.Profile) error {
	d.opMu.Lock()
	defer d.opMu.Unlock()

	d.mu.Lock()
	active := d.poll != nil
	d.mu.Unlock()
	if active {
		// Already up: engine.Start is idempotent, but launching a second
		// poll goroutine here would discard the existing CancelFunc and
		// leak the first poll. Treat Up as idempotent too.
		return nil
	}

	cfg, err := config.Build(p)
	if err != nil {
		return err
	}
	if err := d.eng.Start(cfg); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	d.mu.Lock()
	d.poll = cancel
	d.done = done
	d.mu.Unlock()
	go d.egressLoop(ctx, done)
	return nil
}

func (d *Daemon) Down() error {
	d.opMu.Lock()
	defer d.opMu.Unlock()

	d.mu.Lock()
	cancel := d.poll
	done := d.done
	d.poll = nil
	d.done = nil
	d.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		// Wait for the poll goroutine to actually exit before clearing
		// egress below, otherwise a write already in flight can land
		// after we clear it and leave a stale IP in Status.
		<-done
	}

	d.mu.Lock()
	d.egress = ""
	d.mu.Unlock()

	d.eng.Stop()
	return nil
}

func (d *Daemon) Status() proto.Status {
	d.mu.Lock()
	egress := d.egress
	d.mu.Unlock()
	return proto.Status{
		Running:     d.eng.Running(),
		Workers:     d.eng.Workers(),
		Egress:      egress,
		HandshakeOK: d.eng.HandshakeOK(),
	}
}

func (d *Daemon) egressLoop(ctx context.Context, done chan struct{}) {
	defer close(done)
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	set := func() {
		ip := d.fetchEgress(ctx)
		d.mu.Lock()
		d.egress = ip
		d.mu.Unlock()
	}
	set()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			set()
		}
	}
}

func (d *Daemon) egressOnce(ctx context.Context) string {
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(c, "curl", "-sS", "-4", "--max-time", "8", "https://api.ipify.org").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
