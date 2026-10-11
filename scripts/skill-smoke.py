"""Real CLI: generate a skill, retain variants, cloud transfer and install.
Uses generated fixture credentials and isolated directories only.
"""
import argparse
import json
import os
from pathlib import Path
import secrets
import socket
import subprocess
import time
import urllib.request
import uuid

ROOT=Path(__file__).resolve().parents[1]
p=argparse.ArgumentParser();p.add_argument('--exe',default='.local/akasha-next.exe');p.add_argument('--external-hub',action='store_true');p.add_argument('--hub-url');p.add_argument('--out',default='reports/skill-smoke.json');a=p.parse_args()
base=ROOT/'.local'/('skills-cli-'+uuid.uuid4().hex[:12]);base.mkdir(parents=True)
env=dict(os.environ)
env['AKASHA_VAULT_PASSWORD']=secrets.token_hex(24);env['AKASHA_KEY_PASSPHRASE']=secrets.token_hex(24)
if not a.external_hub:env['AKASHA_HUB_TOKEN']=secrets.token_hex(32)
exe=str((ROOT/a.exe).resolve())
def cli(data,*args,ok=True):
    r=subprocess.run([exe,args[0],'--data',str(base/data),*args[1:]],env=env,cwd=ROOT,capture_output=True,text=True,encoding='utf-8',timeout=90)
    if not ok:assert r.returncode!=0;return None
    assert r.returncode==0,(args[0],r.stderr)
    return json.loads(r.stdout)
hub=None
if not a.external_hub:
    with socket.socket() as s:s.bind(('127.0.0.1',0));port=s.getsockname()[1]
    a.hub_url='http://127.0.0.1:'+str(port)
    hub=subprocess.Popen([exe,'hub','--data',str(base/'hub'),'--listen','127.0.0.1:'+str(port)],env=env,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    for _ in range(60):
        try:urllib.request.urlopen(a.hub_url+'/healthz',timeout=2).close();break
        except Exception:time.sleep(.1)
try:
    space='skills-'+uuid.uuid4().hex[:8]
    cli('a','space-add','--id',space,'--name','Skills fixture')
    project=cli('a','project-add','--space',space,'--name','Procedure source')['id']
    cli('a','remember','--project',project,'--kind','decision','--title','Verified retry procedure','--text','Reuse the same idempotency key when retrying a payment request. Duplicate request tests passed in the fixture.')
    procedure=base/'procedure.md';procedure.write_text('1. Find the payment request handler.\n2. Preserve the idempotency key across retries.\n3. Verify duplicate requests do not charge twice.\n',encoding='utf-8')
    args=['skill-create','--project',project,'--name','payment-retry','--description','Use when implementing idempotent payment retries.','--query','Verified retry procedure','--file',str(procedure)]
    first=cli('a',*args,'--url',a.hub_url)
    assert 'uploaded' in first['cloud'] and Path(first['local']['path'],'SKILL.md').exists()
    second=cli('a',*args);assert second['version']['id']==first['version']['id'] and not second['published']
    procedure.write_text(procedure.read_text(encoding='utf-8')+'4. Verify the timeout recovery case.\n',encoding='utf-8')
    variant=cli('a',*args,'--url',a.hub_url);assert variant['version']['sha256']!=first['version']['sha256']
    versions=cli('a','skill-versions','--space',space,'--name','payment-retry');assert len(versions)==2
    import hashlib
    shared='shared-'+hashlib.sha256(space.encode()).hexdigest()[:32]
    key=base/'shared.akasha-key'
    cli('a','key-export','--project',shared,'--out',str(key))
    cli('b','sync-clone','--project',shared,'--url',a.hub_url,'--file',str(key))
    remote=cli('b','skill-versions','--space',space,'--name','payment-retry');assert {x['sha256'] for x in remote}=={x['sha256'] for x in versions}
    dest=base/'installed'/'payment-retry'
    installed=cli('b','skill-install','--space',space,'--id',first['version']['id'],'--out',str(dest));assert installed['changed']
    cli('b','skill-install','--space',space,'--id',variant['version']['id'],'--out',str(dest),ok=False)
    updated=cli('b','skill-install','--space',space,'--id',variant['version']['id'],'--out',str(dest),'--replace');assert Path(updated['backup'],'SKILL.md').exists()
    cli('a','skill-create','--project',project,'--space','foreign','--name','wrong-group','--description','Use for fixture checks.','--file',str(procedure),ok=False)
    report={'passed':True,'checks':['single command: generated SKILL.md + evidence + provenance + local repository + encrypted upload','identical input creates no duplicate','changed procedure retains both versions under one name','second computer retrieves identical hashes','specific version installation and preserved replacement backup','cross-group creation denied'],'versions':2,'source_revisions':len(first['source_revisions']),'fixture_directory':str(base),'installed_directory':str(dest),'external_hub':a.external_hub}
    (ROOT/a.out).write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf-8');print(json.dumps(report,ensure_ascii=False,indent=2))
finally:
    if hub:hub.terminate();hub.wait(timeout=20)
