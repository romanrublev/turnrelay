// Package profile is the user-facing VPN profile: what the CLI reads and
// sends to the daemon. It intentionally holds no sing-box specifics.
package profile

import (
	"encoding/json"
	"errors"
	"os"
)

type Profile struct {
	Link            string `json:"link"`
	Server          string `json:"server"`
	Connections     int    `json:"connections"`
	Mode            string `json:"mode"`
	WGPrivateKey    string `json:"wg_private_key"`
	WGPeerPublicKey string `json:"wg_peer_public_key"`
	WGAddress       string `json:"wg_address"`
	// ServerType selects the transport: "wireguard" (default) tunnels a
	// WireGuard endpoint through the relay; "exit" makes the relay a direct
	// TCP/UDP proxy (KCP+smux, no WireGuard) and requires Password.
	ServerType string `json:"server_type,omitempty"`
	// Password is the pre-shared exit-server password (server_type "exit").
	Password string `json:"password,omitempty"`
	// LogLevel is the sing-box log level (info by default). Set it to "debug"
	// to make worker and handshake lines visible, which is also what the status
	// worker count is parsed from.
	LogLevel string `json:"log_level,omitempty"`
}

// IsExit reports whether the profile uses the exit (proxy) transport.
func (p Profile) IsExit() bool { return p.ServerType == "exit" }

func Defaults() Profile {
	return Profile{Connections: 18, Mode: "srtp", WGAddress: "10.8.0.2/32"}
}

func Load(path string) (Profile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Defaults(), err
	}
	p := Defaults()
	if err := json.Unmarshal(b, &p); err != nil {
		return Defaults(), err
	}
	return p, nil
}

func (p Profile) Validate() error {
	if p.Link == "" {
		return errors.New("profile: link is empty")
	}
	if p.Server == "" {
		return errors.New("profile: server is empty")
	}
	if p.IsExit() {
		if p.Password == "" {
			return errors.New("profile: password is required for server_type exit")
		}
		return nil
	}
	switch {
	case p.WGPrivateKey == "":
		return errors.New("profile: wg_private_key is empty")
	case p.WGPeerPublicKey == "":
		return errors.New("profile: wg_peer_public_key is empty")
	}
	return nil
}
