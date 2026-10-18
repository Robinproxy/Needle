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
TMP_DIR=
VERSION=
YES=false
PURGE=false
UNREGISTER=false
INSTALL_PENDING=false

fail() { echo "ERROR: $*" >&2; exit 1; }
cleanup() {
    if [ "$INSTALL_PENDING" = true ]; then
        rollback_install || echo 'ERROR: rollback incomplete; inspect the Agent installation.' >&2
    fi
    [ -z "$TMP_DIR" ] || rm -rf "$TMP_DIR"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

usage() {
    cat <<'HELP'
Needle Agent for OpenWrt / ImmortalWrt (x86_64 and aarch64)
Usage: sh needle-agent-openwrt.sh [install|upgrade|status|uninstall] [options]
  --version vX.Y.Z  Install a specific release (default: latest stable)
  --unregister     With uninstall: remove the node from the Server first
  --purge          With uninstall: also delete local configuration
  -y, --yes        Skip uninstall confirmation
Installation requires an interactive terminal. Upgrade preserves agent.yaml.
HELP
}

require_system() {
    [ "$(id -u)" = 0 ] || fail 'Run as root.'
    [ -f /etc/openwrt_release ] && [ -f /etc/rc.common ] || fail 'OpenWrt / ImmortalWrt with procd is required.'
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
    # Extract only the binary from the verified package.
    tar xzf "$TMP_DIR/$ARCHIVE" -C "$TMP_DIR" needle-agent
    [ -f "$TMP_DIR/needle-agent" ] && [ ! -L "$TMP_DIR/needle-agent" ] || fail 'Agent binary missing.'
    chmod 755 "$TMP_DIR/needle-agent"
    "$TMP_DIR/needle-agent" -h >/dev/null 2>&1 || fail 'Agent binary cannot execute on this router.'
}

require_interactive() {
    [ -t 0 ] || fail 'Download the script, then run: sh /tmp/needle-agent-openwrt.sh install'
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
    prompt 'Region code [CN]: '; REGION=${ANSWER:-CN}
    prompt 'Agent token (empty = generate): '; TOKEN=$ANSWER
    if [ -z "$TOKEN" ]; then TOKEN=$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n'); fi
    prompt 'Report interval in seconds [30]: '; INTERVAL=${ANSWER:-30}
    case "$INTERVAL" in ''|*[!0-9]*) fail 'Interval must be a positive integer.' ;; esac
    [ "$INTERVAL" -gt 0 ] && [ "$INTERVAL" -le 86400 ] || fail 'Interval must be 1–86400 seconds.'
    echo 'Billing: forever (default), 1m, 3m, 6m, 12m, or skip'
    prompt 'Billing period [forever]: '; PERIOD=${ANSWER:-forever}; EXPIRY=
    case "$PERIOD" in
        forever) ;;
        skip) PERIOD= ;;
        1m|3m|6m|12m)
            prompt 'Next renewal date (YYYY-MM-DD): '; EXPIRY=$ANSWER
            printf '%s\n' "$EXPIRY" | grep -Eq '^[0-9]{4}-[0-9]{2}-[0-9]{2}$' || fail 'Expected YYYY-MM-DD.'
            CHECK_DATE=$(date -d "$EXPIRY" '+%Y-%m-%d' 2>/dev/null || true)
            [ "$CHECK_DATE" = "$EXPIRY" ] || fail 'Invalid renewal date.' ;;
        *) fail 'Unsupported billing period.' ;;
    esac
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
    {
        printf 'hostname: '; yaml_quote "$HOSTNAME"; printf '\nserver: '; yaml_quote "$SERVER_URL"
        printf '\ntoken: '; yaml_quote "$TOKEN"; printf '\nregion: '; yaml_quote "$REGION"
        printf '\nbilling_period: '; yaml_quote "$PERIOD"; printf '\nexpires_at: '; yaml_quote "$EXPIRY"
        printf '\ninterval: %s\ntls_skip_verify: false\nallow_plain_http: %s\ninterfaces: [' "$INTERVAL" "$ALLOW_HTTP"
        yaml_quote "$IFACE"; printf ']\ntcpping:\n'
        for CARRIER in cm cu ct; do
            case "$CARRIER" in cm) NAME=CMv4 ;; cu) NAME=CUv4 ;; ct) NAME=CTv4 ;; esac
            printf '  - name: "%s"\n    target: "sh-%s-v4.ip.zstaticcdn.com:80"\n' "$NAME" "$CARRIER"
        done
    } > "$TMP_DIR/agent.yaml"
}

write_service() {
    cat > "$SERVICE" <<'INIT'
#!/bin/sh /etc/rc.common
START=99
STOP=10
USE_PROCD=1
start_service() {
    [ -s /etc/needle/agent.yaml ] || return 1
    procd_open_instance
    procd_set_param command /usr/bin/needle-agent /etc/needle/agent.yaml
    procd_set_param respawn 3600 10 0
    procd_set_param file /etc/needle/agent.yaml
    procd_set_param stdout 1
    procd_set_param stderr 1
    procd_close_instance
}
INIT
    chmod 755 "$SERVICE"
}

preserve_config() {
    touch "$BACKUP_LIST"
    grep -qxF "$CONFIG_DIR/" "$BACKUP_LIST" || printf '%s\n' "$CONFIG_DIR/" >> "$BACKUP_LIST"
}

backup_install() {
    OLD_RUNNING=false
    OLD_ENABLED=false
    OLD_CONFIG_DIR=false
    [ ! -d "$CONFIG_DIR" ] || OLD_CONFIG_DIR=true
    if [ -x "$SERVICE" ]; then
        if "$SERVICE" running >/dev/null 2>&1; then OLD_RUNNING=true; fi
        if "$SERVICE" enabled >/dev/null 2>&1; then OLD_ENABLED=true; fi
    fi
    for ITEM in binary config service version backup-list; do
        case "$ITEM" in
            binary) FILE=$BINARY ;;
            config) FILE=$CONFIG ;;
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
    if [ -x "$SERVICE" ]; then
        "$SERVICE" stop >/dev/null 2>&1 || true
        "$SERVICE" disable >/dev/null 2>&1 || true
    fi
    for ITEM in binary config service version backup-list; do
        case "$ITEM" in
            binary) FILE=$BINARY ;;
            config) FILE=$CONFIG ;;
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
    if [ -x "$SERVICE" ]; then
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
    if [ "$OLD_CONFIG_DIR" = false ]; then rmdir "$CONFIG_DIR" 2>/dev/null || true; fi
}

install_agent() {
    if [ -f "$CONFIG" ]; then
        echo 'Existing configuration found; preserving it.'
    else
        [ "$ACTION" = install ] || fail 'Not installed; run install first.'
        require_interactive
    fi
    download
    if [ ! -f "$CONFIG" ]; then configure; fi
    backup_install
    mkdir -p "$CONFIG_DIR"
    chmod 700 "$CONFIG_DIR"
    # Stage on the destination filesystem before stopping the old process.
    cp "$TMP_DIR/needle-agent" "$BINARY.new"
    chmod 755 "$BINARY.new"
    INSTALL_PENDING=true
    if [ -x "$SERVICE" ]; then "$SERVICE" stop; fi
    mv -f "$BINARY.new" "$BINARY"
    if [ -f "$TMP_DIR/agent.yaml" ]; then mv "$TMP_DIR/agent.yaml" "$CONFIG"; fi
    chmod 600 "$CONFIG"
    write_service
    preserve_config
    "$SERVICE" enable
    "$SERVICE" start
    # procd start is asynchronous: reject processes that immediately crash,
    # including bad YAML and invalid Server URLs, before reporting success.
    sleep 1
    "$SERVICE" running >/dev/null 2>&1 || fail 'Agent did not stay running; check logread -e needle-agent.'
    printf '%s\n' "$VERSION" > "$CONFIG_DIR/version"
    INSTALL_PENDING=false
    echo "Installed $VERSION. Configuration: $CONFIG"
    if [ -n "${TOKEN:-}" ]; then
        echo 'Allow this unique token on the Server (upgrade Server to v0.6.4+ for forever):'
        printf '  needle-server -db /path/to/needle.db allow-token %s\n' "$TOKEN"
    fi
    echo 'Logs: logread -e needle-agent'
    echo 'Firmware sysupgrade preserves configuration when keeping settings; reinstall the Agent afterward.'
}

uninstall_agent() {
    if [ "$YES" != true ]; then
        prompt 'Remove Needle Agent? Configuration is kept unless --purge is specified. [y/N]: '
        case "$ANSWER" in y|Y|yes) ;; *) return ;; esac
    fi
    if [ "$UNREGISTER" = true ]; then
        [ -x "$BINARY" ] && [ -f "$CONFIG" ] || fail 'Binary and configuration are needed to unregister.'
        if [ -x "$SERVICE" ]; then "$SERVICE" stop; fi
        if ! "$BINARY" -unregister "$CONFIG"; then
            if [ -x "$SERVICE" ]; then "$SERVICE" start; fi
            fail 'Unregister failed; local installation retained and service restarted.'
        fi
    fi
    if [ -x "$SERVICE" ]; then "$SERVICE" stop; "$SERVICE" disable; fi
    rm -f "$SERVICE" "$BINARY" "$BINARY.new" "$CONFIG_DIR/version"
    if [ "$PURGE" = true ]; then
        rm -f "$CONFIG"
        rmdir "$CONFIG_DIR" 2>/dev/null || true
        if [ -f "$BACKUP_LIST" ]; then
            sed '\|^/etc/needle/$|d' "$BACKUP_LIST" > "$BACKUP_LIST.needle.tmp"
            cat "$BACKUP_LIST.needle.tmp" > "$BACKUP_LIST"
            rm -f "$BACKUP_LIST.needle.tmp"
        fi
    fi
    echo 'Agent removed. Server history is retained unless --unregister was used.'
}

main() {
    ACTION=${1:-install}
    [ "$#" -eq 0 ] || shift
    case "$ACTION" in help|-h|--help) usage; return ;; install|upgrade|status|uninstall) ;; *) usage; exit 1 ;; esac
    while [ "$#" -gt 0 ]; do
        case "$1" in
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
    require_system
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
}

main "$@"
