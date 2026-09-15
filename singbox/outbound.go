package singbox

import (
	"context"
	"fmt"
	"net"
	"sync"

	"github.com/romanrublev/turnrelay"
	"github.com/romanrublev/turnrelay/exit"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/dialer"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

var (
	_ adapter.Outbound = (*Outbound)(nil)
)

// Outbound is a sing-box outbound that wraps the core turnrelay.Dialer. In
// the default (wireguard) server type it presents N TURN allocations as one
// UDP-only datagram pipe, used as the wireguard endpoint's detour. In exit
// server type it is a normal TCP+UDP proxy outbound backed by an exit.Client
// over that same datagram pipe, with no WireGuard involved.
type Outbound struct {
	outbound.Adapter
	ctx    context.Context
	logger log.ContextLogger
	dialer *turnrelay.Dialer
	exit   bool
	mu     sync.Mutex
	client *exit.Client // exit mode only, created in Start
}

// NewOutbound builds the outbound from its JSON options. The credential pool
// is created here but only started in Start.
func NewOutbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options TurnrelayOutboundOptions) (adapter.Outbound, error) {
	serverType := options.ServerType
	if serverType == "" {
		serverType = ServerTypeWireGuard
	}
	var isExit bool
	switch serverType {
	case ServerTypeWireGuard:
	case ServerTypeExit:
		if options.Password == "" {
			return nil, E.New("turnrelay: server_type exit requires a password")
		}
		isExit = true
	default:
		return nil, E.New("turnrelay: unknown server_type ", serverType)
	}
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
	networks := []string{N.NetworkUDP}
	if isExit {
		networks = []string{N.NetworkTCP, N.NetworkUDP}
	}
	return &Outbound{
		Adapter: outbound.NewAdapterWithDialerOptions(TypeTurnrelay, tag, networks, options.DialerOptions),
		ctx:     ctx,
		logger:  logger,
		dialer:  d,
		exit:    isExit,
	}, nil
}

// dialHook wires the core library's socket dialer to sing-box's, so that every
// socket the library opens (the VK API and the TURN relays) goes through
// sing-box's dialer. This is what makes route.auto_detect_interface (and any
// detour / bind_interface) apply to those sockets, so they bind to the physical
// interface and bypass sing-box's own tun instead of looping back into it.
//
// It is built whenever a network manager is present in the context, i.e. when
// running under a real box. dialer.New reads runtime managers from the context
// and cannot run without them, so a bare unit test context (no network manager)
// gets a nil hook and the core library dials directly.
func dialHook(ctx context.Context, options option.DialerOptions) (func(context.Context, string, string) (net.Conn, error), error) {
	if service.FromContext[adapter.NetworkManager](ctx) == nil {
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
// In exit mode it also opens the datagram pipe to the exit server and wraps
// it in an exit.Client.
func (o *Outbound) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	if err := o.dialer.Start(o.ctx); err != nil {
		return err
	}
	if !o.exit {
		return nil
	}
	pc, err := o.dialer.ListenPacket(o.ctx, M.Socksaddr{})
	if err != nil {
		return err
	}
	logf := func(string, ...any) {}
	if o.logger != nil {
		logf = func(format string, args ...any) { o.logger.Debug(fmt.Sprintf(format, args...)) }
	}
	o.mu.Lock()
	o.client = exit.NewClient(pc, o.dialer.ServerAddr(), exit.ClientOptions{Logf: logf})
	o.mu.Unlock()
	return nil
}

func (o *Outbound) Close() error {
	o.mu.Lock()
	c := o.client
	o.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
	return o.dialer.Close()
}

func (o *Outbound) exitClient() (*exit.Client, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.client == nil {
		return nil, E.New("turnrelay: outbound not started")
	}
	return o.client, nil
}

func (o *Outbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	if !o.exit {
		return o.dialer.DialContext(ctx, network, destination)
	}
	c, err := o.exitClient()
	if err != nil {
		return nil, err
	}
	return c.DialContext(ctx, network, destination)
}

func (o *Outbound) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	if !o.exit {
		return o.dialer.ListenPacket(ctx, destination)
	}
	c, err := o.exitClient()
	if err != nil {
		return nil, err
	}
	return c.ListenPacket(ctx, destination)
}
