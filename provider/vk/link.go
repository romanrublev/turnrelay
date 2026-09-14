package vk

import (
	"errors"
	"strings"
)

// ParseCallLink accepts a full VK call link on vk.ru or vk.com, or a bare
// hash, and returns the hash.
func ParseCallLink(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("vk: empty call link")
	}
	if i := strings.Index(s, "join/"); i >= 0 {
		s = s[i+len("join/"):]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if len(s) < 8 || strings.ContainsAny(s, " \t") {
		return "", errors.New("vk: malformed call link")
	}
	return s, nil
}
