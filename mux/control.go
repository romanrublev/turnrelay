// Package mux runs N obfuscated TURN allocations as one datagram pipe.
package mux

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
)

// Control frames start with 0xff, which is outside WireGuard's message type
// range (1..4); a server that predates them forwards them to WireGuard,
// which drops them as malformed. Formats follow anton48/vk-turn-proxy
// (server/group.go, probe-echo in server/main.go).
const (
	HelloLen = 20
	ProbeLen = 12
	// AuthLen is the auth frame: magic plus a 16-byte tag.
	AuthLen = 20
)

var (
	helloMagic = [4]byte{0xff, 'G', 'R', 'P'}
	probeMagic = [4]byte{0xff, 'P', 'N', 'G'}
	authMagic  = [4]byte{0xff, 'A', 'U', 'T'}
)

func EncodeHello(session [16]byte) []byte {
	b := make([]byte, HelloLen)
	copy(b, helloMagic[:])
	copy(b[4:], session[:])
	return b
}

func ParseHello(b []byte) (session [16]byte, ok bool) {
	if len(b) != HelloLen || [4]byte(b[:4]) != helloMagic {
		return session, false
	}
	copy(session[:], b[4:])
	return session, true
}

func EncodeProbe(seq uint64) []byte {
	b := make([]byte, ProbeLen)
	copy(b, probeMagic[:])
	binary.BigEndian.PutUint64(b[4:], seq)
	return b
}

func ParseProbe(b []byte) (uint64, bool) {
	if len(b) < ProbeLen || [4]byte(b[:4]) != probeMagic {
		return 0, false
	}
	return binary.BigEndian.Uint64(b[4:12]), true
}

// AuthTag is the session authenticator a worker sends right after hello:
// HMAC-SHA256 keyed with the pre-shared password over the session UUID,
// truncated to 16 bytes. The exit server verifies it before it lets an
// allocation join a session; without it the server would be an open proxy.
func AuthTag(password string, session [16]byte) []byte {
	m := hmac.New(sha256.New, []byte(password))
	m.Write(session[:])
	return m.Sum(nil)[:16]
}

func EncodeAuth(tag []byte) []byte {
	b := make([]byte, AuthLen)
	copy(b, authMagic[:])
	copy(b[4:], tag)
	return b
}

func ParseAuth(b []byte) (tag []byte, ok bool) {
	if len(b) != AuthLen || [4]byte(b[:4]) != authMagic {
		return nil, false
	}
	return b[4:], true
}

// VerifyAuth is constant-time.
func VerifyAuth(password string, session [16]byte, tag []byte) bool {
	return hmac.Equal(tag, AuthTag(password, session))
}

func IsControl(b []byte) bool {
	if len(b) < 4 || b[0] != 0xff {
		return false
	}
	m := [4]byte(b[:4])
	return m == helloMagic || m == probeMagic || m == authMagic
}
