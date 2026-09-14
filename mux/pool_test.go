package mux_test

import (
	"bytes"
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/romanrublev/turnrelay/credpool"
	"github.com/romanrublev/turnrelay/mux"
	"github.com/romanrublev/turnrelay/obfs"
	"github.com/romanrublev/turnrelay/obfs/obfstest"
	"github.com/romanrublev/turnrelay/provider"
	"github.com/romanrublev/turnrelay/relay/turntest"
)

// fakeVPS accepts obfs conns and behaves like anton48's -srtp server:
// hellos are consumed and recorded, probes echoed, payload echoed.
type fakeVPS struct {
	srv    *obfstest.Server
	mu     sync.Mutex
	hellos map[[16]byte]int
	conns  int
}

func newFakeVPS(t *testing.T) *fakeVPS {
	v := &fakeVPS{srv: obfstest.ListenSRTP(t), hellos: map[[16]byte]int{}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		for {
			c, err := v.srv.Accept(ctx)
			if err != nil {
				return
			}
			v.mu.Lock()
			v.conns++
			v.mu.Unlock()
			go v.serve(c)
		}
	}()
	return v
}

func (v *fakeVPS) serve(c net.Conn) {
	defer c.Close()
	buf := make([]byte, 2048)
	for {
		n, err := c.Read(buf)
		if err != nil {
			return
		}
		if id, ok := mux.ParseHello(buf[:n]); ok {
			v.mu.Lock()
			v.hellos[id]++
			v.mu.Unlock()
			continue
		}
		if _, err := c.Write(buf[:n]); err != nil { // probes and payload alike
			return
		}
	}
}

func staticPool(ts *turntest.Server) *credpool.Pool {
	return credpool.New(func(context.Context, string) (provider.Credential, error) {
		return provider.Credential{Username: ts.Username, Password: ts.Password, Relays: []string{ts.Addr()}, Link: "L"}, nil
	}, credpool.Options{Links: []string{"L"}, ConnsPerSlot: 10, CooldownMin: time.Millisecond, CooldownMax: 2 * time.Millisecond})
}

func newPool(t *testing.T, workers int) (*mux.Pool, *fakeVPS, *turntest.Server) {
	ts := turntest.Start(t)
	vps := newFakeVPS(t)
	w, _ := obfs.New(obfs.ModeSRTP, obfs.Options{HandshakeTimeout: 5 * time.Second})
	p := mux.New(mux.Options{
		Workers: workers, Peer: vps.srv.Addr(), Wrapper: w, Creds: staticPool(ts), TURNUDP: true,
		ProbeInterval: 200 * time.Millisecond, ZombieAfter: 2 * time.Second, StartPacing: 10 * time.Millisecond,
		BackoffMin: 50 * time.Millisecond, BackoffMax: 200 * time.Millisecond,
		Logf: t.Logf,
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); p.Close() })
	p.Start(ctx)
	return p, vps, ts
}

func TestPoolEchoAcrossWorkers(t *testing.T) {
	p, vps, ts := newPool(t, 4)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := p.WaitReady(ctx, 4); err != nil {
		t.Fatal(err)
	}
	if ts.Allocations() != 4 {
		t.Fatalf("allocations %d", ts.Allocations())
	}
	const n = 200
	go func() {
		for i := 0; i < n; i++ {
			pkt := bytes.Repeat([]byte{byte(i)}, 1000)
			pkt[0] = 1 // never 0xff: keep clear of the control range
			if err := p.Write(ctx, pkt); err != nil {
				return
			}
		}
	}()
	got := 0
	for got < n {
		b, err := p.Read(ctx)
		if err != nil {
			t.Fatalf("read after %d: %v", got, err)
		}
		if len(b) != 1000 {
			t.Fatalf("len %d", len(b))
		}
		got++
	}
	vps.mu.Lock()
	defer vps.mu.Unlock()
	if len(vps.hellos) != 1 || vps.hellos[p.Session()] < 4 {
		t.Fatalf("hellos %v, session %x", vps.hellos, p.Session())
	}
}

func TestPoolRestartsDeadWorker(t *testing.T) {
	p, _, ts := newPool(t, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := p.WaitReady(ctx, 2); err != nil {
		t.Fatal(err)
	}
	// Kill the relay side: every allocation dies, workers must notice via
	// probe timeout (ZombieAfter) and come back once the relay is up again.
	ts.Restart(t)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		st := p.Stats()
		if st.Restarts >= 2 && st.Active == 2 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("workers did not recover: %+v", p.Stats())
}
