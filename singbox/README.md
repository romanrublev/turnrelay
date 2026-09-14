# turnrelay sing-box outbound

This Go module registers a native `turnrelay` outbound in
[sing-box](https://github.com/SagerNet/sing-box), so a routing client
configures the transport in one JSON block, with no separate `turnrelay-udp`
process. It is a separate module: the core `github.com/romanrublev/turnrelay`
library stays free of the sing-box dependency tree.

## Config

```json
{
  "outbounds": [
    { "type": "turnrelay", "tag": "relay",
      "provider": "vk", "call_link": "https://vk.ru/call/join/<hash>",
      "server": "203.0.113.5", "server_port": 56004,
      "connections": 30, "mode": "srtp" }
  ],
  "endpoints": [
    { "type": "wireguard", "tag": "wg", "detour": "relay",
      "address": ["10.8.0.2/32"], "private_key": "<client wg key>",
      "peers": [{ "address": "203.0.113.5", "port": 56004,
                  "public_key": "<server wg key>",
                  "allowed_ips": ["0.0.0.0/0", "::/0"] }] }
  ],
  "route": { "rules": [
      { "rule_set": "geosite-ru", "outbound": "direct" },
      { "rule_set": "geoip-ru",   "outbound": "direct" } ],
    "final": "wg" }
}
```

Options: `provider` (`vk` default, or `static`), `call_link` / `call_links`,
`turn_server` / `turn_username` / `turn_password` (`static`),
`server` / `server_port` (the VPS running the relay-side server, an IP),
`connections` (default 30, max 60), `mode` (`srtp` default, `wrap`, `dtls`),
`password` (`wrap`, and required for `server_type: "exit"`), `server_type`
(`wireguard` default, or `exit`), `captcha` (`auto` default, `fail`, `wait`),
`udp` (default true). `detour` and `bind_interface` on the outbound apply to
the sockets towards the VK API and the relay.

## Exit mode (no WireGuard)

With a `turnrelay-server` on the VPS (see `docs/server-setup.md`), set
`server_type` to `exit`: the outbound becomes a normal TCP+UDP proxy outbound
and no `wireguard` endpoint is needed.

```json
{
  "outbounds": [
    { "type": "turnrelay", "tag": "relay",
      "server_type": "exit", "password": "<pre-shared password>",
      "provider": "vk", "call_link": "https://vk.ru/call/join/<hash>",
      "server": "203.0.113.5", "server_port": 56004,
      "connections": 30, "mode": "srtp" },
    { "type": "direct", "tag": "direct" }
  ],
  "route": { "rules": [
      { "rule_set": "geosite-ru", "outbound": "direct" },
      { "rule_set": "geoip-ru",   "outbound": "direct" } ],
    "final": "relay" }
}
```

`server_type` defaults to `wireguard` (the detour setup above). In `exit`
mode `password` is required.

## Building a custom sing-box

sing-box registers outbounds at build time. Build a small binary that adds
turnrelay to the standard registry:

```go
package main

import (
	"context"
	"log"

	"github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/include"

	turnrelaybox "github.com/romanrublev/turnrelay/singbox"
)

func main() {
	outboundRegistry := include.OutboundRegistry()
	turnrelaybox.RegisterOutbound(outboundRegistry)
	ctx := box.Context(context.Background(),
		include.InboundRegistry(), outboundRegistry, include.EndpointRegistry(),
		include.DNSTransportRegistry(), include.ServiceRegistry(),
		include.CertificateProviderRegistry())
	// Feed ctx to box.New and run it as the sing-box CLI does; see the
	// sing-box cmd/sing-box sources for the full main.
	instance, err := box.New(box.Options{Context: ctx, Options: parsedOptions})
	if err != nil {
		log.Fatal(err)
	}
	log.Fatal(instance.Start())
}
```

GUI clients that already build sing-box (registering their own outbounds) add
the same one `RegisterOutbound` line to their registry.

## Version

Built against sing-box `v1.14.0`. To use a different sing-box version, bump it
in this module's `go.mod`; the outbound registry API is stable across
`v1.11`..`v1.14`.
