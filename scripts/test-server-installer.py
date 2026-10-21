#!/usr/bin/env python3
"""Portable, isolated preflight and WAL-backup regressions (no service changes)."""
import pathlib
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
