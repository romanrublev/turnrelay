package exit

import (
	"time"

	"github.com/xtaci/kcp-go/v5"
	"github.com/xtaci/smux"
)

// FEC (forward error correction) shard counts, used identically by both ends
// (they are part of the KCP framing, so client and server MUST match). Reed-
// Solomon over the relayed pipe recovers up to fecParityShards lost packets in
// each block of (fecData+fecParity) without a retransmit, which is the point:
// the VK TURN path shows variable 3-19% loss. Overhead is parity/(data+parity).
const (
	fecDataShards   = 10
	fecParityShards = 3
)

// tuneKCP sets the parameters both ends use over the relayed, striped pipe:
// fast retransmit, generous windows, a conservative MTU so a KCP packet plus
// the discriminator and obfs overhead fits one relayed datagram. These are
// tunables, not protocol constants.
func tuneKCP(s *kcp.UDPSession) {
	s.SetNoDelay(1, 20, 2, 1)
	s.SetWindowSize(1024, 1024)
	s.SetMtu(1196) // pipe wraps each KCP packet with a 1-byte kind + 4-byte seq
	s.SetACKNoDelay(true)
	s.SetStreamMode(true)
}

func smuxConfig() *smux.Config {
	c := smux.DefaultConfig()
	c.KeepAliveInterval = 10 * time.Second
	c.KeepAliveTimeout = 30 * time.Second
	return c
}
