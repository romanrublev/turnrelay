package mux

import (
	"sync"
	"time"
)

// Per-worker health (pure logic, no I/O). Each worker rides one VK relay; a
// relay can be alive yet lossy or slow, and because the uplink is a work-
// stealing queue a worker that keeps writing successfully into a lossy relay
// still steals traffic the relay then silently drops. conn.Write cannot see
// that loss, so we measure it out-of-band: the server echoes every probe
// (see mux/control.go, server.go), which gives each worker a request/reply
// channel. Timing the echo yields RTT; counting sent-vs-echoed yields loss.
//
// workerHealth folds those samples into EWMAs. A supervisor snapshots all
// workers and, with pickEvict below, decides which single worst relay to drop
// so the pool re-allocates a fresh one. The wiring (timing echoes, driving the
// supervisor, and per-relay eviction in credpool) is layered on top of this in
// later steps; this file is deliberately I/O-free and unit-tested.

// healthDefaults are the smoothing and timeout constants a worker uses when it
// makes its own workerHealth. They are tunables, not protocol constants.
const (
	// rttAlpha/lossAlpha weight a fresh sample against the running EWMA.
	// RTT is smoothed harder (it is noisier per sample); loss reacts faster
	// so a relay going bad is caught within a few probes.
	defaultRTTAlpha  = 0.2
	defaultLossAlpha = 0.3
	// probeTimeout is how long an unanswered probe waits before it counts as
	// a loss. It must exceed a healthy RTT with margin; the caller sweeps
	// outstanding probes against it from tick.
	defaultProbeTimeout = 2 * time.Second
)

// workerHealth tracks one worker's smoothed RTT and probe-loss plus liveness.
// The owning worker records probes it sends and echoes it receives; a
// supervisor reads snapshots. All access is mutex-guarded because the send,
// receive and sweep can run on different goroutines within one worker, and
// the supervisor reads concurrently. Probe cadence is ~1/s, so the lock is
// never hot.
type workerHealth struct {
	mu          sync.Mutex
	rttAlpha    float64
	lossAlpha   float64
	timeout     time.Duration
	active      bool                 // worker currently in the active state
	rtt         time.Duration        // smoothed round-trip time; 0 until first echo
	loss        float64              // smoothed probe-loss fraction in [0,1]
	samples     int                  // probe outcomes folded (echo or timeout)
	lastAck     time.Time            // when the most recent echo arrived
	outstanding map[uint64]time.Time // seq -> send time, awaiting echo
}

func newWorkerHealth() *workerHealth {
	return &workerHealth{
		rttAlpha:    defaultRTTAlpha,
		lossAlpha:   defaultLossAlpha,
		timeout:     defaultProbeTimeout,
		outstanding: map[uint64]time.Time{},
	}
}

// sentProbe records that probe seq went out at now. It is safe to call for a
// seq already outstanding (the send time is refreshed).
func (h *workerHealth) sentProbe(seq uint64, now time.Time) {
	h.mu.Lock()
	h.outstanding[seq] = now
	h.mu.Unlock()
}

// ackProbe records the echo for seq arriving at now: it folds the RTT and a
// hit (loss 0) into the EWMAs. An echo for a seq that already timed out or was
// never sent is ignored, so a very late echo cannot un-count a loss.
func (h *workerHealth) ackProbe(seq uint64, now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	sent, ok := h.outstanding[seq]
	if !ok {
		return
	}
	delete(h.outstanding, seq)
	rtt := now.Sub(sent)
	if rtt < 0 {
		rtt = 0
	}
	if h.rtt == 0 {
		h.rtt = rtt
	} else {
		h.rtt = time.Duration(float64(h.rtt)*(1-h.rttAlpha) + float64(rtt)*h.rttAlpha)
	}
	h.loss = h.loss * (1 - h.lossAlpha) // fold a hit (0)
	h.lastAck = now
	h.samples++
}

// tick sweeps outstanding probes older than the timeout, folding one miss
// (loss 1) into the EWMA for each. Call it at the probe cadence.
func (h *workerHealth) tick(now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for seq, sent := range h.outstanding {
		if now.Sub(sent) >= h.timeout {
			delete(h.outstanding, seq)
			h.loss = h.loss*(1-h.lossAlpha) + h.lossAlpha // fold a miss (1)
			h.samples++
		}
	}
}

// setActive marks whether the worker is in the active state (visible to the
// supervisor's snapshot). A worker sets it true when its session comes up and
// false when it tears down.
func (h *workerHealth) setActive(v bool) {
	h.mu.Lock()
	h.active = v
	h.mu.Unlock()
}

// reset clears the smoothed metrics and outstanding probes for a fresh session.
// A worker calls it when it comes up on a (possibly new) relay so the previous
// relay's history does not colour the new one. It leaves active alone; the
// caller manages that.
func (h *workerHealth) reset() {
	h.mu.Lock()
	h.rtt = 0
	h.loss = 0
	h.samples = 0
	h.lastAck = time.Time{}
	clear(h.outstanding)
	h.mu.Unlock()
}

// healthSnapshot is a supervisor-visible copy of one worker's health.
type healthSnapshot struct {
	id      int
	active  bool          // worker reached the active state
	rtt     time.Duration // smoothed RTT; 0 if no echo yet
	loss    float64       // smoothed probe-loss in [0,1]
	samples int           // probe outcomes folded
	lastAck time.Time
}

func (h *workerHealth) snapshot(id int) healthSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	return healthSnapshot{id: id, active: h.active, rtt: h.rtt, loss: h.loss, samples: h.samples, lastAck: h.lastAck}
}

// evictConfig parameterises pickEvict. Zero values are filled by defaults().
type evictConfig struct {
	// lossEvict: a worker whose smoothed loss is at or above this is an
	// eviction candidate (given enough samples).
	lossEvict float64
	// rttEvictMult: a worker whose RTT exceeds rttEvictMult times the median
	// active RTT (and rttFloor) is a candidate even if its loss looks fine.
	rttEvictMult float64
	rttFloor     time.Duration
	// minSamples: do not judge a worker until it has folded this many probe
	// outcomes, so a single early miss cannot evict a healthy relay.
	minSamples int
	// keepActive: never evict when it would drop the active-worker count to
	// or below this; a lossy path in hand beats none while a replacement
	// allocates.
	keepActive int
	// lossOutlierMult: a loss-based eviction only fires when the worst
	// worker's loss is at least this multiple of the median active loss.
	// When the whole path is lossy (bad Wi-Fi, congested uplink) every worker
	// loses about equally, so the worst is not an outlier and evicting it just
	// churns a fresh VK relay that loses just as much; the FEC layer, not
	// eviction, handles path-wide loss. A lone bad relay among healthy ones is
	// far above the median and still gets retired.
	lossOutlierMult float64
}

func (c *evictConfig) defaults() {
	if c.lossEvict == 0 {
		c.lossEvict = 0.15
	}
	if c.rttEvictMult == 0 {
		c.rttEvictMult = 4
	}
	if c.rttFloor == 0 {
		c.rttFloor = 400 * time.Millisecond
	}
	if c.minSamples == 0 {
		c.minSamples = 8
	}
	if c.keepActive == 0 {
		c.keepActive = 1
	}
	if c.lossOutlierMult == 0 {
		c.lossOutlierMult = 1.6
	}
}

// pickEvict returns the id of the single worst worker to evict now, or -1 when
// none should be dropped. It evicts at most one per call (the supervisor calls
// it on an interval), so a bad batch is retired gradually and the pool is never
// gutted in one sweep. Loss is the primary signal; a pathological RTT outlier
// is the secondary one. A candidate is only dropped while more than keepActive
// workers remain active, so the pipe is never taken down to nothing chasing a
// marginally better relay.
func pickEvict(snaps []healthSnapshot, cfg evictConfig) int {
	cfg.defaults()
	activeCount := 0
	for _, s := range snaps {
		if s.active {
			activeCount++
		}
	}
	if activeCount <= cfg.keepActive {
		return -1
	}
	medRTT := medianActiveRTT(snaps)

	worst := -1
	var worstLoss float64
	var worstRTT time.Duration
	worstByLoss := false
	for _, s := range snaps {
		if !s.active || s.samples < cfg.minSamples {
			continue
		}
		lossBad := s.loss >= cfg.lossEvict
		rttBad := medRTT > 0 && s.rtt > cfg.rttFloor &&
			float64(s.rtt) > cfg.rttEvictMult*float64(medRTT)
		if !lossBad && !rttBad {
			continue
		}
		// Rank candidates: worse loss first, then worse RTT. A high-loss
		// relay hurts more than a merely slow one.
		if worst == -1 || s.loss > worstLoss || (s.loss == worstLoss && s.rtt > worstRTT) {
			worst = s.id
			worstLoss = s.loss
			worstRTT = s.rtt
			worstByLoss = lossBad
		}
	}
	// Path-wide loss guard: if the winner was flagged for loss but is not an
	// outlier against the median active loss, the whole path is lossy, not this
	// one relay. Retiring it would just churn an equally lossy replacement, so
	// leave it and let FEC absorb the loss.
	if worst >= 0 && worstByLoss {
		if med := medianActiveLoss(snaps, cfg.minSamples); worstLoss < med*cfg.lossOutlierMult {
			return -1
		}
	}
	return worst
}

// medianActiveLoss is the median smoothed loss over active workers with enough
// samples to judge, or 0 when none qualify.
func medianActiveLoss(snaps []healthSnapshot, minSamples int) float64 {
	var losses []float64
	for _, s := range snaps {
		if s.active && s.samples >= minSamples {
			losses = append(losses, s.loss)
		}
	}
	if len(losses) == 0 {
		return 0
	}
	for i := 1; i < len(losses); i++ {
		for j := i; j > 0 && losses[j-1] > losses[j]; j-- {
			losses[j-1], losses[j] = losses[j], losses[j-1]
		}
	}
	return losses[len(losses)/2]
}

// medianActiveRTT is the median RTT over active workers that have an RTT
// sample, or 0 when none do. Used only to spot RTT outliers.
func medianActiveRTT(snaps []healthSnapshot) time.Duration {
	var rtts []time.Duration
	for _, s := range snaps {
		if s.active && s.rtt > 0 {
			rtts = append(rtts, s.rtt)
		}
	}
	if len(rtts) == 0 {
		return 0
	}
	// insertion sort: the slice is tiny (<= worker count, tens).
	for i := 1; i < len(rtts); i++ {
		for j := i; j > 0 && rtts[j-1] > rtts[j]; j-- {
			rtts[j-1], rtts[j] = rtts[j], rtts[j-1]
		}
	}
	return rtts[len(rtts)/2]
}
