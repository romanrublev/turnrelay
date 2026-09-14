#!/usr/bin/env bash
# Build the lab, run it until the client exits, check the server's group log,
# tear everything down and exit non-zero if either the client or the group
# check failed.
set -uo pipefail
cd "$(dirname "$0")"
CONNS=${CONNS:-4}
export CONNS
docker compose build || exit 1
docker compose up --abort-on-container-exit --exit-code-from client
status=$?
log=$(docker compose logs --no-log-prefix vkserver 2>/dev/null || true)
echo "--- vkserver: group/conn lines ---"
printf '%s\n' "$log" | grep -E 'group|conn |hello|SRTP' | tail -20 || true
docker compose down -v

# The upstream server logs "conn N joined group <hex>" once per connection
# it promotes into a group. All CONNS connections must have joined, and
# they must all be in the same group (one session UUID); otherwise the
# tunnel worked by accident (each conn alone) rather than as a pool.
joined=$(printf '%s\n' "$log" | grep -cE 'conn [0-9]+ joined group [0-9a-f]+' || true)
groups=$(printf '%s\n' "$log" | grep -oE 'joined group [0-9a-f]+' | sort -u | wc -l | tr -d ' ')
echo "--- vkserver: $joined conn(s) joined $groups group(s), want $CONNS in 1 ---"
if [ "$joined" -lt "$CONNS" ] || [ "$groups" -ne 1 ]; then
  echo "FAIL: expected $CONNS connections in one group on the server" >&2
  [ "$status" -eq 0 ] && status=1
fi
echo "--- integration: client exit status $status ---"
exit $status
