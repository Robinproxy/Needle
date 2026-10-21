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
SERVER_USER="needle-server"
SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
SCRIPT_URL="https://raw.githubusercontent.com/$REPO/main/scripts/needle-server.sh"

usage() {
  cat <<EOF
Needle Server ops script (binary / systemd)

Usage:
  sudo bash needle-server.sh [command] [options]

Commands:
  install              Interactive install (download + .env + systemd)
  upgrade              Upgrade binary, service identity and permissions (keep settings/data)
  uninstall            Remove service + binary; keep data/ and .env (default)
  uninstall --purge    Also remove default data/config (custom paths require manual purge) (data + .env)
  status               Show install/service status
  help                 Show this help

No command:
  If installed → upgrade, else → install

Options:
  -y, --yes            Skip uninstall confirmation
  --purge              With uninstall: delete data and config too

Token / agent CLI (not part of this script):
  sudo $SERVER_BIN -db $DB_PATH allow-token <token>
  sudo $SERVER_BIN -db $DB_PATH list-tokens
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
  echo "  curl -fsSL $SCRIPT_URL -o /tmp/needle-server.sh"
  echo "  # or: wget -qO /tmp/needle-server.sh $SCRIPT_URL"
  echo "  sudo bash /tmp/needle-server.sh install"
  exit 1
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
    echo "ERROR: checksum is required; download aborted." >&2
    exit 1
  fi

  tar xOzf "$TGZ" needle-server > "$TMP_DIR/needle-server"
  if [ ! -f "$TMP_DIR/needle-server" ]; then
    echo "ERROR: needle-server binary not found in archive"
    exit 1
  fi
  chmod 755 "$TMP_DIR/needle-server"
  if ! "$TMP_DIR/needle-server" -installer-protocol 2>/dev/null | grep -q '^needle-install-v2 '; then
    echo 'ERROR: latest release binary is older than this installer protocol. Use the installer packaged with that release, or wait for a compatible release. Existing installation unchanged.' >&2
    exit 1
  fi

}

require_runtime() {
  [ "$(uname -s)" = Linux ] || { echo 'ERROR: Linux/systemd required.' >&2; return 1; }
  local tool
  for tool in systemctl systemd-analyze pgrep getent useradd groupadd userdel groupdel runuser install stat flock python3; do
    command -v "$tool" >/dev/null || { echo "ERROR: required command: $tool" >&2; return 1; }
  done
  umask 077
  exec 9>"/run/${SERVICE_NAME}-install.lock"
  flock -n 9 || { echo 'ERROR: another Server installer is running.' >&2; return 1; }
}

lock_database_administration() {
  local hash
  hash=$(python3 - "$DB_PATH" <<'PYLOCK'
import hashlib,os,sys
print(hashlib.sha256(os.path.abspath(sys.argv[1]).encode()).hexdigest())
PYLOCK
)
  DB_LOCK="/run/needle-server-db-${hash}.lock"
  [ ! -L "$DB_LOCK" ] || { echo 'ERROR: unsafe database administration lock.' >&2; return 1; }
  exec 8>"$DB_LOCK"
  chmod 600 "$DB_LOCK"
  flock -n 8 || { echo 'ERROR: another administrator is using this database; retry after that operation completes.' >&2; return 1; }
}

ensure_server_account() {
  local entry uid gid home shell members
  if ! getent passwd "$SERVER_USER" >/dev/null; then
    ! getent group "$SERVER_USER" >/dev/null || { echo 'ERROR: orphan service group; refusing reuse.' >&2; return 1; }
    groupadd --system "$SERVER_USER"
    useradd --system --gid "$SERVER_USER" --home-dir /nonexistent --no-create-home --shell "$(command -v nologin)" "$SERVER_USER" || { groupdel "$SERVER_USER"; return 1; }
    ACCOUNT_CREATED=true
  fi
  entry=$(getent passwd "$SERVER_USER")
  IFS=: read -r _ _ uid gid _ home shell <<< "$entry"
  members=$(getent group "$SERVER_USER" | cut -d: -f4)
  case "$shell" in */nologin|/bin/false) ;; *) echo 'ERROR: service account allows login.' >&2; return 1 ;; esac
  [ "$uid" -gt 0 ] && [ "$home" = /nonexistent ] && [ "$(id -G "$SERVER_USER")" = "$gid" ] &&
    [ "$(getent group "$SERVER_USER" | cut -d: -f3)" = "$gid" ] && [ -z "$members" ] || {
    echo 'ERROR: existing service account is not isolated; no changes made to it.' >&2; return 1;
  }
}

# Refuse symlinks, shared data directories, writable executable parents and
# unsupported sandbox paths. Only the DB and known SQLite sidecars are chowned.
validate_server_paths() {
  python3 - "$INSTALL_DIR" "$BIN_DIR" "$SERVER_BIN" "$ENV_FILE" "$SERVICE_FILE" "$DB_PATH" <<'PY'
import os, pathlib, stat, sys
install, bindir, binary, env, unit, db = map(pathlib.Path, sys.argv[1:])
for p in (install, install/'recovery', install/'.server-unit.saved', bindir, binary, env, unit, db, db.parent):
    if not p.is_absolute() or any(c in str(p) for c in '\n\r%$\\"'):
        raise SystemExit('ERROR: absolute paths without systemd expansion characters required: '+str(p))
    for part in (p, *p.parents):
        if part.is_symlink(): raise SystemExit('ERROR: symlink path refused: '+str(part))
for p in (db.parent, binary, env):
    if str(p).startswith(('/home/', '/root/', '/tmp/', '/var/tmp/')):
        raise SystemExit('ERROR: path hidden by ProtectHome/PrivateTmp: '+str(p)+'; relocate explicitly before upgrade')
if db.parent in (install, bindir, pathlib.Path('/'), pathlib.Path('/var'), pathlib.Path('/var/lib')):
    raise SystemExit('ERROR: use a dedicated database directory')
if db.parent.exists():
    allowed = {db.name, db.name+'-wal', db.name+'-shm', db.name+'-journal'}
    for p in db.parent.iterdir():
        if p.name not in allowed or not p.is_file() or p.is_symlink():
            raise SystemExit('ERROR: data directory contains unrelated or unsafe files; use a dedicated directory: '+str(p))
for p in (install.parent, bindir.parent, *binary.parents, *db.parent.parents):
    if p.exists() and (p.stat().st_uid != 0 or stat.S_IMODE(p.stat().st_mode) & 0o022):
        raise SystemExit('ERROR: executable parent must be root managed and not group/world writable: '+str(p))
PY
}

prepare_existing_settings() {
  local source_unit="$SERVICE_FILE"
  if [ ! -s "$source_unit" ] && [ -s "$INSTALL_DIR/.server-unit.saved" ]; then source_unit="$INSTALL_DIR/.server-unit.saved"; fi
  [ -s "$ENV_FILE" ] && [ -s "$source_unit" ] || { echo 'ERROR: existing environment/unit missing; repair explicitly before upgrading.' >&2; return 1; }
  [ -z "$(systemctl show -p DropInPaths --value "$SERVICE_NAME" 2>/dev/null)" ] || { echo 'ERROR: unit drop-ins require a manual reviewed migration; existing service unchanged.' >&2; return 1; }
  # Preserve the effective legacy default too when a unit omitted -l.
  local previous_default=":8008"
  if [ -x "$SERVER_BIN" ] && "$SERVER_BIN" -installer-protocol 2>/dev/null | grep -q '^needle-install-v2 '; then previous_default="127.0.0.1:8008"; fi
  # Parse without executing shell code or sourcing a root environment file.
  python3 - "$source_unit" "$ENV_FILE" "$SERVER_BIN" "$TMP_DIR/settings" "$previous_default" <<'PY'
import pathlib, shlex, sys
unit, env, binary, output, previous_default = sys.argv[1:]
lines=pathlib.Path(unit).read_text().splitlines()
commands=[s.split('=',1)[1] for s in lines if s.startswith('ExecStart=')]
envs=[s.split('=',1)[1] for s in lines if s.startswith('EnvironmentFile=')]
if len(commands)!=1 or envs != [env]: raise SystemExit('ERROR: nonstandard unit/environment; migrate manually')
known={'Description','After','Wants','Type','ExecStart','EnvironmentFile','Restart','RestartSec','NoNewPrivileges','ProtectSystem','ProtectHome','PrivateTmp','PrivateDevices','ReadWritePaths','RestrictNamespaces','LockPersonality','RestrictRealtime','RestrictSUIDSGID','RemoveIPC','CapabilityBoundingSet','AmbientCapabilities','WantedBy','User','Group','SupplementaryGroups','UMask'}
for line in lines:
    line=line.strip()
    if line and not line.startswith(('#',';','[')) and '=' in line and line.split('=',1)[0] not in known:
        raise SystemExit('ERROR: custom unit directive needs a reviewed manual migration: '+line.split('=',1)[0])
args=shlex.split(commands[0])
if not args or args[0]!=binary or any(c in commands[0] for c in '\r\n%\\'): raise SystemExit('ERROR: unsupported ExecStart')
db=None
for i,a in enumerate(args[1:],1):
    if a=='-db' and i+1<len(args): db=args[i+1]
    elif a.startswith('-db='): db=a[4:]
if not db or not pathlib.Path(db).is_absolute(): raise SystemExit('ERROR: explicit absolute -db required')
# Reject unknown arguments rather than quietly discarding TLS or listen options.
for a in args:
    if '\n' in a or '\r' in a: raise SystemExit('ERROR: invalid argument')
env_listen=''
for line in pathlib.Path(env).read_text().splitlines():
    key,sep,value=line.partition('=')
    if sep and key.strip()=='NEEDLE_LISTEN':
        parts=shlex.split(value,comments=True)
        if len(parts)>1:raise SystemExit('ERROR: unsupported listen environment value')
        env_listen=parts[0] if parts else ''
listen=None
for i,arg in enumerate(args):
    if arg=='-l' and i+1<len(args):listen=args[i+1]
    elif arg.startswith('-l='):listen=arg[3:]
command=commands[0]
if listen is None:
    listen=env_listen or previous_default
    if not env_listen:command+=' -l '+listen
elif listen in ('${NEEDLE_LISTEN}','$NEEDLE_LISTEN'):listen=env_listen
if not listen or '$' in listen:raise SystemExit('ERROR: cannot resolve existing listener safely; review environment/ExecStart')
pathlib.Path(output).write_text(db+'\n'+command+'\n')
pathlib.Path(output+'-listen').write_text(listen)
tls=[]
for i,a in enumerate(args):
    if a in ('-cert','-key') and i+1<len(args): tls.append(args[i+1])
    elif a.startswith(('-cert=','-key=')): tls.append(a.split('=',1)[1])
pathlib.Path(output+'-tls').write_text(''.join(p+'\n' for p in tls))
PY
  DB_PATH=$(sed -n '1p' "$TMP_DIR/settings")
  SERVER_EXEC=$(sed -n '2p' "$TMP_DIR/settings")
  DATA_DIR=$(dirname "$DB_PATH")
}

validate_listener_permissions() {
  local effective_listen
  if [ "$FRESH_INSTALL" = true ]; then effective_listen=$LISTEN
  else effective_listen=$(cat "$TMP_DIR/settings-listen"); fi
  python3 - "$effective_listen" <<'PYPORT'
import pathlib,sys
listen=sys.argv[1]
try:
    port=int(listen.rsplit(':',1)[1])
    if port<0 or port>65535:raise ValueError()
except (ValueError,IndexError):raise SystemExit('ERROR: explicit numeric TCP listen port required for safe migration')
setting=pathlib.Path('/proc/sys/net/ipv4/ip_unprivileged_port_start')
floor=int(setting.read_text()) if setting.exists() else 1024
if port and port<floor:raise SystemExit('ERROR: listener needs privileged binding; use a high port behind your existing reverse proxy before migrating. No capabilities or firewall settings are changed.')
PYPORT
}

write_systemd_unit() {
  cat > "$1" <<UNIT
[Unit]
Description=Needle Server
After=network.target

[Service]
Type=simple
User=${SERVER_USER}
Group=${SERVER_USER}
SupplementaryGroups=
ExecStart=${SERVER_EXEC}
EnvironmentFile=${ENV_FILE}
Restart=always
RestartSec=5
UMask=0077
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
PrivateDevices=true
ReadWritePaths="${DATA_DIR}"
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

backup_database() {
  [ -f "$DB_PATH" ] || return 0
  # SQLite backup API copies a consistent view including committed WAL pages.
  # Source is opened without running application migrations.
  python3 - "$DB_PATH" "$RECOVERY/database.sqlite" <<'PY'
import pathlib, sqlite3, sys
src=sqlite3.connect(pathlib.Path(sys.argv[1]).as_uri()+'?mode=ro', uri=True, timeout=30)
dst=sqlite3.connect(sys.argv[2])
with dst: src.backup(dst)
if dst.execute('PRAGMA quick_check').fetchone()[0]!='ok': raise SystemExit('ERROR: backup quick_check failed')
dst.close(); src.close()
PY
  chmod 600 "$RECOVERY/database.sqlite"
}

validate_server_sandbox() {
  local property expected actual
  for property in User Group NoNewPrivileges ProtectSystem ProtectHome PrivateTmp PrivateDevices RestrictSUIDSGID CapabilityBoundingSet AmbientCapabilities UMask; do
    case "$property" in
      User|Group) expected=$SERVER_USER;; ProtectSystem) expected=strict;; UMask) expected=0077;;
      CapabilityBoundingSet|AmbientCapabilities) expected=;; *) expected=yes;;
    esac
    actual=$(systemctl show -p "$property" --value "$SERVICE_NAME")
    [ "$actual" = "$expected" ] || { echo "ERROR: systemd $property differs from required sandbox." >&2; return 1; }
  done
  local writable
  writable=$(systemctl show -p ReadWritePaths --value "$SERVICE_NAME")
  python3 - "$writable" "$DATA_DIR" <<'PYWRITE'
import shlex,sys
if shlex.split(sys.argv[1])!=[sys.argv[2]]:raise SystemExit('ERROR: effective systemd writable paths differ from the dedicated database directory; review unit/drop-ins')
PYWRITE
}

verify_server_active() {
  local pid attempt
  validate_server_sandbox
  for attempt in 1 2 3 4 5; do
    sleep 1
    pid=$(systemctl show -p MainPID --value "$SERVICE_NAME")
    if systemctl is-active --quiet "$SERVICE_NAME" && [ "$pid" -gt 0 ] &&
       [ "$(ps -o uid= -p "$pid" | tr -d ' ')" = "$(id -u "$SERVER_USER")" ]; then return 0; fi
  done
  echo "ERROR: Server failed to start as $SERVER_USER; inspect journalctl -u $SERVICE_NAME. Check DB mount and TLS key access." >&2
  return 1
}

deploy_server() (
  set -e
  validate_server_paths
  validate_listener_permissions
  lock_database_administration
  ACCOUNT_CREATED=false
  ensure_server_account
  if [ -f "$TMP_DIR/settings-tls" ]; then
    while IFS= read -r path; do
      case "$path" in /home/*|/root/*|/tmp/*|/var/tmp/*) echo "ERROR: TLS path hidden by systemd sandbox: $path; relocate or explicitly grant a narrow accessible path." >&2; exit 1;; esac
      runuser -u "$SERVER_USER" -- test -r "$path" || { echo "ERROR: TLS file unreadable as $SERVER_USER: $path; grant read-only access explicitly." >&2; exit 1; }
    done < "$TMP_DIR/settings-tls"
  fi
  # Root controlled recovery area is deliberately outside the writable data dir.
  install -d -o root -g root -m 755 "$INSTALL_DIR"
  install -d -o root -g root -m 700 "$INSTALL_DIR/recovery"
  RECOVERY=$(mktemp -d "$INSTALL_DIR/recovery/migration.XXXXXX")
  local was_active=false was_enabled=false changed=false migration_started=false path
  systemctl is-active --quiet "$SERVICE_NAME" && was_active=true
  systemctl is-enabled --quiet "$SERVICE_NAME" && was_enabled=true
  [ ! -f "$SERVER_BIN" ] || cp -p "$SERVER_BIN" "$RECOVERY/binary"
  [ ! -f "$SERVICE_FILE" ] || cp -p "$SERVICE_FILE" "$RECOVERY/unit"
  [ ! -f "$ENV_FILE" ] || cp -p "$ENV_FILE" "$RECOVERY/environment"
  [ ! -f "$INSTALL_DIR/.server-unit.saved" ] || cp -p "$INSTALL_DIR/.server-unit.saved" "$RECOVERY/saved-unit"
  # Capture exact ownership/mode, never recursively alter a custom directory.
  python3 - "$DATA_DIR" "$DB_PATH" "$RECOVERY/permissions.json" <<'PY'
import json, os, pathlib, sys
folder, db, output=sys.argv[1:]
paths=[folder,db,db+'-wal',db+'-shm',db+'-journal']
state={p:[os.stat(p).st_uid,os.stat(p).st_gid,os.stat(p).st_mode&0o777] for p in paths if os.path.exists(p)}
pathlib.Path(output).write_text(json.dumps(state))
PY
  recover() {
    local result=$?
    trap - EXIT
    if [ "$result" -ne 0 ]; then
      echo "ERROR: deployment failed. Recovery files: $RECOVERY" >&2
      if [ "$changed" = true ]; then systemctl stop "$SERVICE_NAME" || true; fi
      if [ "$migration_started" = false ]; then
        if [ -f "$RECOVERY/binary" ]; then install -o root -g root -m 755 "$RECOVERY/binary" "$SERVER_BIN"; elif [ "$changed" = true ]; then rm -f "$SERVER_BIN"; fi
        if [ -f "$RECOVERY/unit" ]; then cp -p "$RECOVERY/unit" "$SERVICE_FILE"; elif [ "$changed" = true ]; then rm -f "$SERVICE_FILE"; fi
        if [ -f "$RECOVERY/saved-unit" ]; then cp -p "$RECOVERY/saved-unit" "$INSTALL_DIR/.server-unit.saved"; else rm -f "$INSTALL_DIR/.server-unit.saved"; fi
        python3 - "$RECOVERY/permissions.json" <<'PY'
import json, os, sys
for p,(uid,gid,mode) in json.load(open(sys.argv[1])).items():
    if os.path.exists(p): os.chown(p,uid,gid); os.chmod(p,mode)
PY
        systemctl daemon-reload || true
        if [ "$was_active" = true ]; then systemctl start "$SERVICE_NAME" || true; fi
      else
        # New schema may have committed and new reports may have arrived.
        # Never restore an old DB or start an old binary against an unreviewed schema.
        echo 'Database migration may have started. Service remains stopped; database is NOT restored. Repair the new deployment or review downgrade compatibility explicitly.' >&2
      fi
    fi
    exit "$result"
  }
  trap recover EXIT
  if [ "$was_active" = true ]; then systemctl stop "$SERVICE_NAME"; fi
  changed=true
  backup_database
  install -d -o root -g root -m 755 "$BIN_DIR"
  install -d -o "$SERVER_USER" -g "$SERVER_USER" -m 700 "$DATA_DIR"
  for path in "$DB_PATH" "$DB_PATH-wal" "$DB_PATH-shm" "$DB_PATH-journal"; do
    if [ -e "$path" ]; then chown "$SERVER_USER:$SERVER_USER" "$path"; chmod 600 "$path"; fi
  done
  if [ "$FRESH_INSTALL" = true ]; then
    printf 'NEEDLE_LISTEN=%s\n' "$LISTEN" > "$ENV_FILE"
  fi
  chown root:root "$ENV_FILE"; chmod 600 "$ENV_FILE"
  write_systemd_unit "$TMP_DIR/server.service"
  install -o root -g root -m 755 "$TMP_DIR/needle-server" "$BIN_DIR/.needle-server.new"
  mv -f "$BIN_DIR/.needle-server.new" "$SERVER_BIN"
  systemd-analyze verify "$TMP_DIR/server.service"
  install -o root -g root -m 644 "$TMP_DIR/server.service" "$SERVICE_FILE"
  install -o root -g root -m 600 "$TMP_DIR/server.service" "$INSTALL_DIR/.server-unit.saved"
  systemctl daemon-reload
  validate_server_sandbox
  # Opening via CLI performs migrations under the same identity as the daemon.
  migration_started=true
  runuser -u "$SERVER_USER" -- "$SERVER_BIN" -db "$DB_PATH" list-agents >/dev/null
  if [ "$ACCOUNT_CREATED" = true ]; then printf '%s\n' "$(id -u "$SERVER_USER")" > "$INSTALL_DIR/.server-user-created"; fi
  if [ "$FRESH_INSTALL" = true ]; then systemctl enable "$SERVICE_NAME"; fi
  if [ "$was_active" = true ] || [ "$FRESH_INSTALL" = true ]; then
    systemctl start "$SERVICE_NAME"
    verify_server_active
  fi
  echo "Installed $VERSION. Existing active/enabled state preserved (new installs start automatically)."
  echo "Consistent pre-migration backup and originals: $RECOVERY"
  echo "CLI (root automatically runs as the DB owner): sudo $SERVER_BIN -db '$DB_PATH' list-agents"
)

cmd_install() {
  require_root; require_runtime
  if is_installed; then echo 'Existing installation found; using upgrade without overwriting configuration.'; FRESH_INSTALL=false; cmd_upgrade; return; fi
  require_tty_for_install
  download_release_server interactive
  DEFAULT_LISTEN="127.0.0.1:8008"
  read_prompt LISTEN "Listen address [$DEFAULT_LISTEN]: "
  LISTEN=${LISTEN:-$DEFAULT_LISTEN}
  case "$LISTEN" in *$'\n'*|*$'\r'*|*[[:space:]]*|*\$*|*%*) echo 'ERROR: invalid listen address.' >&2; return 1;; esac
  case "$LISTEN" in :*|0.0.0.0:*|\[::\]:*) echo 'WARNING: this may expose the dashboard and API on public interfaces.' >&2;; esac
  SERVER_EXEC="$SERVER_BIN -l \${NEEDLE_LISTEN} -db $DB_PATH"
  FRESH_INSTALL=true
  deploy_server
}

cmd_upgrade() {
  require_root
  # cmd_install may already hold the installer lock.
  if [ "${FRESH_INSTALL:-}" != false ]; then require_runtime; fi
  is_installed || { echo 'ERROR: Server not installed.' >&2; return 1; }
  download_release_server
  prepare_existing_settings
  FRESH_INSTALL=false
  deploy_server
}

cmd_uninstall() {
  require_root
  require_runtime
  if [ -f "$ENV_FILE" ] && { [ -f "$SERVICE_FILE" ] || [ -f "$INSTALL_DIR/.server-unit.saved" ]; }; then
    TMP_DIR=$(mktemp -d); trap 'rm -rf "$TMP_DIR"' EXIT
    prepare_existing_settings
  fi
  validate_server_paths
  lock_database_administration

  local purge=false yes=false
  while [ $# -gt 0 ]; do
    case "$1" in
      --purge) purge=true; shift ;;
      -y|--yes) yes=true; shift ;;
      -h|--help) usage; exit 0 ;;
      *) echo "Unknown option: $1"; usage; exit 1 ;;
    esac
  done

  if [ "$purge" = true ] && [ "$DATA_DIR" != "$INSTALL_DIR/data" ]; then
    echo "ERROR: custom data directory needs explicit manual purge: $DATA_DIR" >&2
    return 1
  fi

  if [ "$yes" != true ]; then
    echo "This will stop needle-server and remove the service unit + binary."
    if [ "$purge" = true ]; then
      echo "Also DELETE entire $INSTALL_DIR (including data and .env)."
    else
      echo "Data kept: $DATA_DIR and $ENV_FILE (use --purge to remove all)."
    fi
    read_prompt ans "Continue? [y/N] "
    case "$ans" in
      y|Y|yes|YES) ;;
      *) echo "aborted"; exit 0 ;;
    esac
  fi

  echo "Stopping service..."
  if [ -f "$SERVICE_FILE" ] || systemctl cat "$SERVICE_NAME" >/dev/null 2>&1 || systemctl is-active --quiet "$SERVICE_NAME"; then
    systemctl stop "$SERVICE_NAME" || { echo 'ERROR: service stop failed; installation retained.' >&2; return 1; }
    systemctl disable "$SERVICE_NAME" || { echo 'ERROR: service disable failed; installation retained.' >&2; return 1; }
  fi
  if [ "$purge" = false ] && [ -f "$SERVICE_FILE" ]; then install -o root -g root -m 600 "$SERVICE_FILE" "$INSTALL_DIR/.server-unit.saved"; fi
  rm -f "$SERVICE_FILE"
  systemctl daemon-reload 2>/dev/null || true

  if [ "$purge" = true ]; then
    echo "Removing $INSTALL_DIR ..."
    local owned_uid=""
    [ ! -f "$INSTALL_DIR/.server-user-created" ] || owned_uid=$(cat "$INSTALL_DIR/.server-user-created")
    rm -rf "$INSTALL_DIR"
    if [ -n "$owned_uid" ] && [ "$(id -u "$SERVER_USER" 2>/dev/null)" = "$owned_uid" ]; then
      # Retain the account if files or running processes suggest any other use.
      if ! pgrep -u "$owned_uid" >/dev/null && [ -z "$(find / -uid "$owned_uid" -print -quit 2>/dev/null)" ]; then
        userdel "$SERVER_USER"
        groupdel "$SERVER_USER" || true
      else echo "Service account retained: it still owns files or processes; review before removing it."; fi
    fi
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
    if grep -q '^NEEDLE_LISTEN=' "$ENV_FILE" 2>/dev/null; then
      echo "  listen:    $(sed -n 's/^NEEDLE_LISTEN=//p' "$ENV_FILE" | head -1)"
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
  echo "Token / agent ops:"
  echo "  $SERVER_BIN -db $DB_PATH allow-token <token>"
  echo "  $SERVER_BIN -db $DB_PATH list-tokens"
  echo "  $SERVER_BIN -db $DB_PATH list-agents"
  echo "  $SERVER_BIN -db $DB_PATH delete-agent <hostname|id>"
}

# --- main ---
if [[ "${BASH_SOURCE[0]}" != "$0" ]]; then return 0; fi
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
  install)   cmd_install; exit 0 ;;
  upgrade)   cmd_upgrade; exit 0 ;;
  uninstall) cmd_uninstall "$@"; exit 0 ;;
  status)    cmd_status; exit 0 ;;
  help|-h|--help) usage; exit 0 ;;
esac
