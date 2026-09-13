package obfs_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/romanrublev/turnrelay/obfs"
	"github.com/romanrublev/turnrelay/obfs/obfstest"
)

// echoDatagrams copies every datagram back, used by all wrapper tests.
func echoDatagrams(c net.Conn) {
	buf := make([]byte, 2048)
	for {
		n, err := c.Read(buf)
		if err != nil {
			return
		}
		if _, err := c.Write(buf[:n]); err != nil {
			return
		}
	}
}

func roundTrip(t *testing.T, mode obfs.Mode, o obfs.Options, srv *obfstest.Server) {
	t.Helper()
	w, err := obfs.New(mode, o)
	if err != nil {
		t.Fatal(err)
	}
	underlay, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() {
		c, err := srv.Accept(ctx)
		if err == nil {
			echoDatagrams(c)
		}
	}()
	conn, err := w.Client(ctx, underlay, srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for i, msg := range []string{"one", "two", string(make([]byte, 1200))} {
		if _, err := conn.Write([]byte(msg)); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 2048)
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if string(buf[:n]) != msg {
			t.Fatalf("datagram %d mismatch: %d bytes", i, n)
		}
	}
}

func TestDTLSRoundTrip(t *testing.T) {
	roundTrip(t, obfs.ModeDTLS, obfs.Options{}, obfstest.ListenDTLS(t))
}

// TestDTLSHandshakeTimesOut is the plain-DTLS counterpart of
// TestSRTPHandshakeTimesOut: dtlsWrapper shares dtlsHandshake with srtpWrapper,
// so it is worth pinning down that the real net.PacketConn path (whose
// SetReadDeadline is handled natively by the OS, not by obfs) already times
// out correctly against a silent peer.
func TestDTLSHandshakeTimesOut(t *testing.T) {
	underlay, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer underlay.Close()
	silent, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer silent.Close()

	w, err := obfs.New(obfs.ModeDTLS, obfs.Options{HandshakeTimeout: 500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := w.Client(context.Background(), underlay, silent.LocalAddr())
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("handshake did not time out")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("handshake did not time out")
	}
}

func TestNewRejectsUnknownMode(t *testing.T) {
	if _, err := obfs.New("plain", obfs.Options{}); err == nil {
		t.Fatal("unknown mode accepted")
	}
	if _, err := obfs.New(obfs.ModeWrap, obfs.Options{}); err == nil {
		t.Fatal("wrap without key accepted")
	}
}
