package credpool

import (
	"context"
	"fmt"
	"sync"
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

// TestReleaseAfterSlotReplaced covers the case where a lease's slot id has
// since been overwritten with a fresh credential: Release/Failed must act on
// the exact *slot the lease was issued against, never on whatever now
// occupies that numeric id, or they corrupt the new slot's active count.
func TestReleaseAfterSlotReplaced(t *testing.T) {
	var n atomic.Int32
	f := func(_ context.Context, link string) (provider.Credential, error) {
		n.Add(1)
		return provider.Credential{Username: link, Relays: []string{"r"}, Link: link}, nil
	}
	p := New(f, opts()) // ConnsPerSlot: 2
	l0, _ := p.Acquire(context.Background(), 0)
	l1, _ := p.Acquire(context.Background(), 1)
	if l0.Slot != 0 || l1.Slot != 0 {
		t.Fatalf("setup: want both leases on slot 0, got %d %d", l0.Slot, l1.Slot)
	}

	var authErr error = &stun.TurnError{ErrorCodeAttr: stun.ErrorCodeAttribute{Code: 401, Reason: []byte("Unauthorized")}}
	p.Failed(l0, fmt.Errorf("relay: allocate: %w", authErr))

	l2, err := p.Acquire(context.Background(), 0) // refetches into slot 0 in place (invalidated, not saturated)
	if err != nil {
		t.Fatal(err)
	}
	if l2.Slot != 0 {
		t.Fatalf("want refetch in place on slot 0, got slot %d", l2.Slot)
	}
	if n.Load() != 2 {
		t.Fatalf("fetches %d, want refetch", n.Load())
	}

	// l1 was leased against the OLD slot object at id 0. Releasing it must
	// not touch the NEW slot's active count.
	p.Release(l1)
	if got := p.Stats().Active; got != 1 {
		t.Fatalf("active %d, want 1 (only l2 remains leased on the new slot)", got)
	}

	// The new slot must still admit exactly ConnsPerSlot leases: l2 holds
	// one, so exactly one more must succeed without triggering a fetch.
	l3, err := p.Acquire(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if l3.Slot != l2.Slot {
		t.Fatalf("want l3 on the same (new) slot %d, got %d", l2.Slot, l3.Slot)
	}
	if n.Load() != 2 {
		t.Fatalf("fetches %d, want no extra fetch for the second lease on the new slot", n.Load())
	}
}

// TestConcurrentQuotaRefetch covers two workers sharing a slot that both hit
// 486 and race to refetch it: exactly one of them must actually fetch, and
// both must end up leasing the same freshly-fetched slot.
func TestConcurrentQuotaRefetch(t *testing.T) {
	var n atomic.Int32
	f := func(_ context.Context, link string) (provider.Credential, error) {
		n.Add(1)
		return provider.Credential{Username: link, Relays: []string{"r"}, Link: link}, nil
	}
	p := New(f, opts()) // ConnsPerSlot: 2
	l0, _ := p.Acquire(context.Background(), 0)
	l1, _ := p.Acquire(context.Background(), 1)
	if n.Load() != 1 {
		t.Fatalf("setup fetches %d, want 1 (both leases share slot 0)", n.Load())
	}

	var quotaErr error = &stun.TurnError{ErrorCodeAttr: stun.ErrorCodeAttribute{Code: 486, Reason: []byte("Allocation Quota Reached")}}
	p.Failed(l0, fmt.Errorf("relay: allocate: %w", quotaErr))
	p.Failed(l1, fmt.Errorf("relay: allocate: %w", quotaErr))

	leases := make([]*Lease, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, worker := range []int{0, 1} {
		wg.Add(1)
		go func(i, worker int) {
			defer wg.Done()
			leases[i], errs[i] = p.Acquire(context.Background(), worker)
		}(i, worker)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
	}
	if n.Load() != 2 {
		t.Fatalf("fetches %d, want exactly one extra fetch", n.Load())
	}
	if leases[0].Slot != leases[1].Slot {
		t.Fatalf("leases landed on different slots: %d vs %d", leases[0].Slot, leases[1].Slot)
	}
}

// TestCaptchaBorrowsSpareCapacity covers a captcha response on a fetch for a
// brand new slot while another slot still has spare capacity: the worker
// must borrow that spare capacity instead of failing with the captcha error.
// Only once every slot is full does the captcha error actually surface.
func TestCaptchaBorrowsSpareCapacity(t *testing.T) {
	var n atomic.Int32
	f := func(_ context.Context, link string) (provider.Credential, error) {
		if n.Add(1) == 1 {
			return provider.Credential{Username: link, Relays: []string{"r"}, Link: link}, nil
		}
		return provider.Credential{}, &provider.CaptchaRequiredError{Sid: "1"}
	}
	p := New(f, opts()) // ConnsPerSlot: 2

	l0, err := p.Acquire(context.Background(), 0) // own slot 0: normal fetch, 1 of 2 used
	if err != nil {
		t.Fatal(err)
	}

	l2, err := p.Acquire(context.Background(), 2) // own slot 1: fetch hits captcha, must borrow slot 0
	if err != nil {
		t.Fatalf("worker 2 should borrow slot 0's spare capacity, got err %v", err)
	}
	if l2.Slot != l0.Slot {
		t.Fatalf("expected worker 2 to borrow slot %d, got %d", l0.Slot, l2.Slot)
	}

	// slot 0 is now full (2/2) and the captcha cooldown is active: a third
	// worker with no slot of its own and nothing to borrow must fail.
	_, err = p.Acquire(context.Background(), 4)
	if !provider.IsCaptcha(err) {
		t.Fatalf("want captcha error for third worker, got %v", err)
	}
}
