#!/usr/bin/env python3
"""Both installers: opt-in upgrades, independent intervals and byte-safe identity."""
from pathlib import Path
import shlex
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent
OLD = """hostname: router
token: 'keep:# secret'
server: https://needle.example.com
interval: 60
interfaces: [eth1]
tcpping: []
billing_currency: CNY
billing_amount: '38'
billing_period: 1m
expires_at: '2026-11-01'
monthly_traffic_limit_gb: 0
"""


class IntervalTests(unittest.TestCase):
    def run_flow(self, script, answers='', mode='intervals', config=OLD):
        text = (ROOT / script).read_text()
        start = text.index('# TCP Ping selection is deliberately explicit;')
        end = text.index('# End of TCP Ping selection.')
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            original = root / 'original.yaml'
            original.write_text(config)
            command = f'''set -eu
TMP_DIR={shlex.quote(tmp)}
{text[start:end]}
tcping_prompt() {{ IFS= read -r TCPING_ANSWER; }}
upgrade_interactive() {{ true; }}
UPGRADE_CONFIG_MODE={shlex.quote(mode)}
'''
            command += ('configure_intervals install\n' if mode == 'install' else f'choose_upgrade_config {shlex.quote(str(original))}\n')
            shell = '/bin/bash' if script == 'needle-agent.sh' else '/bin/sh'
            result = subprocess.run([shell, '-c', command], input=answers, text=True, capture_output=True)
            staged = root / ('intervals.yaml' if mode == 'install' else 'agent.yaml')
            self.assertEqual(original.read_text(), config)
            return result, staged.read_text() if staged.exists() else None

    def test_new_defaults_and_independent_combinations(self):
        for script in ['needle-agent.sh', 'needle-agent-openwrt.sh']:
            result, config = self.run_flow(script, '\n\n', mode='install')
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(config, 'realtime_interval: 5\ninterval: 30\n')
            for realtime, history in [(5,30),(10,30),(5,60),(7,30),(0,30)]:
                result, config = self.run_flow(script, f'{realtime}\n{history}\n', mode='install')
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(config, f'realtime_interval: {realtime}\ninterval: {history}\n')

    def test_legacy_upgrade_keep_does_not_enable_realtime(self):
        for script in ['needle-agent.sh', 'needle-agent-openwrt.sh']:
            result, staged = self.run_flow(script, mode='keep')
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIsNone(staged)
            result, staged = self.run_flow(script, '\n\n')
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn('realtime_interval: 0\n', staged)
            self.assertIn('interval: 60\n', staged)

    def test_interval_only_preserves_all_other_fields(self):
        for script in ['needle-agent.sh', 'needle-agent-openwrt.sh']:
            for config in [OLD, OLD + 'realtime_interval: 7\n']:
                result, staged = self.run_flow(script, '5\n30\n', config=config)
                self.assertEqual(result.returncode, 0, result.stderr)
                for line in config.splitlines():
                    if not line.startswith(('interval:', 'realtime_interval:')):
                        self.assertIn(line + '\n', staged)
                self.assertEqual(staged.count('\ninterval:'), 1)
                self.assertEqual(staged.count('realtime_interval:'), 1)

    def test_existing_interval_values_are_defaults(self):
        for script in ['needle-agent.sh', 'needle-agent-openwrt.sh']:
            result, staged = self.run_flow(script, '\n\n', config=OLD + 'realtime_interval: 7\n')
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn('realtime_interval: 7\ninterval: 60\n', staged)

    def test_invalid_settings_preserve_original(self):
        for script in ['needle-agent.sh', 'needle-agent-openwrt.sh']:
            for answers in ['-1\n30\n','1.5\n30\n','31\n30\n','5\n0\n','0\n86401\n','1e1\n30\n','999999999999999999999999\n30\n']:
                result, staged = self.run_flow(script, answers)
                self.assertNotEqual(result.returncode, 0)
                self.assertIsNone(staged)


if __name__ == '__main__':
    unittest.main()
