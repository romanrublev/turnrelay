// Package exit is the proxy-exit layer on top of the turnrelay datagram pipe:
// smux streams over KCP for TCP, framed datagrams for UDP, and the server
// that terminates both and dials out. See docs/superpowers specs and
// docs/protocol.md.
package exit

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"

	M "github.com/sagernet/sing/common/metadata"
)

// Discriminator byte, the first byte of every payload datagram on the pipe.
// 0xff is reserved for mux control frames, so payload can never look like one
// (a raw KCP packet starts with an arbitrary conv id and needs the prefix).
const (
	KindKCP byte = 0x00
	KindUDP byte = 0x01
	// CmdConnect is the only stream command.
	CmdConnect byte = 0x01
)

var ErrFrame = errors.New("exit: malformed frame")

// EncodeUDPFrame builds the body of a UDP datagram frame (the discriminator
// is added by Demux): [assoc][atyp][addr][port][payload].
func EncodeUDPFrame(assoc uint16, addr M.Socksaddr, payload []byte) ([]byte, error) {
	var b bytes.Buffer
	b.Grow(2 + M.SocksaddrSerializer.AddrPortLen(addr) + len(payload))
	var a [2]byte
	binary.BigEndian.PutUint16(a[:], assoc)
	b.Write(a[:])
	if err := M.SocksaddrSerializer.WriteAddrPort(&b, addr); err != nil {
		return nil, err
	}
	b.Write(payload)
	return b.Bytes(), nil
}

// DecodeUDPFrame parses a frame body. payload aliases b.
func DecodeUDPFrame(b []byte) (assoc uint16, addr M.Socksaddr, payload []byte, err error) {
	if len(b) < 2 {
		return 0, M.Socksaddr{}, nil, ErrFrame
	}
	assoc = binary.BigEndian.Uint16(b[:2])
	r := bytes.NewReader(b[2:])
	addr, err = M.SocksaddrSerializer.ReadAddrPort(r)
	if err != nil || !addr.IsValid() {
		return 0, M.Socksaddr{}, nil, ErrFrame
	}
	return assoc, addr, b[len(b)-r.Len():], nil
}

// WriteStreamHeader is sent once at the start of every smux stream.
func WriteStreamHeader(w io.Writer, addr M.Socksaddr) error {
	var b bytes.Buffer
	b.WriteByte(CmdConnect)
	if err := M.SocksaddrSerializer.WriteAddrPort(&b, addr); err != nil {
		return err
	}
	_, err := w.Write(b.Bytes())
	return err
}

func ReadStreamHeader(r io.Reader) (M.Socksaddr, error) {
	var cmd [1]byte
	if _, err := io.ReadFull(r, cmd[:]); err != nil {
		return M.Socksaddr{}, err
	}
	if cmd[0] != CmdConnect {
		return M.Socksaddr{}, ErrFrame
	}
	addr, err := M.SocksaddrSerializer.ReadAddrPort(r)
	if err != nil {
		return M.Socksaddr{}, err
	}
	if !addr.IsValid() {
		return M.Socksaddr{}, ErrFrame
	}
	return addr, nil
}
