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
	eng    *engine.Engine
	mu     sync.Mutex
	egress string
	poll   context.CancelFunc
}

func New() *Daemon { return &Daemon{eng: engine.New()} }

func (d *Daemon) Up(p profile.Profile) error {
	cfg, err := config.Build(p)
	if err != nil {
		return err
	}
	if err := d.eng.Start(cfg); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.mu.Lock()
	d.poll = cancel
	d.mu.Unlock()
	go d.egressLoop(ctx)
	return nil
}

func (d *Daemon) Down() error {
	d.mu.Lock()
	if d.poll != nil {
		d.poll()
		d.poll = nil
	}
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

func (d *Daemon) egressLoop(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	set := func() {
		ip := d.egressOnce(ctx)
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
