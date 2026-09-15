# turnrelay

[English](README.md) | [Русский](README.ru.md)


Tunnel arbitrary UDP traffic through WebRTC TURN relays, disguised as call
media, and route it with sing-box.

`turnrelay` is a Go library and CLI that opens a set of TURN allocations on a
relay (VK Calls by default, or any relay you have credentials for), wraps
each one to look like an encrypted WebRTC media stream, and presents them as
a single UDP pipe. sing-box consumes it as a native `turnrelay` outbound and
does the actual routing on top, so you get real domain/geosite/geoip rules
over a transport that survives networks where only a few domestic services
are reachable. It plugs into sing-box either behind a `wireguard` endpoint or,
with the `turnrelay-server` exit mode, as a plain TCP+UDP outbound with no
WireGuard at all.

> **Status: beta.** The library, both CLIs (`turnrelay-udp`, `turnrelay-proxy`),
> the VK Calls provider, the native sing-box `turnrelay` outbound (the
> [`singbox/`](singbox/) module) and the `turnrelay-server` proxy-exit mode all
> work end to end. WireGuard is optional: use the native outbound behind a
> `wireguard` endpoint, or run `turnrelay-server` and drop WireGuard entirely.
> See the [Roadmap](ROADMAP.md).

> **For research and educational use.** Tunnelling traffic through a call
> service is against its terms of use and can get the account that creates
> the call link limited or banned. Use links from throwaway accounts, keep
> the connection count modest, and understand the risk before you rely on
> this.

## Why

Some networks (mobile carriers, corporate Wi-Fi, whole-country whitelists)
only let a handful of domestic services through. Video-call services such as
VK Calls have to keep working there, so their TURN relays sit inside the
whitelist and cannot easily be blocked. Tunnelling through those relays,
shaped to look like call media, rides along.

Existing tools that do this are standalone VPN apps: they route by IP only,
have no domain/geosite rules, and on iOS they collide with the one-VPN limit
when you also run a routing client. `turnrelay` is built as a transport that
a real routing engine (sing-box) consumes, so the routing engine keeps its
rules and there is no VPN-over-VPN.

## How it works

```
your traffic ─▶ sing-box (routing rules)
                   └─ wireguard endpoint ──detour──▶ turnrelay
                                                        │  N TURN allocations,
                                                        │  each obfuscated as media
                                                        ▼
                              TURN relay (whitelisted) ──▶ your VPS ──▶ internet
```

- **turnrelay** opens N parallel TURN allocations to the relay over UDP. Their
  bandwidth is cumulative. Each allocation is wrapped in an obfuscation layer
  (DTLS-SRTP by default) so the relay's classifier sees WebRTC media.
- **WireGuard** (sing-box's built-in userspace endpoint) runs over the pipe as
  the L4 transport. It is *not* used for routing or as the obfuscation - just
  to multiplex the allocations into one ordered stream.
- **sing-box** applies your routing rules and decides what enters the tunnel.

That is the WireGuard deployment. You can also skip the `turnrelay-udp` bridge
and register the native `turnrelay` outbound directly in sing-box (the
[`singbox/`](singbox/) module), and skip WireGuard entirely with the
`turnrelay-server` exit mode (`"server_type": "exit"`), where the server dials
destinations itself. See [singbox/README.md](singbox/README.md) and
[docs/server-setup.md](docs/server-setup.md#exit-server-no-wireguard).

Two deployments, same building blocks:

- **Exit abroad:** the VPS is outside the restricted network; turnrelay is the
  exit.
- **Detour:** the VPS is inside the restricted network and forwards into
  another tunnel (VLESS, Hysteria2, ...) that leaves it, so the foreign tunnel
  rides over turnrelay.

## Install

Requires Go 1.27+.

```bash
go install github.com/romanrublev/turnrelay/cmd/turnrelay-udp@latest
```

or from a clone:

```bash
git clone https://github.com/romanrublev/turnrelay
cd turnrelay
make build        # produces bin/turnrelay-udp
```

## Quick start

You need a VPS (the exit) and, for the VK provider, a VK call link.

**1. Set up the server.** On a fresh Debian/Ubuntu VPS, as root:

```bash
scp scripts/vps-setup.sh root@<vps-ip>:/root/
ssh root@<vps-ip> './vps-setup.sh <vps-ip>'
```

This installs WireGuard and the relay-side server, prints the WireGuard keys
and the address to point the client at. Full walk-through, including the
client WireGuard config: [docs/server-setup.md](docs/server-setup.md).

**2. Run the client bridge.** On your machine:

```bash
turnrelay-udp \
  -provider vk \
  -link https://vk.ru/call/join/<hash> \
  -server <vps-ip>:56004 \
  -n 18
```

Wait for `worker 0 up`. It listens on `127.0.0.1:9000` and presents the pipe
as a plain UDP socket that a WireGuard client points its `Endpoint` at.

**3. Route with sing-box.** Give sing-box a `wireguard` endpoint whose peer is
`127.0.0.1:9000` and let its rules do the routing. A minimal config and the
key material are in [docs/server-setup.md](docs/server-setup.md).

**No WireGuard?** Run `turnrelay-server` on the VPS instead of the relay-side
server: it terminates the transport and dials destinations itself, password
authenticated. sing-box then sets `"server_type": "exit"` on the `turnrelay`
outbound and needs no `wireguard` endpoint; without sing-box, `turnrelay-proxy`
is a local SOCKS5 front for any other client. See
[docs/server-setup.md](docs/server-setup.md#exit-server-no-wireguard).

## Configuration

`turnrelay-udp` flags:

| flag | meaning |
|---|---|
| `-provider` | `vk` (VK call link) or `static` (fixed relay credentials) |
| `-link` | VK call link; repeat for more (provider `vk`) |
| `-turn`, `-turn-user`, `-turn-pass` | relay `host:port` and credentials (provider `static`) |
| `-server` | your VPS `host:port` running the relay-side server |
| `-n` | number of TURN allocations (default 30, about 18 per credential) |
| `-mode` | obfuscation: `srtp` (default), `wrap`, `dtls` |
| `-tcp` | reach the relay over TCP instead of UDP (slower, more robust) |
| `-listen` | local UDP socket (default `127.0.0.1:9000`) |
| `-stats` | stats log interval |

Notes:

- **UDP to the relay is the default and the big performance lever.** TCP causes
  TCP-over-TCP meltdown; only fall back to it if UDP is blocked.
- **Connections are cumulative bandwidth but also the ban vector.** Each
  allocation appears as a participant in the call. Keep `-n` modest; the max
  is 60.
- **VK captcha is solved automatically** and credentials are cached; you do not
  normally need to touch a browser.

## Documentation

- [singbox/README.md](singbox/README.md) - the native sing-box `turnrelay` outbound and exit mode.
- [docs/protocol.md](docs/protocol.md) - the wire protocol, hop by hop.
- [docs/server-setup.md](docs/server-setup.md) - server and client setup.
- [ROADMAP.md](ROADMAP.md) - what is done and what is planned.

## Credits

Built on the work of the VK-TURN community, reusing ideas and, where noted in
file headers, code from:

- [cacggghp/vk-turn-proxy](https://github.com/cacggghp/vk-turn-proxy) (GPL-3.0)
- [anton48/vk-turn-proxy-ios](https://github.com/anton48/vk-turn-proxy-ios) (GPL-3.0)
- [amurcanov/proxy-turn-vk-android](https://github.com/amurcanov/proxy-turn-vk-android) (GPL-3.0)

See [NOTICE](NOTICE) for details.

## License

GPL-3.0. See [LICENSE](LICENSE).
