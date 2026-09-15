// Package platform holds the OS-specific paths and service integration.
package platform

import (
	"os"
	"path/filepath"
)

func SocketPath() string   { return "/var/run/turnrelay.sock" }
func OwnerUIDPath() string { return "/var/run/turnrelay.owner" }

func ProfilePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "turnrelay", "profile.json")
}
