#!/usr/bin/env bash
set -eo pipefail

REPO="Robinproxy/Needle"
INSTALL_DIR="/opt/needle"
BIN_DIR="$INSTALL_DIR/bin"
DATA_DIR="$INSTALL_DIR/data"
ENV_FILE="$INSTALL_DIR/.env"
SERVICE_NAME="needle-server"
SERVER_BIN="$BIN_DIR/needle-server"
DB_PATH="$DATA_DIR/needle.db"
SCRIPT_URL="https://raw.githubusercontent.com/$REPO/main/scripts/needle-server.sh"

usage() {
  cat <<EOF
Needle Server ops script (binary / systemd)

Usage:
  sudo bash needle-server.sh [command] [options]

Commands:
  install              Interactive install (download + .env + systemd)
  upgrade              Upgrade binary only (keep .env and data/)
  uninstall            Remove service + binary; keep data/ and .env (default)
  uninstall --purge    Also remove entire $INSTALL_DIR (data + .env)
  status               Show install/service status
  help                 Show this help

No command:
  If installed → upgrade, else → install

Options:
  -y, --yes            Skip uninstall confirmation
  --purge              With uninstall: delete data and config too

Node list/delete (not part of this script):
  sudo $SERVER_BIN -db $DB_PATH list-agents
  sudo $SERVER_BIN -db $DB_PATH delete-agent <hostname|id>

Examples:
  sudo bash needle-server.sh
  sudo bash needle-server.sh install
  sudo bash needle-server.sh upgrade
  sudo bash needle-server.sh uninstall
  sudo bash needle-server.sh uninstall --purge
EOF
}

require_root() {
  if [ "$(id -u)" -ne 0 ]; then
    echo "Please run as root (sudo)."
    exit 1
  fi
}

prefer_tty() {
  if [ -c /dev/tty ]; then
    exec </dev/tty
  fi
}

require_tty_for_install() {
  if [ -c /dev/tty ]; then
    exec </dev/tty
  elif [ ! -t 0 ]; then
    echo "No interactive TTY available for install."
    echo "Download then run:"
    echo "  curl -fsSL $SCRIPT_URL -o /tmp/needle-server.sh"
    echo "  # or: wget -qO /tmp/needle-server.sh $SCRIPT_URL"
    echo "  sudo bash /tmp/needle-server.sh install"
    exit 1
  fi
}

is_installed() {
  [ -x "$SERVER_BIN" ] || [ -f "$ENV_FILE" ] || systemctl cat "$SERVICE_NAME" >/dev/null 2>&1
}

detect_arch() {
  ARCH=$(uname -m)
  case "$ARCH" in
    x86_64|amd64)   GOARCH="amd64" ;;
    aarch64|arm64)  GOARCH="arm64" ;;
    *)              echo "Unsupported architecture: $ARCH"; exit 1 ;;
  esac
}

http_get() {
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
  if [ -n "$ver" ] && [ "$ver" != "latest" ]; then
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

download_release_server() {
  local mode="${1:-}"
  detect_arch
  echo "Fetching latest release..."
  VERSION=$(fetch_latest_version || true)
  if [ -z "$VERSION" ]; then
    if [ "$mode" = "interactive" ]; then
      echo "Failed to fetch latest release automatically."
      read -rp "Version (e.g. v0.4.0): " VERSION
    else
      echo "Failed to fetch latest release version."
      echo "Check network access to GitHub, or install manually from Releases."
      exit 1
    fi
  fi
  if [ -z "$VERSION" ]; then
    echo "Version is required."
    exit 1
  fi
  echo "Latest version: $VERSION"

  DOWNLOAD_URL="https://github.com/$REPO/releases/download/$VERSION/needle-linux-$GOARCH.tar.gz"
  CHECKSUM_URL="${DOWNLOAD_URL}.sha256"
  TMP_DIR=$(mktemp -d)
  # shellcheck disable=SC2064
  trap "rm -rf '$TMP_DIR'" EXIT
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
      echo "The downloaded file may be tampered with. Aborting."
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
}

write_systemd_unit() {
  cat > "/etc/systemd/system/${SERVICE_NAME}.service" <<UNIT
[Unit]
Description=Needle Server
After=network.target

[Service]
Type=simple
ExecStart=${SERVER_BIN} -l \${NEEDLE_LISTEN} -db ${DB_PATH}
EnvironmentFile=${ENV_FILE}
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
UNIT
}

cmd_install() {
  require_root
  require_tty_for_install
  download_release_server interactive

  mkdir -p "$BIN_DIR" "$DATA_DIR"
  cp "$TMP_DIR/needle-server" "$SERVER_BIN"
  chmod +x "$SERVER_BIN"

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
  write_systemd_unit
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
  echo
  echo "List/delete agents:"
  echo "  $SERVER_BIN -db $DB_PATH list-agents"
  echo "  $SERVER_BIN -db $DB_PATH delete-agent <hostname|id>"
}

cmd_upgrade() {
  require_root
  if ! is_installed; then
    echo "Needle Server is not installed."
    echo "Run: sudo bash needle-server.sh install"
    exit 1
  fi
  if [ ! -f "$ENV_FILE" ]; then
    echo "WARNING: $ENV_FILE missing; upgrade will install binary only."
  fi

  download_release_server

  echo "Stopping $SERVICE_NAME..."
  systemctl stop "$SERVICE_NAME" 2>/dev/null || true

  echo "Installing new binary..."
  mkdir -p "$BIN_DIR" "$DATA_DIR"
  cp "$TMP_DIR/needle-server" "$SERVER_BIN"
  chmod +x "$SERVER_BIN"

  if [ -f "$ENV_FILE" ]; then
    echo "Updating systemd unit (config preserved)..."
    write_systemd_unit
  fi

  systemctl daemon-reload
  if [ -f "$ENV_FILE" ]; then
    systemctl enable --now "$SERVICE_NAME"
  else
    echo "WARNING: no $ENV_FILE; service not started."
  fi

  echo
  echo "========================================="
  echo " Needle Server upgraded to $VERSION!"
  echo " Config preserved: $ENV_FILE"
  echo " Data preserved:   $DATA_DIR"
  echo "========================================="
  echo "To view logs: journalctl -u $SERVICE_NAME -f"
}

cmd_uninstall() {
  require_root
  prefer_tty

  local purge=false yes=false
  while [ $# -gt 0 ]; do
    case "$1" in
      --purge) purge=true; shift ;;
      -y|--yes) yes=true; shift ;;
      -h|--help) usage; exit 0 ;;
      *) echo "Unknown option: $1"; usage; exit 1 ;;
    esac
  done

  if [ "$yes" != true ]; then
    echo "This will stop needle-server and remove the service unit + binary."
    if [ "$purge" = true ]; then
      echo "Also DELETE entire $INSTALL_DIR (including data and .env)."
    else
      echo "Data kept: $DATA_DIR and $ENV_FILE (use --purge to remove all)."
    fi
    read -rp "Continue? [y/N] " ans
    case "$ans" in
      y|Y|yes|YES) ;;
      *) echo "aborted"; exit 0 ;;
    esac
  fi

  echo "Stopping service..."
  systemctl stop "$SERVICE_NAME" 2>/dev/null || true
  systemctl disable "$SERVICE_NAME" 2>/dev/null || true
  rm -f "/etc/systemd/system/${SERVICE_NAME}.service"
  systemctl daemon-reload 2>/dev/null || true

  if [ "$purge" = true ]; then
    echo "Removing $INSTALL_DIR ..."
    rm -rf "$INSTALL_DIR"
  else
    echo "Removing binary..."
    rm -f "$SERVER_BIN"
    rmdir "$BIN_DIR" 2>/dev/null || true
  fi

  echo
  echo "========================================="
  echo " Needle Server uninstalled."
  if [ "$purge" = true ]; then
    echo " Data and config removed."
  else
    echo " Data kept at: $DATA_DIR"
    echo " Config kept:  $ENV_FILE"
  fi
  echo "========================================="
}

cmd_status() {
  echo "Install dir: $INSTALL_DIR"
  if [ -x "$SERVER_BIN" ]; then
    echo "Binary:      $SERVER_BIN (present)"
  else
    echo "Binary:      missing"
  fi
  if [ -f "$ENV_FILE" ]; then
    echo "Config:      $ENV_FILE (present)"
    # show listen only, never print token value
    if grep -q '^NEEDLE_LISTEN=' "$ENV_FILE" 2>/dev/null; then
      echo "  listen:    $(sed -n 's/^NEEDLE_LISTEN=//p' "$ENV_FILE" | head -1)"
    fi
    if grep -q '^NEEDLE_TOKEN=' "$ENV_FILE" 2>/dev/null; then
      echo "  token:     (set)"
    else
      echo "  token:     (missing)"
    fi
  else
    echo "Config:      missing"
  fi
  if [ -f "$DB_PATH" ]; then
    echo "Database:    $DB_PATH (present)"
  else
    echo "Database:    $DB_PATH (missing)"
  fi
  if command -v systemctl >/dev/null 2>&1; then
    if systemctl cat "$SERVICE_NAME" >/dev/null 2>&1; then
      echo "Service:     installed"
      systemctl is-active "$SERVICE_NAME" 2>/dev/null | sed 's/^/  active:     /' || true
      systemctl is-enabled "$SERVICE_NAME" 2>/dev/null | sed 's/^/  enabled:    /' || true
    else
      echo "Service:     not installed"
    fi
  fi
  echo
  echo "Agent ops (if installed):"
  echo "  $SERVER_BIN -db $DB_PATH list-agents"
  echo "  $SERVER_BIN -db $DB_PATH delete-agent <hostname|id>"
}

# --- main ---
CMD=""
case "${1:-}" in
  install|upgrade|uninstall|status|help|-h|--help)
    CMD="$1"
    shift
    ;;
  "")
    if is_installed; then
      CMD="upgrade"
    else
      CMD="install"
    fi
    ;;
  *)
    echo "Unknown command: $1"
    usage
    exit 1
    ;;
esac

case "$CMD" in
  install)   cmd_install ;;
  upgrade)   cmd_upgrade ;;
  uninstall) cmd_uninstall "$@" ;;
  status)    cmd_status ;;
  help|-h|--help) usage ;;
esac
