package exit_test

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/protocol/socks"
	"github.com/sagernet/sing/protocol/socks/socks5"

	"github.com/romanrublev/turnrelay/exit"
)

func TestSOCKS5ConnectThroughStack(t *testing.T) {
	c := stack(t, "pw", "pw", true)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = exit.ServeSOCKS5(ctx, ln, c, t.Logf) }()

	echo := tcpEcho(t)
	conn, err := net.DialTimeout("tcp", ln.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := socks.ClientHandshake5(conn, socks5.CommandConnect, M.SocksaddrFromNet(echo), "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("via-socks")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 9)
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "via-socks" {
		t.Fatalf("%q err %v", buf, err)
	}
}

// netDialer is a minimal N.Dialer over the net package, used to point sing's
// SOCKS client at the local ServeSOCKS5 listener.
type netDialer struct{}

func (netDialer) DialContext(ctx context.Context, network string, dest M.Socksaddr) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, network, dest.String())
}

func (netDialer) ListenPacket(ctx context.Context, dest M.Socksaddr) (net.PacketConn, error) {
	var lc net.ListenConfig
	return lc.ListenPacket(ctx, "udp", ":0")
}

// TestSOCKS5UDPAssociateThroughStack drives a SOCKS5 UDP ASSOCIATE through the
// SOCKS front and the full exit stack: sing's SOCKS client -> ServeSOCKS5 ->
// exit.Client UDP -> exit server -> UDP echo, and back.
func TestSOCKS5UDPAssociateThroughStack(t *testing.T) {
	c := stack(t, "pw", "pw", true)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = exit.ServeSOCKS5(ctx, ln, c, t.Logf) }()

	uecho := udpEcho(t)
	sc := socks.NewClient(netDialer{}, M.SocksaddrFromNet(ln.Addr()), socks.Version5, "", "")
	pc, err := sc.ListenPacket(ctx, M.SocksaddrFromNet(uecho))
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()

	// UDP is best effort; fire steadily and accept the first echo.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		tk := time.NewTicker(100 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
				_, _ = pc.WriteTo([]byte("udp"), uecho)
			}
		}
	}()
	buf := make([]byte, 16)
	_ = pc.SetReadDeadline(time.Now().Add(15 * time.Second))
	for {
		n, _, err := pc.ReadFrom(buf)
		if err != nil {
			t.Fatalf("no udp echo via socks5 associate: %v", err)
		}
		if string(buf[:n]) == "udp" {
			return
		}
	}
}
