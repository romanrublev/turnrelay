// Package singbox registers turnrelay as a native sing-box outbound. It is a
// separate Go module so the heavy sing-box dependency tree stays out of the
// core turnrelay library.
package singbox

import (
	"fmt"
	"net/netip"

	"github.com/romanrublev/turnrelay"
	"github.com/sagernet/sing-box/option"
)

const (
	ServerTypeWireGuard = "wireguard"
	ServerTypeExit      = "exit"
)

// TurnrelayOutboundOptions is the JSON schema of a `"type": "turnrelay"`
// outbound. It embeds sing-box's dialer options (so `detour` and
// `bind_interface` apply to the sockets towards the VK API and the relay) and
// server options (`server` / `server_port`, the VPS running the relay-side
// server).
type TurnrelayOutboundOptions struct {
	option.DialerOptions
	option.ServerOptions
	Provider     string   `json:"provider,omitempty"`
	CallLink     string   `json:"call_link,omitempty"`
	CallLinks    []string `json:"call_links,omitempty"`
	TURNServer   string   `json:"turn_server,omitempty"`
	TURNUsername string   `json:"turn_username,omitempty"`
	TURNPassword string   `json:"turn_password,omitempty"`
	Connections  int      `json:"connections,omitempty"`
	Mode         string   `json:"mode,omitempty"`
	Password     string   `json:"password,omitempty"`
	Captcha      string   `json:"captcha,omitempty"`
	UDP          *bool    `json:"udp,omitempty"`
	// ServerType selects what runs on the VPS: "wireguard" (default) is the
	// upstream relay-side server in front of WireGuard, used as the
	// wireguard endpoint's detour; "exit" is turnrelay-server, which makes
	// this outbound a normal TCP+UDP proxy outbound with no WireGuard.
	ServerType string `json:"server_type,omitempty"`
}

// toConfig translates the JSON options into a turnrelay.Config. It parses the
// server address (which must be a literal IP) and leaves defaulting and the
// remaining validation to turnrelay.New, so the two never drift.
func (o *TurnrelayOutboundOptions) toConfig() (turnrelay.Config, error) {
	links := make([]string, 0, len(o.CallLinks)+1)
	if o.CallLink != "" {
		links = append(links, o.CallLink)
	}
	links = append(links, o.CallLinks...)

	var server netip.AddrPort
	if o.Server != "" || o.ServerPort != 0 {
		addr, err := netip.ParseAddr(o.Server)
		if err != nil {
			return turnrelay.Config{}, fmt.Errorf("turnrelay: server must be an IP address: %w", err)
		}
		server = netip.AddrPortFrom(addr, o.ServerPort)
	}

	return turnrelay.Config{
		Provider:     o.Provider,
		CallLinks:    links,
		TURNServer:   o.TURNServer,
		TURNUsername: o.TURNUsername,
		TURNPassword: o.TURNPassword,
		Server:       server,
		Connections:  o.Connections,
		Mode:         turnrelay.Mode(o.Mode),
		Password:     o.Password,
		TURNUDP:      o.UDP,
		Captcha:      turnrelay.CaptchaPolicy(o.Captcha),
	}, nil
}
