package obfs

import (
	"context"
	"errors"
	"io"
	"net"
	"time"
)

// WrapPacketConn applies the WDTT-WRAP-v1 envelope to every datagram of an
// underlying PacketConn. Exported so obfstest can build the server side.
//
// WriteTo is not safe for concurrent use: it reuses wbuf across calls. That
// is fine here because the only caller is pion/dtls's single writer goroutine,
// reached through the *dtls.Conn returned by dtlsHandshake, which serialises
// every write itself.
type WrapPacketConn struct {
	net.PacketConn
	codec *WrapCodec
	rbuf  []byte
	wbuf  []byte
}

func NewWrapPacketConn(inner net.PacketConn, codec *WrapCodec) *WrapPacketConn {
	return &WrapPacketConn{PacketConn: inner, codec: codec, rbuf: make([]byte, maxDatagram), wbuf: make([]byte, 0, maxDatagram)}
}

func (w *WrapPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	for {
		n, addr, err := w.PacketConn.ReadFrom(w.rbuf)
		if err != nil {
			// Drop an oversize datagram instead of surfacing a fatal error
			// to the DTLS layer above (one bad packet must not kill the conn).
			if errors.Is(err, io.ErrShortBuffer) {
				continue
			}
			return 0, nil, err
		}
		if !IsWrapRTP(w.rbuf[:n]) {
			continue
		}
		plain, err := w.codec.Unwrap(b[:0], w.rbuf[:n])
		if err != nil {
			continue // wrong key or corrupted; the DTLS layer above retransmits
		}
		return len(plain), addr, nil
	}
}

func (w *WrapPacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	wire, err := w.codec.Wrap(w.wbuf, b)
	if err != nil {
		return 0, err
	}
	if _, err := w.PacketConn.WriteTo(wire, addr); err != nil {
		return 0, err
	}
	return len(b), nil
}

type wrapWrapper struct {
	key     []byte
	video   bool
	timeout time.Duration
}

func (w *wrapWrapper) Client(ctx context.Context, underlay net.PacketConn, peer net.Addr) (net.Conn, error) {
	codec, err := NewWrapCodec(w.key, w.video)
	if err != nil {
		return nil, err
	}
	pc := NewWrapPacketConn(&peerPacketConn{underlay, peer}, codec)
	return dtlsHandshake(ctx, pc, peer, w.timeout)
}
