package singbox_test

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/romanrublev/turnrelay/mux"
	"github.com/romanrublev/turnrelay/obfs/obfstest"
	"github.com/romanrublev/turnrelay/relay/turntest"
	turnrelaybox "github.com/romanrublev/turnrelay/singbox"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// echoVPS accepts obfs conns and echoes payload, dropping hellos, like the
// core dialer_test.go helper.
func echoVPS(t *testing.T) *obfstest.Server {
	srv := obfstest.ListenSRTP(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		for {
			c, err := srv.Accept(ctx)
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				buf := make([]byte, 2048)
				for {
					n, err := c.Read(buf)
					if err != nil {
						return
					}
					if _, ok := mux.ParseHello(buf[:n]); ok {
						continue
					}
					if _, err := c.Write(buf[:n]); err != nil {
						return
					}
				}
			}()
		}
	}()
	return srv
}

func TestRegisterAndDialThroughOutbound(t *testing.T) {
	ts := turntest.Start(t)
	vps := echoVPS(t)
	serverAP := netip.MustParseAddrPort(vps.Addr().String())

	registry := outbound.NewRegistry()
	turnrelaybox.RegisterOutbound(registry)

	options := turnrelaybox.TurnrelayOutboundOptions{
		ServerOptions: option.ServerOptions{Server: serverAP.Addr().String(), ServerPort: serverAP.Port()},
		Provider:      "static",
		TURNServer:    ts.Addr(),
		TURNUsername:  ts.Username,
		TURNPassword:  ts.Password,
		Connections:   3,
		Mode:          "srtp",
	}
	ctx := context.Background()
	ob, err := registry.CreateOutbound(ctx, nil, log.NewNOPFactory().Logger(), "relay", turnrelaybox.TypeTurnrelay, &options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if c, ok := ob.(interface{ Close() error }); ok {
			_ = c.Close()
		}
	})
	if ob.Type() != turnrelaybox.TypeTurnrelay {
		t.Fatalf("type %q", ob.Type())
	}
	if got := ob.Network(); len(got) != 1 || got[0] != N.NetworkUDP {
		t.Fatalf("network %v", got)
	}

	// Start the pool (the outbound implements the sing-box lifecycle).
	if err := ob.(interface {
		Start(stage adapter.StartStage) error
	}).Start(adapter.StartStateStart); err != nil {
		t.Fatal(err)
	}

	// Datagram round trip through the outbound.
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	c, err := ob.DialContext(dialCtx, "udp", M.ParseSocksaddr("203.0.113.5:56004"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	deadline := time.Now().Add(10 * time.Second)
	var ok bool
	for time.Now().Before(deadline) && !ok {
		msg := []byte("ping")
		if _, err := c.Write(msg); err != nil {
			t.Fatal(err)
		}
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 64)
		n, err := c.Read(buf)
		if err == nil && string(buf[:n]) == "ping" {
			ok = true
		}
	}
	if !ok {
		t.Fatal("no echo through the outbound within the deadline")
	}

	// TCP is refused.
	if _, err := ob.DialContext(dialCtx, "tcp", M.ParseSocksaddr("1.1.1.1:443")); err == nil {
		t.Fatal("tcp should be refused")
	}
}
