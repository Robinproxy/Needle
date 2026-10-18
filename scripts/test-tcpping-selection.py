#!/usr/bin/env python3
"""Exercise the same selection flow embedded in both standalone installers."""
from pathlib import Path
import shlex
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent
MARKER = '# TCP Ping selection is deliberately explicit; no connectivity auto-detection.'
END = '# End of TCP Ping selection.'

def selection(script):
    text = (ROOT / script).read_text()
    return text[text.index(MARKER):text.index(END) + len(END)]

class SelectionTests(unittest.TestCase):
    def run_mode(self, answer):
        outputs = []
        for script, shell in [('needle-agent.sh', '/bin/bash'), ('needle-agent-openwrt.sh', '/bin/sh')]:
            with tempfile.TemporaryDirectory() as tmp:
                source = selection(script)
                commands = f'''set -eu
TMP_DIR={shlex.quote(tmp)}
{source}
tcping_prompt() {{ IFS= read -r TCPING_ANSWER; }}
configure_tcping
cat "$TMP_DIR/tcpping.yaml"
'''
                result = subprocess.run([shell, '-c', commands], input=answer, text=True, capture_output=True)
                outputs.append(result)
        self.assertEqual(outputs[0].returncode, outputs[1].returncode)
        self.assertEqual(outputs[0].stdout, outputs[1].stdout)
        return outputs[0]

    def test_shared_flow_is_identical(self):
        self.assertEqual(selection('needle-agent.sh'), selection('needle-agent-openwrt.sh'))

    def test_modes(self):
        for answer, names in [('\n', ['CMv4','CUv4','CTv4']),('1\n',['CMv4','CUv4','CTv4']),('2\n',['CMv6','CUv6','CTv6']),('3\n',['CMv4','CMv6','CUv4','CUv6','CTv4','CTv6']),('5\n',[])]:
            with self.subTest(answer=answer):
                result = self.run_mode(answer)
                self.assertEqual(result.returncode, 0, result.stderr)
                actual = [line.split("'",2)[1] for line in result.stdout.splitlines() if line.startswith('  - name:')]
                self.assertEqual(actual,names)
                if not names: self.assertIn('tcpping: []',result.stdout)

    def test_custom_presets_and_ipv6_literal(self):
        result = self.run_mode("4\n1 4\n2\nMy VPS\nexample.com:443\nIPv6's route\n[2001:db8::1]:8080\n")
        self.assertEqual(result.returncode,0,result.stderr)
        for value in ["name: 'CMv4'","name: 'CUv6'","name: 'My VPS'","name: 'IPv6''s route'","target: '[2001:db8::1]:8080'"]:
            self.assertIn(value,result.stdout)
        self.assertEqual(result.stdout.count('  - name:'),4)

    def test_custom_empty_disables(self):
        result=self.run_mode('4\n\n0\n')
        self.assertEqual(result.returncode,0,result.stderr)
        self.assertIn('tcpping: []',result.stdout)

    def test_reject_invalid_and_duplicate_targets(self):
        cases=['9\n','4\n1 1\n','4\n7\n','4\n\n33\n','4\n1\n1\nCMv4\nexample.com:443\n','4\n\n1\nVPS\n::1:443\n','4\n\n1\nVPS\nexample.com:0\n','4\n\n1\nVPS\nexample.com:65536\n']
        for answer in cases:
            with self.subTest(answer=answer): self.assertNotEqual(self.run_mode(answer).returncode,0)

if __name__ == '__main__': unittest.main()
