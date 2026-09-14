package turnrelay_test

import (
	"context"
	"net"
	"net/netip"
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
