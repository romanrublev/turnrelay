package main

import (
	"encoding/json"
	"os"
	"testing"
)

func TestStatusUnmarshal(t *testing.T) {
	var s Status
	body := `{"running":true,"workers":18,"egress":"159.195.54.89","handshake_ok":true,"error":""}`
	if err := json.Unmarshal([]byte(body), &s); err != nil {
		t.Fatal(err)
	}
	if !s.Running || s.Workers != 18 || s.Egress != "159.195.54.89" || !s.HandshakeOK {
		t.Fatalf("bad parse: %+v", s)
	}
}

func TestBinaryPathEnvOverride(t *testing.T) {
	t.Setenv("TURNRELAY_BIN", "/custom/turnrelay")
	if got := binaryPath(); got != "/custom/turnrelay" {
		t.Fatalf("binaryPath=%q, want /custom/turnrelay", got)
	}
	_ = os.Unsetenv("TURNRELAY_BIN")
	if got := binaryPath(); got != "turnrelay" {
		t.Fatalf("binaryPath=%q, want turnrelay", got)
	}
}
