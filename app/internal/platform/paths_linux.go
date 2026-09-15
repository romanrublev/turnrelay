package platform

import (
	"os"
	"path/filepath"
)

func SocketPath() string { return "/run/turnrelay/turnrelay.sock" }

// OwnerUIDPath is under /etc, not /run: /run is a tmpfs cleared on reboot, and
// the daemon (started at boot by systemd) reads the owner uid before it can
// recreate its runtime dir, so the uid file must persist.
func OwnerUIDPath() string { return "/etc/turnrelay/owner" }

// RunDir is a root-writable working directory for the daemon: systemd starts it
// in "/", and sing-box writes its cache file (cache.db) relative to the working
// directory. The control socket lives here too. It is recreated on each daemon
// start, so it is fine that /run is volatile.
func RunDir() string { return "/run/turnrelay" }

func ProfilePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "turnrelay", "profile.json")
}
