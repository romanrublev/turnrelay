#!/usr/bin/env bash
# Sets up a turnrelay exit server on a fresh Debian/Ubuntu VPS: builds
# cmd/turnrelay-server, installs it under /usr/local/bin, writes the password
# to an env file readable only by root, and runs it as a hardened systemd
# service on UDP $PORT. No WireGuard is involved: the server dials
# destinations itself.
#
# Usage: sudo PASSWORD='...' [PORT=56004] [MODE=srtp] bash vps-setup-exit.sh
set -euo pipefail

PORT="${PORT:-56004}"
MODE="${MODE:-srtp}"
PASSWORD="${PASSWORD:?set PASSWORD to the pre-shared password}"
GO_VERSION="${GO_VERSION:-1.27.0}"
REPO="${REPO:-https://github.com/romanrublev/turnrelay.git}"

apt-get update
apt-get install -y git curl ca-certificates

if ! command -v go >/dev/null || ! go version | grep -q "go${GO_VERSION}"; then
  curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-$(dpkg --print-architecture).tar.gz" -o /tmp/go.tgz
  rm -rf /usr/local/go && tar -C /usr/local -xzf /tmp/go.tgz
fi
export PATH="/usr/local/go/bin:$PATH"

rm -rf /tmp/turnrelay-src
git clone --depth 1 "$REPO" /tmp/turnrelay-src
(cd /tmp/turnrelay-src && CGO_ENABLED=0 go build -o /usr/local/bin/turnrelay-server ./cmd/turnrelay-server)

install -d -m 0750 /etc/turnrelay
umask 077
printf 'TURNRELAY_PASSWORD=%s\n' "$PASSWORD" > /etc/turnrelay/server.env
chmod 0600 /etc/turnrelay/server.env

cat > /etc/systemd/system/turnrelay-server.service <<EOF
[Unit]
Description=turnrelay exit server
After=network-online.target
Wants=network-online.target

[Service]
EnvironmentFile=/etc/turnrelay/server.env
ExecStart=/usr/local/bin/turnrelay-server -listen :${PORT} -mode ${MODE}
Restart=always
RestartSec=3
DynamicUser=yes
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectControlGroups=yes
RestrictAddressFamilies=AF_INET AF_INET6
RestrictNamespaces=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
SystemCallFilter=@system-service
SystemCallArchitectures=native
CapabilityBoundingSet=

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now turnrelay-server
echo "turnrelay exit server listening on udp/${PORT} (${MODE})"
