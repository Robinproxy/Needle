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

    def run_shell(self, commands, mock_service=True, input_text=None):
        q = lambda value: shlex.quote(str(value))
        setup = f'''
. {q(self.lib)}
BINARY={q(self.binary)}
CONFIG_DIR={q(self.config_dir)}
CONFIG={q(self.config)}
SERVICE={q(self.service)}
BACKUP_LIST={q(self.backup)}
require_system() {{ :; }}
uname() {{ echo x86_64; }}
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
        result = self.run_shell(commands, input_text="router's name\nhttps://needle.example.com\n\nsecret: #token\n\n\npppoe-wan\n\n1000\n")
        self.assertEqual(result.returncode, 0, result.stderr)
        for value in ["hostname: 'router''s name'", "token: 'secret: #token'", "billing_period: 'forever'", "expires_at: ''", "interfaces: ['pppoe-wan']", 'allow_plain_http: false', 'monthly_traffic_limit_gb: 1000.000']:
            self.assertIn(value, result.stdout)

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
        result = self.run_shell('main upgrade --version v9.9.9')
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(before, self.binary.read_bytes())
        actions = self.log.read_text().splitlines()
        self.assertEqual(actions[-1], 'disable')
        self.assertEqual(actions.count('start'), 1)  # failed new version only

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

    def test_uninstall_keeps_config(self):
        result = self.run_shell('main uninstall --yes')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(self.config.exists())
        self.assertFalse(self.binary.exists())
        self.assertFalse(self.service.exists())
        self.assertEqual(self.log.read_text().splitlines(), ['stop','disable'])

    def test_failed_unregister_restores_service(self):
        self.binary.write_text('#!/bin/sh\nexit 1\n')
        result = self.run_shell('main uninstall --yes --unregister')
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue(self.binary.exists())
        self.assertTrue(self.config.exists())
        self.assertEqual(self.log.read_text().splitlines(), ['stop','start'])

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
        for expected in ['USE_PROCD=1', 'respawn 3600 10 0', 'stdout 1', 'stderr 1', 'command /usr/bin/needle-agent /etc/needle/agent.yaml']:
            self.assertIn(expected, service)
        subprocess.run(['/bin/sh', '-n', str(self.service)], check=True)

if __name__ == '__main__':
    unittest.main()
