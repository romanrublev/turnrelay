package platform

import (
	"strings"
	"testing"
)

func TestLaunchdPlistContainsDaemonArgs(t *testing.T) {
	xml := LaunchdPlist("/usr/local/libexec/turnrelay/turnrelay")
	for _, want := range []string{
		"xyz.rublev.turnrelayd",
		"<string>/usr/local/libexec/turnrelay/turnrelay</string>",
		"<string>daemon</string>",
		"RunAtLoad",
		"KeepAlive",
	} {
		if !strings.Contains(xml, want) {
			t.Fatalf("plist missing %q:\n%s", want, xml)
		}
	}
}
