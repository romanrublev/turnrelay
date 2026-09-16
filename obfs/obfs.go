// Package obfs turns a TURN-relayed net.PacketConn into a net.Conn whose
// datagrams look like WebRTC media to the relay's traffic classifier.
package obfs

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

type Mode string

const (
	ModeSRTP Mode = "srtp" // DTLS-SRTP, RFC 5764 keying, RTP PT 100
	ModeWrap Mode = "wrap" // WDTT-WRAP-v1 envelope around plain DTLS
	ModeDTLS Mode = "dtls" // plain DTLS 1.2; deprecated, shaped by VK relays
)

type Options struct {
	Password         string // ModeWrap: HKDF input, ignored if WrapKey is set
	WrapKey          []byte // ModeWrap: raw 32-byte key
	Video            bool   // ModeWrap: PT 96 / larger padding instead of PT 111
	HandshakeTimeout time.Duration
	// ServerFingerprint, when set, is the SHA-256 of the exit server's DTLS
	// leaf certificate (see CertFingerprint). The client verifies the server
	// against it, authenticating the server and stopping an on-path MITM.
	// Empty keeps the legacy unauthenticated behaviour.
	ServerFingerprint []byte
}

// Wrapper runs the mode's handshake over underlay towards peer and returns a
// datagram-preserving net.Conn: one Write is one wire packet, one Read is one
// decoded packet. Implementations own underlay after Client returns.
type Wrapper interface {
	Client(ctx context.Context, underlay net.PacketConn, peer net.Addr) (net.Conn, error)
}

const defaultHandshakeTimeout = 20 * time.Second

func New(mode Mode, o Options) (Wrapper, error) {
	if o.HandshakeTimeout == 0 {
		o.HandshakeTimeout = defaultHandshakeTimeout
	}
	switch mode {
	case ModeDTLS:
		return &dtlsWrapper{timeout: o.HandshakeTimeout, pin: o.ServerFingerprint}, nil
	case ModeSRTP:
		return &srtpWrapper{timeout: o.HandshakeTimeout, pin: o.ServerFingerprint}, nil
	case ModeWrap:
		key := o.WrapKey
		if key == nil {
			var err error
			if key, err = DeriveWrapKey(o.Password); err != nil {
				return nil, err
			}
		}
		if len(key) != WrapKeyLen {
			return nil, fmt.Errorf("obfs: wrap key must be %d bytes", WrapKeyLen)
		}
		return &wrapWrapper{key: key, video: o.Video, timeout: o.HandshakeTimeout, pin: o.ServerFingerprint}, nil
	default:
		return nil, errors.New("obfs: unknown mode " + string(mode))
	}
}

// peerPacketConn pins every WriteTo to one peer; pion/turn's relayed conn
// needs the peer address on each write, DTLS gives it once.
type peerPacketConn struct {
	net.PacketConn
	peer net.Addr
}

func (p *peerPacketConn) WriteTo(b []byte, _ net.Addr) (int, error) {
	return p.PacketConn.WriteTo(b, p.peer)
}
