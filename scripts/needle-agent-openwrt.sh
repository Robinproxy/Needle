#!/bin/sh
# OpenWrt / ImmortalWrt Agent installer; compatible with BusyBox ash.
set -eu
umask 077

REPO=Robinproxy/Needle
BINARY=/usr/bin/needle-agent
CONFIG_DIR=/etc/needle
CONFIG=$CONFIG_DIR/agent.yaml
SERVICE=/etc/init.d/needle-agent
BACKUP_LIST=/etc/sysupgrade.conf
NETWORK_DIR=/sys/class/net
MEMINFO=/proc/meminfo
RANDOM_UUID=/proc/sys/kernel/random/uuid
RC_DIR=/etc/rc.d
LOCK_DIR=/tmp/needle-agent-installer.lock
LOCK_HELD=false
TMP_DIR=
VERSION=
YES=false
PURGE=false
UNREGISTER=false
INSTALL_PENDING=false
INSTALL_MUTATED=false
SERVICE_TOUCHED=false
UNREGISTER_PENDING=false
KEEP_TMP=false
UPGRADE_CONFIG_MODE=ask

fail() { echo "ERROR: $*" >&2; exit 1; }
cleanup() {
    if [ "$UNREGISTER_PENDING" = true ]; then
        UNREGISTER_PENDING=false
        if [ "$UNREGISTER_WAS_RUNNING" = true ] && [ -x "$SERVICE" ]; then
            "$SERVICE" start || echo 'ERROR: could not restart the retained Agent.' >&2
        fi
    fi
    if [ "$INSTALL_PENDING" = true ]; then
        if ! rollback_install; then
            KEEP_TMP=true
            echo "ERROR: rollback incomplete. Recovery files retained in: $TMP_DIR" >&2
        fi
    fi
    if [ "$INSTALL_MUTATED" = true ]; then
        rm -f "$BINARY.new" "$CONFIG.new" "$CONFIG.bak.new" "$SERVICE.new" "$BACKUP_LIST.needle.tmp"
        INSTALL_MUTATED=false
    fi
    if [ -n "$TMP_DIR" ] && [ "$KEEP_TMP" = false ]; then rm -rf "$TMP_DIR"; fi
    TMP_DIR=
    if [ "$LOCK_HELD" = true ]; then
        rm -f "$LOCK_DIR/pid"
        rmdir "$LOCK_DIR" || echo "ERROR: cannot release installer lock: $LOCK_DIR" >&2
        LOCK_HELD=false
    fi
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

usage() {
    cat <<'HELP'
Needle Agent for OpenWrt / ImmortalWrt (x86_64 and aarch64)
Usage: sh needle-agent-openwrt.sh [install|upgrade|status|uninstall] [options]
  --keep-config    Upgrade without changing configuration or prompting
  --reconfigure    Upgrade and reselect probes, allowance, and optional billing; preserve token
  --version vX.Y.Z  Install a specific release (default: latest stable)
  --configure-intervals  Upgrade and change only realtime/history intervals
  --unregister     With uninstall: revoke this Agent credential; keep Server data
  --purge          With uninstall: also delete local configuration
  -y, --yes        Skip uninstall confirmation
Installation requires an interactive terminal. Interactive upgrades offer a config choice;
non-interactive upgrades keep agent.yaml.
HELP
}

require_system() {
    [ "$(id -u)" = 0 ] || fail 'Run as root.'
    [ -f /etc/openwrt_release ] && [ -f /etc/rc.common ] || fail 'OpenWrt / ImmortalWrt with procd is required.'
}

lock_installer() {
    if ! mkdir "$LOCK_DIR" 2>/dev/null; then
        fail "Installer lock exists: $LOCK_DIR. Another operation may be running; inspect its pid file before removing a stale lock."
    fi
    LOCK_HELD=true
    printf '%s\n' "$$" > "$LOCK_DIR/pid"
}

validate_paths() {
    [ ! -L "$CONFIG_DIR" ] || fail "Refusing symlink directory: $CONFIG_DIR"
    [ ! -e "$CONFIG_DIR" ] || [ -d "$CONFIG_DIR" ] || fail "Not a directory: $CONFIG_DIR"
    for FILE in "$BINARY" "$CONFIG" "$CONFIG.bak" "$SERVICE" "$CONFIG_DIR/version" "$BACKUP_LIST"; do
        [ ! -L "$FILE" ] || fail "Refusing symlink: $FILE"
        [ ! -e "$FILE" ] || [ -f "$FILE" ] || fail "Not a regular file: $FILE"
    done
    [ "${ACTION:-}" != uninstall ] || return 0
    for FILE in "$BINARY.new" "$CONFIG.new" "$CONFIG.bak.new" "$SERVICE.new" "$BACKUP_LIST.needle.tmp" \
        "$BINARY.needle-restore" "$CONFIG.needle-restore" "$CONFIG.bak.needle-restore" \
        "$SERVICE.needle-restore" "$CONFIG_DIR/version.needle-restore" "$BACKUP_LIST.needle-restore"; do
        [ ! -e "$FILE" ] && [ ! -L "$FILE" ] || fail "Staging/recovery path already exists; inspect it before retrying: $FILE"
    done
}

wait_stopped() {
    STOP_WAIT=0
    while "$SERVICE" running >/dev/null 2>&1; do
        [ "$STOP_WAIT" -lt 30 ] || { echo 'Agent did not stop; local files retained.' >&2; return 1; }
        sleep 1
        STOP_WAIT=$((STOP_WAIT + 1))
    done
}

stop_agent() {
    "$SERVICE" stop || return 1
    wait_stopped
}

remove_boot_links() {
    for LINK in "$RC_DIR"/S[0-9][0-9]needle-agent "$RC_DIR"/K[0-9][0-9]needle-agent; do
        [ ! -L "$LINK" ] || rm -f "$LINK"
    done
}

fetch() {
    if command -v curl >/dev/null 2>&1; then
        curl -fLsS --connect-timeout 15 --max-time 180 "$1" -o "$2"
    elif command -v wget >/dev/null 2>&1; then
        wget -T 30 -qO "$2" "$1"
    else
        fail 'Install curl or wget and CA certificates using your package manager.'
    fi
}

prompt() {
    printf '%s' "$1" >&2
    IFS= read -r ANSWER || fail 'Interactive input required; download the script and run it with sh.'
}

yaml_quote() {
    # YAML single-quoted scalars escape apostrophes by doubling them.
    printf "'%s'" "$(printf '%s' "$1" | sed "s/'/''/g")"
}

preflight_resources() {
    # Downloads, extracted binaries and recovery snapshots occupy tmpfs RAM.
    # Refuse before downloading when the router already has little headroom.
    AVAILABLE_KB=$(awk '$1 == "MemAvailable:" { print $2; exit }' "$MEMINFO")
    case "$AVAILABLE_KB" in ''|*[!0-9]*) fail 'Cannot determine available router memory.' ;; esac
    [ "$AVAILABLE_KB" -ge 65536 ] || fail 'At least 64 MiB of available memory is required before installation; running Agent is unchanged.'
    TMP_FREE_KB=$(df -Pk /tmp | awk 'END { print $4 }')
    case "$TMP_FREE_KB" in ''|*[!0-9]*) fail 'Cannot determine free temporary space.' ;; esac
    [ "$TMP_FREE_KB" -ge 32768 ] || fail 'At least 32 MiB of free /tmp space is required for download and recovery files.'
}

download() {
    case "$(uname -m)" in
        x86_64|amd64) GOARCH=amd64 ;;
        aarch64|arm64) GOARCH=arm64 ;;
        *) fail 'This release supports only x86_64 and aarch64.' ;;
    esac
    command -v sha256sum >/dev/null 2>&1 || fail 'sha256sum is required.'
    TMP_DIR=$(mktemp -d /tmp/needle-agent.XXXXXX)
    if [ -z "$VERSION" ]; then
        fetch "https://api.github.com/repos/$REPO/releases/latest" "$TMP_DIR/latest.json"
        VERSION=$(sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$TMP_DIR/latest.json" | head -n 1)
    fi
    printf '%s\n' "$VERSION" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$' || fail 'Expected a stable version such as v0.6.5.'
    ARCHIVE="needle-agent-linux-$GOARCH.tar.gz"
    URL="https://github.com/$REPO/releases/download/$VERSION/$ARCHIVE"
    echo "Downloading $VERSION ($GOARCH)..."
    fetch "$URL" "$TMP_DIR/$ARCHIVE" || fail 'Agent-only package unavailable. Choose a release that includes the OpenWrt installer.'
    fetch "$URL.sha256" "$TMP_DIR/expected.sha256" || fail 'Cannot obtain checksum; installation aborted.'
    EXPECTED=$(awk 'NR == 1 {print $1}' "$TMP_DIR/expected.sha256")
    [ "${#EXPECTED}" = 64 ] || fail 'Invalid checksum.'
    case "$EXPECTED" in *[!0-9a-fA-F]*) fail 'Invalid checksum.' ;; esac
    ACTUAL=$(sha256sum "$TMP_DIR/$ARCHIVE" | awk '{print $1}')
    [ "$EXPECTED" = "$ACTUAL" ] || fail 'Checksum mismatch; installation aborted.'
    # Write bytes only: archive permissions, symlinks and hardlinks must not
    # be applied to the router filesystem, even inside the staging directory.
    tar xOzf "$TMP_DIR/$ARCHIVE" needle-agent > "$TMP_DIR/needle-agent"
    [ -s "$TMP_DIR/needle-agent" ] && [ ! -L "$TMP_DIR/needle-agent" ] || fail 'Agent binary missing or empty.'
    chmod 755 "$TMP_DIR/needle-agent"
    "$TMP_DIR/needle-agent" -installer-protocol 2>/dev/null | grep -q '^needle-install-v2 .*server-profiles-v1' || fail 'Release binary is older than this installer protocol; use its packaged installer or wait for a compatible release. Existing Agent unchanged.'
    "$TMP_DIR/needle-agent" -h > "$TMP_DIR/agent-help" 2>&1 || fail 'Agent binary cannot execute on this router.'
}

preflight_install() {
    # A full overlay can affect other router services. Reserve room beyond
    # the new binary; all downloads and snapshots remain in temporary RAM.
    FREE_KB=$(df -Pk "${BINARY%/*}" | awk 'END { print $4 }')
    case "$FREE_KB" in ''|*[!0-9]*) fail 'Cannot determine free installation space.' ;; esac
    BINARY_BYTES=$(wc -c < "$TMP_DIR/needle-agent")
    NEEDED_KB=$((BINARY_BYTES / 1024 + 1 + 4096))
    [ "$FREE_KB" -ge "$NEEDED_KB" ] || fail 'Insufficient free space; keep at least 4 MiB free after staging the Agent.'
    if grep -q '^[[:space:]]*-check-config' "$TMP_DIR/agent-help"; then
        CHECK_CONFIG=$CONFIG
        [ ! -f "$TMP_DIR/agent.yaml" ] || CHECK_CONFIG=$TMP_DIR/agent.yaml
        "$TMP_DIR/needle-agent" -check-config "$CHECK_CONFIG" || fail 'Configuration validation failed; running Agent is unchanged.'
    fi
}

require_interactive() {
    [ -t 0 ] || fail 'Download the script, then run: sh /tmp/needle-agent-openwrt.sh install'
}

tcping_prompt() { prompt "$1"; TCPING_ANSWER=$ANSWER; }

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
    PROFILE_MANAGED=false
    if awk '/^[[:space:]]*["\047]?server_managed["\047]?:[[:space:]]*true([[:space:]]*(#.*)?)?$/ { found=1 } END { exit !found }' "$1"; then
        PROFILE_MANAGED=true
    fi
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
                if [ "$PROFILE_MANAGED" = true ]; then
                    echo '  2) Reconfigure TCP Ping and intervals; preserve token and identity'
                else
                    echo '  2) Reconfigure TCP Ping, legacy allowance/billing, and intervals; preserve token and identity'
                fi
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
            if [ "$PROFILE_MANAGED" = false ]; then
                configure_monthly_traffic upgrade
                configure_billing upgrade
            else
                echo 'Node profile is managed on the Server; update billing and allowance in /admin.'
            fi
        fi
        configure_intervals upgrade "$1"
        stage_tcping_config "$1"
    fi
}
# End of TCP Ping selection.

generate_token() {
    # OpenWrt's minimal BusyBox may omit od/hexdump. The kernel provides a
    # fresh random UUID for each read, without installing another package.
    IFS= read -r GENERATED_UUID < "$RANDOM_UUID" || return 1
    GENERATED_TOKEN=$(printf '%s' "$GENERATED_UUID" | tr -d '-') || return 1
    [ "${#GENERATED_TOKEN}" -eq 32 ] || return 1
    case "$GENERATED_TOKEN" in *[!0-9a-fA-F]*) return 1 ;; esac
    printf '%s' "$GENERATED_TOKEN"
}

configure() {
    require_interactive
    prompt 'Hostname (empty = system hostname): '; HOSTNAME=$ANSWER
    prompt 'Server URL (HTTPS recommended): '; SERVER_URL=$ANSWER
    ALLOW_HTTP=false
    case "$SERVER_URL" in
        https://?*) ;;
        http://?*)
            prompt 'Allow plaintext HTTP, which exposes the Agent token? [y/N]: '
            case "$ANSWER" in y|Y|yes) ALLOW_HTTP=true ;; *) fail 'HTTPS required unless HTTP is explicitly allowed.' ;; esac ;;
        *) fail 'A full http:// or https:// URL is required.' ;;
    esac
    prompt 'Agent token (empty = generate): '; TOKEN=$ANSWER
    if [ -z "$TOKEN" ]; then
        TOKEN=$(generate_token) || fail 'Cannot generate an Agent token; run install again and enter a unique token manually.'
    fi
    configure_intervals install
    echo 'Available network interfaces:'
    for DEVICE in "$NETWORK_DIR"/*; do printf '  %s\n' "${DEVICE##*/}"; done
    DEFAULT_IFACE=
    if command -v ubus >/dev/null 2>&1 && command -v jsonfilter >/dev/null 2>&1; then
        DEFAULT_IFACE=$(ubus call network.interface.wan status 2>/dev/null | jsonfilter -e '@.l3_device' 2>/dev/null || true)
    fi
    echo 'Select one WAN interface (e.g. pppoe-wan or eth1) to avoid counting LAN/WAN traffic twice.'
    prompt "Traffic interface [${DEFAULT_IFACE:-required}]: "; IFACE=${ANSWER:-$DEFAULT_IFACE}
    case "$IFACE" in ''|lo|*[!a-zA-Z0-9_.:-]*) fail 'Select a valid WAN interface.' ;; esac
    [ -d "$NETWORK_DIR/$IFACE" ] || fail "Interface $IFACE does not exist; connect WAN before installation."
    configure_tcping
    {
        printf 'hostname: '; yaml_quote "$HOSTNAME"; printf '\nserver: '; yaml_quote "$SERVER_URL"
        printf '\ntoken: '; yaml_quote "$TOKEN"; printf '\nserver_managed: true'
        printf '\ntls_skip_verify: false\nallow_plain_http: %s\ninterfaces: [' "$ALLOW_HTTP"
        yaml_quote "$IFACE"; printf ']\n'
        cat "$TMP_DIR/intervals.yaml"
        cat "$TMP_DIR/tcpping.yaml"
    } > "$TMP_DIR/agent.yaml"
}

write_service() {
    cat > "$SERVICE.new" <<'INIT'
#!/bin/sh /etc/rc.common
START=99
STOP=10
USE_PROCD=1
start_service() {
    [ -s /etc/needle/agent.yaml ] || return 1
    procd_open_instance
    procd_set_param command /usr/bin/needle-agent /etc/needle/agent.yaml
    procd_set_param respawn 3600 10 5
    procd_set_param nice 10
    procd_set_param term_timeout 25
    procd_set_param env GOMAXPROCS=1 GOMEMLIMIT=32MiB
    procd_set_param limits core="0" nofile="128 128"
    procd_set_param file /etc/needle/agent.yaml
    procd_set_param stdout 1
    procd_set_param stderr 1
    procd_close_instance
}
INIT
    chmod 755 "$SERVICE.new"
    mv -f "$SERVICE.new" "$SERVICE"
}

preserve_config() {
    if [ -f "$BACKUP_LIST" ]; then
        if grep -qxF "$CONFIG_DIR/" "$BACKUP_LIST"; then return; fi
        cp -p "$BACKUP_LIST" "$BACKUP_LIST.needle.tmp"
        if [ -s "$BACKUP_LIST" ] && [ -n "$(tail -c 1 "$BACKUP_LIST")" ]; then
            printf '\n' >> "$BACKUP_LIST.needle.tmp"
        fi
    else
        : > "$BACKUP_LIST.needle.tmp"
    fi
    printf '%s\n' "$CONFIG_DIR/" >> "$BACKUP_LIST.needle.tmp"
    mv -f "$BACKUP_LIST.needle.tmp" "$BACKUP_LIST"
}

backup_install() {
    OLD_RUNNING=false
    OLD_ENABLED=false
    OLD_CONFIG_DIR=false
    if [ -d "$CONFIG_DIR" ]; then
        OLD_CONFIG_DIR=true
    fi
    OLD_SERVICE=false
    if [ -x "$SERVICE" ]; then
        OLD_SERVICE=true
        if "$SERVICE" running >/dev/null 2>&1; then OLD_RUNNING=true; fi
        if "$SERVICE" enabled >/dev/null 2>&1; then OLD_ENABLED=true; fi
    fi
    for ITEM in binary config config-backup service version backup-list; do
        case "$ITEM" in
            binary) FILE=$BINARY ;;
            config) FILE=$CONFIG ;;
            config-backup) FILE=$CONFIG.bak ;;
            service) FILE=$SERVICE ;;
            version) FILE=$CONFIG_DIR/version ;;
            backup-list) FILE=$BACKUP_LIST ;;
        esac
        if [ -f "$FILE" ]; then cp -p "$FILE" "$TMP_DIR/old-$ITEM"; fi
    done
}

rollback_install() {
    INSTALL_PENDING=false
    echo 'Installation failed; restoring the previous Agent installation.' >&2
    if [ "$SERVICE_TOUCHED" = true ] && [ -x "$SERVICE" ]; then
        stop_agent || return 1
        "$SERVICE" disable || return 1
    fi
    for ITEM in binary config config-backup service version backup-list; do
        case "$ITEM" in
            binary) FILE=$BINARY ;;
            config) FILE=$CONFIG ;;
            config-backup) FILE=$CONFIG.bak ;;
            service) FILE=$SERVICE ;;
            version) FILE=$CONFIG_DIR/version ;;
            backup-list) FILE=$BACKUP_LIST ;;
        esac
        if [ -f "$TMP_DIR/old-$ITEM" ]; then
            cp -p "$TMP_DIR/old-$ITEM" "$FILE.needle-restore" || return 1
            mv -f "$FILE.needle-restore" "$FILE" || return 1
        else
            rm -f "$FILE" || return 1
        fi
    done
    rm -f "$BINARY.new"
    if [ "$SERVICE_TOUCHED" = true ] && [ -x "$SERVICE" ]; then
        if [ "$OLD_ENABLED" = true ]; then
            "$SERVICE" enable || return 1
        else
            "$SERVICE" disable || return 1
        fi
        if [ "$OLD_RUNNING" = true ]; then
            "$SERVICE" start || return 1
            sleep 1
            "$SERVICE" running >/dev/null 2>&1 || return 1
        fi
    fi
    if [ "$OLD_CONFIG_DIR" = false ]; then
        rmdir "$CONFIG_DIR" 2>/dev/null || true
    fi
}

install_agent() {
    validate_paths
    if [ -f "$CONFIG" ]; then
        echo 'Existing configuration found.'
    else
        [ "$ACTION" = install ] || fail 'Not installed; run install first.'
        require_interactive
    fi
    preflight_resources
    download
    if [ ! -f "$CONFIG" ]; then
        configure
    elif [ "$ACTION" = upgrade ]; then
        choose_upgrade_config "$CONFIG"
    fi
    preflight_install
    backup_install
    INSTALL_PENDING=true
    INSTALL_MUTATED=true
    mkdir -p "$CONFIG_DIR"
    # Keep existing directory permissions; it may contain user-owned files.
    # New directories inherit umask 077, and Agent configs are always 0600.
    # Stage on the destination filesystem before stopping the old process.
    cp "$TMP_DIR/needle-agent" "$BINARY.new"
    chmod 755 "$BINARY.new"
    if [ "${UPGRADE_RECONFIGURE:-false}" = true ]; then
        cp -p "$CONFIG" "$CONFIG.bak.new"
        chmod 600 "$CONFIG.bak.new"
        mv -f "$CONFIG.bak.new" "$CONFIG.bak"
    fi
    SERVICE_TOUCHED=true
    if [ -x "$SERVICE" ]; then stop_agent; fi
    mv -f "$BINARY.new" "$BINARY"
    if [ -f "$TMP_DIR/agent.yaml" ]; then
        cp "$TMP_DIR/agent.yaml" "$CONFIG.new"
        chmod 600 "$CONFIG.new"
        mv -f "$CONFIG.new" "$CONFIG"
    fi
    chmod 600 "$CONFIG"
    write_service
    preserve_config
    if [ "$OLD_SERVICE" = false ] || [ "$OLD_ENABLED" = true ]; then
        "$SERVICE" enable
    else
        "$SERVICE" disable
    fi
    if [ "$OLD_SERVICE" = false ] || [ "$OLD_RUNNING" = true ]; then
        "$SERVICE" start
        # procd starts asynchronously; reject immediately crashing processes.
        sleep 1
        "$SERVICE" running >/dev/null 2>&1 || fail 'Agent did not stay running; check logread -e needle-agent.'
    fi
    printf '%s\n' "$VERSION" > "$CONFIG_DIR/version"
    INSTALL_PENDING=false
    echo "Installed $VERSION. Configuration: $CONFIG"
    echo 'Existing service running/autostart state was preserved; a new installation starts at boot.'
    if [ "${UPGRADE_RECONFIGURE:-false}" = true ]; then
        echo "Selected settings updated; token and identity preserved. Backup: $CONFIG.bak"
    fi
    if [ -n "${TOKEN:-}" ]; then
        # Quote the current installation's token for safe command copying.
        REGISTRATION_TOKEN=$(printf '%s' "$TOKEN" | sed "s/'/'\"'\"'/g")
        echo 'Run ONE of these commands on the Server host to allow this Agent:'
        echo '  Docker Compose Server (run from its Compose directory):'
        printf "    docker compose exec needle-server needle-server -db /data/needle.db allow-token '%s'\n" "$REGISTRATION_TOKEN"
        echo '  Binary Server (default installation paths):'
        printf "    sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db allow-token '%s'\n" "$REGISTRATION_TOKEN"
        echo '  Hostname binds automatically on the first successful report.'
    fi
    echo 'Logs: logread -e needle-agent'
    echo 'Configure display name, region, billing and traffic allowance in the Server /admin page.'
    echo 'Firmware sysupgrade preserves configuration when keeping settings; reinstall the Agent afterward.'
}

uninstall_agent() {
    if [ "$YES" != true ]; then
        prompt 'Remove Needle Agent? Configuration is kept unless --purge is specified. [y/N]: '
        case "$ANSWER" in y|Y|yes) ;; *) return ;; esac
    fi
    # Only dedicated Needle paths are touched; never follow a symlink into
    # another router component or recursively remove a shared directory.
    validate_paths
    if [ "$UNREGISTER" = true ]; then
        [ -x "$BINARY" ] && [ -f "$CONFIG" ] || fail 'Binary and configuration are needed to unregister.'
        "$BINARY" -installer-protocol 2>/dev/null | grep -q '^needle-install-v2 ' || fail 'Upgrade Server and Agent before --unregister; older Agents may delete Server history. Plain local uninstall remains available.'
        UNREGISTER_WAS_RUNNING=false
        if [ -x "$SERVICE" ] && "$SERVICE" running >/dev/null 2>&1; then UNREGISTER_WAS_RUNNING=true; fi
        UNREGISTER_PENDING=true
        if [ -x "$SERVICE" ]; then stop_agent; fi
        if ! "$BINARY" -unregister "$CONFIG"; then
            fail 'Unregister failed; local installation retained and previous running state will be restored.'
        fi
        UNREGISTER_PENDING=false
    fi
    if [ -x "$SERVICE" ]; then stop_agent; "$SERVICE" disable; fi
    remove_boot_links
    rm -f "$SERVICE" "$SERVICE.new" "$SERVICE.needle-restore" "$BINARY" "$BINARY.new" "$BINARY.needle-restore" \
        "$CONFIG_DIR/version" "$CONFIG_DIR/version.needle-restore"
    if [ "$PURGE" = true ]; then
        rm -f "$CONFIG" "$CONFIG.new" "$CONFIG.bak" "$CONFIG.bak.new" "$CONFIG.needle-restore" "$CONFIG.bak.needle-restore"
        if [ -d "$CONFIG_DIR" ] && ! rmdir "$CONFIG_DIR" 2>/dev/null; then
            echo "Other files retained in: $CONFIG_DIR"
        fi
        rm -f "$BACKUP_LIST.needle.tmp" "$BACKUP_LIST.needle-restore"
        if [ -f "$BACKUP_LIST" ] && grep -qxF "$CONFIG_DIR/" "$BACKUP_LIST"; then
            cp -p "$BACKUP_LIST" "$BACKUP_LIST.needle.tmp"
            awk -v entry="$CONFIG_DIR/" '$0 != entry { print }' "$BACKUP_LIST" > "$BACKUP_LIST.needle.tmp"
            mv -f "$BACKUP_LIST.needle.tmp" "$BACKUP_LIST"
        fi
    fi
    echo 'Agent removed. Server history is retained; --unregister only revokes this credential.'
}

main() {
    VERSION=
    YES=false
    PURGE=false
    UNREGISTER=false
    UPGRADE_RECONFIGURE=false
    SERVICE_TOUCHED=false
    KEEP_TMP=false
    UPGRADE_CONFIG_MODE=ask
    ACTION=${1:-install}
    [ "$#" -eq 0 ] || shift
    case "$ACTION" in help|-h|--help) usage; return ;; install|upgrade|status|uninstall) ;; *) usage; exit 1 ;; esac
    while [ "$#" -gt 0 ]; do
        case "$1" in
            --keep-config|--reconfigure|--configure-intervals)
                [ "$UPGRADE_CONFIG_MODE" = ask ] || fail 'Choose only one configuration option.'
                if [ "$1" = --keep-config ]; then UPGRADE_CONFIG_MODE=keep; elif [ "$1" = --configure-intervals ]; then UPGRADE_CONFIG_MODE=intervals; else UPGRADE_CONFIG_MODE=reconfigure; fi
                ;;
            --version) [ "$#" -ge 2 ] || fail '--version needs a value.'; VERSION=$2; shift ;;
            -y|--yes) YES=true ;;
            --purge) PURGE=true ;;
            --unregister) UNREGISTER=true ;;
            *) fail "Unknown option: $1" ;;
        esac
        shift
    done
    if [ "$ACTION" != uninstall ] && { [ "$PURGE" = true ] || [ "$UNREGISTER" = true ] || [ "$YES" = true ]; }; then
        fail '--purge, --unregister and --yes are only valid with uninstall.'
    fi
    if [ "$UPGRADE_CONFIG_MODE" != ask ] && [ "$ACTION" != upgrade ]; then
        fail '--keep-config, --reconfigure and --configure-intervals are only valid with upgrade.'
    fi
    require_system
    if [ "$ACTION" != status ]; then lock_installer; fi
    case "$ACTION" in
        install|upgrade) install_agent ;;
        uninstall) uninstall_agent ;;
        status)
            if [ -f "$CONFIG_DIR/version" ]; then printf 'Version: '; cat "$CONFIG_DIR/version"; fi
            echo "Config: $CONFIG"
            [ -x "$SERVICE" ] || fail 'Service is not installed.'
            "$SERVICE" status
            ;;
    esac
    cleanup
}

main "$@"
