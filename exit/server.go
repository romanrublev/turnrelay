package exit

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing/common/bufio"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/xtaci/kcp-go/v5"
	"github.com/xtaci/smux"
)

var ErrPrivateDestination = errors.New("exit: destination is private, loopback or link-local")

type ServerOptions struct {
	DialTimeout  time.Duration // per destination dial, default 10s
	MaxStreams   int           // concurrent streams per session, default 256
	AllowPrivate bool          // serve private/loopback/link-local destinations
	Bind         string        // optional local address for outbound sockets
	UDPTimeout   time.Duration // idle timeout per UDP association, default 60s
	Logf         func(string, ...any)
}

func (o *ServerOptions) defaults() {
	if o.DialTimeout == 0 {
		o.DialTimeout = 10 * time.Second
	}
	if o.MaxStreams == 0 {
		o.MaxStreams = 256
	}
	if o.UDPTimeout == 0 {
		o.UDPTimeout = 60 * time.Second
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
}

// Server terminates the exit protocol on a multi-session PacketConn (one
// peer address per client session) and dials out: smux streams over KCP for
// TCP, framed datagrams for UDP.
type Server struct {
	o      ServerOptions
	demux  *Demux
	dialer net.Dialer
	lc     net.ListenConfig
	amu    sync.Mutex
	assocs map[string]*assoc
	once   sync.Once
	closed chan struct{}
}

type assoc struct {
	peer net.Addr
	id   uint16
	pc   net.PacketConn
	last atomic.Int64
}

func NewServer(pc net.PacketConn, o ServerOptions) *Server {
	o.defaults()
	s := &Server{o: o, demux: NewDemux(pc), assocs: map[string]*assoc{}, closed: make(chan struct{})}
	if o.Bind != "" {
		if ip := net.ParseIP(o.Bind); ip != nil {
			s.dialer.LocalAddr = &net.TCPAddr{IP: ip}
		}
	}
	return s
}

func (s *Server) Close() error {
	s.once.Do(func() {
		close(s.closed)
		_ = s.demux.Close()
		s.amu.Lock()
		for _, a := range s.assocs {
			_ = a.pc.Close()
		}
		s.amu.Unlock()
	})
	return nil
}

// Serve blocks until ctx ends or the pipe fails.
func (s *Server) Serve(ctx context.Context) error {
	ln, err := kcp.ServeConn(nil, 0, 0, s.demux.KCP())
	if err != nil {
		return err
	}
	go func() {
		select {
		case <-ctx.Done():
		case <-s.closed:
		}
		_ = ln.Close()
	}()
	go s.serveUDP(ctx)
	for {
		ks, err := ln.AcceptKCP()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		tuneKCP(ks)
		go s.serveSession(ctx, ks)
	}
}

func (s *Server) serveSession(ctx context.Context, ks *kcp.UDPSession) {
	defer ks.Close()
	sess, err := smux.Server(ks, smuxConfig())
	if err != nil {
		return
	}
	defer sess.Close()
	var streams atomic.Int32
	var capLogged bool
	for {
		st, err := sess.AcceptStream()
		if err != nil {
			return
		}
		if int(streams.Load()) >= s.o.MaxStreams {
			_ = st.Close()
			if !capLogged {
				s.o.Logf("exit: session %s hit the stream cap (%d)", ks.RemoteAddr(), s.o.MaxStreams)
				capLogged = true
			}
			continue
		}
		streams.Add(1)
		go func() {
			defer streams.Add(-1)
			s.handleStream(ctx, st)
		}()
	}
}

func (s *Server) handleStream(ctx context.Context, st *smux.Stream) {
	defer st.Close()
	_ = st.SetReadDeadline(time.Now().Add(s.o.DialTimeout))
	dest, err := ReadStreamHeader(st)
	if err != nil {
		return
	}
	_ = st.SetReadDeadline(time.Time{})
	dctx, cancel := context.WithTimeout(ctx, s.o.DialTimeout)
	defer cancel()
	target, err := s.resolveChecked(dctx, dest)
	if err != nil {
		s.o.Logf("exit: refuse %s: %v", dest, err)
		return
	}
	rc, err := s.dialer.DialContext(dctx, "tcp", target.String())
	if err != nil {
		s.o.Logf("exit: dial %s: %v", dest, err)
		return
	}
	_ = bufio.CopyConn(ctx, st, rc)
}

// resolveChecked resolves a destination on the server and applies the
// private-destination policy to the address actually dialed.
func (s *Server) resolveChecked(ctx context.Context, dest M.Socksaddr) (netip.AddrPort, error) {
	var ips []netip.Addr
	if dest.IsFqdn() {
		var err error
		ips, err = net.DefaultResolver.LookupNetIP(ctx, "ip", dest.Fqdn)
		if err != nil {
			return netip.AddrPort{}, err
		}
		if len(ips) == 0 {
			return netip.AddrPort{}, errors.New("exit: no address for " + dest.Fqdn)
		}
	} else {
		ips = []netip.Addr{dest.Addr}
	}
	for _, ip := range ips {
		if !s.o.AllowPrivate && isPrivate(ip.Unmap()) {
			return netip.AddrPort{}, ErrPrivateDestination
		}
	}
	return netip.AddrPortFrom(ips[0].Unmap(), dest.Port), nil
}

func isPrivate(ip netip.Addr) bool {
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || ip.IsInterfaceLocalMulticast()
}

// serveUDP reads UDP frames from every session and forwards them from a
// per-(session, assoc) socket; replies go back as frames to that session.
func (s *Server) serveUDP(ctx context.Context) {
	udp := s.demux.UDP()
	go s.reapAssocs(ctx)
	buf := make([]byte, 65535)
	for {
		n, peer, err := udp.ReadFrom(buf)
		if err != nil {
			return
		}
		id, dest, payload, err := DecodeUDPFrame(buf[:n])
		if err != nil {
			continue
		}
		target, err := s.resolveChecked(ctx, dest)
		if err != nil {
			continue
		}
		a, err := s.assocFor(ctx, peer, id, udp)
		if err != nil {
			continue
		}
		a.last.Store(time.Now().UnixNano())
		_, _ = a.pc.WriteTo(payload, net.UDPAddrFromAddrPort(target))
	}
}

func assocKey(peer net.Addr, id uint16) string {
	return peer.String() + "/" + strconv.Itoa(int(id))
}

func (s *Server) assocFor(ctx context.Context, peer net.Addr, id uint16, udp net.PacketConn) (*assoc, error) {
	key := assocKey(peer, id)
	s.amu.Lock()
	defer s.amu.Unlock()
	if a, ok := s.assocs[key]; ok {
		return a, nil
	}
	bind := ":0"
	if s.o.Bind != "" {
		bind = net.JoinHostPort(s.o.Bind, "0")
	}
	pc, err := s.lc.ListenPacket(ctx, "udp", bind)
	if err != nil {
		return nil, err
	}
	a := &assoc{peer: peer, id: id, pc: pc}
	a.last.Store(time.Now().UnixNano())
	s.assocs[key] = a
	go func() {
		rb := make([]byte, 65535)
		for {
			n, from, err := pc.ReadFrom(rb)
			if err != nil {
				return
			}
			frame, err := EncodeUDPFrame(id, M.SocksaddrFromNet(from), rb[:n])
			if err != nil {
				continue
			}
			a.last.Store(time.Now().UnixNano())
			_, _ = udp.WriteTo(frame, peer)
		}
	}()
	return a, nil
}

func (s *Server) reapAssocs(ctx context.Context) {
	t := time.NewTicker(s.o.UDPTimeout / 2)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.closed:
			return
		case now := <-t.C:
			s.amu.Lock()
			for key, a := range s.assocs {
				if now.UnixNano()-a.last.Load() > int64(s.o.UDPTimeout) {
					_ = a.pc.Close()
					delete(s.assocs, key)
				}
			}
			s.amu.Unlock()
		}
	}
}
