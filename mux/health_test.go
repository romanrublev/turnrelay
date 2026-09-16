package mux

import (
	"testing"
	"time"
)

func TestWorkerHealthRTTAndCleanLoss(t *testing.T) {
	h := newWorkerHealth()
	base := time.Unix(0, 0)
	// Every probe is echoed 50ms later: RTT should converge near 50ms and
	// loss stay at 0.
	for i := uint64(0); i < 20; i++ {
		sent := base.Add(time.Duration(i) * time.Second)
		h.sentProbe(i, sent)
		h.ackProbe(i, sent.Add(50*time.Millisecond))
	}
	s := h.snapshot(3)
	if s.loss != 0 {
		t.Fatalf("clean stream loss = %v, want 0", s.loss)
	}
	if s.rtt < 45*time.Millisecond || s.rtt > 55*time.Millisecond {
		t.Fatalf("rtt = %v, want ~50ms", s.rtt)
	}
	if s.samples != 20 {
		t.Fatalf("samples = %d, want 20", s.samples)
	}
}

func TestWorkerHealthCountsTimeoutsAsLoss(t *testing.T) {
	h := newWorkerHealth()
	base := time.Unix(0, 0)
	// Half the probes are never echoed. Drive it the way a worker does:
	// each round sends a probe, then either sees the echo (even) or, one
	// round later, has it swept out as a timeout (odd). Interleaving hits
	// and misses is what makes the EWMA settle near 0.5.
	for i := uint64(0); i < 40; i++ {
		sent := base.Add(time.Duration(i) * time.Second)
		h.sentProbe(i, sent)
		if i%2 == 0 {
			h.ackProbe(i, sent.Add(30*time.Millisecond))
		} else {
			h.tick(sent.Add(h.timeout + time.Millisecond)) // only this probe is outstanding
		}
	}
	s := h.snapshot(0)
	if s.loss < 0.30 || s.loss > 0.70 {
		t.Fatalf("loss = %v, want ~0.5 for half-dropped probes", s.loss)
	}
}

func TestWorkerHealthLateEchoDoesNotUncountLoss(t *testing.T) {
	h := newWorkerHealth()
	base := time.Unix(0, 0)
	h.sentProbe(1, base)
	h.tick(base.Add(10 * time.Second)) // seq 1 times out -> loss folded
	lossAfterTimeout := h.snapshot(0).loss
	if lossAfterTimeout == 0 {
		t.Fatal("timeout did not register as loss")
	}
	// A very late echo for the already-timed-out seq must be ignored.
	h.ackProbe(1, base.Add(11*time.Second))
	if got := h.snapshot(0).loss; got != lossAfterTimeout {
		t.Fatalf("late echo changed loss from %v to %v", lossAfterTimeout, got)
	}
}

func TestPickEvictDropsLossiestActive(t *testing.T) {
	cfg := evictConfig{minSamples: 4, keepActive: 1}
	snaps := []healthSnapshot{
		{id: 0, active: true, loss: 0.01, samples: 10, rtt: 50 * time.Millisecond},
		{id: 1, active: true, loss: 0.40, samples: 10, rtt: 60 * time.Millisecond}, // worst loss
		{id: 2, active: true, loss: 0.20, samples: 10, rtt: 55 * time.Millisecond},
	}
	if got := pickEvict(snaps, cfg); got != 1 {
		t.Fatalf("pickEvict = %d, want 1 (lossiest)", got)
	}
}

func TestPickEvictNeedsSamples(t *testing.T) {
	cfg := evictConfig{minSamples: 8, keepActive: 1}
	snaps := []healthSnapshot{
		{id: 0, active: true, loss: 0.01, samples: 20},
		{id: 1, active: true, loss: 0.90, samples: 3}, // very lossy but too few samples
	}
	if got := pickEvict(snaps, cfg); got != -1 {
		t.Fatalf("pickEvict = %d, want -1 (not enough samples to judge)", got)
	}
}

func TestPickEvictKeepsMinimumActive(t *testing.T) {
	cfg := evictConfig{minSamples: 4, keepActive: 1}
	// Only one active worker, and it is lossy: still must not be evicted,
	// a bad path beats no path while nothing has replaced it.
	snaps := []healthSnapshot{
		{id: 0, active: true, loss: 0.90, samples: 20},
		{id: 1, active: false, loss: 0, samples: 0},
	}
	if got := pickEvict(snaps, cfg); got != -1 {
		t.Fatalf("pickEvict = %d, want -1 (keep the last active worker)", got)
	}
}

func TestPickEvictRTTOutlier(t *testing.T) {
	cfg := evictConfig{minSamples: 4, keepActive: 1, rttEvictMult: 4, rttFloor: 200 * time.Millisecond, lossEvict: 0.15}
	snaps := []healthSnapshot{
		{id: 0, active: true, loss: 0, samples: 10, rtt: 50 * time.Millisecond},
		{id: 1, active: true, loss: 0, samples: 10, rtt: 60 * time.Millisecond},
		{id: 2, active: true, loss: 0, samples: 10, rtt: 900 * time.Millisecond}, // >4x median, above floor
	}
	if got := pickEvict(snaps, cfg); got != 2 {
		t.Fatalf("pickEvict = %d, want 2 (RTT outlier)", got)
	}
}

func TestPickEvictNothingWrong(t *testing.T) {
	cfg := evictConfig{minSamples: 4, keepActive: 1}
	snaps := []healthSnapshot{
		{id: 0, active: true, loss: 0.02, samples: 10, rtt: 50 * time.Millisecond},
		{id: 1, active: true, loss: 0.03, samples: 10, rtt: 60 * time.Millisecond},
	}
	if got := pickEvict(snaps, cfg); got != -1 {
		t.Fatalf("pickEvict = %d, want -1 (all healthy)", got)
	}
}

func TestPickEvictSkipsPathWideLoss(t *testing.T) {
	cfg := evictConfig{minSamples: 4, keepActive: 1}
	// Every worker loses about the same (~20%): the path is lossy, not one
	// relay. No single worker is an outlier, so nothing should be evicted -
	// evicting would just churn an equally lossy VK relay.
	snaps := []healthSnapshot{
		{id: 0, active: true, loss: 0.18, samples: 10, rtt: 50 * time.Millisecond},
		{id: 1, active: true, loss: 0.20, samples: 10, rtt: 55 * time.Millisecond},
		{id: 2, active: true, loss: 0.22, samples: 10, rtt: 60 * time.Millisecond},
	}
	if got := pickEvict(snaps, cfg); got != -1 {
		t.Fatalf("pickEvict = %d, want -1 (path-wide loss, no outlier)", got)
	}
}

func TestPickEvictStillDropsOutlierOnLossyPath(t *testing.T) {
	cfg := evictConfig{minSamples: 4, keepActive: 1}
	// A moderately lossy path (median ~10%) with one relay far worse (45%):
	// the outlier is well above median*mult and must still be retired.
	snaps := []healthSnapshot{
		{id: 0, active: true, loss: 0.08, samples: 10},
		{id: 1, active: true, loss: 0.10, samples: 10},
		{id: 2, active: true, loss: 0.11, samples: 10},
		{id: 3, active: true, loss: 0.45, samples: 10}, // outlier
	}
	if got := pickEvict(snaps, cfg); got != 3 {
		t.Fatalf("pickEvict = %d, want 3 (outlier on a lossy path)", got)
	}
}
