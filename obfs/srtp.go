package obfs

// DTLS-SRTP mode. Independent implementation from RFC 3550, RFC 3711 and
// RFC 5764 with pion/dtls, pion/rtp and pion/srtp, following the framing of
// anton48/vk-turn-proxy-ios pkg/proxy/srtpwrap (MIT): PT 100, demux on the
// first byte, one RTP packet per datagram.

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/pion/dtls/v3"
	"github.com/pion/rtp"
	"github.com/pion/srtp/v3"
	"github.com/pion/transport/v4/deadline"
)

const (
	srtpPayloadType uint8 = 100
	srtpProfile           = srtp.ProtectionProfileAes128CmHmacSha1_80
	// maxDatagram is the largest UDP payload; read buffers are sized to it
	// so an oversize datagram is never truncated into a fatal error.
	maxDatagram = 65535
)

func isDTLSByte(b byte) bool { return b >= 20 && b <= 63 }
func isRTPByte(b byte) bool  { return b >= 128 && b <= 191 }

type srtpWrapper struct {
	timeout time.Duration
	pin     []byte
}

func (w *srtpWrapper) Client(ctx context.Context, underlay net.PacketConn, peer net.Addr) (net.Conn, error) {
	d := newDemux(underlay, peer)
	dc, err := dtlsHandshake(ctx, d.dtlsSide(), peer, w.timeout, w.pin,
		dtls.WithSRTPProtectionProfiles(dtls.SRTP_AES128_CM_HMAC_SHA1_80))
	if err != nil {
		d.Close()
		return nil, err
	}
	c, err := newSRTPConn(d, dc, true)
	if err != nil {
		_ = dc.Close()
		d.Close()
		return nil, err
	}
	return c, nil
}

// demux reads the underlay once and splits packets between the DTLS
// handshake and the SRTP data path by first byte.
type demux struct {
	raw    net.PacketConn
	peer   net.Addr
	dtlsCh chan []byte
	rtpCh  chan []byte
	done   chan struct{}
	once   sync.Once
	// ownsRaw is false for a demux built over a socket shared by other
	// sessions (obfstest's per-source server demux): Close must not close
	// or nudge the deadline of a socket other sessions still read from.
	ownsRaw bool
}

func newDemux(raw net.PacketConn, peer net.Addr) *demux {
	d := &demux{raw: raw, peer: peer, dtlsCh: make(chan []byte, 64), rtpCh: make(chan []byte, 2048), done: make(chan struct{}), ownsRaw: true}
	go d.loop()
	return d
}

// newDemuxFromChannels builds a demux whose dtlsCh/rtpCh are fed externally
// (by a shared listener loop keyed on source address) instead of by loop.
// Used by obfstest's per-source server sessions; loop is never started, and
// Close never touches raw since it is shared with other sessions.
func newDemuxFromChannels(raw net.PacketConn, peer net.Addr, dtlsCh, rtpCh chan []byte) *demux {
	return &demux{raw: raw, peer: peer, dtlsCh: dtlsCh, rtpCh: rtpCh, done: make(chan struct{})}
}

func (d *demux) loop() {
	buf := make([]byte, maxDatagram)
	for {
		n, _, err := d.raw.ReadFrom(buf)
		if err != nil {
			select {
			case <-d.done:
				return
			default:
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				_ = d.raw.SetReadDeadline(time.Time{})
				continue
			}
			// An oversize datagram truncates against the buffer on some
			// PacketConn implementations; drop it rather than tearing the
			// whole demux down (otherwise one bad packet is a DoS).
			if errors.Is(err, io.ErrShortBuffer) {
				continue
			}
			d.Close()
			return
		}
		if n == 0 {
			continue
		}
		pkt := make([]byte, n)
		copy(pkt, buf[:n])
		var ch chan []byte
		switch {
		case isDTLSByte(pkt[0]):
			ch = d.dtlsCh
		case isRTPByte(pkt[0]):
			ch = d.rtpCh
		default:
			continue
		}
		select {
		case ch <- pkt:
		case <-d.done:
			return
		}
	}
}

func (d *demux) Close() {
	d.once.Do(func() {
		close(d.done)
		if d.ownsRaw {
			_ = d.raw.SetReadDeadline(time.Now())
			_ = d.raw.Close()
		}
	})
}

// dtlsSide is the PacketConn handed to pion/dtls: reads come from dtlsCh,
// writes go straight to the peer.
func (d *demux) dtlsSide() net.PacketConn { return &demuxSide{d: d, dl: deadline.New()} }

// demuxSide's deadline uses github.com/pion/transport/v4/deadline instead of
// a home-grown timer+channel: a naive "snapshot the channel, then select on
// it" implementation is racy against a concurrent SetReadDeadline, because
// SetReadDeadline would allocate a *new* channel and close that one, leaving
// a read already parked in ReadFrom watching the old (often nil, i.e.
// never-firing) channel. pion/dtls relies on exactly this to cancel a
// blocked handshake read (it calls SetReadDeadline with a past time on the
// pending read and waits for the reader to return), so that race meant a
// handshake against a silent peer never timed out. deadline.Deadline avoids
// it: Set closes the very channel a caller already obtained from Done,
// reallocating only after a previous deadline already fired.
type demuxSide struct {
	d  *demux
	dl *deadline.Deadline
}

func (s *demuxSide) ReadFrom(b []byte) (int, net.Addr, error) {
	select {
	case pkt := <-s.d.dtlsCh:
		return copy(b, pkt), s.d.peer, nil
	case <-s.d.done:
		return 0, nil, net.ErrClosed
	case <-s.dl.Done():
		return 0, nil, os.ErrDeadlineExceeded
	}
}

func (s *demuxSide) WriteTo(b []byte, _ net.Addr) (int, error) { return s.d.raw.WriteTo(b, s.d.peer) }
func (s *demuxSide) Close() error                              { return nil }
func (s *demuxSide) LocalAddr() net.Addr                       { return s.d.raw.LocalAddr() }
func (s *demuxSide) SetDeadline(t time.Time) error             { return s.SetReadDeadline(t) }
func (s *demuxSide) SetWriteDeadline(time.Time) error          { return nil }

func (s *demuxSide) SetReadDeadline(t time.Time) error {
	s.dl.Set(t)
	return nil
}

// srtpConn is the net.Conn returned to the mux: RTP framing on Write,
// SRTP unprotect on Read.
type srtpConn struct {
	d      *demux
	dc     *dtls.Conn
	enc    *srtp.Context
	dec    *srtp.Context
	ssrc   uint32
	wmu    sync.Mutex
	seq    uint16
	ts     uint32
	dl     *deadline.Deadline // see demuxSide's doc comment for why this type
	closed chan struct{}
	once   sync.Once
}

func newSRTPConn(d *demux, dc *dtls.Conn, isClient bool) (*srtpConn, error) {
	state, ok := dc.ConnectionState()
	if !ok {
		return nil, errors.New("obfs: dtls state unavailable")
	}
	cfg := &srtp.Config{Profile: srtpProfile}
	if err := cfg.ExtractSessionKeysFromDTLS(&state, isClient); err != nil {
		return nil, fmt.Errorf("obfs: srtp keys: %w", err)
	}
	enc, err := srtp.CreateContext(cfg.Keys.LocalMasterKey, cfg.Keys.LocalMasterSalt, cfg.Profile)
	if err != nil {
		return nil, err
	}
	// Replay protection on the receive context: a captured SRTP packet
	// replayed within the window is dropped (RFC 3711 section 3.3.2).
	dec, err := srtp.CreateContext(cfg.Keys.RemoteMasterKey, cfg.Keys.RemoteMasterSalt, cfg.Profile, srtp.SRTPReplayProtection(64))
	if err != nil {
		return nil, err
	}
	// RFC 3550 section 5.1: the initial sequence number and timestamp are
	// random, so a stream does not announce itself by starting at zero.
	var seed [10]byte
	_, _ = rand.Read(seed[:])
	return &srtpConn{d: d, dc: dc, enc: enc, dec: dec,
		ssrc: binary.BigEndian.Uint32(seed[0:4]),
		seq:  binary.BigEndian.Uint16(seed[4:6]),
		ts:   binary.BigEndian.Uint32(seed[6:10]),
		dl:   deadline.New(), closed: make(chan struct{})}, nil
}

func (c *srtpConn) Read(b []byte) (int, error) {
	for {
		select {
		case pkt := <-c.d.rtpCh:
			plain, err := c.dec.DecryptRTP(nil, pkt, nil)
			if err != nil {
				continue
			}
			var h rtp.Header
			n, err := h.Unmarshal(plain)
			if err != nil {
				continue
			}
			return copy(b, plain[n:]), nil
		case <-c.closed:
			return 0, net.ErrClosed
		case <-c.d.done:
			return 0, net.ErrClosed
		case <-c.dl.Done():
			return 0, os.ErrDeadlineExceeded
		}
	}
}

func (c *srtpConn) Write(b []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	pkt := rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: srtpPayloadType, SequenceNumber: c.seq, Timestamp: c.ts, SSRC: c.ssrc}, Payload: b}
	c.seq++
	c.ts += uint32(len(b))
	raw, err := pkt.Marshal()
	if err != nil {
		return 0, err
	}
	enc, err := c.enc.EncryptRTP(nil, raw, nil)
	if err != nil {
		return 0, err
	}
	if _, err := c.d.raw.WriteTo(enc, c.d.peer); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (c *srtpConn) Close() error {
	c.once.Do(func() {
		close(c.closed)
		_ = c.dc.Close()
		c.d.Close()
	})
	return nil
}

func (c *srtpConn) LocalAddr() net.Addr  { return c.d.raw.LocalAddr() }
func (c *srtpConn) RemoteAddr() net.Addr { return c.d.peer }
func (c *srtpConn) SetDeadline(t time.Time) error {
	c.dl.Set(t)
	return nil
}
func (c *srtpConn) SetReadDeadline(t time.Time) error {
	c.dl.Set(t)
	return nil
}
func (c *srtpConn) SetWriteDeadline(time.Time) error { return nil }

// NewSRTPServerConn is test support, not API-stable. It builds the
// server-side twin of srtpConn for obfstest.ListenSRTP: raw is the shared
// per-socket listener, src identifies this session's peer, dc is an already
// handshaked DTLS connection for that session, and rtpCh delivers the
// session's SRTP-framed datagrams as obfstest's dispatch loop demuxes them
// off the shared socket. isClient is false so ExtractSessionKeysFromDTLS
// derives the server's read/write directions.
//
// rtpCh arrives receive-only (the caller keeps the send side to feed it from
// its dispatch loop), but demux.rtpCh must stay bidirectional so the client
// path's own loop can send into it; a forwarding goroutine bridges the two
// so srtpConn.Read can keep reading via c.d.rtpCh unchanged. The returned
// conn's Close never closes raw (see demux.ownsRaw): the socket is shared by
// every other session on the server.
func NewSRTPServerConn(raw net.PacketConn, src net.Addr, dc *dtls.Conn, rtpCh <-chan []byte) (net.Conn, error) {
	d := newDemuxFromChannels(raw, src, nil, make(chan []byte))
	go func() {
		for {
			select {
			case pkt, ok := <-rtpCh:
				if !ok {
					return
				}
				select {
				case d.rtpCh <- pkt:
				case <-d.done:
					return
				}
			case <-d.done:
				return
			}
		}
	}()
	return newSRTPConn(d, dc, false)
}
