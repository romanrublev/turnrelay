package mux

import (
	"testing"
	"time"
)

func TestWakeDetectorSeedsOnFirstTick(t *testing.T) {
	w := newWakeDetector(20 * time.Second)
	if w.tick(time.Unix(1000, 0)) {
		t.Fatal("first tick reported a wake; it can only seed the clock")
	}
}

func TestWakeDetectorIgnoresSchedulingJitter(t *testing.T) {
	w := newWakeDetector(20 * time.Second)
	now := time.Unix(1000, 0)
	w.tick(now)
	for i := 1; i <= 20; i++ {
		// ticks land late but nowhere near the gap: a busy machine, not sleep
		now = now.Add(wakeTickInterval + 900*time.Millisecond)
		if w.tick(now) {
			t.Fatalf("tick %d (%v late) misread as a wake", i, 900*time.Millisecond)
		}
	}
}

func TestWakeDetectorCatchesSleep(t *testing.T) {
	w := newWakeDetector(20 * time.Second)
	now := time.Unix(1000, 0)
	w.tick(now)
	now = now.Add(wakeTickInterval)
	w.tick(now)
	// the lid closes for ten minutes
	if !w.tick(now.Add(10 * time.Minute)) {
		t.Fatal("a ten-minute gap was not read as a wake")
	}
	// and the ticks that follow are ordinary again
	if w.tick(now.Add(10*time.Minute + wakeTickInterval)) {
		t.Fatal("the tick after the wake reported another wake")
	}
}
