package exit

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/romanrublev/turnrelay/mux"
	"github.com/romanrublev/turnrelay/obfs"
)

// ListenConfig is everything the exit server needs: where to listen, which
// obfuscation the relay-side sessions use, the pre-shared password, and the
// exit policy.
type ListenConfig struct {
	Address  string
	Mode     obfs.Mode
	Password string
	// Cert is the server's DTLS certificate. A persisted (stable) certificate
	// has a stable obfs.CertFingerprint that clients pin; nil generates a fresh
	// one each start, which cannot be pinned. See obfs.ListenOptions.Cert.
	Cert        *tls.Certificate
	Server      ServerOptions
	ZombieAfter time.Duration
	Logf        func(string, ...any)
}

// Instance is a running exit server: obfs listener, server mux and exit
// logic wired together.
type Instance struct {
	l      *obfs.Listener
	m      *mux.Server
	s      *Server
	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
}

func (i *Instance) Addr() net.Addr { return i.l.Addr() }

func (i *Instance) Close() error {
	i.once.Do(func() {
		i.cancel()
		_ = i.l.Close()
		_ = i.s.Close()
		_ = i.m.Close()
		i.wg.Wait()
	})
	return nil
}

func Listen(ctx context.Context, cfg ListenConfig) (*Instance, error) {
	if cfg.Password == "" {
		return nil, errors.New("exit: a password is required")
	}
	if cfg.Mode == "" {
		cfg.Mode = obfs.ModeSRTP
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	if cfg.Server.Logf == nil {
		cfg.Server.Logf = cfg.Logf
	}
	l, err := obfs.Listen(cfg.Mode, cfg.Address, obfs.ListenOptions{Password: cfg.Password, Cert: cfg.Cert})
	if err != nil {
		return nil, err
	}
	m := mux.NewServer(mux.ServerOptions{Password: cfg.Password, ZombieAfter: cfg.ZombieAfter, Logf: cfg.Logf})
	s := NewServer(m.PacketConn(), cfg.Server)
	ctx, cancel := context.WithCancel(ctx)
	inst := &Instance{l: l, m: m, s: s, cancel: cancel}
	inst.wg.Add(2)
	go func() {
		defer inst.wg.Done()
		for {
			c, err := l.Accept(ctx)
			if err != nil {
				return
			}
			go func() {
				if err := m.Handle(ctx, c); err != nil && ctx.Err() == nil {
					cfg.Logf("mux: allocation from %s ended: %v", c.RemoteAddr(), err)
				}
			}()
		}
	}()
	go func() {
		defer inst.wg.Done()
		if err := s.Serve(ctx); err != nil && ctx.Err() == nil {
			cfg.Logf("exit: serve: %v", err)
		}
	}()
	cfg.Logf("exit: listening on %s (%s)", l.Addr(), cfg.Mode)
	return inst, nil
}
