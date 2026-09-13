package obfs_test

import (
	"context"
	"net"
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
