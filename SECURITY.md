# Security and safety

[English](SECURITY.md) | [Русский](SECURITY.ru.md)


## Threat model

turnrelay disguises tunnelled traffic as WebRTC media so a network's traffic
classifier does not flag it. It does **not** hide from the call service
itself: the relay operator can see that allocations exist, how many, and how
much data flows. The account that created the call link is visible to the
service and is the thing most at risk of being limited or banned.

Reduce that risk:

- Use call links from throwaway accounts, not your main one.
- Keep the connection count (`-n`) modest.
- Do not run it continuously at high volume from one link/account.

## What is and is not encrypted

- The tunnelled datagrams (WireGuard inside the obfuscation) are always
  encrypted end to end.
- In `srtp` and `dtls` modes the DTLS handshake itself is visible to the
  relay; in `wrap` mode it sits inside the envelope.
- The `wrap` mode's envelope uses a static key with no forward secrecy; the
  inner WireGuard session has its own. Do not treat the obfuscation layer as
  the confidentiality boundary - WireGuard is.

## Proxy-exit mode

With `server_type: exit` (the `turnrelay-server` deployment) there is no
WireGuard: the confidentiality boundary is the per-allocation DTLS/SRTP, and
the server dials destinations on your behalf. This adds two requirements.

- A pre-shared `password` is mandatory and authenticates the session (an
  HMAC of the session id). It is a shared secret: anyone who holds it can
  dial through your server, so keep it secret and rotate it by changing
  `password` on both ends. A server that dialed arbitrary destinations
  without it would be an open proxy.
- The server refuses private, loopback, link-local, multicast and
  unspecified destinations by default, so a leaked password cannot be used to
  reach the VPS's own network. `-allow-private` disables that; leave it off
  unless you understand the consequence.

## Reporting a vulnerability

Open an issue for non-sensitive reports. For anything that should not be
public, contact the maintainer privately rather than filing a public issue.
