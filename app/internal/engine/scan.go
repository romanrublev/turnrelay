package engine

import (
	"strings"
	"sync/atomic"
)

type Counters struct {
	Workers     atomic.Int32
	HandshakeOK atomic.Bool
}

func scanLine(line string, c *Counters) {
	if strings.Contains(line, "up via") {
		c.Workers.Add(1)
	}
	if strings.Contains(line, "wireguard") && strings.Contains(line, "handshake") &&
		!strings.Contains(line, "initiation") {
		c.HandshakeOK.Store(true)
	}
}
