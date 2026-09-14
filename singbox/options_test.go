package singbox

import (
	"encoding/json"
	"testing"

	"github.com/romanrublev/turnrelay"
)

func TestOptionsToConfigDefaults(t *testing.T) {
	const raw = `{
		"provider": "vk",
		"call_link": "https://vk.ru/call/join/TESTLINK1234",
		"server": "203.0.113.5", "server_port": 56004
	}`
	var o TurnrelayOutboundOptions
	if err := json.Unmarshal([]byte(raw), &o); err != nil {
		t.Fatal(err)
	}
	cfg, err := o.toConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != "vk" {
		t.Fatalf("provider %q", cfg.Provider)
	}
	if len(cfg.CallLinks) != 1 || cfg.CallLinks[0] != "https://vk.ru/call/join/TESTLINK1234" {
		t.Fatalf("call links %v", cfg.CallLinks)
	}
	if cfg.Server.String() != "203.0.113.5:56004" {
		t.Fatalf("server %s", cfg.Server)
	}
	// New applies the real defaults; the mapping leaves these zero-valued.
	if cfg.Connections != 0 || cfg.Mode != "" {
		t.Fatalf("mapping should not pre-fill defaults: %+v", cfg)
	}
}

func TestOptionsToConfigMergesLinksAndFields(t *testing.T) {
	const raw = `{
		"call_link": "https://vk.ru/call/join/AAAA",
		"call_links": ["https://vk.ru/call/join/BBBB"],
		"server": "203.0.113.5", "server_port": 56004,
		"connections": 40, "mode": "wrap", "password": "pw", "captcha": "fail",
		"udp": false
	}`
	var o TurnrelayOutboundOptions
	if err := json.Unmarshal([]byte(raw), &o); err != nil {
		t.Fatal(err)
	}
	cfg, err := o.toConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.CallLinks) != 2 || cfg.CallLinks[0] != "https://vk.ru/call/join/AAAA" || cfg.CallLinks[1] != "https://vk.ru/call/join/BBBB" {
		t.Fatalf("links %v", cfg.CallLinks)
	}
	if cfg.Connections != 40 || cfg.Mode != turnrelay.ModeWrap || cfg.Password != "pw" {
		t.Fatalf("cfg %+v", cfg)
	}
	if cfg.Captcha != turnrelay.CaptchaFail {
		t.Fatalf("captcha %q", cfg.Captcha)
	}
	if cfg.TURNUDP == nil || *cfg.TURNUDP != false {
		t.Fatalf("udp %v", cfg.TURNUDP)
	}
}

func TestOptionsToConfigRejectsNonIPServer(t *testing.T) {
	const raw = `{"call_link": "x", "server": "vps.example.com", "server_port": 56004}`
	var o TurnrelayOutboundOptions
	if err := json.Unmarshal([]byte(raw), &o); err != nil {
		t.Fatal(err)
	}
	if _, err := o.toConfig(); err == nil {
		t.Fatal("a non-IP server must be rejected")
	}
}
