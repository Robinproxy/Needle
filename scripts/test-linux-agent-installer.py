#!/usr/bin/env python3
"""Exercise Linux migration/rollback without modifying host users or systemd.

The separate systemd integration test exercises real ownership and service UIDs.
"""
import os
from pathlib import Path
import shlex
import subprocess
import tempfile
import unittest

SCRIPT = Path(__file__).with_name('needle-agent.sh')


class LinuxInstallerTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.library = self.root / 'installer.sh'
        self.library.write_text(SCRIPT.read_text().split('# --- main ---')[0])
        self.directory = self.root / 'installation'
        self.bindir = self.directory / 'bin'
        self.bindir.mkdir(parents=True)
        self.directory.chmod(0o755)
        self.config = self.directory / 'agent.yaml'
        self.original = b"token: 'same-secret:#value'\ninterval: 30\ntcpping: []\n"
        self.config.write_bytes(self.original)
        self.config.chmod(0o600)
        self.binary = self.bindir / 'needle-agent'
        self.binary.write_text('#!/bin/sh\n# original binary\nexit 0\n')
        self.binary.chmod(0o755)
        self.service = self.root / 'needle-agent.service'
        self.service.write_text('[Service]\nUser=root\n')
        self.download = self.root / 'download'
        self.download.mkdir(mode=0o700)
        (self.download / 'needle-agent').write_text(
            '#!/bin/sh\n# candidate binary\n! grep -q invalid-config "$2"\n')
        (self.download / 'needle-agent').chmod(0o755)
        (self.download / 'agent.yaml').write_bytes(self.original)
        self.active = self.root / 'active'
        self.enabled = self.root / 'enabled'
        self.active.touch()
        self.enabled.touch()
        self.log = self.root / 'commands'

    def run_shell(self, commands='deploy_agent', before='', answers=''):
        q = lambda x: shlex.quote(str(x))
        setup = f'''
. {q(self.library)}
INSTALL_DIR={q(self.directory)}
BIN_DIR={q(self.bindir)}
AGENT_YAML={q(self.config)}
SERVICE_FILE={q(self.service)}
TMP_DIR={q(self.download)}
TEST_ROOT={q(self.root)}
require_root() {{ :; }}
require_linux_runtime() {{ :; }}
ensure_agent_account() {{ echo account >> "$TEST_ROOT/commands"; }}
getent() {{ [ "$1" != passwd ] || echo 'needle-agent:x:123:123::/nonexistent:/usr/sbin/nologin'; }}
chown() {{ echo "chown $*" >> "$TEST_ROOT/commands"; }}
stat() {{ python3 -c 'import os,sys; s=os.stat(sys.argv[1]); print(str(s.st_uid)+":"+str(s.st_gid)+":"+oct(s.st_mode & 4095)[2:])' "$3"; }}
install() {{
  local directory=false mode=
  while [ "$#" -gt 0 ]; do
    case "$1" in
      -d) directory=true; shift ;;
      -o|-g) shift 2 ;;
      -m) mode=$2; shift 2 ;;
      *) break ;;
    esac
  done
  if [ "$directory" = true ]; then mkdir -p "$1"; chmod "$mode" "$1"
  else command install -m "$mode" "$1" "$2"; fi
}}
runuser() {{
  [ "${{FAIL_READ:-0}}" != 1 ] || return 1
  shift 3
  "$@"
}}
wait_agent_active() {{ [ -f "$TEST_ROOT/active" ]; }}
systemctl() {{
  echo "$*" >> "$TEST_ROOT/commands"
  case "$1" in
    is-active) [ -f "$TEST_ROOT/active" ] ;;
    is-enabled) [ -f "$TEST_ROOT/enabled" ] ;;
    stop) rm -f "$TEST_ROOT/active" ;;
    start)
      if [ "${{FAIL_START:-0}}" = 1 ] && grep -q 'User=needle-agent' "$SERVICE_FILE"; then return 1; fi
      touch "$TEST_ROOT/active" ;;
    enable) touch "$TEST_ROOT/enabled" ;;
    disable) rm -f "$TEST_ROOT/enabled" ;;
    daemon-reload)
      if [ "${{FAIL_RELOAD:-0}}" = 1 ] && [ ! -f "$TEST_ROOT/reload-failed" ]; then
        touch "$TEST_ROOT/reload-failed"; return 1
      fi ;;
    show)
      case "$3" in
        User|Group) if [ "${{OVERRIDE_ROOT:-0}}" = 1 ]; then echo root; else echo needle-agent; fi ;;
        ProtectSystem) echo strict ;;
        CapabilityBoundingSet) if [ "${{OVERRIDE_CAPS:-0}}" = 1 ]; then echo cap_net_admin; fi ;;
        AmbientCapabilities|SupplementaryGroups) : ;;
        *) echo yes ;;
      esac ;;
  esac
}}
{before}
{commands}
'''
        return subprocess.run(['/bin/bash', '-c', setup], input=answers, text=True, capture_output=True)

    def assert_originals(self):
        self.assertEqual(self.config.read_bytes(), self.original)
        self.assertEqual(self.config.stat().st_mode & 0o777, 0o600)
        self.assertIn('original binary', self.binary.read_text())
        self.assertEqual(self.service.read_text(), '[Service]\nUser=root\n')
        self.assertTrue(self.active.exists())
        self.assertTrue(self.enabled.exists())

    def test_upgrade_migrates_permissions_and_preserves_token(self):
        result = self.run_shell()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.config.read_bytes(), self.original)
        self.assertEqual(self.config.stat().st_mode & 0o777, 0o640)
        self.assertEqual(self.directory.stat().st_mode & 0o777, 0o750)
        self.assertEqual(self.binary.stat().st_mode & 0o777, 0o755)
        unit = self.service.read_text()
        for line in ['User=needle-agent', 'Group=needle-agent', 'NoNewPrivileges=true',
                     'ProtectSystem=strict', 'ProtectHome=true', 'PrivateTmp=true',
                     'PrivateDevices=true', 'CapabilityBoundingSet=', 'AmbientCapabilities=',
                     'RestrictSUIDSGID=true', 'Restart=always', 'WantedBy=multi-user.target']:
            self.assertIn(line + '\n', unit)
        self.assertNotIn('ReadWritePaths=', unit)

    def test_new_install_creates_enabled_running_service(self):
        for path in [self.config, self.binary, self.service, self.active, self.enabled]:
            path.unlink()
        result = self.run_shell()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(self.active.exists())
        self.assertTrue(self.enabled.exists())
        self.assertEqual(self.config.read_bytes(), self.original)

    def test_disabled_stopped_service_stays_disabled_stopped(self):
        self.active.unlink()
        self.enabled.unlink()
        result = self.run_shell()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(self.active.exists())
        self.assertFalse(self.enabled.exists())
        self.assertIn('User=needle-agent', self.service.read_text())

    def test_invalid_candidate_does_not_stop_or_modify_old_installation(self):
        (self.download / 'agent.yaml').write_text('invalid-config\n')
        result = self.run_shell()
        self.assertNotEqual(result.returncode, 0)
        self.assert_originals()
        self.assertFalse(self.log.exists())

    def test_post_commit_failures_restore_old_files_permissions_and_state(self):
        for failure in ['FAIL_READ', 'FAIL_START', 'FAIL_RELOAD', 'OVERRIDE_ROOT', 'OVERRIDE_CAPS']:
            with self.subTest(failure=failure):
                result = self.run_shell(before=f'{failure}=1')
                self.assertNotEqual(result.returncode, 0)
                self.assert_originals()
                self.assertIn('restoring previous', result.stderr)
                self.assertEqual(list(self.bindir.glob('.needle-agent.*')), [])
                self.assertEqual(list(self.directory.glob('.agent.yaml.*')), [])

    def test_reconfigure_backup_is_root_only_and_old_backup_restored_on_failure(self):
        backup = Path(str(self.config) + '.bak')
        backup.write_text('previous backup')
        (self.download / 'agent.yaml').write_bytes(self.original + b'billing_period: forever\n')
        result = self.run_shell(before='UPGRADE_RECONFIGURE=true; FAIL_START=1')
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(backup.read_text(), 'previous backup')
        self.assert_originals()
        result = self.run_shell(before='UPGRADE_RECONFIGURE=true')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(backup.read_bytes(), self.original)
        self.assertEqual(backup.stat().st_mode & 0o777, 0o600)

    def test_symlink_configuration_rejected_before_mutation(self):
        external = self.root / 'external-secret'
        external.write_bytes(self.original)
        self.config.unlink()
        self.config.symlink_to(external)
        result = self.run_shell()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(external.read_bytes(), self.original)
        self.assertTrue(self.active.exists())

    def test_unrelated_or_privileged_account_is_rejected(self):
        for entry, groups in [
            ('needle-agent:x:0:123::/nonexistent:/usr/sbin/nologin', '123'),
            ('needle-agent:x:123:123::/home/user:/bin/bash', '123'),
            ('needle-agent:x:123:123::/nonexistent:/usr/sbin/nologin', '123 27'),
        ]:
            before = f'''
getent() {{
  if [ "$1" = passwd ]; then echo {shlex.quote(entry)}
  else echo 'needle-agent:x:123:'; fi
}}
id() {{ echo {shlex.quote(groups)}; }}
'''
            result = self.run_shell('validate_agent_account', before=before)
            self.assertNotEqual(result.returncode, 0)


if __name__ == '__main__':
    unittest.main()
