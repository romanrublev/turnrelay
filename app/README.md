# turnrelay

A native VPN client (macOS first) that tunnels traffic over a TURN relay
transport to a VPS, so the VPS's IP is your visible egress.

## Build

```
./build.sh
```

This runs `go build -tags "with_wireguard,with_gvisor,with_quic" -o bin/turnrelay .`
and produces `bin/turnrelay`.

## Install

```
sudo ./bin/turnrelay install
```

This writes a launchd daemon (`/Library/LaunchDaemons/xyz.rublev.turnrelayd.plist`)
that runs `turnrelay daemon` at boot, and records your uid as the control-socket
owner so your unprivileged CLI commands can talk to the privileged daemon. Run
`sudo ./bin/turnrelay uninstall` to remove it.

## Configure

Write a profile to:

```
~/Library/Application Support/turnrelay/profile.json
```

with permissions `0600`:

```json
{
  "link": "...",
  "server": "vps.example.com:443",
  "connections": 18,
  "mode": "srtp",
  "wg_private_key": "...",
  "wg_peer_public_key": "...",
  "wg_address": "10.8.0.2/32"
}
```

Fields:
- `link` - the TURN relay connection link/credential for the provider (VK).
- `server` - the VPS address the relay connects through.
- `connections` - number of relay connections/workers to use.
- `mode` - transport mode (e.g. `srtp`).
- `wg_private_key` - this client's WireGuard private key.
- `wg_peer_public_key` - the VPS-side WireGuard peer public key.
- `wg_address` - this client's WireGuard tunnel address (CIDR).

## Use

```
sudo ./bin/turnrelay install    # once
./bin/turnrelay up
./bin/turnrelay status          # expect: connected ... egress=<VPS ip>
./bin/turnrelay down
```

`turnrelay status` reports `egress=<ip>`; after `up` succeeds this should be
the VPS's IP, not your local ISP's.

## Critical first-run check

This is the top risk in the design and must be verified manually on first run
of a real build against a real VPS:

1. `./bin/turnrelay up`
2. `./bin/turnrelay status` - confirm it reports the VPS IP as egress.
3. `curl -4 https://api.ipify.org` (no proxy set) - this must return the VPS
   IP, confirming default IPv4 traffic is actually routed through the tunnel.
4. Watch the daemon: it must stay healthy and the connection must not loop -
   i.e. the outbound TURN/relay sockets themselves must not get pulled back
   into the tun interface, which would cut off or saturate the relay
   connection.

If step 4 shows a routing loop (the daemon's own outbound sockets getting
routed back through the tunnel), **stop and note it** - do not work around it
silently. The configured fallback is an explicit socket-mark exclusion for
the daemon's outbound connections (excluding marked traffic from the tun
route), in addition to relying on `auto_detect_interface` alone.

5. `./bin/turnrelay down` - confirm it cleans up and egress returns to normal.

## Notes

This checklist is macOS-only for this plan and is not wired into CI: it
needs root, real network access, and a real tun interface, none of which are
available in CI.
