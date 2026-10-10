package engine

import (
	"fmt"
	"strings"
	"sync/atomic"
)

type Counters struct {
	Workers     atomic.Int32 // cumulative "worker up" events (fallback before the first gauge)
	HandshakeOK atomic.Bool
	// Health-pool gauges, set from the mux supervisor's periodic health line.
	ActiveWorkers atomic.Int32 // live active workers from the latest gauge
	GaugeSeen     atomic.Bool  // a health gauge has been parsed at least once
	Evictions     atomic.Int32 // cumulative relays retired by the supervisor
	MaxLossBp     atomic.Int32 // worst active worker's smoothed loss, basis points
	MeanRTTMs     atomic.Int32 // mean active-worker RTT, milliseconds
	Handovers     atomic.Int32 // cumulative path changes the pool reacted to
}

func scanLine(line string, c *Counters) {
	if strings.Contains(line, "up via") {
		c.Workers.Add(1)
	}
	if strings.Contains(line, "wireguard") && strings.Contains(line, "handshake") &&
		!strings.Contains(line, "initiation") {
		c.HandshakeOK.Store(true)
	}
	// The health gauge is a gauge, not a delta: store the latest values.
	if idx := strings.Index(line, "mux: health "); idx >= 0 {
		var active, evictions, maxLossBp, rttMs, handovers int
		// handovers was appended later: an older gauge line parses four fields
		// and simply leaves it at zero.
		n, _ := fmt.Sscanf(line[idx:], "mux: health active=%d evictions=%d max_loss_bp=%d mean_rtt_ms=%d handovers=%d",
			&active, &evictions, &maxLossBp, &rttMs, &handovers)
		if n >= 4 {
			c.ActiveWorkers.Store(int32(active))
			c.GaugeSeen.Store(true)
			c.Evictions.Store(int32(evictions))
			c.MaxLossBp.Store(int32(maxLossBp))
			c.MeanRTTMs.Store(int32(rttMs))
			if n >= 5 {
				c.Handovers.Store(int32(handovers))
			}
		}
	}
}
