# turnrelay: VK-TURN transport for sing-box (design)

Date: 2026-09-14
Status: approved for implementation (milestone 1)

## 1. Problem

Russian networks increasingly enforce whitelists where only a few domestic services are reachable. VK Calls (WebRTC) relies on TURN relay servers that are inside those whitelists and are, in practice, unblockable. A family of open-source tools tunnels arbitrary traffic through those relays disguised as call media.

Every existing client is a standalone VPN app (WireGuard or a local socket hack). None of them can do domain/geosite routing, and on iOS they collide with the one-VPN limit when combined with a routing client such as Happ (sing-box based). The maintainer of the iOS client stated in anton48/vk-turn-proxy-ios#25 that the right solution is to make VK-TURN a transport inside sing-box / Happ.

This project does exactly that.

## 2. Goal

A sing-box user writes a config where selected traffic (chosen by rule-sets) egresses through VK TURN relays and the rest goes direct, with one process and one VPN session:

```json
{
  "outbounds": [
    { "type": "turnrelay", "tag": "relay",
      "provider": "vk", "call_link": "https://vk.ru/call/join/<hash>",
      "server": "203.0.113.5", "server_port": 56004,
      "connections": 30, "mode": "srtp", "udp": true },
    { "type": "direct", "tag": "direct" }
  ],
  "endpoints": [
    { "type": "wireguard", "tag": "wg", "detour": "relay",
      "address": ["10.8.0.2/32"], "private_key": "<client wg key>",
      "peers": [{ "address": "203.0.113.5", "port": 56004,
                  "public_key": "<server wg key>",
                  "allowed_ips": ["0.0.0.0/0", "::/0"] }] }
  ],
  "route": {
    "rules": [
      { "rule_set": "geosite-ru", "outbound": "direct" },
      { "rule_set": "geoip-ru",   "outbound": "direct" }
    ],
    "final": "wg"
  }
}
```

Two deployment scenarios must work with the same building blocks:

- (a) Exit abroad: the VPS is outside RU, `final: wg`.
- (b) RU relay box: the VPS is inside RU; a `vless`/`hysteria2` outbound to a foreign server uses `detour: "wg"`, so the foreign tunnel rides inside WireGuard inside VK-TURN.

## 3. Key findings that shaped the design

1. cacggghp/vk-turn-proxy (the GPL Go core named as the base) is DTLS-only and last touched April 2026. VK relays now shape raw DTLS to ~9 KB/s per allocation (measured by anton48, `pkg/proxy/proxy.go`). The `dtls` mode is kept only as a deprecated legacy option.
2. Two obfuscation modes are alive on VK relays and have GPL-compatible Go implementations:
   - `srtp`: real DTLS-SRTP (RFC 5764, pion/srtp, RTP payload type 100), from anton48/vk-turn-proxy-ios `pkg/proxy/srtpwrap` (MIT) and its server counterpart anton48/vk-turn-proxy branch `add-server-srtp-layer` (GPL-3.0). ~200 KB/s per allocation, linear scaling, 30 allocations give ~50 Mbit/s.
   - `wrap`: WDTT-WRAP-v1 from amurcanov/proxy-turn-vk-android (GPL-3.0): RTP header (PT 111, Opus) plus ChaCha20-Poly1305 with an HKDF key from a shared password, wrapped around plain DTLS. Used by the WDTT server family. FreeTurn (samosvalishe, profiles rtpopus/2/3) and anton48's `-wrap-srtp` (explicit 12-byte nonce, raw hex key) are different envelopes and are deferred to M3.
3. CSQTT (amurcanov/csqtt) is not "another wrapper": it is a different L3 (CQF1 framing, striping, FEC, server-side TUN) that replaces WireGuard. Its only server is Rust under PolyForm Noncommercial. A Go client exists under MIT (`pkg/csqtt` in the iOS repo). It is deferred to a later milestone with its own design (netstack inside the outbound).
4. sing-box's `wireguard` endpoint accepts `detour`. With a detour, `transport/wireguard/client_bind.go` calls `dialer.DialContext("udp", peerAddr)` on the detour outbound and uses the returned conn as the WireGuard bind. Therefore any outbound that implements `N.Dialer` for UDP can carry WireGuard, and sing-box's own gVisor netstack turns it into TCP/UDP streams. No change to the WireGuard endpoint is required.
5. VK quota: 10 allocations per TURN credential, excess answered with TURN error 486. One credential equals one anonymous "participant" in the call. 30 connections therefore mean 3 participants, not 30.
6. anton48's SRTP server (release `srtp-build306`) already groups a client's N connections by a session UUID sent in an in-band hello, uses one socket towards WireGuard and schedules the downlink across connections with work stealing. This is the M1 server target; no new server is needed.

### Naming

The transport is not VK-specific: it is a tunnel through any TURN relay whose traffic looks like WebRTC media. VK Calls is one *credential provider*; OK Calls, Yandex Telemost (historically) and self-hosted coturn are others. Names therefore follow the mechanism, not the carrier: outbound type `turnrelay`, module `github.com/romanrublev/turnrelay`, providers under `provider/` (`vk`, `static`; later `ok`, `telemost`). Only prose that describes the VK ecosystem says "VK".

## 4. Architecture (scheme A)

```
sing-box (Happ)                                     VPS
+------------------------------+                    +------------------------------+
| route rules (geosite/geoip)  |                    | anton48 server -srtp :56004  |
|   ru -> direct               |                    |   group by session UUID      |
|   final -> "wg"              |   VK TURN relay    |   one socket -> wg :51820    |
| endpoint wireguard "wg"      |   (whitelisted)    | WireGuard (wg-quick) -> NAT  |
|   detour: "relay" -----------+--> N allocations --+->                            |
| outbound turnrelay "relay"   |   ChannelData/UDP  |                              |
|   creds <- call_link         |                    |                              |
+------------------------------+                    +------------------------------+
```

Layering, bottom up, for one allocation:

```
UDP socket -> TURN ChannelData (pion/turn, UDP transport by default)
  -> obfuscation layer (srtp | wrap | dtls)
    -> control frames (hello, probe) and WireGuard datagrams
```

The outbound owns N such allocations and presents them as one datagram pipe. Uplink datagrams are pulled from a single queue by whichever worker is free (work stealing); downlink datagrams from all workers are merged into one receive queue. Order is not preserved across workers; WireGuard tolerates it (replay window 8128) and the server can optionally resequence.

WireGuard's role is exactly what the ecosystem already uses it for: an internal transport that multiplexes N allocations into one L3 pipe. Routing is not done by AllowedIPs; sing-box rule-sets decide which connections enter the `wg` endpoint at all.

## 5. Module `github.com/romanrublev/turnrelay`

License GPL-3.0. Provenance is recorded in `NOTICE`: cacggghp/vk-turn-proxy (credential chain), anton48/vk-turn-proxy-ios (credential pool, hello/probe control plane, srtpwrap which is MIT), amurcanov/proxy-turn-vk-android (WRAP-v1). Code is extracted into package form, not copied file by file; the iOS `proxy.go` (5.5k lines with iOS-specific socket stats, speed test and path-restart logic) is not suitable for import into sing-box.

```
(root) turnrelay   public API: Config, Dialer (Start, Close, DialContext, ListenPacket, Stats)
provider/          Credential, CaptchaRequiredError: what every credential source returns
provider/vk/       VK Calls anonymous-join chain (login.vk.ru -> api.vk.ru -> calls.okcdn.ru),
                   utls Chrome profile, captcha detection
provider/static/   fixed username/password for a self-hosted or third-party relay
credpool/          slots of 10 connections per credential, 3-6 s cooldown between fetches,
                   486 -> saturated, TTL 10 min minus safety margin
relay/             pion/turn allocation (UDP default, TCP fallback), STUN Binding keepalive
                   every 10 s, 486/401 classification
obfs/              Wrapper interface { Client(ctx, net.PacketConn, peer) (net.Conn, error) }
                   implementations: srtp, wrap, dtls
mux/               N workers, shared uplink queue with work stealing, merged downlink,
                   group hello and probe-echo control frames, zombie detection
cmd/turnrelay-udp/ CLI: listens on 127.0.0.1:9000 and forwards datagrams into the pool;
                   used for e2e with plain wg-quick and no sing-box
docs/protocol.md   wire protocol note (milestone 1 deliverable)
```

### 5.1 Public API

```go
type Config struct {
    Provider     string        // "vk" (default) or "static"
    CallLinks    []string      // vk: one or more https://vk.ru/call/join/<hash>
    TURNServer   string        // static: relay host:port; vk: optional override of the relay VK returns
    TURNUsername string        // static only
    TURNPassword string        // static only
    Server       netip.AddrPort
    Connections  int           // default 30, max 60
    Mode         Mode          // ModeSRTP (default), ModeWrap, ModeDTLS
    Password     string        // ModeWrap only: HKDF input for the wrap key
    TURNUDP      bool          // default true; false selects TCP to the relay
    Captcha      CaptchaPolicy // Fail (default) or Wait
    Logger       Logger
}

type Dialer struct{ ... }

func New(cfg Config) (*Dialer, error)
func (d *Dialer) Start(ctx context.Context) error   // starts credential pool and workers
func (d *Dialer) Close() error
func (d *Dialer) DialContext(ctx context.Context, network string, dest M.Socksaddr) (net.Conn, error)
func (d *Dialer) ListenPacket(ctx context.Context, dest M.Socksaddr) (net.PacketConn, error)
func (d *Dialer) Stats() Stats
```

`DialContext` accepts only `"udp"`. The returned conn is a datagram conn: each `Write` becomes one payload datagram striped over the pool, each `Read` returns one merged downlink datagram. `dest` is ignored for delivery (the relay always forwards to `Server`); if it differs from `Server` a warning is logged once. Multiple concurrent conns share the pool; downlink datagrams are delivered to the most recently written conn (mirrors what the WireGuard endpoint needs; it holds exactly one bind).

### 5.2 Connection engine (`mux`)

- On `Start`, the credential pool fetches the first credential and workers start with 100 ms pacing; a worker needs a credential slot with free quota, then allocates, then runs the obfuscation handshake (3 handshakes in flight at most), then sends the group hello and joins the pool.
- Uplink: one bounded channel (256 datagrams). Sends block on a full channel (back-pressure), never drop.
- Downlink: each worker reads its obfs conn and pushes into one channel (2048 datagrams); reads block, never drop.
- Control: hello `ff 'G' 'R' 'P' + 16-byte session UUID` sent right after handshake and repeated with every probe; probe `ff 'P' 'N' 'G' + 8-byte big-endian sequence` every 30 s per worker, echoed by the server; a worker with no inbound for 120 s is killed and restarted. Both frames start with 0xff, outside WireGuard's message-type range, so an older server just drops them.
- Worker failure: exponential backoff 2 s to 60 s with jitter; 486 marks the credential slot saturated so the next attempt picks another slot; 401/stale nonce invalidates the slot.
- Captcha: the credential fetch returns `CaptchaRequiredError`; with `Captcha: Fail` the slot cools down for 60 s and the error is surfaced through `Stats` and the logger; with `Wait` a global lockout is honoured. No solver is built into the library (GUI clients may add a callback later).

### 5.3 Obfuscation layer (`obfs`)

- `srtp`: pion/dtls client with `use_srtp` (profile `SRTP_AES128_CM_HMAC_SHA1_80`), self-signed certificate, `InsecureSkipVerify`, `ExtendedMasterSecret: Require`. After the handshake every `Write` becomes one RTP packet (version 2, PT 100, monotonically increasing sequence, timestamp advanced per packet, random SSRC) protected with SRTP keys derived per RFC 5764; `Read` demultiplexes by first byte (20..63 DTLS, 128..191 RTP) and unprotects.
- `wrap`: key = HKDF-SHA256(secret=password, salt="WDTT-WRAP-v1", info="rtp-obfs/chacha20poly1305", 32 bytes). Packet = 12-byte RTP header (V=2, P=1, PT=111, seq, ts, ssrc) + ChaCha20-Poly1305(nonce = ssrc || seq || 0x0000 || ts, aad = header) + 1..24 random padding bytes + padding-length byte. Inside runs plain DTLS 1.2 as in legacy mode. The static key gives no forward secrecy for the wrapper itself; the inner WireGuard session has its own. Documented in the RFC.
- `dtls`: pion/dtls with `TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256`, connection IDs, no wrapper. Deprecated.

All three implement the same `Wrapper` interface so the mux is mode-agnostic.

### 5.4 Credentials (`creds`)

Five HTTP hops, all from a Chrome-fingerprinted utls client with the VK web app ids from cacggghp:

1. `POST https://login.vk.ru/?act=get_anonym_token` -> `access_token` (token1)
2. `POST https://api.vk.ru/method/calls.getCallPreview` (best effort)
3. `POST https://api.vk.ru/method/calls.getAnonymousToken` with `vk_join_link` and a random display name -> `token` (token2); VK Smart Captcha appears here
4. `POST https://calls.okcdn.ru/fb.do method=auth.anonymLogin` -> `session_key` (token3)
5. `POST https://calls.okcdn.ru/fb.do method=vchat.joinConversationByLink` with `anonymToken=token2`, `session_key=token3` -> `turn_server{username, credential, urls[]}`

Credential lifetime is treated as 10 minutes minus a 60 s margin. A global mutex plus a 3-6 s random cooldown serialise fetches. DNS for these hosts is resolved through public resolvers (77.88.8.8, 8.8.8.8, 1.1.1.1) because system DNS is often the first thing a whitelist breaks.

## 6. sing-box integration (milestone 2)

Fork SagerNet/sing-box, add `protocol/turnrelay/outbound.go`, `option/turnrelay.go`, `include/turnrelay.go` behind build tag `with_turnrelay` (same pattern as `with_wireguard`), and docs. The outbound:

- `Type() = "turnrelay"`, `Network() = [udp]`, embeds `outbound.Adapter`.
- Starts the dialer in `Start(StartStateStart)`, closes in `Close`.
- `DialContext`/`ListenPacket` delegate to the dialer; TCP returns `E.New("turnrelay: only udp is supported")`.
- Options: `provider` (`vk` | `static`), `call_link` / `call_links` (vk), `turn_server`, `turn_username`, `turn_password` (static, or `turn_server` alone as an override for vk), `server`, `server_port`, `connections`, `mode`, `password`, `udp`, `captcha`, plus `DialerOptions` used for the outbound sockets towards VK API and the relay (so `bind_interface` and `detour` work for those too).

The RFC issue proposes two acceptable outcomes: in-tree behind the build tag, or an external module that graphical clients register themselves. The code is structured so that the second works without any patch to sing-box.

## 7. Server

Milestone 1 uses anton48/vk-turn-proxy `srtp-build306` unchanged (`-srtp`) with wg-quick behind it. The `wrap` mode targets the WDTT server (amurcanov/proxy-turn-vk-android linux-server); WireGuard keys for WDTT are obtained once via a `getconf` helper (WDTT issues them per device id and password). A proxy-exit server that dials arbitrary destinations is a later milestone, not part of this design.

## 8. Error handling summary

| Condition | Behaviour |
|---|---|
| Credential fetch fails (network) | slot stays empty, retry with backoff, workers wait |
| Captcha required | `CaptchaRequiredError`, slot cooldown 60 s, surfaced in Stats and log |
| TURN 486 quota | slot marked saturated, worker retries with another slot |
| TURN 401 / stale nonce | slot invalidated, re-fetch |
| Handshake timeout | worker restart with backoff; in wrap mode reported as "password or wrap key not accepted" |
| No inbound 120 s | worker killed and restarted |
| Uplink queue full | writer blocks (back-pressure), no drops |
| All workers down | `DialContext` still succeeds; writes block until a worker returns or ctx ends |

## 9. Testing

- Unit: wrap/unwrap known-answer vectors (shared with the Android implementation), hello and probe parsers, credential pool state machine (quota, TTL, cooldown, 486, 401), work-stealing striper, obfs demux by first byte.
- Integration without VK (runs in CI): docker compose with coturn (static long-term credential), anton48 `-srtp` server and wireguard-go; `turnrelay-udp` with `-turn` override and an injected static credential; iperf3 through the WireGuard tunnel must pass and the server log must show one group with N connections.
- End-to-end on the user's VPS with a real VK call link: `curl --interface` through sing-box with `final: wg` returns the VPS address, a RU domain resolves and connects direct, iperf3 over 30 connections sustains at least 30 Mbit/s downlink.

## 10. Milestones

- M1: `docs/protocol.md`, RFC issue in SagerNet/sing-box, `turnrelay` library and `turnrelay-udp` CLI passing the integration and e2e tests above.
- M2: sing-box fork with the `turnrelay` outbound; the config in section 2 works end to end on macOS and Linux.
- M3: `wrap` e2e against WDTT, FreeTurn and anton48 `-wrap-srtp` envelopes, credential disk cache, captcha callback hook; Xray dialer if maintainers are receptive.
- M4: CSQTT mode (netstack inside the outbound) and/or a proxy-exit server.

## 11. Non-goals

- No plaintext or no-DTLS mode: it gets accounts banned and is not offered.
- No TCP stream API in the outbound (that is the deferred proxy-exit design).
- No captcha solver inside the library.
- No copying from PolyForm-Noncommercial sources (amurcanov/csqtt).
