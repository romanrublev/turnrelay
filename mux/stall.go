package mux

import (
	"sync/atomic"
	"time"
)

// Stall detection for a relay that stops taking bytes. A write to the relay can
// block indefinitely (the obfs conns serialise writes and do not honour write
// deadlines), and when it does, every writer on that worker is stuck behind it:
// the uplink holds a stolen datagram, and the probe loop stops calling into the
// health estimator, so neither the loss signal nor the zombie timeout can fire.
// The worker then looks active while carrying nothing. A guard records when a
// write started so an independent watchdog can judge it on its own clock.

const (
	// writeStallAfter is how long a single write may be in flight before the
	// relay is judged stalled and the session torn down. It is well above a
	// healthy relay's write latency (sub-millisecond for UDP, a few ms over
	// TCP) and short enough that a wedged relay costs about a second.
	writeStallAfter = time.Second
	// writeWatchInterval is how often the watchdog looks; it bounds how late
	// the verdict can be.
	writeWatchInterval = 250 * time.Millisecond
)

// writeGuard tracks one writer's in-flight write. Each writer goroutine owns
// its own guard: a shared one would let a fast writer's end() clear the slot a
// slow writer is still stuck in. Lock-free so the write path stays cheap.
type writeGuard struct {
	startedAt atomic.Int64 // unix nanos; 0 means no write in flight
}

// begin marks a write as started.
func (g *writeGuard) begin(now time.Time) { g.startedAt.Store(now.UnixNano()) }

// end marks the write as finished.
func (g *writeGuard) end() { g.startedAt.Store(0) }

// inFlightFor reports how long the current write has been in flight, or 0 when
// the writer is idle or the clock went backwards.
func (g *writeGuard) inFlightFor(now time.Time) time.Duration {
	started := g.startedAt.Load()
	if started == 0 {
		return 0
	}
	if d := now.Sub(time.Unix(0, started)); d > 0 {
		return d
	}
	return 0
}

// stalled reports whether any of the guards has a write in flight for longer
// than limit, and how long that longest write has been stuck.
func stalled(now time.Time, limit time.Duration, guards ...*writeGuard) (time.Duration, bool) {
	var worst time.Duration
	for _, g := range guards {
		if d := g.inFlightFor(now); d > worst {
			worst = d
		}
	}
	return worst, worst >= limit
}
