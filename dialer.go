package turnrelay

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	M "github.com/sagernet/sing/common/metadata"

	"github.com/romanrublev/turnrelay/credpool"
	"github.com/romanrublev/turnrelay/mux"
	"github.com/romanrublev/turnrelay/obfs"
	"github.com/romanrublev/turnrelay/provider"
	"github.com/romanrublev/turnrelay/provider/static"
	"github.com/romanrublev/turnrelay/provider/vk"
)

type Mode = obfs.Mode

const (
	ModeSRTP = obfs.ModeSRTP
	ModeWrap = obfs.ModeWrap
	ModeDTLS = obfs.ModeDTLS

	DefaultConnections = 30
	MaxConnections     = 60
)

type CaptchaPolicy string

const (
	CaptchaFail CaptchaPolicy = "fail"
	CaptchaWait CaptchaPolicy = "wait"
)

var ErrTCPUnsupported = errors.New("turnrelay: only udp is supported; put a wireguard endpoint on top")

const (
	ProviderVK     = "vk"
	ProviderStatic = "static"
)

type Config struct {
	Provider     string   // "vk" (default) | "static"
	CallLinks    []string // vk
	TURNServer   string   // static: relay host:port; vk: optional override
	TURNUsername string   // static
	TURNPassword string   // static
	Server       netip.AddrPort
	Connections  int
	Mode         Mode
	Password     string
	WrapKey      []byte
	TURNUDP      *bool // nil means true
	Captcha      CaptchaPolicy
	Logf         func(string, ...any)
	// DialContext, when set, opens every socket the library makes: the
	// UDP (or TCP) socket of each worker towards the TURN relay, and for
	// provider vk the TCP connections to the VK API. nil uses the net
	// package (and, for the VK API, a resolver that bypasses system DNS).
	// A sing-box outbound passes its own dialer here so detour and
	// bind_interface apply. See relay.Options.DialContext for the contract.
	DialContext func(ctx context.Context, network, address string) (net.Conn, error)
	// test hook: replaces the provider entirely
	Fetcher provider.Fetcher
}

type Stats struct {
	Workers, Active, Connecting, Restarts int
	CredSlots                             int
	CaptchaUntil                          time.Time
	LastError                             string
}

type Dialer struct {
	cfg   Config
	links []string
	creds *credpool.Pool
	pool  *mux.Pool
	peer  *net.UDPAddr

	mismatchOnce sync.Once
}

func New(cfg Config) (*Dialer, error) {
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	if cfg.Provider == "" {
		cfg.Provider = ProviderVK
	}
	var (
		links    []string
		fetcher  = cfg.Fetcher
		override string
	)
	switch cfg.Provider {
	case ProviderVK:
		if len(cfg.CallLinks) == 0 {
			return nil, errors.New("turnrelay: provider vk needs at least one call link")
		}
		for _, l := range cfg.CallLinks {
			h, err := vk.ParseCallLink(l)
			if err != nil {
				return nil, fmt.Errorf("turnrelay: %w", err)
			}
			links = append(links, h)
		}
		override = cfg.TURNServer
		if fetcher == nil {
			client, err := vk.NewClientWithDialer(cfg.DialContext)
			if err != nil {
				return nil, err
			}
			client.Logf = cfg.Logf
			fetcher = client.Fetch
		}
	case ProviderStatic:
		if cfg.TURNServer == "" || cfg.TURNUsername == "" || cfg.TURNPassword == "" {
			return nil, errors.New("turnrelay: provider static needs turn server, username and password")
		}
		links = []string{"static"}
		if fetcher == nil {
			fetcher = static.New(cfg.TURNServer, cfg.TURNUsername, cfg.TURNPassword)
		}
	default:
		return nil, fmt.Errorf("turnrelay: unknown provider %q", cfg.Provider)
	}
	if !cfg.Server.IsValid() || cfg.Server.Port() == 0 {
		return nil, errors.New("turnrelay: server address is required")
	}
	if cfg.Connections == 0 {
		cfg.Connections = DefaultConnections
	}
	if cfg.Connections < 1 || cfg.Connections > MaxConnections {
		return nil, fmt.Errorf("turnrelay: connections must be 1..%d (each 10 use one VK participant slot)", MaxConnections)
	}
	if cfg.Mode == "" {
		cfg.Mode = ModeSRTP
	}
	if cfg.Mode == ModeDTLS {
		cfg.Logf("turnrelay: mode dtls is deprecated: VK relays shape it to a few KB/s")
	}
	wrapper, err := obfs.New(cfg.Mode, obfs.Options{Password: cfg.Password, WrapKey: cfg.WrapKey})
	if err != nil {
		return nil, fmt.Errorf("turnrelay: %w", err)
	}
	if cfg.Captcha == "" {
		cfg.Captcha = CaptchaFail
	}
	// Both policies currently behave the same inside the library: the pool
	// reports the captcha, cools down for a minute, and workers retry with
	// backoff. "wait" is accepted so configs stay valid once M2 wires a
	// callback for graphical clients.
	udp := true
	if cfg.TURNUDP != nil {
		udp = *cfg.TURNUDP
	}
	d := &Dialer{cfg: cfg, links: links, peer: net.UDPAddrFromAddrPort(cfg.Server)}
	d.creds = credpool.New(fetcher, credpool.Options{Links: links, Logf: cfg.Logf})
	d.pool = mux.New(mux.Options{
		Workers: cfg.Connections, Peer: d.peer, Wrapper: wrapper, Creds: d.creds,
		TURNUDP: udp, TURNOverride: override, Logf: cfg.Logf, DialContext: cfg.DialContext,
	})
	return d, nil
}

func (d *Dialer) Start(ctx context.Context) error {
	d.pool.Start(ctx)
	return nil
}

func (d *Dialer) Close() error {
	d.pool.Close()
	return nil
}

func (d *Dialer) ServerAddr() *net.UDPAddr { return d.peer }

func (d *Dialer) WaitReady(ctx context.Context, n int) error { return d.pool.WaitReady(ctx, n) }

func (d *Dialer) DialContext(ctx context.Context, network string, dest M.Socksaddr) (net.Conn, error) {
	if !strings.HasPrefix(network, "udp") {
		return nil, ErrTCPUnsupported
	}
	if dest.IsValid() && dest.AddrPort() != d.cfg.Server {
		d.mismatchOnce.Do(func() {
			d.cfg.Logf("turnrelay: dial to %s ignored, datagrams always go to %s", dest, d.cfg.Server)
		})
	}
	return newDatagramConn(d), nil
}

func (d *Dialer) ListenPacket(ctx context.Context, dest M.Socksaddr) (net.PacketConn, error) {
	return &packetConn{datagramConn: newDatagramConn(d)}, nil
}

func (d *Dialer) Stats() Stats {
	ps, cs := d.pool.Stats(), d.creds.Stats()
	st := Stats{Workers: d.cfg.Connections, Active: ps.Active, Connecting: ps.Connecting, Restarts: ps.Restarts,
		CredSlots: cs.Slots, CaptchaUntil: cs.CaptchaUntil, LastError: ps.LastError}
	if st.LastError == "" {
		st.LastError = cs.LastError
	}
	return st
}
