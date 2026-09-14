package singbox

import (
	"context"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/log"
)

// TypeTurnrelay is the outbound `type` string in a sing-box config.
const TypeTurnrelay = "turnrelay"

// RegisterOutbound adds the turnrelay outbound to a sing-box outbound
// registry. A custom sing-box build calls this on the registry it feeds to
// box.Context. See the README.
func RegisterOutbound(registry *outbound.Registry) {
	outbound.Register[TurnrelayOutboundOptions](registry, TypeTurnrelay,
		func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options TurnrelayOutboundOptions) (adapter.Outbound, error) {
			return NewOutbound(ctx, router, logger, tag, options)
		})
}
