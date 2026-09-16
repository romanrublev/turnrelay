package exit

import (
	"net"
	"testing"
	"time"
)

func TestFECConnPrunesIdlePeers(t *testing.T) {
	c := &fecConn{
		codec:    newFECCodec(),
		send:     map[string]*fecSendBlock{},
		saddr:    map[string]net.Addr{},
		reasm:    map[string]*blockReassembler{},
		lastSeen: map[string]time.Time{},
		idle:     time.Minute,
	}
	now := time.Unix(1000, 0)
	// An active peer touched just now.
	c.lastSeen["active"] = now
	c.reasm["active"] = newBlockReassembler(c.codec, 128)
	c.saddr["active"] = &net.UDPAddr{}
	// A peer idle well past c.idle, with all its per-peer state.
	c.lastSeen["gone"] = now.Add(-2 * time.Minute)
	c.reasm["gone"] = newBlockReassembler(c.codec, 128)
	c.saddr["gone"] = &net.UDPAddr{}
	c.send["gone"] = &fecSendBlock{}

	c.pruneIdleLocked(now)

	for _, m := range []string{"reasm", "saddr", "lastSeen", "send"} {
		switch m {
		case "reasm":
			if _, ok := c.reasm["gone"]; ok {
				t.Fatal("idle peer reasm not pruned")
			}
		case "saddr":
			if _, ok := c.saddr["gone"]; ok {
				t.Fatal("idle peer saddr not pruned")
			}
		case "lastSeen":
			if _, ok := c.lastSeen["gone"]; ok {
				t.Fatal("idle peer lastSeen not pruned")
			}
		case "send":
			if _, ok := c.send["gone"]; ok {
				t.Fatal("idle peer send not pruned")
			}
		}
	}
	if _, ok := c.reasm["active"]; !ok {
		t.Fatal("active peer state must be kept")
	}
}
