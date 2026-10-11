import concurrent.futures,hashlib,json,re
from pathlib import Path
from datetime import datetime,timezone
import requests
ROOT=Path(__file__).resolve().parent
RAW=ROOT/'raw'/'repositories';RAW.mkdir(parents=True,exist_ok=True)
REPOS=['Gentleman-Programming/engram','DeusData/codebase-memory-mcp','getzep/graphiti','mem0ai/mem0','letta-ai/letta','basicmachines-co/basic-memory','topoteretes/cognee','vectorize-io/hindsight','oraios/serena','Aider-AI/aider','microsoft/graphrag','microsoft/LLMLingua','gastownhall/beads','mksglu/context-mode','volcengine/OpenViking','TencentCloud/TencentDB-Agent-Memory','NevaMind-AI/memU','EverMind-AI/EverOS','MemTensor/MemOS','memvid/memvid','doobidoo/mcp-memory-service','campfirein/byterover-cli','tigerless-labs/agent-memory','vercel-labs/lat.md','iwe-org/iwe','aiming-lab/SimpleMem','modelcontextprotocol/go-sdk','tiktoken-go/tokenizer','blevesearch/bleve','automerge/automerge','loro-dev/loro','syncthing/syncthing','restic/restic','casbin/casbin','openfga/openfga','FiloSottile/age']
branches={}
for p in (ROOT/'raw'/'github').glob('*.json'):
 for r in json.loads(p.read_text(encoding='utf8')).get('items',[]): branches[r['full_name']]=r['default_branch']
def fetch(repo):
 for branch in dict.fromkeys([branches.get(repo,'main'),'main','master']):
  for name in ['README.md','README.rst']:
   url=f'https://raw.githubusercontent.com/{repo}/{branch}/{name}'
   try:
    r=requests.get(url,timeout=18)
    if r.status_code!=200:continue
    body=r.text;digest=hashlib.sha256(r.content).hexdigest();(RAW/(digest+'.md')).write_text(body,encoding='utf8')
    excerpts=[re.sub(r'\s+',' ',s).strip() for s in body.splitlines() if re.search(r'memor|token|search|graph|sync|encrypt|license|SQLite|context|conflict|Windows|budget|tenant',s,re.I) and 40<len(s)<900]
    return {'repo':repo,'url':url,'status':200,'fetched_at':datetime.now(timezone.utc).isoformat(),'sha256':digest,'characters':len(body),'excerpts':excerpts[:32]}
   except requests.RequestException:pass
 return {'repo':repo,'status':0}
with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
 with (ROOT/'repositories.jsonl').open('w',encoding='utf8') as f:
  for r in pool.map(fetch,REPOS):f.write(json.dumps(r,ensure_ascii=False)+'\n');print(r['repo'],r['status'],flush=True)
