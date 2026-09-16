package exit

import (
	"math/rand"
	"testing"
	"time"
)

// TestAdaptiveFECLoopRecoversUnderRisingLoss ties the whole adaptive pipeline
// together in-process: loss estimate -> tier -> parity -> block encode -> lossy
// channel -> reassembler, with the estimator fed by the surviving datagrams'
// pipe sequence (gaps = loss). It asserts that a clean phase runs at ~zero
// parity with full delivery, and that when loss appears the tier ramps up and
// late-phase recovery is near-complete despite the drops.
func TestAdaptiveFECLoopRecoversUnderRisingLoss(t *testing.T) {
	codec := newFECCodec()
	est := newLossEstimator(40, 0.5)
	ctrl := newTierController(0) // no dwell: deterministic per-block adaptation
	reasm := newBlockReassembler(codec, 256)
	rng := rand.New(rand.NewSource(1))
	now := time.Unix(0, 0)

	const (
		blocks   = 240
		perBlock = 10
		cleanEnd = 120
		lossRate = 0.10
	)
	var seq uint32
	parityByPhase := map[string]int{}   // last tier's parity seen per phase
	deliveredLate, expectedLate := 0, 0 // blocks 190..239 (well after ramp)
	cleanDelivered, cleanExpected := 0, 0

	for blk := 0; blk < blocks; blk++ {
		tier, _ := ctrl.update(est.rate(), now)
		now = now.Add(20 * time.Millisecond)
		_, parity := tierShards(tier)

		p := 0.0
		phase := "clean"
		if blk >= cleanEnd {
			p = lossRate
			phase = "loss"
		}
		parityByPhase[phase] = parity

		payloads := distinctPayloads(perBlock)
		frames, err := encodeBlock(codec, uint32(blk), payloads, parity)
		if err != nil {
			t.Fatal(err)
		}
		got := 0
		for _, f := range frames {
			s := seq
			seq++
			if rng.Float64() < p {
				continue // datagram dropped: estimator never observes this seq
			}
			est.observe(s)
			got += len(reasm.decode(f))
		}
		switch {
		case phase == "clean":
			cleanDelivered += got
			cleanExpected += perBlock
		case blk >= 190:
			deliveredLate += got
			expectedLate += perBlock
		}
	}

	// Clean phase: ~zero parity overhead and every payload delivered.
	if parityByPhase["clean"] != 0 {
		t.Fatalf("clean phase used parity %d, want 0", parityByPhase["clean"])
	}
	if cleanDelivered != cleanExpected {
		t.Fatalf("clean phase delivered %d/%d, want all", cleanDelivered, cleanExpected)
	}

	// Loss phase: tier must have raised parity, and late recovery near-complete.
	if parityByPhase["loss"] == 0 {
		t.Fatalf("loss phase never raised parity above 0 (tier did not adapt)")
	}
	ratio := float64(deliveredLate) / float64(expectedLate)
	if ratio < 0.97 {
		t.Fatalf("late loss-phase delivery %.3f (%d/%d), want >=0.97 after adaptation",
			ratio, deliveredLate, expectedLate)
	}
	t.Logf("adapted: loss-phase parity=%d, late delivery=%.3f", parityByPhase["loss"], ratio)
}
