package vk

import "testing"

func TestParseCallLink(t *testing.T) {
	for in, want := range map[string]string{
		"https://vk.ru/call/join/AbCdEf123456":      "AbCdEf123456",
		"https://vk.com/call/join/AbCdEf123456?x=1": "AbCdEf123456",
		"  AbCdEf123456  ":                          "AbCdEf123456",
	} {
		got, err := ParseCallLink(in)
		if err != nil || got != want {
			t.Fatalf("%q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "https://vk.ru/call/join/", "short"} {
		if _, err := ParseCallLink(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}
