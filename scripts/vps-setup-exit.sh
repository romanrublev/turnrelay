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
REPO="${REPO:-https://github.com/romanrublev/turnrelay.git}"

# --- pinned Go version and checksums, keep in sync with scripts/vps-setup.sh -
GO_VERSION="${GO_VERSION:-1.27.1}"
GO_SHA256_AMD64=63d339f0da5ab53635a56f2490a7984dfe12dfcff22ad749f63edaf590168445
GO_SHA256_ARM64=3450b45a3f9ee8568792736a5c5e70a1f2e9b36c35a8f74958c03e51d7d92bec
# -------------------------------------------------------------------------

apt-get update
apt-get install -y git curl ca-certificates

if ! command -v go >/dev/null || ! go version | grep -q "go${GO_VERSION}"; then
  case "$(uname -m)" in
  x86_64) GOARCH=amd64; GOSHA=$GO_SHA256_AMD64 ;;
  aarch64) GOARCH=arm64; GOSHA=$GO_SHA256_ARM64 ;;
  *)
    echo "unsupported arch: $(uname -m)" >&2
    exit 1
    ;;
  esac
  TARBALL="go${GO_VERSION}.linux-${GOARCH}.tar.gz"
  curl -fsSL "https://go.dev/dl/${TARBALL}" -o /tmp/go.tgz
  echo "${GOSHA}  /tmp/go.tgz" | sha256sum -c -
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
