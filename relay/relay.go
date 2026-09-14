// Package relay opens one TURN allocation and keeps it alive.
package relay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/pion/logging"
	"github.com/pion/stun/v4"
	"github.com/pion/transport/v4/stdnet"
	"github.com/pion/turn/v5"
)

type Options struct {
	Server     string // host:port of the TURN relay
	Username   string
	Password   string
	UDP        bool // true: UDP transport (default and recommended); false: TCP
	PeerIsIPv6 bool // request an IPv6 relayed address
	Logger     logging.LoggerFactory
	// DialContext opens the socket towards the relay. nil uses the net
	// package directly. It is called once per allocation with network "udp"
	// (or "tcp" when UDP is false) and the resolved relay host:port; a UDP
	// conn must be connected to that address, since it is driven as a
	// PacketConn whose every write goes to the relay. Socket buffers are
	// enlarged only when the returned conn is a *net.UDPConn. This is the
	// hook a sing-box outbound uses for detour and bind_interface.
	DialContext func(ctx context.Context, network, address string) (net.Conn, error)
}

type Allocation struct {
	client  *turn.Client
	relayed net.PacketConn
	base    io.Closer
}

// connectedUDP makes a connected UDP socket usable as net.PacketConn.
type connectedUDP struct{ *net.UDPConn }

func (c *connectedUDP) WriteTo(b []byte, _ net.Addr) (int, error) { return c.Write(b) }

// connectedConn does the same for any connected datagram net.Conn that a
// custom DialContext returns: reads are attributed to the relay, writes
// ignore the destination.
type connectedConn struct {
	net.Conn
	peer net.Addr
}

func (c *connectedConn) ReadFrom(b []byte) (int, net.Addr, error) {
	n, err := c.Conn.Read(b)
	return n, c.peer, err
}

func (c *connectedConn) WriteTo(b []byte, _ net.Addr) (int, error) { return c.Conn.Write(b) }

// dialUDP opens the connected UDP socket to raddr, through o.DialContext
// when set.
func dialUDP(ctx context.Context, o Options, raddr *net.UDPAddr) (net.PacketConn, io.Closer, error) {
	if o.DialContext == nil {
		c, err := net.DialUDP("udp", nil, raddr)
		if err != nil {
			return nil, nil, err
		}
		setBuffers(c)
		return &connectedUDP{c}, c, nil
	}
	c, err := o.DialContext(ctx, "udp", raddr.String())
	if err != nil {
		return nil, nil, err
	}
	if uc, ok := c.(*net.UDPConn); ok {
		setBuffers(uc)
		return &connectedUDP{uc}, uc, nil
	}
	return &connectedConn{Conn: c, peer: raddr}, c, nil
}

func dialTCP(ctx context.Context, o Options, address string) (net.Conn, error) {
	if o.DialContext == nil {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", address)
	}
	return o.DialContext(ctx, "tcp", address)
}

func setBuffers(c *net.UDPConn) {
	_ = c.SetReadBuffer(640 * 1024)
	_ = c.SetWriteBuffer(640 * 1024)
}

func Allocate(ctx context.Context, o Options) (*Allocation, error) {
	if o.Logger == nil {
		f := logging.NewDefaultLoggerFactory()
		f.DefaultLogLevel = logging.LogLevelWarn
		o.Logger = f
	}
	raddr, err := net.ResolveUDPAddr("udp", o.Server)
	if err != nil {
		return nil, fmt.Errorf("relay: resolve %s: %w", o.Server, err)
	}
	var (
		turnConn net.PacketConn
		base     io.Closer
	)
	if o.UDP {
		turnConn, base, err = dialUDP(ctx, o, raddr)
		if err != nil {
			return nil, fmt.Errorf("relay: dial udp: %w", err)
		}
	} else {
		c, err := dialTCP(ctx, o, raddr.String())
		if err != nil {
			return nil, fmt.Errorf("relay: dial tcp: %w", err)
		}
		turnConn, base = turn.NewSTUNConn(c), c
	}
	family := turn.RequestedAddressFamilyIPv4
	if o.PeerIsIPv6 {
		family = turn.RequestedAddressFamilyIPv6
	}
	client, err := turn.NewClient(&turn.ClientConfig{
		STUNServerAddr:         raddr.String(),
		TURNServerAddr:         raddr.String(),
		Conn:                   turnConn,
		Net:                    new(stdnet.Net), // zero value: no interface enumeration (Huawei ROMs deny NETLINK)
		Username:               o.Username,
		Password:               o.Password,
		RequestedAddressFamily: family,
		LoggerFactory:          o.Logger,
	})
	if err != nil {
		_ = base.Close()
		return nil, fmt.Errorf("relay: client: %w", err)
	}
	if err := client.Listen(); err != nil {
		client.Close()
		_ = base.Close()
		return nil, fmt.Errorf("relay: listen: %w", err)
	}
	relayed, err := client.AllocateWithContext(ctx)
	if err != nil {
		client.Close()
		_ = base.Close()
		return nil, fmt.Errorf("relay: allocate: %w", err)
	}
	return &Allocation{client: client, relayed: relayed, base: base}, nil
}

func (a *Allocation) Relayed() net.PacketConn { return a.relayed }
func (a *Allocation) RelayedAddr() net.Addr   { return a.relayed.LocalAddr() }

func (a *Allocation) Close() error {
	err := a.relayed.Close()
	a.client.Close()
	_ = a.base.Close()
	return err
}

// Keepalive sends STUN Binding requests until ctx ends; VK relays drop
// silent allocations well before the 10 minute lifetime.
func (a *Allocation) Keepalive(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_, _ = a.client.SendBindingRequest()
		}
	}
}

// turnCode extracts the STUN/TURN numeric error code from err, if err (or
// something it wraps) is a *stun.TurnError. turn.Client's AllocateWithContext
// returns this type directly for any TURN error response (see pion/turn v5.1.1
// client.go's sendAllocateRequest), and Allocate's fmt.Errorf("...: %w", err)
// wrapping keeps it reachable via errors.As.
func turnCode(err error) (int, bool) {
	var te *stun.TurnError
	if errors.As(err, &te) {
		return int(te.ErrorCodeAttr.Code), true
	}
	return 0, false
}

func IsQuotaError(err error) bool {
	if err == nil {
		return false
	}
	if c, ok := turnCode(err); ok {
		return c == 486
	}
	return strings.Contains(strings.ToLower(err.Error()), "quota")
}

func IsAuthError(err error) bool {
	if err == nil {
		return false
	}
	if c, ok := turnCode(err); ok {
		// 400 is only accepted here as a *typed* TURN error code: pion/turn
		// v5.1.1's server answers a rejected custom AuthHandler (wrong
		// username, or a message-integrity/password mismatch) with a bare
		// 400 rather than the RFC 5389 401; coturn and VK's relays answer
		// 401. Requiring the typed code (rather than a bare "400" substring
		// match against err.Error()) keeps this from misclassifying
		// unrelated failures whose message happens to contain "400", such
		// as a dial error against a host:port ending in 400.
		return c == 401 || c == 438 || c == 400
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "unauthorized") || strings.Contains(s, "stale nonce") ||
		strings.Contains(s, "invalid credential")
}
