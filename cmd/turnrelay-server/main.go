// turnrelay-server is the proxy-exit server: it terminates the obfuscated
// TURN transport on one UDP port and dials destinations on behalf of
// authenticated clients. No WireGuard is involved.
package main

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/signal"
	"strings"
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
	socks5Upstream := flag.String("socks5", "", "route outbound TCP through this SOCKS5 proxy (host:port or socks5://[user:pass@]host:port); UDP associations stay direct")
	socks5User := flag.String("socks5-user", "", "username for -socks5 (optional, overrides URL user)")
	socks5Pass := flag.String("socks5-pass", "", "password for -socks5 (optional, overrides URL password)")
	allowPrivate := flag.Bool("allow-private", false, "serve private, loopback and link-local destinations")
	dialTimeout := flag.Duration("dial-timeout", 10*time.Second, "destination dial timeout")
	maxStreams := flag.Int("max-streams", 256, "concurrent TCP streams per session")
	udpTimeout := flag.Duration("udp-timeout", 60*time.Second, "idle timeout per UDP association")
	certPath := flag.String("cert", "", "path to a persisted DTLS certificate; enables client pinning (its SHA-256 is printed at startup)")
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

	// With -cert, use a stable certificate and print its fingerprint so the
	// client can pin it (server_fingerprint in the profile). Without -cert the
	// server keeps the legacy unauthenticated DTLS, open to an on-path MITM.
	var cert *tls.Certificate
	if *certPath != "" {
		c, err := obfs.LoadOrCreateCertFile(*certPath)
		if err != nil {
			log.Fatalf("cert: %v", err)
		}
		cert = &c
		log.Printf("DTLS certificate fingerprint (set server_fingerprint on clients): %s",
			hex.EncodeToString(obfs.CertFingerprint(c)))
	} else {
		log.Println("warning: no -cert; DTLS is unauthenticated and open to an on-path MITM. Pass -cert to enable client pinning.")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	socks5Addr, socks5UserVal, socks5PassVal := *socks5Upstream, *socks5User, *socks5Pass
	if socks5Addr != "" {
		raw := socks5Addr
		// Accept common forms: host:port, socks5://[user:pass@]host:port,
		// and tolerate the missing-colon typo "socks5//...".
		if strings.HasPrefix(raw, "socks5//") {
			raw = "socks5://" + strings.TrimPrefix(raw, "socks5//")
		} else if strings.HasPrefix(raw, "socks5h//") {
			raw = "socks5h://" + strings.TrimPrefix(raw, "socks5h//")
		}
		if strings.Contains(raw, "://") {
			u, err := url.Parse(raw)
			if err != nil || u.Host == "" || (u.Scheme != "socks5" && u.Scheme != "socks5h") {
				log.Fatalf("invalid -socks5 URL %q (expected socks5://[user:pass@]host:port or host:port)", *socks5Upstream)
			}
			socks5Addr = u.Host
			if u.User != nil {
				if socks5UserVal == "" {
					socks5UserVal = u.User.Username()
				}
				if p, ok := u.User.Password(); ok && socks5PassVal == "" {
					socks5PassVal = p
				}
			}
		} else {
			socks5Addr = raw
		}
	}

	inst, err := exit.Listen(ctx, exit.ListenConfig{
		Address: *listen, Mode: obfs.Mode(*mode), Password: *password, Cert: cert, Logf: logf,
		Server: exit.ServerOptions{
			DialTimeout: *dialTimeout, MaxStreams: *maxStreams, AllowPrivate: *allowPrivate,
			Bind: *bind, UDPTimeout: *udpTimeout, Logf: logf,
			SOCKS5: socks5Addr, SOCKS5User: socks5UserVal, SOCKS5Pass: socks5PassVal,
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer inst.Close()
	<-ctx.Done()
	log.Println("shutting down")
}
