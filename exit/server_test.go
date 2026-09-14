package exit

import (
	"context"
	"io"
	"math/rand/v2"
	"net"
	"net/netip"
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
	ks, err := kcp.NewConn3(rand.Uint32(), server, nil, 0, 0, d.KCP())
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
	frame, err := EncodeUDPFrame(7, M.SocksaddrFromNet(echo), []byte("dgram"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.UDP().WriteTo(frame, server); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 128)
	_ = d.UDP().SetReadDeadline(time.Now().Add(5 * time.Second))
	n, _, err := d.UDP().ReadFrom(buf)
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
