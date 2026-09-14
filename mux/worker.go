package mux

import (
	"context"
	"errors"
	"math/rand/v2"
	"net"
	"sync/atomic"
	"time"

	"github.com/romanrublev/turnrelay/relay"
)

type worker struct {
	id   int
	pool *Pool
}

// run keeps one allocation alive for the lifetime of ctx, restarting it
// with backoff after any failure.
func (w *worker) run(ctx context.Context) {
	backoff := w.pool.o.BackoffMin
	for {
		err := w.once(ctx)
		if ctx.Err() != nil {
			return
		}
		w.pool.restarts.Add(1)
		if err != nil {
			w.pool.setErr(err)
			w.pool.o.Logf("mux: worker %d: %v; retry in %v", w.id, err, backoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(jitter(backoff)):
		}
		backoff = min(backoff*2, w.pool.o.BackoffMax)
	}
}

func (w *worker) once(ctx context.Context) (err error) {
	p := w.pool
	lease, err := p.o.Creds.Acquire(ctx, w.id)
	if err != nil {
		return err
	}
	server := lease.Cred.Relay(lease.Index)
	if p.o.TURNOverride != "" {
		server = p.o.TURNOverride
	}
	p.connecting.Add(1)
	alloc, err := relay.Allocate(ctx, relay.Options{
		Server: server, Username: lease.Cred.Username, Password: lease.Cred.Password,
		UDP: p.o.TURNUDP, PeerIsIPv6: p.o.Peer.IP.To4() == nil,
	})
	if err != nil {
		p.connecting.Add(-1)
		p.o.Creds.Failed(lease, err)
		return err
	}
	defer p.o.Creds.Release(lease)
	defer alloc.Close()

	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go alloc.Keepalive(wctx, p.o.KeepaliveInterval)

	// Bound handshakes in flight: VK rate-limits bursts of new sessions.
	select {
	case p.handshakes <- struct{}{}:
	case <-ctx.Done():
		p.connecting.Add(-1)
		return ctx.Err()
	}
	conn, err := p.o.Wrapper.Client(wctx, alloc.Relayed(), p.o.Peer)
	<-p.handshakes
	p.connecting.Add(-1)
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, err := conn.Write(EncodeHello(p.session)); err != nil {
		return err
	}
	p.active.Add(1)
	p.broadcastReady()
	defer p.active.Add(-1)
	p.o.Logf("mux: worker %d up via %s relayed %s", w.id, server, alloc.RelayedAddr())

	var lastInbound atomic.Int64
	lastInbound.Store(time.Now().UnixNano())
	errCh := make(chan error, 3)

	// uplink: steal from the shared queue
	go func() {
		for {
			select {
			case <-wctx.Done():
				errCh <- nil
				return
			case pkt := <-p.up:
				if _, err := conn.Write(pkt); err != nil {
					// Put it back so another worker carries it: the queue is
					// the only place a datagram may wait, never the floor.
					select {
					case p.up <- pkt:
					default:
					}
					errCh <- err
					return
				}
			}
		}
	}()

	// downlink: control frames consumed here, payload to the shared queue
	go func() {
		buf := make([]byte, 2048)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				errCh <- err
				return
			}
			lastInbound.Store(time.Now().UnixNano())
			if n == 0 || IsControl(buf[:n]) {
				continue
			}
			pkt := make([]byte, n)
			copy(pkt, buf[:n])
			select {
			case p.down <- pkt:
			case <-wctx.Done():
				errCh <- nil
				return
			}
		}
	}()

	// probes and zombie detection
	go func() {
		t := time.NewTicker(p.o.ProbeInterval)
		defer t.Stop()
		var seq uint64
		for {
			select {
			case <-wctx.Done():
				errCh <- nil
				return
			case <-t.C:
				seq++
				if _, err := conn.Write(EncodeProbe(seq)); err != nil {
					errCh <- err
					return
				}
				if _, err := conn.Write(EncodeHello(p.session)); err != nil {
					errCh <- err
					return
				}
				if time.Since(time.Unix(0, lastInbound.Load())) > p.o.ZombieAfter {
					errCh <- errors.New("no inbound traffic, assuming zombie allocation")
					return
				}
			}
		}
	}()

	err = <-errCh
	cancel()
	_ = conn.SetReadDeadline(time.Now())
	if ctx.Err() != nil {
		return nil
	}
	if err == nil {
		err = net.ErrClosed
	}
	return err
}

func jitter(d time.Duration) time.Duration {
	return d/2 + time.Duration(rand.Int64N(int64(d)))
}
