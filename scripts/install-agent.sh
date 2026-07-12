#!/usr/bin/env bash
set -eo pipefail

REPO="Robinproxy/Needle"
INSTALL_DIR="/opt/needle-agent"
BIN_DIR="$INSTALL_DIR/bin"
SERVICE_NAME="needle-agent"

if [ "$(id -u)" -ne 0 ]; then
  echo "Please run as root (sudo)."
  exit 1
fi

# Prefer real TTY for interactive prompts (works with curl|bash)
if [ -c /dev/tty ]; then
  exec </dev/tty
elif [ ! -t 0 ]; then
  echo "No interactive TTY available."
  echo "Download then run:"
  echo "  curl -fsSL https://raw.githubusercontent.com/$REPO/main/scripts/install-agent.sh -o /tmp/needle-install-agent.sh"
  echo "  sudo bash /tmp/needle-install-agent.sh"
  exit 1
fi

# Detect arch
ARCH=$(uname -m)
case "$ARCH" in
  x86_64|amd64)   GOARCH="amd64" ;;
  aarch64|arm64)  GOARCH="arm64" ;;
  *)              echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

file_sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    echo "ERROR: need sha256sum or shasum" >&2
    exit 1
  fi
}

yaml_value() {
  # Read first non-comment key: value from yaml (no grep -P)
  local key="$1" file="$2"
  sed -n "s/^${key}:[[:space:]]*//p" "$file" 2>/dev/null | head -1 | sed 's/^["'\'']//;s/["'\'']$//' | tr -d '\r'
}

fetch_latest_version() {
  local ver
  ver=$(curl -fsSLI -o /dev/null -w '%{url_effective}' \
    "https://github.com/$REPO/releases/latest" 2>/dev/null | sed 's#.*/##')
  if [ -n "$ver" ] && [ "$ver" != "latest" ]; then
    echo "$ver"
    return 0
  fi
  ver=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null \
    | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)
  if [ -n "$ver" ]; then
    echo "$ver"
    return 0
  fi
  return 1
}

echo "Fetching latest release..."
VERSION=$(fetch_latest_version || true)
if [ -z "$VERSION" ]; then
  echo "Failed to fetch latest release automatically."
  read -rp "Version (e.g. v0.4.0): " VERSION
fi
if [ -z "$VERSION" ]; then
  echo "Version is required."
  exit 1
fi
echo "Latest version: $VERSION"

DOWNLOAD_URL="https://github.com/$REPO/releases/download/$VERSION/needle-linux-$GOARCH.tar.gz"
CHECKSUM_URL="${DOWNLOAD_URL}.sha256"
TMP_DIR=$(mktemp -d)
trap "rm -rf $TMP_DIR" EXIT
TGZ="$TMP_DIR/needle-linux-$GOARCH.tar.gz"

echo "Downloading needle-agent $VERSION ($ARCH)..."
if ! curl -fsSL "$DOWNLOAD_URL" -o "$TGZ"; then
  echo "ERROR: failed to download $DOWNLOAD_URL"
  echo "Check network access to GitHub Releases."
  exit 1
fi
if [ ! -s "$TGZ" ]; then
  echo "ERROR: downloaded file is empty"
  exit 1
fi

echo "Verifying checksum..."
EXPECTED_CHECKSUM=$(curl -fsSL "$CHECKSUM_URL" 2>/dev/null | awk '{print $1}' || true)
if [ -n "$EXPECTED_CHECKSUM" ]; then
  ACTUAL_CHECKSUM=$(file_sha256 "$TGZ")
  if [ "$EXPECTED_CHECKSUM" != "$ACTUAL_CHECKSUM" ]; then
    echo "ERROR: Checksum verification failed!"
    echo "  Expected: $EXPECTED_CHECKSUM"
    echo "  Actual:   $ACTUAL_CHECKSUM"
    echo "The downloaded file may be tampered with. Aborting installation."
    exit 1
  fi
  echo "Checksum verified successfully."
else
  echo "WARNING: Could not fetch checksum. Skipping verification."
fi

tar xzf "$TGZ" -C "$TMP_DIR"
if [ ! -f "$TMP_DIR/needle-agent" ]; then
  echo "ERROR: needle-agent binary not found in archive"
  exit 1
fi

# Read old config before stopping anything
OLD_HOSTNAME=""
OLD_TOKEN=""
OLD_SERVER=""
if [ -f "$INSTALL_DIR/agent.yaml" ]; then
  OLD_HOSTNAME=$(yaml_value hostname "$INSTALL_DIR/agent.yaml")
  OLD_TOKEN=$(yaml_value token "$INSTALL_DIR/agent.yaml")
  OLD_SERVER=$(yaml_value server "$INSTALL_DIR/agent.yaml")
fi

systemctl stop "$SERVICE_NAME" 2>/dev/null || true

mkdir -p "$BIN_DIR"
rm -f "$BIN_DIR/needle-agent"
cp "$TMP_DIR/needle-agent" "$BIN_DIR/"
chmod +x "$BIN_DIR/needle-agent"

# Interactive config
read -rp "Hostname (leave empty for auto-detection) []: " HOSTNAME
HOSTNAME="${HOSTNAME:-}"

read -rp "Region (ISO country code, e.g. CN/SG/US) [SG]: " REGION
REGION="${REGION:-SG}"

read -rp "Server URL [http://127.0.0.1:8008]: " SERVER_URL
SERVER_URL="${SERVER_URL:-http://127.0.0.1:8008}"

read -rp "Server token: " TOKEN
while [ -z "$TOKEN" ]; do
  read -rp "Server token is required: " TOKEN
done

read -rp "Report interval (seconds) [30]: " INTERVAL
INTERVAL="${INTERVAL:-30}"

TCPING_DEFAULTS="CMv4 sh-cm-v4.ip.zstaticcdn.com:80 CMv6 sh-cm-v6.ip.zstaticcdn.com:80 CUv4 sh-cu-v4.ip.zstaticcdn.com:80 CUv6 sh-cu-v6.ip.zstaticcdn.com:80 CTv4 sh-ct-v4.ip.zstaticcdn.com:80 CTv6 sh-ct-v6.ip.zstaticcdn.com:80"

echo
echo "TCPing targets (edit the defaults or press enter to keep):"
set -- $TCPING_DEFAULTS
N1="$1"; T1="$2"; N3="$3"; T3="$4"; N5="$5"; T5="$6"; N7="$7"; T7="$8"; N9="$9"; T9="${10}"; N11="${11}"; T11="${12}"
read -rp "  Target 1 name [${N1}]: " V; N1="${V:-$N1}"
read -rp "  Target 1 address [${T1}]: " V; T1="${V:-$T1}"
read -rp "  Target 2 name [${N3}]: " V; N3="${V:-$N3}"
read -rp "  Target 2 address [${T3}]: " V; T3="${V:-$T3}"
read -rp "  Target 3 name [${N5}]: " V; N5="${V:-$N5}"
read -rp "  Target 3 address [${T5}]: " V; T5="${V:-$T5}"
read -rp "  Target 4 name [${N7}]: " V; N7="${V:-$N7}"
read -rp "  Target 4 address [${T7}]: " V; T7="${V:-$T7}"
read -rp "  Target 5 name [${N9}]: " V; N9="${V:-$N9}"
read -rp "  Target 5 address [${T9}]: " V; T9="${V:-$T9}"
read -rp "  Target 6 name [${N11}]: " V; N11="${V:-$N11}"
read -rp "  Target 6 address [${T11}]: " V; T11="${V:-$T11}"

# VPS billing setup
echo
echo "VPS billing setup (for dashboard expiry countdown and traffic reset):"
echo "  1) Monthly (1m)"
echo "  2) Quarterly (3m)"
echo "  3) Semi-annual (6m)"
echo "  4) Annual (12m)"
echo "  0) Skip"
read -rp "Select billing period [0]: " PERIOD_CHOICE

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
  read -rp "Next renewal date [${DEFAULT_EXPIRY}]: " EXPIRES_AT
  EXPIRES_AT="${EXPIRES_AT:-$DEFAULT_EXPIRY}"
fi

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
  - name: "${N3}"
    target: "${T3}"
  - name: "${N5}"
    target: "${T5}"
  - name: "${N7}"
    target: "${T7}"
  - name: "${N9}"
    target: "${T9}"
  - name: "${N11}"
    target: "${T11}"
YAML

chmod 600 "$AGENT_YAML"

# Auto-unregister old hostname if it changed
if [ -n "$OLD_HOSTNAME" ] && [ "$OLD_HOSTNAME" != "$HOSTNAME" ] && [ -n "$OLD_TOKEN" ] && [ -n "$OLD_SERVER" ]; then
  echo "Hostname changed: '$OLD_HOSTNAME' → '$HOSTNAME'"
  echo "Unregistering old agent from server..."
  curl -fsS -X POST "$OLD_SERVER/api/unregister" \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer $OLD_TOKEN" \
    --data-binary "{\"hostname\":\"$OLD_HOSTNAME\"}" >/dev/null 2>&1 || true
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

# Security hardening
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=${INSTALL_DIR}
RestrictNamespaces=true
LockPersonality=true
RestrictRealtime=true
RestrictSUIDSGID=true
RemoveIPC=true
CapabilityBoundingSet=
AmbientCapabilities=

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
