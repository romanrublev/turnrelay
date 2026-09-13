package obfs_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/romanrublev/turnrelay/obfs"
	"github.com/romanrublev/turnrelay/obfs/obfstest"
)

func TestWrapRoundTrip(t *testing.T) {
	key, _ := obfs.DeriveWrapKey("tunnel-secret")
	roundTrip(t, obfs.ModeWrap, obfs.Options{Password: "tunnel-secret"}, obfstest.ListenWrap(t, key))
}

func TestWrapWrongPasswordTimesOut(t *testing.T) {
	key, _ := obfs.DeriveWrapKey("right")
	srv := obfstest.ListenWrap(t, key)
	w, _ := obfs.New(obfs.ModeWrap, obfs.Options{Password: "wrong", HandshakeTimeout: 2 * time.Second})
	underlay, _ := net.ListenPacket("udp", "127.0.0.1:0")
	_, err := w.Client(context.Background(), underlay, srv.Addr())
	if err == nil {
		t.Fatal("handshake with wrong password succeeded")
	}
}
