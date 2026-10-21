#!/usr/bin/env python3
"""Check upgrade choices and byte-preservation of credentials without root."""
from pathlib import Path
import shlex
import subprocess
import tempfile
import unittest

ROOT=Path(__file__).resolve().parent
START='# TCP Ping selection is deliberately explicit; no connectivity auto-detection.'
END='# End of TCP Ping selection.'
OLD="""hostname: 'my-router'
server: 'https://needle.example.com'
token: 'token:# with ''quotes'' and spaces'
region: CN
tcpping:
  - name: Old
    target: old.example:80
interval: 60
interfaces: ['pppoe-wan']
billing_period: forever
expires_at: ''
tls_skip_verify: false
allow_plain_http: false
"""

class UpgradeConfigTests(unittest.TestCase):
    def common(self,script):
        text=(ROOT/script).read_text()
        return text[text.index(START):text.index(END)+len(END)]

    def run_choice(self,script,mode='ask',interactive=True,answers='',config=OLD):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp)
            original=root/'original.yaml';original.write_text(config)
            commands=f'''set -eu
TMP_DIR={shlex.quote(tmp)}
{self.common(script)}
tcping_prompt() {{ IFS= read -r TCPING_ANSWER; }}
upgrade_interactive() {{ {'true' if interactive else 'false'}; }}
UPGRADE_CONFIG_MODE={shlex.quote(mode)}
choose_upgrade_config {shlex.quote(str(original))}
'''
            shell='/bin/bash' if script=='needle-agent.sh' else '/bin/sh'
            result=subprocess.run([shell,'-c',commands],input=answers+"\n\n",text=True,capture_output=True)
            staged=root/'agent.yaml'
            return result, staged.read_text() if staged.exists() else None, original.read_text()

    def test_keep_and_noninteractive_leave_original_untouched(self):
        for script in ['needle-agent.sh','needle-agent-openwrt.sh']:
            for kwargs in [dict(answers='\n'),dict(answers='1\n'),dict(mode='keep'),dict(interactive=False,answers='2\n2\n\n')]:
                with self.subTest(script=script,kwargs=kwargs):
                    result,staged,original=self.run_choice(script,**kwargs)
                    self.assertEqual(result.returncode,0,result.stderr)
                    self.assertIsNone(staged)
                    self.assertEqual(original,OLD)

    def test_reselection_preserves_identity_and_other_settings(self):
        for script in ['needle-agent.sh','needle-agent-openwrt.sh']:
            result,staged,original=self.run_choice(script,answers='2\n2\n\n\n')
            self.assertEqual(result.returncode,0,result.stderr)
            self.assertEqual(original,OLD)
            for line in OLD.splitlines():
                if not line.startswith(('tcpping:','  ')):
                    self.assertIn(line+'\n',staged)
            self.assertNotIn('old.example',staged)
            self.assertEqual(staged.count('  - name:'),3)
            self.assertIn("name: 'CMv6'",staged)
            self.assertNotIn('v4.ip.',staged)

    def test_explicit_reconfigure_and_disabled(self):
        for script in ['needle-agent.sh','needle-agent-openwrt.sh']:
            result,staged,_=self.run_choice(script,mode='reconfigure',answers='5\n\n\n')
            self.assertEqual(result.returncode,0,result.stderr)
            self.assertIn('tcpping: []\n',staged)
            self.assertIn("token: 'token:# with ''quotes'' and spaces'\n",staged)

    def test_input_failure_and_unsupported_layout_leave_original_untouched(self):
        for script in ['needle-agent.sh','needle-agent-openwrt.sh']:
            for kwargs in [dict(answers='2\n'),dict(answers='9\n'),dict(mode='reconfigure',interactive=False),dict(answers='2\n5\n\n',config='{token: keep-me, tcpping: []}\n'),dict(answers='2\n5\n\n',config='tcpping:\n  - name: &identity VPS\n    target: example.com:443\ntoken: *identity\n')]:
                with self.subTest(script=script,kwargs=kwargs):
                    result,_,original=self.run_choice(script,**kwargs)
                    self.assertNotEqual(result.returncode,0)
                    self.assertEqual(original,kwargs.get('config',OLD))

    def test_yaml_key_styles_inline_lists_and_multiline_token(self):
        for config in [
            '"token": "a:#b"\n"hostname": host\n"tcpping": []\ninterval: 30\n',
            "'token': 'keep-me'\n'hostname': host\n'tcpping': []\ninterval: 30\n",
            '---\ntoken: |-\n  token:with:colons\nhostname: host\ntcpping: []\n...\n',
            'token: keep-me\nhostname: host\ninterval: 30\n',
        ]:
            result,staged,original=self.run_choice('needle-agent-openwrt.sh',mode='reconfigure',answers='5\n\n\n',config=config)
            self.assertEqual(result.returncode,0,result.stderr)
            self.assertEqual(original,config)
            self.assertIn('tcpping: []',staged)
            if 'token:with:colons' in config:self.assertIn('token: |-\n  token:with:colons\n',staged)

    def test_quota_reconfiguration_preserves_identity_and_omitted_quota(self):
        config=OLD+"monthly_traffic_limit_gb: 500\n"
        for script in ['needle-agent.sh','needle-agent-openwrt.sh']:
            result,staged,_=self.run_choice(script,mode='reconfigure',answers='5\n1000\n\n',config=config)
            self.assertEqual(result.returncode,0,result.stderr)
            self.assertEqual(staged.count('monthly_traffic_limit_gb:'),1)
            self.assertIn('monthly_traffic_limit_gb: 1000.000',staged)
            self.assertIn("token: 'token:# with ''quotes'' and spaces'",staged)
            result,staged,_=self.run_choice(script,mode='reconfigure',answers='5\n\n\n',config=config)
            self.assertEqual(result.returncode,0,result.stderr)
            self.assertIn('monthly_traffic_limit_gb: 500\n',staged)
            for value in ['-1','1e3','nan','9000001','0.0001','1; echo unsafe']:
                result,staged,original=self.run_choice(script,mode='reconfigure',answers='5\n'+value+'\n',config=config)
                self.assertNotEqual(result.returncode,0,value)
                self.assertIsNone(staged)
                self.assertEqual(original,config)

    def test_linux_reinstall_uses_upgrade_instead_of_regenerating_identity(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp)
            config=root/'agent.yaml';config.write_text(OLD)
            library=root/'installer.sh';library.write_text((ROOT/'needle-agent.sh').read_text().split('# --- main ---')[0])
            commands=f"""
. {shlex.quote(str(library))}
AGENT_YAML={shlex.quote(str(config))}
require_root() {{ :; }}
require_tty_for_install() {{ echo 'unexpected installation' >&2; exit 1; }}
cmd_upgrade() {{ echo 'SAFE_UPGRADE'; }}
cmd_install
"""
            result=subprocess.run(['/bin/bash','-c',commands],text=True,capture_output=True)
            self.assertEqual(result.returncode,0,result.stderr)
            self.assertIn('SAFE_UPGRADE',result.stdout)
            self.assertEqual(config.read_text(),OLD)

    def test_linux_upgrade_applies_choice_and_keeps_backup(self):
        for option,answers,changed in [('--keep-config','',False),('','2\n5\n\n\n',True),('--reconfigure','2\n\n\n',True)]:
            with self.subTest(option=option),tempfile.TemporaryDirectory() as tmp:
                root=Path(tmp);config=root/'agent.yaml';config.write_text(OLD)
                bindir=root/'bin';bindir.mkdir()
                (bindir/'needle-agent').write_text('old binary')
                library=root/'installer.sh';library.write_text((ROOT/'needle-agent.sh').read_text().split('# --- main ---')[0])
                commands=f'''
. {shlex.quote(str(library))}
AGENT_YAML={shlex.quote(str(config))}
BIN_DIR={shlex.quote(str(bindir))}
TMP_DIR={shlex.quote(str(root/'download'))}
mkdir "$TMP_DIR"
require_root() {{ :; }}
require_linux_runtime() {{ :; }}
upgrade_interactive() {{ true; }}
tcping_prompt() {{ IFS= read -r TCPING_ANSWER; }}
download_release_agent() {{ VERSION=v9.9.9; printf 'new binary' > "$TMP_DIR/needle-agent"; }}
write_systemd_unit() {{ :; }}
systemctl() {{ :; }}
deploy_agent() {{
  if [ "$UPGRADE_RECONFIGURE" = true ]; then
    cp -p "$AGENT_YAML" "$AGENT_YAML.bak"
    chmod 600 "$AGENT_YAML.bak"
  fi
  cp "$TMP_DIR/agent.yaml" "$AGENT_YAML"
  chmod 640 "$AGENT_YAML"
  cp "$TMP_DIR/needle-agent" "$BIN_DIR/needle-agent"
}}
cmd_upgrade {option}
'''
                result=subprocess.run(['/bin/bash','-c',commands],input=answers+('\n\n' if changed else ''),text=True,capture_output=True)
                self.assertEqual(result.returncode,0,result.stderr)
                actual=config.read_text()
                for line in OLD.splitlines():
                    if not line.startswith(('tcpping:','  ')):self.assertIn(line+'\n',actual)
                if changed:
                    self.assertEqual(Path(str(config)+'.bak').read_text(),OLD)
                    self.assertEqual(config.stat().st_mode & 0o777,0o640)
                    self.assertNotIn('old.example',actual)
                else:self.assertEqual(actual,OLD)
                self.assertEqual((bindir/'needle-agent').read_text(),'new binary')

if __name__=='__main__':unittest.main()
