# Server and client setup

[English](server-setup.md) | [Русский](server-setup.ru.md)


How to stand up the exit VPS and connect to it, both with a plain WireGuard
client and with sing-box.

You need:

- a Debian or Ubuntu VPS with a public IPv4 address (outside the restricted
  network for the "exit abroad" case);
- for the VK provider, a VK call link (`https://vk.ru/call/join/<hash>`) that
  stays open, joined from another device so the call is live;
- the `turnrelay-udp` binary (`make build`), and `wireguard-tools` or sing-box
  on the client.

## 1. Server

Copy `scripts/vps-setup.sh` to the VPS and run it as root:

```bash
scp scripts/vps-setup.sh root@<vps-ip>:/root/
ssh root@<vps-ip> './vps-setup.sh <vps-ip>'
```

Optional second and third arguments override the WireGuard port (default
51820) and the proxy port (default 56004):

```bash
./vps-setup.sh <vps-ip> 51820 56004
```

The script installs WireGuard and the relay-side server (built from the
upstream `vk-turn-proxy` server at a pinned commit), and is idempotent -
re-running it does not regenerate keys or re-clone. It firewalls the proxy
port (56004) open to the internet, where the relay connects, and keeps the
WireGuard port loopback-only (a public WireGuard port would be trivially
fingerprintable and defeat the point). It prints the two WireGuard keys and
the endpoint you need for the client:

```
server public key:   <...>
client private key:  <...>
proxy endpoint:      <vps-ip>:56004
wg client address:   10.8.0.2/32
wg server address:   10.8.0.1
```

Keep this output.

## 2. Client bridge: turnrelay-udp

`turnrelay-udp` opens the TURN allocations and presents them as a local UDP
socket. Run it on the client:

```bash
turnrelay-udp -listen 127.0.0.1:9000 -provider vk \
  -link https://vk.ru/call/join/<hash> \
  -server <vps-ip>:56004 -n 18 -mode srtp
```

Wait until a worker is up. Each allocation logs:

```
mux: worker 0 up via <relay-host>:<relay-port> relayed <alloc-addr>
```

`<relay-host>` is the actual relay in use - note it, the WireGuard config
below has to route it outside the tunnel. Watch the periodic `stats:` line to
see all workers come up (`Active:` reaches `-n`). The VK captcha, if any, is
solved automatically.

## 3. Client: WireGuard

Point a WireGuard client at the local bridge. Create `wg.conf`:

```ini
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

`Endpoint` is `127.0.0.1:9000` (the local bridge), not the VPS. **MTU must be
1280**: the payload already carries relay and obfuscation framing, and a
larger MTU fragments or black-holes through the relay.

### AllowedIPs: keep the relay outside the tunnel

A naive `AllowedIPs = 0.0.0.0/0` swallows the route to the TURN relay that
`turnrelay-udp` needs to reach directly. Exclude the relay range. Two ways:

**A. Two /1 blocks plus a pinned route (simplest on desktop):**

```ini
AllowedIPs = 0.0.0.0/1, 128.0.0.0/1
PostUp = ip route add <relay-range> via <gateway> dev <iface>
PostDown = ip route del <relay-range> via <gateway> dev <iface>
```

Find `<gateway>` and `<iface>` with `ip route show default`. `<relay-range>`
is the network of the relay from the `via <relay>` log line (VK's are around
`155.212.192.0/20` and `193.203.43.0/24`; check the log for yours).

**B. An AllowedIPs calculator (portable, e.g. mobile):** use
<https://www.procustodibus.com/blog/2021/03/wireguard-allowedips-calculator/>
to subtract the relay range from `0.0.0.0/0` and paste the resulting CIDR
list into `AllowedIPs`. No `PostUp` needed.

Bring it up once a worker is live:

```bash
wg-quick up ./wg.conf
curl https://ifconfig.me   # should return the VPS's IP
```

## 4. Client: sing-box (routing)

To route by rules instead of sending everything through the tunnel, use
sing-box with a `wireguard` endpoint whose peer is the local bridge. Minimal
config:

```json
{
  "inbounds": [
    { "type": "mixed", "listen": "127.0.0.1", "listen_port": 1080 }
  ],
  "endpoints": [
    { "type": "wireguard", "tag": "wg",
      "address": ["10.8.0.2/32"], "mtu": 1280,
      "private_key": "<client private key>",
      "peers": [ { "address": "127.0.0.1", "port": 9000,
                   "public_key": "<server public key>",
                   "allowed_ips": ["0.0.0.0/0"],
                   "persistent_keepalive_interval": 15 } ] }
  ],
  "outbounds": [ { "type": "direct", "tag": "direct" } ],
  "route": {
    "rules": [
      { "rule_set": "geosite-ru", "outbound": "direct" },
      { "rule_set": "geoip-ru",   "outbound": "direct" }
    ],
    "final": "wg"
  }
}
```

Here `allowed_ips: 0.0.0.0/0` is not routing - sing-box's rules decide what
enters the `wg` endpoint; anything sent `direct` never touches it. If the
restricted network also breaks DNS, point sing-box's DNS through the endpoint
(`"dns": {"servers": [{"type": "udp", "server": "8.8.8.8", "detour": "wg"}]}`).

## Troubleshooting

- **`curl` returns your own IP, not the VPS's:** general traffic is not
  entering the tunnel. Check the `AllowedIPs` exclusion / two-/1 route, and
  `wg show` for the peer's allowed IPs.

- **WireGuard handshake never completes:** confirm `-server <vps-ip>:56004`
  matches the proxy port, that the VPS firewall allows inbound UDP on it, and
  that at least one `worker N up` line appeared before `wg-quick up`. The
  WireGuard port (51820) is loopback-only by design - do not open it.

- **Tunnel up but traffic stalls or fragments:** confirm `MTU = 1280`.

- **TURN error 486 (Allocation Quota Reached):** normal past ~18-20
  allocations on one credential; the pool moves the worker to a fresh
  credential. Persistent 486 across all slots right after a restart means the
  previous run's allocations are still counting on the relay (they expire in
  a few minutes); wait and retry.

- **A captcha is reported (`CaptchaUntil:` in the future):** the auto-solver
  did not get through. Restart with fewer connections and wait for the
  cooldown; do not hammer retries, that extends it.

- **Upload much lower than download:** the striped uplink arrives reordered
  and TCP inside the tunnel reads that as loss. Run the server with
  `-uplink-reseq 100ms` (the setup script does).

- **Docker on the VPS:** Docker sets the `FORWARD` policy to DROP, so
  WireGuard clients handshake but get no traffic. The setup script adds
  `FORWARD` accept rules for `wg0`; add them by hand if your server predates
  that.

## Exit server (no WireGuard)

`turnrelay-server` terminates the transport and dials destinations itself,
so the VPS needs no WireGuard and the client needs no `wireguard` endpoint.
It requires a pre-shared password: a server that dials arbitrary
destinations without one is an open proxy. By default it refuses private,
loopback and link-local destinations (`-allow-private` to change that).

```bash
sudo PASSWORD='choose-a-long-random-password' bash scripts/vps-setup-exit.sh
```

Flags (`turnrelay-server -h`): `-listen` (default `:56004`), `-mode`
(`srtp` default, `wrap`, `dtls`), `-password` or `TURNRELAY_PASSWORD`,
`-bind`, `-allow-private`, `-dial-timeout`, `-max-streams`, `-udp-timeout`.

Clients: in sing-box set `"server_type": "exit"` and `"password"` on the
`turnrelay` outbound and drop the `wireguard` endpoint; the outbound is then a
normal TCP+UDP proxy outbound. Without sing-box, run a local SOCKS5 proxy:

```bash
turnrelay-proxy -server <vps-ip>:56004 -password '...' -links https://vk.ru/call/join/<hash>
```

and point any SOCKS5 client at `127.0.0.1:1080`.
