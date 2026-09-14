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
