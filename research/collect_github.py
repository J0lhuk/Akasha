"""Public GitHub API search pages; optional existing gh login, no token extraction."""
import hashlib, json, time, urllib.parse, urllib.request, urllib.error, subprocess, sys
from pathlib import Path
from datetime import datetime, timezone

ROOT = Path(__file__).resolve().parent
RAW = ROOT / 'raw' / 'github'
RAW.mkdir(parents=True, exist_ok=True)
LOG = ROOT / 'github_pages.jsonl'
USE_GH = '--gh' in sys.argv
QUERIES = [
 'agent memory','coding agent memory','mcp memory','knowledge graph agent',
 'persistent memory','context engineering','context compression','prompt compression',
 'token optimization','code knowledge graph','code search','semantic code search',
 'repository map','local knowledge base','markdown knowledge base','obsidian mcp',
 'wiki knowledge graph','local first sync','sqlite sync','CRDT',
 'encrypted sync','secrets manager','password vault','RBAC golang',
 'full text search golang','vector search sqlite','hybrid retrieval','graph retrieval',
 'agent checkpoint','agent context','codex hooks','codex memory',
 'MCP server golang','memory consolidation','episodic memory','temporal knowledge graph',
 'LLM cache','prompt cache','agent collaboration','multi agent memory',
 'memgpt','mem0','graphiti','long term memory',
 'retrieval benchmark','RAG evaluation','event sourcing golang','offline first',
 'knowledge management','local search engine',
 'code intelligence','LLM memory','mcp knowledge','sqlite knowledge',
 'markdown wiki','context window','semantic cache','document graph',
 'agent skills','local RAG'
]
done = {}
if LOG.exists():
 for line in LOG.read_text(encoding='utf-8').splitlines():
  r=json.loads(line)
  if r.get('status')==200: done[(r['query'],r['page'])]=r
count=sum(bool(x.get('items')) for x in done.values())
for query in QUERIES:
 for page in range(1,5):
  if count >= 200: break
  if (query,page) in done: continue
  url='https://api.github.com/search/repositories?'+urllib.parse.urlencode({'q':query+' is:public','per_page':10,'page':page,'sort':'stars','order':'desc'})
  success=False
  for attempt in range(4):
   started=time.monotonic()
   rec={'query':query,'page':page,'url':url,'fetched_at':datetime.now(timezone.utc).isoformat(),'per_page':10}
   try:
    if USE_GH:
     result=subprocess.run(['gh','api',url],capture_output=True,timeout=40)
     if result.returncode: raise RuntimeError(result.stderr.decode('utf8','replace')[:250])
     body=result.stdout;status=200;rec['transport']='authenticated-gh-public-search'
    else:
     req=urllib.request.Request(url,headers={'User-Agent':'Akasha-Research/0.1','Accept':'application/vnd.github+json'})
     with urllib.request.urlopen(req,timeout=35) as response: body=response.read();status=response.status
    data=json.loads(body)
    if any(x.get('private') for x in data.get('items',[])): raise ValueError('private result rejected')
    rec.update(status=status,total_count=data.get('total_count'),incomplete_results=data.get('incomplete_results'),sha256=hashlib.sha256(body).hexdigest(),items=[{'name':x['full_name'],'url':x['html_url'],'description':x.get('description'),'stars':x['stargazers_count'],'language':x.get('language'),'license':(x.get('license') or {}).get('spdx_id'),'updated_at':x['updated_at']} for x in data.get('items',[])])
    (RAW/(rec['sha256']+'.json')).write_bytes(body)
    success=True
   except urllib.error.HTTPError as e:
    rec.update(status=e.code,error=str(e),retry_after=e.headers.get('Retry-After'),reset=e.headers.get('X-RateLimit-Reset'))
   except Exception as e: rec.update(status=0,error=repr(e))
   with LOG.open('a',encoding='utf-8') as out: out.write(json.dumps(rec,ensure_ascii=False)+'\n')
   if success:
    if rec['items']: count+=1
    print(json.dumps({'pages':count,'query':query,'page':page,'items':len(rec['items'])}),flush=True)
    break
   print(json.dumps({'retry':attempt+1,'query':query,'status':rec['status']}),flush=True)
   delay=65
   if rec.get('reset'): delay=max(7,min(180,int(rec['reset'])-time.time()+2))
   time.sleep(delay)
  time.sleep(max(0,(2.2 if USE_GH else 6.8)-(time.monotonic()-started)))
 if count>=200: break
print(json.dumps({'complete':count>=200,'nonempty_pages':count}),flush=True)
