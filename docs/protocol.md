# turnrelay wire protocol

Status: milestone 1 deliverable. Describes what the `turnrelay` library
(`github.com/romanrublev/turnrelay`) puts on the wire, hop by hop, so that a
server implementer or a reviewer can check it against a packet capture.
Every value below is taken from the code on branch `m1` or from the design
spec (`docs/superpowers/specs/2026-09-14-turnrelay-design.md`).

`turnrelay` tunnels UDP datagrams (in practice WireGuard) through WebRTC TURN
relays, disguised as call media. VK Calls is one credential provider
(`provider: vk`); any relay with known long-term credentials is another
(`provider: static`). The transport itself does not depend on VK.

## 1. Overview

```
client (sing-box or turnrelay-udp)        TURN relay (whitelisted)        VPS
+-------------------------------+         +------------------+     +----------------------------+
| WireGuard datagrams           |         |                  |     | anton48/vk-turn-proxy      |
|   -> turnrelay Dialer         |         |  allocation 1  --+-----+-> -srtp :56004             |
|      N workers, one queue     | ChannelData over UDP (or TCP) |     |   group by session UUID  |
|      worker i: TURN alloc i --+---------+-> allocation i  --+-----+->  one UDP socket to       |
|        obfs (srtp|wrap|dtls)  |         |       ...        |     |    WireGuard :51820        |
|        hello / probe          |         |  allocation N  --+-----+->                          |
+-------------------------------+         +------------------+     +----------------------------+
```

The client opens N TURN allocations (default 30, maximum 60), one per worker.
Every allocation carries an independent obfuscated session towards the VPS.
The relay forwards the relayed payload to `server:port` on the VPS, which is
the only public port the VPS needs.

Layer stack for one allocation, bottom up:

| Layer | What is on the wire | Who sees it |
|---|---|---|
| UDP (or TCP) socket to the relay | STUN/TURN messages, then ChannelData frames | the network between client and relay |
| TURN relayed payload | one datagram per ChannelData frame, forwarded to the VPS | the relay |
| obfuscation (`srtp`, `wrap` or `dtls`) | DTLS handshake, then RTP-looking packets (or plain DTLS records) | the relay's traffic classifier, the VPS |
| control frames and payload | `hello`, `probe`, WireGuard datagrams | only the client and the VPS |

The Dialer presents all N allocations as one datagram pipe: a `Write` is one
datagram sent over whichever worker is free, a `Read` returns one datagram
from any worker.

## 2. Credential chain (`provider/vk`)

The VK provider reproduces what the VK web client does when an anonymous
visitor joins a call by link. The chain and the app ids follow
cacggghp/vk-turn-proxy. All requests are `POST` with
`Content-Type: application/x-www-form-urlencoded`, sent from a
`bogdanfinn/tls-client` HTTP client with the Chrome 146 TLS fingerprint, a
cookie jar and a 20 s timeout. Headers on every request: `User-Agent`
(Chrome 146 on Windows 10), `sec-ch-ua` matching that Chrome build,
`sec-ch-ua-mobile: ?0`, `sec-ch-ua-platform: "Windows"`, `Accept: */*`,
`Origin: https://vk.ru`, `Referer: https://vk.ru/`, `Sec-Fetch-Site:
same-site`, `Sec-Fetch-Mode: cors`, `Sec-Fetch-Dest: empty`.

DNS for these hosts bypasses the system resolver and asks `77.88.8.8`,
`77.88.8.1`, `8.8.8.8`, `1.1.1.1` in that order (whitelisted networks often
break system DNS first). When the embedding application supplies
`Config.DialContext` (a sing-box outbound passes its own dialer so `detour`
and `bind_interface` apply), every TCP connection to the VK API is opened
through it with the unresolved `host:443` instead, and that dialer is
responsible for name resolution.

`<hash>` is the part of the call link after `join/`
(`https://vk.ru/call/join/<hash>` or `vk.com`; a bare hash is accepted).

| # | URL | Form fields | Consumed from the JSON response |
|---|---|---|---|
| 1 | `https://login.vk.ru/?act=get_anonym_token` | `client_id=<app id>`, `token_type=messages`, `client_secret=<app secret>`, `version=1`, `app_id=<app id>` | `data.access_token` (token1) |
| 2 | `https://api.vk.ru/method/calls.getCallPreview?v=5.275&client_id=<app id>` | `vk_join_link=https://vk.com/call/join/<hash>`, `fields=photo_200`, `access_token=<token1>` | nothing; best effort, mirrors the web client |
| 3 | `https://api.vk.ru/method/calls.getAnonymousToken?v=5.275&client_id=<app id>` | `vk_join_link=https://vk.com/call/join/<hash>`, `name=<random display name>`, `access_token=<token1>` | `response.token` (token2); `error` object on failure, captcha appears here |
| 4 | `https://calls.okcdn.ru/fb.do` | `session_data={"version":2,"device_id":"<random uuid>","client_version":1.1,"client_type":"SDK_JS"}`, `method=auth.anonymLogin`, `format=JSON`, `application_key=CGMMEJLGDIHBABABA` | `session_key` (token3) |
| 5 | `https://calls.okcdn.ru/fb.do` | `joinLink=<hash>`, `isVideo=false`, `protocolVersion=5`, `capabilities=2F7F`, `anonymToken=<token2>`, `method=vchat.joinConversationByLink`, `format=JSON`, `application_key=CGMMEJLGDIHBABABA`, `session_key=<token3>` | `turn_server.username`, `turn_server.credential`, `turn_server.urls[]` |

The client sleeps 120 ms after hop 1, 300 ms after hop 2, 120 ms after hops 3
and 4. Every dynamic form value is URL-escaped. A non-2xx HTTP status on any
hop is an error naming the host, path and status. When a hop's JSON lacks
the field the chain needs, the error names the hop, the field and the
top-level keys that were present; response values (tokens, `session_key`,
`turn_server.username`, `turn_server.credential`) never appear in errors or
logs, since errors end up in `Stats.LastError`.

From `turn_server.urls[]` only `turn:` and `turns:` entries are kept, entries
with `transport=tcp` are dropped, and the scheme and query are stripped so
that each relay is a bare `host:port`. Worker `i` of a credential uses relay
`urls[i mod len(urls)]`.

App ids (from `provider/vk/client.go`; each has a matching client secret in
the same table there): `6287487` (VK web), `7879029` (m.vk), `52461373` (VK
Video web), `52649896` (m.vk Video), `51781872` (VK ID auth). The chain is
tried with each id in that order until one succeeds; a captcha aborts the
loop immediately.

Captcha. Hop 3 answers with an `error` object instead of `response`:

```json
{"error": {"error_code": 14, "error_msg": "...", "captcha_sid": "...",
           "captcha_img": "...", "redirect_uri": "https://...?...&session_token=..."}}
```

`error_code` 14 is VK Smart Captcha. The provider returns
`provider.CaptchaRequiredError{Sid, Img, RedirectURI, SessionToken}` where
`SessionToken` is the `session_token` query parameter of `redirect_uri`
(`captcha_sid` may arrive as a string or a number). Any other `error_code` is
a plain error.

Captcha (measured live 2026-09-14): the error carries a `redirect_uri` on
`id.vk.ru/not_robot_captcha` whose page embeds an obfuscated proof-of-work
script. The provider solves it in place: parse the IIFE arguments
`}("<input>", <difficulty>, "pow_timeout"))` (input, hex-zero prefix length,
never the obfuscated identifiers), sha256(input + nonce) until the prefix
matches, wrap the result as `v2.` + base64 of
`{"hash","nonce","duration_ms","telemetry":{},"tel_hash":""}` (the page's
own success-branch shape; the legacy three-field shape is answered when the
page has no `tel_hash`), then call `captchaNotRobot.initSession` (with the
page's `window.vk.lang`), `.settings`, `.componentDone` (random 32-hex
browser_fp, a fixed 1920x1080 desktop device JSON), wait 2 to 3 s, `.check`
(hash, the `window.vk` UUID as `debug_info`) and `.endSession`. These calls
carry `Origin: https://id.vk.ru` and Chrome's HTTP/2 header order, which VK
fingerprints. On `status: OK` the `success_token` is sent back on
`calls.getAnonymousToken` together with `captcha_sid`, `captcha_ts`,
`captcha_attempt`. When `check` refuses (status BOT, `show_captcha_type`
slider or image) the captcha error is surfaced unchanged; slider and image
challenges are not solved.

Lifetime and quota (`credpool`):

- A credential is treated as valid for 10 minutes minus a 60 s safety
  margin from the moment it was fetched; after that the slot is re-fetched.
- VK's allocation quota is about 20 allocations per call link (measured
  2026-09-14: the 21st Allocate on a link is answered with TURN error 486
  whichever credential it uses). The pool keeps 10 workers per credential
  slot; a 486 on a credential that had already allocated marks that slot
  saturated and the worker moves to another slot, while a 486 on a fresh
  credential means the link itself is full: the link is frozen for 10
  minutes (no further credential fetches, hence no further captchas) and
  the surplus workers wait. 30 connections therefore need two call links.
  401 or a stale nonce invalidates the slot and triggers a re-fetch.
- Fetches are serialised by a mutex and spaced by a random 3 to 6 s cooldown
  (VK rate-limits the chain). A captcha puts the pool into a 60 s cooldown
  during which no fetch is attempted.
- Slot `s` (workers `10s .. 10s+9`) fetches from call link `s mod len(links)`,
  so several call links spread the participants across calls.

One credential is one anonymous participant in the call. 20 connections on
one link are 2 credentials, so the call shows 2 extra anonymous
participants, not 20. The hard maximum of 60 connections is 6 participants
across at least 3 links.

## 3. Relay (`relay`)

One allocation per worker, made with pion/turn v5:

- Transport to the relay: a connected UDP socket by default (640 KiB send
  and receive buffers); TCP (`TURNUDP: false`, CLI `-tcp`) dials the same
  `host:port` over TCP with STUN framing (`turn.NewSTUNConn`). UDP is the
  recommended transport; the TCP path is slower. With `Config.DialContext`
  set, the socket is whatever that function returns for `("udp",
  host:port)` or `("tcp", host:port)`; a UDP conn is driven as a connected
  PacketConn, and the buffer sizes are applied only when it is a
  `*net.UDPConn`.
- `Allocate` with long-term credentials (username and password from the
  provider) and `REQUESTED-ADDRESS-FAMILY` IPv4, or IPv6 when the VPS
  address is IPv6. The reply's relayed address is what the VPS sees as the
  source of the tunnel.
- The first write towards the VPS makes pion/turn send `CreatePermission`
  for the VPS address, then `ChannelBind`. Until the binding is confirmed
  data goes as `Send` indications; afterwards every datagram is one
  ChannelData frame (4-byte header: channel number, length).
- pion/turn refreshes the allocation at half its lifetime, the permission
  every 120 s and the channel binding every 5 minutes.
- Keepalive: the worker sends a STUN `Binding` request to the relay every
  10 s for the lifetime of the allocation; VK relays drop silent allocations
  well before the 10 minute lifetime.
- Error classification on Allocate: a typed TURN error 486 is a quota error;
  401, 438 (stale nonce) and a typed 400 are auth errors; anything else is a
  transport error and only triggers backoff.

## 4. Obfuscation modes (`obfs`)

All three modes run a DTLS 1.2 client (pion/dtls v3) over the relayed
PacketConn with the same parameters, which is the set the whole server
family accepts:

| Parameter | Value |
|---|---|
| certificate | fresh self-signed ECDSA certificate per connection |
| peer verification | none (`InsecureSkipVerify`) |
| cipher suite | `TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256` only |
| extended master secret | required (RFC 7627) |
| connection id | extension offered (client sends the server's CID, requests none) |
| handshake timeout | 20 s (then the worker restarts) |

### 4.1 `srtp` (default)

Real DTLS-SRTP (RFC 5764). The handshake additionally carries the `use_srtp`
extension with the single profile `SRTP_AES128_CM_HMAC_SHA1_80`. After the
handshake the SRTP master keys and salts are exported from the DTLS session
(`EXTRACTOR-dtls_srtp` exporter, RFC 5764 section 4.2; pion/srtp
`ExtractSessionKeysFromDTLS`); the client encrypts with the client key and
decrypts with the server key. The DTLS connection stays open but carries no
application data after the handshake.

Every `Write` becomes one SRTP packet:

| Offset | Size | Field | Value |
|---|---|---|---|
| 0 | 1 | V, P, X, CC | `0x80` (version 2, no padding, no extension, no CSRC) |
| 1 | 1 | M, PT | `0x64` (marker 0, payload type 100) |
| 2 | 2 | sequence number | random initial value (RFC 3550), +1 per packet, big-endian |
| 4 | 4 | timestamp | random initial value (RFC 3550), + payload length per packet |
| 8 | 4 | SSRC | random, fixed for the connection |
| 12 | n | payload | the datagram (WireGuard packet or control frame), AES-128-CM encrypted |
| 12+n | 10 | authentication tag | HMAC-SHA1, 80 bits, over header and payload |

Wire size is payload + 22 bytes. `Read` demultiplexes the relayed
PacketConn by the first byte: 20..63 is a DTLS record (content types
change_cipher_spec, alert, handshake, application_data) and goes to the DTLS
state machine; 128..191 (RTP version 2) goes to SRTP unprotect; anything
else is dropped. Packets that fail authentication are dropped silently.

What the relay sees: a DTLS-SRTP handshake followed by RTP with payload
type 100, which is what a WebRTC media stream looks like.

### 4.2 `wrap` (WDTT-WRAP-v1)

Designed to be wire compatible with amurcanov/proxy-turn-vk-android and the
WDTT server family (ported from `android-client/obfs.go`); not yet verified
against a WDTT server, only against the in-process counterpart in
`obfs/obfstest`. The envelope is applied to every datagram of the relayed PacketConn,
including the DTLS handshake; inside it runs the plain DTLS session of
section 4.3 and the payload is the DTLS record. The relay only ever sees
RTP-looking packets.

Key: 32 bytes, either given raw (`WrapKey`, CLI `-wrap-key` hex) or derived
from a shared password:

```
key = HKDF-SHA256(IKM = password, salt = "WDTT-WRAP-v1",
                  info = "rtp-obfs/chacha20poly1305", length = 32)
```

Packet layout (`n` is the plaintext length, `r` the random padding length):

| Offset | Size | Field | Value |
|---|---|---|---|
| 0 | 1 | V, P, X, CC | `0xa0` (version 2, padding bit set, no extension, no CSRC) |
| 1 | 1 | M, PT | `0x6f` (payload type 111, Opus) by default; `0x60` (96) in video mode |
| 2 | 2 | sequence number | `initSeq + k` for the k-th packet of this sender, big-endian |
| 4 | 4 | timestamp | `initTS + 960*k + (k >> 16)` |
| 8 | 4 | SSRC | random, fixed for the sender |
| 12 | n + 16 | ChaCha20-Poly1305 ciphertext and 16-byte tag | AAD = the 12 header bytes |
| 28 + n | r | random padding | `r` random in 0..23 (audio) or 0..59 (video) |
| 28 + n + r | 1 | padding length | `r + 1` (1..24 or 1..60), as RTP padding requires |

`initSeq`, `initTS` and the SSRC are random per sender; `k` is a 64-bit
packet counter. 960 is 20 ms of 48 kHz Opus, so the timestamp advances like
an audio stream.

Nonce (12 bytes, derived from the header, so the receiver needs only the key):

| Offset | Size | Value |
|---|---|---|
| 0 | 4 | SSRC |
| 4 | 2 | sequence number |
| 6 | 2 | `00 00` |
| 8 | 4 | timestamp |

Receive path: a packet must be at least 29 bytes, RTP version 2 and payload
type 111 or 96, otherwise it is ignored; with the padding bit set the last
byte gives the padding length (rejected if 0 or larger than the packet);
the remainder is opened with the nonce above. Authentication failures are
dropped: during the handshake DTLS retransmits, afterwards it is one lost
datagram. A wrong password therefore shows up as a DTLS handshake timeout
(`obfs: dtls handshake: ...` after 20 s); the library cannot tell it apart
from a silent server.

Direction and nonce reuse. Both directions use the same key; the nonce
spaces are separated only by the random per-sender SSRC. Within one sender
the timestamp field wraps after 2^32 / 960, about 4.47 million packets.
Because the sequence number and the `k >> 16` term are part of the nonce,
the exact (SSRC, seq, ts) tuple repeats only after 2^48 packets in this
encoder; other WRAP-v1 senders may derive seq and ts differently, so the
conservative budget is the timestamp period. The operational rule is to
rekey by reconnecting well before ~4.4 million packets per allocation. In
practice this never comes close: the mux restarts an allocation on any
error, VK expires allocations at 10 minutes, and a 10 minute allocation at
200 KB/s carries on the order of 100 thousand packets.

### 4.3 `dtls` (legacy, deprecated)

Plain DTLS 1.2 with the parameters of the table above and no envelope; this
is what cacggghp/vk-turn-proxy speaks. Each `Write` is one DTLS
application_data record, so the relay sees DTLS content types (20..23) in
the first byte. VK relays now shape this to about 9 KB/s per allocation
(measured upstream by anton48, `pkg/proxy/proxy.go` in vk-turn-proxy-ios;
not re-measured by this project). The mode is kept for compatibility with
old servers and logs a deprecation warning when selected.

## 5. Control plane (`mux/control.go`)

Two frames travel inside the obfuscation layer, in the same position as a
WireGuard datagram. Formats follow anton48/vk-turn-proxy (`server/group.go`
and the probe echo in `server/main.go`).

Hello, 20 bytes:

| Offset | Size | Value |
|---|---|---|
| 0 | 4 | `ff 47 52 50` (`0xff`, `G`, `R`, `P`) |
| 4 | 16 | session UUID, random per Dialer, identical on all N workers |

Probe, 12 bytes:

| Offset | Size | Value |
|---|---|---|
| 0 | 4 | `ff 50 4e 47` (`0xff`, `P`, `N`, `G`) |
| 4 | 8 | sequence number, big-endian, per worker, starts at 1 |

When sent:

- hello: immediately after the obfuscation handshake, before the worker is
  counted as active, and again after every probe;
- probe: every 30 s per worker, followed by a hello.

Server behaviour (anton48 `-srtp` server): a hello is consumed, never
forwarded; the first hello promotes the connection into the group keyed by
the UUID (one shared UDP socket towards WireGuard per group), later hellos
are ignored. A probe is echoed back verbatim instead of being forwarded. The
client's read loop records the arrival time of every inbound packet, then
drops anything that starts with either magic, so echoes never reach
WireGuard. A worker that has received nothing for 120 s (four missed probes)
is killed and restarted.

Backward compatibility: a WireGuard packet begins with a little-endian
32-bit message type 1..4, so its first byte is `0x01`..`0x04`. `0xff` is
never a valid WireGuard first byte. A server that predates the control
frames forwards them to WireGuard, which discards them as malformed; the
tunnel still works, only without grouping and probe echo (that server also
forwards the probe to WireGuard, so the zombie detector then relies on
WireGuard's own traffic).

## 6. Multiplexing (`mux`)

- N workers (default 30, maximum 60), started 100 ms apart. A worker
  acquires a credential slot, allocates, runs the obfuscation handshake (at
  most 3 handshakes in flight across the pool, since VK rate-limits bursts of
  new sessions), sends the hello and joins the pool.
- Uplink: one bounded queue of 256 datagrams shared by all workers. Each
  worker's send goroutine takes the next datagram from the queue whenever
  its connection is free (work stealing), so a slow allocation takes fewer
  packets. A full queue blocks the writer (back-pressure); nothing is
  dropped. Closing the conn that is writing, or the Dialer, releases a
  parked writer with `net.ErrClosed`; after the Dialer is closed every
  `Write` fails that way. A datagram taken by a worker that dies before
  writing it is put back on the queue.
- Downlink: every worker reads its connection and pushes payload datagrams
  into one queue of 2048; reads block, nothing is dropped. All open datagram
  conns obtained from the Dialer read from this queue; the WireGuard bind
  holds one at a time.
- Ordering: within one allocation order is preserved, across allocations it
  is not, so datagrams arrive reordered whenever allocations have different
  latency. WireGuard tolerates this: its anti-replay window is 8128 packets
  (wireguard-go `replay` ring of 127 blocks of 64 bits), far more than the
  reorder depth that 30 allocations of similar latency produce. Wire
  ordering inside WireGuard's transport stream is not required for
  correctness, only for the anti-replay check.
- Server side: the anton48 server groups the N connections by session UUID
  into one hub with a single UDP socket towards WireGuard, and schedules the
  downlink across the group's connections with the same work-stealing idea.
  It can optionally hold out-of-order uplink packets for a short time
  (`-uplink-reseq`) and route small packets over a dedicated subset of
  connections (`-downlink-small-conns`); the client adds no sequence numbers
  of its own and needs neither option.

## 7. Failure handling

From the design spec, section 8:

| Condition | Behaviour |
|---|---|
| Credential fetch fails (network) | slot stays empty, retry with backoff, workers wait |
| Captcha required | `CaptchaRequiredError`, pool-wide cooldown 60 s (no slot is fetched, workers borrow from slots that still have room), surfaced in Stats and log |
| TURN 486 quota | slot marked saturated, worker retries with another slot |
| TURN 401 / stale nonce | slot invalidated, re-fetch |
| Handshake timeout | worker restart with backoff; in wrap mode reported as "password or wrap key not accepted" |
| No inbound 120 s | worker killed and restarted |
| Uplink queue full | writer blocks (back-pressure), no drops |
| All workers down | `DialContext` still succeeds; writes block until a worker returns or ctx ends |

One deviation from the spec table: the current code does not yet rewrite
the wrap-mode handshake timeout into "password or wrap key not accepted"; it
surfaces the plain handshake timeout, and the mapping is a documented
follow-up.

Worker backoff: 2 s after the first failure, doubling to a cap of 60 s, with
jitter (a delay `d` sleeps between `d/2` and `3d/2`). The ladder resets to
2 s after any session that reached the active state or outlived 60 s, so a
single outage does not tax every later reconnect with the maximum wait.

## 8. Security notes

- TURN: long-term credentials authenticate the client to the relay
  (MESSAGE-INTEGRITY over STUN). The relay sees the relayed payload: the
  tunnelled datagrams are always encrypted (SRTP, the WRAP envelope, or DTLS
  records), while the DTLS handshake itself is visible to the relay in
  `srtp` and `dtls` mode (in `wrap` mode it sits inside the envelope). The
  relay never sees a tunnelled datagram in plaintext.
- `srtp`: DTLS-SRTP with an ephemeral ECDHE key exchange and a fresh
  self-signed certificate per connection. Certificates are not verified
  (the server is identified by address, not by key), so this layer gives
  confidentiality and forward secrecy against a passive relay but no
  authentication of the VPS on its own. The inner WireGuard session provides
  the authentication.
- `wrap`: the envelope is a static symmetric key shared by all clients of a
  server. It has no forward secrecy: whoever learns the password can decrypt
  every recorded envelope. What it protects is only the DTLS session inside,
  which has its own ECDHE forward secrecy, and inside that WireGuard has its
  own. The wrapper exists to make the relay's classifier happy, not to
  protect data.
- `dtls`: same DTLS properties as `srtp`; deprecated for throughput reasons,
  not for security ones.
- WireGuard is the layer that authenticates both ends and protects the
  traffic; nothing in `turnrelay` weakens or replaces it.
- What VK sees: the anonymous-join chain (one display name and device id
  per credential), the number of participants (one per 10 connections) and
  the relayed byte counts per allocation; with `srtp` and `wrap` the payload
  looks like media. VK does not see destinations, since those are inside
  WireGuard.
- Ban footprint: everything a credential does is attributable to the call
  link it was fetched from and to the client's IP. More connections mean more
  participants in the call and more allocations per relay, both easier to
  notice. The default is 30 connections, the hard maximum 60; plaintext and
  no-DTLS modes are not offered because they get accounts banned.

## 9. Compatibility matrix

| `mode` | Server implementation | Server flags | Status |
|---|---|---|---|
| `srtp` | anton48/vk-turn-proxy, branch `add-server-srtp-layer` (release `srtp-build306`) | `-srtp` | supported, tested |
| `wrap` | WDTT server (amurcanov/proxy-turn-vk-android linux-server) | server-side password or key | supported, not yet run end to end |
| `dtls` | cacggghp/vk-turn-proxy server | default | supported, deprecated |
| not yet | FreeTurn (samosvalishe) profiles `rtpopus`, `rtpopus2`, `rtpopus3` | | different envelope, deferred |
| not yet | anton48/vk-turn-proxy `-wrap-srtp` (explicit 12-byte nonce, raw hex key) | `-wrap-srtp -wrap-key` | different envelope, deferred |

Verified so far: the docker interop test in `test/integration/` runs
`turnrelay-udp` in `srtp` mode with static coturn credentials against the
unmodified anton48 `add-server-srtp-layer` server; the server accepted 4
connections into one group, a WireGuard tunnel came up over the pipe and an
HTTP request through it returned the expected body. The run against real
VK relays (`docs/e2e.md`) and the `wrap` and `dtls` modes against their
servers are covered by in-process tests only at the time of writing.
