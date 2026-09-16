package exit

import (
	"time"

	"github.com/xtaci/kcp-go/v5"
	"github.com/xtaci/smux"
)

// FEC is done by our own adaptive block layer (fecConn), not kcp-go, so the
// KCP sessions are created with 0/0 shards. See fecconn.go / fecblock.go.

// tuneKCP sets the parameters both ends use over the relayed, striped pipe:
// fast retransmit, generous windows, a conservative MTU so a KCP packet plus
// the discriminator and obfs overhead fits one relayed datagram. These are
// tunables, not protocol constants.
func tuneKCP(s *kcp.UDPSession) {
	s.SetNoDelay(1, 20, 2, 1)
	s.SetWindowSize(1024, 1024)
	// The pipe wraps each KCP packet: demux kind(1)+seq(4) and the FEC frame
	// header(7)+shard length prefix(2) = 14 bytes over the KCP payload.
	s.SetMtu(1187)
	s.SetACKNoDelay(true)
	s.SetStreamMode(true)
}

func smuxConfig() *smux.Config {
	c := smux.DefaultConfig()
	c.KeepAliveInterval = 10 * time.Second
	c.KeepAliveTimeout = 30 * time.Second
	return c
}
