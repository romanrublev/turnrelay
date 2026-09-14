// turnrelay-server is the proxy-exit server: it terminates the obfuscated
// TURN transport on one UDP port and dials destinations on behalf of
// authenticated clients. No WireGuard is involved.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/romanrublev/turnrelay/exit"
	"github.com/romanrublev/turnrelay/obfs"
)

func main() {
	listen := flag.String("listen", ":56004", "UDP address to listen on")
	mode := flag.String("mode", "srtp", "obfuscation mode: srtp, wrap or dtls")
	password := flag.String("password", os.Getenv("TURNRELAY_PASSWORD"), "pre-shared password (or TURNRELAY_PASSWORD)")
	bind := flag.String("bind", "", "local address for outbound connections")
	allowPrivate := flag.Bool("allow-private", false, "serve private, loopback and link-local destinations")
	dialTimeout := flag.Duration("dial-timeout", 10*time.Second, "destination dial timeout")
	maxStreams := flag.Int("max-streams", 256, "concurrent TCP streams per session")
	udpTimeout := flag.Duration("udp-timeout", 60*time.Second, "idle timeout per UDP association")
	quiet := flag.Bool("quiet", false, "log errors only")
	flag.Parse()

	if *password == "" {
		fmt.Fprintln(os.Stderr, "a password is required: -password or TURNRELAY_PASSWORD")
		os.Exit(2)
	}
	logf := log.Printf
	if *quiet {
		logf = func(string, ...any) {}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	inst, err := exit.Listen(ctx, exit.ListenConfig{
		Address: *listen, Mode: obfs.Mode(*mode), Password: *password, Logf: logf,
		Server: exit.ServerOptions{
			DialTimeout: *dialTimeout, MaxStreams: *maxStreams, AllowPrivate: *allowPrivate,
			Bind: *bind, UDPTimeout: *udpTimeout, Logf: logf,
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer inst.Close()
	<-ctx.Done()
	log.Println("shutting down")
}
