package credpool

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/stun/v4"
	"github.com/romanrublev/turnrelay/provider"
)

func opts() Options {
	return Options{Links: []string{"L1", "L2"}, ConnsPerSlot: 2, TTL: 10 * time.Minute, Margin: time.Minute,
		CooldownMin: 0, CooldownMax: 0, CaptchaCooldown: time.Minute,
		Now: time.Now, Sleep: func(context.Context, time.Duration) error { return nil }, Logf: func(string, ...any) {}}
}

func TestSlotsShareCredentialsAndRotateLinks(t *testing.T) {
	var n atomic.Int32
	f := func(_ context.Context, link string) (provider.Credential, error) {
		n.Add(1)
		return provider.Credential{Username: link + "-u", Relays: []string{"r1", "r2"}, Link: link}, nil
	}
	p := New(f, opts())
	l0, _ := p.Acquire(context.Background(), 0)
	l1, _ := p.Acquire(context.Background(), 1)
	l2, _ := p.Acquire(context.Background(), 2)
	if n.Load() != 2 {
		t.Fatalf("fetches %d", n.Load())
	}
	if l0.Slot != 0 || l1.Slot != 0 || l2.Slot != 1 {
		t.Fatalf("slots %d %d %d", l0.Slot, l1.Slot, l2.Slot)
	}
	if l0.Cred.Link != "L1" || l2.Cred.Link != "L2" {
		t.Fatalf("links %s %s", l0.Cred.Link, l2.Cred.Link)
	}
	if l0.Index == l1.Index {
		t.Fatal("indices within slot must differ")
	}
}

func TestQuotaMovesWorkerToAnotherSlot(t *testing.T) {
	f := func(_ context.Context, link string) (provider.Credential, error) {
		return provider.Credential{Username: link, Relays: []string{"r"}, Link: link}, nil
	}
	p := New(f, opts())
	l0, _ := p.Acquire(context.Background(), 0)
	var quotaErr error = &stun.TurnError{ErrorCodeAttr: stun.ErrorCodeAttribute{Code: 486, Reason: []byte("Allocation Quota Reached")}}
	p.Failed(l0, fmt.Errorf("relay: allocate: %w", quotaErr))
	l0b, err := p.Acquire(context.Background(), 0)
	if err != nil || l0b.Slot == 0 {
		t.Fatalf("worker 0 stayed on saturated slot: %+v %v", l0b, err)
	}
}

func TestAuthErrorRefetches(t *testing.T) {
	var n atomic.Int32
	f := func(_ context.Context, link string) (provider.Credential, error) {
		n.Add(1)
		return provider.Credential{Username: link, Relays: []string{"r"}, Link: link}, nil
	}
	p := New(f, opts())
	l0, _ := p.Acquire(context.Background(), 0)
	var authErr error = &stun.TurnError{ErrorCodeAttr: stun.ErrorCodeAttribute{Code: 401, Reason: []byte("Unauthorized")}}
	p.Failed(l0, fmt.Errorf("relay: allocate: %w", authErr))
	if _, err := p.Acquire(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if n.Load() != 2 {
		t.Fatalf("fetches %d, want re-fetch after auth error", n.Load())
	}
}

func TestExpiredCredentialRefetches(t *testing.T) {
	now := time.Now()
	o := opts()
	o.Now = func() time.Time { return now }
	var n atomic.Int32
	f := func(_ context.Context, link string) (provider.Credential, error) {
		n.Add(1)
		return provider.Credential{Username: link, Relays: []string{"r"}, Link: link, FetchedAt: now}, nil
	}
	p := New(f, o)
	l, _ := p.Acquire(context.Background(), 0)
	p.Release(l)
	now = now.Add(9*time.Minute + 30*time.Second)
	if _, err := p.Acquire(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if n.Load() != 2 {
		t.Fatalf("fetches %d", n.Load())
	}
}

func TestCaptchaCoolsDown(t *testing.T) {
	calls := 0
	f := func(context.Context, string) (provider.Credential, error) {
		calls++
		return provider.Credential{}, &provider.CaptchaRequiredError{Sid: "1"}
	}
	p := New(f, opts())
	_, err := p.Acquire(context.Background(), 0)
	if !provider.IsCaptcha(err) {
		t.Fatalf("got %v", err)
	}
	_, err = p.Acquire(context.Background(), 0)
	if !provider.IsCaptcha(err) || calls != 1 {
		t.Fatalf("second acquire should fail fast from cooldown: calls=%d err=%v", calls, err)
	}
	if p.Stats().CaptchaUntil.IsZero() {
		t.Fatal("stats lack captcha deadline")
	}
}

func TestCooldownBetweenFetches(t *testing.T) {
	o := opts()
	o.CooldownMin, o.CooldownMax = time.Second, time.Second
	var slept time.Duration
	o.Sleep = func(_ context.Context, d time.Duration) error { slept += d; return nil }
	f := func(_ context.Context, link string) (provider.Credential, error) {
		return provider.Credential{Username: link, Relays: []string{"r"}, Link: link}, nil
	}
	p := New(f, o)
	_, _ = p.Acquire(context.Background(), 0)
	_, _ = p.Acquire(context.Background(), 2) // second slot -> second fetch
	if slept < 900*time.Millisecond {
		t.Fatalf("no cooldown between fetches: %v", slept)
	}
}
