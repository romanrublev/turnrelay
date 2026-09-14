package vk

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func TestRacingResolverFirstAnswerWins(t *testing.T) {
	var calls atomic.Int32
	r := newRacingResolver([]string{"slow", "dead", "fast"}, func(ctx context.Context, server, host string) ([]net.IP, error) {
		calls.Add(1)
		switch server {
		case "fast":
			return []net.IP{net.ParseIP("95.213.56.1")}, nil
		case "dead":
			return nil, errors.New("timeout")
		default:
			select {
			case <-time.After(2 * time.Second):
				return []net.IP{net.ParseIP("10.0.0.1")}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	})
	start := time.Now()
	ips, err := r.LookupIP(context.Background(), "login.vk.ru")
	if err != nil || len(ips) != 1 || ips[0].String() != "95.213.56.1" {
		t.Fatalf("got %v %v", ips, err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("waited for the slow resolver")
	}
	// The winner returns before the other goroutines have necessarily
	// recorded their call; give them a moment.
	for deadline := time.Now().Add(time.Second); calls.Load() != 3 && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	if calls.Load() != 3 {
		t.Fatalf("all resolvers must be asked in parallel, got %d calls", calls.Load())
	}
	ips2, _ := r.LookupIP(context.Background(), "login.vk.ru")
	if calls.Load() != 3 || ips2[0].String() != "95.213.56.1" {
		t.Fatal("second lookup must be served from cache")
	}
}

func TestRacingResolverAllFail(t *testing.T) {
	r := newRacingResolver([]string{"a", "b"}, func(context.Context, string, string) ([]net.IP, error) {
		return nil, errors.New("nope")
	})
	if _, err := r.LookupIP(context.Background(), "x.example"); err == nil {
		t.Fatal("expected an error when every resolver fails")
	}
}

func TestRacingDialerSkipsLookupForIPs(t *testing.T) {
	r := newRacingResolver([]string{"a"}, func(context.Context, string, string) ([]net.IP, error) {
		panic("lookup must not run for a literal IP")
	})
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	c, err := r.DialContext(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
}
