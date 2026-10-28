#!/usr/bin/env python3
"""Systemd lifecycle test, using a disposable host or an isolated test instance.

Build the Agent first and pass its absolute path. No network outside loopback,
no production token, and no downloads are used by this test.
Set NEEDLE_SYSTEMD_TEST_ISOLATED=1 to use a separate service/account/directory
on a host with an existing Agent; otherwise a disposable host is required.
"""
import grp
import http.server
import json
import os
from pathlib import Path
import pwd
import queue
import shlex
import subprocess
import sys
import tempfile
import threading
import time

REPO = Path(__file__).resolve().parent.parent
ISOLATED = os.environ.get('NEEDLE_SYSTEMD_TEST_ISOLATED') == '1'
SERVICE = 'needle-agent-security-test' if ISOLATED else 'needle-agent'
ACCOUNT = SERVICE
# /opt may be runner-writable on hosted CI. Use a root-managed parent for the
# disposable instance without changing the production installer's /opt default.
INSTALL = Path('/var/lib') / SERVICE
UNIT = Path('/etc/systemd/system') / f'{SERVICE}.service'
TOKEN = 'isolated-systemd-test-token'


def run(*args, **kwargs):
    return subprocess.run(args, check=True, text=True, capture_output=True, **kwargs)


def main():
    if os.geteuid() != 0 or (not ISOLATED and os.environ.get('NEEDLE_DISPOSABLE_SYSTEMD_TEST') != '1'):
        sys.exit('Requires root and an explicitly isolated instance or disposable systemd host.')
    if not Path('/run/systemd/system').is_dir():
        sys.exit('A running systemd instance is required.')
    try:
        pwd.getpwnam(ACCOUNT)
    except KeyError:
        pass
    else:
        sys.exit('Refusing to touch an existing test account.')
    try:
        grp.getgrnam(ACCOUNT)
    except KeyError:
        pass
    else:
        sys.exit('Refusing to touch an existing test group.')
    if INSTALL.exists() or INSTALL.is_symlink() or UNIT.exists() or UNIT.is_symlink():
        sys.exit('Refusing to touch an existing installation.')
    if subprocess.run(['systemctl', 'cat', SERVICE], capture_output=True).returncode == 0:
        sys.exit('Refusing to touch an existing systemd service.')
    binary = Path(sys.argv[1]).resolve(strict=True)
    reports = queue.Queue()

    class Receiver(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            self.send_response(200); self.end_headers()
            self.wfile.write(b'{"protocol":"persistent-report-v1"}')

        def do_POST(self):
            data = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
            reports.put((self.path, self.headers.get('Authorization'), data))
            self.send_response(200)
            self.end_headers()
            self.wfile.write(json.dumps({'protocol':'persistent-report-v1','report_id':data.get('report_id','')}).encode())

        def log_message(self, *args):
            pass

    receiver = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Receiver)
    threading.Thread(target=receiver.serve_forever, daemon=True).start()
    port = receiver.server_port
    with tempfile.TemporaryDirectory(prefix='needle-systemd-test-') as tmp:
        directory = Path(tmp)
        library = directory / 'installer.sh'
        library.write_text((REPO / 'scripts/needle-agent.sh').read_text().split('# --- main ---')[0])
        config = directory / 'agent.yaml'
        original = f'''hostname: isolated-systemd-test
server: http://127.0.0.1:{port}
token: {TOKEN}
interval: 2
realtime_interval: 1
interfaces: ['lo']
server_managed: true
tcpping:
  - name: loopback
    target: 127.0.0.1:{port}
'''
        config.write_text(original)
        config.chmod(0o600)
        q = shlex.quote

        def shell(body):
            settings = f'''
SERVICE_NAME={q(SERVICE)}
AGENT_USER={q(ACCOUNT)}
AGENT_GROUP={q(ACCOUNT)}
INSTALL_DIR={q(str(INSTALL))}
BIN_DIR="$INSTALL_DIR/bin"
AGENT_YAML="$INSTALL_DIR/agent.yaml"
SERVICE_FILE={q(str(UNIT))}
'''
            return run('bash', '-c', f'. {q(str(library))}\n{settings}\n{body}')

        def deploy(candidate=binary):
            return shell(f'''
require_linux_runtime
TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT
cp {q(str(candidate))} "$TMP_DIR/needle-agent"
cp {q(str(config))} "$TMP_DIR/agent.yaml"
deploy_agent
''')

        def check_report():
            deadline = time.monotonic() + 15
            realtime_received = False
            while time.monotonic() < deadline:
                path, auth, report = reports.get(timeout=15)
                assert auth == 'Bearer ' + TOKEN
                assert report['hostname'] == 'isolated-systemd-test'
                if path == '/api/realtime':
                    assert 'tcpping' not in report and 'billing_period' not in report
                    realtime_received = True
                    continue
                if report['cpu'] is None or report['network'] is None:
                    continue  # Startup establishes rate windows rather than inventing a full sample.
                for name in ['cpu', 'memory', 'disk', 'network', 'load']:
                    assert report[name] is not None, (name, report)
                assert report['cpu']['cores'] > 0
                assert report['memory']['total'] > 0
                assert report['memory']['swap_total'] >= report['memory']['swap_used'] >= 0
                assert report['disk']['total'] > 0
                assert report['uptime'] > 0
                assert report['tcpping'][0]['success']
                if realtime_received and report['network']['total_sent'] > 0 and report['network']['up'] > 0:
                    return
            raise AssertionError('No report with nonzero loopback counters/rate')

        def check_identity_and_permissions():
            pid = int(run('systemctl', 'show', '-p', 'MainPID', '--value', SERVICE).stdout)
            status = dict(line.split(':', 1) for line in Path(f'/proc/{pid}/status').read_text().splitlines() if ':' in line)
            uid = pwd.getpwnam(ACCOUNT).pw_uid
            assert uid > 0 and all(int(n) == uid for n in status['Uid'].split())
            for key in ['CapEff', 'CapPrm', 'CapBnd', 'CapAmb']:
                assert int(status[key].strip(), 16) == 0, (key, status[key])
            assert status['NoNewPrivs'].strip() == '1'
            gid = pwd.getpwnam(ACCOUNT).pw_gid
            for path, owner, group, mode in [
                (INSTALL, 0, gid, 0o750), (INSTALL / 'bin', 0, 0, 0o755),
                (INSTALL / 'bin/needle-agent', 0, 0, 0o755),
                (INSTALL / 'agent.yaml', 0, gid, 0o640), (UNIT, 0, 0, 0o644),
            ]:
                st = path.stat()
                assert (st.st_uid, st.st_gid, st.st_mode & 0o7777) == (owner, group, mode), path
            assert (INSTALL / 'agent.yaml').read_text() == original
            # Exercise actual DAC denial, without truncating any file.
            probe = '''import os,sys
for path in sys.argv[1:]:
 try:
  fd=os.open(path, os.O_WRONLY)
 except PermissionError:
  continue
 else:
  os.close(fd)
  raise SystemExit("unexpected write permission: "+path)
'''
            run('runuser', '-u', ACCOUNT, '--', 'python3', '-c', probe,
                str(INSTALL / 'bin/needle-agent'), str(INSTALL / 'agent.yaml'), str(UNIT))
            run('runuser', '-u', ACCOUNT, '--', 'test', '!', '-w', str(INSTALL))
            run('runuser', '-u', 'nobody', '--', 'test', '!', '-r', str(INSTALL / 'agent.yaml'))
            return pid

        try:
            deploy()
            check_report()
            first_pid = check_identity_and_permissions()
            run('systemctl', 'restart', SERVICE)
            check_report()
            assert check_identity_and_permissions() != first_pid

            # Run collector tests as the dedicated account too, including /proc
            # permission failures. The optional second argument is a Go test binary.
            if len(sys.argv) > 2:
                test_binary = INSTALL / 'bin/collector.test'
                run('install', '-o', 'root', '-g', 'root', '-m', '755', str(Path(sys.argv[2]).resolve()), str(test_binary))
                result = run('runuser', '-u', ACCOUNT, '--', 'env', 'NEEDLE_REQUIRE_NONROOT=1', str(test_binary), '-test.v')
                print(result.stdout)
                test_binary.unlink()

            # Simulate a legacy root service with root-only config, then upgrade
            # through the real command path, keeping the exact original token.
            run('systemctl', 'stop', SERVICE)
            UNIT.write_text(UNIT.read_text().replace(f'User={ACCOUNT}', 'User=root').replace(f'Group={ACCOUNT}', 'Group=root'))
            os.chown(INSTALL / 'agent.yaml', 0, 0)
            os.chmod(INSTALL / 'agent.yaml', 0o600)
            run('systemctl', 'daemon-reload')
            run('systemctl', 'start', SERVICE)
            shell(f'''
download_release_agent() {{
  VERSION=local-test
  TMP_DIR=$(mktemp -d)
  trap 'rm -rf "$TMP_DIR"' EXIT
  cp {q(str(binary))} "$TMP_DIR/needle-agent"
}}
cmd_upgrade --keep-config
''')
            check_identity_and_permissions()
            check_report()

            # A candidate that passes config validation but fails ExecStart must
            # restore the old working installation and leave the token intact.
            failing = directory / 'failing-agent'
            failing.write_text('#!/bin/sh\n[ "$1" = -check-config ] && exit 0\nexit 1\n')
            failing.chmod(0o755)
            try:
                deploy(failing)
            except subprocess.CalledProcessError:
                pass
            else:
                raise AssertionError('Broken candidate was accepted')
            time.sleep(1)
            check_identity_and_permissions()
            assert (INSTALL / 'bin/needle-agent').read_bytes() == binary.read_bytes()
            check_report()
        finally:
            if sys.exc_info()[0] is not None:
                diagnostics = subprocess.run(['journalctl', '-u', SERVICE, '-n', '80', '--no-pager'], text=True, capture_output=True)
                print(diagnostics.stdout, file=sys.stderr)
            receiver.shutdown()
            try:
                shell('cmd_uninstall --yes')
            except subprocess.CalledProcessError as exc:
                print(exc.stdout, exc.stderr, file=sys.stderr)
                raise
        assert not INSTALL.exists() and not UNIT.exists()
        assert subprocess.run(['getent', 'passwd', ACCOUNT], capture_output=True).returncode != 0
        assert subprocess.run(['getent', 'group', ACCOUNT], capture_output=True).returncode != 0
        Path(f'/run/{SERVICE}-install.lock').unlink(missing_ok=True)
    print('PASS: non-root startup, collection, reporting, restart, root migration, rollback and uninstall')


if __name__ == '__main__':
    try:
        main()
    except subprocess.CalledProcessError as exc:
        print(exc.stdout, exc.stderr, file=sys.stderr)
        raise
