"""Isolated Timescale/PostgreSQL migration, accounts and 10-user load test.
Creates/removes only a fresh, uniquely named Compose project and its volumes.
No deployment credentials are read; all credentials below are generated fixtures.
"""
import base64
from concurrent.futures import ThreadPoolExecutor
import hashlib
import json
import os
from pathlib import Path
import secrets
import socket
import subprocess
import time
import urllib.error
import urllib.request
import uuid

ROOT=Path(__file__).resolve().parents[1]
project='akasha-e2e-'+uuid.uuid4().hex[:10]
env=dict(os.environ)
for key in ['AKASHA_DB_PASSWORD','AKASHA_APP_DB_PASSWORD','AKASHA_HUB_TOKEN','AKASHA_ADMIN_PASSWORD']:
    env[key]=secrets.token_hex(32)
env['AKASHA_ADMIN_USERNAME']='fixture-admin'
with socket.socket() as s:
    s.bind(('127.0.0.1',0));port=s.getsockname()[1]
env['AKASHA_HUB_PORT']=str(port)
url='http://127.0.0.1:'+str(port)
base=['docker','compose','--project-name',project,'-f',str(ROOT/'compose.yaml')]
artifact=ROOT/'.local'/project;artifact.mkdir(parents=True)

def command(args,timeout=900):
    p=subprocess.run(args,cwd=ROOT,env=env,capture_output=True,text=True,encoding='utf-8',errors='replace',timeout=timeout)
    output=p.stdout+p.stderr
    for key in ['AKASHA_DB_PASSWORD','AKASHA_APP_DB_PASSWORD','AKASHA_HUB_TOKEN','AKASHA_ADMIN_PASSWORD']:
        output=output.replace(env[key],'[fixture credential]')
    if p.returncode:
        raise RuntimeError('Command failed: '+str(args[:4])+'\n'+output[-9000:])
    return output

def call(path,token='',body=None,expected=200):
    headers={'Content-Type':'application/json'}
    if token:headers['Authorization']='Bearer '+token
    request=urllib.request.Request(url+path,data=None if body is None else json.dumps(body).encode(),headers=headers)
    started=time.perf_counter()
    try:
        with urllib.request.urlopen(request,timeout=45) as response:status=response.status;data=response.read()
    except urllib.error.HTTPError as e:status=e.code;data=e.read()
    elapsed=(time.perf_counter()-started)*1000
    assert status==expected,(path,status,data[:300])
    return json.loads(data) if data else None,elapsed

def psql(sql):
    return command(base+['exec','-T','db','psql','-U','akasha_migrate','-d','akasha','-At','-c',sql],60).strip()

before=command(['docker','ps','-a','--format','{{.ID}} {{.Names}}']).splitlines()
report={'compose_project':project,'users':10,'passed':False,'checks':[]}
try:
    print('Starting isolated PostgreSQL/TimescaleDB, Alembic and hub',flush=True)
    command(base+['up','--build','-d'])
    for attempt in range(90):
        try:call('/healthz');break
        except Exception:time.sleep(1)
    else:raise RuntimeError('hub health deadline exceeded')
    report['timescale_version']=psql("SELECT extversion FROM pg_extension WHERE extname='timescaledb'")
    report['postgres_version']=psql('SHOW server_version')
    assert psql('SELECT version_num FROM alembic_version')=='0001_accounts_timescale'
    assert psql("SELECT count(*) FROM timescaledb_information.hypertables WHERE hypertable_name='request_metrics'")=='1'
    assert psql("SELECT rolsuper FROM pg_roles WHERE rolname='akasha_app'")=='f'
    report['checks'].append('Alembic revision, Timescale hypertable, application database role without superuser')
    owner,_=call('/v1/login',body={'username':'fixture-admin','password':env['AKASHA_ADMIN_PASSWORD']});admin=owner['token']
    call('/v1/login',body={'username':'fixture-admin','password':'invalid-fixture-password'},expected=401)
    passwords=[secrets.token_hex(24) for _ in range(10)];accounts=[]
    for i in range(10):
        u,_=call('/v1/accounts',admin,{'name':'Coder '+str(i),'username':'coder'+str(i),'password':passwords[i]},201);accounts.append(u)
    def login(i):
        result,_=call('/v1/login',body={'username':'coder'+str(i),'password':passwords[i]});return result['token']
    with ThreadPoolExecutor(10) as pool:tokens=list(pool.map(login,range(10)))
    for i in range(10):
        group='work' if i<5 else 'home'
        enrollment={'space':{'id':group,'name':group,'root':''},'project':{'id':'project'+str(i),'space_id':group,'name':'Project '+str(i),'root':'','shared':False}}
        call('/v1/enroll',admin,enrollment)
        call('/v1/grants',admin,{'project_id':'project'+str(i),'user_id':accounts[i]['id'],'role':'editor'})
    for i in range(10):
        catalog,_=call('/v1/catalog',tokens[i]);assert {e['project']['id'] for e in catalog}=={'project'+str(i)}
        call('/v1/events/project'+str((i+5)%10)+'?after=0',tokens[i],expected=403)
    report['checks'].append('Ten concurrent account logins; ten isolated catalogs; cross-group reads denied')
    print('Running 10-user concurrent sync workload',flush=True)
    def worker(i):
        timings=[];group='work' if i<5 else 'home';pid='project'+str(i)
        for n in range(20):
            events=[{'version':1,'space_id':group,'project_id':pid,'id':uuid.uuid4().hex,'ciphertext':base64.b64encode(secrets.token_bytes(512)).decode()}]
            value,ms=call('/v1/events/'+pid,tokens[i],events);assert value['added']==1;timings.append(ms)
            _,ms=call('/v1/events/'+pid+'?after=0',tokens[i]);timings.append(ms)
        return timings
    start=time.perf_counter()
    with ThreadPoolExecutor(10) as pool:timings=[v for part in pool.map(worker,range(10)) for v in part]
    elapsed=time.perf_counter()-start;timings.sort()
    report['load']={'requests':len(timings),'duration_seconds':elapsed,'requests_per_second':len(timings)/elapsed,'p50_ms':timings[len(timings)//2],'p95_ms':timings[int(len(timings)*.95)],'max_ms':max(timings)}
    for i in range(10):
        data,_=call('/v1/events/project'+str(i)+'?after=0',tokens[i]);assert data['head']==20 and len(data['events'])==20
    # All ten editors write to one shared project concurrently; the row lock
    # must preserve a contiguous sequence without lost events or duplicates.
    shared={'space':{'id':'team','name':'team','root':''},'project':{'id':'team-project','space_id':'team','name':'Team','root':'','shared':False}}
    call('/v1/enroll',admin,shared)
    for u in accounts:call('/v1/grants',admin,{'project_id':'team-project','user_id':u['id'],'role':'editor'})
    def shared_writer(i):
        event={'version':1,'space_id':'team','project_id':'team-project','id':uuid.uuid4().hex,'ciphertext':base64.b64encode(secrets.token_bytes(512)).decode()}
        result,_=call('/v1/events/team-project',tokens[i],[event]);assert result['added']==1
        result,_=call('/v1/events/team-project',tokens[i],[event]);assert result['added']==0
    with ThreadPoolExecutor(10) as pool:list(pool.map(shared_writer,range(10)))
    data,_=call('/v1/events/team-project?after=0',admin);assert [e['sequence'] for e in data['events']]==list(range(1,11))
    report['checks'].append('400 parallel HTTP sync requests; 200 preserved events; ten same-project writers and idempotent retries')
    call('/v1/sessions/revoke',admin,{'user_id':accounts[0]['id'],'disable':True})
    call('/v1/catalog',tokens[0],expected=403)
    call('/v1/login',body={'username':'coder0','password':passwords[0]},expected=401)
    call('/v1/logout',tokens[1],{})
    call('/v1/catalog',tokens[1],expected=403)
    call('/v1/stats',tokens[2],expected=403)
    time.sleep(.3)
    stats,_=call('/v1/stats',admin)
    report['metrics']=stats
    assert stats['active_users_24h']>=10 and stats['metrics_dropped']==0,stats
    report['checks'].append('Disable/logout immediately revoke sessions; Timescale metrics restricted to administrator')
    command(base+['run','--rm','migrate'])
    command(base+['restart','hub'])
    for attempt in range(30):
        try:call('/healthz');break
        except Exception:time.sleep(1)
    data,_=call('/v1/events/team-project?after=0',admin);assert data['head']==10
    report['checks'].append('Repeated Alembic upgrade and hub restart preserve accounts, sessions and event history')
    print('Running real encrypted two-client CLI/MCP/hooks/skills smoke against PostgreSQL',flush=True)
    command(['python','scripts/smoke.py','--exe','.local/akasha-next.exe','--external-hub','--hub-url',url,'--out','reports/postgres-encrypted-smoke.json'])
    report['checks'].append('Real CLI: encrypted vault/code/Git/context synchronization through PostgreSQL')
    command(['python','scripts/skill-smoke.py','--exe','.local/akasha-next.exe','--external-hub','--hub-url',url,'--out','reports/postgres-skill-smoke.json'])
    report['checks'].append('Single-command skill creation, encrypted two-client transfer, divergent versions and protected installation through PostgreSQL')
    report['passed']=True
except Exception as error:
    report['failure']=str(error)
    diagnostic=command(base+['logs','--no-color','--tail','90','db','migrate','hub'],30)
    (artifact/'diagnostics.log').write_text(diagnostic,encoding='utf-8')
    print(diagnostic[-10000:],flush=True)
    raise
finally:
    print('Cleaning up only '+project,flush=True)
    try:command(base+['down','--volumes','--remove-orphans'],120)
    finally:
        # Compare identities, including stopped containers. Other projects may
        # restart independently; a running-only snapshot produces false losses.
        after=command(['docker','ps','-a','--format','{{.ID}} {{.Names}}']).splitlines()
        (artifact/'container-inventory.json').write_text(json.dumps({'before':before,'after':after,'missing':sorted(set(before)-set(after))},indent=2),encoding='utf-8')
        report['other_containers_unchanged']=set(before).issubset(set(after))
        report['container_inventory_scope']='pre-existing container identities, including stopped containers'
        (ROOT/'reports/postgres-smoke.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf-8')
        print(json.dumps(report,ensure_ascii=False,indent=2),flush=True)
assert report['passed'] and report['other_containers_unchanged']
