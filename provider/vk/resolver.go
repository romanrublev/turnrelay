package vk

// Whitelisted networks usually break the system resolver first, and any one
// public resolver may be filtered too (in the field only one of four
// answered). So names are resolved by asking every public resolver at once
// and taking the first answer; results are cached for the credential TTL.

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"time"
)

var publicResolvers = []string{"8.8.8.8:53", "1.1.1.1:53", "77.88.8.8:53", "9.9.9.9:53", "208.67.222.222:53"}

const (
	resolveTimeout  = 5 * time.Second
	resolveCacheTTL = 10 * time.Minute
)

type lookupFunc func(ctx context.Context, server, host string) ([]net.IP, error)

type racingResolver struct {
	servers []string
	lookup  lookupFunc
	mu      sync.Mutex
	cache   map[string]cachedIPs
}

type cachedIPs struct {
	ips []net.IP
	exp time.Time
}

func newRacingResolver(servers []string, lookup lookupFunc) *racingResolver {
	if lookup == nil {
		lookup = lookupVia
	}
	return &racingResolver{servers: servers, lookup: lookup, cache: map[string]cachedIPs{}}
}

// lookupVia resolves host through one DNS server using Go's resolver.
func lookupVia(ctx context.Context, server, host string) ([]net.IP, error) {
	r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "udp", server)
	}}
	addrs, err := r.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		if a.IP.To4() != nil {
			ips = append(ips, a.IP)
		}
	}
	if len(ips) == 0 {
		return nil, errors.New("no IPv4 address")
	}
	return ips, nil
}

func (r *racingResolver) LookupIP(ctx context.Context, host string) ([]net.IP, error) {
	r.mu.Lock()
	if c, ok := r.cache[host]; ok && time.Now().Before(c.exp) {
		r.mu.Unlock()
		return c.ips, nil
	}
	r.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	type res struct {
		ips []net.IP
		err error
	}
	out := make(chan res, len(r.servers))
	for _, s := range r.servers {
		go func(server string) {
			ips, err := r.lookup(ctx, server, host)
			out <- res{ips, err}
		}(s)
	}
	var lastErr error
	for range r.servers {
		got := <-out
		if got.err == nil && len(got.ips) > 0 {
			r.mu.Lock()
			r.cache[host] = cachedIPs{ips: got.ips, exp: time.Now().Add(resolveCacheTTL)}
			r.mu.Unlock()
			return got.ips, nil
		}
		lastErr = got.err
	}
	return nil, errors.New("vk: resolve " + host + ": all resolvers failed: " + errString(lastErr))
}

func errString(err error) string {
	if err == nil {
		return "no answer"
	}
	return err.Error()
}

// DialContext dials host:port, resolving the name through the racing
// resolver; literal IPs are dialed directly.
func (r *racingResolver) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}
	if net.ParseIP(host) != nil {
		return d.DialContext(ctx, network, address)
	}
	ips, err := r.LookupIP(ctx, host)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for _, ip := range ips {
		c, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return c, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("no addresses")
	}
	return nil, errors.New("vk: dial " + host + ": " + strings.TrimPrefix(lastErr.Error(), "dial "))
}

// ExportedResolverDial exposes the shared racing resolver's dialer for
// diagnostics.
func ExportedResolverDial() func(ctx context.Context, network, address string) (net.Conn, error) {
	return defaultResolver.DialContext
}
