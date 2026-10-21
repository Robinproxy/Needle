#!/usr/bin/env python3
"""Package already-built binaries, source-matched installers and a manifest."""
import argparse, hashlib, json, pathlib, re, subprocess, tarfile
p=argparse.ArgumentParser();p.add_argument('--arch',choices=['amd64','arm64'],required=True);p.add_argument('--os',default='linux',choices=['linux','darwin']);p.add_argument('--version',required=True);p.add_argument('--commit',required=True);p.add_argument('--binaries',type=pathlib.Path,required=True);p.add_argument('--output',type=pathlib.Path,required=True);a=p.parse_args()
if not re.fullmatch(r'[A-Za-z0-9._-]+',a.version) or not re.fullmatch(r'[a-f0-9]{40}',a.commit):raise SystemExit('Invalid version/commit')
root=pathlib.Path(__file__).resolve().parent.parent;dest=a.output;dest.mkdir(parents=True,exist_ok=True)
files={name:a.binaries/name for name in ['needle-server','needle-agent']}
files.update({name:root/'scripts'/name for name in ['needle-server.sh','needle-agent.sh','needle-agent-openwrt.sh']})
files['agent.yaml.example']=root/'agent.yaml.example';files['release-notes.md']=root/'docs/next-release.md'
sha=lambda path:hashlib.sha256(path.read_bytes()).hexdigest()
source_paths=sorted([*root.joinpath('cmd').rglob('*.go'),*root.joinpath('internal').rglob('*.go'),*root.joinpath('internal/server/static').rglob('*'),*root.joinpath('scripts').glob('*.sh'),*root.joinpath('scripts').glob('*.py'),*root.joinpath('.github/workflows').glob('*.yml'),root/'agent.yaml.example',root/'Dockerfile',root/'go.mod',root/'go.sum',root/'docs/next-release.md'])
source_hash=hashlib.sha256()
for file in source_paths:
    if file.is_file():source_hash.update(str(file.relative_to(root)).encode()+b'\0'+file.read_bytes())
dirty=bool(subprocess.check_output(['git','status','--porcelain','--untracked-files=all','--',*[str(file.relative_to(root)) for file in source_paths if file.is_file()]],cwd=root))
manifest={'source_tree_sha256':source_hash.hexdigest(),'source_dirty':dirty,'version':a.version,'commit':a.commit,'os':a.os,'arch':a.arch,'installer_protocol':'needle-install-v2','report_protocol':'persistent-report-v1','files':{name:sha(path) for name,path in files.items()},'app_js_sha256':sha(root/'internal/server/static/app.js')}
manifest_path=dest/f'manifest-{a.arch}.json';manifest_path.write_text(json.dumps(manifest,indent=2)+'\n')
archives=[]
for prefix,names in [('needle',['needle-server','needle-agent','needle-server.sh','needle-agent.sh','needle-agent-openwrt.sh','agent.yaml.example','release-notes.md']),('needle-agent',['needle-agent','needle-agent-openwrt.sh','agent.yaml.example','release-notes.md'])]:
    archive=dest/f'{prefix}-{a.os}-{a.arch}.tar.gz'
    with tarfile.open(archive,'w:gz') as tf:
        for name in names:tf.add(files[name],arcname=name,recursive=False)
        tf.add(manifest_path,arcname='build-manifest.json',recursive=False)
    (dest/(archive.name+'.sha256')).write_text(sha(archive)+'  '+archive.name+'\n');archives.append(archive)
(dest/f'checksums-{a.arch}.txt').write_text(''.join(sha(file)+'  '+file.name+'\n' for file in archives))
manifest_path.unlink()
print('Packaged '+', '.join(file.name for file in archives))
