// turnrelay-udp bridges a local UDP socket to the VK TURN pipe. Point a
// WireGuard client at -listen and run the matching server on the VPS.
package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	M "github.com/sagernet/sing/common/metadata"

	"github.com/romanrublev/turnrelay"
)

type links []string

func (l *links) String() string     { return strings.Join(*l, ",") }
func (l *links) Set(s string) error { *l = append(*l, s); return nil }

func main() {
	var ls links
	listen := flag.String("listen", "127.0.0.1:9000", "local UDP socket for the WireGuard client")
	prov := flag.String("provider", "vk", "credential provider: vk | static")
	flag.Var(&ls, "link", "VK call link (repeatable, provider vk)")
	turnUser := flag.String("turn-user", "", "relay username (provider static)")
	turnPass := flag.String("turn-pass", "", "relay password (provider static)")
	server := flag.String("server", "", "VPS host:port running the relay-side server")
	n := flag.Int("n", turnrelay.DefaultConnections, "TURN allocations (about 18 per VK credential/participant)")
	mode := flag.String("mode", "srtp", "srtp | wrap | dtls")
	password := flag.String("password", "", "wrap mode: tunnel password")
	wrapKey := flag.String("wrap-key", "", "wrap mode: raw 32-byte key, hex")
	turnServer := flag.String("turn", "", "TURN relay host:port (required for static, optional override for vk)")
	tcp := flag.Bool("tcp", false, "use TCP to the TURN relay (slower)")
	statsEvery := flag.Duration("stats", 10*time.Second, "stats log interval")
	captcha := flag.String("captcha", "auto", "VK captcha policy: auto (solve the proof-of-work captcha) | fail")
	flag.Parse()

	ap, err := netip.ParseAddrPort(*server)
	if err != nil {
		log.Fatalf("-server: %v", err)
	}
	var key []byte
	if *wrapKey != "" {
		if key, err = hex.DecodeString(*wrapKey); err != nil {
			log.Fatalf("-wrap-key: %v", err)
		}
	}
	udp := !*tcp
	d, err := turnrelay.New(turnrelay.Config{
		Provider: *prov, CallLinks: ls, TURNServer: *turnServer, TURNUsername: *turnUser, TURNPassword: *turnPass,
		Server: ap, Connections: *n, Mode: turnrelay.Mode(*mode),
		Password: *password, WrapKey: key, TURNUDP: &udp,
		Captcha: turnrelay.CaptchaPolicy(*captcha),
		Logf:    log.Printf,
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := d.Start(ctx); err != nil {
		log.Fatal(err)
	}
	defer d.Close()

	local, err := net.ListenPacket("udp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	defer local.Close()
	pipe, err := d.DialContext(ctx, "udp", M.SocksaddrFromNetIP(ap))
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("listening on %s, %d connections via %s (%s)", *listen, *n, *server, *mode)

	var lastPeer atomic.Value // net.Addr
	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := local.ReadFrom(buf)
			if err != nil {
				return
			}
			lastPeer.Store(from)
			if _, err := pipe.Write(buf[:n]); err != nil {
				log.Printf("uplink: %v", err)
				return
			}
		}
	}()
	go func() {
		buf := make([]byte, 2048)
		for {
			n, err := pipe.Read(buf)
			if err != nil {
				return
			}
			if to, ok := lastPeer.Load().(net.Addr); ok {
				_, _ = local.WriteTo(buf[:n], to)
			}
		}
	}()
	t := time.NewTicker(*statsEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(os.Stderr, "shutting down")
			return
		case <-t.C:
			log.Printf("stats: %+v", d.Stats())
		}
	}
}
