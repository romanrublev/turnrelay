package platform

import (
	"strings"
	"testing"
)

func TestSystemdUnitContainsDaemonArgs(t *testing.T) {
	unit := SystemdUnit("/usr/local/libexec/turnrelay/turnrelay")
	for _, want := range []string{
		"Description=turnrelay VPN daemon",
		"ExecStart=/usr/local/libexec/turnrelay/turnrelay daemon",
		"Restart=on-failure",
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit missing %q:\n%s", want, unit)
		}
	}
}
