package daemon

import (
	"testing"

	"github.com/romanrublev/turnrelay/app/internal/profile"
)

func sampleProfile() profile.Profile {
	p := profile.Defaults()
	p.Link, p.Server, p.WGPrivateKey, p.WGPeerPublicKey = "https://vk.ru/call/join/X", "127.0.0.1:56004", "k", "pk"
	return p
}

func TestUpInvalidProfileErrors(t *testing.T) {
	d := New()
	if err := d.Up(profile.Profile{}); err == nil {
		t.Fatal("want error building config from empty profile")
	}
	if d.Status().Running {
		t.Fatal("should not be running after failed Up")
	}
}
