#!/usr/bin/env bash
set -euo pipefail

REPO="Robinproxy/Needle"
INSTALL_DIR="/opt/needle-agent"
BIN_DIR="$INSTALL_DIR/bin"
SERVICE_NAME="needle-agent"

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
  read -rp "Version (e.g. v0.1.0): " VERSION
fi

DOWNLOAD_URL="https://github.com/$REPO/releases/download/$VERSION/needle-linux-$GOARCH.tar.gz"
TMP_DIR=$(mktemp -d)
trap "rm -rf $TMP_DIR" EXIT

echo "Downloading needle-agent $VERSION ($ARCH)..."
curl -sL "$DOWNLOAD_URL" | tar xz -C "$TMP_DIR"

# Create directories
mkdir -p "$BIN_DIR"

# Install binary
cp "$TMP_DIR/needle-agent" "$BIN_DIR/"
chmod +x "$BIN_DIR/needle-agent"

# Interactive config
DEFAULT_HOSTNAME=$(hostname)
read -rp "Hostname [${DEFAULT_HOSTNAME}]: " HOSTNAME
HOSTNAME="${HOSTNAME:-$DEFAULT_HOSTNAME}"

read -rp "Region (ISO country code, e.g. CN/SG/US) [SG]: " REGION
REGION="${REGION:-SG}"

read -rp "Server URL (e.g. https://needle.example.com): " SERVER_URL
while [ -z "$SERVER_URL" ]; do
  read -rp "Server URL is required: " SERVER_URL
done

read -rp "Server token: " TOKEN
while [ -z "$TOKEN" ]; do
  read -rp "Server token is required: " TOKEN
done

read -rp "Report interval (seconds) [30]: " INTERVAL
INTERVAL="${INTERVAL:-30}"

# TCPing targets
DEFAULT_TCPING=(
  "CMv4,sh-cm-v4.ip.zstaticcdn.com:80,60"
  "CMv6,sh-cm-v6.ip.zstaticcdn.com:80,60"
  "CUv4,sh-cu-v4.ip.zstaticcdn.com:80,60"
  "CUv6,sh-cu-v6.ip.zstaticcdn.com:80,60"
  "CTv4,sh-ct-v4.ip.zstaticcdn.com:80,60"
  "CTv6,sh-ct-v6.ip.zstaticcdn.com:80,60"
)

echo
echo "TCPing targets (edit the defaults or press enter to keep):"
TCPING_ENTRIES=()
for i in "${!DEFAULT_TCPING[@]}"; do
  IFS=',' read -r NAME TARGET INTERVAL <<< "${DEFAULT_TCPING[$i]}"
  read -rp "  Target $((i+1)) name [${NAME}]: " N
  read -rp "  Target $((i+1)) address [${TARGET}]: " T
  TCPING_ENTRIES+=("${N:-$NAME},${T:-$TARGET},$INTERVAL")
done

# Generate agent.yaml
AGENT_YAML="$INSTALL_DIR/agent.yaml"
cat > "$AGENT_YAML" <<YAML
hostname: "${HOSTNAME}"
server: ${SERVER_URL}
token: ${TOKEN}
region: ${REGION}
interval: ${INTERVAL}
insecure: false
tcpping:
YAML
for entry in "${TCPING_ENTRIES[@]}"; do
  IFS=',' read -r NAME TARGET INT <<< "$entry"
  cat >> "$AGENT_YAML" <<YAML
  - name: "${NAME}"
    target: "${TARGET}"
    interval: ${INT}
YAML
done

chmod 600 "$AGENT_YAML"

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
echo " Hostname: $HOSTNAME"
echo " Region:   $REGION"
echo " Server:   $SERVER_URL"
echo " Config:   $AGENT_YAML"
echo "========================================="
echo "To view logs: journalctl -u $SERVICE_NAME -f"
