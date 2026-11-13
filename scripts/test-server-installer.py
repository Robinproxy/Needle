#!/usr/bin/env python3
"""Portable, isolated preflight and WAL-backup regressions (no service changes)."""
from contextlib import closing, nullcontext
import os
import pathlib
import shlex
import sqlite3
import stat
import subprocess
import sys
import tempfile
import types
import unittest
from unittest import mock

SCRIPT = pathlib.Path(__file__).with_name('needle-server.sh').resolve()
class ServerInstallerTests(unittest.TestCase):
    def setUp(self):
        self.owners = {}

    def shell(self, code, env=None):
        return subprocess.run(['bash','-c', f'source "{SCRIPT}"\n'+code], env=env, capture_output=True, text=True)

    def embedded_python(self, function, marker, args, **patches):
        source = SCRIPT.read_text().split(function+'() {', 1)[1]
        source = source.split("<<'"+marker+"'\n", 1)[1].split('\n'+marker+'\n', 1)[0]
        with mock.patch.object(sys, 'argv', ['installer-test', *map(str, args)]):
            with mock.patch.multiple(os, **patches) if patches else nullcontext():
                exec(compile(source, str(SCRIPT)+':'+function, 'exec'), {})

    def database_permissions(self, action, root):
        # Exercise real no-follow opens, inode checks and chmod as an ordinary
        # CI/macOS user. Only chown and its reported UID/GID are simulated;
        # test-server-systemd.py also exercises the lifecycle with real root.
        real_fstat = os.fstat
        def fstat(fd):
            info = real_fstat(fd)
            fields = list(info)
            fields[4:6] = self.owners.get((info.st_dev, info.st_ino), (info.st_uid, info.st_gid))
            return os.stat_result(fields)
        def fchown(fd, uid, gid):
            info = real_fstat(fd)
            self.owners[(info.st_dev, info.st_ino)] = (uid, gid)
        account = types.SimpleNamespace(pw_uid=123, pw_gid=456)
        with mock.patch('pwd.getpwnam', return_value=account):
            self.embedded_python('database_permissions', 'PYPERMS',
                                 [action, root/'data', root/'data/needle.db', root/'permissions.json', 'needle-server'],
                                 fstat=fstat, fchown=fchown)

    def database_fixture(self, root):
        self.owners.clear()
        (root/'data').mkdir(mode=0o750)
        with closing(sqlite3.connect(root/'data/needle.db')) as conn:
            with conn:
                conn.execute('CREATE TABLE fixture(value TEXT)')
                conn.execute("INSERT INTO fixture VALUES('original')")
        os.chmod(root/'data/needle.db', 0o640)
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
            root=pathlib.Path(temp);(root/'data').mkdir()
            db=root/'data/needle.db'; backup=root/'backup';backup.mkdir()
            conn=sqlite3.connect(db)
            conn.execute('PRAGMA journal_mode=WAL');conn.execute('PRAGMA wal_autocheckpoint=0')
            conn.execute('CREATE TABLE secrets(value TEXT)');conn.execute("INSERT INTO secrets VALUES('test-only')");conn.commit()
            self.assertGreater(pathlib.Path(str(db)+'-wal').stat().st_size,0)
            self.database_permissions('prepare', root)
            self.database_permissions('check', root)
            r=self.shell(f'DB_PATH="{db}"; RECOVERY="{backup}"; database_permissions() {{ :; }}; backup_database')
            self.assertEqual(r.returncode,0,r.stderr)
            copy=sqlite3.connect(backup/'database.sqlite')
            self.assertEqual(copy.execute('SELECT * FROM secrets').fetchall(),[('test-only',)])
            self.assertEqual((backup/'database.sqlite').stat().st_mode & 0o777,0o600)
            copy.close();conn.close()
            self.database_permissions('activate', root)
            self.assertEqual(db.stat().st_mode & 0o777, 0o600)

    def test_database_prepare_activate_and_restore_permissions(self):
        for action in ('activate', 'restore'):
            with self.subTest(action=action), tempfile.TemporaryDirectory() as temp:
                root = pathlib.Path(temp)
                self.database_fixture(root)
                original = (root/'data/needle.db').read_bytes()
                self.database_permissions('prepare', root)
                directory = (root/'data').stat()
                self.assertEqual(self.owners[(directory.st_dev, directory.st_ino)], (0, 0))
                self.assertEqual(stat.S_IMODE(directory.st_mode), 0o700)
                self.database_permissions('check', root)
                self.database_permissions(action, root)
                db = (root/'data/needle.db').stat()
                self.assertEqual(stat.S_IMODE(db.st_mode), 0o600 if action == 'activate' else 0o640)
                self.assertEqual(stat.S_IMODE((root/'data').stat().st_mode), 0o700 if action == 'activate' else 0o750)
                self.assertEqual((root/'data/needle.db').read_bytes(), original)
                if action == 'activate':
                    self.assertEqual(self.owners[(db.st_dev, db.st_ino)], (123, 456))

    def test_database_links_and_special_files_cannot_change_external_files(self):
        for name in ('needle.db', 'needle.db-wal', 'needle.db-shm', 'needle.db-journal'):
            for kind in ('symlink', 'hardlink', 'fifo'):
                with self.subTest(name=name, kind=kind), tempfile.TemporaryDirectory() as temp:
                    root = pathlib.Path(temp)
                    self.database_fixture(root)
                    target = root/'external.sqlite'
                    with closing(sqlite3.connect(target)) as conn:
                        with conn: conn.execute('CREATE TABLE private(value TEXT)')
                    target.chmod(0o644)
                    original = target.read_bytes(), target.stat()
                    link = root/'data'/name
                    link.unlink(missing_ok=True)
                    if kind == 'symlink': link.symlink_to(target)
                    elif kind == 'hardlink': os.link(target, link)
                    else: os.mkfifo(link)
                    with self.assertRaises((OSError, SystemExit)):
                        self.database_permissions('prepare', root)
                    with self.assertRaises((OSError, SystemExit)):
                        self.database_permissions('restore', root)
                    self.assertEqual(target.read_bytes(), original[0])
                    current = target.stat()
                    self.assertEqual((current.st_uid, current.st_gid, stat.S_IMODE(current.st_mode)),
                                     (original[1].st_uid, original[1].st_gid, 0o644))
                    self.assertNotIn((current.st_dev, current.st_ino), self.owners)

    def test_database_replacements_are_refused_during_activation_and_recovery(self):
        for action in ('check', 'activate', 'restore'):
            for entry in ('needle.db', 'needle.db-wal'):
                with self.subTest(action=action, entry=entry), tempfile.TemporaryDirectory() as temp:
                    root = pathlib.Path(temp)
                    self.database_fixture(root)
                    (root/'data/needle.db-wal').write_bytes(b'old WAL fixture')
                    self.database_permissions('prepare', root)
                    # Rename keeps the old inode allocated, preventing reuse.
                    replaced = root/'data'/entry
                    replaced.rename(root/'old-file')
                    replaced.write_bytes(b'replacement')
                    with self.assertRaisesRegex(SystemExit, 'replaced'):
                        self.database_permissions(action, root)
                    self.assertEqual(replaced.read_bytes(), b'replacement')

    def test_database_directory_replacement_is_refused(self):
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            self.database_fixture(root)
            self.database_permissions('prepare', root)
            (root/'data').rename(root/'old-data')
            (root/'data').mkdir()
            with self.assertRaisesRegex(SystemExit, 'directory changed'):
                self.database_permissions('restore', root)

    def test_fresh_database_directory_can_be_activated_without_a_database_yet(self):
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            self.database_permissions('prepare', root)
            self.database_permissions('check', root)
            self.database_permissions('activate', root)
            directory = (root/'data').stat()
            self.assertEqual(self.owners[(directory.st_dev, directory.st_ino)], (123, 456))
            self.assertEqual(stat.S_IMODE(directory.st_mode), 0o700)
            self.assertFalse((root/'data/needle.db').exists())

    def test_disappeared_database_is_not_silently_recreated(self):
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            self.database_fixture(root)
            self.database_permissions('prepare', root)
            (root/'data/needle.db').unlink()
            for action in ('check', 'activate', 'restore'):
                with self.subTest(action=action), self.assertRaisesRegex(SystemExit, 'disappeared'):
                    self.database_permissions(action, root)

    def test_restore_assigns_new_backup_sidecar_to_original_database_owner(self):
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            self.database_fixture(root)
            self.database_permissions('prepare', root)
            (root/'data/needle.db-shm').write_bytes(b'new SHM fixture')
            self.database_permissions('restore', root)
            shm = (root/'data/needle.db-shm').stat()
            db = (root/'data/needle.db').stat()
            self.assertEqual(self.owners[(shm.st_dev, shm.st_ino)], (db.st_uid, db.st_gid))
            self.assertEqual(stat.S_IMODE(shm.st_mode), 0o600)

    def test_existing_binary_requires_safe_ownership_mode_and_parents(self):
        for case in ('safe', 'writable binary', 'non-root binary', 'symlink binary', 'directory binary',
                     'writable parent', 'non-root parent', 'missing binary'):
            with self.subTest(case=case), tempfile.TemporaryDirectory() as temp:
                root = pathlib.Path(temp).resolve()
                binary = root/'needle-server'
                binary.write_text('#!/bin/sh\nexit 0\n')
                binary.chmod(0o777 if case == 'writable binary' else 0o755)
                if case == 'symlink binary':
                    binary.unlink(); binary.symlink_to('/bin/sh')
                elif case == 'directory binary':
                    binary.unlink(); binary.mkdir()
                elif case == 'missing binary': binary.unlink()
                real_lstat = pathlib.Path.lstat
                def lstat(path):
                    info = real_lstat(path)
                    fields = list(info)
                    fields[4] = 501 if ((case == 'non-root binary' and path == binary) or
                                             (case == 'non-root parent' and path == root)) else 0
                    if path != binary:
                        fields[0] &= ~0o022
                        if case == 'writable parent' and path == root: fields[0] |= 0o020
                    return os.stat_result(fields)
                with mock.patch.object(pathlib.Path, 'lstat', lstat):
                    if case in ('safe', 'missing binary'):
                        self.embedded_python('validate_existing_server_binary', 'PYBINARY', [binary])
                    else:
                        with self.assertRaises(SystemExit):
                            self.embedded_python('validate_existing_server_binary', 'PYBINARY', [binary])

    def test_untrusted_old_binary_is_never_executed(self):
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            binary = root/'needle-server'
            binary.write_text(f'#!/bin/sh\ntouch {shlex.quote(str(root/"executed"))}\necho "needle-install-v2 test"\n')
            binary.chmod(0o777)
            (root/'.env').write_text('NEEDLE_LISTEN=127.0.0.1:8008\n')
            (root/'unit').write_text(f'[Service]\nExecStart={binary} -db /srv/needle/monitor.db\nEnvironmentFile={root}/.env\n')
            result = self.shell(f'SERVER_BIN="{binary}"; ENV_FILE="{root}/.env"; SERVICE_FILE="{root}/unit"; TMP_DIR="{root}"; systemctl() {{ :; }}; prepare_existing_settings')
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse((root/'executed').exists())
            self.assertFalse((root/'settings').exists())
    def test_symlink_refused(self):
        with tempfile.TemporaryDirectory() as temp:
            link=pathlib.Path(temp)/'link';link.symlink_to('/opt')
            r=self.shell(f'INSTALL_DIR="{link}"; validate_existing_server_binary() {{ :; }}; validate_server_paths')
            self.assertNotEqual(r.returncode,0);self.assertIn('symlink',r.stderr)
    def test_legacy_listener_and_custom_db_preserved(self):
        with tempfile.TemporaryDirectory() as temp:
            unit=pathlib.Path(temp)/'old.service';env=pathlib.Path(temp)/'.env'
            unit.write_text(f'[Service]\nExecStart=/opt/needle/bin/needle-server -l ${{NEEDLE_LISTEN}} -db /srv/needle/monitor.db -cert /etc/needle/cert.pem -key /etc/needle/key.pem\nEnvironmentFile={env}\n')
            env.write_text('NEEDLE_LISTEN=0.0.0.0:8008\n')
            before=env.read_bytes()
            r=self.shell(f'TMP_DIR="{temp}"; SERVICE_FILE="{unit}"; ENV_FILE="{env}"; systemctl() {{ return 0; }}; validate_existing_server_binary() {{ :; }}; prepare_existing_settings; printf "%s\\n%s" "$DB_PATH" "$SERVER_EXEC"')
            self.assertEqual(r.returncode,0,r.stderr)
            self.assertIn('/srv/needle/monitor.db',r.stdout); self.assertIn('-cert /etc/needle/cert.pem',r.stdout)
            self.assertEqual(before,env.read_bytes())
    def test_legacy_unit_without_listen_keeps_public_default(self):
        with tempfile.TemporaryDirectory() as temp:
            unit=pathlib.Path(temp)/'old.service';env=pathlib.Path(temp)/'.env'
            unit.write_text(f'[Service]\nExecStart=/opt/needle/bin/needle-server -db /srv/needle/monitor.db\nEnvironmentFile={env}\n')
            env.write_text('# no explicit listener in legacy installation\n')
            r=self.shell(f'TMP_DIR="{temp}"; SERVICE_FILE="{unit}"; ENV_FILE="{env}"; systemctl() {{ return 0; }}; validate_existing_server_binary() {{ :; }}; prepare_existing_settings; cat "$TMP_DIR/settings"')
            self.assertEqual(r.returncode,0,r.stderr)
            self.assertIn('-l :8008',r.stdout)
    def test_unsafe_recovery_symlink_is_rejected(self):
        with tempfile.TemporaryDirectory() as temp:
            (pathlib.Path(temp)/'recovery').symlink_to('/opt')
            r=self.shell(f'INSTALL_DIR="{temp}"; validate_existing_server_binary() {{ :; }}; validate_server_paths')
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
validate_existing_server_binary() {{ :; }}
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
