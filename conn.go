package turnrelay

import (
	"context"
	"net"
	"os"
	"sync"
	"time"
)

// datagramConn is one consumer of the shared pipe. Several may exist (the
// WireGuard bind re-dials after errors); all read from the same downlink.
type datagramConn struct {
	d      *Dialer
	mu     sync.Mutex
	rctx   context.Context
	rstop  context.CancelFunc
	closed chan struct{}
	once   sync.Once
}

func newDatagramConn(d *Dialer) *datagramConn {
	c := &datagramConn{d: d, closed: make(chan struct{})}
	c.rctx, c.rstop = context.WithCancel(context.Background())
	return c
}

func (c *datagramConn) readCtx() context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rctx
}

func (c *datagramConn) Read(b []byte) (int, error) {
	select {
	case <-c.closed:
		return 0, net.ErrClosed
	default:
	}
	pkt, err := c.d.pool.Read(c.readCtx())
	if err != nil {
		if c.readCtx().Err() != nil {
			select {
			case <-c.closed:
				return 0, net.ErrClosed
			default:
				return 0, os.ErrDeadlineExceeded
			}
		}
		return 0, err
	}
	return copy(b, pkt), nil
}

func (c *datagramConn) Write(b []byte) (int, error) {
	select {
	case <-c.closed:
		return 0, net.ErrClosed
	default:
	}
	if err := c.d.pool.Write(context.Background(), b); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (c *datagramConn) Close() error {
	c.once.Do(func() { close(c.closed); c.rstop() })
	return nil
}

func (c *datagramConn) LocalAddr() net.Addr  { return &net.UDPAddr{IP: net.IPv4zero} }
func (c *datagramConn) RemoteAddr() net.Addr { return c.d.peer }

func (c *datagramConn) SetDeadline(t time.Time) error    { return c.SetReadDeadline(t) }
func (c *datagramConn) SetWriteDeadline(time.Time) error { return nil }

// SetReadDeadline cancels any in-flight Read's context before installing the
// new one. Even the zero-time reset therefore interrupts a currently-blocked
// Read, which comes back with os.ErrDeadlineExceeded rather than continuing
// to block. pion and wireguard-go only call SetReadDeadline(time.Now()) to
// interrupt reads on shutdown, so this matches what they expect.
func (c *datagramConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rstop()
	if t.IsZero() {
		c.rctx, c.rstop = context.WithCancel(context.Background())
	} else {
		c.rctx, c.rstop = context.WithDeadline(context.Background(), t)
	}
	return nil
}

type packetConn struct{ *datagramConn }

func (p *packetConn) ReadFrom(b []byte) (int, net.Addr, error) {
	n, err := p.Read(b)
	return n, p.d.peer, err
}

func (p *packetConn) WriteTo(b []byte, _ net.Addr) (int, error) { return p.Write(b) }
