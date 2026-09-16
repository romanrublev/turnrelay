package exit

import (
	"encoding/binary"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/transport/v4/deadline"
)

// Demux splits one datagram PacketConn into a KCP side and a UDP side by the
// discriminator byte, preserving the peer address (the server on the client,
// the session address on the server). Both sides write through the same
// underlying conn and prepend their own discriminator.
type Demux struct {
	pc      net.PacketConn
	kcp     *sideConn
	udp     *sideConn
	done    chan struct{}
	once    sync.Once
	sendSeq atomic.Uint32  // per-datagram sequence for KCP-side frames we send
	est     *lossEstimator // loss estimate from the KCP-side sequence we receive
}

type packet struct {
	b    []byte
	addr net.Addr
}

type sideConn struct {
	d    *Demux
	kind byte
	in   chan packet
	dl   *deadline.Deadline
}

func NewDemux(pc net.PacketConn) *Demux {
	d := &Demux{pc: pc, done: make(chan struct{}), est: newLossEstimator(256, 0.3)}
	d.kcp = &sideConn{d: d, kind: KindKCP, in: make(chan packet, 1024), dl: deadline.New()}
	d.udp = &sideConn{d: d, kind: KindUDP, in: make(chan packet, 1024), dl: deadline.New()}
	go d.loop()
	return d
}

func (d *Demux) KCP() net.PacketConn { return d.kcp }
func (d *Demux) UDP() net.PacketConn { return d.udp }

func (d *Demux) loop() {
	buf := make([]byte, 65535)
	for {
		n, addr, err := d.pc.ReadFrom(buf)
		if err != nil {
			_ = d.Close()
			return
		}
		if n < 2 {
			continue
		}
		var side *sideConn
		var pkt []byte
		switch buf[0] {
		case KindKCP:
			// KCP-side frames carry a 4-byte sequence after the discriminator,
			// used only to estimate the pipe's loss rate; it is stripped here.
			if n < 5 {
				continue
			}
			d.est.observe(binary.BigEndian.Uint32(buf[1:5]))
			side = d.kcp
			pkt = make([]byte, n-5)
			copy(pkt, buf[5:n])
		case KindUDP:
			side = d.udp
			pkt = make([]byte, n-1)
			copy(pkt, buf[1:n])
		default:
			continue
		}
		select {
		case side.in <- packet{b: pkt, addr: addr}:
		case <-d.done:
			return
		default:
			// Datagram semantics: drop under backpressure; KCP retransmits.
		}
	}
}

func (d *Demux) Close() error {
	d.once.Do(func() {
		close(d.done)
		_ = d.pc.Close()
	})
	return nil
}

func (s *sideConn) ReadFrom(b []byte) (int, net.Addr, error) {
	select {
	case p := <-s.in:
		return copy(b, p.b), p.addr, nil
	case <-s.d.done:
		return 0, nil, net.ErrClosed
	case <-s.dl.Done():
		return 0, nil, os.ErrDeadlineExceeded
	}
}

func (s *sideConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	var out []byte
	if s.kind == KindKCP {
		out = make([]byte, len(b)+5)
		out[0] = s.kind
		binary.BigEndian.PutUint32(out[1:5], s.d.sendSeq.Add(1)-1)
		copy(out[5:], b)
	} else {
		out = make([]byte, len(b)+1)
		out[0] = s.kind
		copy(out[1:], b)
	}
	if _, err := s.d.pc.WriteTo(out, addr); err != nil {
		return 0, err
	}
	return len(b), nil
}

// LossRate is the smoothed loss estimate of the incoming KCP-side pipe, in
// [0,1], derived from gaps in the received sequence numbers.
func (d *Demux) LossRate() float64 { return d.est.rate() }

func (s *sideConn) Close() error                      { return s.d.Close() }
func (s *sideConn) LocalAddr() net.Addr               { return s.d.pc.LocalAddr() }
func (s *sideConn) SetDeadline(t time.Time) error     { s.dl.Set(t); return nil }
func (s *sideConn) SetReadDeadline(t time.Time) error { s.dl.Set(t); return nil }
func (s *sideConn) SetWriteDeadline(time.Time) error  { return nil }
