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
  echo "Failed to fetch latest release version."
  echo "Check network access to GitHub, or install manually from Releases."
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
    echo "The downloaded file may be tampered with. Aborting upgrade."
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

echo "Stopping $SERVICE_NAME..."
systemctl stop "$SERVICE_NAME" 2>/dev/null || true

echo "Installing new binary..."
mkdir -p "$BIN_DIR"
cp "$TMP_DIR/needle-agent" "$BIN_DIR/"
chmod +x "$BIN_DIR/needle-agent"

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
