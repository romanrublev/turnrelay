#!/usr/bin/env bash
# Installs WireGuard and the anton48 SRTP server on a fresh Debian/Ubuntu VPS,
# for the real VK end-to-end run described in docs/e2e.md.
#
# Usage: vps-setup.sh <public-ip> [wg-port=51820] [proxy-port=56004]
#
# Safe to re-run: it does not regenerate WireGuard keys that already exist,
# does not re-download Go or re-clone the anton48 source if already present,
# and never calls iptables directly (the only iptables rules are the
# PostUp/PostDown lines inside wg0.conf -- MASQUERADE plus a rule that keeps
# the WireGuard port loopback-only, see below -- which wg-quick itself
# adds/removes exactly once per up/down; systemctl enable --now does not
# restart an already-running unit, so re-running this script does not
# duplicate them).
#
# Only the proxy port ($PXPORT) is meant to be reachable from the internet:
# that is where the VK relay connects. The WireGuard port ($WGPORT) is only
# ever dialed locally, by the anton48 server over loopback (see the
# systemd unit's -connect 127.0.0.1:$WGPORT below). Exposing the WireGuard
# port publicly would hand scanners a fingerprintable WireGuard endpoint,
# defeating the whole point of tunneling it inside the SRTP disguise, so
# this script firewalls it to loopback only and does not open it in ufw.
set -euo pipefail

PUB=${1:?usage: vps-setup.sh <public-ip> [wg-port=51820] [proxy-port=56004]}
WGPORT=${2:-51820}
PXPORT=${3:-56004}

# --- pinned versions, bump these when needed ---------------------------------
# Go: distro packages (apt-get install golang-go) are too old for the anton48
# go.mod (it asks for go 1.25.5, see test/integration/Dockerfile.vkserver), so
# we install the official tarball from go.dev instead.
GO_VERSION=1.27.1
GO_SHA256_AMD64=63d339f0da5ab53635a56f2490a7984dfe12dfcff22ad749f63edaf590168445
GO_SHA256_ARM64=3450b45a3f9ee8568792736a5c5e70a1f2e9b36c35a8f74958c03e51d7d92bec

# anton48/vk-turn-proxy, branch add-server-srtp-layer, pinned to a known-good
# commit (the one test/integration/ was validated against). Looked up with:
#   git ls-remote https://github.com/anton48/vk-turn-proxy add-server-srtp-layer
VKPROXY_BRANCH=add-server-srtp-layer
VKPROXY_COMMIT=37d6607f5c8f3bc4c84595874c6dafa0f5b1d847
# -------------------------------------------------------------------------

if [ "$(id -u)" -ne 0 ]; then
	echo "run as root" >&2
	exit 1
fi
if ! [ -f /etc/debian_version ]; then
	echo "this script targets Debian/Ubuntu" >&2
	exit 1
fi

echo "==> installing packages"
apt-get update
apt-get install -y wireguard wireguard-tools git iptables curl iperf3

echo "==> installing Go $GO_VERSION"
case "$(uname -m)" in
x86_64) GOARCH=amd64; GOSHA=$GO_SHA256_AMD64 ;;
aarch64) GOARCH=arm64; GOSHA=$GO_SHA256_ARM64 ;;
*)
	echo "unsupported arch: $(uname -m)" >&2
	exit 1
	;;
esac
if [ "$(/usr/local/go/bin/go version 2>/dev/null | awk '{print $3}')" != "go$GO_VERSION" ]; then
	TARBALL="go${GO_VERSION}.linux-${GOARCH}.tar.gz"
	TMP=$(mktemp -d)
	curl -fsSL "https://go.dev/dl/${TARBALL}" -o "$TMP/$TARBALL"
	echo "${GOSHA}  $TMP/$TARBALL" | sha256sum -c -
	rm -rf /usr/local/go
	tar -C /usr/local -xzf "$TMP/$TARBALL"
	rm -rf "$TMP"
else
	echo "    already at go$GO_VERSION, skipping download"
fi
if [ ! -f /etc/profile.d/go.sh ]; then
	echo 'export PATH=$PATH:/usr/local/go/bin' >/etc/profile.d/go.sh
fi
export PATH=$PATH:/usr/local/go/bin

echo "==> building anton48/vk-turn-proxy ($VKPROXY_COMMIT)"
mkdir -p /opt/turnrelay
cd /opt/turnrelay
if [ ! -d vk-turn-proxy/.git ]; then
	git clone -b "$VKPROXY_BRANCH" https://github.com/anton48/vk-turn-proxy vk-turn-proxy
fi
(
	cd vk-turn-proxy
	git fetch origin "$VKPROXY_BRANCH"
	git checkout -q "$VKPROXY_COMMIT"
)
(cd vk-turn-proxy && go build -o /opt/turnrelay/server ./server)

echo "==> WireGuard keys"
umask 077
[ -f server.key ] || { wg genkey >server.key; wg pubkey <server.key >server.pub; }
[ -f client.key ] || { wg genkey >client.key; wg pubkey <client.key >client.pub; }

echo "==> WireGuard interface wg0"
IFACE=$(ip -o -4 route show to default | awk '{print $5; exit}')
cat >/etc/wireguard/wg0.conf <<EOC
[Interface]
Address = 10.8.0.1/24
ListenPort = $WGPORT
PrivateKey = $(cat server.key)
PostUp = iptables -I FORWARD 1 -i wg0 -j ACCEPT; iptables -I FORWARD 1 -o wg0 -j ACCEPT
PostDown = iptables -D FORWARD -i wg0 -j ACCEPT; iptables -D FORWARD -o wg0 -j ACCEPT
PostUp = iptables -t nat -A POSTROUTING -s 10.8.0.0/24 -o $IFACE -j MASQUERADE
PostDown = iptables -t nat -D POSTROUTING -s 10.8.0.0/24 -o $IFACE -j MASQUERADE
# Keep the WireGuard port itself off the public internet: only the anton48
# server, over loopback, ever needs to reach it. A publicly reachable
# WireGuard port is trivially fingerprintable and would defeat the SRTP
# disguise, so drop anything for this port that did not arrive on lo.
PostUp = iptables -I INPUT 1 -p udp --dport $WGPORT ! -i lo -j DROP
PostUp = ip6tables -I INPUT 1 -p udp --dport $WGPORT ! -i lo -j DROP
PostDown = iptables -D INPUT -p udp --dport $WGPORT ! -i lo -j DROP
PostDown = ip6tables -D INPUT -p udp --dport $WGPORT ! -i lo -j DROP
[Peer]
PublicKey = $(cat client.pub)
AllowedIPs = 10.8.0.2/32
EOC
chmod 600 /etc/wireguard/wg0.conf
sysctl -w net.ipv4.ip_forward=1
grep -q '^net.ipv4.ip_forward' /etc/sysctl.conf 2>/dev/null || echo 'net.ipv4.ip_forward=1' >>/etc/sysctl.conf
systemctl enable --now wg-quick@wg0

echo "==> turnrelay-server systemd unit"
cat >/etc/systemd/system/turnrelay-server.service <<EOC
[Unit]
Description=vk-turn-proxy SRTP server
After=network.target wg-quick@wg0.service
Requires=wg-quick@wg0.service

[Service]
ExecStart=/opt/turnrelay/server -listen 0.0.0.0:$PXPORT -connect 127.0.0.1:$WGPORT -srtp -uplink-reseq 100ms
Restart=always
RestartSec=5
# The server parses DTLS/SRTP from any host on the public proxy port; keep a
# bug in it from becoming root on the box that holds the WireGuard key.
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
EOC
systemctl daemon-reload
systemctl enable --now turnrelay-server

echo "==> firewall"
# Only the proxy port is public; the WireGuard port stays loopback-only via
# the iptables INPUT DROP rule in wg0.conf's PostUp/PostDown above.
if command -v ufw >/dev/null 2>&1 && ufw status | grep -q "Status: active"; then
	ufw allow "$PXPORT"/udp
fi

echo
echo "=================================================================="
echo "server public key:   $(cat server.pub)"
echo "client private key:  $(cat client.key)"
echo "proxy endpoint:      $PUB:$PXPORT"
echo "wg client address:   10.8.0.2/32"
echo "wg server address:   10.8.0.1"
echo "firewall:            $PXPORT/udp public, $WGPORT/udp loopback-only (iptables DROP)"
echo
echo "anton48 commit:      $VKPROXY_COMMIT"
echo "go version:          $GO_VERSION"
echo "=================================================================="
echo "Use the client private key and server public key above to fill in"
echo "wg-vk.conf on the operator laptop. See docs/e2e.md."
