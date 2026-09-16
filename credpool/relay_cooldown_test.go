package credpool

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/romanrublev/turnrelay/provider"
)

// clk is a manually advanced clock so cooldown expiry is testable without
// sleeping.
type clk struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clk) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clk) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func cooldownOpts(c *clk) Options {
	o := opts()
	o.Now = c.now
	o.RelayCooldown = 5 * time.Minute
	return o
}

// A credential with two relays: FailedRelay on the first must steer the next
// lease within the same slot to the second, unpoisoned relay.
func TestFailedRelayMovesWithinSlot(t *testing.T) {
	c := &clk{t: time.Unix(1_000_000, 0)}
	f := func(_ context.Context, link string) (provider.Credential, error) {
		return provider.Credential{Username: link, Relays: []string{"r1", "r2"}, Link: link}, nil
	}
	p := New(f, cooldownOpts(c))
	l0, _ := p.Acquire(context.Background(), 0)
	if got := l0.Cred.Relay(l0.Index); got != "r1" {
		t.Fatalf("first lease relay = %q, want r1", got)
	}
	p.FailedRelay(l0)

	l1, err := p.Acquire(context.Background(), 1) // worker 1 -> own slot 0
	if err != nil {
		t.Fatal(err)
	}
	if l1.Slot != 0 {
		t.Fatalf("expected to stay on slot 0, got slot %d", l1.Slot)
	}
	if got := l1.Cred.Relay(l1.Index); got != "r2" {
		t.Fatalf("lease after cooling r1 rode %q, want r2", got)
	}
}

// A single-relay credential is effectively poisoned by FailedRelay: with no
// live relay left on its slot, the worker must acquire from a different link.
func TestFailedRelayPoisonsSingleRelayCredential(t *testing.T) {
	c := &clk{t: time.Unix(1_000_000, 0)}
	relayByLink := map[string]string{"L1": "rA", "L2": "rB"}
	f := func(_ context.Context, link string) (provider.Credential, error) {
		return provider.Credential{Username: link, Relays: []string{relayByLink[link]}, Link: link}, nil
	}
	p := New(f, cooldownOpts(c))
	l0, _ := p.Acquire(context.Background(), 0)
	if l0.Cred.Link != "L1" || l0.Cred.Relay(l0.Index) != "rA" {
		t.Fatalf("first lease = link %s relay %s", l0.Cred.Link, l0.Cred.Relay(l0.Index))
	}
	p.FailedRelay(l0)

	l0b, err := p.Acquire(context.Background(), 0) // same worker, own slot 0
	if err != nil {
		t.Fatal(err)
	}
	if got := l0b.Cred.Relay(l0b.Index); got == "rA" {
		t.Fatalf("worker landed back on cooled relay rA")
	}
	if l0b.Cred.Link != "L2" {
		t.Fatalf("expected move to link L2, got %s", l0b.Cred.Link)
	}
}

// Once RelayCooldown elapses the relay is usable again.
func TestRelayCooldownExpires(t *testing.T) {
	c := &clk{t: time.Unix(1_000_000, 0)}
	f := func(_ context.Context, link string) (provider.Credential, error) {
		return provider.Credential{Username: link, Relays: []string{"only"}, Link: link}, nil
	}
	o := cooldownOpts(c)
	o.Links = []string{"L1"} // single link so there is nowhere else to go
	p := New(f, o)
	l0, _ := p.Acquire(context.Background(), 0)
	p.FailedRelay(l0)

	// While cooling, the relay must be reported as such.
	p.mu.Lock()
	cooling := p.relayCooling("only")
	p.mu.Unlock()
	if !cooling {
		t.Fatal("relay should be cooling right after FailedRelay")
	}

	c.add(o.RelayCooldown + time.Second)
	l0b, err := p.Acquire(context.Background(), 0)
	if err != nil {
		t.Fatalf("acquire after cooldown expiry: %v", err)
	}
	if l0b.Cred.Relay(l0b.Index) != "only" {
		t.Fatalf("relay not reusable after cooldown, got %q", l0b.Cred.Relay(l0b.Index))
	}
}

// FailedRelay on a nil or slot-less lease must be a no-op, never a panic.
func TestFailedRelayNilSafe(t *testing.T) {
	c := &clk{t: time.Unix(1_000_000, 0)}
	f := func(_ context.Context, link string) (provider.Credential, error) {
		return provider.Credential{Username: link, Relays: []string{"r"}, Link: link}, nil
	}
	p := New(f, cooldownOpts(c))
	p.FailedRelay(nil)
	p.FailedRelay(&Lease{})
}

// TestAcquireBacksOffWhenAllRelaysCooling: once the only reachable relay is
// cooled, Acquire must return ErrRelayCooling after a single fetch, not loop
// fetching a fresh VK credential every few seconds (the ban-footprint storm).
func TestAcquireBacksOffWhenAllRelaysCooling(t *testing.T) {
	c := &clk{t: time.Unix(1_000_000, 0)}
	var fetches atomic.Int32
	f := func(_ context.Context, link string) (provider.Credential, error) {
		fetches.Add(1)
		return provider.Credential{Username: link, Relays: []string{"only"}, Link: link}, nil
	}
	o := cooldownOpts(c)
	o.Links = []string{"L1"} // one link, one relay: nowhere to escape a cooldown
	p := New(f, o)

	l0, err := p.Acquire(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	p.FailedRelay(l0)
	fetchesBefore := fetches.Load()

	_, err = p.Acquire(context.Background(), 0)
	if !errors.Is(err, ErrRelayCooling) {
		t.Fatalf("want ErrRelayCooling while the only relay cools, got %v", err)
	}
	if extra := fetches.Load() - fetchesBefore; extra > 1 {
		t.Fatalf("Acquire fetched %d fresh credentials while cooling; want at most 1 (no storm)", extra)
	}
}
