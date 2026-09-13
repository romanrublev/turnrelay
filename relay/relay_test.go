package relay_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/romanrublev/turnrelay/relay"
	"github.com/romanrublev/turnrelay/relay/turntest"
)

func TestAllocateAndEcho(t *testing.T) {
	ts := turntest.Start(t)
	peer, _ := net.ListenPacket("udp4", "127.0.0.1:0")
	defer peer.Close()
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := peer.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = peer.WriteTo(buf[:n], from)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a, err := relay.Allocate(ctx, relay.Options{Server: ts.Addr(), Username: ts.Username, Password: ts.Password, UDP: true})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if ts.Allocations() != 1 {
		t.Fatalf("allocations %d", ts.Allocations())
	}
	rc := a.Relayed()
	if _, err := rc.WriteTo([]byte("ping"), peer.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1500)
	_ = rc.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, from, err := rc.ReadFrom(buf)
	if err != nil || string(buf[:n]) != "ping" {
		t.Fatalf("echo: n=%d err=%v", n, err)
	}
	if from.String() != peer.LocalAddr().String() {
		t.Fatalf("from %s", from)
	}
}

func TestAllocateAuthAndQuotaErrors(t *testing.T) {
	ts := turntest.Start(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := relay.Allocate(ctx, relay.Options{Server: ts.Addr(), Username: "nobody", Password: "x", UDP: true})
	if !relay.IsAuthError(err) {
		t.Fatalf("want auth error, got %v", err)
	}
	ts.SetQuota(0)
	_, err = relay.Allocate(ctx, relay.Options{Server: ts.Addr(), Username: ts.Username, Password: ts.Password, UDP: true})
	if !relay.IsQuotaError(err) {
		t.Fatalf("want quota error, got %v", err)
	}
	if relay.IsQuotaError(errors.New("boom")) || relay.IsAuthError(nil) {
		t.Fatal("classifier false positive")
	}
}

func TestAllocateTCP(t *testing.T) {
	t.Skip("turntest is UDP only; TCP transport is covered by the e2e run against a real relay")
}

func TestRestartKeepsAddress(t *testing.T) {
	ts := turntest.Start(t)
	addr := ts.Addr()

	ts.Restart(t)

	if ts.Addr() != addr {
		t.Fatalf("addr changed: got %s, want %s", ts.Addr(), addr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a, err := relay.Allocate(ctx, relay.Options{Server: ts.Addr(), Username: ts.Username, Password: ts.Password, UDP: true})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if ts.Allocations() != 1 {
		t.Fatalf("allocations %d", ts.Allocations())
	}
}
