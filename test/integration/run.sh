#!/usr/bin/env bash
# Build the lab, run it until the client exits, show the server's group log,
# tear everything down and exit with the client's status.
set -uo pipefail
cd "$(dirname "$0")"
docker compose build || exit 1
docker compose up --abort-on-container-exit --exit-code-from client
status=$?
echo "--- vkserver: group/conn lines ---"
docker compose logs --no-log-prefix vkserver | grep -E 'group|conn |hello|SRTP' | tail -20 || true
docker compose down -v
echo "--- integration: client exit status $status ---"
exit $status
