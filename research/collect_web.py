"""Bounded primary-source documentation corpus. One worker per source family."""
import concurrent.futures, hashlib, json, re, threading, time
from collections import deque
from datetime import datetime, timezone
from pathlib import Path
from urllib.parse import urljoin, urlsplit, urlunsplit, unquote
import requests
from bs4 import BeautifulSoup

ROOT=Path(__file__).resolve().parent
RAW=ROOT/'raw'/'web'; RAW.mkdir(parents=True,exist_ok=True)
LOG=ROOT/'web_sources.jsonl'
LOCK=threading.Lock()
SEEN=set(); HASHES=set(); ACCEPTED=set()
if LOG.exists():
 for line in LOG.read_text(encoding='utf-8').splitlines():
  r=json.loads(line)
  if r.get('status')==200:
   SEEN.add(r['url'])
   if r.get('accepted'): HASHES.add(r['sha256']); ACCEPTED.add(r['url'])

# Groups are official maintainers' documentation or primary research, not aggregators.
SOURCES=[
 ('codex','https://learn.chatgpt.com',['/docs/hooks','/docs/extend/mcp','/docs/app-server','/docs/config-reference','/docs/agents-md','/docs/skills'],r'^/docs/(hooks|extend|app-server|config|agents|skills|memories|non-interactive|sdk|permissions|git-worktrees)',16),
 ('openai','https://developers.openai.com',['/api/docs/guides/prompt-caching','/api/docs/guides/compaction','/plugins/build/plugins','/api/docs/guides/latency-optimization','/api/docs/guides/retrieval'],r'^/(api/docs/guides/(prompt|compaction|latency|retrieval|conversation|evaluation|your-data|tools|counting)|plugins/(build|concepts|guides|plan))',14),
 ('sqlite','https://www.sqlite.org',['/fts5.html','/wal.html','/backup.html','/isolation.html','/lang_transaction.html','/lang_with.html','/json1.html','/lockingv3.html','/sessionintro.html','/limits.html','/pragma.html','/atomiccommit.html','/queryplanner.html','/howtocorrupt.html','/fileformat2.html','/foreignkeys.html','/c3ref/backup_finish.html','/lang_upsert.html'],r'^/(fts5|wal|backup|isolation|lang_|json1|lockingv3|session|limits|pragma|atomiccommit|queryplanner|howtocorrupt|fileformat2|foreignkeys|c3ref/backup)',18),
 ('mcp','https://modelcontextprotocol.io',['/docs/learn/architecture','/specification/2025-11-25','/docs/develop/build-server','/docs/tutorials/security/authorization'],r'^/(docs|specification)/',16),
 ('mem0','https://docs.mem0.ai',['/open-source/overview','/core-concepts/memory-types','/open-source/features/graph-memory','/open-source/features/metadata-filtering','/platform/features/graph-memory'],r'^/(open-source|core-concepts|platform/features)',14),
 ('zep','https://help.getzep.com',['/graphiti/graphiti/overview','/concepts','/v2/graphiti/overview','/graphiti/core-concepts/temporal-awareness'],r'^/(graphiti|v2|concepts|memory|users|sessions|projects)',14),
 ('letta','https://docs.letta.com',['/guides/agents/memory','/guides/agents/memory-blocks','/guides/agents/archival-memory','/guides/agents/context-window','/guides/agents/overview'],r'^/(guides|concepts)/',16),
 ('basic-memory','https://docs.basicmemory.com',['/','/user-guide/','/concepts/knowledge-graph/','/integrations/codex/'],r'^/(user-guide|concepts|integrations|reference|guides|advanced)',14),
 ('cognee','https://docs.cognee.ai',['/','/core-concepts/overview','/guides/overview'],r'^/(core-concepts|guides|how-to-guides|setup-configuration|guides-and-tutorials|reference)',12),
 ('hindsight','https://hindsight.vectorize.io',['/developer','/developer/api/retain','/developer/api/recall','/developer/api/reflect','/architecture','/blog/'],r'^/(developer|architecture|blog)/',14),
 ('aider','https://aider.chat',['/docs/repomap.html','/docs/usage.html','/docs/usage/conventions.html','/docs/config/options.html','/docs/benchmarks.html'],r'^/docs/(repomap|usage|config|benchmarks|more|llms)',16),
 ('serena','https://oraios.github.io',['/serena/','/serena/01-about/000_introduction.html'],r'^/serena/',12),
 ('obsidian','https://help.obsidian.md',['/Home','/Linking+notes+and+files/Internal+links','/Plugins/Graph+view','/Plugins/Search','/Sync/Introduction+to+Obsidian+Sync'],r'^/(Linking|Plugins|Sync|Files|Editing|Advanced|Extending|Publish|Obsidian|import|sync|plugins|links|vault|How|Getting)',14),
 ('joplin','https://joplinapp.org',['/help/apps/sync/','/help/apps/sync/e2ee/','/help/dev/spec/sync/','/help/dev/spec/e2ee/','/help/apps/search/','/help/apps/conflict/'],r'^/help/(apps|dev/spec)/',14),
 ('automerge','https://automerge.org',['/docs/','/docs/hello/','/docs/reference/under-the-hood/storage/'],r'^/docs/',14),
 ('yjs','https://docs.yjs.dev',['/','/getting-started/a-collaborative-editor','/api/document-updates','/api/shared-types/y.map','/api/internals'],r'^/(api|getting-started|tutorials|ecosystem|further-reading)',14),
 ('loro','https://loro.dev',['/docs','/docs/concepts/crdt','/docs/concepts/version','/docs/concepts/sync'],r'^/docs/',14),
 ('syncthing','https://docs.syncthing.net',['/users/syncing.html','/users/versioning.html','/users/security.html','/users/untrusted.html','/specs/bep-v1.html','/dev/device-ids.html'],r'^/(users|specs|dev)/',14),
 ('restic','https://restic.readthedocs.io',['/en/stable/100_references.html','/en/stable/030_preparing_a_new_repo.html','/en/stable/045_working_with_repos.html','/en/stable/070_encryption.html'],r'^/en/stable/',14),
 ('openfga','https://openfga.dev',['/docs','/docs/modeling/roles-and-permissions','/docs/concepts','/docs/best-practices'],r'^/docs/',14),
 ('casbin','https://casbin.org',['/docs/overview/','/docs/rbac/','/docs/rbac-with-domains/','/docs/abac/'],r'^/docs/',14),
 ('owasp','https://cheatsheetseries.owasp.org',['/cheatsheets/Secrets_Management_Cheat_Sheet.html','/cheatsheets/Authorization_Cheat_Sheet.html','/cheatsheets/LLM_Prompt_Injection_Prevention_Cheat_Sheet.html','/cheatsheets/Cryptographic_Storage_Cheat_Sheet.html','/cheatsheets/Password_Storage_Cheat_Sheet.html','/cheatsheets/REST_Security_Cheat_Sheet.html','/cheatsheets/SQL_Injection_Prevention_Cheat_Sheet.html','/cheatsheets/Authentication_Cheat_Sheet.html','/cheatsheets/Input_Validation_Cheat_Sheet.html','/cheatsheets/Key_Management_Cheat_Sheet.html','/cheatsheets/Logging_Cheat_Sheet.html','/cheatsheets/Transport_Layer_Security_Cheat_Sheet.html'],r'^/cheatsheets/(Secrets_|Authorization_|LLM_|Cryptographic_|Password_|REST_|SQL_|Authentication_|Input_|Key_|Logging_|Transport_|Multi_Tenant_|File_Upload_)',14),
 ('go','https://go.dev',['/doc/database/transactions','/doc/database/manage-connections','/doc/database/cancel-operations','/doc/security/best-practices','/doc/security/fuzz/','/doc/diagnostics','/doc/articles/race_detector','/blog/go-context','/blog/profiling-go-programs','/ref/mem'],r'^/(doc/(database|security|diagnostics|articles/race_detector)|blog/(go-context|profiling-go)|ref/mem)',12),
 ('bleve','https://blevesearch.com',['/docs/','/docs/Query/','/docs/Index-Mapping/'],r'^/docs/',12),
 ('qdrant','https://qdrant.tech',['/documentation/concepts/hybrid-queries/','/documentation/concepts/indexing/','/documentation/guides/multitenancy/','/documentation/concepts/filtering/'],r'^/documentation/(concepts|guides)/',14),
 ('chroma','https://docs.trychroma.com',['/docs/overview/introduction','/docs/collections/configure','/docs/querying-collections/query-and-get'],r'^/docs/(overview|collections|querying-collections|embeddings|run-chroma)',10),
 ('langgraph','https://docs.langchain.com',['/oss/python/langgraph/memory','/oss/python/langgraph/persistence','/oss/python/langgraph/durable-execution','/oss/python/langchain/short-term-memory','/oss/python/langchain/long-term-memory'],r'^/oss/python/(langgraph|langchain)/',16),
 ('langmem','https://langchain-ai.github.io',['/langmem/','/langmem/concepts/conceptual_guide/','/langmem/guides/extract_semantic_memories/'],r'^/langmem/',10),
 ('graph-rag','https://microsoft.github.io',['/graphrag/','/graphrag/query/local_search/','/graphrag/index/overview/','/graphrag/query/drift_search/'],r'^/graphrag/',14),
 ('llmlingua','https://www.microsoft.com',['/en-us/research/project/llmlingua/','/en-us/research/project/llmlingua/llmlingua-2/','/en-us/research/project/llmlingua/longllmlingua/'],r'^/en-us/research/(project/llmlingua|publication/.*(prompt|memory|retrieval|compress))',8),
 ('local-first','https://www.inkandswitch.com',['/local-first/','/local-first-software/','/blog/local-first/'],r'^/(local-first|local-first-software|blog/local-first)',3),
 ('papers','https://arxiv.org',['/abs/2310.08560','/abs/2310.05736','/abs/2310.06839','/abs/2403.12968','/abs/2402.17753','/abs/2410.10813','/abs/2501.13956','/abs/2502.05589','/abs/2504.19413','/abs/2404.16130','/abs/2404.09036'],r'^/abs/',11),
]
KEYWORDS=re.compile(r'\b(memor(?:y|ies)|context|retriev\w*|search|graph|token\w*|compress\w*|sync\w*|conflict\w*|encrypt\w*|auth\w*|permission\w*|tenant\w*|checkpoint\w*|cache|caching|sqlite|transaction\w*|index\w*|concurren\w*|agent\w*|wiki|offline|CRDT|backup\w*|secret\w*|version\w*|MCP|Go|golang)\b',re.I)
def canonical(u):
 s=urlsplit(u)
 return urlunsplit((s.scheme,s.netloc.lower(),s.path.rstrip('/') or '/',s.query if s.path=='/abs' else '', ''))
def run_source(src):
 name,base,paths,pattern,limit=src
 queue=deque(urljoin(base,p) for p in paths); local=set(); accepted=0; tries=0
 session=requests.Session();session.headers.update({'User-Agent':'Akasha-Research/0.1 (bounded primary documentation review)'})
 while queue and accepted<limit and tries<limit*5:
  url=canonical(queue.popleft())
  if url in local: continue
  local.add(url)
  with LOCK:
   if url in SEEN:
    if url in ACCEPTED: accepted+=1
    continue
   SEEN.add(url)
  tries+=1
  rec={'family':name,'url':url,'fetched_at':datetime.now(timezone.utc).isoformat(),'accepted':False}
  try:
   response=session.get(url,timeout=(10,22));rec.update(status=response.status_code,final_url=response.url)
   response.raise_for_status()
   if 'text/' not in response.headers.get('Content-Type',''): raise ValueError('non-text document')
   soup=BeautifulSoup(response.content[:4_000_000],'lxml')
   links=[urljoin(response.url,a.get('href','')) for a in soup.select('a[href]')]
   title=soup.title.get_text(' ',strip=True) if soup.title else url
   for tag in soup.select('script,style,nav,header,footer,aside,svg,noscript,form'): tag.decompose()
   content=soup.select_one('article') or soup.select_one('main') or soup.select_one('[role="main"]') or soup.body or soup
   text=content.get_text('\n',strip=True)
   text=re.sub(r'\n{3,}','\n\n',text).strip()
   digest=hashlib.sha256(text.encode()).hexdigest()
   terms=sorted(set(t.lower() for t in KEYWORDS.findall(text)))
   paras=[re.sub(r'\s+',' ',p).strip() for p in re.split(r'\n|(?<=[.!?])\s+(?=[A-Z])',text)]
   snippets=sorted([p for p in paras if 60<=len(p)<=650],key=lambda p:(len(KEYWORDS.findall(p)),len(p)),reverse=True)[:3]
   with LOCK:
    duplicate=digest in HASHES
    valid=len(text)>700 and len(terms)>=3 and not duplicate and not re.search(r'just a moment|access denied|page not found|404 not found',title,re.I)
    if valid: HASHES.add(digest);ACCEPTED.add(url)
   rec.update(title=title,characters=len(text),sha256=digest,terms=terms,screening_evidence=snippets,accepted=valid,duplicate=duplicate)
   if valid:
    accepted+=1
    (RAW/(digest+'.txt')).write_text(text,encoding='utf-8')
   for link in links:
    p=urlsplit(link)
    if p.scheme=='https' and p.netloc==urlsplit(base).netloc and re.search(pattern,unquote(p.path)) and not re.search(r'\.(png|jpg|gif|svg|pdf|zip|gz|exe)$|/tags/|/category/|/page/|/api-reference/|/reference/cli/',p.path): queue.append(link)
  except Exception as e: rec.setdefault('status',0);rec['error']=str(e)[:250]
  with LOCK:
   with LOG.open('a',encoding='utf-8') as f:f.write(json.dumps(rec,ensure_ascii=False)+'\n')
  time.sleep(.6)
 with LOCK: print(json.dumps({'family':name,'accepted':accepted,'attempted':tries,'total_accepted':len(ACCEPTED)}),flush=True)
 return accepted
if __name__=='__main__':
 with concurrent.futures.ThreadPoolExecutor(max_workers=5) as pool:
  list(pool.map(run_source,SOURCES))
 print(json.dumps({'total_unique_screened':len(ACCEPTED)}),flush=True)
