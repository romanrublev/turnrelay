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
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/romanrublev/turnrelay/exit"
	"github.com/romanrublev/turnrelay/obfs"
	"github.com/romanrublev/turnrelay/passwords"
)

func main() {
	listen := flag.String("listen", ":56004", "UDP address to listen on")
	mode := flag.String("mode", "srtp", "obfuscation mode: srtp, wrap or dtls")
	password := flag.String("password", os.Getenv("TURNRELAY_PASSWORD"), "pre-shared password (or TURNRELAY_PASSWORD)")
	passwordFile := flag.String("password-file", "", "file with accepted passwords, one per line; re-read automatically when it changes (or on SIGHUP)")
	reloadEvery := flag.Duration("password-reload-interval", 2*time.Second, "how often -password-file is checked for changes")
	bind := flag.String("bind", "", "local address for outbound connections")
	allowPrivate := flag.Bool("allow-private", false, "serve private, loopback and link-local destinations")
	dialTimeout := flag.Duration("dial-timeout", 10*time.Second, "destination dial timeout")
	maxStreams := flag.Int("max-streams", 256, "concurrent TCP streams per session")
	udpTimeout := flag.Duration("udp-timeout", 60*time.Second, "idle timeout per UDP association")
	certPath := flag.String("cert", "", "path to a persisted DTLS certificate; enables client pinning (its SHA-256 is printed at startup)")
	quiet := flag.Bool("quiet", false, "log errors only")
	flag.Parse()

	if *passwordFile != "" {
		// -password defaults to $TURNRELAY_PASSWORD; only an explicit
		// -password conflicts with -password-file.
		flag.Visit(func(f *flag.Flag) {
			if f.Name == "password" {
				fmt.Fprintln(os.Stderr, "use either -password-file or -password, not both")
				os.Exit(2)
			}
		})
	} else if *password == "" {
		fmt.Fprintln(os.Stderr, "a password is required: -password-file, -password or TURNRELAY_PASSWORD")
		os.Exit(2)
	}
	logf := log.Printf
	if *quiet {
		logf = func(string, ...any) {}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var pwSource passwords.Source
	if *passwordFile != "" {
		if fi, err := os.Stat(*passwordFile); err == nil && fi.Mode().Perm()&0o077 != 0 {
			log.Printf("warning: %s is readable by other users (mode %v); chmod 600 it", *passwordFile, fi.Mode().Perm())
		}
		pf, err := passwords.OpenFile(*passwordFile, logf)
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("loaded %d password(s) from %s", len(pf.List()), *passwordFile)
		go pf.Run(ctx, *reloadEvery)
		// SIGHUP forces an immediate re-read.
		hup := make(chan os.Signal, 1)
		signal.Notify(hup, syscall.SIGHUP)
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-hup:
					if _, err := pf.Reload(); err != nil {
						log.Print(err)
					}
				}
			}
		}()
		pwSource = pf
	} else {
		pwSource = passwords.Static{*password}
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

	inst, err := exit.Listen(ctx, exit.ListenConfig{
		Address: *listen, Mode: obfs.Mode(*mode), Passwords: pwSource, Cert: cert, Logf: logf,
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
