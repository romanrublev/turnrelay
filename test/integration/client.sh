#!/usr/bin/env bash
# Client side of the interop test: bring up turnrelay-udp against coturn and
# the anton48 SRTP server, point a real WireGuard at it, fetch the web target
# through the tunnel and compare the body.
set -uo pipefail

LOG=/tmp/turnrelay-udp.log
WANT=ok-through-relay
CONNS=${CONNS:-4}

turnrelay-udp -listen 127.0.0.1:9000 -provider static \
  -turn "${TURN:-172.28.0.10:3478}" -turn-user user -turn-pass pass \
  -server "${SERVER:-172.28.0.20:56004}" -n "$CONNS" -mode srtp -stats 5s >"$LOG" 2>&1 &
RELAY_PID=$!

fail() {
  echo "FAIL: $*" >&2
  echo "--- turnrelay-udp log ---" >&2
  cat "$LOG" >&2
  echo "--- wg show ---" >&2
  wg show >&2 || true
  kill "$RELAY_PID" 2>/dev/null
  exit 1
}

for i in $(seq 1 60); do
  up=$(grep -c 'up via' "$LOG" 2>/dev/null || true)
  if [ "${up:-0}" -ge "$CONNS" ]; then break; fi
  kill -0 "$RELAY_PID" 2>/dev/null || fail "turnrelay-udp exited early"
  sleep 1
done
[ "${up:-0}" -ge "$CONNS" ] || fail "only $up of $CONNS workers came up within 60s"
echo "turnrelay-udp: $up workers up"

wg-quick up /etc/wireguard/wg0.conf || fail "wg-quick up failed"

body=$(curl -s --max-time 15 http://172.28.0.40:5678 || true)
body=${body//$'\n'/}
echo "curl body: '$body'"
[ "$body" = "$WANT" ] || fail "body mismatch: got '$body', want '$WANT'"

echo "$WANT"
echo "--- wg show ---"
wg show
echo "--- turnrelay-udp log tail ---"
tail -n 12 "$LOG"
wg-quick down /etc/wireguard/wg0.conf >/dev/null 2>&1 || true
kill "$RELAY_PID" 2>/dev/null
exit 0
