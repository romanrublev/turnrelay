package obfs_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/romanrublev/turnrelay/obfs"
)

func TestListenAllModes(t *testing.T) {
	key, err := obfs.DeriveWrapKey("pw")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		mode obfs.Mode
		lo   obfs.ListenOptions
		co   obfs.Options
	}{
		{obfs.ModeSRTP, obfs.ListenOptions{}, obfs.Options{}},
		{obfs.ModeDTLS, obfs.ListenOptions{}, obfs.Options{}},
		{obfs.ModeWrap, obfs.ListenOptions{WrapKey: key}, obfs.Options{WrapKey: key}},
	} {
		t.Run(string(tc.mode), func(t *testing.T) {
			l, err := obfs.Listen(tc.mode, "127.0.0.1:0", tc.lo)
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			w, err := obfs.New(tc.mode, tc.co)
			if err != nil {
				t.Fatal(err)
			}
			underlay, err := net.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cc, err := w.Client(ctx, underlay, l.Addr())
			if err != nil {
				t.Fatal(err)
			}
			defer cc.Close()
			sc, err := l.Accept(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer sc.Close()
			if _, err := cc.Write([]byte("ping")); err != nil {
				t.Fatal(err)
			}
			buf := make([]byte, 16)
			_ = sc.SetReadDeadline(time.Now().Add(5 * time.Second))
			n, err := sc.Read(buf)
			if err != nil || string(buf[:n]) != "ping" {
				t.Fatalf("server read %q err %v", buf[:n], err)
			}
			if _, err := sc.Write([]byte("pong")); err != nil {
				t.Fatal(err)
			}
			_ = cc.SetReadDeadline(time.Now().Add(5 * time.Second))
			n, err = cc.Read(buf)
			if err != nil || string(buf[:n]) != "pong" {
				t.Fatalf("client read %q err %v", buf[:n], err)
			}
		})
	}
}
