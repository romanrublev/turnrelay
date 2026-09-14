# Roadmap

[English](ROADMAP.md) | [Русский](ROADMAP.ru.md)


`turnrelay` is in beta. This is where it is and where it is going. Order and
scope may change.

## Done

**Core transport (library + CLI).**

- `turnrelay` Go library: N TURN allocations presented as one UDP dialer
  (`DialContext`/`ListenPacket`), usable as a sing-box `N.Dialer`.
- Obfuscation modes: `srtp` (DTLS-SRTP, the default), `wrap` (WDTT-style RTP
  envelope), `dtls` (legacy).
- Credential providers: `vk` (anonymous join of a VK call link, with the
  captcha solved automatically) and `static` (fixed credentials for any relay,
  e.g. self-hosted coturn).
- Quota-aware credential pool: about 18 allocations per credential, automatic
  re-fetch, cooldowns, work-stealing uplink with no datagram loss.
- `turnrelay-udp` CLI: a local UDP bridge that a WireGuard client points at,
  so the transport works with sing-box (or any WireGuard client) today.
- Server side: works with the unmodified upstream relay-side server; a docker
  interop test and a VPS setup script are included.

## Next

**Native sing-box outbound (the main goal).**

Add a `turnrelay` outbound type to sing-box so the config is a single block
and no separate `turnrelay-udp` process is needed:

```json
{ "type": "turnrelay", "tag": "relay", "provider": "vk",
  "call_link": "https://vk.ru/call/join/<hash>",
  "server": "<vps-ip>", "server_port": 56004,
  "connections": 18, "mode": "srtp" }
```

The `wireguard` endpoint then uses it via `detour`. This is what makes
turnrelay usable inside sing-box-based clients (including on iOS, where the
one-VPN limit rules out a second app). An RFC for upstream is drafted in
[docs/sing-box-rfc.md](docs/sing-box-rfc.md); the outbound also works as an
external module for clients that register it themselves.

## Later

- **More obfuscation interop.** Verified interop for `wrap` against the WDTT
  and FreeTurn server families; a CSQTT (RTP/VKQUIC) mode.
- **More credential carriers.** The transport is not VK-specific. Other
  WebRTC call services with whitelisted relays can be added as providers.
- **Xray transport.** The same dialer for Xray-core, if maintainers are
  receptive.
- **Proxy-exit server mode.** A server that forwards arbitrary dialed
  destinations, so WireGuard is not required in every deployment.

## Non-goals

- No plaintext / no-obfuscation mode - it gets accounts banned.
- No captcha-solving service or account farming.
