#!/usr/bin/env bash
set -eo pipefail

REPO="Robinproxy/Needle"
INSTALL_DIR="/opt/needle-agent"
BIN_DIR="$INSTALL_DIR/bin"
SERVICE_NAME="needle-agent"
AGENT_YAML="$INSTALL_DIR/agent.yaml"
SCRIPT_URL="https://raw.githubusercontent.com/$REPO/main/scripts/needle-agent.sh"

usage() {
  cat <<EOF
Needle Agent ops script

Usage:
  sudo bash needle-agent.sh [command] [options]

Commands:
  install              Interactive install (download + config + systemd)
  upgrade              Zero-interaction upgrade (keep agent.yaml)
  uninstall            Remove local agent only (default)
  uninstall --unregister
                       Call Server /api/unregister, then remove local agent
  status               Show install/service status
  help                 Show this help

No command:
  If agent.yaml exists → upgrade, else → install

Options:
  -y, --yes            Skip uninstall confirmation
  --unregister         With uninstall: also notify Server to delete this node

Examples:
  sudo bash needle-agent.sh
  sudo bash needle-agent.sh install
  sudo bash needle-agent.sh upgrade
  sudo bash needle-agent.sh uninstall
  sudo bash needle-agent.sh uninstall --unregister
EOF
}

require_root() {
  if [ "$(id -u)" -ne 0 ]; then
    echo "Please run as root (sudo)."
    exit 1
  fi
}

# Prompt from /dev/tty without rebinding bash stdin (curl|bash -s safe).
read_prompt() {
  local __var="$1"
  local __prompt="$2"
  local __val=""
  if [ -c /dev/tty ]; then
    IFS= read -r -p "$__prompt" __val </dev/tty || true
  elif [ -t 0 ]; then
    IFS= read -r -p "$__prompt" __val || true
  else
    echo "No interactive TTY available." >&2
    exit 1
  fi
  printf -v "$__var" '%s' "$__val"
}

require_tty_for_install() {
  if [ -c /dev/tty ] || [ -t 0 ]; then
    return 0
  fi
  echo "No interactive TTY available for install."
  echo "Download then run:"
  echo "  curl -fsSL $SCRIPT_URL -o /tmp/needle-agent.sh"
  echo "  # or: wget -qO /tmp/needle-agent.sh $SCRIPT_URL"
  echo "  sudo bash /tmp/needle-agent.sh install"
  exit 1
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

yaml_value() {
  local key="$1" file="$2"
  sed -n "s/^${key}:[[:space:]]*//p" "$file" 2>/dev/null | head -1 | sed 's/^["'\'']//;s/["'\'']$//' | tr -d '\r'
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

download_release_agent() {
  # sets VERSION, unpacks needle-agent into TMP_DIR
  detect_arch
  echo "Fetching latest release..."
  VERSION=$(fetch_latest_version || true)
  if [ -z "$VERSION" ]; then
    if [ "$1" = "interactive" ]; then
      echo "Failed to fetch latest release automatically."
      read_prompt VERSION "Version (e.g. v0.4.0): "
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

  echo "Downloading needle-agent $VERSION ($ARCH)..."
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
  if [ ! -f "$TMP_DIR/needle-agent" ]; then
    echo "ERROR: needle-agent binary not found in archive"
    exit 1
  fi
}

write_systemd_unit() {
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
}

unregister_from_server() {
  local server token hostname
  if [ ! -f "$AGENT_YAML" ]; then
    echo "WARNING: $AGENT_YAML not found, skip unregister."
    return 0
  fi
  server=$(yaml_value server "$AGENT_YAML")
  token=$(yaml_value token "$AGENT_YAML")
  hostname=$(yaml_value hostname "$AGENT_YAML")
  if [ -z "$hostname" ]; then
    hostname=$(hostname 2>/dev/null || true)
  fi
  if [ -z "$server" ] || [ -z "$token" ] || [ -z "$hostname" ]; then
    echo "WARNING: missing server/token/hostname in config, skip unregister."
    return 0
  fi

  echo "Unregistering hostname '$hostname' from $server ..."
  if command -v curl >/dev/null 2>&1; then
    if curl -fsS -X POST "$server/api/unregister" \
      -H "Content-Type: application/json" \
      -H "Authorization: Bearer $token" \
      --data-binary "{\"hostname\":\"$hostname\"}" >/dev/null; then
      echo "Unregistered from server."
    else
      echo "WARNING: unregister request failed (will still remove local agent)."
    fi
  elif command -v wget >/dev/null 2>&1; then
    if wget -qO- --method=POST \
      --header="Content-Type: application/json" \
      --header="Authorization: Bearer $token" \
      --body-data="{\"hostname\":\"$hostname\"}" \
      "$server/api/unregister" >/dev/null 2>&1; then
      echo "Unregistered from server."
    else
      echo "WARNING: unregister request failed (will still remove local agent)."
    fi
  else
    echo "WARNING: no curl/wget, skip unregister."
  fi
}

cmd_install() {
  require_root
  require_tty_for_install
  download_release_agent interactive

  OLD_HOSTNAME=""
  OLD_TOKEN=""
  OLD_SERVER=""
  if [ -f "$AGENT_YAML" ]; then
    OLD_HOSTNAME=$(yaml_value hostname "$AGENT_YAML")
    OLD_TOKEN=$(yaml_value token "$AGENT_YAML")
    OLD_SERVER=$(yaml_value server "$AGENT_YAML")
  fi

  systemctl stop "$SERVICE_NAME" 2>/dev/null || true

  mkdir -p "$BIN_DIR"
  rm -f "$BIN_DIR/needle-agent"
  cp "$TMP_DIR/needle-agent" "$BIN_DIR/"
  chmod +x "$BIN_DIR/needle-agent"

  read_prompt HOSTNAME "Hostname (leave empty for auto-detection) []: "
  HOSTNAME="${HOSTNAME:-}"

  read_prompt REGION "Region (ISO country code, e.g. CN/SG/US) [SG]: "
  REGION="${REGION:-SG}"

  DEFAULT_SERVER_URL="http://127.0.0.1:8008"
  while true; do
    read_prompt SERVER_URL "Server URL [${DEFAULT_SERVER_URL}]: "
    SERVER_URL="${SERVER_URL:-$DEFAULT_SERVER_URL}"
    case "$SERVER_URL" in
      http://*|https://*) break ;;
      *) echo "  ERROR: URL must start with http:// or https://" ;;
    esac
  done

  echo "Testing connectivity to $SERVER_URL/api/health ..."
  if http_get "$SERVER_URL/api/health" 2>/dev/null | grep -q 'ok'; then
    echo "  Server reachable."
  else
    echo "  WARNING: cannot reach $SERVER_URL/api/health"
    echo "  (server might not be running yet, or URL might be wrong)"
    read_prompt CONTINUE "Continue anyway? [y/N] "
    case "$CONTINUE" in
      y|Y|yes|YES) ;;
      *) echo "aborted"; exit 0 ;;
    esac
  fi

  # Per-agent unique token (not a shared server secret)
  DEFAULT_TOKEN=""
  if command -v openssl >/dev/null 2>&1; then
    DEFAULT_TOKEN=$(openssl rand -hex 16)
  elif command -v xxd >/dev/null 2>&1; then
    DEFAULT_TOKEN=$(head -c 16 /dev/urandom | xxd -p -c 256)
  else
    DEFAULT_TOKEN=$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')
  fi
  read_prompt TOKEN "Agent token (enter for auto-generated) [${DEFAULT_TOKEN}]: "
  TOKEN="${TOKEN:-$DEFAULT_TOKEN}"
  while [ -z "$TOKEN" ]; do
    read_prompt TOKEN "Agent token is required: "
  done

  read_prompt INTERVAL "Report interval (seconds) [30]: "
  INTERVAL="${INTERVAL:-30}"

  echo
  echo "VPS billing setup (for dashboard expiry countdown and traffic reset):"
  echo "  1) Monthly (1m)"
  echo "  2) Quarterly (3m)"
  echo "  3) Semi-annual (6m)"
  echo "  4) Annual (12m)"
  echo "  0) Skip"
  read_prompt PERIOD_CHOICE "Select billing period [0]: "

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
    read_prompt EXPIRES_AT "Next renewal date [${DEFAULT_EXPIRY}]: "
    EXPIRES_AT="${EXPIRES_AT:-$DEFAULT_EXPIRY}"
  fi

  TCPING_DEFAULTS="CMv4 sh-cm-v4.ip.zstaticcdn.com:80 CMv6 sh-cm-v6.ip.zstaticcdn.com:80 CUv4 sh-cu-v4.ip.zstaticcdn.com:80 CUv6 sh-cu-v6.ip.zstaticcdn.com:80 CTv4 sh-ct-v4.ip.zstaticcdn.com:80 CTv6 sh-ct-v6.ip.zstaticcdn.com:80"

  echo
  echo "TCPing targets (edit the defaults or press enter to keep):"
  # shellcheck disable=SC2086
  set -- $TCPING_DEFAULTS
  N1="$1"; T1="$2"; N3="$3"; T3="$4"; N5="$5"; T5="$6"; N7="$7"; T7="$8"; N9="$9"; T9="${10}"; N11="${11}"; T11="${12}"
  read_prompt V "  Target 1 name [${N1}]: "; N1="${V:-$N1}"
  read_prompt V "  Target 1 address [${T1}]: "; T1="${V:-$T1}"
  read_prompt V "  Target 2 name [${N3}]: "; N3="${V:-$N3}"
  read_prompt V "  Target 2 address [${T3}]: "; T3="${V:-$T3}"
  read_prompt V "  Target 3 name [${N5}]: "; N5="${V:-$N5}"
  read_prompt V "  Target 3 address [${T5}]: "; T5="${V:-$T5}"
  read_prompt V "  Target 4 name [${N7}]: "; N7="${V:-$N7}"
  read_prompt V "  Target 4 address [${T7}]: "; T7="${V:-$T7}"
  read_prompt V "  Target 5 name [${N9}]: "; N9="${V:-$N9}"
  read_prompt V "  Target 5 address [${T9}]: "; T9="${V:-$T9}"
  read_prompt V "  Target 6 name [${N11}]: "; N11="${V:-$N11}"
  read_prompt V "  Target 6 address [${T11}]: "; T11="${V:-$T11}"

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

  if [ -n "$OLD_HOSTNAME" ] && [ "$OLD_HOSTNAME" != "$HOSTNAME" ] && [ -n "$OLD_TOKEN" ] && [ -n "$OLD_SERVER" ]; then
    echo "Hostname changed: '$OLD_HOSTNAME' → '$HOSTNAME'"
    echo "Unregistering old agent from server..."
    if command -v curl >/dev/null 2>&1; then
      curl -fsS -X POST "$OLD_SERVER/api/unregister" \
        -H "Content-Type: application/json" \
        -H "Authorization: Bearer $OLD_TOKEN" \
        --data-binary "{\"hostname\":\"$OLD_HOSTNAME\"}" >/dev/null 2>&1 || true
    elif command -v wget >/dev/null 2>&1; then
      wget -qO- --method=POST \
        --header="Content-Type: application/json" \
        --header="Authorization: Bearer $OLD_TOKEN" \
        --body-data="{\"hostname\":\"$OLD_HOSTNAME\"}" \
        "$OLD_SERVER/api/unregister" >/dev/null 2>&1 || true
    fi
  fi

  echo "Installing systemd service..."
  write_systemd_unit
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
  echo
  echo ">>> IMPORTANT: allow this agent on the Server (whitelist) <<<"
  echo "  Agent token: $TOKEN"
  echo
  echo "  Binary Server:"
  echo "    sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db allow-token $TOKEN"
  echo
  echo "  Docker Server (in compose directory):"
  echo "    docker compose exec needle-server \\"
  echo "      needle-server -db /data/needle.db allow-token $TOKEN"
  echo
  echo "  Hostname binds automatically on first successful report."
  echo "========================================="
  echo "To view logs: journalctl -u $SERVICE_NAME -f"
}

cmd_upgrade() {
  require_root
  if [ ! -f "$AGENT_YAML" ]; then
    echo "Needle Agent is not installed (missing $AGENT_YAML)."
    echo "Run: sudo bash needle-agent.sh install"
    exit 1
  fi

  download_release_agent

  echo "Stopping $SERVICE_NAME..."
  systemctl stop "$SERVICE_NAME" 2>/dev/null || true

  echo "Installing new binary..."
  mkdir -p "$BIN_DIR"
  cp "$TMP_DIR/needle-agent" "$BIN_DIR/"
  chmod +x "$BIN_DIR/needle-agent"

  echo "Updating systemd service..."
  write_systemd_unit
  systemctl daemon-reload
  systemctl enable --now "$SERVICE_NAME"

  echo
  echo "========================================="
  echo " Needle Agent upgraded to $VERSION!"
  echo " Config preserved: $AGENT_YAML"
  echo "========================================="
  echo "To view logs: journalctl -u $SERVICE_NAME -f"
}

cmd_uninstall() {
  require_root

  local do_unregister=false yes=false
  while [ $# -gt 0 ]; do
    case "$1" in
      --unregister) do_unregister=true; shift ;;
      -y|--yes) yes=true; shift ;;
      -h|--help) usage; exit 0 ;;
      *) echo "Unknown option: $1"; usage; exit 1 ;;
    esac
  done

  if [ "$yes" != true ]; then
    echo "This will stop needle-agent and remove $INSTALL_DIR"
    if [ "$do_unregister" = true ]; then
      echo "and also call Server /api/unregister (if config allows)."
    else
      echo "(local only; Server DB entry is kept unless you pass --unregister)"
    fi
    read_prompt ans "Continue? [y/N] "
    case "$ans" in
      y|Y|yes|YES) ;;
      *) echo "aborted"; exit 0 ;;
    esac
  fi

  if [ "$do_unregister" = true ]; then
    unregister_from_server
  fi

  echo "Stopping service..."
  systemctl stop "$SERVICE_NAME" 2>/dev/null || true
  systemctl disable "$SERVICE_NAME" 2>/dev/null || true
  rm -f "/etc/systemd/system/${SERVICE_NAME}.service"
  systemctl daemon-reload 2>/dev/null || true

  echo "Removing $INSTALL_DIR ..."
  rm -rf "$INSTALL_DIR"

  echo
  echo "========================================="
  echo " Needle Agent uninstalled (local)."
  if [ "$do_unregister" = true ]; then
    echo " Unregister was attempted against Server."
  else
    echo " Server node data was NOT removed."
    echo " On Server: needle-server -db <path> delete-agent <hostname>"
  fi
  echo "========================================="
}

cmd_status() {
  echo "Install dir: $INSTALL_DIR"
  if [ -f "$AGENT_YAML" ]; then
    echo "Config:      $AGENT_YAML (present)"
    echo "  hostname:  $(yaml_value hostname "$AGENT_YAML")"
    echo "  server:    $(yaml_value server "$AGENT_YAML")"
    echo "  region:    $(yaml_value region "$AGENT_YAML")"
  else
    echo "Config:      missing"
  fi
  if [ -x "$BIN_DIR/needle-agent" ]; then
    echo "Binary:      $BIN_DIR/needle-agent (present)"
  else
    echo "Binary:      missing"
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
}

# --- main ---
CMD=""
case "${1:-}" in
  install|upgrade|uninstall|status|help|-h|--help)
    CMD="$1"
    shift
    ;;
  "")
    if [ -f "$AGENT_YAML" ]; then
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
  install)   cmd_install; exit 0 ;;
  upgrade)   cmd_upgrade; exit 0 ;;
  uninstall) cmd_uninstall "$@"; exit 0 ;;
  status)    cmd_status; exit 0 ;;
  help|-h|--help) usage; exit 0 ;;
esac
