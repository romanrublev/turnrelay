# sing-box integration proposal

Draft text for an issue proposing a native `turnrelay` outbound in
SagerNet/sing-box. See [ROADMAP.md](../ROADMAP.md).

Title: `New outbound: turnrelay (tunnel through WebRTC TURN relays, e.g. VK Calls, for censorship circumvention)`

## Summary

We propose a UDP-only outbound, `turnrelay`, that carries datagrams through
WebRTC TURN relays disguised as call media, for the existing `wireguard`
endpoint to use as `detour`. It exists as a Go library
(`github.com/romanrublev/turnrelay`, GPL-3.0); should it live in-tree
behind a build tag or stay external?

## Motivation

Russian networks increasingly enforce whitelists where only a few domestic
services are reachable. VK Calls relies on TURN relays that are inside those
whitelists. Several open source tools already tunnel traffic through them,
but all are standalone VPN apps: no domain or geosite routing, and on iOS
they collide with the one-VPN limit when combined with a routing client such
as Happ. The iOS client maintainer said in anton48/vk-turn-proxy-ios#25 that
the right fix is a transport inside sing-box. This proposal does that.

## Proposed design

`turnrelay` is a UDP-only outbound with pluggable credential providers:
`vk` (anonymous join of a VK call link) and `static` (fixed credentials for
any TURN relay, e.g. self-hosted coturn). It opens N TURN allocations,
obfuscates each as a media stream and presents them as one datagram pipe:
`Write` stripes one datagram over the pool, `Read` returns one merged
datagram. TCP returns "only udp is supported".

L4 comes from the `wireguard` endpoint through `detour`:
`transport/wireguard/client_bind.go` already dials UDP on the detour and
uses the conn as the WireGuard bind, so nothing changes in the endpoint.
Rule-sets decide which connections enter it:

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

Two deployments use the same blocks: (a) exit abroad, VPS outside RU,
`final: wg` as above; (b) RU relay box, VPS inside RU, a `vless` or
`hysteria2` outbound to a foreign server with `detour: "wg"`, so the foreign
tunnel rides inside WireGuard inside the relay. The VPS runs the existing
anton48/vk-turn-proxy server (`-srtp`) in front of WireGuard.

## Config schema

| Option | Type | Meaning |
|---|---|---|
| `provider` | `vk` \| `static` | credential source, default `vk` |
| `call_link` / `call_links` | string(s) | VK call link(s); one credential (anonymous participant) per 10 connections, about 20 connections per link |
| `turn_server` | host:port | `static`: the relay; `vk`: optional override |
| `turn_username`, `turn_password` | string | `static` only |
| `server`, `server_port` | address, port | the VPS running the server |
| `connections` | int | allocations, default 30, max 60 |
| `mode` | `srtp` \| `wrap` \| `dtls` | obfuscation, default `srtp` |
| `password` | string | `wrap` only: HKDF input for the envelope key |
| `udp` | bool | transport to the relay, default true (false selects TCP) |
| `captcha` | `fail` \| `wait` | on a VK captcha, default `fail` |
| `DialerOptions` | | for the sockets towards the VK API and the relay (`bind_interface`, `detour`); the library takes them as one `DialContext` hook (`turnrelay.Config.DialContext`) |

## Transport details

The wire protocol (credential chain, TURN usage, byte layouts, control
frames, multiplexing, failure handling) is in `docs/protocol.md` of the
repository. Modes: `srtp` is real DTLS-SRTP (RFC 5764, RTP payload type
100), the default; `wrap` is the WDTT-WRAP-v1 envelope (RTP header,
ChaCha20-Poly1305, HKDF key from a password) around plain DTLS, for the WDTT
server family; `dtls` is the legacy plain DTLS, deprecated.

Throughput figures are not measured by us. The upstream iOS client author
(anton48) reports that VK relays shape raw DTLS to about 9 KB/s per
allocation and that SRTP gives about 200 KB/s per allocation, scaling
linearly to about 50 Mbit/s with 30 allocations; the UDP versus TCP
comparison (about 66 versus 21 Mbit/s to the relay) comes from reports by
users of the iOS client. What we have verified ourselves is functional only: in a
docker interop test the unmodified anton48 server accepts 4 connections in
one group and a WireGuard tunnel with HTTP through it works.

## Security notes

- The `wrap` envelope uses a static key shared with the server and has no
  forward secrecy; the DTLS session inside it and the WireGuard session inside
  that both do. `srtp` and `dtls` use ephemeral ECDHE.
- WireGuard remains the layer that authenticates both ends.
- VK sees the anonymous participants in the call (one per 10 connections) and
  the bytes relayed per allocation, not destinations.
- The ban footprint grows with the connection count: default 30, hard
  maximum 60. Plaintext modes are not offered.

## Licensing and provenance

The library is GPL-3.0, compatible with sing-box. It derives from GPL-3.0
projects: cacggghp/vk-turn-proxy (credential chain), anton48/vk-turn-proxy-ios
(credential pool, control frames; its `srtpwrap` package is MIT) and
amurcanov/proxy-turn-vk-android (WRAP-v1). No PolyForm Noncommercial code
(amurcanov/csqtt) is used. Dependencies: pion/turn, pion/dtls, pion/srtp,
pion/rtp, bogdanfinn/tls-client (Chrome TLS fingerprint for the VK API),
github.com/sagernet/sing (the `N.Dialer` interface and `M.Socksaddr`),
golang.org/x/crypto.

## Ask

Either outcome works for us:

(a) in-tree: `protocol/turnrelay`, `option/turnrelay.go` and
`include/turnrelay.go` behind a `with_turnrelay` build tag, like
`with_wireguard`;

(b) external: keep the module `github.com/romanrublev/turnrelay` for
graphical clients to register themselves; this needs no patch to sing-box.
The library already implements sagernet/sing's `N.Dialer` (`DialContext`
and `ListenPacket`), so a client can hand a `turnrelay.Dialer` to the
`wireguard` endpoint as its detour today.

Which do you prefer, and are there objections to the outbound shape (a
UDP-only outbound used as the detour of the `wireguard` endpoint)?

## Status

Library and `turnrelay-udp` CLI exist, with an in-process test suite
(nothing touches VK) and a docker interop test against the unmodified
upstream server. The sing-box outbound also exists now, as the external
`singbox/` module described above (option (b)): it registers a `turnrelay`
outbound against the standard sing-box registries with one `RegisterOutbound`
call, no patch to sing-box required. Option (a), folding it in-tree behind a
`with_turnrelay` build tag, is the open question for maintainers. Repository:
https://github.com/romanrublev/turnrelay
