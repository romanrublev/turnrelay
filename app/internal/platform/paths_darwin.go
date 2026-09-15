// Package platform holds the OS-specific paths and service integration.
package platform

import (
	"os"
	"path/filepath"
)

func SocketPath() string   { return "/var/run/turnrelay.sock" }
func OwnerUIDPath() string { return "/var/run/turnrelay.owner" }

// RunDir is a root-writable working directory for the daemon. A launchd daemon
// starts in "/", which is read-only on macOS, and sing-box writes its cache
// file (cache.db) relative to the working directory, so the daemon moves here
// first.
func RunDir() string { return "/var/run/turnrelay" }

func ProfilePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "turnrelay", "profile.json")
}
