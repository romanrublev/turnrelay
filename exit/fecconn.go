package exit

import (
	"net"
	"os"
	"sync"
	"time"

	"github.com/pion/transport/v4/deadline"
)

// fecConn wraps the KCP-side pipe PacketConn with adaptive block FEC. KCP runs
// on top of it unchanged. Outbound KCP packets are grouped per destination into
// blocks; each data datagram is framed and sent, and parity datagrams are added
// when the block fills or a short timer fires. The parity count comes from a
// loss-driven tier, so protection is adaptive with no KCP reconnect. Inbound
// frames are reassembled (recovering lost data from parity) and the recovered
// KCP packets are delivered up.
//
// The sender chooses parity from its OWN received-loss estimate (a proxy for the
// forward path; a receiver->sender feedback loop is a later refinement). Server
// side is multi-peer: send blocks and reassemblers are keyed by address.
type fecConn struct {
	inner  net.PacketConn
	codec  *fecCodec
	ctrl   *tierController
	lossFn func() float64
	data   int           // data shards per block
	flush  time.Duration // max time a partial block waits

	mu    sync.Mutex
	send  map[string]*fecSendBlock
	saddr map[string]net.Addr
	reasm map[string]*blockReassembler
	nextB uint32

	deliv chan fecDelivered
	done  chan struct{}
	once  sync.Once
	rdl   *deadline.Deadline
}

type fecSendBlock struct {
	id      uint32
	bufs    [][]byte
	firstAt time.Time
}

type fecDelivered struct {
	b    []byte
	addr net.Addr
}

func newFECConn(inner net.PacketConn, lossFn func() float64) *fecConn {
	c := &fecConn{
		inner:  inner,
		codec:  newFECCodec(),
		ctrl:   newTierController(5 * time.Second),
		lossFn: lossFn,
		data:   10,
		flush:  15 * time.Millisecond,
		send:   map[string]*fecSendBlock{},
		saddr:  map[string]net.Addr{},
		reasm:  map[string]*blockReassembler{},
		deliv:  make(chan fecDelivered, 1024),
		done:   make(chan struct{}),
		rdl:    deadline.New(),
	}
	go c.readLoop()
	go c.flushLoop()
	return c
}

func (c *fecConn) parity() int {
	loss := 0.0
	if c.lossFn != nil {
		loss = c.lossFn()
	}
	tier, _ := c.ctrl.update(loss, time.Now())
	_, p := tierShards(tier)
	return p
}

func (c *fecConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	key := addr.String()
	c.mu.Lock()
	sb := c.send[key]
	if sb == nil {
		sb = &fecSendBlock{id: c.nextB, firstAt: time.Now()}
		c.nextB++
		c.send[key] = sb
		c.saddr[key] = addr
	}
	cp := make([]byte, len(b))
	copy(cp, b)
	sb.bufs = append(sb.bufs, cp)
	var frames [][]byte
	if len(sb.bufs) >= c.data {
		frames = c.sealLocked(key)
	}
	c.mu.Unlock()
	if err := c.sendFrames(frames, addr); err != nil {
		return 0, err
	}
	return len(b), nil
}

// sealLocked encodes and removes the current block for key; caller holds mu and
// sends the returned frames outside the lock.
func (c *fecConn) sealLocked(key string) [][]byte {
	sb := c.send[key]
	if sb == nil || len(sb.bufs) == 0 {
		return nil
	}
	frames, err := encodeBlock(c.codec, sb.id, sb.bufs, c.parity())
	delete(c.send, key)
	if err != nil {
		return nil
	}
	return frames
}

func (c *fecConn) sendFrames(frames [][]byte, addr net.Addr) error {
	for _, f := range frames {
		if _, err := c.inner.WriteTo(f, addr); err != nil {
			return err
		}
	}
	return nil
}

func (c *fecConn) flushLoop() {
	t := time.NewTicker(c.flush / 2)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case now := <-t.C:
			type pending struct {
				frames [][]byte
				addr   net.Addr
			}
			var out []pending
			c.mu.Lock()
			for key, sb := range c.send {
				if len(sb.bufs) > 0 && now.Sub(sb.firstAt) >= c.flush {
					out = append(out, pending{c.sealLocked(key), c.saddr[key]})
				}
			}
			c.mu.Unlock()
			for _, p := range out {
				_ = c.sendFrames(p.frames, p.addr)
			}
		}
	}
}

func (c *fecConn) reasmFor(key string) *blockReassembler {
	r := c.reasm[key]
	if r == nil {
		r = newBlockReassembler(c.codec, 128)
		c.reasm[key] = r
	}
	return r
}

func (c *fecConn) readLoop() {
	buf := make([]byte, 65535)
	for {
		n, addr, err := c.inner.ReadFrom(buf)
		if err != nil {
			c.Close()
			return
		}
		frame := make([]byte, n)
		copy(frame, buf[:n])
		c.mu.Lock()
		payloads := c.reasmFor(addr.String()).decode(frame)
		c.mu.Unlock()
		for _, p := range payloads {
			select {
			case c.deliv <- fecDelivered{b: p, addr: addr}:
			case <-c.done:
				return
			}
		}
	}
}

func (c *fecConn) ReadFrom(b []byte) (int, net.Addr, error) {
	select {
	case d := <-c.deliv:
		return copy(b, d.b), d.addr, nil
	case <-c.done:
		return 0, nil, net.ErrClosed
	case <-c.rdl.Done():
		return 0, nil, os.ErrDeadlineExceeded
	}
}

func (c *fecConn) Close() error {
	c.once.Do(func() {
		close(c.done)
		_ = c.inner.Close()
	})
	return nil
}

func (c *fecConn) LocalAddr() net.Addr               { return c.inner.LocalAddr() }
func (c *fecConn) SetDeadline(t time.Time) error     { c.rdl.Set(t); return nil }
func (c *fecConn) SetReadDeadline(t time.Time) error { c.rdl.Set(t); return nil }
func (c *fecConn) SetWriteDeadline(time.Time) error  { return nil }
