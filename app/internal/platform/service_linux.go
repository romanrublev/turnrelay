package platform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

const systemdUnitName = "turnrelay.service"
const systemdUnitPath = "/etc/systemd/system/turnrelay.service"

// SystemdUnit renders the systemd service unit that runs the daemon as root.
func SystemdUnit(execPath string) string {
	return fmt.Sprintf(`[Unit]
Description=turnrelay VPN daemon
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s daemon
Restart=on-failure
RestartSec=2

[Install]
WantedBy=multi-user.target
`, execPath)
}

func Install(execPath string, ownerUID uint32) error {
	// Copy the binary to a root-owned path and point systemd at that, not at
	// the (possibly user-writable) binary the admin ran. See installBinary.
	installed, err := installBinary(execPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(OwnerUIDPath()), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(OwnerUIDPath(), []byte(strconv.FormatUint(uint64(ownerUID), 10)), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(systemdUnitPath, []byte(SystemdUnit(installed)), 0o644); err != nil {
		return err
	}
	if err := exec.Command("systemctl", "daemon-reload").Run(); err != nil {
		return err
	}
	return exec.Command("systemctl", "enable", "--now", systemdUnitName).Run()
}

func Uninstall() error {
	_ = exec.Command("systemctl", "disable", "--now", systemdUnitName).Run()
	_ = os.Remove(systemdUnitPath)
	_ = os.Remove(OwnerUIDPath())
	_ = os.Remove(InstalledBinaryPath())
	_ = exec.Command("systemctl", "daemon-reload").Run()
	return nil
}
