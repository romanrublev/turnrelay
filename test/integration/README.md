# Interop test: turnrelay-udp against the real anton48 SRTP server

`make integration` (from the repo root) proves that `turnrelay-udp` talks to
the unmodified upstream `anton48/vk-turn-proxy` server (branch
`add-server-srtp-layer`) through a TURN relay, and that a real WireGuard
tunnel runs over the result. Nothing touches VK: the client uses
`-provider static` with coturn credentials.

It is a manual docker test, not part of `go test`. It needs Docker with
Compose v2+ and takes about a minute (the first build clones and compiles the
upstream server, about 40 s on an arm64 Mac).

## Topology

One bridge network, `172.28.0.0/24`:

| service    | ip           | what                                                                 |
|------------|--------------|----------------------------------------------------------------------|
| `coturn`   | 172.28.0.10  | `coturn/coturn:4.6`, long-term creds `user:pass`, relay ports 49152-49200 |
| `vkserver` | 172.28.0.20  | upstream server built from git, `-listen 0.0.0.0:56004 -connect 172.28.0.30:51820 -srtp` |
| `wgserver` | 172.28.0.30  | Alpine + wireguard-tools, `wg/server.conf`, ip_forward + MASQUERADE  |
| `web`      | 172.28.0.40  | `hashicorp/http-echo -text=ok-through-relay -listen=:5678`           |
| `client`   | 172.28.0.50  | `turnrelay-udp` built from this repo + wireguard-tools + curl        |

Path of a packet from the client:

    wg0 (10.99.0.2) -> 127.0.0.1:9000 turnrelay-udp -> coturn allocation
      -> 172.28.0.20:56004 vkserver (DTLS-SRTP, group of 4 conns)
      -> 172.28.0.30:51820 wgserver wg0 (10.99.0.1) -> MASQUERADE -> web:5678

## What the client does

`client.sh` (the container entrypoint):

1. starts `turnrelay-udp -listen 127.0.0.1:9000 -provider static -turn 172.28.0.10:3478 -turn-user user -turn-pass pass -server 172.28.0.20:56004 -n 4 -mode srtp` in the background, logging to a file;
2. waits up to 60 s for four `mux: worker N up via ...` lines;
3. `wg-quick up` on `wg/client.conf` (Endpoint `127.0.0.1:9000`, MTU 1280, AllowedIPs `172.28.0.40/32`, PersistentKeepalive 5);
4. `curl -s --max-time 15 http://172.28.0.40:5678` and compares the body to `ok-through-relay`.

It exits 0 only on a match. On any failure it dumps the `turnrelay-udp` log
and `wg show` and exits 1; `run.sh` propagates that status, prints the
server's `group`/`conn` log lines and tears the lab down.

Expected server log:

    SRTP session from 172.28.0.10:49185
    group <hex>: opened a shared socket to 172.28.0.30:51820 (1 group(s) now)
    conn 1 joined group <hex>
    ...
    conn 4 joined group <hex>

## WireGuard

Both WireGuard containers use the Docker VM's kernel module (`NET_ADMIN`
plus `/dev/net/tun`); `wireguard-go` is installed as well so `wg-quick`
falls back to it automatically if the kernel module is missing.

The keys in `wg/*.conf` are throwaway test keys generated for this lab and
committed on purpose. Do not reuse them anywhere.

## Knobs

`client.sh` reads `CONNS` (default 4), `TURN` and `SERVER` from the
environment, e.g. `docker compose run --rm -e CONNS=8 client`.
