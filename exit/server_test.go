package exit

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"
	"github.com/xtaci/kcp-go/v5"
	"github.com/xtaci/smux"
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

// startServer runs an exit server on a loopback UDP socket (no TURN, no
// mux) and returns its address.
func startServer(t *testing.T, o ServerOptions) net.Addr {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(pc, o)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); s.Close() })
	go func() { _ = s.Serve(ctx) }()
	return pc.LocalAddr()
}

// rawClient is a KCP+smux client over a loopback socket, built by hand so
// this test proves the server without depending on exit.Client (Task 6).
func rawClient(t *testing.T, server net.Addr) (*smux.Session, *Demux) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	d := NewDemux(pc)
	t.Cleanup(func() { d.Close() })
	ks, err := kcp.NewConn3(rand.Uint32(), server, nil, 0, 0, newFECConn(d.KCP(), d.LossRate))
	if err != nil {
		t.Fatal(err)
	}
	tuneKCP(ks)
	sess, err := smux.Client(ks, smuxConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess, d
}

// rawUDP wraps a demux's UDP side in the same realtime FEC the server applies,
// so a hand-built client's UDP frames match the server's FEC framing.
func rawUDP(t *testing.T, d *Demux) net.PacketConn {
	t.Helper()
	c := newRealtimeFECConn(d.UDP(), d.LossRate)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestServerTCPStreamsEcho(t *testing.T) {
	echo := tcpEcho(t)
	server := startServer(t, ServerOptions{AllowPrivate: true})
	sess, _ := rawClient(t, server)
	dest := M.SocksaddrFromNet(echo)
	for i := 0; i < 4; i++ {
		st, err := sess.OpenStream()
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		if err := WriteStreamHeader(st, dest); err != nil {
			t.Fatal(err)
		}
		msg := []byte{'p', 'i', 'n', 'g', byte(i)}
		if _, err := st.Write(msg); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 16)
		_ = st.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, err := io.ReadFull(st, buf[:len(msg)])
		if err != nil || string(buf[:n]) != string(msg) {
			t.Fatalf("stream %d: %q err %v", i, buf[:n], err)
		}
	}
}

func TestServerDeniesPrivateByDefault(t *testing.T) {
	echo := tcpEcho(t)
	server := startServer(t, ServerOptions{})
	sess, _ := rawClient(t, server)
	st, err := sess.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := WriteStreamHeader(st, M.SocksaddrFromNet(echo)); err != nil {
		t.Fatal(err)
	}
	_, _ = st.Write([]byte("ping"))
	_ = st.SetReadDeadline(time.Now().Add(3 * time.Second))
	if n, err := st.Read(make([]byte, 16)); err == nil {
		t.Fatalf("loopback destination served %d bytes", n)
	}
}

func TestServerUDPFramesEcho(t *testing.T) {
	echo := udpEcho(t)
	server := startServer(t, ServerOptions{AllowPrivate: true})
	_, d := rawClient(t, server)
	u := rawUDP(t, d)
	frame, err := EncodeUDPFrame(7, M.SocksaddrFromNet(echo), []byte("dgram"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.WriteTo(frame, server); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 128)
	_ = u.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, _, err := u.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	assoc, from, payload, err := DecodeUDPFrame(buf[:n])
	if err != nil || assoc != 7 || string(payload) != "dgram" {
		t.Fatalf("assoc=%d from=%s payload=%q err=%v", assoc, from, payload, err)
	}
	if from.Port != netip.MustParseAddrPort(echo.String()).Port() {
		t.Fatalf("reply source %s, want the echo port", from)
	}
}

// TestServerDeniesFqdnResolvingToLoopback exercises resolveChecked's
// resolve-then-loop branch: an FQDN that resolves to a loopback address
// must be refused by the default (AllowPrivate=false) server just like a
// literal loopback IP is.
func TestServerDeniesFqdnResolvingToLoopback(t *testing.T) {
	echo := tcpEcho(t)
	server := startServer(t, ServerOptions{})
	sess, _ := rawClient(t, server)
	st, err := sess.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	port := netip.MustParseAddrPort(echo.String()).Port()
	dest := M.ParseSocksaddrHostPort("localhost", port)
	if err := WriteStreamHeader(st, dest); err != nil {
		t.Fatal(err)
	}
	_, _ = st.Write([]byte("ping"))
	_ = st.SetReadDeadline(time.Now().Add(3 * time.Second))
	if n, err := st.Read(make([]byte, 16)); err == nil {
		t.Fatalf("fqdn resolving to loopback served %d bytes", n)
	}
}

// TestServerStopsOnCtxCancel checks that cancelling ctx tears down both the
// KCP and the UDP side of Serve, not just the KCP listener.
func TestServerStopsOnCtxCancel(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(pc, ServerOptions{AllowPrivate: true})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	time.Sleep(50 * time.Millisecond) // let Serve start accepting
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return within 2s of ctx cancel")
	}
	t.Cleanup(func() { s.Close() })

	// The UDP side must be down too: a frame sent to the server's address
	// now gets no reply within a short deadline.
	echo := udpEcho(t)
	cpc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	d := NewDemux(cpc)
	u := rawUDP(t, d)
	t.Cleanup(func() { d.Close() })
	frame, err := EncodeUDPFrame(1, M.SocksaddrFromNet(echo), []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.WriteTo(frame, pc.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 128)
	_ = u.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if n, _, err := u.ReadFrom(buf); err == nil {
		t.Fatalf("got %d bytes after Serve stopped on ctx cancel, want nothing", n)
	}
}

// TestServerUDPNoHeadOfLineBlocking: a datagram to a slow-resolving FQDN on
// one association must not stall UDP for a second association pointing at a
// literal IP. With the resolve done off the shared read loop, the fast
// association gets its echo while the slow one is still blocked in DNS.
func TestServerUDPNoHeadOfLineBlocking(t *testing.T) {
	echo := udpEcho(t)
	echoAP := netip.MustParseAddrPort(echo.String())
	release := make(chan struct{})
	resolver := func(ctx context.Context, host string) ([]netip.Addr, error) {
		if host == "slow.invalid" {
			select {
			case <-release:
			case <-ctx.Done():
			}
			return nil, errors.New("slow: gave up")
		}
		return nil, errors.New("unexpected host " + host)
	}
	server := startServer(t, ServerOptions{AllowPrivate: true, Resolver: resolver, DialTimeout: 30 * time.Second})
	_, d := rawClient(t, server)
	u := rawUDP(t, d)

	slowFrame, err := EncodeUDPFrame(1, M.ParseSocksaddrHostPort("slow.invalid", 9999), []byte("slow"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.WriteTo(slowFrame, server); err != nil {
		t.Fatal(err)
	}
	fastFrame, err := EncodeUDPFrame(2, M.SocksaddrFromNet(echo), []byte("fast"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.WriteTo(fastFrame, server); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 128)
	_ = u.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, _, err := u.ReadFrom(buf)
	if err != nil {
		close(release)
		t.Fatalf("fast association got no reply while the slow one blocked: %v", err)
	}
	assoc, from, payload, err := DecodeUDPFrame(buf[:n])
	if err != nil || assoc != 2 || string(payload) != "fast" {
		close(release)
		t.Fatalf("reply assoc=%d payload=%q err=%v", assoc, payload, err)
	}
	if from.Port != echoAP.Port() {
		close(release)
		t.Fatalf("reply source %s, want the echo port", from)
	}
	close(release)
}

// TestServerUDPCachesResolvedDestination: repeated datagrams to the same FQDN
// destination on one association resolve it once, not per datagram.
func TestServerUDPCachesResolvedDestination(t *testing.T) {
	echo := udpEcho(t)
	echoAP := netip.MustParseAddrPort(echo.String())
	var calls atomic.Int32
	resolver := func(ctx context.Context, host string) ([]netip.Addr, error) {
		if host == "echo.test" {
			calls.Add(1)
			return []netip.Addr{echoAP.Addr()}, nil
		}
		return nil, errors.New("unexpected host " + host)
	}
	server := startServer(t, ServerOptions{AllowPrivate: true, Resolver: resolver})
	_, d := rawClient(t, server)
	u := rawUDP(t, d)
	dest := M.ParseSocksaddrHostPort("echo.test", echoAP.Port())
	for i := 0; i < 5; i++ {
		frame, err := EncodeUDPFrame(9, dest, []byte{byte(i)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := u.WriteTo(frame, server); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 64)
		_ = u.SetReadDeadline(time.Now().Add(3 * time.Second))
		n, _, err := u.ReadFrom(buf)
		if err != nil {
			t.Fatalf("i=%d: %v", i, err)
		}
		_, _, payload, err := DecodeUDPFrame(buf[:n])
		if err != nil || len(payload) != 1 || payload[0] != byte(i) {
			t.Fatalf("i=%d payload=%v err=%v", i, payload, err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("resolver called %d times, want 1 (cached)", got)
	}
}
