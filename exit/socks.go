package exit

import (
	std_bufio "bufio"
	"context"
	"net"
	"time"

	"github.com/sagernet/sing/common/bufio"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/protocol/socks"
)

// ServeSOCKS5 accepts SOCKS5 clients on ln (no authentication; bind it to
// loopback) and proxies CONNECT through Client.DialContext and UDP ASSOCIATE
// through Client.ListenPacket. It returns when ln is closed or ctx ends.
func ServeSOCKS5(ctx context.Context, ln net.Listener, c *Client, logf func(string, ...any)) error {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	h := &socksHandler{c: c, logf: logf}
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go func() {
			source := M.SocksaddrFromNet(conn.RemoteAddr())
			if err := socks.HandleConnectionEx(ctx, conn, std_bufio.NewReader(conn), nil, h, packetListener{}, 60*time.Second, source, nil); err != nil {
				logf("socks: %s: %v", source, err)
			}
		}()
	}
}

type socksHandler struct {
	c    *Client
	logf func(string, ...any)
}

func (h *socksHandler) NewConnectionEx(ctx context.Context, conn net.Conn, source, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	rc, err := h.c.DialContext(ctx, "tcp", destination)
	if err != nil {
		_ = N.ReportHandshakeFailure(conn, err)
		_ = conn.Close()
		if onClose != nil {
			onClose(err)
		}
		return
	}
	// The SOCKS5 reply is written lazily on the first Read or Write of conn.
	// bufio.CopyConn below drives both directions concurrently, so without
	// this explicit, synchronous handshake it races to flip that lazy state
	// from two goroutines at once. Writing it here, before either goroutine
	// starts, makes the flip happen-before both.
	if err = N.ReportConnHandshakeSuccess(conn, rc); err != nil {
		_ = conn.Close()
		_ = rc.Close()
		if onClose != nil {
			onClose(err)
		}
		return
	}
	err = bufio.CopyConn(ctx, conn, rc)
	if onClose != nil {
		onClose(err)
	}
}

func (h *socksHandler) NewPacketConnectionEx(ctx context.Context, conn N.PacketConn, source, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	pc, err := h.c.ListenPacket(ctx, destination)
	if err != nil {
		_ = conn.Close()
		if onClose != nil {
			onClose(err)
		}
		return
	}
	err = bufio.CopyPacketConn(ctx, conn, bufio.NewPacketConn(pc))
	if onClose != nil {
		onClose(err)
	}
}

// packetListener binds the local UDP socket a SOCKS5 UDP ASSOCIATE client
// sends to.
type packetListener struct{}

func (packetListener) ListenPacket(lc net.ListenConfig, ctx context.Context, network, address string) (net.PacketConn, error) {
	return lc.ListenPacket(ctx, network, address)
}
