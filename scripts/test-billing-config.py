#!/usr/bin/env python3
"""Exercise billing prompts and safe upgrade edits in both standalone installers."""
import importlib.util
from pathlib import Path
import shlex
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent
sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location('upgrade_checks', ROOT / 'test-agent-upgrade-config.py')
upgrade = importlib.util.module_from_spec(spec)
spec.loader.exec_module(upgrade)


class BillingConfigTests(unittest.TestCase):
    def run_billing(self, script, answers):
        with tempfile.TemporaryDirectory() as tmp:
            commands = f'''set -eu
TMP_DIR={shlex.quote(tmp)}
{upgrade.UpgradeConfigTests().common(script)}
tcping_prompt() {{ IFS= read -r TCPING_ANSWER; }}
configure_billing
cat "$TMP_DIR/billing.yaml"
'''
            shell = '/bin/bash' if script == 'needle-agent.sh' else '/bin/sh'
            return subprocess.run([shell, '-c', commands], input=answers, text=True, capture_output=True)

    def test_periods_and_actual_transferred_renewal_date(self):
        for script in ['needle-agent.sh', 'needle-agent-openwrt.sh']:
            for period in ['1m', '3m', '6m', '12m', '24m', '36m']:
                with self.subTest(script=script, period=period):
                    result = self.run_billing(script, f'cad\n90.10\n{period}\n2028-02-29\n')
                    self.assertEqual(result.returncode, 0, result.stderr)
                    for line in ["billing_currency: 'CAD'", "billing_amount: '90.10'", f"billing_period: '{period}'", "expires_at: '2028-02-29'"]:
                        self.assertIn(line, result.stdout)

    def test_free_unset_and_permanent_are_independent(self):
        for script in ['needle-agent.sh', 'needle-agent-openwrt.sh']:
            for amount in ['', '0', '99.00']:
                result = self.run_billing(script, f'\n{amount}\nforever\n')
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn(f"billing_amount: '{amount}'", result.stdout)
                self.assertIn("expires_at: ''", result.stdout)
            result = self.run_billing(script, '\n\nskip\n')
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("billing_period: ''", result.stdout)

    def test_invalid_currency_amount_period_and_dates(self):
        for script in ['needle-agent.sh', 'needle-agent-openwrt.sh']:
            answers = ['$\n', 'USDD\n', 'USD\n-1\n', 'USD\n1e3\n', 'USD\n1.0000001\n', 'USD\n1000000000000\n', 'USD\n1; echo unsafe\n', 'USD\n5\nskip\n', 'USD\n5\n2m\n']
            answers += [f'USD\n5\n24m\n{date}\n' for date in ['2026-02-29', '2100-02-29', '2028-04-31', '2028-13-01', '2028-00-01', '2028-01-00', '2028-2-01', '']]
            for answer in answers:
                with self.subTest(script=script, answer=answer):
                    result = self.run_billing(script, answer)
                    self.assertNotEqual(result.returncode, 0)
                    self.assertNotIn('billing_currency:', result.stdout)

    def test_reconfigure_preserves_identity_and_replaces_only_billing_fields(self):
        config = upgrade.OLD + "billing_currency: USD\nbilling_amount: '5'\nmonthly_traffic_limit_gb: 1000\n"
        for script in ['needle-agent.sh', 'needle-agent-openwrt.sh']:
            result, staged, original = upgrade.UpgradeConfigTests().run_choice(script, mode='reconfigure', config=config, answers='5\n\ny\nCNY\n80.00\n36m\n2029-02-28\n')
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(original, config)
            for line in ["token: 'token:# with ''quotes'' and spaces'", "hostname: 'my-router'", "interfaces: ['pppoe-wan']", 'monthly_traffic_limit_gb: 1000']:
                self.assertIn(line, staged)
            for key in ['billing_currency', 'billing_amount', 'billing_period', 'expires_at']:
                self.assertEqual(staged.count(key + ':'), 1)
            self.assertIn("billing_currency: 'CNY'", staged)
            self.assertIn("billing_period: '36m'", staged)
            self.assertIn("expires_at: '2029-02-28'", staged)
            # Default N must preserve even billing values unknown to this installer.
            result, staged, _ = upgrade.UpgradeConfigTests().run_choice(script, mode='reconfigure', config=config, answers='5\n\n\n')
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("billing_amount: '5'", staged)
            self.assertIn('billing_currency: USD', staged)

    def test_quoted_keys_multiline_values_and_unsafe_layout(self):
        answers = '5\n\ny\nUSD\n0\nforever\n'
        for script in ['needle-agent.sh', 'needle-agent-openwrt.sh']:
            config = "token: |-\n  keep:exact\n\"billing_currency\": 'CAD'\n'billing_amount': >-\n  90.00\nbilling_period: 24m\nexpires_at: '2028-10-15'\n"
            result, staged, _ = upgrade.UpgradeConfigTests().run_choice(script, mode='reconfigure', config=config, answers=answers)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn('token: |-\n  keep:exact\n', staged)
            self.assertNotIn('90.00', staged)
            self.assertIn("billing_amount: '0'", staged)
            for bad in ["token: &keep 90\nbilling_amount: *keep\n", "token: keep\nbilling_amount: 5\nbilling_amount: 6\n"]:
                result, staged, original = upgrade.UpgradeConfigTests().run_choice(script, mode='reconfigure', config=bad, answers=answers)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(original, bad)
                self.assertIsNone(staged)


if __name__ == '__main__':
    unittest.main()
