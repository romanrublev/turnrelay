package relay_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/pion/stun/v4"
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

// turnErr builds a *stun.TurnError with the given numeric code, wrapped the
// same way Allocate wraps errors from turn.Client (fmt.Errorf with %w), so
// errors.As has to unwrap it just like it would for a real classifier call.
func turnErr(code stun.ErrorCode) error {
	// Mirror how turn.Client.AllocateWithContext actually returns this: as
	// a plain error interface value (relay.Allocate then wraps whatever it
	// gets back with %w without ever seeing the concrete *stun.TurnError
	// type), so the static type at this call site matches production.
	var err error = &stun.TurnError{
		StunMessageType: stun.NewType(stun.MethodAllocate, stun.ClassErrorResponse),
		ErrorCodeAttr:   stun.ErrorCodeAttribute{Code: code},
	}
	return fmt.Errorf("relay: allocate: %w", err)
}

func TestClassifiers(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		wantQuota bool
		wantAuth  bool
	}{
		{"typed 486 is quota", turnErr(486), true, false},
		{"typed 401 is auth", turnErr(401), false, true},
		{"typed 438 is auth", turnErr(438), false, true},
		{"typed 400 is auth", turnErr(400), false, true},
		{
			"untyped error with 400 in the address is neither",
			errors.New("relay: dial udp: 10.0.0.1:3400: connection refused"),
			false, false,
		},
		{
			"untyped quota response text still matches via the word quota",
			errors.New("Allocate error response (error 486: Allocation Quota Reached)"),
			true, false,
		},
		{"nil is neither", nil, false, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := relay.IsQuotaError(c.err); got != c.wantQuota {
				t.Errorf("IsQuotaError(%v) = %v, want %v", c.err, got, c.wantQuota)
			}
			if got := relay.IsAuthError(c.err); got != c.wantAuth {
				t.Errorf("IsAuthError(%v) = %v, want %v", c.err, got, c.wantAuth)
			}
		})
	}
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
