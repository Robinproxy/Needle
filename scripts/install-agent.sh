#!/usr/bin/env bash
set -eo pipefail

REPO="Robinproxy/Needle"
INSTALL_DIR="/opt/needle-agent"
BIN_DIR="$INSTALL_DIR/bin"
SERVICE_NAME="needle-agent"

TTY="/dev/tty"
if [ ! -c "$TTY" ]; then
  echo "No tty available. Run interactively (not from pipe)." >&2
  exit 1
fi

if [ "$(id -u)" -ne 0 ]; then
  echo "Please run as root (sudo)."
  exit 1
fi

# Detect arch
ARCH=$(uname -m)
case "$ARCH" in
  x86_64)  GOARCH="amd64" ;;
  aarch64) GOARCH="arm64" ;;
  *)       echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

# Get latest release version
echo "Fetching latest release..."
VERSION=$(curl -sL "https://api.github.com/repos/$REPO/releases/latest" | grep '"tag_name"' | cut -d'"' -f4)
if [ -z "$VERSION" ]; then
  echo "Failed to fetch latest release. Set manually:"
  read -rp "Version (e.g. v0.1.0): " VERSION < /dev/tty
fi

DOWNLOAD_URL="https://github.com/$REPO/releases/download/$VERSION/needle-linux-$GOARCH.tar.gz"
TMP_DIR=$(mktemp -d)
trap "rm -rf $TMP_DIR" EXIT

echo "Downloading needle-agent $VERSION ($ARCH)..."
curl -sL "$DOWNLOAD_URL" | tar xz -C "$TMP_DIR"

# Read old config before stopping anything
OLD_HOSTNAME=""
OLD_TOKEN=""
OLD_SERVER=""
if [ -f "$INSTALL_DIR/agent.yaml" ]; then
    OLD_HOSTNAME=$(grep -oP '^hostname:\s*"\K[^"]+' "$INSTALL_DIR/agent.yaml" 2>/dev/null || grep -oP '^hostname:\s*\K\S+' "$INSTALL_DIR/agent.yaml" 2>/dev/null || true)
    OLD_TOKEN=$(grep -oP '^token:\s*\K\S+' "$INSTALL_DIR/agent.yaml" 2>/dev/null || true)
    OLD_SERVER=$(grep -oP '^server:\s*\K\S+' "$INSTALL_DIR/agent.yaml" 2>/dev/null || true)
fi

# Stop old service and remove old binary
systemctl stop "$SERVICE_NAME" 2>/dev/null || true

mkdir -p "$BIN_DIR"
rm -f "$BIN_DIR/needle-agent"

# Install binary
cp "$TMP_DIR/needle-agent" "$BIN_DIR/"
chmod +x "$BIN_DIR/needle-agent"

# Interactive config
read -rp "Hostname (leave empty for auto-detection) []: " HOSTNAME < /dev/tty
HOSTNAME="${HOSTNAME:-}"

read -rp "Region (ISO country code, e.g. CN/SG/US) [SG]: " REGION < /dev/tty
REGION="${REGION:-SG}"

read -rp "Server URL [http://127.0.0.1:8008]: " SERVER_URL < /dev/tty
SERVER_URL="${SERVER_URL:-http://127.0.0.1:8008}"

read -rp "Server token: " TOKEN < /dev/tty
while [ -z "$TOKEN" ]; do
  read -rp "Server token is required: " TOKEN < /dev/tty
done

read -rp "Report interval (seconds) [30]: " INTERVAL < /dev/tty
INTERVAL="${INTERVAL:-30}"

TCPING_DEFAULTS="CMv4 sh-cm-v4.ip.zstaticcdn.com:80 CMv6 sh-cm-v6.ip.zstaticcdn.com:80 CUv4 sh-cu-v4.ip.zstaticcdn.com:80 CUv6 sh-cu-v6.ip.zstaticcdn.com:80 CTv4 sh-ct-v4.ip.zstaticcdn.com:80 CTv6 sh-ct-v6.ip.zstaticcdn.com:80"

echo
echo "TCPing targets (edit the defaults or press enter to keep):"
set -- $TCPING_DEFAULTS
N1="$1"; T1="$2"; N3="$3"; T3="$4"; N5="$5"; T5="$6"; N7="$7"; T7="$8"; N9="$9"; T9="${10}"; N11="${11}"; T11="${12}"
read -rp "  Target 1 name [${N1}]: " V < /dev/tty; N1="${V:-$N1}"
read -rp "  Target 1 address [${T1}]: " V < /dev/tty; T1="${V:-$T1}"
read -rp "  Target 2 name [${N3}]: " V < /dev/tty; N3="${V:-$N3}"
read -rp "  Target 2 address [${T3}]: " V < /dev/tty; T3="${V:-$T3}"
read -rp "  Target 3 name [${N5}]: " V < /dev/tty; N5="${V:-$N5}"
read -rp "  Target 3 address [${T5}]: " V < /dev/tty; T5="${V:-$T5}"
read -rp "  Target 4 name [${N7}]: " V < /dev/tty; N7="${V:-$N7}"
read -rp "  Target 4 address [${T7}]: " V < /dev/tty; T7="${V:-$T7}"
read -rp "  Target 5 name [${N9}]: " V < /dev/tty; N9="${V:-$N9}"
read -rp "  Target 5 address [${T9}]: " V < /dev/tty; T9="${V:-$T9}"
read -rp "  Target 6 name [${N11}]: " V < /dev/tty; N11="${V:-$N11}"
read -rp "  Target 6 address [${T11}]: " V < /dev/tty; T11="${V:-$T11}"

# VPS billing setup
echo
echo "VPS billing setup (for dashboard expiry countdown and traffic reset):"
echo "  1) Monthly (1m)"
echo "  2) Quarterly (3m)"
echo "  3) Semi-annual (6m)"
echo "  4) Annual (12m)"
echo "  0) Skip"
read -rp "Select billing period [0]: " PERIOD_CHOICE < /dev/tty

EXPIRES_AT=""
BILLING_PERIOD=""
case "$PERIOD_CHOICE" in
  1) BILLING_PERIOD="1m";  DEFAULT_EXPIRY=$(date -d "+1 month" +%Y-%m-%d 2>/dev/null || date -v+1m +%Y-%m-%d) ;;
  2) BILLING_PERIOD="3m";  DEFAULT_EXPIRY=$(date -d "+3 months" +%Y-%m-%d 2>/dev/null || date -v+3m +%Y-%m-%d) ;;
  3) BILLING_PERIOD="6m";  DEFAULT_EXPIRY=$(date -d "+6 months" +%Y-%m-%d 2>/dev/null || date -v+6m +%Y-%m-%d) ;;
  4) BILLING_PERIOD="12m"; DEFAULT_EXPIRY=$(date -d "+1 year" +%Y-%m-%d 2>/dev/null || date -v+1y +%Y-%m-%d) ;;
  *) BILLING_PERIOD="" ;;
esac

if [ -n "$BILLING_PERIOD" ]; then
    read -rp "Next renewal date [${DEFAULT_EXPIRY}]: " EXPIRES_AT < /dev/tty
    EXPIRES_AT="${EXPIRES_AT:-$DEFAULT_EXPIRY}"
fi

# Generate agent.yaml
AGENT_YAML="$INSTALL_DIR/agent.yaml"
cat > "$AGENT_YAML" <<YAML
hostname: "${HOSTNAME}"
server: ${SERVER_URL}
token: ${TOKEN}
region: ${REGION}
billing_period: "${BILLING_PERIOD}"
expires_at: "${EXPIRES_AT}"
interval: ${INTERVAL}
insecure: false
tcpping:
  - name: "${N1}"
    target: "${T1}"
    interval: 60
  - name: "${N3}"
    target: "${T3}"
    interval: 60
  - name: "${N5}"
    target: "${T5}"
    interval: 60
  - name: "${N7}"
    target: "${T7}"
    interval: 60
  - name: "${N9}"
    target: "${T9}"
    interval: 60
  - name: "${N11}"
    target: "${T11}"
    interval: 60
YAML

chmod 600 "$AGENT_YAML"

# Auto-unregister old hostname if it changed
if [ -n "$OLD_HOSTNAME" ] && [ "$OLD_HOSTNAME" != "$HOSTNAME" ] && [ -n "$OLD_TOKEN" ] && [ -n "$OLD_SERVER" ]; then
    echo "Hostname changed: '$OLD_HOSTNAME' → '$HOSTNAME'"
    echo "Unregistering old agent from server..."
    curl -s -X POST "$OLD_SERVER/api/unregister" \
        -H "Content-Type: application/json" \
        -d "{\"hostname\":\"$OLD_HOSTNAME\",\"token\":\"$OLD_TOKEN\"}" >/dev/null 2>&1 || true
fi

echo "Installing systemd service..."
cat > "/etc/systemd/system/${SERVICE_NAME}.service" <<UNIT
[Unit]
Description=Needle Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=${BIN_DIR}/needle-agent ${AGENT_YAML}
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
UNIT

systemctl daemon-reload
systemctl enable --now "$SERVICE_NAME"

echo
echo "========================================="
echo " Needle Agent installed!"
echo " Version:  $VERSION"
echo " Hostname: ${HOSTNAME:-auto-detected}"
echo " Region:   $REGION"
echo " Server:   $SERVER_URL"
echo " Config:   $AGENT_YAML"
echo "========================================="
echo "To view logs: journalctl -u $SERVICE_NAME -f"
