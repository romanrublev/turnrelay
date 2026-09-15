#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")"
go build -tags "with_wireguard,with_gvisor,with_quic" -o bin/turnrelay .
echo "built bin/turnrelay"
