"""Windows CLI login -> protected credential file -> authenticated call -> revoke."""
import json
import os
from pathlib import Path
import secrets
import socket
import subprocess
import time
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[1]
base = ROOT / '.local' / ('accounts-cli-' + uuid.uuid4().hex[:10])
base.mkdir(parents=True)
exe = str(ROOT / 'bin/akasha.exe')
env = dict(os.environ)
env.pop('AKASHA_DATABASE_URL', None)
env['AKASHA_HUB_TOKEN'] = secrets.token_hex(32)
env['AKASHA_ACCOUNT_FIXTURE'] = secrets.token_hex(24)
env['AKASHA_ADMIN_USERNAME'] = 'fixture-admin'
env['AKASHA_ADMIN_PASSWORD'] = env['AKASHA_ACCOUNT_FIXTURE']
data = str(base / 'client')

def cli(command, *args, ok=True):
    done = subprocess.run([exe, command, '--data', data, *args], cwd=ROOT, env=env,
                          capture_output=True, text=True, encoding='utf-8', timeout=40)
    if not ok:
        assert done.returncode != 0
        return
    assert done.returncode == 0, done.stderr
    return json.loads(done.stdout)

cli('account-set', '--username', 'fixture-admin', '--pass-env', 'AKASHA_ACCOUNT_FIXTURE')
with socket.socket() as sock:
    sock.bind(('127.0.0.1', 0))
    port = sock.getsockname()[1]
url = 'http://127.0.0.1:' + str(port)
server = subprocess.Popen([exe, 'hub', '--data', str(base / 'hub'), '--listen', '127.0.0.1:' + str(port)],
                          env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
try:
    for _ in range(100):
        try:
            urllib.request.urlopen(url + '/healthz', timeout=1).close()
            break
        except Exception:
            time.sleep(.1)
    credential = base / 'session.credential'
    login = cli('hub-login', '--url', url, '--username', 'fixture-admin',
                '--pass-env', 'AKASHA_ACCOUNT_FIXTURE', '--out', str(credential))
    assert 'token' not in login and credential.exists()
    assert env['AKASHA_ACCOUNT_FIXTURE'].encode() not in credential.read_bytes()
    assert cli('sync-catalog', '--url', url, '--hub-token-file', str(credential)) == []
    cli('hub-login', '--url', url, '--username', 'fixture-admin',
        '--pass-env', 'AKASHA_ACCOUNT_FIXTURE', '--out', str(credential), ok=False)
    cli('hub-sessions-revoke', '--url', url, '--hub-token-file', str(credential), '--user', 'owner')
    cli('sync-catalog', '--url', url, '--hub-token-file', str(credential), ok=False)
    report = {'passed': True, 'platform': os.name,
              'checks': ['account password login from native CLI', 'protected session file without token in output',
                         'credential file authenticates a second process', 'existing credential file is preserved',
                         'revocation immediately denies further calls'], 'fixture': str(base)}
    (ROOT / 'reports/account-smoke.json').write_text(json.dumps(report, indent=2), encoding='utf-8')
    print(json.dumps(report, indent=2))
finally:
    server.terminate()
    server.wait(timeout=20)
