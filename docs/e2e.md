# End-to-end run against a real VK call

This is the operator runbook for the milestone 1 acceptance test in
`docs/superpowers/specs/2026-09-14-turnrelay-design.md` section 9: a real VK
call link, a VPS running the anton48 SRTP server, and a WireGuard tunnel
carried over `turnrelay-udp`. Everything up to here (library, CLI, docker
interop against the unmodified anton48 server) is already verified in
`test/integration/`; this document is the first run that touches actual VK
infrastructure.

You need:

- a fresh Debian or Ubuntu VPS, outside Russia, with a public IPv4 address;
- a VK call link (`https://vk.ru/call/join/<hash>` or similar) that stays
  open for the duration of the test, joined from a second device or browser
  tab so the call has at least one other participant;
- `wireguard-tools` and `iperf3` on the operator laptop;
- the `turnrelay-udp` binary built from this repo (`go build ./cmd/turnrelay-udp`).

## 1. Server setup

Copy `scripts/vps-setup.sh` to the VPS and run it as root:

```
scp scripts/vps-setup.sh root@<vps-ip>:/root/
ssh root@<vps-ip> './vps-setup.sh <vps-ip>'
```

Optional second and third arguments override the WireGuard port (default
51820) and the proxy port (default 56004):

```
./vps-setup.sh <vps-ip> 51820 56004
```

The script is idempotent: re-running it does not regenerate the WireGuard
keys, does not re-download Go or re-clone `anton48/vk-turn-proxy` if already
present, and never issues `iptables` commands itself (the MASQUERADE rule
lives in `wg0.conf`'s `PostUp`/`PostDown`, applied once per `wg-quick`
up/down by `wg-quick` itself).

It installs Go from the official go.dev tarball rather than
`apt-get install golang-go`, because the anton48 server's `go.mod` asks for a
newer Go than Debian/Ubuntu ship. It clones `anton48/vk-turn-proxy` at a
pinned commit (recorded as `VKPROXY_COMMIT` near the top of the script,
looked up with `git ls-remote https://github.com/anton48/vk-turn-proxy
add-server-srtp-layer`); bump that variable if you need a newer server build.

At the end it prints:

```
server public key:   <...>
client private key:  <...>
proxy endpoint:      <vps-ip>:56004
wg client address:   10.8.0.2/32
wg server address:   10.8.0.1
anton48 commit:      <pinned commit>
go version:          <pinned version>
```

Keep this output; you need the two keys and the endpoint for the client
config in step 2.

## 2. Client WireGuard config

On the operator laptop, create `wg-vk.conf`:

```
[Interface]
PrivateKey = <client private key from step 1>
Address = 10.8.0.2/32
DNS = 1.1.1.1
MTU = 1280

[Peer]
PublicKey = <server public key from step 1>
Endpoint = 127.0.0.1:9000
AllowedIPs = ...
PersistentKeepalive = 5
```

`Endpoint` is `127.0.0.1:9000`, not the VPS address: `turnrelay-udp` runs
locally and is what actually reaches the VPS, tunneled through the VK relay.
MTU is 1280 because the payload already carries relay and obfuscation
framing overhead; a larger MTU causes fragmentation or silent drops through
the VK relay.

### AllowedIPs: excluding the VK relay

If `AllowedIPs` is a naive `0.0.0.0/0`, the tunnel also swallows the route to
the VK TURN relay itself, which `turnrelay-udp` needs to reach directly
(outside the tunnel) to keep the allocations alive. You need one of:

**Option A: the two-/1 trick plus an explicit route (simplest).**

```
AllowedIPs = 0.0.0.0/1, 128.0.0.0/1
```

This covers all of IPv4 without literally being `0.0.0.0/0`, which on most
WireGuard implementations means it does not replace the kernel's existing
default route entry, only adds a lower-priority one. That leaves room to add
a `PostUp` line that keeps the relay's own /20 pinned to the physical
gateway, where a more specific route always wins:

```
PostUp = ip route add 155.212.192.0/20 via <physical-gateway> dev <physical-iface>
PostDown = ip route del 155.212.192.0/20 via <physical-gateway> dev <physical-iface>
```

Find `<physical-gateway>` and `<physical-iface>` with
`ip route show default` before bringing the tunnel up. `155.212.192.0/20` is
the known VK relay block; confirm the relay address(es) your run actually
used by checking the `turnrelay-udp` log for `via <relay>` (see step 3) and
adjust the excluded range if a session lands outside it.

**Option B: an AllowedIPs calculator (more portable, e.g. mobile clients
without PostUp scripting).**

Use the calculator at
<https://www.procustodibus.com/blog/2021/03/wireguard-allowedips-calculator/>,
enter `155.212.192.0/20` as the range to exclude from `0.0.0.0/0`, and paste
the resulting list of CIDR blocks directly into `AllowedIPs`. No `PostUp`
route is needed with this approach because the exclusion is baked into the
route list itself.

Either way, do not bring `wg-vk.conf` up yet.

## 3. Run turnrelay-udp

```
./turnrelay-udp -listen 127.0.0.1:9000 -provider vk -link <call link> \
  -server <vps-ip>:56004 -n 30 -mode srtp -stats 10s
```

Wait for readiness. Each allocation logs a line like:

```
mux: worker 0 up via <relay-host>:<relay-port> relayed <alloc-addr>
```

`<relay-host>` is the actual VK TURN relay address in use for that
allocation; note it down, it is what step 2's exclusion is protecting.
Do not proceed until you see at least `worker 0 up`; for the full acceptance
run you want all 30 workers up (watch the periodic `stats:` line, described
below).

## 4. Bring up the tunnel and test

```
wg-quick up ./wg-vk.conf
curl https://ifconfig.me
```

`ifconfig.me` must return the VPS's public IP, not your own. If it hangs or
times out, see Troubleshooting below before going further.

Start `iperf3` in server mode on the VPS, bound to the WireGuard address
(not the public interface):

```
ssh root@<vps-ip> 'iperf3 -s -B 10.8.0.1'
```

Then from the laptop, run the acceptance load test: reverse mode (server
sends, since the relay path is asymmetric and downlink is what the acceptance
criteria measures), 30 seconds:

```
iperf3 -c 10.8.0.1 -R -t 30
```

## 5. Acceptance criteria

All of the following must hold for the run to count as passing:

- `curl https://ifconfig.me` returns the VPS's public IP.
- `iperf3 -c 10.8.0.1 -R -t 30` reports at least 30 Mbit/s average over the
  30-second reverse transfer, with 30 connections active on the
  `turnrelay-udp` side (see next point) for the whole run.
- The `turnrelay-udp` periodic `stats:` line shows `Active: 30` (all 30
  allocations up, none restarting) and `CaptchaUntil` is the zero time
  (`0001-01-01 00:00:00 +0000 UTC`), i.e. no captcha was ever triggered
  during the run.

Record after the run, in this file or in the task report: the date, VPS
provider/region, measured throughput (Mbit/s), the `Active`/`Restarts`
values from the stats line at the end of the test, and the VK relay
address(es) seen in the `via <relay>` log lines.

## Troubleshooting

- **Credential pool reports a captcha (`CaptchaUntil` set to a future
  time in the stats line, or a log line mentioning captcha):** VK is rate
  limiting credential requests. Restart with fewer connections, e.g. `-n 10`,
  and wait for `CaptchaUntil` to pass before trying again or ramping back up.
  Do not hammer retries; that extends the cooldown.

- **TURN allocation fails with error 486 in the logs:** too many allocations
  requested against a single call link/credential (VK caps at 10 per
  credential, i.e. per participant). Add a second `-link <other participant's
  call link>` so the load is split across more than one credential, or lower
  `-n`.

- **WireGuard handshake never completes (`wg show` shows no recent
  handshake):** check `-server <vps-ip>:56004` matches the proxy port
  `vps-setup.sh` opened (default 56004), and that the VPS firewall/cloud
  security group allows inbound UDP on that port from the internet (the VK
  relay, not your laptop, is what connects to it). Also confirm at least one
  `mux: worker N up` line appeared before `wg-quick up` was run: without a
  live worker there is nothing to carry the handshake.

- **Tunnel comes up but traffic stalls or fragments:** confirm `MTU = 1280`
  in `wg-vk.conf`; the relay and obfuscation framing leaves less room for the
  WireGuard/IP/UDP headers than a normal internet path, and the default 1420
  MTU produces black-holed or fragmented packets through the relay.

- **`curl https://ifconfig.me` returns your own IP, not the VPS's:** the
  `AllowedIPs` exclusion or two-/1 route from step 2 is misconfigured and
  general traffic is not entering the tunnel at all; check `ip route` and
  `wg show` for the peer's allowed IPs.
