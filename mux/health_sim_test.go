package mux

import (
	"math/rand/v2"
	"testing"
	"time"
)

// TestHealthPoolLowersFleetLossSimulation is a controlled bench for the
// health-based worker pool: it isolates the eviction loop from live VK path
// noise by driving the real workerHealth estimator and pickEvict decision over
// a modelled fleet of relays, some clean and some lossy. It measures the mean
// aggregate path loss the fleet rides with eviction off versus on, and asserts
// eviction lowers it by migrating workers off lossy relays. This quantifies the
// algorithm's benefit deterministically (seeded RNG), which a live A/B on VK
// cannot because the path's own loss swings dwarf the signal.
func TestHealthPoolLowersFleetLossSimulation(t *testing.T) {
	const (
		nRelays      = 300 // pool of relays the "credpool" can hand out
		nWorkers     = 60
		rounds       = 3000 // one round == one health-probe interval (~1s)
		superviseEvy = 5    // rounds between supervise passes (5s at 1s probes)
		coolRounds   = 600  // how long an evicted relay stays avoided
		warmupFrac   = 2    // measure steady state over the last 1/warmupFrac
	)
	rng := rand.New(rand.NewPCG(42, 1))

	// Relay loss profile: ~30% of relays are lossy (10-25%), the rest clean
	// (0-2%). This mirrors the observed VK mix (some of the workers ride dead
	// or lossy relays).
	relayLoss := make([]float64, nRelays)
	for i := range relayLoss {
		if rng.Float64() < 0.30 {
			relayLoss[i] = 0.10 + rng.Float64()*0.15
		} else {
			relayLoss[i] = rng.Float64() * 0.02
		}
	}

	run := func(evict bool) float64 {
		r2 := rand.New(rand.NewPCG(7, 99)) // independent draw stream, same for both runs
		cooledUntil := make([]int, nRelays)
		for i := range cooledUntil {
			cooledUntil[i] = -1
		}
		pickRelay := func(round int) int {
			for tries := 0; tries < 50; tries++ {
				c := r2.IntN(nRelays)
				if cooledUntil[c] <= round {
					return c
				}
			}
			return r2.IntN(nRelays) // give up avoiding cooldown; take any
		}

		workerRelay := make([]int, nWorkers)
		health := make([]*workerHealth, nWorkers)
		seq := make([]uint64, nWorkers)
		for i := range health {
			health[i] = newWorkerHealth()
			health[i].setActive(true)
			workerRelay[i] = pickRelay(0)
		}

		var cfg evictConfig
		cfg.defaults()
		base := time.Unix(0, 0)
		var lossSum float64
		var lossN int
		measureFrom := rounds - rounds/warmupFrac

		for round := 0; round < rounds; round++ {
			now := base.Add(time.Duration(round) * time.Second)
			for i := 0; i < nWorkers; i++ {
				seq[i]++
				health[i].sentProbe(seq[i], now)
				health[i].tick(now) // sweep older unacked probes into loss
				if r2.Float64() >= relayLoss[workerRelay[i]] {
					health[i].ackProbe(seq[i], now.Add(50*time.Millisecond))
				}
			}
			if round >= measureFrom {
				for i := 0; i < nWorkers; i++ {
					lossSum += relayLoss[workerRelay[i]]
					lossN++
				}
			}
			if evict && round > 0 && round%superviseEvy == 0 {
				snaps := make([]healthSnapshot, nWorkers)
				for i := range health {
					snaps[i] = health[i].snapshot(i)
				}
				if id := pickEvict(snaps, cfg); id >= 0 {
					cooledUntil[workerRelay[id]] = round + coolRounds
					workerRelay[id] = pickRelay(round)
					health[id].reset()
				}
			}
		}
		return lossSum / float64(lossN)
	}

	off := run(false)
	on := run(true)
	reduction := (off - on) / off * 100
	t.Logf("fleet mean path loss: off=%.3f%% on=%.3f%% (%.0f%% reduction)", off*100, on*100, reduction)
	if on >= off {
		t.Fatalf("eviction did not lower fleet loss: off=%.4f on=%.4f", off, on)
	}
	// The health pool should retire relays above the eviction threshold; expect
	// a clear reduction, not a marginal one.
	if reduction < 25 {
		t.Fatalf("eviction reduced fleet loss by only %.0f%%, expected a clear win", reduction)
	}
}
