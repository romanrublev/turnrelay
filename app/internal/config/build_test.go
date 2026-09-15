package config

import (
	"encoding/json"
	"testing"

	"github.com/romanrublev/turnrelay/app/internal/profile"
)

func sampleProfile() profile.Profile {
	p := profile.Defaults()
	p.Link = "https://vk.ru/call/join/X"
	p.Server = "159.195.54.89:56004"
	p.WGPrivateKey = "PRIV"
	p.WGPeerPublicKey = "PUB"
	return p
}

func TestBuildShape(t *testing.T) {
	b, err := Build(sampleProfile())
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("not valid json: %v", err)
	}
	// tun inbound
	ins := m["inbounds"].([]any)
	if ins[0].(map[string]any)["type"] != "tun" {
		t.Fatalf("first inbound not tun: %v", ins[0])
	}
	// turnrelay outbound carries the link and server host+port
	obs := m["outbounds"].([]any)
	relay := obs[0].(map[string]any)
	if relay["type"] != "turnrelay" || relay["call_link"] != "https://vk.ru/call/join/X" {
		t.Fatalf("relay wrong: %v", relay)
	}
	if relay["server"] != "159.195.54.89" || relay["server_port"].(float64) != 56004 {
		t.Fatalf("relay server split wrong: %v", relay)
	}
	// wg endpoint detours through relay
	eps := m["endpoints"].([]any)
	wg := eps[0].(map[string]any)
	if wg["type"] != "wireguard" || wg["detour"] != "relay" {
		t.Fatalf("wg wrong: %v", wg)
	}
	if m["route"].(map[string]any)["final"] != "wg" {
		t.Fatalf("route.final not wg")
	}
}

func TestBuildRejectsInvalidProfile(t *testing.T) {
	if _, err := Build(profile.Profile{}); err == nil {
		t.Fatal("want error for empty profile")
	}
}

func TestBuildExitMode(t *testing.T) {
	p := sampleProfile()
	p.ServerType = "exit"
	p.Password = "secret"
	b, err := Build(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if _, hasEP := m["endpoints"]; hasEP {
		t.Fatal("exit mode must not emit a wireguard endpoint")
	}
	relay := m["outbounds"].([]any)[0].(map[string]any)
	if relay["server_type"] != "exit" || relay["password"] != "secret" {
		t.Fatalf("relay exit fields wrong: %v", relay)
	}
	if m["route"].(map[string]any)["final"] != "relay" {
		t.Fatalf("exit route.final should be relay")
	}
	dns0 := m["dns"].(map[string]any)["servers"].([]any)[0].(map[string]any)
	if dns0["detour"] != "relay" {
		t.Fatalf("exit remote dns detour should be relay, got %v", dns0["detour"])
	}
}
