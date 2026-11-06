#!/usr/bin/env python3
"""Exercise publication against disposable Git repositories and a fake registry."""
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import textwrap
import unittest

ROOT = Path(__file__).resolve().parent.parent
PROMOTE = ROOT / 'scripts/promote-docker-latest.sh'
IMAGE = 'ghcr.io/robinproxy/needle'
DIGEST_A = IMAGE + '@sha256:' + 'a' * 64
DIGEST_B = IMAGE + '@sha256:' + 'b' * 64


class PublicationTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.checkout = self.root / 'checkout'
        self.remote = self.root / 'remote.git'
        self.bin = self.root / 'bin'
        self.bin.mkdir()
        self.state = self.root / 'registry.json'
        self.state.write_text(json.dumps({IMAGE + ':latest': 'previous', IMAGE + ':v0.7.14': 'version-image'}))
        self.log = self.root / 'docker.jsonl'
        self.env = os.environ.copy()
        for key in ['GIT_DIR', 'GIT_WORK_TREE', 'GIT_INDEX_FILE']:
            self.env.pop(key, None)
        self.env.update(GIT_CONFIG_GLOBAL='/dev/null', GIT_CONFIG_NOSYSTEM='1',
                        GIT_TERMINAL_PROMPT='0', GITHUB_REF='refs/heads/main',
                        NEEDLE_IMAGE=IMAGE, TEST_REGISTRY=str(self.state),
                        TEST_DOCKER_LOG=str(self.log), TEST_DIGEST=DIGEST_A)
        self.git('init', '--bare', '--initial-branch=main', str(self.remote), cwd=self.root)
        self.git('init', '--initial-branch=main', str(self.checkout), cwd=self.root)
        self.git('remote', 'add', 'origin', str(self.remote))
        self.sha_a = self.advance('A')
        docker = self.bin / 'docker'
        docker.write_text('''#!/usr/bin/env python3
import json, os, pathlib, sys
args=sys.argv[1:]
state=pathlib.Path(os.environ['TEST_REGISTRY'])
registry=json.loads(state.read_text())
with open(os.environ['TEST_DOCKER_LOG'],'a') as log: log.write(json.dumps(args)+'\\n')
if args[:2]==['tag','needle-candidate'] and len(args)==3:
    pass
elif args[:1]==['push'] and len(args)==2:
    registry[args[1]]=os.environ['TEST_DIGEST']
elif args[:3]==['image','inspect','--format'] and len(args)==5:
    print(registry[args[4]])
elif args[:5]==['buildx','imagetools','create','--prefer-index=false','--tag'] and len(args)==7:
    registry[args[5]]=args[6]
else:
    raise SystemExit('unexpected Docker command: '+repr(args))
state.write_text(json.dumps(registry))
''')
        docker.chmod(0o755)
        self.env['PATH'] = str(self.bin) + os.pathsep + self.env['PATH']

    def git(self, *args, cwd=None):
        return subprocess.check_output(['git', *args], cwd=cwd or self.checkout,
                                       env=self.env, text=True, stderr=subprocess.DEVNULL).strip()

    def advance(self, message):
        self.git('-c', 'user.name=Needle CI test', '-c', 'user.email=ci@example.invalid',
                 '-c', 'commit.gpgsign=false', 'commit', '--allow-empty', '-m', message)
        self.git('push', 'origin', 'main')
        return self.git('rev-parse', 'HEAD')

    def promote(self, sha, image, ref='refs/heads/main'):
        return subprocess.run(['bash', str(PROMOTE), sha, image], cwd=self.checkout,
                              env=dict(self.env, GITHUB_REF=ref), text=True, capture_output=True)

    def registry(self):
        return json.loads(self.state.read_text())

    def calls(self):
        return self.log.read_text().splitlines() if self.log.exists() else []

    def test_current_main_promotes_only_the_tested_digest(self):
        result = self.promote(self.sha_a, DIGEST_A)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.registry()[IMAGE + ':latest'], DIGEST_A)
        self.assertEqual(self.registry()[IMAGE + ':v0.7.14'], 'version-image')
        self.assertEqual(len(self.calls()), 1)

    def test_late_build_and_old_rerun_cannot_roll_back_latest(self):
        sha_b = self.advance('B')
        result = self.promote(sha_b, DIGEST_B)
        self.assertEqual(result.returncode, 0, result.stderr)
        for _ in range(2):  # A completes late, then somebody reruns A.
            result = self.promote(self.sha_a, DIGEST_A)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn('no longer main', result.stdout)
        self.assertEqual(self.registry()[IMAGE + ':latest'], DIGEST_B)
        self.assertEqual(len(self.calls()), 1)

    def test_tag_run_never_promotes_even_at_current_main(self):
        result = self.promote(self.sha_a, DIGEST_A, 'refs/tags/v0.7.14')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.registry()[IMAGE + ':latest'], 'previous')
        self.assertEqual(self.calls(), [])

    def test_mutable_tags_and_wrong_repositories_are_rejected(self):
        for image in [IMAGE + ':ci-1-1', IMAGE + ':latest', DIGEST_A + '\n',
                      'ghcr.io/other/image@sha256:' + 'a' * 64, IMAGE + '@sha256:short']:
            with self.subTest(image=image):
                self.assertNotEqual(self.promote(self.sha_a, image).returncode, 0)
        self.assertNotEqual(self.promote('invalid-sha', DIGEST_A).returncode, 0)
        self.assertEqual(self.registry()[IMAGE + ':latest'], 'previous')
        self.assertEqual(self.calls(), [])

    def test_remote_failure_and_malformed_head_fail_closed(self):
        git = self.bin / 'git'
        for body in ['exit 128', "printf 'invalid refs/heads/main\\n'", "printf ''"]:
            with self.subTest(body=body):
                git.write_text('#!/bin/sh\n' + body + '\n')
                git.chmod(0o755)
                self.assertNotEqual(self.promote(self.sha_a, DIGEST_A).returncode, 0)
                self.assertEqual(self.registry()[IMAGE + ':latest'], 'previous')
                self.assertEqual(self.calls(), [])

    def test_workflow_pushes_only_its_candidate_or_version(self):
        workflow = (ROOT / '.github/workflows/docker.yml').read_text()
        block = re.search(r'        id: publish\n.*?        run: \|\n((?:          [^\n]*\n|\n)+)', workflow, re.S)
        self.assertIsNotNone(block, 'missing image-publication step')
        commands = textwrap.dedent(block.group(1))
        for ref_type, attempt, tag in [('tag', '1', 'v0.7.15'), ('branch', '1', 'ci-123-1'), ('branch', '2', 'ci-123-2')]:
            with self.subTest(ref_type=ref_type, attempt=attempt):
                output = self.root / 'output'
                output.write_text('')
                result = subprocess.run(['bash', '-euo', 'pipefail', '-c', commands], cwd=self.checkout,
                    env=dict(self.env, RELEASE_REF_TYPE=ref_type, RELEASE_REF_NAME='v0.7.15' if ref_type=='tag' else 'main',
                             GITHUB_RUN_ID='123', GITHUB_RUN_ATTEMPT=attempt, GITHUB_OUTPUT=str(output)),
                    text=True, capture_output=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(self.registry()[IMAGE + ':' + tag], DIGEST_A)
                self.assertEqual(output.read_text(), 'image_ref=' + DIGEST_A + '\n')
                self.assertEqual(self.registry()[IMAGE + ':latest'], 'previous')
                self.assertEqual(self.registry()[IMAGE + ':v0.7.14'], 'version-image')


if __name__ == '__main__':
    unittest.main()
