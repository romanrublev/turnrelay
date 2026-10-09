package mux

import (
	"testing"
	"time"
)

func TestWriteGuardIdleAndInFlight(t *testing.T) {
	var g writeGuard
	now := time.Unix(1000, 0)
	if d := g.inFlightFor(now); d != 0 {
		t.Fatalf("idle guard reported %v in flight", d)
	}
	g.begin(now)
	if d := g.inFlightFor(now.Add(300 * time.Millisecond)); d != 300*time.Millisecond {
		t.Fatalf("in-flight = %v, want 300ms", d)
	}
	g.end()
	if d := g.inFlightFor(now.Add(time.Hour)); d != 0 {
		t.Fatalf("finished write still counted: %v", d)
	}
}

// A write that completes quickly must never be judged stalled, however long the
// worker lives afterwards.
func TestStalledIgnoresCompletedWrites(t *testing.T) {
	var up, probe writeGuard
	now := time.Unix(1000, 0)
	up.begin(now)
	up.end()
	probe.begin(now)
	probe.end()
	if d, ok := stalled(now.Add(10*time.Second), writeStallAfter, &up, &probe); ok {
		t.Fatalf("idle guards judged stalled after %v", d)
	}
}

// A single writer stuck past the limit is caught, and the reported duration is
// the stuck write's age.
func TestStalledCatchesStuckWriter(t *testing.T) {
	var up, probe writeGuard
	now := time.Unix(1000, 0)
	up.begin(now) // uplink wedged on a relay that stopped taking bytes
	probe.begin(now)
	probe.end()

	if _, ok := stalled(now.Add(writeStallAfter-time.Millisecond), writeStallAfter, &up, &probe); ok {
		t.Fatal("judged stalled before the limit elapsed")
	}
	d, ok := stalled(now.Add(writeStallAfter+500*time.Millisecond), writeStallAfter, &up, &probe)
	if !ok {
		t.Fatal("stuck uplink write not caught")
	}
	if d < writeStallAfter {
		t.Fatalf("reported %v in flight, want at least %v", d, writeStallAfter)
	}
}

// Each writer has its own guard: the probe loop finishing its writes must not
// clear the uplink's stuck one (the bug a single shared slot would have).
func TestStalledPerWriterGuards(t *testing.T) {
	var up, probe writeGuard
	t0 := time.Unix(1000, 0)
	up.begin(t0) // stuck from the start
	for i := 1; i <= 5; i++ {
		at := t0.Add(time.Duration(i) * 300 * time.Millisecond)
		probe.begin(at)
		probe.end() // healthy, fast writes alongside the stuck one
	}
	if _, ok := stalled(t0.Add(2*time.Second), writeStallAfter, &up, &probe); !ok {
		t.Fatal("a fast writer masked the stuck one")
	}
}
