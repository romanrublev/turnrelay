package exit

import (
	"context"
	"errors"
	"math/rand/v2"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/pion/transport/v4/deadline"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/xtaci/kcp-go/v5"
	"github.com/xtaci/smux"
)

var ErrNetwork = errors.New("exit: DialContext supports tcp only; use ListenPacket for udp")

type ClientOptions struct {
	Logf func(string, ...any)
}

// Client is the proxy side of the exit protocol over one datagram pipe to
// the exit server: TCP through smux streams over a lazily established KCP
// session, UDP through framed datagrams with one association per
// ListenPacket conn.
type Client struct {
	pipe   net.PacketConn
	server net.Addr
	demux  *Demux
	o      ClientOptions

	mu   sync.Mutex
	ks   *kcp.UDPSession
	sess *smux.Session

	amu    sync.Mutex
	assocs map[uint16]*clientAssoc
	next   uint16

	closed chan struct{}
	once   sync.Once
}

func NewClient(pipe net.PacketConn, server net.Addr, o ClientOptions) *Client {
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	c := &Client{pipe: pipe, server: server, demux: NewDemux(pipe), o: o, assocs: map[uint16]*clientAssoc{}, closed: make(chan struct{})}
	c.next = uint16(rand.Uint32())
	go c.udpLoop()
	return c
}

func (c *Client) Close() error {
	c.once.Do(func() {
		close(c.closed)
		c.mu.Lock()
		if c.sess != nil {
			_ = c.sess.Close()
		}
		if c.ks != nil {
			_ = c.ks.Close()
		}
		c.ks, c.sess = nil, nil
		c.mu.Unlock()
		_ = c.demux.Close()
	})
	return nil
}

// session returns the live smux session, (re)establishing KCP and smux when
// there is none or the previous one died. It fails fast once the client is
// closed, instead of standing up an orphaned session that Close will never
// tear down.
func (c *Client) session() (*smux.Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.closed:
		return nil, net.ErrClosed
	default:
	}
	if c.sess != nil && !c.sess.IsClosed() {
		return c.sess, nil
	}
	if c.ks != nil {
		_ = c.ks.Close()
		c.ks, c.sess = nil, nil
	}
	// Adaptive block FEC wraps the pipe; kcp-go's own FEC is off (0,0).
	pipe := newFECConn(c.demux.KCP(), c.demux.LossRate)
	ks, err := kcp.NewConn3(rand.Uint32(), c.server, nil, 0, 0, pipe)
	if err != nil {
		return nil, err
	}
	tuneKCP(ks)
	sess, err := smux.Client(ks, smuxConfig())
	if err != nil {
		_ = ks.Close()
		return nil, err
	}
	c.ks, c.sess = ks, sess
	c.o.Logf("exit: session established")
	return sess, nil
}

func (c *Client) DialContext(ctx context.Context, network string, dest M.Socksaddr) (net.Conn, error) {
	if !strings.HasPrefix(network, "tcp") {
		return nil, ErrNetwork
	}
	sess, err := c.session()
	if err != nil {
		return nil, err
	}
	st, err := sess.OpenStream()
	if err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = st.SetWriteDeadline(dl)
	}
	if err := WriteStreamHeader(st, dest); err != nil {
		_ = st.Close()
		return nil, err
	}
	_ = st.SetWriteDeadline(time.Time{})
	return st, nil
}

// ListenPacket returns a conn bound to a fresh association id; dest is
// ignored (every WriteTo names its own destination).
func (c *Client) ListenPacket(ctx context.Context, dest M.Socksaddr) (net.PacketConn, error) {
	c.amu.Lock()
	defer c.amu.Unlock()
	for {
		c.next++
		if _, taken := c.assocs[c.next]; !taken {
			break
		}
	}
	a := &clientAssoc{c: c, id: c.next, in: make(chan packet, 256), dl: deadline.New(), done: make(chan struct{})}
	c.assocs[a.id] = a
	return a, nil
}

func (c *Client) udpLoop() {
	buf := make([]byte, 65535)
	udp := c.demux.UDP()
	for {
		n, _, err := udp.ReadFrom(buf)
		if err != nil {
			return
		}
		id, from, payload, err := DecodeUDPFrame(buf[:n])
		if err != nil {
			continue
		}
		c.amu.Lock()
		a := c.assocs[id]
		c.amu.Unlock()
		if a == nil {
			continue
		}
		pkt := make([]byte, len(payload))
		copy(pkt, payload)
		select {
		case a.in <- packet{b: pkt, addr: from.UDPAddr()}:
		default:
		}
	}
}

type clientAssoc struct {
	c    *Client
	id   uint16
	in   chan packet
	dl   *deadline.Deadline
	done chan struct{}
	once sync.Once
}

func (a *clientAssoc) ReadFrom(b []byte) (int, net.Addr, error) {
	select {
	case p := <-a.in:
		return copy(b, p.b), p.addr, nil
	case <-a.done:
		return 0, nil, net.ErrClosed
	case <-a.c.closed:
		return 0, nil, net.ErrClosed
	case <-a.dl.Done():
		return 0, nil, os.ErrDeadlineExceeded
	}
}

func (a *clientAssoc) WriteTo(b []byte, addr net.Addr) (int, error) {
	frame, err := EncodeUDPFrame(a.id, M.SocksaddrFromNet(addr), b)
	if err != nil {
		return 0, err
	}
	if _, err := a.c.demux.UDP().WriteTo(frame, a.c.server); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (a *clientAssoc) Close() error {
	a.once.Do(func() {
		close(a.done)
		a.c.amu.Lock()
		delete(a.c.assocs, a.id)
		a.c.amu.Unlock()
	})
	return nil
}

func (a *clientAssoc) LocalAddr() net.Addr               { return a.c.pipe.LocalAddr() }
func (a *clientAssoc) SetDeadline(t time.Time) error     { a.dl.Set(t); return nil }
func (a *clientAssoc) SetReadDeadline(t time.Time) error { a.dl.Set(t); return nil }
func (a *clientAssoc) SetWriteDeadline(time.Time) error  { return nil }
