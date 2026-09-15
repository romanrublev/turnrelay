package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/romanrublev/turnrelay/app/internal/profile"
)

func sampleProfile() profile.Profile {
	p := profile.Defaults()
	p.Link, p.Server, p.WGPrivateKey, p.WGPeerPublicKey = "https://vk.ru/call/join/X", "127.0.0.1:56004", "k", "pk"
	return p
}

func TestUpInvalidProfileErrors(t *testing.T) {
	d := New()
	if err := d.Up(profile.Profile{}); err == nil {
		t.Fatal("want error building config from empty profile")
	}
	if d.Status().Running {
		t.Fatal("should not be running after failed Up")
	}
}

// TestDownWhenNotRunningIsSafe covers the baseline in the fix-round-1
// review: Down on a fresh Daemon (no prior Up) must not panic or error,
// and must leave egress empty.
func TestDownWhenNotRunningIsSafe(t *testing.T) {
	d := New()
	if err := d.Down(); err != nil {
		t.Fatalf("Down on a fresh daemon should be a no-op, got err: %v", err)
	}
	if got := d.Status().Egress; got != "" {
		t.Fatalf("egress = %q, want empty", got)
	}
	if d.Status().Running {
		t.Fatal("should not be running")
	}
}

// TestDownIsIdempotent covers the baseline in the fix-round-1 review:
// calling Down repeatedly must be safe.
func TestDownIsIdempotent(t *testing.T) {
	d := New()
	for i := 0; i < 3; i++ {
		if err := d.Down(); err != nil {
			t.Fatalf("Down call %d: %v", i, err)
		}
	}
	if got := d.Status().Egress; got != "" {
		t.Fatalf("egress = %q, want empty", got)
	}
}

// TestUpWhenAlreadyActiveDoesNotStartSecondPoll is a white-box regression
// test for fix-round-1 finding 1 (goroutine leak on a second Up while
// already running): it simulates an already-active poll by setting the
// unexported fields directly (a real second Up needs a successfully
// started engine, which needs root and cannot run in this environment).
// Before the fix, Up unconditionally overwrote d.poll with a new
// CancelFunc without invoking the old one, leaking the first poll
// goroutine. After the fix, Up must detect the active poll and return
// early without touching config.Build/engine.Start or the existing
// poll/done fields.
func TestUpWhenAlreadyActiveDoesNotStartSecondPoll(t *testing.T) {
	d := New()
	oldCancelled := false
	oldDone := make(chan struct{})
	d.mu.Lock()
	d.poll = func() { oldCancelled = true }
	d.done = oldDone
	d.mu.Unlock()

	if err := d.Up(sampleProfile()); err != nil {
		t.Fatalf("Up while already active should be a no-op, got err: %v", err)
	}
	if oldCancelled {
		t.Fatal("Up must not cancel the existing poll when one is already active")
	}

	d.mu.Lock()
	samePoll := d.poll != nil
	sameDone := d.done == oldDone
	d.mu.Unlock()
	if !samePoll || !sameDone {
		t.Fatal("Up must leave the existing poll/done in place instead of replacing them")
	}
}

// TestDownJoinsPollBeforeClearingEgress is a white-box regression test
// for fix-round-1 finding 2 (stale-egress race after Down): it drives the
// real egressLoop goroutine through the fetchEgress seam (no real engine
// or network call needed) and verifies Down does not return, and does
// not clear egress, until the in-flight write from the poll goroutine has
// finished. Before the fix, Down cleared egress and returned without
// waiting for the goroutine, so a write already in flight could land
// after Down and leave a stale IP in Status.
func TestDownJoinsPollBeforeClearingEgress(t *testing.T) {
	d := New()

	fetchStarted := make(chan struct{})
	releaseFetch := make(chan struct{})
	d.fetchEgress = func(ctx context.Context) string {
		close(fetchStarted)
		<-releaseFetch
		return "203.0.113.9"
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	d.mu.Lock()
	d.poll = cancel
	d.done = done
	d.mu.Unlock()
	go d.egressLoop(ctx, done)

	<-fetchStarted // the poll goroutine is inside fetchEgress, about to write egress

	downReturned := make(chan struct{})
	go func() {
		if err := d.Down(); err != nil {
			t.Errorf("Down: %v", err)
		}
		close(downReturned)
	}()

	select {
	case <-downReturned:
		t.Fatal("Down returned before the in-flight poll write finished")
	case <-time.After(100 * time.Millisecond):
		// expected: Down is blocked waiting on the poll goroutine
	}

	close(releaseFetch) // let the in-flight fetchEgress return and write egress

	select {
	case <-downReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("Down did not return after the poll goroutine finished")
	}

	if got := d.Status().Egress; got != "" {
		t.Fatalf("egress = %q, want empty (Down must clear it after joining the poll)", got)
	}
}
