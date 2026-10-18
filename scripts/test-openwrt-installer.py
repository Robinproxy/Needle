#!/usr/bin/env python3
"""Exercise installer lifecycle with isolated files and mocked router services."""
import hashlib
import io
import os
from pathlib import Path
import shlex
import subprocess
import tarfile
import tempfile
import unittest

SCRIPT = Path(__file__).with_name('needle-agent-openwrt.sh')

class InstallerTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        source = SCRIPT.read_text()
        assert source.endswith('main "$@"\n')
        self.lib = self.root / 'installer.sh'
        self.lib.write_text(source.removesuffix('main "$@"\n'))
        self.config_dir = self.root / 'config'
        self.config_dir.mkdir()
        self.config = self.config_dir / 'agent.yaml'
        self.config.write_text("token: 'keep-me'\nbilling_period: 'forever'\n")
        self.binary = self.root / 'needle-agent'
        self.binary.write_text('#!/bin/sh\nexit 0\n')
        self.binary.chmod(0o755)
        self.service = self.root / 'service'
        self.backup = self.root / 'sysupgrade.conf'
        self.lock = self.root / 'installer.lock'
        self.rc_dir = self.root / 'rc.d'
        self.rc_dir.mkdir()
        self.meminfo = self.root / 'meminfo'
        self.meminfo.write_text('MemTotal:         262144 kB\nMemAvailable:     131072 kB\n')
        self.log = self.root / 'service.log'
        self.service.write_text(f'#!/bin/sh\necho "$1" >> {shlex.quote(str(self.log))}\n')
        self.service.chmod(0o755)
        self.archive = self.root / 'fixture.tar.gz'
        with tarfile.open(self.archive, 'w:gz') as tar:
            data = b'#!/bin/sh\n# new agent\nexit 0\n'
            entry = tarfile.TarInfo('needle-agent')
            entry.size = len(data)
            entry.mode = 0o755
            tar.addfile(entry, io.BytesIO(data))
        digest = hashlib.sha256(self.archive.read_bytes()).hexdigest()
        self.checksum = self.root / 'fixture.sha256'
        self.checksum.write_text(f'{digest}  needle-agent-linux-amd64.tar.gz\n')
        self.sha = self.root / 'sha256sum'
        self.sha.write_text('#!/usr/bin/env python3\nimport hashlib,sys\nprint(hashlib.sha256(open(sys.argv[1],"rb").read()).hexdigest(),sys.argv[1])\n')
        self.sha.chmod(0o755)

    def run_shell(self, commands, mock_service=True, input_text=None, mock_wait=True):
        q = lambda value: shlex.quote(str(value))
        setup = f'''
. {q(self.lib)}
BINARY={q(self.binary)}
CONFIG_DIR={q(self.config_dir)}
CONFIG={q(self.config)}
SERVICE={q(self.service)}
BACKUP_LIST={q(self.backup)}
LOCK_DIR={q(self.lock)}
RC_DIR={q(self.rc_dir)}
MEMINFO={q(self.meminfo)}
require_system() {{ :; }}
uname() {{ echo x86_64; }}
mktemp() {{ command mktemp -d {q(self.root)}/needle-agent.XXXXXX; }}
fetch() {{
 case "$1" in
  *.sha256) cp {q(self.checksum)} "$2" ;;
  *.tar.gz) cp {q(self.archive)} "$2" ;;
  *) printf '%s' '{{"tag_name":"v9.9.9"}}' > "$2" ;;
 esac
}}
'''
        if mock_service:
            setup += 'write_service() { :; }\n'
        if mock_wait:
            setup += 'wait_stopped() { :; }\n'
        env = dict(os.environ, PATH=f'{self.root}:{os.environ["PATH"]}')
        return subprocess.run(['/bin/sh', '-c', setup + commands], env=env, text=True, capture_output=True, input=input_text)

    def test_initial_configuration_defaults_and_escaping(self):
        network = self.root / 'net'
        (network / 'pppoe-wan').mkdir(parents=True)
        commands = f"""
require_interactive() {{ :; }}
prompt() {{ IFS= read -r ANSWER; }}
NETWORK_DIR={shlex.quote(str(network))}
TMP_DIR=$(mktemp -d /tmp/needle-agent-test.XXXXXX)
configure
cat "$TMP_DIR/agent.yaml"
"""
        result = self.run_shell(commands, input_text="router's name\nhttps://needle.example.com\n\nsecret: #token\n030\n\npppoe-wan\n\n1000\n")
        self.assertEqual(result.returncode, 0, result.stderr)
        for value in ["hostname: 'router''s name'", "token: 'secret: #token'", "billing_period: 'forever'", "expires_at: ''", "interfaces: ['pppoe-wan']", 'interval: 30\n', 'allow_plain_http: false', 'monthly_traffic_limit_gb: 1000.000']:
            self.assertIn(value, result.stdout)

    def test_empty_token_uses_kernel_uuid_without_od(self):
        network = self.root / 'net'
        (network / 'eth1').mkdir(parents=True)
        uuid = self.root / 'random-uuid'
        uuid.write_text('c2c625be-145e-46f2-b616-6409fe41fbdd\n')
        before = self.config.read_bytes()
        result = self.run_shell(f'''
require_interactive() {{ :; }}
prompt() {{ IFS= read -r ANSWER; }}
od() {{ echo 'od must not be required' >&2; return 127; }}
RANDOM_UUID={shlex.quote(str(uuid))}
NETWORK_DIR={shlex.quote(str(network))}
TMP_DIR=$(mktemp -d /tmp/needle-agent-test.XXXXXX)
configure
cat "$TMP_DIR/agent.yaml"
''', input_text='router\nhttps://needle.example.com\n\n\n\n\neth1\n5\n0\n')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("token: 'c2c625be145e46f2b6166409fe41fbdd'", result.stdout)
        self.assertNotIn('od must not be required', result.stderr)
        self.assertEqual(before, self.config.read_bytes())
        self.assertFalse(self.log.exists())

    def test_failed_token_generation_stops_before_remaining_prompts(self):
        uuid = self.root / 'random-uuid'
        before = {path: path.read_bytes() for path in [self.config, self.binary, self.service]}
        for content in [None, '', 'not-a-uuid\n', 'g' * 32 + '\n']:
            with self.subTest(content=content):
                if content is not None:
                    uuid.write_text(content)
                result = self.run_shell(f'''
require_interactive() {{ :; }}
prompt() {{ printf '%s' "$1" >&2; IFS= read -r ANSWER; }}
RANDOM_UUID={shlex.quote(str(uuid))}
TMP_DIR=$(mktemp -d /tmp/needle-agent-test.XXXXXX)
configure
''', input_text='router\nhttps://needle.example.com\n\n\n')
                self.assertNotEqual(result.returncode, 0)
                self.assertIn('enter a unique token manually', result.stderr)
                self.assertNotIn('Report interval', result.stderr)
                for path, data in before.items():
                    self.assertEqual(path.read_bytes(), data)
                self.assertFalse(self.log.exists())

    def test_upgrade_preserves_config_and_restarts(self):
        before = self.config.read_bytes()
        result = self.run_shell('main upgrade\nmain upgrade --version v9.9.9')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(before, self.config.read_bytes())
        self.assertIn('new agent', self.binary.read_text())
        self.assertEqual([line for line in self.log.read_text().splitlines() if line not in ('running', 'enabled')], ['stop','enable','start'] * 2)
        self.assertEqual(self.backup.read_text().splitlines(), [str(self.config_dir) + '/'])
        self.assertEqual(self.config.stat().st_mode & 0o777, 0o600)

    def test_reconfigure_upgrade_preserves_token_and_other_settings(self):
        old = "hostname: 'home-router'\ntoken: 'keep:# token with ''quotes'''\nserver: https://needle.example.com\ntcpping: []\ninterval: 60\ninterfaces: [pppoe-wan]\n"
        self.config.write_text(old)
        result = self.run_shell("""
upgrade_interactive() { true; }
tcping_prompt() { IFS= read -r TCPING_ANSWER; }
main upgrade --reconfigure --version v9.9.9
""", input_text='2\n\n')
        self.assertEqual(result.returncode, 0, result.stderr)
        actual = self.config.read_text()
        for line in old.splitlines():
            if not line.startswith('tcpping:'): self.assertIn(line+'\n',actual)
        self.assertIn("name: 'CMv6'",actual)
        self.assertNotIn('v4.ip.',actual)
        self.assertEqual(Path(str(self.config)+'.bak').read_text(),old)
        self.assertEqual(Path(str(self.config)+'.bak').stat().st_mode & 0o777,0o600)
        result = self.run_shell('main uninstall --yes --purge')
        self.assertEqual(result.returncode,0,result.stderr)
        self.assertFalse(Path(str(self.config)+'.bak').exists())

    def test_failed_reconfigure_upgrade_restores_original_configuration(self):
        old=self.config.read_bytes()
        self.service.write_text(f'#!/bin/sh\nif [ "$1" = start ] && grep -q "new agent" {shlex.quote(str(self.binary))}; then exit 1; fi\nexit 0\n')
        self.service.chmod(0o755)
        result=self.run_shell("""
upgrade_interactive() { true; }
tcping_prompt() { IFS= read -r TCPING_ANSWER; }
main upgrade --reconfigure
""",input_text='5\n\n')
        self.assertNotEqual(result.returncode,0)
        self.assertEqual(self.config.read_bytes(),old)

    def test_failed_start_restores_previous_installation(self):
        self.service.write_text(f'#!/bin/sh\nif [ "$1" = start ] && grep -q "new agent" {shlex.quote(str(self.binary))}; then exit 1; fi\necho "$1" >> {shlex.quote(str(self.log))}\n')
        self.service.chmod(0o755)
        self.backup.write_text('/etc/custom/\n')
        (self.config_dir / 'version').write_text('v1.0.0\n')
        before = {path: path.read_bytes() for path in [self.binary, self.config, self.service, self.backup, self.config_dir / 'version']}
        result = self.run_shell('main upgrade --version v9.9.9')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('restoring the previous', result.stderr)
        for path, content in before.items():
            self.assertEqual(path.read_bytes(), content, str(path))
        self.assertEqual([line for line in self.log.read_text().splitlines() if line != 'running'][-1], 'start')

    def test_rollback_preserves_stopped_and_disabled_service(self):
        self.service.write_text(f'#!/bin/sh\necho "$1" >> {shlex.quote(str(self.log))}\ncase "$1" in running|enabled) exit 1 ;; start) exit 1 ;; esac\n')
        self.service.chmod(0o755)
        before = self.binary.read_bytes()
        result = self.run_shell('write_service() { return 1; }; main upgrade --version v9.9.9')
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(before, self.binary.read_bytes())
        actions = self.log.read_text().splitlines()
        self.assertEqual(actions[-1], 'disable')
        self.assertEqual(actions.count('start'), 0)

    def test_successful_upgrade_preserves_stopped_and_disabled_state(self):
        self.service.write_text(f'''#!/bin/sh
echo "$1" >> {shlex.quote(str(self.log))}
case "$1" in running|enabled) exit 1 ;; esac
''')
        result = self.run_shell('main upgrade --version v9.9.9')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('new agent', self.binary.read_text())
        actions = self.log.read_text().splitlines()
        self.assertNotIn('start', actions)
        self.assertNotIn('enable', actions)
        self.assertFalse(self.lock.exists())

    def test_immediate_crash_is_not_reported_as_success(self):
        self.service.write_text(f'#!/bin/sh\nif [ "$1" = running ] && grep -q "new agent" {shlex.quote(str(self.binary))}; then exit 1; fi\nexit 0\n')
        self.service.chmod(0o755)
        before = self.binary.read_bytes()
        result = self.run_shell('main upgrade --version v9.9.9')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('did not stay running', result.stderr)
        self.assertEqual(before, self.binary.read_bytes())
        self.assertFalse((self.config_dir / 'version').exists())

    def test_failed_first_install_removes_partial_installation(self):
        self.config.unlink()
        self.binary.unlink()
        self.service.unlink()
        result = self.run_shell("""
require_interactive() { :; }
configure() { printf 'billing_period: forever\\n' > "$TMP_DIR/agent.yaml"; }
write_service() {
    cat > "$SERVICE" <<'SERVICE'
#!/bin/sh
case "$1" in
    enable) touch "${0}.autostart" ;;
    disable) rm -f "${0}.autostart" ;;
    start|running) exit 1 ;;
esac
SERVICE
    chmod 755 "$SERVICE"
}
main install --version v9.9.9
""")
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.config.exists())
        self.assertFalse(self.binary.exists())
        self.assertFalse(self.service.exists())
        self.assertFalse(self.backup.exists())
        self.assertFalse(Path(str(self.service) + ".autostart").exists())

    def test_missing_checksum_and_unsupported_architecture_preserve_installation(self):
        before = self.binary.read_bytes()
        for command in [
            'fetch() { case "$1" in *.sha256) return 1 ;; *) cp ' + shlex.quote(str(self.archive)) + ' "$2" ;; esac; }; main upgrade --version v9.9.9',
            'uname() { echo mips; }; main upgrade --version v9.9.9',
        ]:
            result = self.run_shell(command)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(before, self.binary.read_bytes())
            self.assertFalse(self.log.exists())

    def test_bad_checksum_leaves_installation_running(self):
        self.checksum.write_text('0' * 64 + '  fixture.tar.gz\n')
        before = self.binary.read_bytes()
        result = self.run_shell('main upgrade')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('Checksum mismatch', result.stderr)
        self.assertEqual(before, self.binary.read_bytes())
        self.assertFalse(self.log.exists())

    def test_low_overlay_space_aborts_before_touching_running_installation(self):
        self.config_dir.chmod(0o750)
        self.backup.write_text('/etc/keep-this-entry/\n')
        before = {path: (path.read_bytes(), path.stat().st_mode & 0o777)
                  for path in [self.binary, self.config, self.service, self.backup]}
        result = self.run_shell('''
df() {
    printf 'Filesystem 1024-blocks Used Available Capacity Mounted on\n'
    if [ "$2" = /tmp ]; then
        printf 'tmpfs 200000 100000 100000 50%% /tmp\n'
    else
        printf 'overlay 100000 99999 1 100%% /overlay\n'
    fi
}
main upgrade --version v9.9.9
''')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('Insufficient free space', result.stderr)
        for path, (content, mode) in before.items():
            self.assertEqual(path.read_bytes(), content, str(path))
            self.assertEqual(path.stat().st_mode & 0o777, mode, str(path))
        self.assertEqual(self.config_dir.stat().st_mode & 0o777, 0o750)
        self.assertFalse(self.log.exists())
        self.assertFalse(Path(str(self.binary) + '.new').exists())
        self.assertFalse(self.lock.exists())
        self.assertFalse(list(self.root.glob('needle-agent.*')))

    def test_low_available_memory_aborts_before_download_or_service_changes(self):
        self.meminfo.write_text('MemTotal:         262144 kB\nMemAvailable:       1024 kB\n')
        requested = self.root / 'download-requested'
        before = {path: path.read_bytes() for path in [self.binary, self.config, self.service]}
        result = self.run_shell(f'''
download() {{ touch {shlex.quote(str(requested))}; return 1; }}
main upgrade --version v9.9.9
''')
        self.assertNotEqual(result.returncode, 0)
        self.assertRegex(result.stderr.lower(), 'memory|memavailable')
        self.assertFalse(requested.exists())
        for path, content in before.items():
            self.assertEqual(path.read_bytes(), content, str(path))
        self.assertFalse(self.log.exists())
        self.assertFalse(self.lock.exists())
        self.assertFalse(list(self.root.glob('needle-agent.*')))

    def test_low_temporary_space_aborts_before_download_or_service_changes(self):
        requested = self.root / 'download-requested'
        before = {path: path.read_bytes() for path in [self.binary, self.config, self.service]}
        result = self.run_shell(f'''
df() {{
    printf 'Filesystem 1024-blocks Used Available Capacity Mounted on\\n'
    if [ "$2" = /tmp ]; then
        printf 'tmpfs 100000 99999 1 100%% /tmp\\n'
    else
        printf 'overlay 200000 100000 100000 50%% /overlay\\n'
    fi
}}
download() {{ touch {shlex.quote(str(requested))}; return 1; }}
main upgrade --version v9.9.9
''')
        self.assertNotEqual(result.returncode, 0)
        self.assertRegex(result.stderr.lower(), 'space|/tmp|temporary')
        self.assertFalse(requested.exists())
        for path, content in before.items():
            self.assertEqual(path.read_bytes(), content, str(path))
        self.assertFalse(self.log.exists())
        self.assertFalse(self.lock.exists())
        self.assertFalse(list(self.root.glob('needle-agent.*')))

    def test_advertised_config_check_failure_aborts_before_service_stop(self):
        checked_path = self.root / 'checked-config'
        script = f'''#!/bin/sh
case "$1" in
    -h) printf '  -check-config string\\n'; exit 0 ;;
    -check-config) printf '%s' "$2" > {shlex.quote(str(checked_path))}; exit 1 ;;
esac
exit 0
'''.encode()
        with tarfile.open(self.archive, 'w:gz') as archive:
            entry = tarfile.TarInfo('needle-agent')
            entry.size = len(script)
            entry.mode = 0o755
            archive.addfile(entry, io.BytesIO(script))
        self.checksum.write_text(hashlib.sha256(self.archive.read_bytes()).hexdigest() + '  fixture.tar.gz\n')
        before = {path: path.read_bytes() for path in [self.binary, self.config, self.service]}
        result = self.run_shell('main upgrade --version v9.9.9')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('Configuration validation failed', result.stderr)
        self.assertEqual(checked_path.read_text(), str(self.config))
        for path, content in before.items():
            self.assertEqual(path.read_bytes(), content, str(path))
        self.assertFalse(self.log.exists())
        self.assertFalse(Path(str(self.binary) + '.new').exists())
        self.assertFalse(self.lock.exists())
        self.assertFalse(list(self.root.glob('needle-agent.*')))

    def test_archive_links_cannot_touch_unrelated_router_files(self):
        before = self.binary.read_bytes()
        for link_type, name in [(tarfile.SYMTYPE, 'symlink'), (tarfile.LNKTYPE, 'hardlink')]:
            with self.subTest(member=name):
                victim = self.root / ('router-config-' + name)
                victim.write_text('#!/bin/sh\n# Unrelated router configuration\nexit 0\n')
                victim.chmod(0o600)
                with tarfile.open(self.archive, 'w:gz') as archive:
                    entry = tarfile.TarInfo('needle-agent')
                    entry.type = link_type
                    entry.linkname = str(victim)
                    entry.mode = 0o755
                    archive.addfile(entry)
                self.checksum.write_text(hashlib.sha256(self.archive.read_bytes()).hexdigest() + '  fixture.tar.gz\n')
                result = self.run_shell('main upgrade --version v9.9.9')
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(victim.read_text(), '#!/bin/sh\n# Unrelated router configuration\nexit 0\n')
                self.assertEqual(victim.stat().st_mode & 0o777, 0o600)
                self.assertEqual(self.binary.read_bytes(), before)
                self.assertFalse(self.log.exists())
                self.assertFalse(self.lock.exists())
                self.assertFalse(list(self.root.glob('needle-agent.*')))

    def test_backup_list_missing_trailing_newline_keeps_unrelated_entries(self):
        self.backup.write_bytes(b'/etc/custom-without-newline')
        result = self.run_shell('main upgrade --version v9.9.9')
        self.assertEqual(result.returncode, 0, result.stderr)
        expected = '/etc/custom-without-newline\n' + str(self.config_dir) + '/\n'
        self.assertEqual(self.backup.read_text(), expected)
        with self.backup.open('a') as stream:
            stream.write('/etc/unrelated-without-newline')
        result = self.run_shell('main uninstall --yes --purge')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.backup.read_text(), '/etc/custom-without-newline\n/etc/unrelated-without-newline\n')
        self.assertFalse(self.config.exists())
        self.assertFalse(self.lock.exists())

    def test_existing_configuration_directory_mode_is_preserved_without_stat(self):
        self.config_dir.chmod(0o750)
        unrelated = self.config_dir / 'unrelated-user-file'
        unrelated.write_text('Preserve this file and directory permissions\n')
        result = self.run_shell('stat() { return 127; }; main upgrade --version v9.9.9')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.config_dir.stat().st_mode & 0o777, 0o750)
        self.assertEqual(unrelated.read_text(), 'Preserve this file and directory permissions\n')
        before = self.binary.read_bytes()
        result = self.run_shell(f'''
stat() {{ return 127; }}
cp() {{
    for CP_ARG do CP_DEST=$CP_ARG; done
    if [ "$CP_DEST" = {shlex.quote(str(self.binary) + '.new')} ]; then
        printf 'partial binary' > "$CP_DEST"
        return 1
    fi
    command cp "$@"
}}
main upgrade --version v9.9.9
''')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('restoring the previous', result.stderr)
        self.assertEqual(self.binary.read_bytes(), before)
        self.assertEqual(self.config_dir.stat().st_mode & 0o777, 0o750)
        self.assertEqual(unrelated.read_text(), 'Preserve this file and directory permissions\n')
        self.assertFalse(Path(str(self.binary) + '.new').exists())
        self.assertFalse(self.lock.exists())

    def test_uninstall_keeps_config(self):
        result = self.run_shell('main uninstall --yes')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(self.config.exists())
        self.assertFalse(self.binary.exists())
        self.assertFalse(self.service.exists())
        self.assertEqual([line for line in self.log.read_text().splitlines() if line not in ('running', 'enabled')], ['stop','disable'])

    def test_failed_unregister_restores_service(self):
        self.binary.write_text('#!/bin/sh\nexit 1\n')
        result = self.run_shell('main uninstall --yes --unregister')
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue(self.binary.exists())
        self.assertTrue(self.config.exists())
        self.assertEqual([line for line in self.log.read_text().splitlines() if line not in ('running', 'enabled')], ['stop','start'])

    def test_failed_unregister_does_not_start_previously_stopped_service(self):
        self.binary.write_text('#!/bin/sh\nexit 1\n')
        self.service.write_text(f'''#!/bin/sh
echo "$1" >> {shlex.quote(str(self.log))}
case "$1" in running|enabled) exit 1 ;; esac
''')
        result = self.run_shell('main uninstall --yes --unregister')
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue(self.binary.exists())
        self.assertTrue(self.config.exists())
        self.assertNotIn('start', self.log.read_text().splitlines())
        self.assertFalse(self.lock.exists())

    def test_interrupted_unregister_restores_original_running_state(self):
        self.binary.write_text('#!/bin/sh\nkill -TERM "$PPID"\nsleep 0.1\nexit 1\n')
        before = {path: path.read_bytes() for path in [self.binary, self.config, self.service]}
        result = self.run_shell('main uninstall --yes --unregister')
        self.assertNotEqual(result.returncode, 0)
        for path, content in before.items():
            self.assertEqual(path.read_bytes(), content, str(path))
        actions = [line for line in self.log.read_text().splitlines() if line not in ('running', 'enabled')]
        self.assertEqual(actions, ['stop', 'start'])
        self.assertFalse(self.lock.exists())

    def test_uninstall_waits_for_process_exit_before_disabling_and_removing(self):
        stopping = self.root / 'stopping'
        polls = self.root / 'running-polls'
        self.service.write_text(f'''#!/bin/sh
echo "$1" >> {shlex.quote(str(self.log))}
case "$1" in
    stop) touch {shlex.quote(str(stopping))} ;;
    running)
        [ -f {shlex.quote(str(stopping))} ] || exit 0
        count=$(cat {shlex.quote(str(polls))} 2>/dev/null || echo 0)
        count=$((count + 1))
        printf '%s' "$count" > {shlex.quote(str(polls))}
        [ "$count" -lt 3 ]
        exit $?
        ;;
esac
''')
        result = self.run_shell('sleep() { :; }; main uninstall --yes', mock_wait=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(int(polls.read_text()), 3)
        actions = self.log.read_text().splitlines()
        self.assertLess(max(index for index, action in enumerate(actions) if action == 'running'), actions.index('disable'))
        self.assertFalse(self.binary.exists())
        self.assertFalse(self.service.exists())
        self.assertFalse(self.lock.exists())

    def test_uninstall_timeout_retains_files_if_agent_does_not_stop(self):
        before = {path: path.read_bytes() for path in [self.binary, self.config, self.service]}
        result = self.run_shell('sleep() { :; }; main uninstall --yes --purge', mock_wait=False)
        self.assertNotEqual(result.returncode, 0)
        for path, content in before.items():
            self.assertEqual(path.read_bytes(), content, str(path))
        actions = self.log.read_text().splitlines()
        self.assertGreater(actions.count('running'), 1)
        self.assertNotIn('disable', actions)
        self.assertFalse(self.lock.exists())

    def test_upgrade_rejects_symbolic_links_without_touching_target(self):
        paths = {
            'binary': self.binary,
            'config': self.config,
            'service': self.service,
            'version': self.config_dir / 'version',
            'backup-list': self.backup,
            'binary-stage': Path(str(self.binary) + '.new'),
            'config-stage': Path(str(self.config) + '.new'),
            'service-stage': Path(str(self.service) + '.new'),
            'config-backup': Path(str(self.config) + '.bak'),
            'config-backup-stage': Path(str(self.config) + '.bak.new'),
            'backup-list-stage': Path(str(self.backup) + '.needle.tmp'),
            'binary-restore': Path(str(self.binary) + '.needle-restore'),
            'config-restore': Path(str(self.config) + '.needle-restore'),
        }
        for name, path in paths.items():
            with self.subTest(path=name):
                existed = path.exists()
                old_content = path.read_bytes() if existed else None
                old_mode = path.stat().st_mode & 0o777 if existed else None
                if existed:
                    path.unlink()
                victim = self.root / ('unrelated-router-file-' + name)
                victim.write_text('PRESERVE THIS ROUTER FILE\n')
                path.symlink_to(victim)
                try:
                    result = self.run_shell('main upgrade --version v9.9.9')
                    self.assertNotEqual(result.returncode, 0, result.stdout)
                    self.assertEqual(victim.read_text(), 'PRESERVE THIS ROUTER FILE\n')
                    self.assertTrue(path.is_symlink())
                    self.assertFalse(self.lock.exists())
                finally:
                    if path.is_symlink():
                        path.unlink()
                    if existed:
                        path.write_bytes(old_content)
                        path.chmod(old_mode)

    def test_upgrade_rejects_symbolic_link_configuration_directory(self):
        original = self.root / 'original-config'
        self.config_dir.rename(original)
        self.config_dir.symlink_to(original, target_is_directory=True)
        original.chmod(0o755)
        before = self.binary.read_bytes()
        result = self.run_shell('main upgrade --version v9.9.9')
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(before, self.binary.read_bytes())
        self.assertEqual(original.stat().st_mode & 0o777, 0o755)
        self.assertTrue(self.config_dir.is_symlink())
        self.assertFalse(self.log.exists())
        self.assertFalse(self.lock.exists())

    def test_upgrade_does_not_overwrite_unclaimed_staging_files(self):
        stages = [
            Path(str(self.binary) + '.new'),
            Path(str(self.config) + '.new'),
            Path(str(self.service) + '.new'),
            Path(str(self.config) + '.bak.new'),
            Path(str(self.backup) + '.needle.tmp'),
            Path(str(self.config) + '.needle-restore'),
        ]
        before = self.binary.read_bytes()
        for stage in stages:
            with self.subTest(path=stage.name):
                stage.write_text('Unclaimed previous recovery data\n')
                try:
                    result = self.run_shell('main upgrade --version v9.9.9')
                    self.assertNotEqual(result.returncode, 0)
                    self.assertEqual(stage.read_text(), 'Unclaimed previous recovery data\n')
                    self.assertEqual(self.binary.read_bytes(), before)
                    self.assertFalse(self.log.exists())
                    self.assertFalse(self.lock.exists())
                finally:
                    if stage.exists():
                        stage.unlink()

    def test_failed_staging_cleans_partial_binary_and_restores_directory_mode(self):
        self.config_dir.chmod(0o755)
        before = self.binary.read_bytes()
        result = self.run_shell(f'''
cp() {{
    for CP_ARG do CP_DEST=$CP_ARG; done
    if [ "$CP_DEST" = {shlex.quote(str(self.binary) + '.new')} ]; then
        printf 'partial binary' > "$CP_DEST"
        return 1
    fi
    command cp "$@"
}}
main upgrade --version v9.9.9
''')
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(before, self.binary.read_bytes())
        self.assertFalse(Path(str(self.binary) + '.new').exists())
        self.assertEqual(self.config_dir.stat().st_mode & 0o777, 0o755)
        self.assertFalse(self.lock.exists())

    def test_failed_reconfiguration_restores_existing_backup(self):
        backup = Path(str(self.config) + '.bak')
        backup.write_text('PREVIOUS KNOWN GOOD BACKUP\n')
        backup.chmod(0o600)
        self.service.write_text(f'''#!/bin/sh
if [ "$1" = start ] && grep -q "new agent" {shlex.quote(str(self.binary))}; then exit 1; fi
exit 0
''')
        result = self.run_shell('''
upgrade_interactive() { true; }
tcping_prompt() { IFS= read -r TCPING_ANSWER; }
main upgrade --reconfigure --version v9.9.9
''', input_text='5\n\n')
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(backup.read_text(), 'PREVIOUS KNOWN GOOD BACKUP\n')
        self.assertEqual(backup.stat().st_mode & 0o777, 0o600)
        self.assertFalse(self.lock.exists())

    def test_failed_reconfiguration_without_old_backup_does_not_leave_one(self):
        backup = Path(str(self.config) + '.bak')
        self.service.write_text(f'''#!/bin/sh
if [ "$1" = start ] && grep -q "new agent" {shlex.quote(str(self.binary))}; then exit 1; fi
exit 0
''')
        result = self.run_shell('''
upgrade_interactive() { true; }
tcping_prompt() { IFS= read -r TCPING_ANSWER; }
main upgrade --reconfigure --version v9.9.9
''', input_text='5\n\n')
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(backup.exists())
        self.assertFalse(self.lock.exists())

    def test_failed_rollback_preserves_recovery_files_and_reports_directory(self):
        recovery_path = self.root / 'recovery-path'
        self.service.write_text(f'''#!/bin/sh
if [ "$1" = start ] && grep -q "new agent" {shlex.quote(str(self.binary))}; then exit 1; fi
exit 0
''')
        result = self.run_shell(f'''
cp() {{
    for CP_ARG do CP_DEST=$CP_ARG; done
    if [ "$CP_DEST" = {shlex.quote(str(self.config) + '.needle-restore')} ]; then
        printf '%s' "$TMP_DIR" > {shlex.quote(str(recovery_path))}
        return 1
    fi
    command cp "$@"
}}
main upgrade --version v9.9.9
''')
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue(recovery_path.exists(), result.stderr)
        recovery = Path(recovery_path.read_text())
        self.assertTrue(recovery.is_dir(), result.stderr)
        self.assertTrue((recovery / 'old-config').is_file())
        self.assertTrue((recovery / 'old-binary').is_file())
        self.assertIn(str(recovery), result.stderr)
        self.assertFalse(self.lock.exists())

    def test_live_installer_lock_blocks_mutations_and_allows_later_retry(self):
        self.lock.mkdir()
        (self.lock / 'pid').write_text(str(os.getpid()) + '\n')
        before = {path: path.read_bytes() for path in [self.binary, self.config, self.service]}
        for args in ['upgrade --version v9.9.9', 'uninstall --yes --purge']:
            with self.subTest(action=args):
                result = self.run_shell('main ' + args)
                self.assertNotEqual(result.returncode, 0)
                for path, content in before.items():
                    self.assertEqual(path.read_bytes(), content, str(path))
                self.assertFalse(self.log.exists())
                self.assertTrue(self.lock.exists())
        (self.lock / 'pid').unlink()
        self.lock.rmdir()
        result = self.run_shell('main upgrade --version v9.9.9')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(self.lock.exists())

    def test_purge_cleans_owned_files_preserving_other_configuration(self):
        self.backup.write_text('/etc/custom/\n' + str(self.config_dir) + '/\n/etc/unrelated/\n')
        unrelated = self.config_dir / 'unrelated.txt'
        unrelated.write_text('user-owned configuration\n')
        (self.config_dir / 'version').write_text('v9.9.9\n')
        Path(str(self.config) + '.bak').write_text('old token\n')
        result = self.run_shell('main uninstall --yes --purge')
        self.assertEqual(result.returncode, 0, result.stderr)
        for path in [self.binary, self.service, self.config, Path(str(self.config) + '.bak'), self.config_dir / 'version']:
            self.assertFalse(path.exists(), str(path))
        self.assertEqual(unrelated.read_text(), 'user-owned configuration\n')
        self.assertEqual(self.backup.read_text(), '/etc/custom/\n/etc/unrelated/\n')
        self.assertFalse(Path(str(self.backup) + '.needle.tmp').exists())
        self.assertFalse(self.lock.exists())

    def test_purge_cleans_orphaned_autostart_links_only_for_needle_agent(self):
        self.service.unlink()
        owned = [self.rc_dir / 'S99needle-agent', self.rc_dir / 'K10needle-agent']
        for path in owned:
            path.symlink_to(self.service)
        other = self.rc_dir / 'S50network'
        other.symlink_to(self.root / 'unrelated-network-service')
        result = self.run_shell('main uninstall --yes --purge')
        self.assertEqual(result.returncode, 0, result.stderr)
        for path in owned:
            self.assertFalse(path.is_symlink(), str(path))
        self.assertTrue(other.is_symlink())
        self.assertFalse(self.binary.exists())
        self.assertFalse(self.config.exists())
        self.assertFalse(self.lock.exists())

    def test_upgrade_without_config_and_invalid_options_do_not_mutate(self):
        self.config.unlink()
        for args in ['upgrade', 'install --purge', 'upgrade --version', 'upgrade --version invalid']:
            result = self.run_shell('main ' + args)
            self.assertNotEqual(result.returncode, 0, args)
        self.assertFalse(self.log.exists())

    def test_procd_and_yaml_quoting(self):
        result = self.run_shell("write_service\nyaml_quote \"router's token: #1\"", mock_service=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "'router''s token: #1'")
        service = self.service.read_text()
        for expected in ['USE_PROCD=1', 'respawn 3600 10 5', 'nice 10', 'term_timeout 25', 'GOMAXPROCS=1', 'GOMEMLIMIT=32MiB', 'core="0"', 'nofile="128 128"', 'stdout 1', 'stderr 1', 'command /usr/bin/needle-agent /etc/needle/agent.yaml']:
            self.assertIn(expected, service)
        subprocess.run(['/bin/sh', '-n', str(self.service)], check=True)

if __name__ == '__main__':
    unittest.main()
