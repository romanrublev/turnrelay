# turnrelay wire protocol

[English](protocol.md) | [Русский (README)](../README.ru.md)


Describes what the `turnrelay` library
(`github.com/romanrublev/turnrelay`) puts on the wire, hop by hop, so a
server implementer or a reviewer can check it against a packet capture.
Every value below is taken from the library code.

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

The VK provider joins a call by link as an anonymous visitor and reads the
TURN credentials VK hands out for the call. There are two paths: a
captcha-free one used by default, and a legacy one kept as a fallback.

### 2.1 Captcha-free path (default)

This is the flow the VK Calls app itself uses. It goes through `api.vk.me`
with VK Connect's public `client_id` (`8093730`, no secret) and is not
captcha-gated. Requests are bodiless `POST`s (every parameter is in the URL),
sent from a `bogdanfinn/tls-client` HTTP client with a **Safari iOS TLS
fingerprint** and a matching iOS `User-Agent` (a Chrome fingerprint here is
rejected). `<link>` is `https://vk.ru/call/join/<hash>`, URL-escaped.

| # | method (`https://api.vk.me/method/...`) | key parameters | consumed |
|---|---|---|---|
| 1 | `auth.getAnonymToken` | `v=5.276`, `client_id=8093730`, `link`, `device_id`, `anonymName` | `response.token` (anonymous token) |
| 2 | `messages.getCallPreview` | `anonymous_token`, `device_id`, `link` | confirms the call; `user_id`, `secret` when present |
| 3 | `messages.getAnonymCallToken` | `anonymous_token`, `device_id`, `link`, `name` (and `user_id`/`secret` if step 2 returned them) | `response.token` (call token) |
| 4 | `calls.okcdn.ru/fb.do` | `session_data={"version":2,"device_id":"<uuid>","client_version":"1.0.1"}`, `method=auth.anonymLogin`, `application_key=CGMMEJLGDIHBABABA` | `session_key` |
| 5 | `calls.okcdn.ru/fb.do` | `joinLink=<hash>`, `isVideo=false`, `protocolVersion=5`, `anonymToken=<call token>`, `method=vchat.joinConversationByLink`, `application_key=CGMMEJLGDIHBABABA`, `session_key` | `turn_server.username`, `turn_server.credential`, `turn_server.urls[]` |

A network-level failure (VK resets pooled HTTP/2 connections often) drops idle
connections and retries once on a fresh one. If a captcha gate ever appears
here, or a step errors, the provider falls back to the legacy path.

### 2.2 Legacy path (fallback)

The older flow the VK web client uses. Requests are `POST`s with
`application/x-www-form-urlencoded` bodies, from a Chrome TLS fingerprint,
Origin/Referer `https://vk.ru`. It uses `login.vk.ru` for an anonymous access
token, then `api.vk.ru/method/calls.getAnonymousToken` for the call token,
then the same two `calls.okcdn.ru` steps. VK now captcha-gates
`calls.getAnonymousToken` and rejects freshly created links there, so this
path mostly serves as a backstop.

Several VK app id/secret pairs are tried in order; a captcha aborts the loop.

### Captcha (legacy path only)

`calls.getAnonymousToken` may answer with `error_code` 14 (VK Smart Captcha):

```json
{"error": {"error_code": 14, "captcha_sid": "...",
           "redirect_uri": "https://...?session_token=..."}}
```

The provider solves a proof-of-work captcha automatically: it fetches the
`id.vk.ru/not_robot_captcha` page behind `redirect_uri`, parses the
obfuscated PoW parameters (`}("<input>", <difficulty>, ...)`), computes
`sha256(input + nonce)` until it starts with `<difficulty>` hex zeros, wraps
the result as `v2.` + base64 of `{"hash","nonce","duration_ms","telemetry":{},"tel_hash":""}`,
and runs the `captchaNotRobot.{initSession,settings,componentDone,check,endSession}`
calls (with `Origin: https://id.vk.ru` and Chrome's HTTP/2 header order,
which VK fingerprints). On success the `success_token` is replayed on
`calls.getAnonymousToken`. Slider and image challenges are not solved; those
surface the captcha error unchanged.

### Relay list, lifetime and quota

From `turn_server.urls[]` only `turn:`/`turns:` entries are kept,
`transport=tcp` entries are dropped, and the scheme and query are stripped so
each relay is a bare `host:port`. Worker `i` of a credential uses relay
`urls[i mod len(urls)]`.

- **Lifetime.** A captcha-free credential is good for hours; the pool treats a
  credential as valid for its TTL (8 hours, minus a 30-minute margin) and
  re-fetches before the next allocation after that. Existing allocations keep
  working past the TTL because the relay accepts TURN Refresh with the
  original credential; the TTL only gates new allocations.
- **Quota.** VK's quota is per credential (per anonymous TURN username): about
  20 allocations, after which the relay answers TURN error 486 (Allocation
  Quota Reached). The call link itself is not limited - several credentials
  from one link each get their own ~20, so one link carries the full
  connection count. The pool keeps `ConnsPerSlot` workers (18) per credential
  slot; a 486 saturates that slot and the worker moves to a fresh slot, which
  mints another credential. A 401 or stale nonce invalidates the slot and
  re-fetches.
- **Pacing.** Fetches are serialised and spaced by a random cooldown. Slot `s`
  (workers `18s .. 18s+17`) fetches from call link `s mod len(links)`, so
  multiple links spread the participants across calls.

One credential is one anonymous participant in the call, good for ~18
connections, so the participant count is roughly `connections / 18`. One call
link is enough for the 60-connection maximum.

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
  before their nominal lifetime.
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
(as measured upstream by anton48). The mode is kept for compatibility with
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
HTTP request through it returned the expected body. Real VK relays are exercised through the VK provider end to end; the `wrap`
and `dtls` modes against their servers are covered by in-process tests.
