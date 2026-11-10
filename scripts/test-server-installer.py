#!/usr/bin/env python3
"""Portable, isolated preflight and WAL-backup regressions (no service changes)."""
import pathlib
import shlex
import sqlite3
import subprocess
import tempfile
import unittest

SCRIPT = pathlib.Path(__file__).with_name('needle-server.sh').resolve()
class ServerInstallerTests(unittest.TestCase):
    def shell(self, code, env=None):
        return subprocess.run(['bash','-c', f'source "{SCRIPT}"\n'+code], env=env, capture_output=True, text=True)
    def test_unit_identity_and_minimal_write_path(self):
        with tempfile.TemporaryDirectory() as temp:
            out=pathlib.Path(temp)/'unit'
            r=self.shell(f'write_systemd_unit "{out}"')
            self.assertEqual(r.returncode,0,r.stderr)
            s=out.read_text()
            for line in ['User=needle-server','Group=needle-server','UMask=0077','PrivateDevices=true','CapabilityBoundingSet=','ProtectSystem=strict','ReadWritePaths="/opt/needle/data"']:
                self.assertIn(line,s)

    def account_database(self, **database):
        uid = database.get('uid', 123)
        gid = database.get('gid', 123)
        entry = f'needle-server:x:{uid}:{gid}::/nonexistent:/usr/sbin/nologin'
        group = f'needle-server:x:{gid}:{database.get("members", "")}'
        users = '' if database.get('empty_passwd') else '\n'.join(filter(None, ['root:x:0:0::/root:/bin/sh', '' if database.get('omit_user') else entry, database.get('extra_user', '')]))
        groups = '' if database.get('empty_group') else '\n'.join(filter(None, ['root:x:0:', '' if database.get('omit_group') else group, database.get('extra_group', '')]))
        q = shlex.quote
        return f'''
ACCOUNT_USER_PRESENT={str(database.get('existing', True)).lower()}
ACCOUNT_GROUP_PRESENT={str(database.get('existing', True)).lower()}
getent() {{
  case "$1:$#" in
    passwd:1) printf '%s\\n' {q(users)}; return {database.get('passwd_status', 0)} ;;
    group:1) printf '%s\\n' {q(groups)}; return {database.get('group_status', 0)} ;;
    passwd:2) [ "$2" = needle-server ] && [ "$ACCOUNT_USER_PRESENT" = true ] || return 2; printf '%s\\n' {q(entry)} ;;
    group:2) [ "$2" = needle-server ] && [ "$ACCOUNT_GROUP_PRESENT" = true ] || return 2; printf '%s\\n' {q(group)} ;;
    *) return 2 ;;
  esac
}}
id() {{ [ "$1" = -G ] && [ "$2" = needle-server ] || return 1; printf '%s\\n' {q(str(gid))}; }}
useradd() {{ [ "$ACCOUNT_USER_PRESENT" = false ] || return 1; ACCOUNT_USER_PRESENT=true; echo useradd; }}
groupadd() {{ [ "$ACCOUNT_GROUP_PRESENT" = false ] || return 1; ACCOUNT_GROUP_PRESENT=true; echo groupadd; }}
userdel() {{ echo unexpected-userdel >&2; return 1; }}
groupdel() {{ echo unexpected-groupdel >&2; return 1; }}
nologin() {{ :; }}
'''

    def test_fresh_service_account_passes_isolation_validation(self):
        result = self.shell(self.account_database(existing=False) + '\nensure_server_account\n[ "$ACCOUNT_CREATED" = true ]\n')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(result.stdout.splitlines(), ['groupadd', 'useradd'])

    def test_service_uid_and_gid_must_be_exclusive(self):
        cases = [
            ('isolated account', {}, True),
            ('explicit self membership', {'members': 'needle-server'}, True),
            ('unrelated identities', {'extra_user': 'other:x:456:456::/home/other:/bin/sh', 'extra_group': 'other:x:456:other'}, True),
            ('root UID', {'uid': 0}, False),
            ('root primary GID', {'gid': 0}, False),
            ('shared UID', {'extra_user': 'other:x:123:456::/home/other:/bin/sh'}, False),
            ('shared primary GID', {'extra_user': 'other:x:456:123::/home/other:/bin/sh'}, False),
            ('explicit other member', {'members': 'other'}, False),
            ('shared numeric group', {'extra_group': 'other:x:123:other'}, False),
            ('duplicate user entry', {'extra_user': 'needle-server:x:123:123::/nonexistent:/usr/sbin/nologin'}, False),
            ('duplicate group entry', {'extra_group': 'needle-server:x:123:'}, False),
            ('failed passwd enumeration', {'passwd_status': 2}, False),
            ('failed group enumeration', {'group_status': 2}, False),
            ('empty passwd enumeration', {'empty_passwd': True}, False),
            ('empty group enumeration', {'empty_group': True}, False),
            ('user missing from enumeration', {'omit_user': True}, False),
            ('group missing from enumeration', {'omit_group': True}, False),
        ]
        for name, database, allowed in cases:
            with self.subTest(name=name):
                result = self.shell(self.account_database(**database) + '\nensure_server_account\n')
                self.assertEqual(result.returncode == 0, allowed, result.stdout + result.stderr)

    def test_shared_identity_rejects_upgrade_before_changing_existing_server(self):
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            originals = {'needle.db': b'unchanged database fixture', '.env': b'NEEDLE_LISTEN=127.0.0.1:8008\n',
                         'needle-server': b'unchanged binary', 'unit': b'unchanged service'}
            for name, data in originals.items():
                (root / name).write_bytes(data)
            q = shlex.quote
            code = self.account_database(extra_user='other:x:123:456::/home/other:/bin/sh') + f'''
INSTALL_DIR={q(str(root))}; DB_PATH="$INSTALL_DIR/needle.db"; ENV_FILE="$INSTALL_DIR/.env"
SERVER_BIN="$INSTALL_DIR/needle-server"; SERVICE_FILE="$INSTALL_DIR/unit"
validate_server_paths() {{ :; }}
validate_listener_permissions() {{ :; }}
lock_database_administration() {{ :; }}
systemctl() {{ echo unexpected-systemctl >> "$INSTALL_DIR/changes"; return 1; }}
install() {{ echo unexpected-install >> "$INSTALL_DIR/changes"; return 1; }}
chown() {{ echo unexpected-chown >> "$INSTALL_DIR/changes"; return 1; }}
deploy_server
'''
            result = self.shell(code)
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse((root / 'changes').exists())
            for name, data in originals.items():
                self.assertEqual((root / name).read_bytes(), data)
    def test_consistent_backup_includes_uncheckpointed_wal(self):
        with tempfile.TemporaryDirectory() as temp:
            db=pathlib.Path(temp)/'live.db'; backup=pathlib.Path(temp)/'backup';backup.mkdir()
            conn=sqlite3.connect(db)
            conn.execute('PRAGMA journal_mode=WAL');conn.execute('PRAGMA wal_autocheckpoint=0')
            conn.execute('CREATE TABLE secrets(value TEXT)');conn.execute("INSERT INTO secrets VALUES('test-only')");conn.commit()
            self.assertGreater(pathlib.Path(str(db)+'-wal').stat().st_size,0)
            r=self.shell(f'DB_PATH="{db}"; RECOVERY="{backup}"; backup_database')
            self.assertEqual(r.returncode,0,r.stderr)
            copy=sqlite3.connect(backup/'database.sqlite')
            self.assertEqual(copy.execute('SELECT * FROM secrets').fetchall(),[('test-only',)])
            self.assertEqual((backup/'database.sqlite').stat().st_mode & 0o777,0o600)
            copy.close();conn.close()
    def test_symlink_refused(self):
        with tempfile.TemporaryDirectory() as temp:
            link=pathlib.Path(temp)/'link';link.symlink_to('/opt')
            r=self.shell(f'INSTALL_DIR="{link}"; validate_server_paths')
            self.assertNotEqual(r.returncode,0);self.assertIn('symlink',r.stderr)
    def test_legacy_listener_and_custom_db_preserved(self):
        with tempfile.TemporaryDirectory() as temp:
            unit=pathlib.Path(temp)/'old.service';env=pathlib.Path(temp)/'.env'
            unit.write_text(f'[Service]\nExecStart=/opt/needle/bin/needle-server -l ${{NEEDLE_LISTEN}} -db /srv/needle/monitor.db -cert /etc/needle/cert.pem -key /etc/needle/key.pem\nEnvironmentFile={env}\n')
            env.write_text('NEEDLE_LISTEN=0.0.0.0:8008\n')
            before=env.read_bytes()
            r=self.shell(f'TMP_DIR="{temp}"; SERVICE_FILE="{unit}"; ENV_FILE="{env}"; systemctl() {{ return 0; }}; prepare_existing_settings; printf "%s\\n%s" "$DB_PATH" "$SERVER_EXEC"')
            self.assertEqual(r.returncode,0,r.stderr)
            self.assertIn('/srv/needle/monitor.db',r.stdout); self.assertIn('-cert /etc/needle/cert.pem',r.stdout)
            self.assertEqual(before,env.read_bytes())
    def test_legacy_unit_without_listen_keeps_public_default(self):
        with tempfile.TemporaryDirectory() as temp:
            unit=pathlib.Path(temp)/'old.service';env=pathlib.Path(temp)/'.env'
            unit.write_text(f'[Service]\nExecStart=/opt/needle/bin/needle-server -db /srv/needle/monitor.db\nEnvironmentFile={env}\n')
            env.write_text('# no explicit listener in legacy installation\n')
            r=self.shell(f'TMP_DIR="{temp}"; SERVICE_FILE="{unit}"; ENV_FILE="{env}"; systemctl() {{ return 0; }}; prepare_existing_settings; cat "$TMP_DIR/settings"')
            self.assertEqual(r.returncode,0,r.stderr)
            self.assertIn('-l :8008',r.stdout)
    def test_unsafe_recovery_symlink_is_rejected(self):
        with tempfile.TemporaryDirectory() as temp:
            (pathlib.Path(temp)/'recovery').symlink_to('/opt')
            r=self.shell(f'INSTALL_DIR="{temp}"; validate_server_paths')
            self.assertNotEqual(r.returncode,0);self.assertIn('symlink',r.stderr)

    def test_fresh_loopback_and_fail_closed_checksum(self):
        s=SCRIPT.read_text()
        self.assertIn('DEFAULT_LISTEN="127.0.0.1:8008"',s)
        self.assertNotIn('Skipping verification',s)
        self.assertIn('Database migration may have started',s)
        self.assertNotIn('cp "$DB_PATH"',s)

    def test_purge_reads_retained_custom_database_before_service_changes(self):
        with tempfile.TemporaryDirectory() as temp:
            root=pathlib.Path(temp);env=root/'.env';saved=root/'.server-unit.saved'
            env.write_text('NEEDLE_LISTEN=127.0.0.1:8008\n')
            saved.write_text(f'[Service]\nExecStart=/opt/needle/bin/needle-server -db /srv/needle/custom.db\nEnvironmentFile={env}\n')
            r=self.shell(f'''
INSTALL_DIR="{root}"; ENV_FILE="{env}"; SERVICE_FILE="{root}/absent.service"
require_root() {{ :; }}; require_runtime() {{ :; }}
validate_server_paths() {{ :; }}; lock_database_administration() {{ :; }}
systemctl() {{ [ "$1" = show ] || {{ echo unexpected-service-change >&2; return 1; }}; }}
cmd_uninstall --purge -y
''')
            self.assertNotEqual(r.returncode,0)
            self.assertIn('custom data directory',r.stderr)
            self.assertNotIn('unexpected-service-change',r.stderr)
            self.assertTrue(env.exists());self.assertTrue(saved.exists())

    def test_stop_failure_retains_unit_and_binary(self):
        with tempfile.TemporaryDirectory() as temp:
            root=pathlib.Path(temp);unit=root/'unit';binary=root/'binary'
            unit.write_text('test unit');binary.write_text('test binary')
            r=self.shell(f'''
INSTALL_DIR="{root}"; SERVICE_FILE="{unit}"; SERVER_BIN="{binary}"; ENV_FILE="{root}/absent.env"
require_root() {{ :; }}; require_runtime() {{ :; }}
validate_server_paths() {{ :; }}; lock_database_administration() {{ :; }}
systemctl() {{ return 1; }}
cmd_uninstall -y
''')
            self.assertNotEqual(r.returncode,0);self.assertIn('service stop failed',r.stderr)
            self.assertTrue(unit.exists());self.assertTrue(binary.exists())
if __name__=='__main__': unittest.main()
