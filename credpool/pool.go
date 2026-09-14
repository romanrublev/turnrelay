// Package credpool hands VK TURN credentials to workers under VK's quota:
// 10 allocations per credential, one anonymous participant per credential.
// Policy follows anton48/vk-turn-proxy-ios pkg/proxy/credpool.go (GPL-3.0).
package credpool

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/romanrublev/turnrelay/provider"
	"github.com/romanrublev/turnrelay/relay"
)

type Fetcher = provider.Fetcher

type Options struct {
	Links           []string
	ConnsPerSlot    int
	TTL             time.Duration
	Margin          time.Duration
	CooldownMin     time.Duration
	CooldownMax     time.Duration
	CaptchaCooldown time.Duration
	Now             func() time.Time
	Sleep           func(context.Context, time.Duration) error
	Logf            func(string, ...any)
}

func (o *Options) defaults() {
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	if o.ConnsPerSlot <= 0 {
		o.ConnsPerSlot = 10
	}
	if o.TTL <= 0 {
		o.TTL = 10 * time.Minute
	}
	if o.Margin <= 0 {
		o.Margin = time.Minute
	}
	if o.Margin >= o.TTL {
		// expired() treats a credential as usable for TTL-Margin; a margin
		// at or above the TTL would make every credential expire the
		// moment it was fetched and the pool would re-fetch in a loop.
		o.Logf("credpool: margin %v is not below ttl %v, using %v", o.Margin, o.TTL, o.TTL/10)
		o.Margin = o.TTL / 10
	}
	if o.CooldownMin == 0 && o.CooldownMax == 0 {
		o.CooldownMin, o.CooldownMax = 3*time.Second, 6*time.Second
	}
	if o.CaptchaCooldown == 0 {
		o.CaptchaCooldown = time.Minute
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Sleep == nil {
		o.Sleep = func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		}
	}
}

type Lease struct {
	Cred  provider.Credential
	Slot  int
	Index int

	// s is the exact slot object this lease was issued against. Release
	// and Failed always act on it directly rather than looking p.slots[Slot]
	// back up by number: fetchInto can replace whatever *slot currently
	// sits at that numeric id (a saturated or invalidated slot is refreshed
	// in place once it becomes safe to do so), and a stale lease must never
	// be able to mutate the new occupant of its old id.
	s *slot
}

type slot struct {
	cred      provider.Credential
	valid     bool
	saturated bool
	active    map[int]bool // index -> in use
}

type Stats struct {
	Slots        int
	Active       int
	LastError    string
	CaptchaUntil time.Time
}

type Pool struct {
	fetch Fetcher
	o     Options

	mu        sync.Mutex
	slots     map[int]*slot
	fetchMu   sync.Mutex // one fetch at a time
	lastFetch time.Time
	captcha   time.Time
	lastErr   error
}

func New(f Fetcher, o Options) *Pool {
	o.defaults()
	return &Pool{fetch: f, o: o, slots: map[int]*slot{}}
}

func (p *Pool) linkFor(s int) string { return p.o.Links[s%len(p.o.Links)] }

// shortLink is what the log gets to see of a call link hash: enough to tell
// links apart, not enough to join the call from a log line.
func shortLink(link string) string {
	if len(link) <= 6 {
		return link
	}
	return link[:6] + "..."
}

// expired reports whether s's credential is older than TTL-Margin. s must
// be non-nil.
func (p *Pool) expired(s *slot) bool {
	return !p.o.Now().Before(s.cred.FetchedAt.Add(p.o.TTL - p.o.Margin))
}

func (p *Pool) usable(s *slot) bool {
	return s != nil && s.valid && !s.saturated && !p.expired(s) && len(s.active) < p.o.ConnsPerSlot
}

func (p *Pool) lease(id int, s *slot) *Lease {
	for i := 0; i < p.o.ConnsPerSlot; i++ {
		if !s.active[i] {
			s.active[i] = true
			return &Lease{Cred: s.cred, Slot: id, Index: i, s: s}
		}
	}
	return nil
}

// borrow returns a lease from any currently usable slot. p.mu must be held.
func (p *Pool) borrow() (*Lease, bool) {
	for id, s := range p.slots {
		if p.usable(s) {
			return p.lease(id, s), true
		}
	}
	return nil, false
}

func (p *Pool) Acquire(ctx context.Context, worker int) (*Lease, error) {
	own := worker / p.o.ConnsPerSlot
	for {
		p.mu.Lock()
		ownSlot := p.slots[own]
		if p.usable(ownSlot) {
			l := p.lease(own, ownSlot)
			p.mu.Unlock()
			return l, nil
		}
		captchaActive := p.o.Now().Before(p.captcha)
		// Borrow spare capacity from another slot when this worker's own
		// slot exists but has degraded (saturated, invalidated, or
		// expired), or when a captcha cooldown is blocking new fetches
		// entirely: either way, another slot with room beats waiting.
		// A worker whose own slot was never established (map key absent)
		// and no captcha is active instead goes straight to fetching its
		// own, so a freshly-needed slot gets its own credential and Link
		// rotation instead of silently sharing another slot's quota.
		if ownSlot != nil || captchaActive {
			if l, ok := p.borrow(); ok {
				p.mu.Unlock()
				return l, nil
			}
		}
		if captchaActive {
			until := p.captcha
			p.mu.Unlock()
			return nil, fmt.Errorf("credpool: captcha cooldown until %s: %w", until.Format(time.Kitchen), &provider.CaptchaRequiredError{})
		}
		p.mu.Unlock()
		if err := p.fetchInto(ctx, own); err != nil {
			// The fetch itself failed (captcha, quota, network...); before
			// surfacing that, see whether another slot became usable while
			// we were trying (e.g. a concurrent fetch on another worker's
			// slot landed in the meantime).
			p.mu.Lock()
			if l, ok := p.borrow(); ok {
				p.mu.Unlock()
				return l, nil
			}
			p.mu.Unlock()
			return nil, err
		}
	}
}

func (p *Pool) fetchInto(ctx context.Context, id int) error {
	p.fetchMu.Lock()
	defer p.fetchMu.Unlock()
	p.mu.Lock()
	for {
		s := p.slots[id]
		if p.usable(s) { // someone already fetched a slot we can use while we waited for fetchMu
			p.mu.Unlock()
			return nil
		}
		if s == nil || !s.valid || p.expired(s) {
			break // id is free to (re)fetch into: absent, invalidated, or expired
		}
		// s exists, is valid and unexpired: it is either saturated or
		// simply full (still holding live leases). Either way it must not
		// be overwritten, so try the next id.
		id++
	}
	p.mu.Unlock()
	if !p.lastFetch.IsZero() {
		wait := p.o.CooldownMin
		if p.o.CooldownMax > p.o.CooldownMin {
			wait += time.Duration(rand.Int64N(int64(p.o.CooldownMax - p.o.CooldownMin)))
		}
		if rem := wait - p.o.Now().Sub(p.lastFetch); rem > 0 {
			if err := p.o.Sleep(ctx, rem); err != nil {
				return err
			}
		}
	}
	cred, err := p.fetch(ctx, p.linkFor(id))
	p.lastFetch = p.o.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil {
		p.lastErr = err
		if provider.IsCaptcha(err) {
			p.captcha = p.o.Now().Add(p.o.CaptchaCooldown)
		}
		return err
	}
	if cred.FetchedAt.IsZero() {
		cred.FetchedAt = p.o.Now()
	}
	p.slots[id] = &slot{cred: cred, valid: true, active: map[int]bool{}}
	p.o.Logf("credpool: slot %d refreshed from %s (%d relays)", id, shortLink(cred.Link), len(cred.Relays))
	return nil
}

func (p *Pool) Release(l *Lease) {
	if l == nil || l.s == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(l.s.active, l.Index)
}

// Failed records why the allocation made with l did not work, then releases it.
func (p *Pool) Failed(l *Lease, err error) {
	if l == nil || l.s == nil {
		return
	}
	p.mu.Lock()
	switch {
	case relay.IsQuotaError(err):
		l.s.saturated = true
		p.o.Logf("credpool: slot %d saturated (486)", l.Slot)
	case relay.IsAuthError(err):
		l.s.valid = false
		p.o.Logf("credpool: slot %d invalidated (%v)", l.Slot, err)
	}
	p.lastErr = err
	p.mu.Unlock()
	p.Release(l)
}

func (p *Pool) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := Stats{Slots: len(p.slots), CaptchaUntil: p.captcha}
	for _, s := range p.slots {
		st.Active += len(s.active)
	}
	if p.lastErr != nil {
		st.LastError = p.lastErr.Error()
	}
	return st
}
