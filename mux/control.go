// Package mux runs N obfuscated TURN allocations as one datagram pipe.
package mux

import "encoding/binary"

// Control frames start with 0xff, which is outside WireGuard's message type
// range (1..4); a server that predates them forwards them to WireGuard,
// which drops them as malformed. Formats follow anton48/vk-turn-proxy
// (server/group.go, probe-echo in server/main.go).
const (
	HelloLen = 20
	ProbeLen = 12
)

var (
	helloMagic = [4]byte{0xff, 'G', 'R', 'P'}
	probeMagic = [4]byte{0xff, 'P', 'N', 'G'}
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

func IsControl(b []byte) bool {
	if len(b) < 4 || b[0] != 0xff {
		return false
	}
	m := [4]byte(b[:4])
	return m == helloMagic || m == probeMagic
}
