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

if [ ! -f "$INSTALL_DIR/agent.yaml" ]; then
  echo "Needle Agent is not installed. Please run install-agent.sh first."
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
  echo "Failed to fetch latest release version."
  exit 1
fi
echo "Latest version: $VERSION"

DOWNLOAD_URL="https://github.com/$REPO/releases/download/$VERSION/needle-linux-$GOARCH.tar.gz"
CHECKSUM_URL="${DOWNLOAD_URL}.sha256"
TMP_DIR=$(mktemp -d)
trap "rm -rf $TMP_DIR" EXIT

echo "Downloading needle-agent $VERSION ($ARCH)..."
curl -sL "$DOWNLOAD_URL" -o "$TMP_DIR/needle-linux-$GOARCH.tar.gz"

# SHA256 checksum verification
echo "Verifying checksum..."
EXPECTED_CHECKSUM=$(curl -sL "$CHECKSUM_URL" | awk '{print $1}')
if [ -n "$EXPECTED_CHECKSUM" ]; then
  ACTUAL_CHECKSUM=$(sha256sum "$TMP_DIR/needle-linux-$GOARCH.tar.gz" | awk '{print $1}')
  if [ "$EXPECTED_CHECKSUM" != "$ACTUAL_CHECKSUM" ]; then
    echo "ERROR: Checksum verification failed!"
    echo "  Expected: $EXPECTED_CHECKSUM"
    echo "  Actual:   $ACTUAL_CHECKSUM"
    echo "The downloaded file may be tampered with. Aborting upgrade."
    exit 1
  fi
  echo "Checksum verified successfully."
else
  echo "WARNING: Could not fetch checksum. Skipping verification."
fi

tar xzf "$TMP_DIR/needle-linux-$GOARCH.tar.gz" -C "$TMP_DIR"

# Stop service
echo "Stopping $SERVICE_NAME..."
systemctl stop "$SERVICE_NAME" 2>/dev/null || true

# Install new binary
echo "Installing new binary..."
mkdir -p "$BIN_DIR"
cp "$TMP_DIR/needle-agent" "$BIN_DIR/"
chmod +x "$BIN_DIR/needle-agent"

# Update systemd service unit with security hardening
echo "Updating systemd service..."
cat > "/etc/systemd/system/${SERVICE_NAME}.service" <<UNIT
[Unit]
Description=Needle Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=${BIN_DIR}/needle-agent ${INSTALL_DIR}/agent.yaml
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
echo " Needle Agent upgraded to $VERSION!"
echo " Config preserved: $INSTALL_DIR/agent.yaml"
echo "========================================="
echo "To view logs: journalctl -u $SERVICE_NAME -f"