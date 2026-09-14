// turnrelay-proxy is a local SOCKS5 proxy whose upstream is a turnrelay
// exit server reached through TURN relays: any SOCKS5-capable client
// (browsers, curl, V2RayN) can use it, no WireGuard involved.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	M "github.com/sagernet/sing/common/metadata"

	"github.com/romanrublev/turnrelay"
	"github.com/romanrublev/turnrelay/exit"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:1080", "local SOCKS5 address")
	provider := flag.String("provider", "vk", "credential provider: vk or static")
	links := flag.String("links", "", "comma-separated VK call links (provider vk)")
	turnServer := flag.String("turn", "", "TURN relay host:port (provider static, or vk override)")
	turnUser := flag.String("turn-user", "", "TURN username (provider static)")
	turnPass := flag.String("turn-pass", os.Getenv("TURNRELAY_TURN_PASSWORD"), "TURN password (provider static, or TURNRELAY_TURN_PASSWORD)")
	server := flag.String("server", "", "exit server ip:port")
	n := flag.Int("connections", turnrelay.DefaultConnections, "TURN allocations")
	mode := flag.String("mode", "srtp", "obfuscation mode: srtp, wrap or dtls")
	password := flag.String("password", os.Getenv("TURNRELAY_PASSWORD"), "pre-shared password (or TURNRELAY_PASSWORD)")
	statsEvery := flag.Duration("stats", 30*time.Second, "stats log interval")
	flag.Parse()

	if *server == "" || *password == "" {
		fmt.Fprintln(os.Stderr, "-server and -password (or TURNRELAY_PASSWORD) are required")
		os.Exit(2)
	}
	ap, err := netip.ParseAddrPort(*server)
	if err != nil {
		log.Fatal(err)
	}
	var callLinks []string
	if *links != "" {
		callLinks = strings.Split(*links, ",")
	}
	d, err := turnrelay.New(turnrelay.Config{
		Provider: *provider, CallLinks: callLinks,
		TURNServer: *turnServer, TURNUsername: *turnUser, TURNPassword: *turnPass,
		Server: ap, Connections: *n, Mode: turnrelay.Mode(*mode), Password: *password,
		Logf: log.Printf,
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := d.Start(ctx); err != nil {
		log.Fatal(err)
	}
	defer d.Close()
	pc, err := d.ListenPacket(ctx, M.Socksaddr{})
	if err != nil {
		log.Fatal(err)
	}
	client := exit.NewClient(pc, d.ServerAddr(), exit.ClientOptions{Logf: log.Printf})
	defer client.Close()

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("socks5 on %s, %d connections via %s (%s)", *listen, *n, *server, *mode)
	go func() {
		t := time.NewTicker(*statsEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				log.Printf("stats: %+v", d.Stats())
			}
		}
	}()
	if err := exit.ServeSOCKS5(ctx, ln, client, log.Printf); err != nil {
		log.Fatal(err)
	}
}
