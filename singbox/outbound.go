package singbox

import (
	"context"
	"fmt"
	"net"
	"reflect"

	"github.com/romanrublev/turnrelay"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/dialer"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

var (
	_ adapter.Outbound = (*Outbound)(nil)
)

// Outbound is a UDP-only sing-box outbound that presents N TURN allocations as
// one datagram pipe. It wraps the core turnrelay.Dialer; the wireguard
// endpoint uses it through detour.
type Outbound struct {
	outbound.Adapter
	ctx    context.Context
	dialer *turnrelay.Dialer
}

// NewOutbound builds the outbound from its JSON options. The credential pool
// is created here but only started in Start.
func NewOutbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options TurnrelayOutboundOptions) (adapter.Outbound, error) {
	cfg, err := options.toConfig()
	if err != nil {
		return nil, err
	}
	hook, err := dialHook(ctx, options.DialerOptions)
	if err != nil {
		return nil, err
	}
	cfg.DialContext = hook
	if logger != nil {
		cfg.Logf = func(format string, args ...any) { logger.Debug(fmt.Sprintf(format, args...)) }
	}
	d, err := turnrelay.New(cfg)
	if err != nil {
		return nil, err
	}
	return &Outbound{
		Adapter: outbound.NewAdapterWithDialerOptions(TypeTurnrelay, tag, []string{N.NetworkUDP}, options.DialerOptions),
		ctx:     ctx,
		dialer:  d,
	}, nil
}

// dialHook wires the core library's socket dialer to sing-box's, so detour and
// bind_interface apply to the sockets towards the VK API and the relay. It is
// built only when DialerOptions is set: dialer.New reads runtime managers from
// the context and cannot run on a context without a box, and an unset
// DialerOptions has nothing to apply, so the core library dials directly.
func dialHook(ctx context.Context, options option.DialerOptions) (func(context.Context, string, string) (net.Conn, error), error) {
	if reflect.DeepEqual(options, option.DialerOptions{}) {
		return nil, nil
	}
	sbDialer, err := dialer.New(ctx, options, true)
	if err != nil {
		return nil, err
	}
	return func(c context.Context, network, address string) (net.Conn, error) {
		return sbDialer.DialContext(c, network, M.ParseSocksaddr(address))
	}, nil
}

// Start starts the credential and worker pools once the box reaches the start
// stage. The wireguard endpoint depends on this outbound (via detour), so
// sing-box starts it first and the pool is filling before the endpoint binds.
func (o *Outbound) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	return o.dialer.Start(o.ctx)
}

func (o *Outbound) Close() error {
	return o.dialer.Close()
}

func (o *Outbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	return o.dialer.DialContext(ctx, network, destination)
}

func (o *Outbound) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	return o.dialer.ListenPacket(ctx, destination)
}
