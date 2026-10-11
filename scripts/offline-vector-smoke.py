"""Verify real Linux embeddings/index/search with Docker networking disabled.
Only model-install may download pinned runtimes. No project API keys are used.
"""
import json
import math
from pathlib import Path
import shutil
import subprocess
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]
model = 'multilingual-e5-small-int8-761b726d-mean-v1'
data = ROOT / '.local/linux-vector-data'
folder = data / 'models' / model
folder.mkdir(parents=True, exist_ok=True)
for name in ['model.onnx', 'tokenizer.json']:
    target = folder / name
    if not target.exists():
        shutil.copyfile(ROOT / '.local/vector-data/models' / model / name, target)

def run(*args, network='none'):
    command = ['docker', 'run', '--rm', '--name', 'akasha-vector-' + uuid.uuid4().hex[:10],
               '--network', network, '--cpus', '2', '--memory', '1g', '--pids-limit', '80',
               '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges:true',
               '--mount', f'type=bind,source={data},target=/data',
               'akasha:0.2.0', args[0], '--data', '/data', *args[1:]]
    completed = subprocess.run(command, cwd=ROOT, capture_output=True, text=True, encoding='utf-8', timeout=600)
    if completed.returncode:
        raise RuntimeError(completed.stderr[-4000:])
    return json.loads(completed.stdout)

print('Installing/verifying pinned Linux runtime; model weights already local', flush=True)
installation = run('model-install', network='bridge')
print('Running native inference, indexing and retrieval with --network none', flush=True)
start = time.perf_counter()
embedded = run('embed', '--text', 'Как избежать повторного списания денег?')
norm = math.sqrt(sum(x*x for x in embedded['vector']))
assert len(embedded['vector']) == 384 and abs(norm-1) < 1e-5
suffix = uuid.uuid4().hex[:8]
run('space-add', '--id', 'offline-'+suffix, '--name', 'Offline fixture')
project = run('project-add', '--space', 'offline-'+suffix, '--name', 'Vector fixture')['id']
run('remember', '--project', project, '--kind', 'decision', '--title', 'Payments', '--text', 'An idempotency key prevents a payment request from charging the customer twice when retries occur.')
run('remember', '--project', project, '--kind', 'note', '--title', 'Layout', '--text', 'CSS grid controls the placement of visual items in rows and columns.')
indexed = run('vector-index', '--project', project)
found = run('vector-search', '--project', project, '--query', 'Как избежать повторного списания денег?')
assert indexed['chunks'] == 2 and found[0]['title'] == 'Payments', found
report = {'passed': True, 'platform': 'linux/amd64', 'network_for_inference': 'none',
          'model': model, 'dimensions': 384, 'norm': norm, 'chunks': indexed['chunks'],
          'top_match': found[0]['title'], 'duration_seconds': time.perf_counter()-start,
          'model_install_network': 'bridge; pinned runtime downloads only', 'container_memory_limit_bytes': 1073741824}
(ROOT / 'reports/linux-offline-vector.json').write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding='utf-8')
print(json.dumps(report, ensure_ascii=False, indent=2))
