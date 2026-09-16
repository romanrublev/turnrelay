package exit

import (
	"testing"
	"time"
)

func TestTierShards(t *testing.T) {
	cases := []struct {
		tier         fecTier
		data, parity int
	}{
		{fecTierClean, 10, 0},
		{fecTierMild, 10, 2},
		{fecTierHeavy, 10, 4},
	}
	for _, c := range cases {
		d, p := tierShards(c.tier)
		if d != c.data || p != c.parity {
			t.Fatalf("tier %d: got %d/%d, want %d/%d", c.tier, d, p, c.data, c.parity)
		}
	}
}

func TestLossEstimatorNoLoss(t *testing.T) {
	e := newLossEstimator(100, 0.5)
	for i := uint32(0); i < 300; i++ {
		e.observe(i)
	}
	if r := e.rate(); r != 0 {
		t.Fatalf("clean stream loss rate = %v, want 0", r)
	}
}

func TestLossEstimatorDropsEveryTenth(t *testing.T) {
	e := newLossEstimator(100, 1.0) // alpha 1: rate == last window's raw loss
	for i := uint32(0); i < 1000; i++ {
		if i%10 == 9 {
			continue // drop 10%
		}
		e.observe(i)
	}
	r := e.rate()
	if r < 0.08 || r > 0.12 {
		t.Fatalf("loss rate = %v, want ~0.10", r)
	}
}

func TestLossEstimatorIgnoresLateArrivals(t *testing.T) {
	e := newLossEstimator(50, 1.0)
	// fill a window cleanly so base advances past 0
	for i := uint32(0); i < 60; i++ {
		e.observe(i)
	}
	// a very late packet from before the window base must not panic or crash
	e.observe(1)
	_ = e.rate()
}

func TestTierControllerRaisesAndHolds(t *testing.T) {
	c := newTierController(10 * time.Second)
	t0 := time.Unix(0, 0)
	// clean -> mild on loss above upMild
	if tier, changed := c.update(0.05, t0); tier != fecTierMild || !changed {
		t.Fatalf("expected raise to Mild, got tier=%d changed=%v", tier, changed)
	}
	// within dwell: no change even if loss spikes
	if tier, changed := c.update(0.20, t0.Add(time.Second)); tier != fecTierMild || changed {
		t.Fatalf("dwell should hold Mild, got tier=%d changed=%v", tier, changed)
	}
	// after dwell with heavy loss -> Heavy
	if tier, changed := c.update(0.20, t0.Add(11*time.Second)); tier != fecTierHeavy || !changed {
		t.Fatalf("expected raise to Heavy, got tier=%d changed=%v", tier, changed)
	}
}

func TestTierControllerHysteresisDown(t *testing.T) {
	c := newTierController(0) // no dwell, test hysteresis alone
	t0 := time.Unix(0, 0)
	c.update(0.20, t0) // -> Mild
	c.update(0.20, t0) // -> Heavy
	if c.cur != fecTierHeavy {
		t.Fatalf("setup: want Heavy, got %d", c.cur)
	}
	// loss between downHeavy(0.05) and downMild is ambiguous; use a value that
	// drops Heavy->Mild but not below downMild, so it should stop at Mild.
	if tier, _ := c.update(0.03, t0); tier != fecTierMild {
		t.Fatalf("Heavy should drop to Mild at loss 0.03, got %d", tier)
	}
	// now clean -> below downMild(0.01) drops Mild->Clean
	if tier, _ := c.update(0.0, t0); tier != fecTierClean {
		t.Fatalf("Mild should drop to Clean at loss 0, got %d", tier)
	}
}
