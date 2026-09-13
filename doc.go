// Package turnrelay presents N TURN allocations, each wrapped in an
// obfuscation layer that mimics WebRTC media, as a single UDP datagram pipe.
//
// The Dialer implements the DialContext/ListenPacket pair used by sing-box
// (N.Dialer) for the "udp" network only: every Write becomes one datagram
// striped over the allocation pool, every Read returns one merged downlink
// datagram. Higher layers (WireGuard in sing-box) provide reliability and
// ordering.
package turnrelay
