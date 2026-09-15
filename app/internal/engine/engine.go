// Package engine runs a sing-box instance in-process with the turnrelay
// outbound registered, and exposes worker/handshake counters parsed from its
// log stream.
package engine

import (
	"context"
	"sync"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json"

	turnrelaybox "github.com/romanrublev/turnrelay/singbox"
)

type Engine struct {
	mu       sync.Mutex
	instance *box.Box
	counters Counters
}

func New() *Engine { return &Engine{} }

// logWriter implements log.PlatformWriter: it feeds every message to the
// counter scanner. (A file sink is added by the daemon in a later task.)
type logWriter struct{ c *Counters }

func (w logWriter) WriteMessage(level log.Level, message string) { scanLine(message, w.c) }

func (e *Engine) Start(configJSON []byte) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.instance != nil {
		return nil
	}
	e.counters.Workers.Store(0)
	e.counters.HandshakeOK.Store(false)

	outboundRegistry := include.OutboundRegistry()
	turnrelaybox.RegisterOutbound(outboundRegistry)
	ctx := box.Context(context.Background(),
		include.InboundRegistry(), outboundRegistry, include.EndpointRegistry(),
		include.DNSTransportRegistry(), include.ServiceRegistry(),
		include.CertificateProviderRegistry())

	options, err := json.UnmarshalExtendedContext[option.Options](ctx, configJSON)
	if err != nil {
		return err
	}
	instance, err := box.New(box.Options{
		Context:           ctx,
		Options:           options,
		PlatformLogWriter: logWriter{c: &e.counters},
	})
	if err != nil {
		return err
	}
	if err := instance.Start(); err != nil {
		instance.Close()
		return err
	}
	e.instance = instance
	return nil
}

func (e *Engine) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.instance == nil {
		return
	}
	e.instance.Close()
	e.instance = nil
	e.counters.Workers.Store(0)
	e.counters.HandshakeOK.Store(false)
}

func (e *Engine) Running() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.instance != nil
}

func (e *Engine) Workers() int      { return int(e.counters.Workers.Load()) }
func (e *Engine) HandshakeOK() bool { return e.counters.HandshakeOK.Load() }
