#!/usr/bin/env bash
set -eo pipefail

REPO="Robinproxy/Needle"
INSTALL_DIR="/opt/needle"
BIN_DIR="$INSTALL_DIR/bin"
DATA_DIR="$INSTALL_DIR/data"
ENV_FILE="$INSTALL_DIR/.env"
SERVICE_NAME="needle-server"

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

echo "Downloading needle-server $VERSION ($ARCH)..."
curl -sL "$DOWNLOAD_URL" | tar xz -C "$TMP_DIR"

# Create directories
mkdir -p "$BIN_DIR" "$DATA_DIR"

# Install binary
cp "$TMP_DIR/needle-server" "$BIN_DIR/"
chmod +x "$BIN_DIR/needle-server"

# Interactive config
DEFAULT_LISTEN=":8008"
read -rp "Listen address [${DEFAULT_LISTEN}]: " LISTEN < /dev/tty
LISTEN="${LISTEN:-$DEFAULT_LISTEN}"

DEFAULT_TOKEN=$(head -c 32 /dev/urandom | xxd -p | head -c 32)
read -rp "Server token (enter for random) [${DEFAULT_TOKEN}]: " TOKEN < /dev/tty
TOKEN="${TOKEN:-$DEFAULT_TOKEN}"

cat > "$ENV_FILE" <<EOF
NEEDLE_LISTEN=${LISTEN}
NEEDLE_TOKEN=${TOKEN}
EOF
chmod 600 "$ENV_FILE"

echo "Installing systemd service..."
cat > "/etc/systemd/system/${SERVICE_NAME}.service" <<UNIT
[Unit]
Description=Needle Server
After=network.target

[Service]
Type=simple
ExecStart=${BIN_DIR}/needle-server -l \${NEEDLE_LISTEN}
EnvironmentFile=${ENV_FILE}
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
UNIT

systemctl daemon-reload
systemctl enable --now "$SERVICE_NAME"

echo
echo "========================================="
echo " Needle Server installed!"
echo " Version:   $VERSION"
echo " Dashboard: http://$(curl -s ifconfig.me 2>/dev/null || echo 'localhost')$(echo $LISTEN | sed 's/^://')"
echo " Token:     $TOKEN"
echo " Config:    $ENV_FILE"
echo " Data:      $DATA_DIR"
echo "========================================="
echo "To view logs: journalctl -u $SERVICE_NAME -f"
