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
//
// l1's Index (1) is made to collide with a live index on the replacement
// slot, so a naive id-based lookup in Release/Failed would silently corrupt
// (or saturate) the new slot instead of being a no-op on the orphaned old
// one; a delete/mark that never touches anything live would pass by
// accident if the two leases used different indices.
func TestReleaseAfterSlotReplaced(t *testing.T) {
	var n atomic.Int32
	f := func(_ context.Context, link string) (provider.Credential, error) {
		n.Add(1)
		return provider.Credential{Username: link, Relays: []string{"r"}, Link: link}, nil
	}
	p := New(f, opts()) // ConnsPerSlot: 2
	l0, _ := p.Acquire(context.Background(), 0) // old slot 0, index 0
	l1, _ := p.Acquire(context.Background(), 1) // old slot 0, index 1
	if l0.Slot != 0 || l1.Slot != 0 {
		t.Fatalf("setup: want both leases on slot 0, got %d %d", l0.Slot, l1.Slot)
	}

	var authErr error = &stun.TurnError{ErrorCodeAttr: stun.ErrorCodeAttribute{Code: 401, Reason: []byte("Unauthorized")}}
	p.Failed(l0, fmt.Errorf("relay: allocate: %w", authErr))

	l2, err := p.Acquire(context.Background(), 0) // refetches into slot 0 in place: new slot 0, index 0
	if err != nil {
		t.Fatal(err)
	}
	if l2.Slot != 0 {
		t.Fatalf("want refetch in place on slot 0, got slot %d", l2.Slot)
	}
	if n.Load() != 2 {
		t.Fatalf("fetches %d, want refetch", n.Load())
	}

	// Fill the new slot's second index BEFORE releasing the stale lease, so
	// l1's stale Index (1) collides with l3's live one on the new slot.
	l3, err := p.Acquire(context.Background(), 1) // new slot 0, index 1
	if err != nil {
		t.Fatal(err)
	}
	if l3.Slot != 0 || n.Load() != 2 {
		t.Fatalf("setup: want l3 on new slot 0 with no extra fetch, got slot %d fetches %d", l3.Slot, n.Load())
	}

	// l1 was leased against the OLD slot object at id 0, with the same
	// Index (1) l3 now legitimately holds on the NEW slot. Releasing it
	// must act on the orphaned old slot, never free l3's live index.
	p.Release(l1)
	if got := p.Stats().Active; got != 2 {
		t.Fatalf("active %d, want 2 (stale release must not free l3's index)", got)
	}

	// Slot 0 is genuinely full (l2, l3): a further worker must not be
	// served from it without a fetch.
	l4, err := p.Acquire(context.Background(), 4)
	if err != nil {
		t.Fatal(err)
	}
	if l4.Slot == 0 || n.Load() != 3 {
		t.Fatalf("over-lease: l4 on slot %d, fetches %d, want a fresh slot with an extra fetch", l4.Slot, n.Load())
	}

	// A stale Failed(486) against the old slot object must not saturate
	// the new slot: free up the new slot's other index, mark the stale
	// lease's (defunct) old slot saturated, then confirm the new slot is
	// still usable for worker 0's group with no extra fetch.
	var quotaErr error = &stun.TurnError{ErrorCodeAttr: stun.ErrorCodeAttribute{Code: 486, Reason: []byte("Allocation Quota Reached")}}
	p.Release(l3)
	p.Failed(l1, fmt.Errorf("relay: allocate: %w", quotaErr))
	l5, err := p.Acquire(context.Background(), 0)
	if err != nil || l5.Slot != 0 || n.Load() != 3 {
		t.Fatalf("stale Failed(486) leaked onto new slot: l5=%+v err=%v fetches=%d", l5, err, n.Load())
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
//
// ConnsPerSlot is 3, not 2, so that after the first captcha-triggered borrow
// (worker 3, via the post-fetchInto-error retry) slot 0 still has exactly
// one free index left. That lets a second borrow (worker 6) be pinned to the
// *upfront* "ownSlot != nil || captchaActive" branch specifically: since it
// never calls fetchInto at all, the fetch counter must not move, which only
// happens through that branch (the retry-after-error path always burns a
// fetch call first).
func TestCaptchaBorrowsSpareCapacity(t *testing.T) {
	var n atomic.Int32
	f := func(_ context.Context, link string) (provider.Credential, error) {
		if n.Add(1) == 1 {
			return provider.Credential{Username: link, Relays: []string{"r"}, Link: link}, nil
		}
		return provider.Credential{}, &provider.CaptchaRequiredError{Sid: "1"}
	}
	o := opts()
	o.ConnsPerSlot = 3
	p := New(f, o)

	l0, err := p.Acquire(context.Background(), 0) // own slot 0: normal fetch, 1 of 3 used
	if err != nil {
		t.Fatal(err)
	}

	l2, err := p.Acquire(context.Background(), 3) // own slot 1: fetch hits captcha, must borrow slot 0 via the retry
	if err != nil {
		t.Fatalf("worker 3 should borrow slot 0's spare capacity, got err %v", err)
	}
	if l2.Slot != l0.Slot {
		t.Fatalf("expected worker 3 to borrow slot %d, got %d", l0.Slot, l2.Slot)
	}
	if n.Load() != 2 {
		t.Fatalf("fetches %d, want exactly 2 (initial fetch plus the captcha attempt)", n.Load())
	}

	// The captcha deadline is now active and slot 0 has exactly one free
	// index (2 of 3 used). A worker whose own slot doesn't exist yet must
	// borrow that last index through the upfront captchaActive branch,
	// without ever calling the fetcher again.
	l4, err := p.Acquire(context.Background(), 6) // own slot 2: no slot, captcha active -> must borrow slot 0 directly
	if err != nil {
		t.Fatalf("worker 6 should borrow slot 0's last free index under the active captcha cooldown, got err %v", err)
	}
	if l4.Slot != l0.Slot {
		t.Fatalf("expected worker 6 to borrow slot %d, got %d", l0.Slot, l4.Slot)
	}
	if n.Load() != 2 {
		t.Fatalf("fetches %d, want no fetcher call for the captchaActive borrow", n.Load())
	}

	// slot 0 is now full (3/3) and the captcha cooldown is still active: a
	// further slot-less worker with nothing to borrow must fail.
	_, err = p.Acquire(context.Background(), 9) // own slot 3
	if !provider.IsCaptcha(err) {
		t.Fatalf("want captcha error once slot 0 is full, got %v", err)
	}
}
