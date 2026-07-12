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

# Prefer real TTY for interactive prompts (works with curl|bash)
if [ -c /dev/tty ]; then
  exec </dev/tty
fi

# Detect arch
ARCH=$(uname -m)
case "$ARCH" in
  x86_64|amd64)   GOARCH="amd64" ;;
  aarch64|arm64)  GOARCH="arm64" ;;
  *)              echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

http_get() {
  # http_get URL [output_file]
  # If output_file omitted, write body to stdout.
  local url="$1" out="${2:-}"
  if command -v curl >/dev/null 2>&1; then
    if [ -n "$out" ]; then
      curl -fsSL "$url" -o "$out"
    else
      curl -fsSL "$url"
    fi
  elif command -v wget >/dev/null 2>&1; then
    if [ -n "$out" ]; then
      wget -qO "$out" "$url"
    else
      wget -qO- "$url"
    fi
  else
    echo "ERROR: need curl or wget. On Debian/Ubuntu: apt-get update && apt-get install -y curl" >&2
    exit 1
  fi
}

http_final_url() {
  local url="$1"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSLI -o /dev/null -w '%{url_effective}' "$url" 2>/dev/null || true
  elif command -v wget >/dev/null 2>&1; then
    # wget prints "Location: ..." on stderr with --max-redirect=0; follow and parse
    wget --max-redirect=0 --server-response -O /dev/null "$url" 2>&1 \
      | sed -n 's/^[Ll]ocation:[[:space:]]*//p' | tail -1 | tr -d '\r' || true
  fi
}

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

rand_token() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex 16
  elif command -v xxd >/dev/null 2>&1; then
    head -c 16 /dev/urandom | xxd -p -c 256
  else
    od -An -N16 -tx1 /dev/urandom | tr -d ' \n'
  fi
}

fetch_latest_version() {
  local ver loc
  loc=$(http_final_url "https://github.com/$REPO/releases/latest")
  ver=$(echo "$loc" | sed 's#.*/##')
  if [ -n "$ver" ] && [ "$ver" != "latest" ] && [ "$ver" != "" ]; then
    echo "$ver"
    return 0
  fi
  ver=$(http_get "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null \
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
  if [ -t 0 ]; then
    read -rp "Version (e.g. v0.4.0): " VERSION
  else
    echo "No TTY for manual version input. Download the script and run interactively."
    exit 1
  fi
fi
echo "Latest version: $VERSION"

DOWNLOAD_URL="https://github.com/$REPO/releases/download/$VERSION/needle-linux-$GOARCH.tar.gz"
CHECKSUM_URL="${DOWNLOAD_URL}.sha256"
TMP_DIR=$(mktemp -d)
trap "rm -rf $TMP_DIR" EXIT
TGZ="$TMP_DIR/needle-linux-$GOARCH.tar.gz"

echo "Downloading needle-server $VERSION ($ARCH)..."
if ! http_get "$DOWNLOAD_URL" "$TGZ"; then
  echo "ERROR: failed to download $DOWNLOAD_URL"
  echo "Check network access to GitHub Releases."
  exit 1
fi
if [ ! -s "$TGZ" ]; then
  echo "ERROR: downloaded file is empty"
  exit 1
fi

echo "Verifying checksum..."
EXPECTED_CHECKSUM=$(http_get "$CHECKSUM_URL" 2>/dev/null | awk '{print $1}' || true)
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
if [ ! -f "$TMP_DIR/needle-server" ]; then
  echo "ERROR: needle-server binary not found in archive"
  exit 1
fi

mkdir -p "$BIN_DIR" "$DATA_DIR"
cp "$TMP_DIR/needle-server" "$BIN_DIR/"
chmod +x "$BIN_DIR/needle-server"

DEFAULT_LISTEN=":8008"
read -rp "Listen address [${DEFAULT_LISTEN}]: " LISTEN
LISTEN="${LISTEN:-$DEFAULT_LISTEN}"

DEFAULT_TOKEN=$(rand_token)
read -rp "Server token (enter for random) [${DEFAULT_TOKEN}]: " TOKEN
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
echo " Dashboard: http://$(http_get "https://ifconfig.me" 2>/dev/null || echo 'localhost')$(echo "$LISTEN" | sed 's/^://')"
echo " Config:    $ENV_FILE"
echo " Data:      $DATA_DIR"
echo "========================================="
echo "To view logs: journalctl -u $SERVICE_NAME -f"
echo "Token saved to: $ENV_FILE (cat $ENV_FILE to view)"
