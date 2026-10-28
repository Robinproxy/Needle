#!/usr/bin/env python3
"""Disposable CI only. Never point this at an existing production installation."""
import json, os, pathlib, shutil, subprocess, sys, tempfile, time, urllib.request
import pty, select
if os.environ.get('NEEDLE_DISPOSABLE_SYSTEMD_TEST') != '1' or os.geteuid() != 0:
    raise SystemExit('Run as root only in a disposable systemd CI runner with NEEDLE_DISPOSABLE_SYSTEMD_TEST=1')
binary=pathlib.Path(sys.argv[1]).resolve()
script=pathlib.Path(__file__).with_name('needle-server.sh').resolve()
# Hosted runners can make /opt writable by the runner account. Keep the
# disposable instance under a root-managed parent; never relax installer checks
# or change permissions on a shared runner directory to accommodate this test.
base=pathlib.Path('/var/lib/needle-server-security-test'); name='needle-server-security-test'
unit=pathlib.Path('/etc/systemd/system/'+name+'.service'); db=base/'data/needle.db'
if base.exists() or unit.exists() or subprocess.run(['getent','passwd',name],capture_output=True).returncode==0:
    raise SystemExit('Refusing to overwrite an existing test namespace')
def run(*args,**kwargs): return subprocess.run(args,check=True,**kwargs)
def cli(*args): return run(str(base/'bin/needle-server'),'-db',str(db),*args,capture_output=True,text=True)
def admin_setup(command):
    master, slave = pty.openpty()
    process = subprocess.Popen([str(base/'bin/needle-server'), '-db', str(db), command], stdin=slave, stdout=slave, stderr=slave)
    os.close(slave)
    output = b''
    def expect(marker):
        nonlocal output
        deadline = time.monotonic() + 20
        while marker not in output:
            if time.monotonic() >= deadline:
                raise AssertionError('Admin terminal setup timed out')
            if select.select([master], [], [], 1)[0]:
                output += os.read(master, 65536)
    try:
        for marker, value in [(b'Administrator username', 'ci-admin'), (b'Public origin', 'https://needle-ci.invalid'), (b'Password (at least', 'isolated-admin-password-2026'), (b'Confirm password', 'isolated-admin-password-2026')]:
            expect(marker)
            os.write(master, value.encode() + b'\n')
        expect(b'Administrator saved.')
        assert process.wait(timeout=10) == 0
        assert b'isolated-admin-password-2026' not in output, 'Password echoed to terminal'
    finally:
        os.close(master)
        if process.poll() is None:
            process.kill(); process.wait()
def http(path,payload=None,token=None):
    data=None if payload is None else json.dumps(payload).encode()
    req=urllib.request.Request('http://127.0.0.1:18928'+path,data=data,headers={'Content-Type':'application/json',**({'Authorization':'Bearer '+token} if token else {})})
    with urllib.request.urlopen(req,timeout=5) as r:return json.load(r)
def wait_healthy():
    for _ in range(50):
        try:
            if http('/api/health')['status']=='ok':return
        except OSError:pass
        time.sleep(.2)
    raise AssertionError('Server did not become healthy')
try:
    (base/'bin').mkdir(parents=True);db.parent.mkdir()
    shutil.copy2(binary,base/'bin/needle-server');os.chmod(base/'bin/needle-server',0o755)
    (base/'.env').write_text('NEEDLE_LISTEN=127.0.0.1:18928\n');os.chmod(base/'.env',0o600)
    cli('allow-token','ci-server-only')
    unit.write_text(f'[Service]\nExecStart={base}/bin/needle-server -l ${{NEEDLE_LISTEN}} -db {db}\nEnvironmentFile={base}/.env\n[Install]\nWantedBy=multi-user.target\n')
    run('systemctl','daemon-reload');run('systemctl','enable','--now',name)
    wait_healthy()
    http('/api/report',{'hostname':'CI Demo','billing_period':'forever','created_at':int(time.time()),'uptime':100,'network':{'total_sent':100,'total_recv':200}},'ci-server-only')
    with tempfile.TemporaryDirectory() as temp:
        shutil.copy2(binary,pathlib.Path(temp)/'needle-server')
        code=f'''source "{script}"
INSTALL_DIR={base}; BIN_DIR=$INSTALL_DIR/bin; DATA_DIR=$INSTALL_DIR/data
ENV_FILE=$INSTALL_DIR/.env; SERVICE_NAME={name}; SERVER_USER={name}
SERVICE_FILE={unit}; SERVER_BIN=$BIN_DIR/needle-server; DB_PATH=$DATA_DIR/needle.db
TMP_DIR={temp}; VERSION=ci
# Namespace is isolated; do not download or modify production.
download_release_server() {{ return 0; }}
cmd_upgrade
'''
        run('bash','-c',code)
    uid=int(subprocess.check_output(['id','-u',name]))
    assert uid!=0 and db.stat().st_uid==uid
    assert (base/'bin/needle-server').stat().st_uid==0
    assert (base/'.env').read_text()=='NEEDLE_LISTEN=127.0.0.1:18928\n'
    for operation in [('list-agents',),('list-tokens',),('allow-token','ci-extra'),('-y','revoke-token','ci-extra')]:
        cli(*operation)
        # This checks CLI/DAC compatibility, not burst admission. Respect the
        # production per-token limit instead of disabling it for the fixture.
        time.sleep(1.05)
        http('/api/report',{'hostname':'CI Demo','created_at':int(time.time()),'uptime':101},'ci-server-only')
        for p in db.parent.iterdir(): assert p.stat().st_uid==uid and p.stat().st_mode&0o077==0,p
    # Exercise hidden password input after root CLI switches to the DB owner.
    admin_setup('admin-init')
    request = urllib.request.Request('http://127.0.0.1:18928/api/admin/login', data=json.dumps({'username':'ci-admin','password':'isolated-admin-password-2026'}).encode(), headers={'Content-Type':'application/json','Origin':'https://needle-ci.invalid','X-Needle-Admin':'1'})
    with urllib.request.urlopen(request, timeout=5) as response:
        cookie = response.headers['Set-Cookie'].split(';', 1)[0]
        assert json.load(response)['csrf_token']
    request = urllib.request.Request('http://127.0.0.1:18928/api/admin/nodes', headers={'Cookie':cookie})
    with urllib.request.urlopen(request, timeout=5) as response:
        assert json.load(response)['nodes'][0]['hostname'] == 'CI Demo'
    admin_setup('admin-reset')
    try:
        urllib.request.urlopen(request, timeout=5)
        raise AssertionError('CLI reset did not invalidate an existing session')
    except urllib.error.HTTPError as error:
        assert error.code == 401
    for p in db.parent.iterdir(): assert p.stat().st_uid==uid and p.stat().st_mode&0o077==0,p
    # Root CLI must neither inherit group 0 from a bad DB nor race installer
    # ownership migration. Both failures happen before opening SQLite.
    gid=int(subprocess.check_output(['id','-g',name]))
    os.chown(db,uid,0)
    rejected=subprocess.run([str(base/'bin/needle-server'),'-db',str(db),'list-agents'],capture_output=True,text=True)
    assert rejected.returncode!=0 and 'primary group' in rejected.stderr
    os.chown(db,uid,gid)
    import fcntl, hashlib
    lock_path=pathlib.Path('/run/needle-server-db-'+hashlib.sha256(str(db).encode()).hexdigest()+'.lock')
    with lock_path.open('a') as lock:
        fcntl.flock(lock,fcntl.LOCK_EX)
        rejected=subprocess.run([str(base/'bin/needle-server'),'-db',str(db),'list-agents'],capture_output=True,text=True)
        assert rejected.returncode!=0 and 'installer/administrator' in rejected.stderr
    for _ in range(2):
        run('systemctl','restart',name)
        wait_healthy()
    # Normal uninstall retains data, token and the non-login owner. Reinstall
    # from the saved unit must preserve listen/custom arguments and permissions.
    with tempfile.TemporaryDirectory() as temp:
        shutil.copy2(binary,pathlib.Path(temp)/'needle-server')
        prefix=f'''source "{script}"
INSTALL_DIR={base}; BIN_DIR=$INSTALL_DIR/bin; DATA_DIR=$INSTALL_DIR/data
ENV_FILE=$INSTALL_DIR/.env; SERVICE_NAME={name}; SERVER_USER={name}
SERVICE_FILE={unit}; SERVER_BIN=$BIN_DIR/needle-server; DB_PATH=$DATA_DIR/needle.db
TMP_DIR={temp}; VERSION=ci
download_release_server() {{ return 0; }}
'''
        run('bash','-c',prefix+'cmd_uninstall --yes')
        assert db.exists() and not unit.exists() and not (base/'bin/needle-server').exists()
        assert subprocess.run(['getent','passwd',name],capture_output=True).returncode==0
        run('bash','-c',prefix+'cmd_upgrade')
        run('systemctl','start',name)
        wait_healthy()
        cli('-y','delete-agent','CI Demo')
        assert http('/api/agents')==[]
        run('bash','-c',prefix+'cmd_uninstall --purge --yes')
        assert not base.exists() and not unit.exists()
        assert subprocess.run(['getent','passwd',name],capture_output=True).returncode!=0
    with tempfile.TemporaryDirectory() as temp:
        shutil.copy2(binary,pathlib.Path(temp)/'needle-server')
        prefix=f'''source "{script}"
INSTALL_DIR={base}; BIN_DIR=$INSTALL_DIR/bin; DATA_DIR=$INSTALL_DIR/data
ENV_FILE=$INSTALL_DIR/.env; SERVICE_NAME={name}; SERVER_USER={name}
SERVICE_FILE={unit}; SERVER_BIN=$BIN_DIR/needle-server; DB_PATH=$DATA_DIR/needle.db
TMP_DIR={temp}; VERSION=ci
download_release_server() {{ return 0; }}
require_tty_for_install() {{ return 0; }}
read_prompt() {{ printf -v "$1" '%s' '127.0.0.1:18928'; }}
'''
        run('bash','-c',prefix+'cmd_install')
        assert (base/'.env').read_text()=='NEEDLE_LISTEN=127.0.0.1:18928\n'
        assert subprocess.check_output(['systemctl','is-enabled',name],text=True).strip()=='enabled'
        wait_healthy()
        run('bash','-c',prefix+'cmd_uninstall --purge --yes')
        assert not base.exists()
    print('PASS: root migration, non-root startup/restart, consistent backup, CLI followed by writes, DB/WAL/SHM permissions, retained uninstall/reinstall, explicit node deletion and purge')
except Exception:
    subprocess.run(['journalctl','-u',name,'-n','80','--no-pager'],check=False)
    raise
finally:
    subprocess.run(['systemctl','disable','--now',name],capture_output=True)
    unit.unlink(missing_ok=True);subprocess.run(['systemctl','daemon-reload'],capture_output=True)
    shutil.rmtree(base,ignore_errors=True)
    subprocess.run(['userdel',name],capture_output=True);subprocess.run(['groupdel',name],capture_output=True)
    pathlib.Path('/run/'+name+'-install.lock').unlink(missing_ok=True)
    import hashlib
    pathlib.Path('/run/needle-server-db-'+hashlib.sha256(str(db).encode()).hexdigest()+'.lock').unlink(missing_ok=True)
