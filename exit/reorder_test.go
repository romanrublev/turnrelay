package exit

import (
	"testing"
	"time"
)

func payloads(b [][]byte) string {
	s := ""
	for _, p := range b {
		s += string(p)
	}
	return s
}

// In-order arrivals pass straight through.
func TestReorderInOrder(t *testing.T) {
	r := newReorderBuffer(12 * time.Millisecond)
	now := time.Unix(0, 0)
	got := ""
	for i := uint32(0); i < 5; i++ {
		got += payloads(r.push(1, i, []byte{byte('a' + i)}, now))
	}
	if got != "abcde" {
		t.Fatalf("in-order delivery = %q, want abcde", got)
	}
}

// A datagram that arrives out of order is held, then delivered in order once
// the gap fills, with no duplication.
func TestReorderRepairsSwap(t *testing.T) {
	r := newReorderBuffer(12 * time.Millisecond)
	now := time.Unix(0, 0)
	var got string
	got += payloads(r.push(1, 0, []byte("a"), now)) // deliver a
	got += payloads(r.push(1, 2, []byte("c"), now)) // hold c (gap at 1)
	if got != "a" {
		t.Fatalf("after holding c, got %q, want a", got)
	}
	got += payloads(r.push(1, 1, []byte("b"), now)) // fills gap -> b then c
	if got != "abc" {
		t.Fatalf("after gap fill, got %q, want abc", got)
	}
}

// A genuinely lost datagram is not waited for forever: after releaseAfter the
// held datagram behind the hole is released.
func TestReorderReleasesAfterTimeout(t *testing.T) {
	r := newReorderBuffer(12 * time.Millisecond)
	t0 := time.Unix(0, 0)
	r.push(1, 0, []byte("a"), t0)        // deliver a; expected=1
	out := r.push(1, 2, []byte("c"), t0) // seq 1 missing; hold c
	if len(out) != 0 {
		t.Fatalf("c should be held, got %d", len(out))
	}
	// A later push past the release window gives up on seq 1 and frees c.
	got := payloads(r.push(1, 3, []byte("d"), t0.Add(20*time.Millisecond)))
	if got != "cd" {
		t.Fatalf("after timeout release, got %q, want cd (seq 1 skipped)", got)
	}
}

// The timer-driven flush releases a gap when the flow goes quiet after a loss.
func TestReorderFlushReleasesIdleGap(t *testing.T) {
	r := newReorderBuffer(12 * time.Millisecond)
	t0 := time.Unix(0, 0)
	r.push(7, 0, []byte("a"), t0)
	r.push(7, 2, []byte("c"), t0) // hold c behind missing seq 1
	out := r.flush(t0.Add(20 * time.Millisecond))
	if len(out) != 1 || string(out[0].payload) != "c" || out[0].flow != 7 {
		t.Fatalf("flush should release c for flow 7, got %+v", out)
	}
}

// A late duplicate of an already-delivered sequence is dropped.
func TestReorderDropsOldAndDup(t *testing.T) {
	r := newReorderBuffer(12 * time.Millisecond)
	now := time.Unix(0, 0)
	r.push(1, 0, []byte("a"), now)
	r.push(1, 1, []byte("b"), now)
	if out := r.push(1, 0, []byte("a"), now); len(out) != 0 { // old
		t.Fatalf("old seq re-delivered: %q", payloads(out))
	}
	if out := r.push(1, 1, []byte("b"), now); len(out) != 0 { // dup of delivered
		t.Fatalf("delivered seq re-delivered: %q", payloads(out))
	}
}

// Distinct flows are independent: a hole in one does not stall another.
func TestReorderFlowsIndependent(t *testing.T) {
	r := newReorderBuffer(12 * time.Millisecond)
	now := time.Unix(0, 0)
	r.push(1, 0, []byte("a"), now)
	r.push(1, 2, []byte("c"), now) // flow 1 stalled at gap
	// flow 2 flows freely
	got := payloads(r.push(2, 0, []byte("x"), now)) + payloads(r.push(2, 1, []byte("y"), now))
	if got != "xy" {
		t.Fatalf("flow 2 delivery = %q, want xy (independent of flow 1's gap)", got)
	}
}

// The sequencer numbers each flow independently and monotonically.
func TestFlowSequencer(t *testing.T) {
	s := newFlowSequencer()
	if s.next(1) != 0 || s.next(1) != 1 || s.next(1) != 2 {
		t.Fatal("flow 1 sequence not monotonic from 0")
	}
	if s.next(2) != 0 || s.next(2) != 1 {
		t.Fatal("flow 2 sequence must be independent")
	}
}

// maxPerFlow bounds memory: an unfilled gap cannot buffer without limit; the
// buffer releases forward progress instead of growing forever.
func TestReorderBoundsPerFlow(t *testing.T) {
	r := newReorderBuffer(time.Hour) // never time-release; force the size bound
	now := time.Unix(0, 0)
	r.push(1, 0, []byte("a"), now) // expected=1
	total := 0
	for i := uint32(2); i < uint32(2+defaultMaxPerFlow+50); i++ {
		total += len(r.push(1, i, []byte("z"), now)) // all behind missing seq 1
	}
	if total == 0 {
		t.Fatal("size bound never forced a release; buffer would grow unbounded")
	}
}
