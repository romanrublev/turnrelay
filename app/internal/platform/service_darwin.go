package platform

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

const launchdLabel = "xyz.rublev.turnrelayd"
const launchdPlistPath = "/Library/LaunchDaemons/xyz.rublev.turnrelayd.plist"

func LaunchdPlist(execPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array><string>%s</string><string>daemon</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>/var/log/turnrelay.log</string>
  <key>StandardErrorPath</key><string>/var/log/turnrelay.log</string>
</dict>
</plist>
`, launchdLabel, execPath)
}

func Install(execPath string, ownerUID uint32) error {
	if err := os.WriteFile(OwnerUIDPath(), []byte(strconv.FormatUint(uint64(ownerUID), 10)), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(launchdPlistPath, []byte(LaunchdPlist(execPath)), 0o644); err != nil {
		return err
	}
	return exec.Command("launchctl", "bootstrap", "system", launchdPlistPath).Run()
}

func Uninstall() error {
	_ = exec.Command("launchctl", "bootout", "system/"+launchdLabel).Run()
	_ = os.Remove(launchdPlistPath)
	_ = os.Remove(OwnerUIDPath())
	return nil
}
