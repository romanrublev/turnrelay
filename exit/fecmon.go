package exit

import (
	"sync"
	"time"
)

// Adaptive FEC control (pure logic, no I/O). The pipe carries a per-datagram
// sequence number; the receiver estimates the loss rate from gaps, and a tier
// controller maps that rate to an FEC strength tier with hysteresis and a
// minimum dwell so the session does not flap between tiers. The network wiring
// (sequence in the frame, and re-establishing KCP at the chosen tier) is layered
// on top of this in a later step.

// fecTier selects an FEC strength. Higher tiers add more parity (more loss
// recovery, more overhead). tierShards is the single source of truth for the
// (data, parity) shard counts each tier uses; both ends must agree on it.
type fecTier int

const (
	fecTierClean fecTier = iota // no parity: full throughput when the path is clean
	fecTierMild                 // light protection
	fecTierHeavy                // heavy protection for a lossy path
)

func tierShards(t fecTier) (data, parity int) {
	switch t {
	case fecTierMild:
		return 10, 2
	case fecTierHeavy:
		return 10, 4
	default:
		return 10, 0
	}
}

// lossEstimator turns a stream of per-datagram sequence numbers into a smoothed
// loss rate. It samples over a window of expected sequence numbers and folds
// each window's raw loss into an EWMA. Concurrency-safe.
type lossEstimator struct {
	mu    sync.Mutex
	win   uint32  // window size, in sequence numbers
	alpha float64 // EWMA weight for a fresh window sample
	base  uint32  // first expected seq of the current window
	top   uint32  // highest seq seen in the current window
	have  uint32  // datagrams received in the current window
	seen  bool    // any datagram observed yet
	ewma  float64 // smoothed loss rate in [0,1]
}

func newLossEstimator(window uint32, alpha float64) *lossEstimator {
	if window == 0 {
		window = 256
	}
	return &lossEstimator{win: window, alpha: alpha}
}

// observe records one received datagram's sequence number. Late arrivals (seq
// below the current window base) are ignored: they were already counted as lost
// and re-crediting them would understate loss.
func (e *lossEstimator) observe(seq uint32) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.seen {
		e.seen = true
		e.base = seq
		e.top = seq
		e.have = 1
		return
	}
	if seq < e.base {
		return
	}
	if seq > e.top {
		e.top = seq
	}
	e.have++
	if e.top-e.base+1 >= e.win {
		expected := e.top - e.base + 1
		var raw float64
		if e.have < expected {
			raw = 1 - float64(e.have)/float64(expected)
		}
		e.ewma = e.ewma*(1-e.alpha) + raw*e.alpha
		e.base = e.top + 1
		e.have = 0
	}
}

// rate returns the current smoothed loss rate in [0,1].
func (e *lossEstimator) rate() float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.ewma
}

// tierController maps a loss rate to an fecTier. Up-thresholds sit above
// down-thresholds (hysteresis), and a tier holds for at least dwell before it
// may change again, so brief loss spikes or lulls do not cause reconnect churn.
type tierController struct {
	mu         sync.Mutex
	cur        fecTier
	dwell      time.Duration
	lastChange time.Time
	// thresholds: raise to Mild above upMild, to Heavy above upHeavy; drop from
	// Heavy below downHeavy, from Mild below downMild.
	upMild, downMild   float64
	upHeavy, downHeavy float64
}

func newTierController(dwell time.Duration) *tierController {
	return &tierController{
		dwell:    dwell,
		upMild:   0.005, downMild: 0.002,
		upHeavy: 0.03, downHeavy: 0.015,
	}
}

// update feeds a fresh loss rate and returns the tier to use now and whether it
// changed. It moves at most one tier per call (a change resets the dwell timer).
func (c *tierController) update(loss float64, now time.Time) (fecTier, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.lastChange.IsZero() && now.Sub(c.lastChange) < c.dwell {
		return c.cur, false
	}
	next := c.cur
	switch c.cur {
	case fecTierClean:
		if loss > c.upMild {
			next = fecTierMild
		}
	case fecTierMild:
		if loss > c.upHeavy {
			next = fecTierHeavy
		} else if loss < c.downMild {
			next = fecTierClean
		}
	case fecTierHeavy:
		if loss < c.downHeavy {
			next = fecTierMild
		}
	}
	if next == c.cur {
		return c.cur, false
	}
	c.cur = next
	c.lastChange = now
	return c.cur, true
}
