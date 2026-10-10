package mux

import "time"

// Handover: the path under the pool changed (Wi-Fi to LTE, a laptop woke from
// sleep). Every TURN allocation is bound to the old source address and is dead,
// but a UDP write to a gone path usually still "succeeds" locally, so nothing
// errors: the pool would sit there until the zombie timeout noticed minutes
// later. Detecting the change and dropping every session at once is what turns
// that into a couple of seconds.

const (
	// wakeTickInterval is how often the sleep detector looks at the clock.
	wakeTickInterval = 2 * time.Second
	// wakeGap is how far a tick must slip before the gap is read as the
	// process having been suspended (a laptop asleep) rather than scheduling
	// jitter. Well above a loaded machine's tick delay, well below the zombie
	// timeout it replaces.
	wakeGap = 20 * time.Second
	// handoverBackoff paces the reconnect after a handover. The relay is not
	// at fault, so the backoff ladder does not apply; this only spreads the
	// pool's reconnects so they do not arrive at the relay as one burst.
	handoverBackoff = 400 * time.Millisecond
)

// wakeDetector turns a stream of tick times into "the process was suspended".
// A monotonic timer cannot see sleep: the tick simply arrives late, so the gap
// between ticks is the signal.
type wakeDetector struct {
	gap  time.Duration
	last time.Time
}

func newWakeDetector(gap time.Duration) *wakeDetector {
	if gap <= 0 {
		gap = wakeGap
	}
	return &wakeDetector{gap: gap}
}

// tick records a tick at now and reports whether the machine appears to have
// slept since the previous one. The first tick only seeds the clock.
func (w *wakeDetector) tick(now time.Time) bool {
	prev := w.last
	w.last = now
	if prev.IsZero() {
		return false
	}
	return now.Sub(prev) >= w.gap
}
