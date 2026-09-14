package mux

import (
	"context"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"sync"
	"time"

	"github.com/pion/transport/v4/deadline"
)

// SessionAddr identifies one client session on the server-side pipe. It is
// the net.Addr the exit layer (and KCP) keys sessions by.
type SessionAddr [16]byte

func (a SessionAddr) Network() string { return "turnrelay" }
func (a SessionAddr) String() string  { return hex.EncodeToString(a[:]) }

var ErrAuth = errors.New("mux: allocation failed authentication")

type ServerOptions struct {
	// Password is required: an allocation must present a valid auth frame
	// after its hello or it is closed.
	Password string
	// ZombieAfter drops a session that has had no live allocation for this
	// long (default 120s).
	ZombieAfter   time.Duration
	UplinkQueue   int // merged uplink depth, default 2048
	DownlinkQueue int // per-session downlink depth, default 2048
	Logf          func(string, ...any)
}

func (o *ServerOptions) defaults() {
	if o.ZombieAfter == 0 {
		o.ZombieAfter = 120 * time.Second
	}
	if o.UplinkQueue == 0 {
		o.UplinkQueue = 2048
	}
	if o.DownlinkQueue == 0 {
		o.DownlinkQueue = 2048
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
}

type serverPacket struct {
	b    []byte
	addr SessionAddr
}

type serverSession struct {
	addr SessionAddr
	down chan []byte
	// live and lastLive are guarded by Server.mu, not atomics: reap() must
	// observe the live-count-drops-to-zero transition and the lastLive
	// timestamp it produces as one consistent snapshot, which two
	// independent atomics cannot guarantee across a preemption.
	live     int
	lastLive int64 // unix nanos when live last dropped to zero
}

// Server is the server-side twin of Pool: it takes allocation conns (one per
// obfuscated session from the relay), authenticates them, groups them by
// session and presents every session as one datagram pipe through
// PacketConn.
type Server struct {
	o        ServerOptions
	up       chan serverPacket
	mu       sync.Mutex
	sessions map[SessionAddr]*serverSession
	closed   chan struct{}
	once     sync.Once
}

func NewServer(o ServerOptions) *Server {
	o.defaults()
	s := &Server{o: o, up: make(chan serverPacket, o.UplinkQueue), sessions: map[SessionAddr]*serverSession{}, closed: make(chan struct{})}
	go s.reap()
	return s
}

func (s *Server) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

func (s *Server) join(addr SessionAddr) *serverSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[addr]
	if !ok {
		sess = &serverSession{addr: addr, down: make(chan []byte, s.o.DownlinkQueue)}
		s.sessions[addr] = sess
		s.o.Logf("mux: session %s opened", addr)
	}
	sess.live++
	return sess
}

func (s *Server) leave(sess *serverSession) {
	s.mu.Lock()
	sess.live--
	if sess.live == 0 {
		sess.lastLive = time.Now().UnixNano()
	}
	s.mu.Unlock()
}

func (s *Server) lookup(addr SessionAddr) *serverSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[addr]
}

// reap drops sessions with no live allocation for ZombieAfter.
func (s *Server) reap() {
	t := time.NewTicker(s.o.ZombieAfter / 4)
	defer t.Stop()
	for {
		select {
		case <-s.closed:
			return
		case now := <-t.C:
			s.mu.Lock()
			for addr, sess := range s.sessions {
				if sess.live == 0 && now.UnixNano()-sess.lastLive > int64(s.o.ZombieAfter) {
					delete(s.sessions, addr)
					s.o.Logf("mux: session %s reaped", addr)
				}
			}
			s.mu.Unlock()
		}
	}
}

// Handle runs one allocation conn until it fails or ctx ends: it expects a
// hello, then an auth frame, then relays payload up and stripes downlink
// datagrams from the session queue, echoing probes.
func (s *Server) Handle(ctx context.Context, conn net.Conn) error {
	defer conn.Close()
	buf := make([]byte, 65535)
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	n, err := conn.Read(buf)
	if err != nil {
		return err
	}
	session, ok := ParseHello(buf[:n])
	if !ok {
		return errors.New("mux: first frame is not a hello")
	}
	n, err = conn.Read(buf)
	if err != nil {
		return err
	}
	tag, ok := ParseAuth(buf[:n])
	if !ok || !VerifyAuth(s.o.Password, session, tag) {
		s.o.Logf("mux: auth failed for an allocation from %s", conn.RemoteAddr())
		return ErrAuth
	}
	_ = conn.SetReadDeadline(time.Time{})

	sess := s.join(SessionAddr(session))
	defer s.leave(sess)
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// The uplink/downlink goroutines below only notice wctx.Done() at their
	// channel selects, not while blocked inside conn.Read/conn.Write. Force
	// those syscalls to return on cancel by yanking the conn's deadline, or
	// a goroutine parked in I/O would never reach errCh and Handle would
	// block forever, leaking the conn and both goroutines.
	stop := context.AfterFunc(wctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()
	errCh := make(chan error, 2)

	// downlink: steal from the session queue
	go func() {
		for {
			select {
			case <-wctx.Done():
				errCh <- nil
				return
			case pkt := <-sess.down:
				if _, err := conn.Write(pkt); err != nil {
					s.requeue(sess, pkt)
					errCh <- err
					return
				}
			}
		}
	}()

	// uplink: control frames consumed here, payload to the merged queue
	go func() {
		rbuf := make([]byte, 65535)
		for {
			n, err := conn.Read(rbuf)
			if err != nil {
				errCh <- err
				return
			}
			if n == 0 {
				continue
			}
			if IsControl(rbuf[:n]) {
				if _, ok := ParseProbe(rbuf[:n]); ok {
					echo := make([]byte, n)
					copy(echo, rbuf[:n])
					_, _ = conn.Write(echo)
				}
				continue
			}
			pkt := make([]byte, n)
			copy(pkt, rbuf[:n])
			select {
			case s.up <- serverPacket{b: pkt, addr: sess.addr}:
			case <-wctx.Done():
				errCh <- nil
				return
			}
		}
	}()

	err = <-errCh
	cancel()
	return err
}

// requeue hands a datagram a dying allocation could not send back to the
// session for a live one; it is dropped if the queue is full.
func (s *Server) requeue(sess *serverSession, pkt []byte) {
	select {
	case sess.down <- pkt:
	default:
	}
}

// PacketConn presents every session as one net.PacketConn keyed by
// SessionAddr, the shape kcp.ServeConn wants.
func (s *Server) PacketConn() net.PacketConn {
	return &serverPacketConn{s: s, dl: deadline.New()}
}

type serverPacketConn struct {
	s  *Server
	dl *deadline.Deadline
}

func (p *serverPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	select {
	case pkt := <-p.s.up:
		return copy(b, pkt.b), pkt.addr, nil
	case <-p.s.closed:
		return 0, nil, net.ErrClosed
	case <-p.dl.Done():
		return 0, nil, os.ErrDeadlineExceeded
	}
}

func (p *serverPacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	sa, ok := addr.(SessionAddr)
	if !ok {
		return 0, errors.New("mux: WriteTo needs a SessionAddr")
	}
	sess := p.s.lookup(sa)
	if sess == nil {
		return 0, net.ErrClosed
	}
	pkt := make([]byte, len(b))
	copy(pkt, b)
	select {
	case sess.down <- pkt:
	case <-p.s.closed:
		return 0, net.ErrClosed
	default:
		// full: datagram semantics, drop
	}
	return len(b), nil
}

func (p *serverPacketConn) Close() error                      { return p.s.Close() }
func (p *serverPacketConn) LocalAddr() net.Addr               { return SessionAddr{} }
func (p *serverPacketConn) SetDeadline(t time.Time) error     { p.dl.Set(t); return nil }
func (p *serverPacketConn) SetReadDeadline(t time.Time) error { p.dl.Set(t); return nil }
func (p *serverPacketConn) SetWriteDeadline(time.Time) error  { return nil }
