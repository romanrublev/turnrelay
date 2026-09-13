// Package relay opens one TURN allocation and keeps it alive.
package relay

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/pion/logging"
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
}

type Allocation struct {
	client  *turn.Client
	relayed net.PacketConn
	base    io.Closer
}

// connectedUDP makes a connected UDP socket usable as net.PacketConn.
type connectedUDP struct{ *net.UDPConn }

func (c *connectedUDP) WriteTo(b []byte, _ net.Addr) (int, error) { return c.Write(b) }

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
		c, err := net.DialUDP("udp", nil, raddr)
		if err != nil {
			return nil, fmt.Errorf("relay: dial udp: %w", err)
		}
		_ = c.SetReadBuffer(640 * 1024)
		_ = c.SetWriteBuffer(640 * 1024)
		turnConn, base = &connectedUDP{c}, c
	} else {
		var d net.Dialer
		c, err := d.DialContext(ctx, "tcp", raddr.String())
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

func IsQuotaError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "486") || strings.Contains(strings.ToLower(s), "quota")
}

func IsAuthError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	// pion/turn v5.1.1's server sends a bare 400 (Bad Request) with no reason
	// text when a custom AuthHandler rejects credentials (wrong username or
	// integrity check failure), instead of the RFC 5389 401 on the final
	// rejection; the initial challenge round-trip is a 401 too, but that is
	// handled internally by turn.Client and never surfaces here. "400" is
	// included so a bad-username/bad-password Allocate is still classified
	// as an auth error rather than falling through to a generic failure.
	return strings.Contains(s, "401") || strings.Contains(s, "438") || strings.Contains(s, "400") ||
		strings.Contains(s, "unauthorized") || strings.Contains(s, "stale nonce") ||
		strings.Contains(s, "authentication") || strings.Contains(s, "invalid credential")
}
