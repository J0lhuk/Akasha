"""Bounded local validation. Never replaces the running Akasha executable."""
import json
import os
import platform
import subprocess
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
REPORTS = ROOT / 'ilang/reports'
REPORTS.mkdir(parents=True, exist_ok=True)
env = dict(os.environ, GOMAXPROCS=str(min(4, os.cpu_count() or 1)))
results = {'platform': platform.platform(), 'logical_cpus': os.cpu_count(), 'validation_gomaxprocs': int(env['GOMAXPROCS']), 'steps': []}

def run(name, command, extra_env=None):
    started = time.perf_counter()
    p = subprocess.run(command, cwd=ROOT, env={**env, **(extra_env or {})}, capture_output=True)
    elapsed = round(time.perf_counter()-started, 3)
    (REPORTS / (name+'.log')).write_bytes(p.stdout+p.stderr)
    entry = {'name': name, 'command': command, 'exit_code': p.returncode, 'seconds': elapsed}
    results['steps'].append(entry)
    (REPORTS/'validation.json').write_text(json.dumps(results,indent=2)+'\n',encoding='utf8')
    print(json.dumps(entry), flush=True)
    if p.returncode:
        print((p.stdout+p.stderr).decode('utf8',errors='replace')[-6000:],flush=True)
        raise SystemExit(p.returncode)

run('go-test', ['go','test','-p=4','-json','./...'])
run('go-vet', ['go','vet','-p=4','./...'])
run('build-windows', ['go','build','-p=4','-trimpath','-o','bin/akasha-ilang.exe','./cmd/akasha'], {'GOOS':'windows','GOARCH':'amd64','CGO_ENABLED':'0'})
run('build-linux', ['go','build','-p=4','-trimpath','-o','bin/akasha-ilang-linux-amd64','./cmd/akasha'], {'GOOS':'linux','GOARCH':'amd64','CGO_ENABLED':'0'})
run('benchmark', ['go','test','./internal/ilang','-run=^$','-bench=BenchmarkCompileComposition','-benchmem','-benchtime=1s','-count=3'])
run('cli-smoke', ['python','-X','utf8','ilang/examples/cli_smoke.py'])
print('All validation stages passed.',flush=True)
