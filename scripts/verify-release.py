#!/usr/bin/env python3
"""Verify actual archives, version/commit, offline config and served frontend.
Linux arm64 binaries may run through qemu-aarch64 (-L sysroot for CGO Server).
"""
import argparse, hashlib, json, pathlib, shlex, socket, struct, subprocess, tarfile, tempfile, time, urllib.error, urllib.request
p=argparse.ArgumentParser();p.add_argument('archive',type=pathlib.Path);p.add_argument('--version',required=True);p.add_argument('--commit',required=True);p.add_argument('--server-runner',default='');p.add_argument('--agent-runner',default='');a=p.parse_args()
sha=lambda data:hashlib.sha256(data).hexdigest()
expected=(pathlib.Path(str(a.archive)+'.sha256')).read_text().split()[0]
assert sha(a.archive.read_bytes())==expected,'archive checksum mismatch'
with tempfile.TemporaryDirectory(prefix='needle-artifact-') as temp:
    folder=pathlib.Path(temp)
    with tarfile.open(a.archive,'r:gz') as tf:
        assert len(tf.getnames())==len(set(tf.getnames())),'duplicate archive entries'
        for info in tf.getmembers():
            assert info.isfile() and '/' not in info.name and '\\' not in info.name and info.name not in ('.','..'),'unsafe archive entry'
        tf.extractall(folder,filter='data')
    manifest=json.loads((folder/'build-manifest.json').read_text())
    assert manifest['version']==a.version and manifest['commit']==a.commit
    agent_only=a.archive.name.startswith('needle-agent-')
    required={'needle-agent','needle-agent-openwrt.sh','agent.yaml.example','release-notes.md','build-manifest.json'}
    if not agent_only:required.update({'needle-server','needle-server.sh','needle-agent.sh'})
    assert all((folder/name).is_file() for name in required),'required packaged component missing'
    assert manifest['installer_protocol']=='needle-install-v2' and manifest['report_protocol']=='persistent-report-v1'
    for name,digest in manifest['files'].items():
        file=folder/name
        if file.exists():assert sha(file.read_bytes())==digest,name
    for name in ['needle-agent']+(['needle-server'] if (folder/'needle-server').exists() else []):
        data=(folder/name).read_bytes()
        if manifest['os']=='linux':
            assert data[:4]==b'\x7fELF' and data[4]==2
            assert struct.unpack('<H',data[18:20])[0]=={'amd64':62,'arm64':183}[manifest['arch']]
            if name=='needle-agent':
                offset=struct.unpack('<Q',data[32:40])[0];size,count=struct.unpack('<HH',data[54:58])
                assert all(struct.unpack('<I',data[offset+i*size:offset+i*size+4])[0]!=3 for i in range(count)),'Agent must not depend on an ELF interpreter'
        runner=shlex.split(a.agent_runner if name=='needle-agent' else a.server_runner)
        command=runner+[str(folder/name)]
        output=subprocess.check_output(command+['-version'],text=True)
        assert output.strip()==f'{name} {a.version} commit={a.commit}',output
        assert subprocess.check_output(command+['-installer-protocol'],text=True).startswith('needle-install-v2 ')
    config=folder/'verify.yaml';config.write_text('hostname: artifact-check\nserver: http://127.0.0.1:8008\ntoken: artifact-test-only\nrealtime_interval: 5\ninterval: 30\nhistory_queue_capacity: 64\nhistory_retry_max_age_seconds: 1800\nserver_managed: true\ntcpping: []\n')
    subprocess.run(shlex.split(a.agent_runner)+[str(folder/'needle-agent'),'-check-config',str(config)],check=True)
    if (folder/'needle-server').exists():
        with socket.socket() as sock:sock.bind(('127.0.0.1',0));port=sock.getsockname()[1]
        proc=subprocess.Popen(shlex.split(a.server_runner)+[str(folder/'needle-server'),'-l',f'127.0.0.1:{port}','-db',str(folder/'check.db')],stdout=subprocess.DEVNULL,stderr=subprocess.PIPE,text=True)
        try:
            for _ in range(100):
                if proc.poll() is not None:raise RuntimeError(proc.stderr.read())
                try:
                    with urllib.request.urlopen(f'http://127.0.0.1:{port}/app.js',timeout=2) as response:script=response.read()
                    assert sha(script)==manifest['app_js_sha256'],'embedded frontend differs from manifest/source';break
                except (OSError,urllib.error.URLError):time.sleep(.1)
            else:raise RuntimeError('packaged Server startup timeout')
        finally:
            proc.terminate()
            try:proc.wait(timeout=10)
            except subprocess.TimeoutExpired:proc.kill();proc.wait()
    print('PASS archive, checksums, platform, binary version/commit, protocol, offline configuration'+(' and embedded frontend/startup' if (folder/'needle-server').exists() else ''))
