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
present, and never issues `iptables` commands itself (the MASQUERADE rule and
the loopback-only rule below both live in `wg0.conf`'s `PostUp`/`PostDown`,
applied once per `wg-quick` up/down by `wg-quick` itself).

Only the proxy port (56004 by default) ends up reachable from the internet;
that is where the VK relay connects. The WireGuard port (51820 by default)
is only ever dialed locally, from the anton48 server over loopback, so the
script firewalls it to loopback-only with an `iptables -A INPUT -p udp
--dport <wg-port> ! -i lo -j DROP` rule and does not open it in `ufw`. A
publicly reachable WireGuard port would be trivially fingerprintable by
scanners and would defeat the point of disguising the traffic as SRTP.

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
firewall:            56004/udp public, 51820/udp loopback-only (iptables DROP)
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
the relay range documented by the upstream project for call traffic
(cacggghp/vk-turn-proxy README, <https://github.com/cacggghp/vk-turn-proxy>);
on 2026-09-14 our own runs were handed relays in `193.203.43.0/24`
(`193.203.43.18:19302`, `193.203.43.30:19302`) instead, so exclude both
ranges or, better, whatever the log shows. Confirm the relay
address(es) your run actually used by checking the `turnrelay-udp` log for
`via <relay>` (see step 3) and adjust the excluded range if a session lands
outside it.

**Option B: an AllowedIPs calculator (more portable, e.g. mobile clients
without PostUp scripting).**

Use the calculator at
<https://www.procustodibus.com/blog/2021/03/wireguard-allowedips-calculator/>,
enter `155.212.192.0/20` (the range from the cacggghp README, see above) as
the range to exclude from `0.0.0.0/0`, and paste the resulting list of CIDR
blocks directly into `AllowedIPs`. No `PostUp` route is needed with this
approach because the exclusion is baked into the route list itself.

Either way, do not bring `wg-vk.conf` up yet.

## 3. Run turnrelay-udp

```
./turnrelay-udp -listen 127.0.0.1:9000 -provider vk -link <call link> \
  -server <vps-ip>:56004 -n 20 -mode srtp -stats 10s
```

One call link may carry many connections (users of csqtt/wdtt report ~60);
the exact VK quota is not yet pinned down (see docs/protocol.md). If you hit
TURN error 486, add a second `-link` from another call or lower `-n`. VK's captcha is solved automatically
(`-captcha auto`, the default); a solve shows up in the log as
`vk: captcha solved, retrying getAnonymousToken`.

Wait for readiness. Each allocation logs a line like:

```
mux: worker 0 up via <relay-host>:<relay-port> relayed <alloc-addr>
```

`<relay-host>` is the actual VK TURN relay address in use for that
allocation; note it down, it is what step 2's exclusion is protecting.
Do not proceed until you see at least `worker 0 up`; for the full acceptance
run you want all 30 workers up (watch the periodic `stats:` line, described
below).

Every `-stats` interval (10s here), `turnrelay-udp` also logs a line like:

```
stats: {Workers:30 Active:30 Connecting:0 Restarts:0 CredSlots:3 CaptchaUntil:0001-01-01 00:00:00 +0000 UTC LastError:}
```

That is Go's default struct formatting (`%+v`), so there is no space after
each field's colon; `grep 'stats:'` on the log to follow it, and match
literally on `Active:30` / `CaptchaUntil:0001-01-01` (no spaces) rather than
`Active: 30`.

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
- The `turnrelay-udp` periodic `stats:` line (see step 3 for its exact,
  space-free format) shows `Active:30` (all 30 allocations up, none
  restarting) and `CaptchaUntil` is the zero time
  (`CaptchaUntil:0001-01-01 00:00:00 +0000 UTC`), i.e. no captcha was ever
  triggered during the run.

Record after the run, in this file or in the task report: the date, VPS
provider/region, measured throughput (Mbit/s), the `Active`/`Restarts`
values from the stats line at the end of the test, and the VK relay
address(es) seen in the `via <relay>` log lines.

## 6. Runs

### 2026-09-14, first live run

- VPS: Debian 13 with Docker, 159.195.54.89 (netcup), server
  `add-server-srtp-layer` @ 37d6607 with `-srtp -uplink-reseq 100ms`.
- Client: macOS, `turnrelay-udp -n 20` (one call link; 20 is the per-link
  quota, see Troubleshooting), tunnel driven by sing-box 1.14 with a
  `wireguard` endpoint whose peer is `127.0.0.1:9000` and a `mixed` inbound
  (no root needed; DNS pointed at 8.8.8.8 through the endpoint because the
  host resolver was filtered). Relays: `193.203.43.18:19302`,
  `193.203.43.30:19302`.
- Credentials: VK asked for a captcha on every fetch; the automatic solver
  passed every time (6 of 6 that day).
- Exit IP through the tunnel: the VPS address, on every check.
- Throughput with the relays reached directly (a Happ VPN on the host was
  told to route the relay ranges and the VPS `direct`): download 24 to 29
  Mbit/s, upload 12 to 16 Mbit/s on 20 connections; the host's own link
  measured 21 Mbit/s down at the time, so the tunnel was at the link's
  ceiling, not its own. With the relays reached through that VPN instead:
  14.5 down, 8 to 10 up.
- Stability: `Active:20 Restarts:0` for 15 minutes straight, including the
  credential expiry at 10 minutes: VK keeps refreshing existing allocations
  after their credential's TTL has passed, so expiry only affects new
  allocations and never restarts a working pool. Server-side uplink
  reordering was 87.6% before and 0.0% after the resequencer, 0 losses.
- Not met: the 30 Mbit/s criterion, which needs a second call link (30
  connections) and a faster host link than the one available.

## Troubleshooting

- **Credential pool reports a captcha (`CaptchaUntil:` set to a future
  time in the stats line, or a log line mentioning captcha):** VK is rate
  limiting credential requests. Restart with fewer connections, e.g. `-n 10`,
  and wait for `CaptchaUntil` to pass before trying again or ramping back up.
  Do not hammer retries; that extends the cooldown.

- **`credpool: link ... is at VK's allocation quota` / TURN error 486 in
  the logs:** the pool saw a relay refuse a fresh credential's first
  allocation and, conservatively, stopped fetching for that link for 10
  minutes. The real quota is not yet measured (it is probably per credential,
  which more `-link`s or more credentials work around); add a second
  `-link <call link of another call>` or lower `-n`. Note that after a
  network flap the old allocations keep counting on the relay until they
  expire (10 minutes), so a restart right after one can see 486 that is not
  a real ceiling.

- **Upload is far below download:** the uplink is striped over N
  allocations with different latencies and arrives reordered; TCP inside
  the tunnel reads that as loss. Run the server with `-uplink-reseq 100ms`
  (the setup script does since 2026-09-14): measured 0.4 to 3.3 Mbit/s on
  10 connections.

- **Docker on the VPS:** Docker sets the `FORWARD` chain policy to DROP, so
  WireGuard clients handshake but get no traffic. The setup script adds
  `FORWARD` accept rules for `wg0` in `PostUp`; on a server set up before
  2026-09-14 add them by hand (see `scripts/vps-setup.sh`).

- **WireGuard handshake never completes (`wg show` shows no recent
  handshake):** check `-server <vps-ip>:56004` matches the proxy port
  `vps-setup.sh` opened (default 56004), and that the VPS firewall/cloud
  security group allows inbound UDP on that port from the internet (the VK
  relay, not your laptop, is what connects to it). The WireGuard port itself
  (51820 by default) is deliberately loopback-only on the VPS, see step 1;
  do not open it in the cloud security group, that is not the fix. Also
  confirm at least one `mux: worker N up` line appeared before `wg-quick up`
  was run: without a live worker there is nothing to carry the handshake.

- **Tunnel comes up but traffic stalls or fragments:** confirm `MTU = 1280`
  in `wg-vk.conf`; the relay and obfuscation framing leaves less room for the
  WireGuard/IP/UDP headers than a normal internet path, and the default 1420
  MTU produces black-holed or fragmented packets through the relay.

- **`curl https://ifconfig.me` returns your own IP, not the VPS's:** the
  `AllowedIPs` exclusion or two-/1 route from step 2 is misconfigured and
  general traffic is not entering the tunnel at all; check `ip route` and
  `wg show` for the peer's allowed IPs.
