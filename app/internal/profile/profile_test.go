package profile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingReturnsDefaults(t *testing.T) {
	p, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if !os.IsNotExist(err) {
		t.Fatalf("err=%v, want IsNotExist", err)
	}
	if p.Connections != 18 || p.Mode != "srtp" || p.WGAddress != "10.8.0.2/32" {
		t.Fatalf("defaults wrong: %+v", p)
	}
}

func TestLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.json")
	body := `{"link":"L","server":"S:1","connections":8,"mode":"srtp","wg_private_key":"k","wg_peer_public_key":"pk","wg_address":"10.8.0.2/32"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.Link != "L" || p.Server != "S:1" || p.Connections != 8 {
		t.Fatalf("bad parse: %+v", p)
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestValidateMissingFields(t *testing.T) {
	if err := (Profile{Link: "L"}).Validate(); err == nil {
		t.Fatal("want error for missing server/keys")
	}
}
