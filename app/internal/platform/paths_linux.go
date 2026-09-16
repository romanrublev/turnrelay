package platform

import (
	"os"
	"path/filepath"
)

func SocketPath() string { return "/run/turnrelay/turnrelay.sock" }

// InstalledBinaryPath is the root-owned location the daemon binary is copied to
// on install; the systemd unit points here, never at the admin's own copy.
func InstalledBinaryPath() string { return "/usr/local/lib/turnrelay/turnrelayd" }

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
