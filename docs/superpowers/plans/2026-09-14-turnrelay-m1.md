# turnrelay Milestone 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A Go library `turnrelay` whose `Dialer` presents N TURN allocations (credentials from VK Calls or a static relay) (SRTP or WDTT-WRAP obfuscated) as one UDP datagram pipe usable as a sing-box `N.Dialer`, plus a `turnrelay-udp` CLI, an in-process test harness, a protocol note and a sing-box RFC draft.

**Architecture:** Bottom-up layers, each its own package with an in-process test peer: `obfs` (per-allocation DTLS / DTLS-SRTP / WDTT-WRAP conn), `relay` (pion/turn allocation), `provider/vk` + `provider/static` + `credpool` (credential sources and a quota-aware pool), `mux` (N workers, work-stealing uplink, merged downlink, hello/probe control plane), `turnrelay` (public Dialer at the module root). Tests never touch VK: pion/turn's server package plays the relay, `obfstest` plays the VPS server.

**Tech Stack:** Go 1.25+, pion/turn v5, pion/dtls v3, pion/srtp v3, pion/rtp, bogdanfinn/tls-client (Chrome TLS fingerprint for VK API), golang.org/x/crypto (HKDF, ChaCha20-Poly1305), sagernet/sing (`M.Socksaddr`, `N.Dialer`).

**Spec:** `docs/superpowers/specs/2026-09-14-turnrelay-design.md`

## Global Constraints

- Module path `github.com/romanrublev/turnrelay`, license GPL-3.0, provenance in `NOTICE`.
- Public API lives in package `turnrelay` at the module root; subpackages `obfs`, `relay`, `provider`, `provider/vk`, `provider/static`, `credpool`, `mux`.
- Names follow the mechanism, not the carrier: no `vk` in type names, flags or config keys except the provider value `"vk"` and VK-specific fields (`call_link`).
- Defaults: `Connections` 30 (max 60), `Mode` srtp, TURN transport UDP, probe every 30 s, zombie after 120 s, worker start pacing 100 ms, at most 3 obfs handshakes in flight, uplink queue 256, downlink queue 2048, 10 connections per credential, credential TTL 10 min minus 60 s margin, 3-6 s cooldown between credential fetches.
- Control frames: hello `ff 47 52 50` + 16-byte session id (20 bytes); probe `ff 50 4e 47` + 8-byte big-endian sequence (12 bytes).
- WRAP-v1: key = HKDF-SHA256(secret=password, salt="WDTT-WRAP-v1", info="rtp-obfs/chacha20poly1305"), RTP header V=2 P=1 PT=111 (audio) or 96 (video), nonce = ssrc(4) || seq(2) || 0x0000 || ts(4), aad = 12-byte header, padding 1..24 bytes (audio) or 1..60 (video), last byte = padding length.
- SRTP: DTLS with `use_srtp` profile `SRTP_AES128_CM_HMAC_SHA1_80`, RTP PT=100, demux by first byte (20..63 DTLS, 128..191 RTP).
- No plaintext mode. No copying from amurcanov/csqtt. Never drop datagrams inside the library: block on full queues.
- Text published to GitHub (commits, docs): no em-dashes, no AI attribution; commits authored by the repository owner only.
- Every task ends with `go vet ./... && go test ./...` green and a commit.

---

## File structure

```
go.mod, LICENSE, NOTICE, README.md, Makefile, .github/workflows/ci.yml
doc.go                 package turnrelay
dialer.go              Config, Dialer, New, Start, Close, DialContext, ListenPacket, Stats
conn.go                datagramConn (net.Conn) and packetConn (net.PacketConn) over mux.Pool
dialer_test.go         in-process end to end: pion turn server + obfstest server + Dialer
obfs/
  obfs.go              Mode, Options, Wrapper interface, New()
  dtls.go              legacy DTLS wrapper (client side)
  wrapv1.go            WDTT-WRAP-v1 codec: DeriveWrapKey, WrapCodec.Wrap/Unwrap, IsWrapRTP
  wrapv1_test.go
  wrap.go              wrap Wrapper: codec PacketConn adapter + DTLS
  srtp.go              DTLS-SRTP wrapper: demux, RTP framing, SRTP contexts
  srtp_test.go, dtls_test.go, wrap_test.go
  obfstest/
    server.go          Listen{DTLS,SRTP,Wrap}: in-process servers returning net.Conn per client
relay/
  relay.go             Allocate(ctx, Options) (*Allocation, error); errors
  relay_test.go        against pion/turn server
  turntest/
    server.go          Start(t) -> in-process pion/turn server with static credential
provider/
  provider.go          Credential, CaptchaRequiredError, IsCaptcha
  vk/
    client.go          Client, Fetch, Doer, Endpoints, apps list
    captcha.go         vkAPIError -> CaptchaRequiredError
    link.go            ParseCallLink
    namegen.go         random display names
    client_test.go     fake Doer with canned responses
  static/
    static.go          Fetcher for a fixed relay credential
credpool/
  pool.go              Pool, Options, Lease, Acquire, error classification hooks
  pool_test.go
mux/
  control.go           hello/probe encode/parse
  control_test.go
  worker.go            one allocation lifecycle
  pool.go              Pool: Start/Close/Write/Read/Stats, work stealing
  pool_test.go         against turntest + obfstest
cmd/turnrelay-udp/main.go
test/integration/     docker compose interop with the real anton48 server (manual)
scripts/vps-setup.sh  server side runbook for e2e
docs/protocol.md, docs/rfc-sing-box-issue.md
```

---

### Task 1: Module scaffold

**Files:**
- Create: `LICENSE`, `NOTICE`, `README.md`, `Makefile`, `.github/workflows/ci.yml`, `doc.go`
- Modify: `go.mod` (already initialised; deps will be tidied by later tasks)

**Interfaces:**
- Produces: the module and CI that every later task runs under.

- [ ] **Step 1: Write LICENSE and NOTICE**

`LICENSE`: the verbatim GPL-3.0 text (`curl -sL https://www.gnu.org/licenses/gpl-3.0.txt > LICENSE`).

`NOTICE`:
```
turnrelay
Copyright (c) 2026 Roman Rublev

Licensed under the GNU General Public License v3.0 (see LICENSE).

This project reuses ideas and, where noted in file headers, code from:

- cacggghp/vk-turn-proxy (GPL-3.0): VK credential chain, TURN allocation flow.
- anton48/vk-turn-proxy-ios (GPL-3.0; pkg/proxy/srtpwrap is MIT): DTLS-SRTP framing,
  credential pool policy, group hello and probe-echo control frames.
- amurcanov/proxy-turn-vk-android (GPL-3.0): WDTT-WRAP-v1 envelope.

No code from amurcanov/csqtt (PolyForm Noncommercial 1.0.0) is used.
```

- [ ] **Step 2: Write README.md (short) and doc.go**

`README.md`:
```markdown
# turnrelay

Go library that carries UDP datagrams through VK Calls TURN relays, disguised as WebRTC media,
and exposes them as a dialer for sing-box. Milestone 1: library, `turnrelay-udp` CLI, protocol note.

See `docs/protocol.md` and `docs/superpowers/specs/2026-09-14-turnrelay-design.md`.

License: GPL-3.0. Provenance: see NOTICE.
```

`doc.go`:
```go
// Package turnrelay presents N TURN allocations, each wrapped in an
// obfuscation layer that mimics WebRTC media, as a single UDP datagram pipe.
//
// The Dialer implements the DialContext/ListenPacket pair used by sing-box
// (N.Dialer) for the "udp" network only: every Write becomes one datagram
// striped over the allocation pool, every Read returns one merged downlink
// datagram. Higher layers (WireGuard in sing-box) provide reliability and
// ordering.
package turnrelay
```

- [ ] **Step 3: Write Makefile and CI**

`Makefile`:
```make
.PHONY: test vet lint build
test:
	go test -race -count=1 ./...
vet:
	go vet ./...
build:
	CGO_ENABLED=0 go build -o bin/turnrelay-udp ./cmd/turnrelay-udp
```

`.github/workflows/ci.yml`:
```yaml
name: ci
on: [push, pull_request]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.25' }
      - run: go vet ./...
      - run: go test -race -count=1 ./...
```

- [ ] **Step 4: Verify the module builds**

Run: `go build ./... && go vet ./...`
Expected: no output (doc.go only). Note `package turnrelay` at the module root.

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "chore: module scaffold, license, notice, ci"
```

---

### Task 2: Control frames (hello, probe)

**Files:**
- Create: `mux/control.go`, `mux/control_test.go`

**Interfaces:**
- Produces:
  - `const HelloLen = 20`, `const ProbeLen = 12`
  - `func EncodeHello(session [16]byte) []byte`
  - `func ParseHello(b []byte) (session [16]byte, ok bool)`
  - `func EncodeProbe(seq uint64) []byte`
  - `func ParseProbe(b []byte) (seq uint64, ok bool)`
  - `func IsControl(b []byte) bool` (true for hello or probe)

- [ ] **Step 1: Write the failing test**

```go
package mux

import (
	"bytes"
	"testing"
)

func TestHelloRoundTrip(t *testing.T) {
	var id [16]byte
	for i := range id {
		id[i] = byte(i + 1)
	}
	b := EncodeHello(id)
	if len(b) != HelloLen {
		t.Fatalf("len %d", len(b))
	}
	if !bytes.Equal(b[:4], []byte{0xff, 'G', 'R', 'P'}) {
		t.Fatalf("magic %x", b[:4])
	}
	got, ok := ParseHello(b)
	if !ok || got != id {
		t.Fatalf("parse: ok=%v got=%x", ok, got)
	}
	if _, ok := ParseHello(b[:19]); ok {
		t.Fatal("short hello accepted")
	}
	if !IsControl(b) {
		t.Fatal("hello not control")
	}
}

func TestProbeRoundTrip(t *testing.T) {
	b := EncodeProbe(0x0102030405060708)
	if len(b) != ProbeLen {
		t.Fatalf("len %d", len(b))
	}
	if !bytes.Equal(b, []byte{0xff, 'P', 'N', 'G', 1, 2, 3, 4, 5, 6, 7, 8}) {
		t.Fatalf("wire %x", b)
	}
	seq, ok := ParseProbe(b)
	if !ok || seq != 0x0102030405060708 {
		t.Fatalf("parse: ok=%v seq=%x", ok, seq)
	}
	if IsControl([]byte{1, 0, 0, 0}) {
		t.Fatal("wireguard handshake init treated as control")
	}
	if IsControl([]byte{0xff, 'X', 'Y', 'Z'}) {
		t.Fatal("unknown 0xff frame treated as control")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./mux/ -run 'TestHello|TestProbe' -v`
Expected: FAIL, "undefined: EncodeHello".

- [ ] **Step 3: Write minimal implementation**

```go
// Package mux runs N obfuscated TURN allocations as one datagram pipe.
package mux

import "encoding/binary"

// Control frames start with 0xff, which is outside WireGuard's message type
// range (1..4); a server that predates them forwards them to WireGuard,
// which drops them as malformed. Formats follow anton48/vk-turn-proxy
// (server/group.go, probe-echo in server/main.go).
const (
	HelloLen = 20
	ProbeLen = 12
)

var (
	helloMagic = [4]byte{0xff, 'G', 'R', 'P'}
	probeMagic = [4]byte{0xff, 'P', 'N', 'G'}
)

func EncodeHello(session [16]byte) []byte {
	b := make([]byte, HelloLen)
	copy(b, helloMagic[:])
	copy(b[4:], session[:])
	return b
}

func ParseHello(b []byte) (session [16]byte, ok bool) {
	if len(b) != HelloLen || [4]byte(b[:4]) != helloMagic {
		return session, false
	}
	copy(session[:], b[4:])
	return session, true
}

func EncodeProbe(seq uint64) []byte {
	b := make([]byte, ProbeLen)
	copy(b, probeMagic[:])
	binary.BigEndian.PutUint64(b[4:], seq)
	return b
}

func ParseProbe(b []byte) (uint64, bool) {
	if len(b) < ProbeLen || [4]byte(b[:4]) != probeMagic {
		return 0, false
	}
	return binary.BigEndian.Uint64(b[4:12]), true
}

func IsControl(b []byte) bool {
	if len(b) < 4 || b[0] != 0xff {
		return false
	}
	m := [4]byte(b[:4])
	return m == helloMagic || m == probeMagic
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./mux/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add mux && git commit -m "mux: hello and probe control frames"
```

---

### Task 3: WDTT-WRAP-v1 codec

**Files:**
- Create: `obfs/wrapv1.go`, `obfs/wrapv1_test.go`

**Interfaces:**
- Produces:
  - `const WrapKeyLen = 32`
  - `func DeriveWrapKey(password string) ([]byte, error)`
  - `type WrapCodec struct{...}`; `func NewWrapCodec(key []byte, video bool) (*WrapCodec, error)`
  - `func (c *WrapCodec) Wrap(dst, payload []byte) ([]byte, error)` (appends to dst[:0], returns the wire packet)
  - `func (c *WrapCodec) Unwrap(dst, wire []byte) ([]byte, error)`
  - `func IsWrapRTP(wire []byte) bool`

- [ ] **Step 1: Write the failing test**

```go
package obfs

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestDeriveWrapKey(t *testing.T) {
	k1, err := DeriveWrapKey("secret")
	if err != nil || len(k1) != WrapKeyLen {
		t.Fatalf("derive: %v len=%d", err, len(k1))
	}
	k2, _ := DeriveWrapKey("secret")
	k3, _ := DeriveWrapKey("other")
	if !bytes.Equal(k1, k2) || bytes.Equal(k1, k3) {
		t.Fatal("key derivation not deterministic per password")
	}
	if _, err := DeriveWrapKey(""); err == nil {
		t.Fatal("empty password accepted")
	}
}

func TestWrapRoundTripAndHeader(t *testing.T) {
	key, _ := DeriveWrapKey("pw")
	c, err := NewWrapCodec(key, false)
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte{0xAB}, 1200)
	w1, err := c.Wrap(nil, payload)
	if err != nil {
		t.Fatal(err)
	}
	if !IsWrapRTP(w1) {
		t.Fatal("not classified as RTP")
	}
	if w1[0] != 0x80|0x20 || w1[1]&0x7f != 111 {
		t.Fatalf("header %x", w1[:2])
	}
	pad := int(w1[len(w1)-1])
	if pad < 1 || pad > 24 {
		t.Fatalf("padding %d", pad)
	}
	if len(w1) != 12+len(payload)+16+pad {
		t.Fatalf("len %d", len(w1))
	}
	w2, _ := c.Wrap(nil, payload)
	seq1 := binary.BigEndian.Uint16(w1[2:4])
	seq2 := binary.BigEndian.Uint16(w2[2:4])
	ts1 := binary.BigEndian.Uint32(w1[4:8])
	ts2 := binary.BigEndian.Uint32(w2[4:8])
	if seq2 != seq1+1 || ts2 != ts1+960 {
		t.Fatalf("seq %d->%d ts %d->%d", seq1, seq2, ts1, ts2)
	}
	if binary.BigEndian.Uint32(w1[8:12]) != binary.BigEndian.Uint32(w2[8:12]) {
		t.Fatal("ssrc changed between packets")
	}
	d := &WrapCodec{}
	_ = d
	out, err := c.Unwrap(nil, w1)
	if err != nil || !bytes.Equal(out, payload) {
		t.Fatalf("unwrap: %v", err)
	}
	// Unwrap is keyed only: a second codec with the same key decodes too.
	c2, _ := NewWrapCodec(key, true)
	out, err = c2.Unwrap(nil, w2)
	if err != nil || !bytes.Equal(out, payload) {
		t.Fatalf("cross unwrap: %v", err)
	}
}

func TestWrapRejects(t *testing.T) {
	key, _ := DeriveWrapKey("pw")
	c, _ := NewWrapCodec(key, false)
	w, _ := c.Wrap(nil, []byte("hello"))
	w[15] ^= 1
	if _, err := c.Unwrap(nil, w); err == nil {
		t.Fatal("tampered packet accepted")
	}
	if _, err := c.Unwrap(nil, []byte{0x80, 111, 0}); err == nil {
		t.Fatal("short packet accepted")
	}
	if _, err := c.Unwrap(nil, append([]byte{0x00}, w[1:]...)); err == nil {
		t.Fatal("non-RTP accepted")
	}
	if _, err := c.Wrap(nil, nil); err == nil {
		t.Fatal("empty payload accepted")
	}
	if _, err := NewWrapCodec([]byte("short"), false); err == nil {
		t.Fatal("short key accepted")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./obfs/ -run Wrap -v`
Expected: FAIL, "undefined: DeriveWrapKey".

- [ ] **Step 3: Write minimal implementation**

```go
package obfs

// WDTT-WRAP-v1 envelope, wire compatible with amurcanov/proxy-turn-vk-android
// (android-client/obfs.go, GPL-3.0). Each datagram becomes one RTP-looking
// packet: 12-byte header, ChaCha20-Poly1305 ciphertext, random padding and a
// trailing padding-length byte (RTP P bit set). The nonce is derived from
// the header, so the receiver needs only the key.

import (
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

const (
	WrapKeyLen     = 32
	wrapHdrLen     = 12
	wrapTSStep     = 960 // 20 ms of 48 kHz Opus
	wrapPTAudio    = 111
	wrapPTVideo    = 96
	wrapPadAudio   = 24
	wrapPadVideo   = 60
	wrapMinWireLen = wrapHdrLen + chacha20poly1305.Overhead + 1
)

func DeriveWrapKey(password string) ([]byte, error) {
	if password == "" {
		return nil, errors.New("obfs: empty wrap password")
	}
	key := make([]byte, WrapKeyLen)
	r := hkdf.New(sha256.New, []byte(password), []byte("WDTT-WRAP-v1"), []byte("rtp-obfs/chacha20poly1305"))
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, fmt.Errorf("obfs: derive wrap key: %w", err)
	}
	return key, nil
}

type WrapCodec struct {
	aead   cipher.AEAD
	ssrc   uint32
	pt     uint8
	padMax int

	mu      sync.Mutex
	initSeq uint16
	initTS  uint32
	count   uint64
}

func NewWrapCodec(key []byte, video bool) (*WrapCodec, error) {
	if len(key) != WrapKeyLen {
		return nil, fmt.Errorf("obfs: wrap key must be %d bytes, got %d", WrapKeyLen, len(key))
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	var seed [10]byte
	if _, err := rand.Read(seed[:]); err != nil {
		return nil, err
	}
	c := &WrapCodec{
		aead:    aead,
		ssrc:    binary.BigEndian.Uint32(seed[0:4]),
		pt:      wrapPTAudio,
		padMax:  wrapPadAudio,
		initSeq: binary.BigEndian.Uint16(seed[4:6]),
		initTS:  binary.BigEndian.Uint32(seed[6:10]),
	}
	if video {
		c.pt, c.padMax = wrapPTVideo, wrapPadVideo
	}
	return c, nil
}

func wrapNonce(ssrc uint32, seq uint16, ts uint32) [12]byte {
	var n [12]byte
	binary.BigEndian.PutUint32(n[0:4], ssrc)
	binary.BigEndian.PutUint16(n[4:6], seq)
	binary.BigEndian.PutUint32(n[8:12], ts)
	return n
}

func (c *WrapCodec) Wrap(dst, payload []byte) ([]byte, error) {
	if len(payload) == 0 {
		return nil, errors.New("obfs: empty payload")
	}
	c.mu.Lock()
	n := c.count
	c.count++
	c.mu.Unlock()
	seq := c.initSeq + uint16(n)
	ts := c.initTS + uint32(n)*wrapTSStep + uint32(n>>16)

	var rnd [1]byte
	_, _ = rand.Read(rnd[:])
	padRand := int(rnd[0]) % c.padMax
	padTotal := padRand + 1

	total := wrapHdrLen + len(payload) + chacha20poly1305.Overhead + padTotal
	out := append(dst[:0], make([]byte, total)...)
	out[0] = 0x80 | 0x20
	out[1] = c.pt & 0x7f
	binary.BigEndian.PutUint16(out[2:4], seq)
	binary.BigEndian.PutUint32(out[4:8], ts)
	binary.BigEndian.PutUint32(out[8:12], c.ssrc)
	nonce := wrapNonce(c.ssrc, seq, ts)
	sealed := c.aead.Seal(out[wrapHdrLen:wrapHdrLen], nonce[:], payload, out[:wrapHdrLen])
	padStart := wrapHdrLen + len(sealed)
	if padRand > 0 {
		_, _ = rand.Read(out[padStart : padStart+padRand])
	}
	out[total-1] = byte(padTotal)
	return out, nil
}

func (c *WrapCodec) Unwrap(dst, wire []byte) ([]byte, error) {
	if len(wire) < wrapMinWireLen {
		return nil, errors.New("obfs: wrap packet too short")
	}
	if wire[0]>>6 != 2 {
		return nil, errors.New("obfs: not RTP v2")
	}
	end := len(wire)
	if wire[0]&0x20 != 0 {
		pad := int(wire[end-1])
		if pad == 0 || pad > end-wrapHdrLen {
			return nil, fmt.Errorf("obfs: invalid padding %d", pad)
		}
		end -= pad
	}
	if end-wrapHdrLen <= chacha20poly1305.Overhead {
		return nil, errors.New("obfs: no ciphertext")
	}
	seq := binary.BigEndian.Uint16(wire[2:4])
	ts := binary.BigEndian.Uint32(wire[4:8])
	ssrc := binary.BigEndian.Uint32(wire[8:12])
	nonce := wrapNonce(ssrc, seq, ts)
	plain, err := c.aead.Open(dst[:0], nonce[:], wire[wrapHdrLen:end], wire[:wrapHdrLen])
	if err != nil {
		return nil, fmt.Errorf("obfs: wrap auth: %w", err)
	}
	return plain, nil
}

func IsWrapRTP(wire []byte) bool {
	if len(wire) < wrapMinWireLen || wire[0]>>6 != 2 {
		return false
	}
	pt := wire[1] & 0x7f
	return pt == wrapPTAudio || pt == wrapPTVideo
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./obfs/ -run Wrap -v`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add obfs && git commit -m "obfs: WDTT-WRAP-v1 codec"
```

---

### Task 4: Wrapper interface, legacy DTLS wrapper, in-process DTLS test server

**Files:**
- Create: `obfs/obfs.go`, `obfs/dtls.go`, `obfs/obfstest/server.go`, `obfs/dtls_test.go`

**Interfaces:**
- Produces:
  - `type Mode string`; `ModeSRTP Mode = "srtp"`, `ModeWrap = "wrap"`, `ModeDTLS = "dtls"`
  - `type Options struct { Password string; WrapKey []byte; Video bool; HandshakeTimeout time.Duration }`
  - `type Wrapper interface { Client(ctx context.Context, underlay net.PacketConn, peer net.Addr) (net.Conn, error) }`
  - `func New(mode Mode, o Options) (Wrapper, error)`
  - `func dtlsClientOptions(cert tls.Certificate) []dtls.ClientOption` (shared by dtls and wrap)
  - `obfstest.Server` with `Addr() *net.UDPAddr`, `Accept(ctx) (net.Conn, error)`, `Close()`; constructors `obfstest.ListenDTLS(t)`, later `ListenSRTP(t)`, `ListenWrap(t, key)`

- [ ] **Step 1: Write obfs.go**

```go
// Package obfs turns a TURN-relayed net.PacketConn into a net.Conn whose
// datagrams look like WebRTC media to the relay's traffic classifier.
package obfs

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

type Mode string

const (
	ModeSRTP Mode = "srtp" // DTLS-SRTP, RFC 5764 keying, RTP PT 100
	ModeWrap Mode = "wrap" // WDTT-WRAP-v1 envelope around plain DTLS
	ModeDTLS Mode = "dtls" // plain DTLS 1.2; deprecated, shaped by VK relays
)

type Options struct {
	Password         string // ModeWrap: HKDF input, ignored if WrapKey is set
	WrapKey          []byte // ModeWrap: raw 32-byte key
	Video            bool   // ModeWrap: PT 96 / larger padding instead of PT 111
	HandshakeTimeout time.Duration
}

// Wrapper runs the mode's handshake over underlay towards peer and returns a
// datagram-preserving net.Conn: one Write is one wire packet, one Read is one
// decoded packet. Implementations own underlay after Client returns.
type Wrapper interface {
	Client(ctx context.Context, underlay net.PacketConn, peer net.Addr) (net.Conn, error)
}

const defaultHandshakeTimeout = 20 * time.Second

func New(mode Mode, o Options) (Wrapper, error) {
	if o.HandshakeTimeout == 0 {
		o.HandshakeTimeout = defaultHandshakeTimeout
	}
	switch mode {
	case ModeDTLS:
		return &dtlsWrapper{timeout: o.HandshakeTimeout}, nil
	case ModeSRTP:
		return &srtpWrapper{timeout: o.HandshakeTimeout}, nil
	case ModeWrap:
		key := o.WrapKey
		if key == nil {
			var err error
			if key, err = DeriveWrapKey(o.Password); err != nil {
				return nil, err
			}
		}
		if len(key) != WrapKeyLen {
			return nil, fmt.Errorf("obfs: wrap key must be %d bytes", WrapKeyLen)
		}
		return &wrapWrapper{key: key, video: o.Video, timeout: o.HandshakeTimeout}, nil
	default:
		return nil, errors.New("obfs: unknown mode " + string(mode))
	}
}

// peerPacketConn pins every WriteTo to one peer; pion/turn's relayed conn
// needs the peer address on each write, DTLS gives it once.
type peerPacketConn struct {
	net.PacketConn
	peer net.Addr
}

func (p *peerPacketConn) WriteTo(b []byte, _ net.Addr) (int, error) {
	return p.PacketConn.WriteTo(b, p.peer)
}
```

- [ ] **Step 2: Write dtls.go**

```go
package obfs

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"time"

	"github.com/pion/dtls/v3"
	"github.com/pion/dtls/v3/pkg/crypto/selfsign"
)

// dtlsClientOptions is the cipher/extension set the whole server family
// accepts (cacggghp server: ECDHE-ECDSA-AES128-GCM, extended master secret,
// connection ids).
func dtlsClientOptions(cert tls.Certificate) []dtls.ClientOption {
	return []dtls.ClientOption{
		dtls.WithCertificates(cert),
		dtls.WithInsecureSkipVerify(true),
		dtls.WithExtendedMasterSecret(dtls.RequireExtendedMasterSecret),
		dtls.WithCipherSuites(dtls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256),
		dtls.WithConnectionIDGenerator(dtls.OnlySendCIDGenerator()),
	}
}

func dtlsHandshake(ctx context.Context, conn net.PacketConn, peer net.Addr, timeout time.Duration, extra ...dtls.ClientOption) (*dtls.Conn, error) {
	cert, err := selfsign.GenerateSelfSigned()
	if err != nil {
		return nil, fmt.Errorf("obfs: self-signed cert: %w", err)
	}
	opts := append(dtlsClientOptions(cert), extra...)
	dc, err := dtls.ClientWithOptions(conn, peer, opts...)
	if err != nil {
		return nil, fmt.Errorf("obfs: dtls client: %w", err)
	}
	hctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := dc.HandshakeContext(hctx); err != nil {
		_ = dc.Close()
		return nil, fmt.Errorf("obfs: dtls handshake: %w", err)
	}
	return dc, nil
}

type dtlsWrapper struct{ timeout time.Duration }

func (w *dtlsWrapper) Client(ctx context.Context, underlay net.PacketConn, peer net.Addr) (net.Conn, error) {
	return dtlsHandshake(ctx, &peerPacketConn{underlay, peer}, peer, w.timeout)
}
```

- [ ] **Step 3: Write obfstest/server.go (DTLS part; SRTP and Wrap constructors added in Tasks 5 and 6)**

```go
// Package obfstest provides in-process counterparts of the VPS server for
// each obfuscation mode. They mirror what cacggghp/anton48 servers accept.
package obfstest

import (
	"context"
	"crypto/tls"
	"net"
	"testing"

	"github.com/pion/dtls/v3"
	"github.com/pion/dtls/v3/pkg/crypto/selfsign"
)

type Server struct {
	addr  *net.UDPAddr
	conns chan net.Conn
	close func()
}

func (s *Server) Addr() *net.UDPAddr { return s.addr }
func (s *Server) Close()             { s.close() }

func (s *Server) Accept(ctx context.Context) (net.Conn, error) {
	select {
	case c := <-s.conns:
		return c, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func serverOptions(cert tls.Certificate) []dtls.ServerOption {
	return []dtls.ServerOption{
		dtls.WithCertificates(cert),
		dtls.WithExtendedMasterSecret(dtls.RequireExtendedMasterSecret),
		dtls.WithCipherSuites(dtls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256),
		dtls.WithConnectionIDGenerator(dtls.RandomCIDGenerator(8)),
	}
}

// ListenDTLS is the legacy cacggghp server: a DTLS listener on 127.0.0.1.
func ListenDTLS(t *testing.T) *Server {
	t.Helper()
	cert, err := selfsign.GenerateSelfSigned()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := dtls.ListenWithOptions("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}, serverOptions(cert)...)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{addr: ln.Addr().(*net.UDPAddr), conns: make(chan net.Conn, 64)}
	ctx, cancel := context.WithCancel(context.Background())
	s.close = func() { cancel(); _ = ln.Close() }
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				dc := c.(*dtls.Conn)
				if err := dc.HandshakeContext(ctx); err != nil {
					_ = dc.Close()
					return
				}
				s.conns <- dc
			}()
		}
	}()
	t.Cleanup(s.Close)
	return s
}
```

- [ ] **Step 4: Write the test (dtls_test.go)**

```go
package obfs_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/romanrublev/turnrelay/obfs"
	"github.com/romanrublev/turnrelay/obfs/obfstest"
)

// echoDatagrams copies every datagram back, used by all wrapper tests.
func echoDatagrams(c net.Conn) {
	buf := make([]byte, 2048)
	for {
		n, err := c.Read(buf)
		if err != nil {
			return
		}
		if _, err := c.Write(buf[:n]); err != nil {
			return
		}
	}
}

func roundTrip(t *testing.T, mode obfs.Mode, o obfs.Options, srv *obfstest.Server) {
	t.Helper()
	w, err := obfs.New(mode, o)
	if err != nil {
		t.Fatal(err)
	}
	underlay, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() {
		c, err := srv.Accept(ctx)
		if err == nil {
			echoDatagrams(c)
		}
	}()
	conn, err := w.Client(ctx, underlay, srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for i, msg := range []string{"one", "two", string(make([]byte, 1200))} {
		if _, err := conn.Write([]byte(msg)); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 2048)
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if string(buf[:n]) != msg {
			t.Fatalf("datagram %d mismatch: %d bytes", i, n)
		}
	}
}

func TestDTLSRoundTrip(t *testing.T) {
	roundTrip(t, obfs.ModeDTLS, obfs.Options{}, obfstest.ListenDTLS(t))
}

func TestNewRejectsUnknownMode(t *testing.T) {
	if _, err := obfs.New("plain", obfs.Options{}); err == nil {
		t.Fatal("unknown mode accepted")
	}
	if _, err := obfs.New(obfs.ModeWrap, obfs.Options{}); err == nil {
		t.Fatal("wrap without key accepted")
	}
}
```

Until Tasks 5 and 6 exist, `obfs.New` references `srtpWrapper` and `wrapWrapper`; add temporary stubs in `obfs.go` so it compiles:

```go
type srtpWrapper struct{ timeout time.Duration }
type wrapWrapper struct {
	key     []byte
	video   bool
	timeout time.Duration
}
func (w *srtpWrapper) Client(context.Context, net.PacketConn, net.Addr) (net.Conn, error) { return nil, errors.New("not implemented") }
func (w *wrapWrapper) Client(context.Context, net.PacketConn, net.Addr) (net.Conn, error) { return nil, errors.New("not implemented") }
```
(Tasks 5 and 6 move these types into their own files.)

- [ ] **Step 5: Run tests**

Run: `go test ./obfs/... -v`
Expected: PASS for TestDTLSRoundTrip, TestNewRejectsUnknownMode and the wrap codec tests.

- [ ] **Step 6: Commit**

```bash
git add obfs && git commit -m "obfs: wrapper interface, legacy dtls mode, in-process dtls test server"
```

---

### Task 5: DTLS-SRTP wrapper

**Files:**
- Create: `obfs/srtp.go`, `obfs/srtp_test.go`
- Modify: `obfs/obfs.go` (remove the srtpWrapper stub), `obfs/obfstest/server.go` (add `ListenSRTP`)

**Interfaces:**
- Produces: `srtpWrapper` implementing `Wrapper`; `obfstest.ListenSRTP(t) *Server` whose accepted conns speak the same RTP/SRTP framing (this is what anton48's `-srtp` server does).
- Wire: after DTLS with `use_srtp`, each Write = RTP header (V2, PT 100, seq++, ts += len, fixed random SSRC) + payload, protected by SRTP with keys from `srtp.Config.ExtractSessionKeysFromDTLS`. Reads demux by first byte: 20..63 to DTLS, 128..191 to SRTP.

- [ ] **Step 1: Write the test**

```go
package obfs_test

import (
	"testing"

	"github.com/romanrublev/turnrelay/obfs"
	"github.com/romanrublev/turnrelay/obfs/obfstest"
)

func TestSRTPRoundTrip(t *testing.T) {
	roundTrip(t, obfs.ModeSRTP, obfs.Options{}, obfstest.ListenSRTP(t))
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./obfs/ -run SRTP -v`
Expected: FAIL (undefined obfstest.ListenSRTP / "not implemented").

- [ ] **Step 3: Write srtp.go**

```go
package obfs

// DTLS-SRTP mode. Independent implementation from RFC 3550, RFC 3711 and
// RFC 5764 with pion/dtls, pion/rtp and pion/srtp, following the framing of
// anton48/vk-turn-proxy-ios pkg/proxy/srtpwrap (MIT): PT 100, demux on the
// first byte, one RTP packet per datagram.

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/pion/dtls/v3"
	"github.com/pion/rtp"
	"github.com/pion/srtp/v3"
)

const (
	srtpPayloadType uint8 = 100
	srtpProfile           = srtp.ProtectionProfileAes128CmHmacSha1_80
)

func isDTLSByte(b byte) bool { return b >= 20 && b <= 63 }
func isRTPByte(b byte) bool  { return b >= 128 && b <= 191 }

type srtpWrapper struct{ timeout time.Duration }

func (w *srtpWrapper) Client(ctx context.Context, underlay net.PacketConn, peer net.Addr) (net.Conn, error) {
	d := newDemux(underlay, peer)
	dc, err := dtlsHandshake(ctx, d.dtlsSide(), peer, w.timeout,
		dtls.WithSRTPProtectionProfiles(dtls.SRTP_AES128_CM_HMAC_SHA1_80))
	if err != nil {
		d.Close()
		return nil, err
	}
	c, err := newSRTPConn(d, dc, true)
	if err != nil {
		_ = dc.Close()
		d.Close()
		return nil, err
	}
	return c, nil
}

// demux reads the underlay once and splits packets between the DTLS
// handshake and the SRTP data path by first byte.
type demux struct {
	raw    net.PacketConn
	peer   net.Addr
	dtlsCh chan []byte
	rtpCh  chan []byte
	done   chan struct{}
	once   sync.Once
}

func newDemux(raw net.PacketConn, peer net.Addr) *demux {
	d := &demux{raw: raw, peer: peer, dtlsCh: make(chan []byte, 64), rtpCh: make(chan []byte, 2048), done: make(chan struct{})}
	go d.loop()
	return d
}

func (d *demux) loop() {
	buf := make([]byte, 2048)
	for {
		n, _, err := d.raw.ReadFrom(buf)
		if err != nil {
			select {
			case <-d.done:
				return
			default:
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				_ = d.raw.SetReadDeadline(time.Time{})
				continue
			}
			d.Close()
			return
		}
		if n == 0 {
			continue
		}
		pkt := make([]byte, n)
		copy(pkt, buf[:n])
		var ch chan []byte
		switch {
		case isDTLSByte(pkt[0]):
			ch = d.dtlsCh
		case isRTPByte(pkt[0]):
			ch = d.rtpCh
		default:
			continue
		}
		select {
		case ch <- pkt:
		case <-d.done:
			return
		}
	}
}

func (d *demux) Close() {
	d.once.Do(func() {
		close(d.done)
		_ = d.raw.SetReadDeadline(time.Now())
		_ = d.raw.Close()
	})
}

// dtlsSide is the PacketConn handed to pion/dtls: reads come from dtlsCh,
// writes go straight to the peer.
func (d *demux) dtlsSide() net.PacketConn { return &demuxSide{d: d} }

type demuxSide struct {
	d        *demux
	dlMu     sync.Mutex
	deadline chan struct{}
	dlTimer  *time.Timer
}

func (s *demuxSide) ReadFrom(b []byte) (int, net.Addr, error) {
	s.dlMu.Lock()
	dl := s.deadline
	s.dlMu.Unlock()
	select {
	case pkt := <-s.d.dtlsCh:
		return copy(b, pkt), s.d.peer, nil
	case <-s.d.done:
		return 0, nil, net.ErrClosed
	case <-dl:
		return 0, nil, os.ErrDeadlineExceeded
	}
}

func (s *demuxSide) WriteTo(b []byte, _ net.Addr) (int, error) { return s.d.raw.WriteTo(b, s.d.peer) }
func (s *demuxSide) Close() error                              { return nil }
func (s *demuxSide) LocalAddr() net.Addr                       { return s.d.raw.LocalAddr() }
func (s *demuxSide) SetDeadline(t time.Time) error             { return s.SetReadDeadline(t) }
func (s *demuxSide) SetWriteDeadline(time.Time) error          { return nil }

func (s *demuxSide) SetReadDeadline(t time.Time) error {
	s.dlMu.Lock()
	defer s.dlMu.Unlock()
	if s.dlTimer != nil {
		s.dlTimer.Stop()
		s.dlTimer = nil
	}
	if t.IsZero() {
		s.deadline = nil
		return nil
	}
	ch := make(chan struct{})
	s.deadline = ch
	d := time.Until(t)
	if d <= 0 {
		close(ch)
		return nil
	}
	s.dlTimer = time.AfterFunc(d, func() { close(ch) })
	return nil
}

// srtpConn is the net.Conn returned to the mux: RTP framing on Write,
// SRTP unprotect on Read.
type srtpConn struct {
	d      *demux
	dc     *dtls.Conn
	enc    *srtp.Context
	dec    *srtp.Context
	ssrc   uint32
	wmu    sync.Mutex
	seq    uint16
	ts     uint32
	rdl    demuxSide // reused only for its deadline machinery
	closed chan struct{}
	once   sync.Once
}

func newSRTPConn(d *demux, dc *dtls.Conn, isClient bool) (*srtpConn, error) {
	state, ok := dc.ConnectionState()
	if !ok {
		return nil, errors.New("obfs: dtls state unavailable")
	}
	cfg := &srtp.Config{Profile: srtpProfile}
	if err := cfg.ExtractSessionKeysFromDTLS(&state, isClient); err != nil {
		return nil, fmt.Errorf("obfs: srtp keys: %w", err)
	}
	enc, err := srtp.CreateContext(cfg.Keys.LocalMasterKey, cfg.Keys.LocalMasterSalt, cfg.Profile)
	if err != nil {
		return nil, err
	}
	dec, err := srtp.CreateContext(cfg.Keys.RemoteMasterKey, cfg.Keys.RemoteMasterSalt, cfg.Profile)
	if err != nil {
		return nil, err
	}
	var ssrc [4]byte
	_, _ = rand.Read(ssrc[:])
	return &srtpConn{d: d, dc: dc, enc: enc, dec: dec, ssrc: binary.BigEndian.Uint32(ssrc[:]), rdl: demuxSide{d: d}, closed: make(chan struct{})}, nil
}

func (c *srtpConn) Read(b []byte) (int, error) {
	for {
		c.rdl.dlMu.Lock()
		dl := c.rdl.deadline
		c.rdl.dlMu.Unlock()
		select {
		case pkt := <-c.d.rtpCh:
			plain, err := c.dec.DecryptRTP(nil, pkt, nil)
			if err != nil {
				continue
			}
			var h rtp.Header
			n, err := h.Unmarshal(plain)
			if err != nil {
				continue
			}
			return copy(b, plain[n:]), nil
		case <-c.closed:
			return 0, net.ErrClosed
		case <-c.d.done:
			return 0, net.ErrClosed
		case <-dl:
			return 0, os.ErrDeadlineExceeded
		}
	}
}

func (c *srtpConn) Write(b []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	pkt := rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: srtpPayloadType, SequenceNumber: c.seq, Timestamp: c.ts, SSRC: c.ssrc}, Payload: b}
	c.seq++
	c.ts += uint32(len(b))
	raw, err := pkt.Marshal()
	if err != nil {
		return 0, err
	}
	enc, err := c.enc.EncryptRTP(nil, raw, nil)
	if err != nil {
		return 0, err
	}
	if _, err := c.d.raw.WriteTo(enc, c.d.peer); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (c *srtpConn) Close() error {
	c.once.Do(func() {
		close(c.closed)
		_ = c.dc.Close()
		c.d.Close()
	})
	return nil
}

func (c *srtpConn) LocalAddr() net.Addr                { return c.d.raw.LocalAddr() }
func (c *srtpConn) RemoteAddr() net.Addr               { return c.d.peer }
func (c *srtpConn) SetDeadline(t time.Time) error      { return c.rdl.SetReadDeadline(t) }
func (c *srtpConn) SetReadDeadline(t time.Time) error  { return c.rdl.SetReadDeadline(t) }
func (c *srtpConn) SetWriteDeadline(time.Time) error   { return nil }
```

Remove the `srtpWrapper` stub from `obfs.go`.

- [ ] **Step 4: Add obfstest.ListenSRTP**

The server side needs a per-source demux on one UDP socket. Add to `obfstest/server.go`:

```go
// ListenSRTP mirrors anton48's -srtp server: one UDP socket, sessions keyed
// by source address, DTLS with use_srtp, then RTP/SRTP framed datagrams.
func ListenSRTP(t *testing.T) *Server {
	t.Helper()
	cert, err := selfsign.GenerateSelfSigned()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{addr: raw.LocalAddr().(*net.UDPAddr), conns: make(chan net.Conn, 64)}
	ctx, cancel := context.WithCancel(context.Background())
	s.close = func() { cancel(); _ = raw.Close() }
	opts := append(serverOptions(cert), dtls.WithSRTPProtectionProfiles(dtls.SRTP_AES128_CM_HMAC_SHA1_80))
	go serveSRTP(ctx, raw, opts, s.conns)
	t.Cleanup(s.Close)
	return s
}
```

and `serveSRTP` in the same package, which is the server-side twin of `obfs.demux`: a map `source string -> *session{dtlsCh, rtpCh}`; on the first packet from a source create the session and run `dtls.ServerWithOptions(sideConn, src, opts...)` + `HandshakeContext`, extract SRTP keys with `isClient=false`, and publish an `srtpServerConn` whose Read/Write mirror `obfs.srtpConn` (same RTP framing, PT 100). Implement `srtpServerConn` by copying the Read/Write bodies from `obfs/srtp.go` with `c.d.raw.WriteTo(enc, src)`; the demux side conn is a struct with `ReadFrom` from `dtlsCh` and `WriteTo` to `raw.WriteTo(b, src)`. To avoid duplicating `demuxSide`, export a helper in obfs: `func NewSRTPServerConn(raw net.PacketConn, src net.Addr, dc *dtls.Conn, rtpCh <-chan []byte) (net.Conn, error)` which builds the same `srtpConn` with `isClient=false` and a demux whose `rtpCh` is the supplied channel (add a constructor `newDemuxFromChannels(raw, peer, dtlsCh, rtpCh)` that does not start `loop`). Keep `NewSRTPServerConn` documented as "test support, not API-stable".

- [ ] **Step 5: Run tests**

Run: `go test -race ./obfs/... -v`
Expected: PASS including TestSRTPRoundTrip; the 1200-byte datagram must survive.

- [ ] **Step 6: Commit**

```bash
git add obfs && git commit -m "obfs: dtls-srtp mode with in-process server"
```

---

### Task 6: WRAP mode wrapper (WDTT envelope around DTLS)

**Files:**
- Create: `obfs/wrap.go`, `obfs/wrap_test.go`
- Modify: `obfs/obfs.go` (remove stub), `obfs/obfstest/server.go` (add `ListenWrap`)

**Interfaces:**
- Produces: `wrapWrapper` implementing `Wrapper`; `type WrapPacketConn` (exported for obfstest) wrapping a PacketConn with a `WrapCodec` so each WriteTo is wrapped and each ReadFrom unwrapped; `obfstest.ListenWrap(t, key []byte) *Server`.

- [ ] **Step 1: Write the test**

```go
package obfs_test

import (
	"testing"

	"github.com/romanrublev/turnrelay/obfs"
	"github.com/romanrublev/turnrelay/obfs/obfstest"
)

func TestWrapRoundTrip(t *testing.T) {
	key, _ := obfs.DeriveWrapKey("tunnel-secret")
	roundTrip(t, obfs.ModeWrap, obfs.Options{Password: "tunnel-secret"}, obfstest.ListenWrap(t, key))
}

func TestWrapWrongPasswordTimesOut(t *testing.T) {
	key, _ := obfs.DeriveWrapKey("right")
	srv := obfstest.ListenWrap(t, key)
	w, _ := obfs.New(obfs.ModeWrap, obfs.Options{Password: "wrong", HandshakeTimeout: 2 * time.Second})
	underlay, _ := net.ListenPacket("udp", "127.0.0.1:0")
	_, err := w.Client(context.Background(), underlay, srv.Addr())
	if err == nil {
		t.Fatal("handshake with wrong password succeeded")
	}
}
```
(add `context`, `net`, `time` imports.)

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./obfs/ -run Wrap -v`
Expected: FAIL (ListenWrap undefined).

- [ ] **Step 3: Write wrap.go**

```go
package obfs

import (
	"context"
	"net"
	"time"
)

// WrapPacketConn applies the WDTT-WRAP-v1 envelope to every datagram of an
// underlying PacketConn. Exported so obfstest can build the server side.
type WrapPacketConn struct {
	net.PacketConn
	codec *WrapCodec
	rbuf  []byte
	wbuf  []byte
}

func NewWrapPacketConn(inner net.PacketConn, codec *WrapCodec) *WrapPacketConn {
	return &WrapPacketConn{PacketConn: inner, codec: codec, rbuf: make([]byte, 2048), wbuf: make([]byte, 0, 2048)}
}

func (w *WrapPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	for {
		n, addr, err := w.PacketConn.ReadFrom(w.rbuf)
		if err != nil {
			return 0, nil, err
		}
		if !IsWrapRTP(w.rbuf[:n]) {
			continue
		}
		plain, err := w.codec.Unwrap(b[:0], w.rbuf[:n])
		if err != nil {
			continue // wrong key or corrupted; the DTLS layer above retransmits
		}
		return len(plain), addr, nil
	}
}

func (w *WrapPacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	wire, err := w.codec.Wrap(w.wbuf, b)
	if err != nil {
		return 0, err
	}
	if _, err := w.PacketConn.WriteTo(wire, addr); err != nil {
		return 0, err
	}
	return len(b), nil
}

type wrapWrapper struct {
	key     []byte
	video   bool
	timeout time.Duration
}

func (w *wrapWrapper) Client(ctx context.Context, underlay net.PacketConn, peer net.Addr) (net.Conn, error) {
	codec, err := NewWrapCodec(w.key, w.video)
	if err != nil {
		return nil, err
	}
	pc := NewWrapPacketConn(&peerPacketConn{underlay, peer}, codec)
	return dtlsHandshake(ctx, pc, peer, w.timeout)
}
```

Note on concurrency: `WrapPacketConn.WriteTo` is called from pion/dtls's single writer, and the mux writes only through the returned `*dtls.Conn` (which serialises), so `wbuf` needs no mutex; document that in a comment.

- [ ] **Step 4: Add obfstest.ListenWrap**

The server side needs per-source packet conns; the simplest construction is `pionudp.Listen("udp", addr)` from `github.com/pion/transport/v4/udp`, which multiplexes one socket into per-source `net.PacketConn`s, then wrap each accepted conn with `obfs.NewWrapPacketConn` and run `dtls.ServerWithOptions`. Full code:

```go
// ListenWrap mirrors the WDTT server: UDP socket, WRAP-v1 envelope, then DTLS.
func ListenWrap(t *testing.T, key []byte) *Server {
	t.Helper()
	cert, err := selfsign.GenerateSelfSigned()
	if err != nil {
		t.Fatal(err)
	}
	pl, err := pionudp.Listen("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{addr: pl.Addr().(*net.UDPAddr), conns: make(chan net.Conn, 64)}
	ctx, cancel := context.WithCancel(context.Background())
	s.close = func() { cancel(); _ = pl.Close() }
	go func() {
		for {
			pc, raddr, err := pl.Accept()
			if err != nil {
				return
			}
			go func() {
				codec, err := obfs.NewWrapCodec(key, false)
				if err != nil {
					return
				}
				dc, err := dtls.ServerWithOptions(obfs.NewWrapPacketConn(pc, codec), raddr, serverOptions(cert)...)
				if err != nil {
					return
				}
				if err := dc.HandshakeContext(ctx); err != nil {
					_ = dc.Close()
					return
				}
				s.conns <- dc
			}()
		}
	}()
	t.Cleanup(s.Close)
	return s
}
```
with `pionudp "github.com/pion/transport/v4/udp"` and `"github.com/romanrublev/turnrelay/obfs"` imported (obfstest importing obfs is fine; obfs tests are in package `obfs_test`).

- [ ] **Step 5: Run tests**

Run: `go test -race ./obfs/... -v`
Expected: PASS; TestWrapWrongPasswordTimesOut takes ~2 s.

- [ ] **Step 6: Commit**

```bash
git add obfs && git commit -m "obfs: wrap mode (WDTT envelope around dtls)"
```

---

### Task 7: TURN relay allocation and in-process TURN server

**Files:**
- Create: `relay/relay.go`, `relay/relay_test.go`, `relay/turntest/server.go`

**Interfaces:**
- Produces:
  - `type Options struct { Server string; Username, Password string; UDP bool; PeerIsIPv6 bool; Logger logging.LoggerFactory }`
  - `type Allocation struct{ ... }` with `Relayed() net.PacketConn`, `RelayedAddr() net.Addr`, `Close() error`, `Keepalive(ctx context.Context, every time.Duration)` (blocking loop sending STUN Binding requests)
  - `func Allocate(ctx context.Context, o Options) (*Allocation, error)`
  - `func IsQuotaError(err error) bool` (486), `func IsAuthError(err error) bool` (401, 438 stale nonce, "Unauthorized")
  - `turntest.Start(t) *turntest.Server` with `Addr() string`, `Username`, `Password`, `Allocations() int`, and `SetQuota(n int)` so tests can force 486.

- [ ] **Step 1: Write turntest/server.go**

```go
// Package turntest runs pion/turn as a stand-in for VK's relay.
package turntest

import (
	"net"
	"sync/atomic"
	"testing"

	"github.com/pion/logging"
	"github.com/pion/turn/v5"
)

type Server struct {
	addr     string
	Username string
	Password string
	Realm    string
	allocs   atomic.Int32
	quota    atomic.Int32
	srv      *turn.Server
}

func (s *Server) Addr() string       { return s.addr }
func (s *Server) Allocations() int   { return int(s.allocs.Load()) }
func (s *Server) SetQuota(n int)     { s.quota.Store(int32(n)) }

func Start(t *testing.T) *Server {
	t.Helper()
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{addr: pc.LocalAddr().String(), Username: "user", Password: "pass", Realm: "turnrelay.test"}
	s.quota.Store(1 << 30)
	key := turn.GenerateAuthKey(s.Username, s.Realm, s.Password)
	s.srv, err = turn.NewServer(turn.ServerConfig{
		Realm:         s.Realm,
		LoggerFactory: logging.NewDefaultLoggerFactory(),
		AuthHandler: func(username, realm string, _ net.Addr) ([]byte, bool) {
			if username == s.Username && realm == s.Realm {
				return key, true
			}
			return nil, false
		},
		QuotaHandler: func(string, string, net.Addr) bool {
			return s.allocs.Load() < s.quota.Load()
		},
		EventHandler: turn.EventHandler{
			OnAllocationCreated: func(_, _ net.Addr, _, _, _ string, _ net.Addr, _ int) { s.allocs.Add(1) },
			OnAllocationDeleted: func(_, _ net.Addr, _, _, _ string) { s.allocs.Add(-1) },
		},
		PacketConnConfigs: []turn.PacketConnConfig{{
			PacketConn: pc,
			RelayAddressGenerator: &turn.RelayAddressGeneratorStatic{
				RelayAddress: net.ParseIP("127.0.0.1"),
				Address:      "127.0.0.1",
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.srv.Close() })
	return s
}
```

Signatures above match pion/turn v5.1.1 (`go doc github.com/pion/turn/v5/internal/allocation EventHandler`); if the resolved version differs, adjust the parameter lists, not the counting logic.

- [ ] **Step 2: Write the failing test (relay_test.go)**

```go
package relay_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/romanrublev/turnrelay/relay"
	"github.com/romanrublev/turnrelay/relay/turntest"
)

func TestAllocateAndEcho(t *testing.T) {
	ts := turntest.Start(t)
	peer, _ := net.ListenPacket("udp4", "127.0.0.1:0")
	defer peer.Close()
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := peer.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = peer.WriteTo(buf[:n], from)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a, err := relay.Allocate(ctx, relay.Options{Server: ts.Addr(), Username: ts.Username, Password: ts.Password, UDP: true})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if ts.Allocations() != 1 {
		t.Fatalf("allocations %d", ts.Allocations())
	}
	rc := a.Relayed()
	if _, err := rc.WriteTo([]byte("ping"), peer.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1500)
	_ = rc.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, from, err := rc.ReadFrom(buf)
	if err != nil || string(buf[:n]) != "ping" {
		t.Fatalf("echo: n=%d err=%v", n, err)
	}
	if from.String() != peer.LocalAddr().String() {
		t.Fatalf("from %s", from)
	}
}

func TestAllocateAuthAndQuotaErrors(t *testing.T) {
	ts := turntest.Start(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := relay.Allocate(ctx, relay.Options{Server: ts.Addr(), Username: "nobody", Password: "x", UDP: true})
	if !relay.IsAuthError(err) {
		t.Fatalf("want auth error, got %v", err)
	}
	ts.SetQuota(0)
	_, err = relay.Allocate(ctx, relay.Options{Server: ts.Addr(), Username: ts.Username, Password: ts.Password, UDP: true})
	if !relay.IsQuotaError(err) {
		t.Fatalf("want quota error, got %v", err)
	}
	if relay.IsQuotaError(errors.New("boom")) || relay.IsAuthError(nil) {
		t.Fatal("classifier false positive")
	}
}

func TestAllocateTCP(t *testing.T) {
	t.Skip("turntest is UDP only; TCP transport is covered by the e2e run against a real relay")
}
```

- [ ] **Step 3: Run to verify it fails**

Run: `go test ./relay/... -v`
Expected: FAIL, package relay missing.

- [ ] **Step 4: Write relay.go**

```go
// Package relay opens one TURN allocation and keeps it alive.
package relay

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/pion/logging"
	"github.com/pion/transport/v4/stdnet"
	"github.com/pion/turn/v5"
)

type Options struct {
	Server     string // host:port of the TURN relay
	Username   string
	Password   string
	UDP        bool // true: UDP transport (default and recommended); false: TCP
	PeerIsIPv6 bool // request an IPv6 relayed address
	Logger     logging.LoggerFactory
}

type Allocation struct {
	client  *turn.Client
	relayed net.PacketConn
	base    io.Closer
}

// connectedUDP makes a connected UDP socket usable as net.PacketConn.
type connectedUDP struct{ *net.UDPConn }

func (c *connectedUDP) WriteTo(b []byte, _ net.Addr) (int, error) { return c.Write(b) }

func Allocate(ctx context.Context, o Options) (*Allocation, error) {
	if o.Logger == nil {
		f := logging.NewDefaultLoggerFactory()
		f.DefaultLogLevel = logging.LogLevelWarn
		o.Logger = f
	}
	raddr, err := net.ResolveUDPAddr("udp", o.Server)
	if err != nil {
		return nil, fmt.Errorf("relay: resolve %s: %w", o.Server, err)
	}
	var (
		turnConn net.PacketConn
		base     io.Closer
	)
	if o.UDP {
		c, err := net.DialUDP("udp", nil, raddr)
		if err != nil {
			return nil, fmt.Errorf("relay: dial udp: %w", err)
		}
		_ = c.SetReadBuffer(640 * 1024)
		_ = c.SetWriteBuffer(640 * 1024)
		turnConn, base = &connectedUDP{c}, c
	} else {
		var d net.Dialer
		c, err := d.DialContext(ctx, "tcp", raddr.String())
		if err != nil {
			return nil, fmt.Errorf("relay: dial tcp: %w", err)
		}
		turnConn, base = turn.NewSTUNConn(c), c
	}
	family := turn.RequestedAddressFamilyIPv4
	if o.PeerIsIPv6 {
		family = turn.RequestedAddressFamilyIPv6
	}
	client, err := turn.NewClient(&turn.ClientConfig{
		STUNServerAddr:         raddr.String(),
		TURNServerAddr:         raddr.String(),
		Conn:                   turnConn,
		Net:                    new(stdnet.Net), // zero value: no interface enumeration (Huawei ROMs deny NETLINK)
		Username:               o.Username,
		Password:               o.Password,
		RequestedAddressFamily: family,
		LoggerFactory:          o.Logger,
	})
	if err != nil {
		_ = base.Close()
		return nil, fmt.Errorf("relay: client: %w", err)
	}
	if err := client.Listen(); err != nil {
		client.Close()
		_ = base.Close()
		return nil, fmt.Errorf("relay: listen: %w", err)
	}
	relayed, err := client.AllocateWithContext(ctx)
	if err != nil {
		client.Close()
		_ = base.Close()
		return nil, fmt.Errorf("relay: allocate: %w", err)
	}
	return &Allocation{client: client, relayed: relayed, base: base}, nil
}

func (a *Allocation) Relayed() net.PacketConn { return a.relayed }
func (a *Allocation) RelayedAddr() net.Addr   { return a.relayed.LocalAddr() }

func (a *Allocation) Close() error {
	err := a.relayed.Close()
	a.client.Close()
	_ = a.base.Close()
	return err
}

// Keepalive sends STUN Binding requests until ctx ends; VK relays drop
// silent allocations well before the 10 minute lifetime.
func (a *Allocation) Keepalive(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_, _ = a.client.SendBindingRequest()
		}
	}
}

func IsQuotaError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "486") || strings.Contains(strings.ToLower(s), "quota")
}

func IsAuthError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "401") || strings.Contains(s, "438") ||
		strings.Contains(s, "unauthorized") || strings.Contains(s, "stale nonce") ||
		strings.Contains(s, "authentication") || strings.Contains(s, "invalid credential")
}
```
(add `"io"` to imports.)

- [ ] **Step 5: Run tests**

Run: `go test -race ./relay/... -v`
Expected: PASS. If the 486 test fails because pion reports the quota refusal with different text, print `err.Error()` and extend `IsQuotaError` to match it (keep "486" and "quota").

- [ ] **Step 6: Commit**

```bash
git add relay && git commit -m "relay: turn allocation with keepalive and in-process turn server"
```

---

### Task 8: Credential providers (provider, provider/static, provider/vk)

**Files:**
- Create: `provider/provider.go`, `provider/static/static.go`, `provider/vk/client.go`, `provider/vk/captcha.go`, `provider/vk/link.go`, `provider/vk/namegen.go`, `provider/vk/client_test.go`

**Interfaces:**
- Produces in `provider` (carrier-neutral):
  - `type Credential struct { Username, Password string; Relays []string /* host:port, transport=tcp entries dropped */; Link string; FetchedAt time.Time }`
  - `func (c Credential) Relay(i int) string` (round-robin pick)
  - `type CaptchaRequiredError struct { Sid, RedirectURI, SessionToken, Img string }` with `Error()`
  - `func IsCaptcha(err error) bool`
  - `type Fetcher func(ctx context.Context, link string) (Credential, error)` (credpool re-exports this type)
- Produces in `provider/static`:
  - `func New(server, username, password string) provider.Fetcher` (ignores the link argument)
- Produces in `provider/vk`:
  - `type Doer interface { Do(*fhttp.Request) (*fhttp.Response, error) }`
  - `type Endpoints struct { Login, API, OK string }`; `var DefaultEndpoints`
  - `type Client struct { HTTP Doer; Endpoints Endpoints; Sleep func(time.Duration); Logf func(string, ...any) }`
  - `func NewClient() (*Client, error)` (tls-client, Chrome 146 profile, public-resolver dialer)
  - `func (c *Client) Fetch(ctx context.Context, link string) (Credential, error)`
  - `func ParseCallLink(s string) (string, error)` returns the hash for `https://vk.ru/call/join/<hash>`, `vk.com`, bare hash

- [ ] **Step 0: Write provider/provider.go and provider/static/static.go**

```go
// Package provider defines what a credential source hands to the pool.
package provider

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type Credential struct {
	Username  string
	Password  string
	Relays    []string // host:port; TCP-only entries are dropped by providers
	Link      string   // provider-specific source id (call link hash, "static")
	FetchedAt time.Time
}

func (c Credential) Relay(i int) string {
	if len(c.Relays) == 0 {
		return ""
	}
	return c.Relays[i%len(c.Relays)]
}

type Fetcher func(ctx context.Context, link string) (Credential, error)

// CaptchaRequiredError: the provider needs a human. The library never solves
// captchas; callers cool down or surface it.
type CaptchaRequiredError struct {
	Sid, RedirectURI, SessionToken, Img string
}

func (e *CaptchaRequiredError) Error() string {
	return fmt.Sprintf("provider: captcha required (sid %s)", e.Sid)
}

func IsCaptcha(err error) bool {
	var ce *CaptchaRequiredError
	return errors.As(err, &ce)
}
```

```go
// Package static returns one fixed credential: self-hosted coturn, or a
// relay whose credentials were obtained elsewhere.
package static

import (
	"context"
	"time"

	"github.com/romanrublev/turnrelay/provider"
)

func New(server, username, password string) provider.Fetcher {
	return func(context.Context, string) (provider.Credential, error) {
		return provider.Credential{Username: username, Password: password, Relays: []string{server}, Link: "static", FetchedAt: time.Now()}, nil
	}
}
```

- [ ] **Step 1: Write provider/vk/link.go and its test first**

```go
package vk

import (
	"errors"
	"strings"
)

// ParseCallLink accepts a full VK call link on vk.ru or vk.com, or a bare
// hash, and returns the hash.
func ParseCallLink(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("vk: empty call link")
	}
	if i := strings.Index(s, "join/"); i >= 0 {
		s = s[i+len("join/"):]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if len(s) < 8 || strings.ContainsAny(s, " \t") {
		return "", errors.New("vk: malformed call link")
	}
	return s, nil
}
```

Test:
```go
func TestParseCallLink(t *testing.T) {
	for in, want := range map[string]string{
		"https://vk.ru/call/join/AbCdEf123456":         "AbCdEf123456",
		"https://vk.com/call/join/AbCdEf123456?x=1":    "AbCdEf123456",
		"  AbCdEf123456  ":                             "AbCdEf123456",
	} {
		got, err := ParseCallLink(in)
		if err != nil || got != want {
			t.Fatalf("%q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "https://vk.ru/call/join/", "short"} {
		if _, err := ParseCallLink(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}
```

- [ ] **Step 2: Write the fake-Doer test for Fetch (provider/vk/client_test.go)**

```go
package vk

import (
	"context"
	"errors"
	"io"
	"net/url"
	"strings"
	"testing"

	fhttp "github.com/bogdanfinn/fhttp"

	"github.com/romanrublev/turnrelay/provider"
)

type fakeDoer struct {
	t     *testing.T
	calls []string
	resp  map[string]string // url path -> body, consumed in order per path
}

func (f *fakeDoer) Do(r *fhttp.Request) (*fhttp.Response, error) {
	body, _ := io.ReadAll(r.Body)
	f.calls = append(f.calls, r.URL.Host+r.URL.Path+"?"+string(body))
	key := r.URL.Host + r.URL.Path
	if strings.Contains(string(body), "method=vchat.joinConversationByLink") {
		key += "#join"
	} else if strings.Contains(string(body), "method=auth.anonymLogin") {
		key += "#login"
	}
	b, ok := f.resp[key]
	if !ok {
		f.t.Fatalf("unexpected request %s", key)
	}
	return &fhttp.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(b))}, nil
}

func newTestClient(d Doer) *Client {
	return &Client{HTTP: d, Endpoints: DefaultEndpoints, Sleep: func(time.Duration) {}, Logf: func(string, ...any) {}}
}

func TestFetchHappyPath(t *testing.T) {
	d := &fakeDoer{t: t, resp: map[string]string{
		"login.vk.ru/":                                   `{"data":{"access_token":"T1"}}`,
		"api.vk.ru/method/calls.getCallPreview":          `{"response":{}}`,
		"api.vk.ru/method/calls.getAnonymousToken":       `{"response":{"token":"T2"}}`,
		"calls.okcdn.ru/fb.do#login":                     `{"session_key":"T3"}`,
		"calls.okcdn.ru/fb.do#join":                      `{"turn_server":{"username":"u1","credential":"p1","urls":["turn:155.212.200.1:3478?transport=udp","turn:155.212.200.1:3478?transport=tcp","turn:155.212.200.2:3478"]}}`,
	}}
	c := newTestClient(d)
	cred, err := c.Fetch(context.Background(), "AbCdEf123456")
	if err != nil {
		t.Fatal(err)
	}
	if cred.Username != "u1" || cred.Password != "p1" {
		t.Fatalf("cred %+v", cred)
	}
	if len(cred.Relays) != 2 || cred.Relays[0] != "155.212.200.1:3478" || cred.Relays[1] != "155.212.200.2:3478" {
		t.Fatalf("relays %v", cred.Relays)
	}
	if cred.Relay(3) != "155.212.200.2:3478" {
		t.Fatal("round robin")
	}
	join := d.calls[len(d.calls)-1]
	for _, want := range []string{"anonymToken=T2", "session_key=T3", "joinLink=AbCdEf123456", "protocolVersion=5"} {
		if !strings.Contains(join, want) {
			t.Fatalf("join request lacks %s: %s", want, join)
		}
	}
	tok2 := d.calls[2]
	if !strings.Contains(tok2, "vk_join_link="+url.QueryEscape("https://vk.com/call/join/AbCdEf123456")) && !strings.Contains(tok2, "vk_join_link=https://vk.com/call/join/AbCdEf123456") {
		t.Fatalf("token2 request: %s", tok2)
	}
}

func TestFetchCaptcha(t *testing.T) {
	d := &fakeDoer{t: t, resp: map[string]string{
		"login.vk.ru/":                             `{"data":{"access_token":"T1"}}`,
		"api.vk.ru/method/calls.getCallPreview":    `{"response":{}}`,
		"api.vk.ru/method/calls.getAnonymousToken": `{"error":{"error_code":14,"error_msg":"Captcha needed","captcha_sid":"123","captcha_img":"https://id.vk.ru/captcha","redirect_uri":"https://id.vk.ru/not_robot?session_token=ST&x=1"}}`,
	}}
	_, err := newTestClient(d).Fetch(context.Background(), "AbCdEf123456")
	var ce *provider.CaptchaRequiredError
	if !errors.As(err, &ce) || !provider.IsCaptcha(err) || ce.SessionToken != "ST" || ce.Sid != "123" {
		t.Fatalf("want captcha error, got %v", err)
	}
}

func TestFetchAPIError(t *testing.T) {
	d := &fakeDoer{t: t, resp: map[string]string{
		"login.vk.ru/":                             `{"data":{"access_token":"T1"}}`,
		"api.vk.ru/method/calls.getCallPreview":    `{"response":{}}`,
		"api.vk.ru/method/calls.getAnonymousToken": `{"error":{"error_code":29,"error_msg":"Rate limit reached"}}`,
	}}
	_, err := newTestClient(d).Fetch(context.Background(), "AbCdEf123456")
	if err == nil || provider.IsCaptcha(err) || !strings.Contains(err.Error(), "29") {
		t.Fatalf("got %v", err)
	}
}
```

Note: `Fetch` tries each VK app id in turn; on captcha it must stop immediately (not try the next app), on other errors it continues and returns the last error. The fake map serves the same body to every app, so `TestFetchAPIError` sees `len(d.calls)` = 3 hops × number of apps.

- [ ] **Step 3: Run to verify failure**

Run: `go test ./provider/... -v`
Expected: FAIL (undefined symbols).

- [ ] **Step 4: Write provider/vk/captcha.go and namegen.go**

```go
package vk

import (
	"fmt"
	neturl "net/url"

	"github.com/romanrublev/turnrelay/provider"
)

// vkAPIError turns the "error" object of a VK API response into an error.
// Error 14 on calls.getAnonymousToken is VK Smart Captcha.
func vkAPIError(obj map[string]any) error {
	code, _ := obj["error_code"].(float64)
	msg, _ := obj["error_msg"].(string)
	if int(code) == 14 {
		ce := &provider.CaptchaRequiredError{Img: str(obj["captcha_img"]), RedirectURI: str(obj["redirect_uri"])}
		switch v := obj["captcha_sid"].(type) {
		case string:
			ce.Sid = v
		case float64:
			ce.Sid = fmt.Sprintf("%.0f", v)
		}
		if u, err := neturl.Parse(ce.RedirectURI); err == nil {
			ce.SessionToken = u.Query().Get("session_token")
		}
		return ce
	}
	return fmt.Errorf("vk: API error %d: %s", int(code), msg)
}

func str(v any) string { s, _ := v.(string); return s }
```

`namegen.go`: a `func randomName() string` returning "<First> <Last>" from two short Russian/English lists (20 entries each) chosen with `math/rand/v2`; VK shows this name as the participant.

- [ ] **Step 5: Write provider/vk/client.go**

```go
// Package vk obtains TURN credentials from a VK Calls link, the way the VK
// web client does it (anonymous join). Chain and app ids follow
// cacggghp/vk-turn-proxy (GPL-3.0).
package vk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	neturl "net/url"
	"strings"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	tlsclient "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
	"github.com/google/uuid"

	"github.com/romanrublev/turnrelay/provider"
)

type Credential = provider.Credential

type Doer interface {
	Do(*fhttp.Request) (*fhttp.Response, error)
}

type Endpoints struct{ Login, API, OK string }

var DefaultEndpoints = Endpoints{
	Login: "https://login.vk.ru/?act=get_anonym_token",
	API:   "https://api.vk.ru/method/",
	OK:    "https://calls.okcdn.ru/fb.do",
}

type app struct{ id, secret string }

var apps = []app{
	{"6287487", "QbYic1K3lEV5kTGiqlq2"},  // VK_WEB_APP_ID
	{"7879029", "aR5NKGmm03GYrCiNKsaw"},  // VK_MVK_APP_ID
	{"52461373", "o557NLIkAErNhakXrQ7A"}, // VK_WEB_VKVIDEO_APP_ID
	{"52649896", "WStp4ihWG4l3nmXZgIbC"}, // VK_MVK_VKVIDEO_APP_ID
	{"51781872", "IjjCNl4L4Tf5QZEXIHKK"}, // VK_ID_AUTH_APP
}

const (
	userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36"
	secChUA   = `"Not(A:Brand";v="99", "Google Chrome";v="146", "Chromium";v="146"`
	okAppKey  = "CGMMEJLGDIHBABABA"
)

type Client struct {
	HTTP      Doer
	Endpoints Endpoints
	Sleep     func(time.Duration)
	Logf      func(string, ...any)
}

// publicResolverDialer bypasses the system resolver, which whitelisted
// networks often break first.
func publicResolverDialer() net.Dialer {
	return net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second, Resolver: &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			var last error
			for _, s := range []string{"77.88.8.8:53", "77.88.8.1:53", "8.8.8.8:53", "1.1.1.1:53"} {
				c, err := d.DialContext(ctx, "udp", s)
				if err == nil {
					return c, nil
				}
				last = err
			}
			return nil, last
		},
	}}
}

func NewClient() (*Client, error) {
	hc, err := tlsclient.NewHttpClient(tlsclient.NewNoopLogger(),
		tlsclient.WithTimeoutSeconds(20),
		tlsclient.WithClientProfile(profiles.Chrome_146),
		tlsclient.WithCookieJar(tlsclient.NewCookieJar()),
		tlsclient.WithDialer(publicResolverDialer()),
	)
	if err != nil {
		return nil, fmt.Errorf("vk: http client: %w", err)
	}
	return &Client{HTTP: hc, Endpoints: DefaultEndpoints, Sleep: time.Sleep, Logf: func(string, ...any) {}}, nil
}

func (c *Client) post(ctx context.Context, url, form string) (map[string]any, error) {
	req, err := fhttp.NewRequestWithContext(ctx, "POST", url, bytes.NewBufferString(form))
	if err != nil {
		return nil, err
	}
	h := req.Header
	h.Set("User-Agent", userAgent)
	h.Set("sec-ch-ua", secChUA)
	h.Set("sec-ch-ua-mobile", "?0")
	h.Set("sec-ch-ua-platform", `"Windows"`)
	h.Set("Content-Type", "application/x-www-form-urlencoded")
	h.Set("Accept", "*/*")
	h.Set("Origin", "https://vk.ru")
	h.Set("Referer", "https://vk.ru/")
	h.Set("Sec-Fetch-Site", "same-site")
	h.Set("Sec-Fetch-Mode", "cors")
	h.Set("Sec-Fetch-Dest", "empty")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("vk: %s: bad json: %w", url, err)
	}
	return m, nil
}

// Fetch runs the anonymous-join chain for one call link hash. It tries each
// known VK app id; a captcha aborts immediately.
func (c *Client) Fetch(ctx context.Context, link string) (Credential, error) {
	var last error
	for _, a := range apps {
		cred, err := c.fetchWith(ctx, link, a)
		if err == nil {
			return cred, nil
		}
		if provider.IsCaptcha(err) {
			return Credential{}, err
		}
		c.Logf("vk: app %s: %v", a.id, err)
		last = err
	}
	return Credential{}, fmt.Errorf("vk: all app ids failed: %w", last)
}

func (c *Client) fetchWith(ctx context.Context, link string, a app) (Credential, error) {
	e := c.Endpoints
	// 1. anonymous access token
	r, err := c.post(ctx, e.Login, fmt.Sprintf("client_id=%s&token_type=messages&client_secret=%s&version=1&app_id=%s", a.id, a.secret, a.id))
	if err != nil {
		return Credential{}, err
	}
	data, _ := r["data"].(map[string]any)
	token1 := str(data["access_token"])
	if token1 == "" {
		return Credential{}, fmt.Errorf("vk: no access_token in %v", r)
	}
	c.Sleep(120 * time.Millisecond)
	joinURL := "https://vk.com/call/join/" + link
	// 2. preview (best effort, mirrors the web client)
	_, _ = c.post(ctx, e.API+"calls.getCallPreview?v=5.275&client_id="+a.id, "vk_join_link="+joinURL+"&fields=photo_200&access_token="+token1)
	c.Sleep(300 * time.Millisecond)
	// 3. anonymous call token; captcha shows up here
	r, err = c.post(ctx, e.API+"calls.getAnonymousToken?v=5.275&client_id="+a.id, "vk_join_link="+joinURL+"&name="+neturl.QueryEscape(randomName())+"&access_token="+token1)
	if err != nil {
		return Credential{}, err
	}
	if eo, ok := r["error"].(map[string]any); ok {
		return Credential{}, vkAPIError(eo)
	}
	resp, _ := r["response"].(map[string]any)
	token2 := str(resp["token"])
	if token2 == "" {
		return Credential{}, fmt.Errorf("vk: no token in %v", r)
	}
	c.Sleep(120 * time.Millisecond)
	// 4. OK calls session
	session := fmt.Sprintf(`{"version":2,"device_id":"%s","client_version":1.1,"client_type":"SDK_JS"}`, uuid.New())
	r, err = c.post(ctx, e.OK, "session_data="+neturl.QueryEscape(session)+"&method=auth.anonymLogin&format=JSON&application_key="+okAppKey)
	if err != nil {
		return Credential{}, err
	}
	token3 := str(r["session_key"])
	if token3 == "" {
		return Credential{}, fmt.Errorf("vk: no session_key in %v", r)
	}
	c.Sleep(120 * time.Millisecond)
	// 5. join -> TURN credentials
	r, err = c.post(ctx, e.OK, "joinLink="+link+"&isVideo=false&protocolVersion=5&capabilities=2F7F&anonymToken="+token2+"&method=vchat.joinConversationByLink&format=JSON&application_key="+okAppKey+"&session_key="+token3)
	if err != nil {
		return Credential{}, err
	}
	ts, _ := r["turn_server"].(map[string]any)
	cred := Credential{Username: str(ts["username"]), Password: str(ts["credential"]), Link: link, FetchedAt: time.Now()}
	urls, _ := ts["urls"].([]any)
	for _, u := range urls {
		s := str(u)
		if !strings.HasPrefix(s, "turn:") && !strings.HasPrefix(s, "turns:") {
			continue
		}
		if strings.Contains(s, "transport=tcp") {
			continue
		}
		s = strings.TrimPrefix(strings.TrimPrefix(strings.SplitN(s, "?", 2)[0], "turn:"), "turns:")
		cred.Relays = append(cred.Relays, s)
	}
	if cred.Username == "" || cred.Password == "" || len(cred.Relays) == 0 {
		return Credential{}, fmt.Errorf("vk: incomplete turn_server in %v", r)
	}
	return cred, nil
}
```

- [ ] **Step 6: Run tests**

Run: `go mod tidy && go test -race ./provider/... -v`
Expected: PASS (4 tests).

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum provider && git commit -m "provider: credential interface, static provider, VK anonymous-join chain"
```

---

### Task 9: Credential pool

**Files:**
- Create: `credpool/pool.go`, `credpool/pool_test.go`

**Interfaces:**
- Produces:
  - `type Fetcher = provider.Fetcher`
  - `type Options struct { Links []string; ConnsPerSlot int; TTL, Margin, CooldownMin, CooldownMax, CaptchaCooldown time.Duration; Now func() time.Time; Sleep func(context.Context, time.Duration) error; Logf func(string, ...any) }`
  - `type Lease struct { Cred provider.Credential; Slot int; Index int /* 0..ConnsPerSlot-1 within slot, use for Relay(i) */ }`
  - `func New(f Fetcher, o Options) *Pool`
  - `func (p *Pool) Acquire(ctx context.Context, worker int) (*Lease, error)` blocks through cooldowns; returns `provider.CaptchaRequiredError` (wrapped) immediately when the slot is in captcha cooldown
  - `func (p *Pool) Release(l *Lease)`; `func (p *Pool) Failed(l *Lease, err error)` (486 -> slot saturated; auth -> slot invalidated; then Release)
  - `func (p *Pool) Stats() Stats { Slots, Active int; LastError string; CaptchaUntil time.Time }`

Policy (from anton48 credpool): slot s serves workers `s*ConnsPerSlot .. s*ConnsPerSlot+ConnsPerSlot-1`; slot s uses link `Links[s % len(Links)]`; a slot is usable when it has a credential younger than `TTL-Margin`, is not saturated and has fewer than `ConnsPerSlot` active leases; if the worker's own slot is unusable it takes any usable slot, else fetches a credential for its own slot (one fetch at a time, global cooldown `CooldownMin..CooldownMax` between fetches).

- [ ] **Step 1: Write the failing tests**

```go
package credpool

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/romanrublev/turnrelay/provider"
)

func opts() Options {
	return Options{Links: []string{"L1", "L2"}, ConnsPerSlot: 2, TTL: 10 * time.Minute, Margin: time.Minute,
		CooldownMin: 0, CooldownMax: 0, CaptchaCooldown: time.Minute,
		Now: time.Now, Sleep: func(context.Context, time.Duration) error { return nil }, Logf: func(string, ...any) {}}
}

func TestSlotsShareCredentialsAndRotateLinks(t *testing.T) {
	var n atomic.Int32
	f := func(_ context.Context, link string) (provider.Credential, error) {
		n.Add(1)
		return provider.Credential{Username: link + "-u", Relays: []string{"r1", "r2"}, Link: link}, nil
	}
	p := New(f, opts())
	l0, _ := p.Acquire(context.Background(), 0)
	l1, _ := p.Acquire(context.Background(), 1)
	l2, _ := p.Acquire(context.Background(), 2)
	if n.Load() != 2 {
		t.Fatalf("fetches %d", n.Load())
	}
	if l0.Slot != 0 || l1.Slot != 0 || l2.Slot != 1 {
		t.Fatalf("slots %d %d %d", l0.Slot, l1.Slot, l2.Slot)
	}
	if l0.Cred.Link != "L1" || l2.Cred.Link != "L2" {
		t.Fatalf("links %s %s", l0.Cred.Link, l2.Cred.Link)
	}
	if l0.Index == l1.Index {
		t.Fatal("indices within slot must differ")
	}
}

func TestQuotaMovesWorkerToAnotherSlot(t *testing.T) {
	f := func(_ context.Context, link string) (provider.Credential, error) {
		return provider.Credential{Username: link, Relays: []string{"r"}, Link: link}, nil
	}
	p := New(f, opts())
	l0, _ := p.Acquire(context.Background(), 0)
	p.Failed(l0, errors.New("relay: allocate: 486 Allocation Quota Reached"))
	l0b, err := p.Acquire(context.Background(), 0)
	if err != nil || l0b.Slot == 0 {
		t.Fatalf("worker 0 stayed on saturated slot: %+v %v", l0b, err)
	}
}

func TestAuthErrorRefetches(t *testing.T) {
	var n atomic.Int32
	f := func(_ context.Context, link string) (provider.Credential, error) {
		n.Add(1)
		return provider.Credential{Username: link, Relays: []string{"r"}, Link: link}, nil
	}
	p := New(f, opts())
	l0, _ := p.Acquire(context.Background(), 0)
	p.Failed(l0, errors.New("relay: allocate: 401 Unauthorized"))
	if _, err := p.Acquire(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if n.Load() != 2 {
		t.Fatalf("fetches %d, want re-fetch after auth error", n.Load())
	}
}

func TestExpiredCredentialRefetches(t *testing.T) {
	now := time.Now()
	o := opts()
	o.Now = func() time.Time { return now }
	var n atomic.Int32
	f := func(_ context.Context, link string) (provider.Credential, error) {
		n.Add(1)
		return provider.Credential{Username: link, Relays: []string{"r"}, Link: link, FetchedAt: now}, nil
	}
	p := New(f, o)
	l, _ := p.Acquire(context.Background(), 0)
	p.Release(l)
	now = now.Add(9*time.Minute + 30*time.Second)
	if _, err := p.Acquire(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if n.Load() != 2 {
		t.Fatalf("fetches %d", n.Load())
	}
}

func TestCaptchaCoolsDown(t *testing.T) {
	calls := 0
	f := func(context.Context, string) (provider.Credential, error) {
		calls++
		return provider.Credential{}, &provider.CaptchaRequiredError{Sid: "1"}
	}
	p := New(f, opts())
	_, err := p.Acquire(context.Background(), 0)
	if !provider.IsCaptcha(err) {
		t.Fatalf("got %v", err)
	}
	_, err = p.Acquire(context.Background(), 0)
	if !provider.IsCaptcha(err) || calls != 1 {
		t.Fatalf("second acquire should fail fast from cooldown: calls=%d err=%v", calls, err)
	}
	if p.Stats().CaptchaUntil.IsZero() {
		t.Fatal("stats lack captcha deadline")
	}
}

func TestCooldownBetweenFetches(t *testing.T) {
	o := opts()
	o.CooldownMin, o.CooldownMax = time.Second, time.Second
	var slept time.Duration
	o.Sleep = func(_ context.Context, d time.Duration) error { slept += d; return nil }
	f := func(_ context.Context, link string) (provider.Credential, error) {
		return provider.Credential{Username: link, Relays: []string{"r"}, Link: link}, nil
	}
	p := New(f, o)
	_, _ = p.Acquire(context.Background(), 0)
	_, _ = p.Acquire(context.Background(), 2) // second slot -> second fetch
	if slept < time.Second {
		t.Fatalf("no cooldown between fetches: %v", slept)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./credpool/ -v`
Expected: FAIL, package missing.

- [ ] **Step 3: Write pool.go**

```go
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
	if o.ConnsPerSlot <= 0 {
		o.ConnsPerSlot = 10
	}
	if o.TTL == 0 {
		o.TTL = 10 * time.Minute
	}
	if o.Margin == 0 {
		o.Margin = time.Minute
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
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
}

type Lease struct {
	Cred  provider.Credential
	Slot  int
	Index int
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

func (p *Pool) usable(s *slot) bool {
	return s != nil && s.valid && !s.saturated &&
		p.o.Now().Before(s.cred.FetchedAt.Add(p.o.TTL-p.o.Margin)) &&
		len(s.active) < p.o.ConnsPerSlot
}

func (p *Pool) lease(id int, s *slot) *Lease {
	for i := 0; i < p.o.ConnsPerSlot; i++ {
		if !s.active[i] {
			s.active[i] = true
			return &Lease{Cred: s.cred, Slot: id, Index: i}
		}
	}
	return nil
}

func (p *Pool) Acquire(ctx context.Context, worker int) (*Lease, error) {
	own := worker / p.o.ConnsPerSlot
	for {
		p.mu.Lock()
		if s := p.slots[own]; p.usable(s) {
			l := p.lease(own, s)
			p.mu.Unlock()
			return l, nil
		}
		for id, s := range p.slots {
			if p.usable(s) {
				l := p.lease(id, s)
				p.mu.Unlock()
				return l, nil
			}
		}
		if until := p.captcha; p.o.Now().Before(until) {
			p.mu.Unlock()
			return nil, fmt.Errorf("credpool: captcha cooldown until %s: %w", until.Format(time.Kitchen), &provider.CaptchaRequiredError{})
		}
		p.mu.Unlock()
		if err := p.fetchInto(ctx, own); err != nil {
			return nil, err
		}
	}
}

func (p *Pool) fetchInto(ctx context.Context, id int) error {
	p.fetchMu.Lock()
	defer p.fetchMu.Unlock()
	p.mu.Lock()
	if p.usable(p.slots[id]) { // someone fetched while we waited
		p.mu.Unlock()
		return nil
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
	p.o.Logf("credpool: slot %d refreshed from %s (%d relays)", id, cred.Link, len(cred.Relays))
	return nil
}

func (p *Pool) Release(l *Lease) {
	if l == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if s := p.slots[l.Slot]; s != nil {
		delete(s.active, l.Index)
	}
}

// Failed records why the allocation made with l did not work, then releases it.
func (p *Pool) Failed(l *Lease, err error) {
	if l == nil {
		return
	}
	p.mu.Lock()
	if s := p.slots[l.Slot]; s != nil {
		switch {
		case relay.IsQuotaError(err):
			s.saturated = true
			p.o.Logf("credpool: slot %d saturated (486)", l.Slot)
		case relay.IsAuthError(err):
			s.valid = false
			p.o.Logf("credpool: slot %d invalidated (%v)", l.Slot, err)
		}
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
```

Note for `TestQuotaMovesWorkerToAnotherSlot`: after the 486 the worker's own slot 0 is saturated; the loop finds no usable slot and calls `fetchInto(ctx, 0)`, which would overwrite slot 0. Fix inside `fetchInto`: when `p.slots[id]` is saturated, fetch into the lowest unused id instead (`for p.slots[id] != nil && p.slots[id].saturated { id++ }`), so saturated credentials are kept out of rotation until they expire. Implement that before running the tests.

- [ ] **Step 4: Run tests**

Run: `go test -race ./credpool/ -v`
Expected: PASS (6 tests).

- [ ] **Step 5: Commit**

```bash
git add credpool && git commit -m "credpool: quota-aware credential pool with cooldowns"
```

---

### Task 10: Mux pool (N workers, work-stealing uplink, merged downlink)

**Files:**
- Create: `mux/worker.go`, `mux/pool.go`, `mux/pool_test.go`

**Interfaces:**
- Consumes: `credpool.Pool` (Acquire/Release/Failed, Lease), `relay.Allocate`/`Allocation`, `obfs.Wrapper`, control frames from Task 2.
- Produces:
  - `type Acquirer interface { Acquire(ctx, worker int) (*credpool.Lease, error); Release(*credpool.Lease); Failed(*credpool.Lease, error) }`
  - `type Options struct { Workers int; Peer *net.UDPAddr; Wrapper obfs.Wrapper; Creds Acquirer; TURNUDP bool; TURNOverride string; ProbeInterval, ZombieAfter, StartPacing, KeepaliveInterval time.Duration; HandshakeSlots int; UplinkQueue, DownlinkQueue int; BackoffMin, BackoffMax time.Duration; Logf func(string, ...any) }`
  - `type Pool struct{...}`; `func New(o Options) *Pool`; `func (p *Pool) Start(ctx context.Context)`; `func (p *Pool) Close()`
  - `func (p *Pool) Write(ctx context.Context, b []byte) error` (copies b, blocks while the uplink queue is full)
  - `func (p *Pool) Read(ctx context.Context) ([]byte, error)`
  - `func (p *Pool) Session() [16]byte`
  - `func (p *Pool) Stats() Stats { Active, Connecting, Restarts int; LastError string }`
  - `func (p *Pool) WaitReady(ctx context.Context, n int) error` (blocks until n workers are active)

- [ ] **Step 1: Write the failing test**

The test wires turntest as the relay and obfstest as the VPS. The obfstest side must behave like anton48's server: consume hellos, echo probes, echo everything else. Add to `pool_test.go`:

```go
package mux_test

import (
	"bytes"
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/romanrublev/turnrelay/credpool"
	"github.com/romanrublev/turnrelay/provider"
	"github.com/romanrublev/turnrelay/mux"
	"github.com/romanrublev/turnrelay/obfs"
	"github.com/romanrublev/turnrelay/obfs/obfstest"
	"github.com/romanrublev/turnrelay/relay/turntest"
)

// fakeVPS accepts obfs conns and behaves like anton48's -srtp server:
// hellos are consumed and recorded, probes echoed, payload echoed.
type fakeVPS struct {
	srv    *obfstest.Server
	mu     sync.Mutex
	hellos map[[16]byte]int
	conns  int
}

func newFakeVPS(t *testing.T) *fakeVPS {
	v := &fakeVPS{srv: obfstest.ListenSRTP(t), hellos: map[[16]byte]int{}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		for {
			c, err := v.srv.Accept(ctx)
			if err != nil {
				return
			}
			v.mu.Lock()
			v.conns++
			v.mu.Unlock()
			go v.serve(c)
		}
	}()
	return v
}

func (v *fakeVPS) serve(c net.Conn) {
	defer c.Close()
	buf := make([]byte, 2048)
	for {
		n, err := c.Read(buf)
		if err != nil {
			return
		}
		if id, ok := mux.ParseHello(buf[:n]); ok {
			v.mu.Lock()
			v.hellos[id]++
			v.mu.Unlock()
			continue
		}
		if _, err := c.Write(buf[:n]); err != nil { // probes and payload alike
			return
		}
	}
}

func staticPool(ts *turntest.Server) *credpool.Pool {
	return credpool.New(func(context.Context, string) (provider.Credential, error) {
		return provider.Credential{Username: ts.Username, Password: ts.Password, Relays: []string{ts.Addr()}, Link: "L"}, nil
	}, credpool.Options{Links: []string{"L"}, ConnsPerSlot: 10, CooldownMin: time.Millisecond, CooldownMax: 2 * time.Millisecond})
}

func newPool(t *testing.T, workers int) (*mux.Pool, *fakeVPS, *turntest.Server) {
	ts := turntest.Start(t)
	vps := newFakeVPS(t)
	w, _ := obfs.New(obfs.ModeSRTP, obfs.Options{HandshakeTimeout: 5 * time.Second})
	p := mux.New(mux.Options{
		Workers: workers, Peer: vps.srv.Addr(), Wrapper: w, Creds: staticPool(ts), TURNUDP: true,
		ProbeInterval: 200 * time.Millisecond, ZombieAfter: 2 * time.Second, StartPacing: 10 * time.Millisecond,
		BackoffMin: 50 * time.Millisecond, BackoffMax: 200 * time.Millisecond,
		Logf: t.Logf,
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); p.Close() })
	p.Start(ctx)
	return p, vps, ts
}

func TestPoolEchoAcrossWorkers(t *testing.T) {
	p, vps, ts := newPool(t, 4)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := p.WaitReady(ctx, 4); err != nil {
		t.Fatal(err)
	}
	if ts.Allocations() != 4 {
		t.Fatalf("allocations %d", ts.Allocations())
	}
	const n = 200
	go func() {
		for i := 0; i < n; i++ {
			pkt := bytes.Repeat([]byte{byte(i)}, 1000)
			pkt[0] = 1 // never 0xff: keep clear of the control range
			if err := p.Write(ctx, pkt); err != nil {
				return
			}
		}
	}()
	got := 0
	for got < n {
		b, err := p.Read(ctx)
		if err != nil {
			t.Fatalf("read after %d: %v", got, err)
		}
		if len(b) != 1000 {
			t.Fatalf("len %d", len(b))
		}
		got++
	}
	vps.mu.Lock()
	defer vps.mu.Unlock()
	if len(vps.hellos) != 1 || vps.hellos[p.Session()] < 4 {
		t.Fatalf("hellos %v, session %x", vps.hellos, p.Session())
	}
}

func TestPoolRestartsDeadWorker(t *testing.T) {
	p, _, ts := newPool(t, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := p.WaitReady(ctx, 2); err != nil {
		t.Fatal(err)
	}
	// Kill the relay side: every allocation dies, workers must notice via
	// probe timeout (ZombieAfter) and come back once the relay is up again.
	ts.Restart(t)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		st := p.Stats()
		if st.Restarts >= 2 && st.Active == 2 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("workers did not recover: %+v", p.Stats())
}
```

`turntest.Server.Restart(t)` is needed: close the pion server and its socket, then start a new one on the same port (`net.ListenPacket("udp4", s.addr)`). Add it to `turntest/server.go` by extracting the construction into `func (s *Server) start(t *testing.T, pc net.PacketConn)`.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./mux/ -run Pool -v`
Expected: FAIL (undefined mux.New, mux.Options, ...).

- [ ] **Step 3: Write worker.go**

```go
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
	p.readyCond.Broadcast()
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
```

- [ ] **Step 4: Write pool.go**

```go
package mux

import (
	"context"
	"crypto/rand"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/romanrublev/turnrelay/credpool"
	"github.com/romanrublev/turnrelay/obfs"
)

type Acquirer interface {
	Acquire(ctx context.Context, worker int) (*credpool.Lease, error)
	Release(*credpool.Lease)
	Failed(*credpool.Lease, error)
}

type Options struct {
	Workers           int
	Peer              *net.UDPAddr
	Wrapper           obfs.Wrapper
	Creds             Acquirer
	TURNUDP           bool
	TURNOverride      string
	ProbeInterval     time.Duration
	ZombieAfter       time.Duration
	StartPacing       time.Duration
	KeepaliveInterval time.Duration
	HandshakeSlots    int
	UplinkQueue       int
	DownlinkQueue     int
	BackoffMin        time.Duration
	BackoffMax        time.Duration
	Logf              func(string, ...any)
}

func (o *Options) defaults() {
	if o.Workers <= 0 {
		o.Workers = 30
	}
	if o.ProbeInterval == 0 {
		o.ProbeInterval = 30 * time.Second
	}
	if o.ZombieAfter == 0 {
		o.ZombieAfter = 120 * time.Second
	}
	if o.StartPacing == 0 {
		o.StartPacing = 100 * time.Millisecond
	}
	if o.KeepaliveInterval == 0 {
		o.KeepaliveInterval = 10 * time.Second
	}
	if o.HandshakeSlots == 0 {
		o.HandshakeSlots = 3
	}
	if o.UplinkQueue == 0 {
		o.UplinkQueue = 256
	}
	if o.DownlinkQueue == 0 {
		o.DownlinkQueue = 2048
	}
	if o.BackoffMin == 0 {
		o.BackoffMin = 2 * time.Second
	}
	if o.BackoffMax == 0 {
		o.BackoffMax = 60 * time.Second
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
}

type Stats struct {
	Active     int
	Connecting int
	Restarts   int
	LastError  string
}

type Pool struct {
	o          Options
	session    [16]byte
	up         chan []byte
	down       chan []byte
	handshakes chan struct{}
	active     atomic.Int32
	connecting atomic.Int32
	restarts   atomic.Int32
	errMu      sync.Mutex
	lastErr    error
	readyMu    sync.Mutex
	readyCond  *sync.Cond
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	closeOnce  sync.Once
	closed     chan struct{}
}

func New(o Options) *Pool {
	o.defaults()
	p := &Pool{o: o, up: make(chan []byte, o.UplinkQueue), down: make(chan []byte, o.DownlinkQueue),
		handshakes: make(chan struct{}, o.HandshakeSlots), closed: make(chan struct{})}
	_, _ = rand.Read(p.session[:])
	p.readyCond = sync.NewCond(&p.readyMu)
	return p
}

func (p *Pool) Session() [16]byte { return p.session }

func (p *Pool) Start(ctx context.Context) {
	ctx, p.cancel = context.WithCancel(ctx)
	for i := 0; i < p.o.Workers; i++ {
		w := &worker{id: i, pool: p}
		p.wg.Add(1)
		go func(delay time.Duration) {
			defer p.wg.Done()
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			w.run(ctx)
		}(time.Duration(i) * p.o.StartPacing)
	}
}

func (p *Pool) Close() {
	p.closeOnce.Do(func() {
		if p.cancel != nil {
			p.cancel()
		}
		close(p.closed)
		p.readyCond.Broadcast()
		p.wg.Wait()
	})
}

func (p *Pool) Write(ctx context.Context, b []byte) error {
	pkt := make([]byte, len(b))
	copy(pkt, b)
	select {
	case p.up <- pkt:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-p.closed:
		return net.ErrClosed
	}
}

func (p *Pool) Read(ctx context.Context) ([]byte, error) {
	select {
	case pkt := <-p.down:
		return pkt, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.closed:
		return nil, net.ErrClosed
	}
}

// WaitReady blocks until at least n workers are active.
func (p *Pool) WaitReady(ctx context.Context, n int) error {
	stop := context.AfterFunc(ctx, p.readyCond.Broadcast)
	defer stop()
	p.readyMu.Lock()
	defer p.readyMu.Unlock()
	for int(p.active.Load()) < n {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		select {
		case <-p.closed:
			return net.ErrClosed
		default:
		}
		p.readyCond.Wait()
	}
	return nil
}

func (p *Pool) setErr(err error) {
	p.errMu.Lock()
	p.lastErr = err
	p.errMu.Unlock()
}

func (p *Pool) Stats() Stats {
	p.errMu.Lock()
	defer p.errMu.Unlock()
	st := Stats{Active: int(p.active.Load()), Connecting: int(p.connecting.Load()), Restarts: int(p.restarts.Load())}
	if p.lastErr != nil {
		st.LastError = p.lastErr.Error()
	}
	return st
}
```

`p.readyCond.Broadcast()` in `worker.once` after `p.active.Add(1)` must hold `readyMu` for the wait loop to be race-free: wrap it as `p.readyMu.Lock(); p.readyCond.Broadcast(); p.readyMu.Unlock()`.

- [ ] **Step 5: Run tests**

Run: `go test -race ./mux/ -v -timeout 120s`
Expected: PASS. `TestPoolRestartsDeadWorker` relies on the probe path: after the relay restarts, allocations are gone, so the relay never forwards probes, `lastInbound` ages past `ZombieAfter` (2 s) and the worker restarts. If it flakes, raise the outer deadline before touching the logic.

- [ ] **Step 6: Commit**

```bash
git add mux relay/turntest && git commit -m "mux: worker pool with work-stealing uplink, merged downlink, probes"
```

---

### Task 11: Public Dialer (turnrelay package)

**Files:**
- Create: `dialer.go`, `conn.go`, `dialer_test.go`

**Interfaces:**
- Consumes: provider.Fetcher, vk.NewClient, static.New, credpool.Pool, obfs.New, mux.Pool.
- Produces (the API sing-box will call):
  ```go
  type Mode = obfs.Mode
  type CaptchaPolicy string // "fail" (default) | "wait"
  type Config struct {
      Provider     string        // "vk" (default) | "static"
      CallLinks    []string      // vk
      TURNServer   string        // static: relay host:port; vk: optional override
      TURNUsername string        // static
      TURNPassword string        // static
      Server       netip.AddrPort
      Connections  int
      Mode         Mode
      Password     string
      WrapKey      []byte
      TURNUDP      *bool         // nil means true
      Captcha      CaptchaPolicy
      Logf         func(string, ...any)
      // test hook: replaces the provider entirely
      Fetcher      provider.Fetcher
  }
  type Stats struct { Workers, Active, Connecting, Restarts int; CredSlots int; CaptchaUntil time.Time; LastError string }
  type Dialer struct{ ... }
  func New(cfg Config) (*Dialer, error)
  func (d *Dialer) Start(ctx context.Context) error
  func (d *Dialer) Close() error
  func (d *Dialer) DialContext(ctx context.Context, network string, dest M.Socksaddr) (net.Conn, error)
  func (d *Dialer) ListenPacket(ctx context.Context, dest M.Socksaddr) (net.PacketConn, error)
  func (d *Dialer) Stats() Stats
  func (d *Dialer) WaitReady(ctx context.Context, n int) error
  ```
  `DialContext` accepts only `"udp"`, `"udp4"`, `"udp6"`; anything else returns `ErrTCPUnsupported`. The returned `net.Conn` is a datagram conn; `ListenPacket` wraps the same pipe as a `net.PacketConn` whose `ReadFrom` reports `Server` as the source and whose `WriteTo` ignores the address.
  Validation errors: unknown provider, vk without links or with an invalid link, static without server/username/password, invalid server, `Connections` > 60, wrap mode without password/key.

- [ ] **Step 1: Write the failing test**

```go
package turnrelay_test

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"

	"github.com/romanrublev/turnrelay"
	"github.com/romanrublev/turnrelay/provider"
	"github.com/romanrublev/turnrelay/mux"
	"github.com/romanrublev/turnrelay/obfs/obfstest"
	"github.com/romanrublev/turnrelay/relay/turntest"
)

func echoVPS(t *testing.T) *obfstest.Server {
	srv := obfstest.ListenSRTP(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		for {
			c, err := srv.Accept(ctx)
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				buf := make([]byte, 2048)
				for {
					n, err := c.Read(buf)
					if err != nil {
						return
					}
					if _, ok := mux.ParseHello(buf[:n]); ok {
						continue
					}
					if _, err := c.Write(buf[:n]); err != nil {
						return
					}
				}
			}()
		}
	}()
	return srv
}

func newDialer(t *testing.T) *turnrelay.Dialer {
	ts := turntest.Start(t)
	vps := echoVPS(t)
	d, err := turnrelay.New(turnrelay.Config{
		CallLinks:   []string{"https://vk.ru/call/join/TESTLINK1234"},
		Server:      netip.MustParseAddrPort(vps.Addr().String()),
		Connections: 3,
		Mode:        turnrelay.ModeSRTP,
		Logf:        t.Logf,
		Fetcher: func(context.Context, string) (provider.Credential, error) {
			return provider.Credential{Username: ts.Username, Password: ts.Password, Relays: []string{ts.Addr()}, Link: "TESTLINK1234"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); _ = d.Close() })
	if err := d.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDialContextUDPRoundTrip(t *testing.T) {
	d := newDialer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := d.WaitReady(ctx, 1); err != nil {
		t.Fatal(err)
	}
	c, err := d.DialContext(ctx, "udp", M.ParseSocksaddr("203.0.113.5:56004"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for i := 0; i < 20; i++ {
		msg := []byte{1, byte(i), 2, 3}
		if _, err := c.Write(msg); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 2048)
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, err := c.Read(buf)
		if err != nil || string(buf[:n]) != string(msg) {
			t.Fatalf("i=%d n=%d err=%v", i, n, err)
		}
	}
	if _, err := d.DialContext(ctx, "tcp", M.ParseSocksaddr("1.1.1.1:443")); err != turnrelay.ErrTCPUnsupported {
		t.Fatalf("tcp: %v", err)
	}
	st := d.Stats()
	if st.Active < 1 || st.Workers != 3 {
		t.Fatalf("stats %+v", st)
	}
}

func TestListenPacket(t *testing.T) {
	d := newDialer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := d.WaitReady(ctx, 1); err != nil {
		t.Fatal(err)
	}
	pc, err := d.ListenPacket(ctx, M.Socksaddr{Addr: netip.IPv4Unspecified()})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	if _, err := pc.WriteTo([]byte("x"), &net.UDPAddr{IP: net.IPv4(203, 0, 113, 5), Port: 56004}); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	_ = pc.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, from, err := pc.ReadFrom(buf)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if from.String() != d.ServerAddr().String() {
		t.Fatalf("from %s", from)
	}
}

func TestConfigValidation(t *testing.T) {
	base := turnrelay.Config{CallLinks: []string{"https://vk.ru/call/join/TESTLINK1234"}, Server: netip.MustParseAddrPort("203.0.113.5:56004")}
	cases := map[string]func(*turnrelay.Config){
		"no links":       func(c *turnrelay.Config) { c.CallLinks = nil },
		"bad link":       func(c *turnrelay.Config) { c.CallLinks = []string{"x"} },
		"bad provider":   func(c *turnrelay.Config) { c.Provider = "yandex" },
		"static no user": func(c *turnrelay.Config) { c.Provider = "static"; c.TURNServer = "1.2.3.4:3478" },
		"no server":      func(c *turnrelay.Config) { c.Server = netip.AddrPort{} },
		"too many":       func(c *turnrelay.Config) { c.Connections = 61 },
		"wrap no key":    func(c *turnrelay.Config) { c.Mode = turnrelay.ModeWrap },
		"bad mode":       func(c *turnrelay.Config) { c.Mode = "plain" },
	}
	for name, mutate := range cases {
		c := base
		mutate(&c)
		if _, err := turnrelay.New(c); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := turnrelay.New(base); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	st := turnrelay.Config{Provider: "static", TURNServer: "1.2.3.4:3478", TURNUsername: "u", TURNPassword: "p", Server: base.Server}
	if _, err := turnrelay.New(st); err != nil {
		t.Fatalf("valid static config rejected: %v", err)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test . -v`
Expected: FAIL (undefined turnrelay.New ...).

- [ ] **Step 3: Write dialer.go**

```go
package turnrelay

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"

	M "github.com/sagernet/sing/common/metadata"

	"github.com/romanrublev/turnrelay/credpool"
	"github.com/romanrublev/turnrelay/provider"
	"github.com/romanrublev/turnrelay/provider/static"
	"github.com/romanrublev/turnrelay/provider/vk"
	"github.com/romanrublev/turnrelay/mux"
	"github.com/romanrublev/turnrelay/obfs"
)

type Mode = obfs.Mode

const (
	ModeSRTP = obfs.ModeSRTP
	ModeWrap = obfs.ModeWrap
	ModeDTLS = obfs.ModeDTLS

	DefaultConnections = 30
	MaxConnections     = 60
)

type CaptchaPolicy string

const (
	CaptchaFail CaptchaPolicy = "fail"
	CaptchaWait CaptchaPolicy = "wait"
)

var ErrTCPUnsupported = errors.New("turnrelay: only udp is supported; put a wireguard endpoint on top")

const (
	ProviderVK     = "vk"
	ProviderStatic = "static"
)

type Config struct {
	Provider     string
	CallLinks    []string
	TURNServer   string
	TURNUsername string
	TURNPassword string
	Server       netip.AddrPort
	Connections  int
	Mode         Mode
	Password     string
	WrapKey      []byte
	TURNUDP      *bool
	Captcha      CaptchaPolicy
	Logf         func(string, ...any)
	Fetcher      provider.Fetcher // nil: build from Provider
}

type Stats struct {
	Workers, Active, Connecting, Restarts int
	CredSlots                             int
	CaptchaUntil                          time.Time
	LastError                             string
}

type Dialer struct {
	cfg   Config
	links []string
	creds *credpool.Pool
	pool  *mux.Pool
	peer  *net.UDPAddr
}

func New(cfg Config) (*Dialer, error) {
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	if cfg.Provider == "" {
		cfg.Provider = ProviderVK
	}
	var (
		links    []string
		fetcher  = cfg.Fetcher
		override string
	)
	switch cfg.Provider {
	case ProviderVK:
		if len(cfg.CallLinks) == 0 {
			return nil, errors.New("turnrelay: provider vk needs at least one call link")
		}
		for _, l := range cfg.CallLinks {
			h, err := vk.ParseCallLink(l)
			if err != nil {
				return nil, fmt.Errorf("turnrelay: %w", err)
			}
			links = append(links, h)
		}
		override = cfg.TURNServer
		if fetcher == nil {
			client, err := vk.NewClient()
			if err != nil {
				return nil, err
			}
			client.Logf = cfg.Logf
			fetcher = client.Fetch
		}
	case ProviderStatic:
		if cfg.TURNServer == "" || cfg.TURNUsername == "" || cfg.TURNPassword == "" {
			return nil, errors.New("turnrelay: provider static needs turn server, username and password")
		}
		links = []string{"static"}
		if fetcher == nil {
			fetcher = static.New(cfg.TURNServer, cfg.TURNUsername, cfg.TURNPassword)
		}
	default:
		return nil, fmt.Errorf("turnrelay: unknown provider %q", cfg.Provider)
	}
	if !cfg.Server.IsValid() || cfg.Server.Port() == 0 {
		return nil, errors.New("turnrelay: server address is required")
	}
	if cfg.Connections == 0 {
		cfg.Connections = DefaultConnections
	}
	if cfg.Connections < 1 || cfg.Connections > MaxConnections {
		return nil, fmt.Errorf("turnrelay: connections must be 1..%d (each 10 use one VK participant slot)", MaxConnections)
	}
	if cfg.Mode == "" {
		cfg.Mode = ModeSRTP
	}
	if cfg.Mode == ModeDTLS {
		cfg.Logf("turnrelay: mode dtls is deprecated: VK relays shape it to a few KB/s")
	}
	wrapper, err := obfs.New(cfg.Mode, obfs.Options{Password: cfg.Password, WrapKey: cfg.WrapKey})
	if err != nil {
		return nil, fmt.Errorf("turnrelay: %w", err)
	}
	if cfg.Captcha == "" {
		cfg.Captcha = CaptchaFail
	}
	// Both policies currently behave the same inside the library: the pool
	// reports the captcha, cools down for a minute, and workers retry with
	// backoff. "wait" is accepted so configs stay valid once M2 wires a
	// callback for graphical clients.
	udp := true
	if cfg.TURNUDP != nil {
		udp = *cfg.TURNUDP
	}
	d := &Dialer{cfg: cfg, links: links, peer: net.UDPAddrFromAddrPort(cfg.Server)}
	d.creds = credpool.New(fetcher, credpool.Options{Links: links, Logf: cfg.Logf})
	d.pool = mux.New(mux.Options{
		Workers: cfg.Connections, Peer: d.peer, Wrapper: wrapper, Creds: d.creds,
		TURNUDP: udp, TURNOverride: override, Logf: cfg.Logf,
	})
	return d, nil
}

func (d *Dialer) Start(ctx context.Context) error {
	d.pool.Start(ctx)
	return nil
}

func (d *Dialer) Close() error {
	d.pool.Close()
	return nil
}

func (d *Dialer) ServerAddr() *net.UDPAddr { return d.peer }

func (d *Dialer) WaitReady(ctx context.Context, n int) error { return d.pool.WaitReady(ctx, n) }

func (d *Dialer) DialContext(ctx context.Context, network string, dest M.Socksaddr) (net.Conn, error) {
	if !strings.HasPrefix(network, "udp") {
		return nil, ErrTCPUnsupported
	}
	if dest.IsValid() && dest.AddrPort() != d.cfg.Server {
		d.cfg.Logf("turnrelay: dial to %s ignored, datagrams always go to %s", dest, d.cfg.Server)
	}
	return newDatagramConn(d), nil
}

func (d *Dialer) ListenPacket(ctx context.Context, dest M.Socksaddr) (net.PacketConn, error) {
	return &packetConn{datagramConn: newDatagramConn(d)}, nil
}

func (d *Dialer) Stats() Stats {
	ps, cs := d.pool.Stats(), d.creds.Stats()
	st := Stats{Workers: d.cfg.Connections, Active: ps.Active, Connecting: ps.Connecting, Restarts: ps.Restarts,
		CredSlots: cs.Slots, CaptchaUntil: cs.CaptchaUntil, LastError: ps.LastError}
	if st.LastError == "" {
		st.LastError = cs.LastError
	}
	return st
}
```

- [ ] **Step 4: Write conn.go**

```go
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

func (c *datagramConn) SetDeadline(t time.Time) error      { return c.SetReadDeadline(t) }
func (c *datagramConn) SetWriteDeadline(time.Time) error   { return nil }

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
```

A subtlety: `SetReadDeadline` cancels the previous context, which would make an in-flight `Read` return `os.ErrDeadlineExceeded` even for the zero-time reset. pion and wireguard-go call `SetReadDeadline(time.Now())` only to interrupt reads, so this is the behaviour they expect; document it in a comment.

- [ ] **Step 5: Run tests**

Run: `go test -race . -v -timeout 120s`
Expected: PASS (3 tests).

- [ ] **Step 6: Commit**

```bash
git add dialer.go conn.go dialer_test.go && git commit -m "turnrelay: public dialer with datagram conn and packet conn"
```

---

### Task 12: turnrelay-udp CLI

**Files:**
- Create: `cmd/turnrelay-udp/main.go`

**Interfaces:**
- Consumes: `turnrelay.New/Start/DialContext/Stats`.
- Produces: binary `turnrelay-udp` with flags `-listen 127.0.0.1:9000`, `-provider vk|static`, `-link` (repeatable, vk), `-turn host:port` (static relay, or override for vk), `-turn-user`, `-turn-pass` (static), `-server host:port`, `-n 30`, `-mode srtp|wrap|dtls`, `-password`, `-wrap-key hex`, `-tcp` (TURN over TCP), `-stats 10s`. It forwards datagrams from the local socket into the pipe and returns downlink datagrams to the last local sender (the same local-peer scheme as cacggghp's client, so wg-quick with `Endpoint = 127.0.0.1:9000` works unchanged).

- [ ] **Step 1: Write main.go**

```go
// turnrelay-udp bridges a local UDP socket to the VK TURN pipe. Point a
// WireGuard client at -listen and run the matching server on the VPS.
package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	M "github.com/sagernet/sing/common/metadata"

	"github.com/romanrublev/turnrelay"
)

type links []string

func (l *links) String() string     { return strings.Join(*l, ",") }
func (l *links) Set(s string) error { *l = append(*l, s); return nil }

func main() {
	var ls links
	listen := flag.String("listen", "127.0.0.1:9000", "local UDP socket for the WireGuard client")
	prov := flag.String("provider", "vk", "credential provider: vk | static")
	flag.Var(&ls, "link", "VK call link (repeatable, provider vk)")
	turnUser := flag.String("turn-user", "", "relay username (provider static)")
	turnPass := flag.String("turn-pass", "", "relay password (provider static)")
	server := flag.String("server", "", "VPS host:port running the relay-side server")
	n := flag.Int("n", turnrelay.DefaultConnections, "TURN allocations (each 10 = one VK participant)")
	mode := flag.String("mode", "srtp", "srtp | wrap | dtls")
	password := flag.String("password", "", "wrap mode: tunnel password")
	wrapKey := flag.String("wrap-key", "", "wrap mode: raw 32-byte key, hex")
	turnServer := flag.String("turn", "", "TURN relay host:port (required for static, optional override for vk)")
	tcp := flag.Bool("tcp", false, "use TCP to the TURN relay (slower)")
	statsEvery := flag.Duration("stats", 10*time.Second, "stats log interval")
	flag.Parse()

	ap, err := netip.ParseAddrPort(*server)
	if err != nil {
		log.Fatalf("-server: %v", err)
	}
	var key []byte
	if *wrapKey != "" {
		if key, err = hex.DecodeString(*wrapKey); err != nil {
			log.Fatalf("-wrap-key: %v", err)
		}
	}
	udp := !*tcp
	d, err := turnrelay.New(turnrelay.Config{
		Provider: *prov, CallLinks: ls, TURNServer: *turnServer, TURNUsername: *turnUser, TURNPassword: *turnPass,
		Server: ap, Connections: *n, Mode: turnrelay.Mode(*mode),
		Password: *password, WrapKey: key, TURNUDP: &udp,
		Logf: log.Printf,
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := d.Start(ctx); err != nil {
		log.Fatal(err)
	}
	defer d.Close()

	local, err := net.ListenPacket("udp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	defer local.Close()
	pipe, err := d.DialContext(ctx, "udp", M.SocksaddrFromNetIP(ap))
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("listening on %s, %d connections via %s (%s)", *listen, *n, *server, *mode)

	var lastPeer atomic.Value // net.Addr
	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := local.ReadFrom(buf)
			if err != nil {
				return
			}
			lastPeer.Store(from)
			if _, err := pipe.Write(buf[:n]); err != nil {
				log.Printf("uplink: %v", err)
				return
			}
		}
	}()
	go func() {
		buf := make([]byte, 2048)
		for {
			n, err := pipe.Read(buf)
			if err != nil {
				return
			}
			if to, ok := lastPeer.Load().(net.Addr); ok {
				_, _ = local.WriteTo(buf[:n], to)
			}
		}
	}()
	t := time.NewTicker(*statsEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(os.Stderr, "shutting down")
			return
		case <-t.C:
			log.Printf("stats: %+v", d.Stats())
		}
	}
}
```

- [ ] **Step 2: Build and smoke-test the flags**

Run: `go build ./cmd/turnrelay-udp && ./turnrelay-udp -h 2>&1 | head -5 && ./turnrelay-udp -server 203.0.113.5:56004; echo exit=$?`
Expected: usage prints; the second run fails fast with "provider vk needs at least one call link", exit 1.

- [ ] **Step 3: Commit**

```bash
rm -f turnrelay-udp && git add cmd && git commit -m "cmd: turnrelay-udp local socket bridge"
```

---

### Task 13: Interop test against the real anton48 SRTP server (docker, no VK)

**Files:**
- Create: `test/integration/docker-compose.yml`, `test/integration/Dockerfile.vkserver`, `test/integration/Dockerfile.client`, `test/integration/coturn.conf`, `test/integration/wg/server.conf`, `test/integration/wg/client.conf`, `test/integration/run.sh`, `test/integration/README.md`

**Interfaces:**
- Consumes: `turnrelay-udp` binary.
- Produces: a script that proves `turnrelay-udp` interoperates with the unmodified anton48 server (`add-server-srtp-layer`) and a real WireGuard, using coturn as the relay. Manual (`make integration`), not part of `go test`.

- [ ] **Step 1: Write the compose topology**

Services on one bridge network `172.28.0.0/24`:
- `coturn` (image `coturn/coturn:4.6`), `--user=user:pass --realm=turnrelay.test --lt-cred-mech --listening-ip=172.28.0.10 --relay-ip=172.28.0.10 --no-tls --no-dtls --min-port=49152 --max-port=49200`, static ip `.10`.
- `vkserver` built from `Dockerfile.vkserver`: `git clone -b add-server-srtp-layer https://github.com/anton48/vk-turn-proxy` + `go build ./server`, runs `./server -listen 0.0.0.0:56004 -connect 172.28.0.30:51820 -srtp`, static ip `.20`.
- `wgserver` (image `lscr.io/linuxserver/wireguard`), config `wg/server.conf` (Address 10.99.0.1/24, ListenPort 51820, peer = client key, AllowedIPs 10.99.0.2/32), `sysctls net.ipv4.ip_forward=1`, iptables MASQUERADE, static ip `.30`.
- `web` (image `hashicorp/http-echo`, `-text=ok-through-relay`), static ip `.40`.
- `client` built from `Dockerfile.client`: multi-stage build of `turnrelay-udp` from the repo root, plus `wireguard-tools` and `curl`; `cap_add: NET_ADMIN`; entrypoint runs `turnrelay-udp -listen 127.0.0.1:9000 -provider static -turn 172.28.0.10:3478 -turn-user user -turn-pass pass -server 172.28.0.20:56004 -n 4 &`, waits for "worker 0 up", `wg-quick up ./wg/client.conf` (Endpoint 127.0.0.1:9000, MTU 1280, AllowedIPs 172.28.0.40/32), then `curl -s --max-time 10 http://172.28.0.40:5678` and exits 0 only if the body equals `ok-through-relay`.

- [ ] **Step 2: Confirm the static provider path**

No code change: `-provider static` (Task 12) skips VK entirely, so the compose client never reaches the internet.

- [ ] **Step 3: Write run.sh**

```bash
#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
docker compose build
docker compose up --abort-on-container-exit --exit-code-from client
status=$?
docker compose logs vkserver | grep -E 'group|hello|conn' | tail -20 || true
docker compose down -v
exit $status
```

Add to `Makefile`: `integration:\n\tbash test/integration/run.sh`.

- [ ] **Step 4: Run it**

Run: `make integration`
Expected: client container prints `ok-through-relay` and exits 0; `vkserver` log shows one group with 4 connections (lines like `group <hex>: ... conns=4`).

Known interop risks to check in the server log if it fails: (1) the server demuxes DTLS vs RTP by first byte, so our RTP packets must never start with a byte in 20..63 (they start with 0x80, fine); (2) the server expects `SRTP_AES128_CM_HMAC_SHA1_80` and rejects other profiles; (3) hello must be exactly 20 bytes.

- [ ] **Step 5: Commit**

```bash
git add test/integration Makefile && git commit -m "test: docker interop with the anton48 srtp server, coturn and wireguard"
```

---

### Task 14: VPS runbook for the VK e2e run

**Files:**
- Create: `scripts/vps-setup.sh`, `docs/e2e.md`

**Interfaces:**
- Produces: a reproducible server side for the e2e acceptance criteria in the spec (section 9).

- [ ] **Step 1: Write scripts/vps-setup.sh (Debian/Ubuntu, run as root)**

```bash
#!/usr/bin/env bash
# Installs WireGuard and the anton48 SRTP server on a fresh Debian/Ubuntu VPS.
# Usage: vps-setup.sh <public-ip> [wg-port=51820] [proxy-port=56004]
set -euo pipefail
PUB=${1:?public ip}; WGPORT=${2:-51820}; PXPORT=${3:-56004}
apt-get update && apt-get install -y wireguard git golang-go iptables curl
mkdir -p /opt/turnrelay && cd /opt/turnrelay
[ -d vk-turn-proxy ] || git clone -b add-server-srtp-layer https://github.com/anton48/vk-turn-proxy
(cd vk-turn-proxy && go build -o /opt/turnrelay/server ./server)
umask 077
[ -f server.key ] || { wg genkey > server.key; wg pubkey < server.key > server.pub; }
[ -f client.key ] || { wg genkey > client.key; wg pubkey < client.key > client.pub; }
IFACE=$(ip -o -4 route show to default | awk '{print $5}')
cat > /etc/wireguard/wg0.conf <<EOC
[Interface]
Address = 10.8.0.1/24
ListenPort = $WGPORT
PrivateKey = $(cat server.key)
PostUp = iptables -t nat -A POSTROUTING -s 10.8.0.0/24 -o $IFACE -j MASQUERADE
PostDown = iptables -t nat -D POSTROUTING -s 10.8.0.0/24 -o $IFACE -j MASQUERADE
[Peer]
PublicKey = $(cat client.pub)
AllowedIPs = 10.8.0.2/32
EOC
sysctl -w net.ipv4.ip_forward=1
systemctl enable --now wg-quick@wg0
cat > /etc/systemd/system/turnrelay-server.service <<EOC
[Unit]
Description=vk-turn-proxy SRTP server
After=network.target wg-quick@wg0.service
[Service]
ExecStart=/opt/turnrelay/server -listen 0.0.0.0:$PXPORT -connect 127.0.0.1:$WGPORT -srtp
Restart=always
RestartSec=5
[Install]
WantedBy=multi-user.target
EOC
systemctl daemon-reload && systemctl enable --now turnrelay-server
echo "server public key: $(cat server.pub)"
echo "client private key: $(cat client.key)"
echo "proxy endpoint: $PUB:$PXPORT   wg tunnel address: 10.8.0.2/32"
```

- [ ] **Step 2: Write docs/e2e.md**

Steps for the operator: run the script; on the laptop create `wg-vk.conf` (PrivateKey = client key, Address 10.8.0.2/32, DNS 1.1.1.1, MTU 1280, Peer PublicKey = server key, Endpoint 127.0.0.1:9000, AllowedIPs 0.0.0.0/1,128.0.0.0/1 minus the VK relay range as in cacggghp README); run `turnrelay-udp -listen 127.0.0.1:9000 -link <call link> -server <ip>:56004 -n 30`; wait for `worker 0 up`; `wg-quick up ./wg-vk.conf`; acceptance: `curl https://ifconfig.me` returns the VPS IP, `iperf3 -c <vps> -R -t 20` (iperf3 server on the VPS, bound to 10.8.0.1) shows >= 30 Mbit/s, `turnrelay-udp` stats show `Active: 30`, `CaptchaUntil` zero. Record the numbers in the doc after the run.

- [ ] **Step 3: Commit**

```bash
git add scripts docs/e2e.md && git commit -m "docs: VPS setup script and e2e runbook"
```

---

### Task 15: Protocol note

**Files:**
- Create: `docs/protocol.md`

**Interfaces:**
- Produces: the milestone 1 written deliverable; the RFC issue links to it.

- [ ] **Step 1: Write docs/protocol.md with these sections, each with the concrete values from the spec and code**

1. Overview diagram (client, VK relay, VPS) and the layer stack per allocation.
2. Credential chain: the five HTTP requests with method, URL, form fields and the JSON field consumed from each response; app ids; captcha error shape (`error_code 14`, `redirect_uri` with `session_token`); TTL and quota (10 allocations per credential, 486); why 30 connections = 3 participants.
3. Relay: TURN Allocate / CreatePermission / ChannelBind as done by pion/turn, UDP vs TCP transport, keepalive.
4. Obfuscation modes with byte layouts:
   - `srtp`: DTLS handshake parameters, `use_srtp` profile, RTP header field values, SRTP key derivation reference, demux rule.
   - `wrap`: HKDF parameters, header, nonce construction, padding, direction and nonce-reuse note (nonce space per SSRC is 2^32 timestamps stepped by 960: rekey by reconnecting well before ~4.4M packets; the mux restarts allocations on any error and VK expires allocations at 10 minutes, so in practice sessions are far shorter).
   - `dtls`: legacy, deprecated, measured shaping.
5. Control plane: hello and probe layouts, when sent, server behaviour (consume hello, echo probe), backward compatibility argument (0xff outside WireGuard message types).
6. Multiplexing: work-stealing uplink, merged downlink, reordering characteristics and why WireGuard tolerates them (replay window 8128), the server-side group hub and optional resequencer.
7. Failure handling table (copy from the spec section 8).
8. Security notes: what each layer protects, the static-key wrapper having no forward secrecy, VK sees relay metadata (participant count, bytes), ban footprint.
9. Compatibility matrix: our mode name vs server implementation and flags (anton48 `-srtp`; WDTT server for `wrap`; cacggghp for `dtls`; FreeTurn/samosvalishe/anton48 `-wrap-srtp` not yet).

- [ ] **Step 2: Commit**

```bash
git add docs/protocol.md && git commit -m "docs: VK-TURN wire protocol note"
```

---

### Task 16: sing-box RFC issue draft

**Files:**
- Create: `docs/rfc-sing-box-issue.md`

**Interfaces:**
- Produces: the text to post as a GitHub issue in SagerNet/sing-box after the owner reviews it. Not posted by the implementer.

- [ ] **Step 1: Write the draft with this structure (English, no em-dashes)**

Title: `New outbound: turnrelay (tunnel through WebRTC TURN relays, e.g. VK Calls, for censorship circumvention)`

Sections:
1. Summary: two sentences.
2. Motivation: Russian whitelists; VK Calls TURN relays are whitelisted; existing tools are standalone VPN apps without domain routing and collide with the one-VPN limit on iOS; the iOS client maintainer's statement (link anton48/vk-turn-proxy-ios#25).
3. Proposed design: `turnrelay` is a UDP-only outbound with pluggable credential providers (`vk` today, `static` for any relay); L4 comes from the existing `wireguard` endpoint through `detour`; config example from the spec; how scenario (a) and (b) compose; nothing changes in the WireGuard endpoint.
4. Config schema: the option table from spec section 6.
5. Transport details: link to `docs/protocol.md`; modes; measurements (raw DTLS shaped to ~9 KB/s per allocation; SRTP ~200 KB/s per allocation, 30 allocations ~50 Mbit/s; UDP vs TCP relay transport ~66 vs ~21 Mbit/s).
6. Security notes: static wrap key has no forward secrecy; the inner WireGuard session does; VK sees participant count and bytes; ban footprint grows with connections, default 30 and hard max 60.
7. Licensing and provenance: library is GPL-3.0 (compatible with sing-box), derived from GPL projects, no PolyForm code; dependency list (pion/turn, pion/dtls, pion/srtp, tls-client).
8. Ask: (a) in-tree behind a `with_turnrelay` build tag, or (b) keep it as an external module `github.com/romanrublev/turnrelay` that clients register; which do you prefer, and any objections to the outbound shape (UDP-only outbound used as detour)?
9. Status: library and CLI exist with an in-process test suite and a docker interop test; link to the repo.

- [ ] **Step 2: Commit**

```bash
git add docs/rfc-sing-box-issue.md && git commit -m "docs: sing-box RFC issue draft"
```

---

## Self-review notes

- Spec coverage: section 4 (architecture) -> Tasks 10, 11; 5.1 API -> Task 11; 5.2 engine -> Task 10; 5.3 obfs -> Tasks 3-6; 5.4 providers -> Task 8; credpool -> Task 9; relay -> Task 7; CLI -> Task 12; testing section 9 -> unit tests in every task, docker interop Task 13, e2e Task 14; deliverables protocol note and RFC -> Tasks 15, 16. sing-box integration (spec section 6) is milestone 2 and intentionally absent.
- Captcha policy `wait` (spec 5.2): Task 11 accepts the value but Task 9's pool already returns the captcha error immediately during cooldown; `wait` differs only in that the mux worker's backoff naturally retries after the cooldown, which is what both policies do today. Document in Task 11 that `wait` and `fail` currently behave the same at the library level (the difference is surfaced to sing-box logging in M2).
- Type consistency: `credpool.Lease` fields (`Cred`, `Slot`, `Index`) used in Task 10; `relay.Options` fields used in Task 10; `obfs.Options{Password, WrapKey, Video, HandshakeTimeout}` used in Tasks 4-6 and 11; `mux.Options` fields identical in Task 10 and 11; `turntest.Server` methods `Addr/Username/Password/Allocations/SetQuota/Restart` used in Tasks 7, 10, 11.
