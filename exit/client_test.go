package exit

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"
)

func newClient(t *testing.T, server net.Addr) *Client {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(pc, server, ClientOptions{Logf: t.Logf})
	t.Cleanup(func() { c.Close() })
	return c
}

func TestClientDialTCP(t *testing.T) {
	echo := tcpEcho(t)
	c := newClient(t, startServer(t, ServerOptions{AllowPrivate: true}))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			conn, err := c.DialContext(ctx, "tcp", M.SocksaddrFromNet(echo))
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			msg := []byte{'m', 's', 'g', byte(i)}
			if _, err := conn.Write(msg); err != nil {
				t.Error(err)
				return
			}
			buf := make([]byte, len(msg))
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != string(msg) {
				t.Errorf("stream %d: %q err %v", i, buf, err)
			}
		}(i)
	}
	wg.Wait()
	if _, err := c.DialContext(ctx, "udp", M.SocksaddrFromNet(echo)); err != ErrNetwork {
		t.Fatalf("udp via DialContext: %v", err)
	}
}

// TestClientSessionAfterCloseErrors guards against session() standing up an
// orphaned KCP/smux pair after Close: once closed, DialContext must fail
// fast with net.ErrClosed instead of creating a session nothing will ever
// tear down.
func TestClientSessionAfterCloseErrors(t *testing.T) {
	echo := tcpEcho(t)
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := startServer(t, ServerOptions{AllowPrivate: true})
	c := NewClient(pc, server, ClientOptions{Logf: t.Logf})
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.DialContext(ctx, "tcp", M.SocksaddrFromNet(echo)); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("DialContext after Close: got %v, want net.ErrClosed", err)
	}
}

func TestClientListenPacketAssociations(t *testing.T) {
	echo := udpEcho(t)
	c := newClient(t, startServer(t, ServerOptions{AllowPrivate: true}))
	ctx := context.Background()
	a, err := c.ListenPacket(ctx, M.Socksaddr{})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := c.ListenPacket(ctx, M.Socksaddr{})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	for _, pc := range []net.PacketConn{a, b} {
		if _, err := pc.WriteTo([]byte("dgram"), echo); err != nil {
			t.Fatal(err)
		}
	}
	for name, pc := range map[string]net.PacketConn{"a": a, "b": b} {
		buf := make([]byte, 64)
		_ = pc.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, from, err := pc.ReadFrom(buf)
		if err != nil || string(buf[:n]) != "dgram" {
			t.Fatalf("%s: %q err %v", name, buf[:n], err)
		}
		if from.String() != echo.String() {
			t.Fatalf("%s: from %s want %s", name, from, echo)
		}
	}
}
