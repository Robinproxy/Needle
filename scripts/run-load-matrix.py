#!/usr/bin/env python3
"""Sequential, repeatable matrix; all targets/DBs are disposable loopback children."""
import argparse, hashlib, json, pathlib, subprocess
p=argparse.ArgumentParser();p.add_argument('binary');p.add_argument('output');p.add_argument('--duration',default='35s');p.add_argument('--stability',default='180s');a=p.parse_args()
out=pathlib.Path(a.output);out.mkdir(parents=True,exist_ok=True)
for pattern in ['spread','burst','recovery']:
    for nodes in [100,500,1000]:
        for sse in [0,1,8,32]:
            target=out/f'{nodes}-{sse}-{pattern}.json'
            if target.exists() and target.stat().st_size:
                json.loads(target.read_text());continue
            result=subprocess.run([a.binary,'-nodes',str(nodes),'-sse',str(sse),'-duration',a.duration,'-pattern',pattern],capture_output=True,text=True)
            if result.returncode:raise SystemExit(result.stderr)
            target.write_text(result.stdout);json.loads(result.stdout)
            print(target.name,flush=True)
for name,extra,duration in [('faults-slow',['-faults','-slow-clients','1'],'65s'),('stability',[],a.stability)]:
    target=out/(name+'.json')
    if target.exists() and target.stat().st_size:continue
    result=subprocess.run([a.binary,'-nodes','1000','-sse','32','-duration',duration,*extra],capture_output=True,text=True)
    if result.returncode:raise SystemExit(result.stderr)
    target.write_text(result.stdout);print(target.name,flush=True)
