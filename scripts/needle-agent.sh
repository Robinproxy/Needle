#!/usr/bin/env bash
set -eo pipefail

REPO="Robinproxy/Needle"
INSTALL_DIR="/opt/needle-agent"
BIN_DIR="$INSTALL_DIR/bin"
SERVICE_NAME="needle-agent"
AGENT_USER="needle-agent"
AGENT_GROUP="needle-agent"
SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
AGENT_YAML="$INSTALL_DIR/agent.yaml"
SCRIPT_URL="https://raw.githubusercontent.com/$REPO/main/scripts/needle-agent.sh"

usage() {
  cat <<EOF
Needle Agent ops script

Usage:
  sudo bash needle-agent.sh [command] [options]

Commands:
  install              Interactive install (download + config + systemd)
  upgrade              Upgrade; choose keep config or reconfigure settings
  uninstall            Remove local agent only (default)
  uninstall --unregister
                       Call Server /api/unregister, then remove local agent
  status               Show install/service status
  help                 Show this help

No command:
  If agent.yaml exists → upgrade, else → install

Options:
  -y, --yes            Skip uninstall confirmation
  --keep-config        With upgrade: keep configuration without prompting
  --reconfigure        With upgrade: reselect probes, allowance, and optional billing; preserve token
  --configure-intervals
                       With upgrade: change only realtime/history intervals
  --unregister         With uninstall: revoke this Agent credential; keep Server data

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
    IFS= read -r -p "$__prompt" __val </dev/tty || return 1
  elif [ -t 0 ]; then
    IFS= read -r -p "$__prompt" __val || return 1
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
    echo "ERROR: checksum is required; download aborted." >&2
 exit 1
  fi

  tar xOzf "$TGZ" needle-agent > "$TMP_DIR/needle-agent"
  if [ ! -f "$TMP_DIR/needle-agent" ]; then
    echo "ERROR: needle-agent binary not found in archive"
    exit 1
  fi
  chmod 755 "$TMP_DIR/needle-agent"
  if ! "$TMP_DIR/needle-agent" -installer-protocol 2>/dev/null | grep -q '^needle-install-v2 '; then
    echo 'ERROR: latest release binary is older than this installer protocol. Use the installer packaged with that release, or wait for a compatible release. Existing installation unchanged.' >&2
    exit 1
  fi

}

require_linux_runtime() {
  [ "$(uname -s)" = Linux ] || { echo 'ERROR: this installer requires Linux and systemd.' >&2; exit 1; }
  local tool
  for tool in systemctl getent useradd groupadd userdel groupdel runuser install stat flock; do
    command -v "$tool" >/dev/null || { echo "ERROR: required command not found: $tool" >&2; exit 1; }
  done
  # Serialize installation, upgrades and removal. /run is managed by root.
  umask 077
  exec 9>"/run/${SERVICE_NAME}-install.lock"
  flock -n 9 || { echo 'ERROR: another Needle Agent installer is running.' >&2; exit 1; }
}

validate_agent_account() {
  local entry name password uid gid gecos home shell group_entry group_gid members
  entry=$(getent passwd "$AGENT_USER") || return 1
  IFS=: read -r name password uid gid gecos home shell <<< "$entry"
  group_entry=$(getent group "$AGENT_GROUP") || return 1
  IFS=: read -r name password group_gid members <<< "$group_entry"
  case "$shell" in */nologin|/bin/false) ;; *) return 1 ;; esac
  [ "$uid" -gt 0 ] && [ "$gid" -gt 0 ] && [ "$gid" = "$group_gid" ] &&
    [ "$home" = /nonexistent ] && [ "$(id -G "$AGENT_USER")" = "$gid" ] || return 1
  # Other users must not inherit access to this Agent's token.
  [ -z "$members" ] || [ "$members" = "$AGENT_USER" ]
}

ensure_agent_account() {
  local shell
  if ! getent passwd "$AGENT_USER" >/dev/null; then
    if getent group "$AGENT_GROUP" >/dev/null; then
      echo 'ERROR: an existing needle-agent group has no matching service user; refusing to reuse it.' >&2
      return 1
    fi
    shell=$(command -v nologin) || { echo 'ERROR: nologin is required.' >&2; return 1; }
    groupadd --system "$AGENT_GROUP"
    if ! useradd --system --gid "$AGENT_GROUP" --home-dir /nonexistent --no-create-home --shell "$shell" "$AGENT_USER"; then
      groupdel "$AGENT_GROUP"
      return 1
    fi
  fi
  validate_agent_account || { echo 'ERROR: needle-agent is not an isolated service account; refusing to alter it.' >&2; return 1; }
}

write_systemd_unit() {
  cat > "$1" <<UNIT
[Unit]
Description=Needle Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${AGENT_USER}
Group=${AGENT_GROUP}
SupplementaryGroups=
ExecStart=${BIN_DIR}/needle-agent ${AGENT_YAML}
Restart=always
RestartSec=10

# Security hardening
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
PrivateDevices=true
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

validate_systemd_sandbox() {
  local property expected actual setting
  for setting in "User=$AGENT_USER" "Group=$AGENT_GROUP" 'SupplementaryGroups=' \
      'NoNewPrivileges=yes' 'ProtectSystem=strict' 'ProtectHome=yes' \
      'PrivateTmp=yes' 'PrivateDevices=yes' 'RestrictSUIDSGID=yes' \
      'CapabilityBoundingSet=' 'AmbientCapabilities='; do
    property=${setting%%=*}
    expected=${setting#*=}
    actual=$(systemctl show -p "$property" --value "$SERVICE_NAME")
    if [ "$actual" != "$expected" ]; then
      echo "ERROR: effective systemd $property differs from the required sandbox; check drop-in overrides and systemd support." >&2
      return 1
    fi
  done
}

wait_agent_active() {
  local attempt
  # Allow immediate ExecStart/config failures to surface before reporting success.
  for attempt in 1 2 3 4 5; do
    sleep 1
    if systemctl is-active --quiet "$SERVICE_NAME"; then
      local pid actual_uid
      pid=$(systemctl show -p MainPID --value "$SERVICE_NAME")
      actual_uid=$(ps -o uid= -p "$pid" | tr -d ' ')
      [ "$actual_uid" = "$(id -u "$AGENT_USER")" ] && [ "$actual_uid" != 0 ] && return 0
    fi
  done
  echo "ERROR: Agent did not remain active as $AGENT_USER; inspect journalctl -u $SERVICE_NAME." >&2
  return 1
}

# Both install and upgrade commit through this path. Originals stay root-only
# in TMP_DIR until validation and service startup have succeeded.
deploy_agent() (
  set -e
  # Subshell variables intentionally aren't local: EXIT runs after a function's
  # local scope unwinds, and rollback must still see the saved deployment state.
  had_binary=false had_config=false had_unit=false had_backup=false
  was_active=false was_enabled=false changed=false had_user=false
  old_install_mode= old_bin_mode= staged_binary= staged_config= staged_unit=
  for path in "$INSTALL_DIR" "$BIN_DIR" "$BIN_DIR/needle-agent" "$AGENT_YAML" "$AGENT_YAML.bak" "$SERVICE_FILE"; do
    [ ! -L "$path" ] || { echo "ERROR: refusing to replace symlink: $path" >&2; exit 1; }
  done
  chmod 755 "$TMP_DIR/needle-agent"
  "$TMP_DIR/needle-agent" -check-config "$TMP_DIR/agent.yaml"
  write_systemd_unit "$TMP_DIR/needle-agent.service"
  [ -s "$TMP_DIR/needle-agent.service" ] || { echo 'ERROR: empty systemd unit.' >&2; exit 1; }
  getent passwd "$AGENT_USER" >/dev/null && had_user=true
  ensure_agent_account

  if [ -d "$INSTALL_DIR" ]; then old_install_mode=$(stat -c '%u:%g:%a' "$INSTALL_DIR"); fi
  if [ -d "$BIN_DIR" ]; then old_bin_mode=$(stat -c '%u:%g:%a' "$BIN_DIR"); fi
  if [ -f "$BIN_DIR/needle-agent" ]; then cp -p "$BIN_DIR/needle-agent" "$TMP_DIR/old-binary"; had_binary=true; fi
  if [ -f "$AGENT_YAML" ]; then cp -p "$AGENT_YAML" "$TMP_DIR/old-config"; had_config=true; fi
  if [ -f "$SERVICE_FILE" ]; then cp -p "$SERVICE_FILE" "$TMP_DIR/old-unit"; had_unit=true; fi
  if [ -f "$AGENT_YAML.bak" ]; then cp -p "$AGENT_YAML.bak" "$TMP_DIR/old-backup"; had_backup=true; fi
  systemctl is-active --quiet "$SERVICE_NAME" && was_active=true
  systemctl is-enabled --quiet "$SERVICE_NAME" && was_enabled=true

  deployment_exit() {
    local result=$?
    trap - EXIT
    if [ "$result" -ne 0 ] && [ "$changed" = true ]; then
      set +e
      echo 'ERROR: deployment failed; restoring previous binary, configuration and service.' >&2
      local restore_failed=false
      if [ -f "$SERVICE_FILE" ]; then systemctl stop "$SERVICE_NAME" || restore_failed=true; fi
      if [ "$had_binary" = true ]; then
        rm -f "$BIN_DIR/needle-agent"
        cp -p "$TMP_DIR/old-binary" "$BIN_DIR/needle-agent" || restore_failed=true
      else rm -f "$BIN_DIR/needle-agent"; fi
      if [ "$had_config" = true ]; then cp -p "$TMP_DIR/old-config" "$AGENT_YAML" || restore_failed=true
      else rm -f "$AGENT_YAML"; fi
      if [ "$had_backup" = true ]; then cp -p "$TMP_DIR/old-backup" "$AGENT_YAML.bak" || restore_failed=true
      else rm -f "$AGENT_YAML.bak"; fi
      if [ "$had_unit" = true ]; then cp -p "$TMP_DIR/old-unit" "$SERVICE_FILE" || restore_failed=true
      else rm -f "$SERVICE_FILE"; fi
      if [ -n "$old_install_mode" ]; then
        chown "${old_install_mode%:*}" "$INSTALL_DIR" && chmod "${old_install_mode##*:}" "$INSTALL_DIR" || restore_failed=true
      fi
      if [ -n "$old_bin_mode" ]; then
        chown "${old_bin_mode%:*}" "$BIN_DIR" && chmod "${old_bin_mode##*:}" "$BIN_DIR" || restore_failed=true
      fi
      systemctl daemon-reload || restore_failed=true
      if [ "$was_enabled" = true ]; then systemctl enable "$SERVICE_NAME" || restore_failed=true
      else systemctl disable "$SERVICE_NAME" 2>/dev/null; fi
      if [ "$was_active" = true ]; then systemctl start "$SERVICE_NAME" || restore_failed=true; fi
      if [ "$restore_failed" = true ]; then
        # Do not let the download cleanup erase the only remaining originals.
        install -d -o root -g root -m 700 "$INSTALL_DIR/recovery"
        cp -p "$TMP_DIR"/old-* "$INSTALL_DIR/recovery/"
        echo "ERROR: rollback needs attention; originals preserved in $INSTALL_DIR/recovery (root only)." >&2
      elif [ "$had_user" = false ]; then
        userdel "$AGENT_USER"
        if getent group "$AGENT_GROUP" >/dev/null; then groupdel "$AGENT_GROUP"; fi
      fi
    fi
    rm -f "$staged_binary" "$staged_config" "$staged_unit"
    exit "$result"
  }
  trap deployment_exit EXIT
  # A stop failure must not lead to replacing a still-running service.
  changed=true
  if [ "$had_unit" = true ] || [ "$was_active" = true ]; then systemctl stop "$SERVICE_NAME"; fi
  install -d -o root -g "$AGENT_GROUP" -m 750 "$INSTALL_DIR"
  install -d -o root -g root -m 755 "$BIN_DIR"
  staged_binary=$(mktemp "$BIN_DIR/.needle-agent.XXXXXX")
  install -o root -g root -m 755 "$TMP_DIR/needle-agent" "$staged_binary"
  staged_config=$(mktemp "$INSTALL_DIR/.agent.yaml.XXXXXX")
  install -o root -g "$AGENT_GROUP" -m 640 "$TMP_DIR/agent.yaml" "$staged_config"
  staged_unit=$(mktemp "${SERVICE_FILE}.XXXXXX")
  install -o root -g root -m 644 "$TMP_DIR/needle-agent.service" "$staged_unit"
  mv -f "$staged_binary" "$BIN_DIR/needle-agent"
  mv -f "$staged_config" "$AGENT_YAML"
  mv -f "$staged_unit" "$SERVICE_FILE"
  if [ "${UPGRADE_RECONFIGURE:-false}" = true ] && [ "$had_config" = true ]; then
    install -o root -g root -m 600 "$TMP_DIR/old-config" "$AGENT_YAML.bak"
  elif [ "$had_backup" = true ]; then
    chown root:root "$AGENT_YAML.bak"
    chmod 600 "$AGENT_YAML.bak"
  fi
  # Catch config-read/parent-directory permissions as the actual service user.
  runuser -u "$AGENT_USER" -- "$BIN_DIR/needle-agent" -check-config "$AGENT_YAML"
  systemctl daemon-reload
  validate_systemd_sandbox
  if [ "$had_config" = false ]; then
    systemctl enable "$SERVICE_NAME"
    systemctl start "$SERVICE_NAME"
    wait_agent_active
  elif [ "$was_active" = true ]; then
    systemctl start "$SERVICE_NAME"
    wait_agent_active
  fi
)

unregister_from_server() {
  if [ ! -x "$BIN_DIR/needle-agent" ] || [ ! -r "$AGENT_YAML" ]; then
    echo 'ERROR: binary and configuration required to revoke this Agent credential. Local installation retained.' >&2
    return 1
  fi
  if ! "$BIN_DIR/needle-agent" -installer-protocol 2>/dev/null | grep -q '^needle-install-v2 '; then
    echo 'ERROR: upgrade Server and Agent before using --unregister; an older Agent may call the destructive legacy endpoint. Plain local uninstall remains available.' >&2
    return 1
  fi
  if ! "$BIN_DIR/needle-agent" -unregister "$AGENT_YAML"; then
    echo 'ERROR: unregister failed or safe Server semantics were not confirmed. Local installation retained; use plain uninstall only if credential revocation is not required.' >&2
    return 1
  fi
  echo 'Agent credential revoked; Server monitoring data retained.'
}

tcping_prompt() { read_prompt TCPING_ANSWER "$1"; }

# TCP Ping selection is deliberately explicit; no connectivity auto-detection.
tcping_yaml_quote() {
    printf "'%s'" "$(printf '%s' "$1" | sed "s/'/''/g")"
}

tcping_add_target() {
    TCPING_NAME=$1
    TCPING_TARGET=$2
    [ -n "$TCPING_NAME" ] && [ "${#TCPING_NAME}" -le 64 ] || { echo 'Invalid TCP Ping name (1–64 characters).' >&2; return 1; }
    if grep -Fxq -e "$TCPING_NAME" "$TMP_DIR/tcpping-names"; then
        echo "Duplicate TCP Ping name: $TCPING_NAME" >&2; return 1
    fi
    [ "${#TCPING_TARGET}" -le 256 ] || { echo 'TCP Ping target is too long.' >&2; return 1; }
    TCPING_PORT=${TCPING_TARGET##*:}
    TCPING_HOST=${TCPING_TARGET%:*}
    case "$TCPING_TARGET" in *:*) ;; *) echo 'Use host:port or [IPv6]:port for TCP Ping.' >&2; return 1 ;; esac
    case "$TCPING_HOST" in '') echo 'TCP Ping host is required.' >&2; return 1 ;; \[*\]) ;; *:*) echo 'Enclose IPv6 addresses in brackets.' >&2; return 1 ;; esac
    case "$TCPING_PORT" in ''|*[!0-9]*) echo 'TCP Ping port must be 1–65535.' >&2; return 1 ;; esac
    [ "$TCPING_PORT" -ge 1 ] && [ "$TCPING_PORT" -le 65535 ] || { echo 'TCP Ping port must be 1–65535.' >&2; return 1; }
    [ "$TCPING_COUNT" -lt 32 ] || { echo 'At most 32 TCP Ping targets are supported.' >&2; return 1; }
    printf '%s\n' "$TCPING_NAME" >> "$TMP_DIR/tcpping-names"
    {
        printf '  - name: '; tcping_yaml_quote "$TCPING_NAME"
        printf '\n    target: '; tcping_yaml_quote "$TCPING_TARGET"; printf '\n'
    } >> "$TMP_DIR/tcpping-entries"
    TCPING_COUNT=$((TCPING_COUNT + 1))
}

tcping_add_preset() {
    case "$1" in
        1) tcping_add_target CMv4 sh-cm-v4.ip.zstaticcdn.com:80 ;;
        2) tcping_add_target CMv6 sh-cm-v6.ip.zstaticcdn.com:80 ;;
        3) tcping_add_target CUv4 sh-cu-v4.ip.zstaticcdn.com:80 ;;
        4) tcping_add_target CUv6 sh-cu-v6.ip.zstaticcdn.com:80 ;;
        5) tcping_add_target CTv4 sh-ct-v4.ip.zstaticcdn.com:80 ;;
        6) tcping_add_target CTv6 sh-ct-v6.ip.zstaticcdn.com:80 ;;
        *) echo 'Invalid TCP Ping preset.' >&2; return 1 ;;
    esac
}

configure_tcping() {
    TCPING_COUNT=0
    : > "$TMP_DIR/tcpping-names"
    : > "$TMP_DIR/tcpping-entries"
    echo 'TCP Ping: 1) IPv4 only  2) IPv6 only  3) Dual stack  4) Custom  5) Disabled'
    tcping_prompt 'TCP Ping mode [1]: '
    case "${TCPING_ANSWER:-1}" in
        1) for TCPING_PRESET in 1 3 5; do tcping_add_preset "$TCPING_PRESET"; done ;;
        2) for TCPING_PRESET in 2 4 6; do tcping_add_preset "$TCPING_PRESET"; done ;;
        3) for TCPING_PRESET in 1 2 3 4 5 6; do tcping_add_preset "$TCPING_PRESET"; done ;;
        4)
            echo 'Presets: 1) CMv4  2) CMv6  3) CUv4  4) CUv6  5) CTv4  6) CTv6'
            tcping_prompt 'Preset numbers separated by spaces (empty = none): '
            case "$TCPING_ANSWER" in *[!1-6\ ]*) echo 'Use preset numbers 1–6 separated by spaces.' >&2; return 1 ;; esac
            # Intentional splitting: input above accepts only preset digits and spaces.
            for TCPING_PRESET in $TCPING_ANSWER; do tcping_add_preset "$TCPING_PRESET"; done
            tcping_prompt 'Number of additional custom targets [0]: '
            TCPING_EXTRA=${TCPING_ANSWER:-0}
            case "$TCPING_EXTRA" in *[!0-9]*) echo 'Target count must be a non-negative integer.' >&2; return 1 ;; esac
            [ "$TCPING_EXTRA" -le "$((32 - TCPING_COUNT))" ] || { echo 'At most 32 TCP Ping targets are supported.' >&2; return 1; }
            TCPING_INDEX=1
            while [ "$TCPING_INDEX" -le "$TCPING_EXTRA" ]; do
                tcping_prompt "Custom target $TCPING_INDEX name: "; TCPING_CUSTOM_NAME=$TCPING_ANSWER
                tcping_prompt "Custom target $TCPING_INDEX address (host:port or [IPv6]:port): "
                tcping_add_target "$TCPING_CUSTOM_NAME" "$TCPING_ANSWER"
                TCPING_INDEX=$((TCPING_INDEX + 1))
            done
            ;;
        5) ;;
        *) echo 'Select TCP Ping mode 1–5.' >&2; return 1 ;;
    esac
    if [ "$TCPING_COUNT" -eq 0 ]; then
        printf 'tcpping: []\n' > "$TMP_DIR/tcpping.yaml"
    else
        { printf 'tcpping:\n'; cat "$TMP_DIR/tcpping-entries"; } > "$TMP_DIR/tcpping.yaml"
    fi
}

configure_monthly_traffic() {
    rm -f "$TMP_DIR/traffic.yaml"
    if [ "${1:-install}" = upgrade ]; then
        tcping_prompt 'Monthly upload + download allowance in GB (1000 = 1 TB; 0 = no limit; Enter = keep existing): '
        [ -n "$TCPING_ANSWER" ] || return 0
    else
        tcping_prompt 'Monthly upload + download allowance in GB (1000 = 1 TB; 0 = no limit) [0]: '
    fi
    TRAFFIC_LIMIT_GB=${TCPING_ANSWER:-0}
    TRAFFIC_LIMIT_GB=$(printf '%s\n' "$TRAFFIC_LIMIT_GB" | LC_ALL=C awk '
        /^[0-9]+([.][0-9][0-9]?[0-9]?)?$/ { if ($0 + 0 <= 9000000) { printf "%.3f", $0 + 0; ok = 1 } }
        END { if (!ok) exit 1 }
    ') || { echo 'Traffic allowance must be 0–9000000 GB, with up to 3 decimal places.' >&2; return 1; }
    printf 'monthly_traffic_limit_gb: %s\n' "$TRAFFIC_LIMIT_GB" > "$TMP_DIR/traffic.yaml"
}

upgrade_interactive() { [ -t 0 ]; }


interval_setting() {
    # The Agent validates the full YAML later; only simple scalar defaults are
    # read here. Unsupported layouts fail closed rather than changing identity.
    awk -v wanted="$1" '
        /^[^ \t#][^:]*:/ {
            key=$0; sub(/[ \t]*:.*/, "", key); gsub(/^["\047]|["\047]$/, "", key)
            if (key==wanted) {
                value=$0; sub(/^[^:]*:[ \t]*/, "", value); sub(/[ \t]+#.*/, "", value)
                gsub(/^["\047]|["\047 \t]$/, "", value)
                if (value !~ /^[0-9]+$/ || value+0>86400) exit 1
                printf "%d",value+0; found=1
            }
        }
        END { if (!found) exit 1 }
    ' "$2"
}

configure_intervals() {
    DEFAULT_INTERVAL=30
    DEFAULT_REALTIME_INTERVAL=5
    if [ "${1:-install}" = upgrade ]; then
        DEFAULT_INTERVAL=$(interval_setting interval "$2" || printf '30')
        [ "$DEFAULT_INTERVAL" -gt 0 ] || DEFAULT_INTERVAL=30
        DEFAULT_REALTIME_INTERVAL=$(interval_setting realtime_interval "$2" || printf '0')
    fi
    tcping_prompt "Realtime status interval in seconds; 0 = disabled [$DEFAULT_REALTIME_INTERVAL]: "
    REALTIME_INTERVAL=${TCPING_ANSWER:-$DEFAULT_REALTIME_INTERVAL}
    tcping_prompt "Full report / history interval in seconds [$DEFAULT_INTERVAL]: "
    INTERVAL=${TCPING_ANSWER:-$DEFAULT_INTERVAL}
    REALTIME_INTERVAL=$(printf '%s\n' "$REALTIME_INTERVAL" | LC_ALL=C awk '/^[0-9]+$/ && $0+0<=86400 {printf "%d",$0+0;ok=1} END {if(!ok)exit 1}') || { echo 'Realtime interval must be an integer from 0 to 86400.' >&2; return 1; }
    INTERVAL=$(printf '%s\n' "$INTERVAL" | LC_ALL=C awk '/^[0-9]+$/ && $0+0>=1 && $0+0<=86400 {printf "%d",$0+0;ok=1} END {if(!ok)exit 1}') || { echo 'History interval must be an integer from 1 to 86400.' >&2; return 1; }
    [ "$REALTIME_INTERVAL" -le "$INTERVAL" ] || { echo 'Realtime interval must be 0 (disabled) or no greater than the history interval.' >&2; return 1; }
    printf 'realtime_interval: %s\ninterval: %s\n' "$REALTIME_INTERVAL" "$INTERVAL" > "$TMP_DIR/intervals.yaml"
}

configure_billing() {
    rm -f "$TMP_DIR/billing.yaml"
    if [ "${1:-install}" = upgrade ]; then
        tcping_prompt 'Replace currency, amount, billing period, and renewal date? [y/N]: '
        case "$TCPING_ANSWER" in
            y|Y|yes|YES) ;;
            ''|n|N|no|NO) return 0 ;;
            *) echo 'Choose y or n.' >&2; return 1 ;;
        esac
        echo 'All four billing fields will be replaced. Other settings and the token are preserved.'
    fi
    echo 'Currency: USD, CNY, EUR, GBP, CAD, HKD, JPY, AUD, or another three-letter code.'
    tcping_prompt 'Currency [USD]: '
    BILLING_CURRENCY=$(printf '%s' "${TCPING_ANSWER:-USD}" | tr '[:lower:]' '[:upper:]')
    case "$BILLING_CURRENCY" in
        [A-Z][A-Z][A-Z]) ;;
        *) echo 'Currency must be a three-letter code.' >&2; return 1 ;;
    esac
    tcping_prompt 'Amount for the entire billing period (0 = free; Enter = not configured): '
    BILLING_AMOUNT=$TCPING_ANSWER
    if [ -n "$BILLING_AMOUNT" ]; then
        printf '%s\n' "$BILLING_AMOUNT" | LC_ALL=C awk -F. '
            /^[0-9]+([.][0-9]+)?$/ && length($1) <= 12 && (NF == 1 || length($2) <= 6) { ok = 1 }
            END { if (!ok) exit 1 }
        ' || { echo 'Amount must be non-negative, with up to 12 integer and 6 fractional digits.' >&2; return 1; }
    fi
    echo 'Billing period: 1m, 3m, 6m, 12m, 24m, 36m, forever (no expiry), or skip.'
    tcping_prompt 'Billing period [forever]: '
    BILLING_PERIOD=${TCPING_ANSWER:-forever}
    EXPIRES_AT=
    case "$BILLING_PERIOD" in
        forever) ;;
        skip)
            BILLING_PERIOD=
            [ -z "$BILLING_AMOUNT" ] || { echo 'Choose a billing period when an amount is configured.' >&2; return 1; } ;;
        1m|3m|6m|12m|24m|36m)
            tcping_prompt 'Next renewal / top-up date (YYYY-MM-DD; actual due date, including for a transferred node): '
            EXPIRES_AT=$TCPING_ANSWER
            printf '%s\n' "$EXPIRES_AT" | LC_ALL=C awk -F- '
                /^[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]$/ {
                    y=$1+0; m=$2+0; d=$3+0
                    days[1]=31; days[2]=28+(y%4==0 && (y%100!=0 || y%400==0)); days[3]=31
                    days[4]=30; days[5]=31; days[6]=30; days[7]=31; days[8]=31
                    days[9]=30; days[10]=31; days[11]=30; days[12]=31
                    if (y>=1970 && m>=1 && m<=12 && d>=1 && d<=days[m]) ok=1
                }
                END { if (!ok) exit 1 }
            ' || { echo 'Invalid renewal date; use a real calendar date in YYYY-MM-DD format.' >&2; return 1; } ;;
        *) echo 'Unsupported billing period.' >&2; return 1 ;;
    esac
    {
        printf 'billing_currency: '; tcping_yaml_quote "$BILLING_CURRENCY"
        printf '\nbilling_amount: '; tcping_yaml_quote "$BILLING_AMOUNT"
        printf '\nbilling_period: '; tcping_yaml_quote "$BILLING_PERIOD"
        printf '\nexpires_at: '; tcping_yaml_quote "$EXPIRES_AT"; printf '\n'
    } > "$TMP_DIR/billing.yaml"
}

stage_tcping_config() {
    # Change the top-level tcpping section and optionally quota/billing settings.
    # Credentials and other settings are copied verbatim, including multiline values.
    # Reject layouts we cannot safely edit instead of regenerating an identity.
    REPLACE_TCPING=0
    [ ! -f "$TMP_DIR/tcpping.yaml" ] || REPLACE_TCPING=1
    REPLACE_INTERVALS=0
    [ ! -f "$TMP_DIR/intervals.yaml" ] || REPLACE_INTERVALS=1
    REPLACE_QUOTA=0
    [ ! -f "$TMP_DIR/traffic.yaml" ] || REPLACE_QUOTA=1
    REPLACE_BILLING=0
    [ ! -f "$TMP_DIR/billing.yaml" ] || REPLACE_BILLING=1
    awk -v tcping="$REPLACE_TCPING" -v intervals="$REPLACE_INTERVALS" -v quota="$REPLACE_QUOTA" -v billing="$REPLACE_BILLING" '
        BEGIN { skip = 0; token = 0; documents = 0; sections = 0; quotas = 0; unsafe = 0 }
        /^---([ \t]*#.*)?[ \t]*$/ { documents++; if (documents > 1) exit 1; print; next }
        /^\.\.\.([ \t]*#.*)?[ \t]*$/ { next }
        /^(token|"token"|\047token\047)[ \t]*:/ { token = 1 }
        tcping && /^(tcpping|"tcpping"|\047tcpping\047)[ \t]*:/ { skip = 1; sections++; if ($0 ~ /[&*]/) unsafe = 1; next }
        quota && /^(monthly_traffic_limit_gb|"monthly_traffic_limit_gb"|\047monthly_traffic_limit_gb\047)[ \t]*:/ { skip = 1; quotas++; if ($0 ~ /[&*]/) unsafe = 1; next }
        intervals && /^[^ \t#][^:]*:/ {
            key=$0; sub(/[ \t]*:.*/, "", key); gsub(/^["\047]|["\047]$/, "", key)
            if (key=="interval" || key=="realtime_interval") {
                skip=1; count[key]++; if(count[key]>1 || $0 ~ /[&*]/) unsafe=1; next
            }
        }
        billing && /^[^ \t#][^:]*:/ {
            key = $0; sub(/[ \t]*:.*/, "", key); gsub(/^["\047]|["\047]$/, "", key)
            if (key == "billing_currency" || key == "billing_amount" || key == "billing_period" || key == "expires_at") {
                skip = 1; count[key]++; if (count[key] > 1 || $0 ~ /[&*]/) unsafe = 1; next
            }
        }
        skip && /^[^ \t#][^:]*:/ { skip = 0 }
        skip && /[&*]/ { unsafe = 1 }
        !skip { print }
        END { if (!token || documents > 1 || sections > 1 || quotas > 1 || unsafe) exit 1 }
    ' "$1" > "$TMP_DIR/agent.yaml" || {
        rm -f "$TMP_DIR/agent.yaml"
        echo 'Cannot safely update this YAML layout; original configuration is unchanged. Edit the Agent settings manually.' >&2
        return 1
    }
    if [ -f "$TMP_DIR/tcpping.yaml" ]; then cat "$TMP_DIR/tcpping.yaml" >> "$TMP_DIR/agent.yaml"; fi
    if [ -f "$TMP_DIR/intervals.yaml" ]; then cat "$TMP_DIR/intervals.yaml" >> "$TMP_DIR/agent.yaml"; fi
    if [ -f "$TMP_DIR/traffic.yaml" ]; then cat "$TMP_DIR/traffic.yaml" >> "$TMP_DIR/agent.yaml"; fi
    if [ -f "$TMP_DIR/billing.yaml" ]; then cat "$TMP_DIR/billing.yaml" >> "$TMP_DIR/agent.yaml"; fi
}

choose_upgrade_config() {
    UPGRADE_RECONFIGURE=false
    INTERVALS_ONLY=false
    case "${UPGRADE_CONFIG_MODE:-ask}" in
        keep) ;;
        intervals)
            upgrade_interactive || { echo 'Interval settings require an interactive terminal.' >&2; return 1; }
            UPGRADE_RECONFIGURE=true; INTERVALS_ONLY=true
            ;;
        reconfigure)
            upgrade_interactive || { echo 'Reconfiguration requires an interactive terminal.' >&2; return 1; }
            UPGRADE_RECONFIGURE=true
            ;;
        ask)
            if upgrade_interactive; then
                echo 'Upgrade configuration:'
                echo '  1) Keep existing configuration (default)'
                echo '  2) Reconfigure TCP Ping, monthly allowance, and optional billing settings; preserve token and identity'
                echo '  3) Change realtime/history intervals only; preserve all other settings'
                tcping_prompt 'Configuration choice [1]: '
                case "${TCPING_ANSWER:-1}" in
                    1) ;;
                    2) UPGRADE_RECONFIGURE=true ;;
                    3) UPGRADE_RECONFIGURE=true; INTERVALS_ONLY=true ;;
                    *) echo 'Select configuration choice 1, 2 or 3.' >&2; return 1 ;;
                esac
            fi
            ;;
        *) echo 'Invalid upgrade configuration mode.' >&2; return 1 ;;
    esac
    if [ "$UPGRADE_RECONFIGURE" = true ]; then
        if [ "$INTERVALS_ONLY" = false ]; then
            configure_tcping
            configure_monthly_traffic upgrade
            configure_billing upgrade
        fi
        configure_intervals upgrade "$1"
        stage_tcping_config "$1"
    fi
}
# End of TCP Ping selection.

cmd_install() {
  require_root
  if [ -f "$AGENT_YAML" ]; then
    echo 'Existing configuration found; using the upgrade flow to preserve token and identity.'
    cmd_upgrade
    return
  fi
  require_linux_runtime
  require_tty_for_install
  download_release_agent interactive

  read_prompt HOSTNAME "Hostname (leave empty for auto-detection) []: "
  HOSTNAME="${HOSTNAME:-}"

  read_prompt REGION "Region (ISO country code, e.g. CN/SG/US) [SG]: "
  REGION="${REGION:-SG}"

  DEFAULT_SERVER_URL="http://127.0.0.1:8008"
  ALLOW_PLAIN_HTTP=false
  while true; do
    read_prompt SERVER_URL "Server URL [${DEFAULT_SERVER_URL}]: "
    SERVER_URL="${SERVER_URL:-$DEFAULT_SERVER_URL}"
    case "$SERVER_URL" in
      http://*|https://*) break ;;
      *) echo "  ERROR: URL must start with http:// or https://" ;;
    esac
  done

  case "$SERVER_URL" in
    https://*) ;;
    http://localhost|http://localhost:*|http://localhost/*|http://127.0.0.1|http://127.0.0.1:*|http://127.0.0.1/*|http://\[::1\]|http://\[::1\]:*|http://\[::1\]/*) ;;
    http://*)
      echo
      echo "  SECURITY WARNING: this remote HTTP URL will send the Agent token without encryption."
      echo "  Prefer HTTPS through Cloudflare Tunnel, Caddy, or Nginx."
      read_prompt CONTINUE_PLAINTEXT "Allow plaintext HTTP anyway? [y/N] "
      case "$CONTINUE_PLAINTEXT" in
        y|Y|yes|YES) ALLOW_PLAIN_HTTP=true ;;
        *) echo "aborted"; exit 0 ;;
      esac
      ;;
  esac

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

  configure_intervals install

  configure_billing

  configure_tcping
  configure_monthly_traffic

  cat > "$TMP_DIR/agent.yaml" <<YAML
hostname: "${HOSTNAME}"
server: ${SERVER_URL}
token: ${TOKEN}
region: ${REGION}
tls_skip_verify: false
allow_plain_http: ${ALLOW_PLAIN_HTTP}
YAML
  cat "$TMP_DIR/intervals.yaml" "$TMP_DIR/billing.yaml" "$TMP_DIR/tcpping.yaml" "$TMP_DIR/traffic.yaml" >> "$TMP_DIR/agent.yaml"
  chmod 600 "$TMP_DIR/agent.yaml"

  echo "Installing Agent and unprivileged systemd service..."
  deploy_agent

  echo
  echo "========================================="
  echo " Needle Agent installed!"
  echo " Version:  $VERSION"
  echo " Hostname: ${HOSTNAME:-auto-detected}"
  echo " Region:   $REGION"
  echo " Server:   $SERVER_URL"
  echo " Config:   $AGENT_YAML"
  echo " Run as:   $AGENT_USER (root manages installation and systemd)"
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
  require_linux_runtime

  UPGRADE_CONFIG_MODE=ask
  while [ $# -gt 0 ]; do
    case "$1" in
      --keep-config|--reconfigure|--configure-intervals)
        [ "$UPGRADE_CONFIG_MODE" = ask ] || { echo 'Choose only one configuration option.' >&2; exit 1; }
        if [ "$1" = --keep-config ]; then UPGRADE_CONFIG_MODE=keep; elif [ "$1" = --configure-intervals ]; then UPGRADE_CONFIG_MODE=intervals; else UPGRADE_CONFIG_MODE=reconfigure; fi
        ;;
      -h|--help) usage; return ;;
      *) echo "Unknown upgrade option: $1" >&2; exit 1 ;;
    esac
    shift
  done
  download_release_agent
  choose_upgrade_config "$AGENT_YAML"
  if [ "$UPGRADE_RECONFIGURE" != true ]; then
    cp "$AGENT_YAML" "$TMP_DIR/agent.yaml"
  fi
  chmod 600 "$TMP_DIR/agent.yaml"
  echo "Updating Agent, permissions and unprivileged systemd service..."
  deploy_agent

  echo
  echo "========================================="
  echo " Needle Agent upgraded to $VERSION!"
  echo " Run as: $AGENT_USER; existing running/autostart state preserved."
  if [ "$UPGRADE_RECONFIGURE" = true ]; then
    echo " Selected settings updated; token and identity preserved. Backup: $AGENT_YAML.bak"
  else
    echo " Config preserved: $AGENT_YAML"
  fi
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
      echo "(local only; Server data is always kept; --unregister revokes this credential)"
    fi
    read_prompt ans "Continue? [y/N] "
    case "$ans" in
      y|Y|yes|YES) ;;
      *) echo "aborted"; exit 0 ;;
    esac
  fi

  require_linux_runtime
  if getent passwd "$AGENT_USER" >/dev/null; then
    validate_agent_account || { echo 'ERROR: refusing to remove an unrelated needle-agent account.' >&2; exit 1; }
  fi

  if [ "$do_unregister" = true ]; then
    unregister_from_server
  fi

  echo "Stopping service..."
  if [ -f "$SERVICE_FILE" ] || systemctl is-active --quiet "$SERVICE_NAME"; then systemctl stop "$SERVICE_NAME"; fi
  systemctl disable "$SERVICE_NAME" 2>/dev/null || true
  rm -f "$SERVICE_FILE"
  systemctl daemon-reload 2>/dev/null || true

  echo "Removing $INSTALL_DIR ..."
  rm -rf "$INSTALL_DIR"
  if getent passwd "$AGENT_USER" >/dev/null; then userdel "$AGENT_USER"; fi
  if getent group "$AGENT_GROUP" >/dev/null; then groupdel "$AGENT_GROUP"; fi

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
  upgrade)   cmd_upgrade "$@"; exit 0 ;;
  uninstall) cmd_uninstall "$@"; exit 0 ;;
  status)    cmd_status; exit 0 ;;
  help|-h|--help) usage; exit 0 ;;
esac
