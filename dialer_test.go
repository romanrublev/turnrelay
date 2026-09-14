package turnrelay_test

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"sync"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"

	"github.com/romanrublev/turnrelay"
	"github.com/romanrublev/turnrelay/mux"
	"github.com/romanrublev/turnrelay/obfs/obfstest"
	"github.com/romanrublev/turnrelay/provider"
	"github.com/romanrublev/turnrelay/relay/turntest"
)

func echoVPS(t *testing.T) *obfstest.Server {
	srv := obfstest.ListenSRTP(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		for {
			c, err := srv.Accept(ctx)
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				buf := make([]byte, 2048)
				for {
					n, err := c.Read(buf)
					if err != nil {
						return
					}
					if _, ok := mux.ParseHello(buf[:n]); ok {
						continue
					}
					if _, err := c.Write(buf[:n]); err != nil {
						return
					}
				}
			}()
		}
	}()
	return srv
}

func newDialer(t *testing.T) *turnrelay.Dialer {
	ts := turntest.Start(t)
	vps := echoVPS(t)
	d, err := turnrelay.New(turnrelay.Config{
		CallLinks:   []string{"https://vk.ru/call/join/TESTLINK1234"},
		Server:      netip.MustParseAddrPort(vps.Addr().String()),
		Connections: 3,
		Mode:        turnrelay.ModeSRTP,
		Logf:        t.Logf,
		Fetcher: func(context.Context, string) (provider.Credential, error) {
			return provider.Credential{Username: ts.Username, Password: ts.Password, Relays: []string{ts.Addr()}, Link: "TESTLINK1234"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); _ = d.Close() })
	if err := d.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDialContextUDPRoundTrip(t *testing.T) {
	d := newDialer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := d.WaitReady(ctx, 1); err != nil {
		t.Fatal(err)
	}
	c, err := d.DialContext(ctx, "udp", M.ParseSocksaddr("203.0.113.5:56004"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for i := 0; i < 20; i++ {
		msg := []byte{1, byte(i), 2, 3}
		if _, err := c.Write(msg); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 2048)
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, err := c.Read(buf)
		if err != nil || string(buf[:n]) != string(msg) {
			t.Fatalf("i=%d n=%d err=%v", i, n, err)
		}
	}
	if _, err := d.DialContext(ctx, "tcp", M.ParseSocksaddr("1.1.1.1:443")); err != turnrelay.ErrTCPUnsupported {
		t.Fatalf("tcp: %v", err)
	}
	st := d.Stats()
	if st.Active < 1 || st.Workers != 3 {
		t.Fatalf("stats %+v", st)
	}
}

func TestListenPacket(t *testing.T) {
	d := newDialer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := d.WaitReady(ctx, 1); err != nil {
		t.Fatal(err)
	}
	pc, err := d.ListenPacket(ctx, M.Socksaddr{Addr: netip.IPv4Unspecified()})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	if _, err := pc.WriteTo([]byte("x"), &net.UDPAddr{IP: net.IPv4(203, 0, 113, 5), Port: 56004}); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	_ = pc.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, from, err := pc.ReadFrom(buf)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if from.String() != d.ServerAddr().String() {
		t.Fatalf("from %s", from)
	}
}

func TestConfigValidation(t *testing.T) {
	base := turnrelay.Config{CallLinks: []string{"https://vk.ru/call/join/TESTLINK1234"}, Server: netip.MustParseAddrPort("203.0.113.5:56004")}
	cases := map[string]func(*turnrelay.Config){
		"no links":       func(c *turnrelay.Config) { c.CallLinks = nil },
		"bad link":       func(c *turnrelay.Config) { c.CallLinks = []string{"x"} },
		"bad provider":   func(c *turnrelay.Config) { c.Provider = "yandex" },
		"static no user": func(c *turnrelay.Config) { c.Provider = "static"; c.TURNServer = "1.2.3.4:3478" },
		"no server":      func(c *turnrelay.Config) { c.Server = netip.AddrPort{} },
		"too many":       func(c *turnrelay.Config) { c.Connections = 61 },
		"wrap no key":    func(c *turnrelay.Config) { c.Mode = turnrelay.ModeWrap },
		"bad mode":       func(c *turnrelay.Config) { c.Mode = "plain" },
	}
	for name, mutate := range cases {
		c := base
		mutate(&c)
		if _, err := turnrelay.New(c); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := turnrelay.New(base); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	st := turnrelay.Config{Provider: "static", TURNServer: "1.2.3.4:3478", TURNUsername: "u", TURNPassword: "p", Server: base.Server}
	if _, err := turnrelay.New(st); err != nil {
		t.Fatalf("valid static config rejected: %v", err)
	}
}

func TestReadDeadlineInterrupts(t *testing.T) {
	d := newDialer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := d.WaitReady(ctx, 1); err != nil {
		t.Fatal(err)
	}
	c, err := d.DialContext(ctx, "udp", M.ParseSocksaddr("203.0.113.5:56004"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// interrupt parks a Read, then after a short delay calls
	// SetReadDeadline(deadline) from this goroutine and asserts the parked
	// Read wakes up with a timeout error rather than raw context.Canceled.
	interrupt := func(deadline time.Time) {
		t.Helper()
		errCh := make(chan error, 1)
		go func() {
			_, err := c.Read(make([]byte, 16))
			errCh <- err
		}()
		time.Sleep(100 * time.Millisecond)
		if err := c.SetReadDeadline(deadline); err != nil {
			t.Fatalf("SetReadDeadline(%v): %v", deadline, err)
		}
		select {
		case err := <-errCh:
			if !errors.Is(err, os.ErrDeadlineExceeded) {
				t.Fatalf("deadline %v: err = %v, want os.ErrDeadlineExceeded", deadline, err)
			}
			ne, ok := err.(net.Error)
			if !ok || !ne.Timeout() {
				t.Fatalf("deadline %v: err = %v, want a timeout net.Error", deadline, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("deadline %v: Read did not return within 2s", deadline)
		}
	}

	interrupt(time.Now())
	interrupt(time.Time{})               // zero deadline: still interrupts a parked Read
	interrupt(time.Now().Add(time.Hour)) // far-future deadline: still interrupts a parked Read

	// After clearing the deadline, a Read blocks normally and returns a
	// datagram once one arrives.
	if err := c.SetReadDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := c.Read(buf)
	if err != nil || string(buf[:n]) != "hello" {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestConnAfterClose(t *testing.T) {
	d := newDialer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := d.WaitReady(ctx, 1); err != nil {
		t.Fatal(err)
	}
	c, err := d.DialContext(ctx, "udp", M.ParseSocksaddr("203.0.113.5:56004"))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := c.Read(make([]byte, 16)); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Read after close: %v", err)
	}
	if _, err := c.Write([]byte("x")); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Write after close: %v", err)
	}
	if err := c.SetReadDeadline(time.Now()); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("SetReadDeadline after close: %v", err)
	}
}

func TestTwoConnsShareDownlink(t *testing.T) {
	d := newDialer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := d.WaitReady(ctx, 1); err != nil {
		t.Fatal(err)
	}
	c1, err := d.DialContext(ctx, "udp", M.ParseSocksaddr("203.0.113.5:56004"))
	if err != nil {
		t.Fatal(err)
	}
	defer c1.Close()
	c2, err := d.DialContext(ctx, "udp", M.ParseSocksaddr("203.0.113.5:56004"))
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()

	if _, err := c1.Write([]byte("shared")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	_ = c2.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := c2.Read(buf)
	if err != nil || string(buf[:n]) != "shared" {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

// newStalledDialer builds a Dialer whose credential fetch never returns, so
// no worker ever comes up and nothing drains the uplink queue. Close still
// works: the fetcher honours ctx, which Dialer.Close cancels.
func newStalledDialer(t *testing.T) *turnrelay.Dialer {
	t.Helper()
	d, err := turnrelay.New(turnrelay.Config{
		CallLinks:   []string{"https://vk.ru/call/join/TESTLINK1234"},
		Server:      netip.MustParseAddrPort("127.0.0.1:56004"),
		Connections: 1,
		Fetcher: func(ctx context.Context, _ string) (provider.Credential, error) {
			<-ctx.Done()
			return provider.Credential{}, ctx.Err()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// TestWriteReleasedByConnClose: a Write parked on the full uplink queue must
// return net.ErrClosed when its own conn is closed, not only when the whole
// Dialer is. wireguard-go closes and re-dials its bind conn on errors; a
// Write that outlived the conn would leak a goroutine per re-dial.
func TestWriteReleasedByConnClose(t *testing.T) {
	d := newStalledDialer(t)
	c, err := d.DialContext(context.Background(), "udp", M.ParseSocksaddr("127.0.0.1:56004"))
	if err != nil {
		t.Fatal(err)
	}
	filled := make(chan struct{})
	parked := make(chan error, 1)
	go func() {
		for i := 0; i < mux.DefaultUplinkQueue; i++ {
			if _, err := c.Write([]byte{1}); err != nil {
				parked <- err
				return
			}
		}
		close(filled)
		_, err := c.Write([]byte{1}) // queue is full and nobody drains it: parks here
		parked <- err
	}()
	select {
	case <-filled:
	case err := <-parked:
		t.Fatalf("write failed while filling the queue: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("filling the queue did not finish")
	}
	select {
	case err := <-parked:
		t.Fatalf("write did not park on the full queue: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-parked:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("parked Write returned %v, want net.ErrClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("parked Write not released by conn Close within 2s")
	}
}

// TestWriteAfterDialerClose: once the Dialer is closed, Write must return
// net.ErrClosed every time, even though the uplink queue has room. The
// pool used to select at random between the free queue slot and the closed
// signal, so this failed about half the time.
func TestWriteAfterDialerClose(t *testing.T) {
	for i := 0; i < 50; i++ {
		d := newStalledDialer(t)
		c, err := d.DialContext(context.Background(), "udp", M.ParseSocksaddr("127.0.0.1:56004"))
		if err != nil {
			t.Fatal(err)
		}
		if err := d.Close(); err != nil {
			t.Fatal(err)
		}
		for j := 0; j < 4; j++ {
			if _, err := c.Write([]byte{1}); !errors.Is(err, net.ErrClosed) {
				t.Fatalf("iteration %d write %d: got %v, want net.ErrClosed", i, j, err)
			}
		}
		_ = c.Close()
	}
}

// TestDialContextHookReachesRelay: Config.DialContext is the socket factory
// for every worker's connection to the TURN relay (what a sing-box outbound
// hands in for detour and bind_interface).
func TestDialContextHookReachesRelay(t *testing.T) {
	ts := turntest.Start(t)
	vps := echoVPS(t)
	var mu sync.Mutex
	var dialed []string
	d, err := turnrelay.New(turnrelay.Config{
		CallLinks:   []string{"https://vk.ru/call/join/TESTLINK1234"},
		Server:      netip.MustParseAddrPort(vps.Addr().String()),
		Connections: 2,
		Logf:        t.Logf,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			mu.Lock()
			dialed = append(dialed, network+" "+address)
			mu.Unlock()
			var nd net.Dialer
			return nd.DialContext(ctx, network, address)
		},
		Fetcher: func(context.Context, string) (provider.Credential, error) {
			return provider.Credential{Username: ts.Username, Password: ts.Password, Relays: []string{ts.Addr()}, Link: "TESTLINK1234"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	t.Cleanup(func() { _ = d.Close() })
	if err := d.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := d.WaitReady(ctx, 2); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got := append([]string(nil), dialed...)
	mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("DialContext calls = %v, want one per worker", got)
	}
	for _, g := range got {
		if g != "udp "+ts.Addr() {
			t.Fatalf("DialContext call %q, want %q", g, "udp "+ts.Addr())
		}
	}
	c, err := d.DialContext(ctx, "udp", M.ParseSocksaddr(vps.Addr().String()))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("via-hook")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := c.Read(buf)
	if err != nil || string(buf[:n]) != "via-hook" {
		t.Fatalf("n=%d err=%v", n, err)
	}
}
