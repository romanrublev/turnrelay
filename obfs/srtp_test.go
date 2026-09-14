package obfs_test

import (
	"context"
	"encoding/binary"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/romanrublev/turnrelay/obfs"
	"github.com/romanrublev/turnrelay/obfs/obfstest"
)

func TestSRTPRoundTrip(t *testing.T) {
	roundTrip(t, obfs.ModeSRTP, obfs.Options{}, obfstest.ListenSRTP(t))
}

// TestSRTPHandshakeTimesOut is a regression test: demuxSide/srtpConn used to
// snapshot their deadline channel before blocking in select, so a
// SetReadDeadline call from a concurrent goroutine (which is how pion/dtls
// cancels a blocked handshake read) allocated a fresh channel instead of
// closing the one the parked read already held, and the read never woke up.
func TestSRTPHandshakeTimesOut(t *testing.T) {
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

	w, err := obfs.New(obfs.ModeSRTP, obfs.Options{HandshakeTimeout: 500 * time.Millisecond})
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

// sniffConn records every datagram written to the underlay, so a test can
// look at what the relay would see.
type sniffConn struct {
	net.PacketConn
	mu     sync.Mutex
	writes [][]byte
}

func (s *sniffConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	s.mu.Lock()
	s.writes = append(s.writes, append([]byte(nil), b...))
	s.mu.Unlock()
	return s.PacketConn.WriteTo(b, addr)
}

func (s *sniffConn) rtp() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out [][]byte
	for _, w := range s.writes {
		if len(w) > 0 && w[0] >= 128 && w[0] <= 191 {
			out = append(out, w)
		}
	}
	return out
}

// TestSRTPWireFormat checks section 4.1 of docs/protocol.md against the
// bytes on the underlay: after the handshake each Write is one RTP packet
// with V=2 and no P/X/CC (0x80), M=0 and PT 100 (0x64), 12 header bytes
// plus the payload plus the 10-byte HMAC-SHA1-80 tag, and the sequence
// number increments by exactly one per packet.
func TestSRTPWireFormat(t *testing.T) {
	srv := obfstest.ListenSRTP(t)
	w, err := obfs.New(obfs.ModeSRTP, obfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	underlay := &sniffConn{PacketConn: raw}
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
	if n := len(underlay.rtp()); n != 0 {
		t.Fatalf("%d RTP packets on the wire before any Write", n)
	}
	payloads := []string{"first payload", "second"}
	for _, p := range payloads {
		if _, err := conn.Write([]byte(p)); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 2048)
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		if _, err := conn.Read(buf); err != nil {
			t.Fatal(err)
		}
	}
	pkts := underlay.rtp()
	if len(pkts) != len(payloads) {
		t.Fatalf("%d RTP packets on the wire, want %d", len(pkts), len(payloads))
	}
	var prevSeq uint16
	for i, pkt := range pkts {
		if pkt[0] != 0x80 {
			t.Fatalf("packet %d: first byte %#x, want 0x80", i, pkt[0])
		}
		if pkt[1] != 100 {
			t.Fatalf("packet %d: second byte %d, want payload type 100", i, pkt[1])
		}
		if want := len(payloads[i]) + 22; len(pkt) != want {
			t.Fatalf("packet %d: %d bytes on the wire, want payload+22 = %d", i, len(pkt), want)
		}
		seq := binary.BigEndian.Uint16(pkt[2:4])
		if i > 0 && seq != prevSeq+1 {
			t.Fatalf("packet %d: sequence %d, want %d", i, seq, prevSeq+1)
		}
		prevSeq = seq
	}
}

// TestSRTPRandomInitialSequence: two connections must not start from the
// same sequence number and timestamp (RFC 3550 wants both random).
func TestSRTPRandomInitialSequence(t *testing.T) {
	srv := obfstest.ListenSRTP(t)
	w, err := obfs.New(obfs.ModeSRTP, obfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() {
		for {
			c, err := srv.Accept(ctx)
			if err != nil {
				return
			}
			go echoDatagrams(c)
		}
	}()
	seen := map[[6]byte]bool{}
	for i := 0; i < 3; i++ {
		raw, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		underlay := &sniffConn{PacketConn: raw}
		conn, err := w.Client(ctx, underlay, srv.Addr())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
		pkts := underlay.rtp()
		if len(pkts) != 1 {
			t.Fatalf("conn %d: %d RTP packets, want 1", i, len(pkts))
		}
		var k [6]byte
		copy(k[:], pkts[0][2:8]) // sequence number and timestamp
		if seen[k] {
			t.Fatalf("conn %d repeated initial seq/ts %x", i, k)
		}
		if k == ([6]byte{}) {
			t.Fatalf("conn %d started at seq 0 and ts 0", i)
		}
		seen[k] = true
		_ = conn.Close()
	}
}
