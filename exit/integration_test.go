package exit_test

import (
	"context"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"

	"github.com/romanrublev/turnrelay"
	"github.com/romanrublev/turnrelay/exit"
	"github.com/romanrublev/turnrelay/obfs"
	"github.com/romanrublev/turnrelay/relay/turntest"
)

func tcpEcho(t *testing.T) net.Addr {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	return ln.Addr()
}

func udpEcho(t *testing.T) net.Addr {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo(buf[:n], from)
		}
	}()
	return pc.LocalAddr()
}

// stack brings up an exit server behind an in-process TURN relay and a real
// turnrelay.Dialer (static provider) with the given password, and returns an
// exit client over it.
func stack(t *testing.T, serverPassword, clientPassword string, allowPrivate bool) *exit.Client {
	t.Helper()
	ts := turntest.Start(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	inst, err := exit.Listen(ctx, exit.ListenConfig{
		Address: "127.0.0.1:0", Mode: obfs.ModeSRTP, Password: serverPassword,
		Server: exit.ServerOptions{AllowPrivate: allowPrivate}, Logf: t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { inst.Close() })
	d, err := turnrelay.New(turnrelay.Config{
		Provider: "static", TURNServer: ts.Addr(), TURNUsername: ts.Username, TURNPassword: ts.Password,
		Server: netip.MustParseAddrPort(inst.Addr().String()), Connections: 3, Mode: turnrelay.ModeSRTP,
		Password: clientPassword, Logf: t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := d.Start(ctx); err != nil {
		t.Fatal(err)
	}
	pc, err := d.ListenPacket(ctx, M.Socksaddr{})
	if err != nil {
		t.Fatal(err)
	}
	c := exit.NewClient(pc, d.ServerAddr(), exit.ClientOptions{Logf: t.Logf})
	t.Cleanup(func() { c.Close() })
	return c
}

func TestFullStackTCPAndUDP(t *testing.T) {
	c := stack(t, "pw", "pw", true)
	echo := tcpEcho(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	for i := 0; i < 4; i++ {
		conn, err := c.DialContext(ctx, "tcp", M.SocksaddrFromNet(echo))
		if err != nil {
			t.Fatal(err)
		}
		msg := []byte{'t', 'c', 'p', byte(i)}
		if _, err := conn.Write(msg); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, len(msg))
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != string(msg) {
			t.Fatalf("stream %d: %q err %v", i, buf, err)
		}
		conn.Close()
	}
	uecho := udpEcho(t)
	pc, err := c.ListenPacket(ctx, M.Socksaddr{})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	// UDP through the stack is best effort (no retransmit at any hop), so fire
	// steadily and accept the first echo. A single 1/s cadence gives only a
	// handful of attempts and flakes under -race load.
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
			t.Fatalf("no udp echo through the stack: %v", err)
		}
		if string(buf[:n]) == "udp" {
			return
		}
	}
}

func TestFullStackWrongPasswordGetsNothing(t *testing.T) {
	c := stack(t, "pw", "wrong", true)
	echo := tcpEcho(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := c.DialContext(ctx, "tcp", M.SocksaddrFromNet(echo))
	if err != nil {
		return // refused at dial time is also fine
	}
	defer conn.Close()
	_, _ = conn.Write([]byte("ping"))
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if n, err := conn.Read(make([]byte, 16)); err == nil {
		t.Fatalf("wrong password served %d bytes", n)
	}
}

func TestFullStackDeniesPrivateDestination(t *testing.T) {
	c := stack(t, "pw", "pw", false)
	echo := tcpEcho(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := c.DialContext(ctx, "tcp", M.SocksaddrFromNet(echo))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = conn.Write([]byte("ping"))
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if n, err := conn.Read(make([]byte, 16)); err == nil {
		t.Fatalf("loopback destination served %d bytes", n)
	}
}
