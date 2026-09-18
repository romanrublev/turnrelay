// Package config turns a user profile into a sing-box tun-mode configuration.
// It emits JSON (not sing-box option structs) so it is trivial to test and so
// the engine owns all sing-box parsing.
package config

import (
	"encoding/json"
	"net"
	"strconv"

	"github.com/romanrublev/turnrelay/app/internal/profile"
)

func Build(p profile.Profile) ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	host, portStr, err := net.SplitHostPort(p.Server)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, err
	}
	level := p.LogLevel
	if level == "" {
		level = "info"
	}

	relay := map[string]any{
		"type": "turnrelay", "tag": "relay",
		"provider":    "vk",
		"server":      host,
		"server_port": port,
		"connections": p.Connections,
		"mode":        p.Mode,
	}
	// One or several VK call links: more links means more credentials, hence
	// more workers and resilience to a single link's join limit.
	if p.Link != "" {
		relay["call_link"] = p.Link
	}
	if len(p.Links) > 0 {
		relay["call_links"] = p.Links
	}

	// final is the outbound that carries all captured traffic: the WireGuard
	// endpoint in the default transport, or the relay itself in exit mode. The
	// remote DNS server tunnels through that same outbound.
	final := "wg"
	if p.IsExit() {
		relay["server_type"] = "exit"
		relay["password"] = p.Password
		if p.ServerFingerprint != "" {
			relay["server_fingerprint"] = p.ServerFingerprint
		}
		final = "relay"
	}

	cfg := map[string]any{
		"log": map[string]any{"level": level},
		"dns": map[string]any{
			"servers": []any{
				map[string]any{"type": "https", "tag": "remote", "server": "1.1.1.1", "detour": final},
				map[string]any{"type": "local", "tag": "local"},
			},
			"final": "remote",
		},
		"inbounds": []any{
			map[string]any{
				"type": "tun", "tag": "tun-in",
				"address":      []any{"172.19.0.1/30", "fdfe:dcba:9876::1/126"},
				"mtu":          1280,
				"auto_route":   true,
				"strict_route": true,
				"stack":        "gvisor",
			},
		},
		"outbounds": []any{
			relay,
			map[string]any{"type": "direct", "tag": "direct"},
		},
		"route": map[string]any{
			"auto_detect_interface":   true,
			"default_domain_resolver": map[string]any{"server": "local"},
			"rules": []any{
				map[string]any{"action": "sniff"},
				map[string]any{"protocol": "dns", "action": "hijack-dns"},
			},
			"final": final,
		},
	}

	// The WireGuard transport adds an endpoint that detours through the relay;
	// exit mode needs no WireGuard at all.
	if !p.IsExit() {
		cfg["endpoints"] = []any{
			map[string]any{
				"type": "wireguard", "tag": "wg", "detour": "relay",
				"system":      false,
				"mtu":         1280,
				"address":     []any{p.WGAddress},
				"private_key": p.WGPrivateKey,
				"peers": []any{
					map[string]any{
						"address": host, "port": port,
						"public_key":  p.WGPeerPublicKey,
						"allowed_ips": []any{"0.0.0.0/0", "::/0"},
					},
				},
			},
		}
	}
	return json.Marshal(cfg)
}
