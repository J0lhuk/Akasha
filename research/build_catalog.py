"""Build the inspectable research ledger. Screening exclusions are explicit, not hidden."""
import csv,hashlib,json
from pathlib import Path
from urllib.parse import urlsplit
ROOT=Path(__file__).resolve().parent
accepted=[json.loads(s) for s in (ROOT/'web_sources.jsonl').read_text(encoding='utf8').splitlines() if json.loads(s).get('accepted')]
EXCLUSIONS={27:'Node SDK installation: peripheral to selected runtime',64:'Generic prompt-generation guide: outside memory mechanisms',67:'Web search API rather than local knowledge retrieval',92:'General usage index',107:'Blog listing rather than individual article',123:'License information tracked separately from technical article count',124:'Integration directory',142:'Provider setup: peripheral',158:'Business marketing page',166:'Provider setup: peripheral',175:'Provider setup: peripheral',218:'Documentation index',219:'Release listing',224:'Installation-only page',235:'llms.txt is a source index',250:'Contribution guide',263:'Documentation index',275:'Documentation index',294:'Application directory',297:'Build setup guide',309:'Framework landing page',310:'Framework landing page',312:'Generic framework component overview',322:'Case-study listing',332:'Generic quickstart',336:'Contribution/development setup',338:'Generic calculator quickstart',341:'Framework server setup',355:'Duplicate LLMLingua-2 publication',358:'Unrelated axion physics paper',359:'Duplicate LLMLingua publication',360:'Duplicate LongLLMLingua publication'}
FOCUS={
 'codex':'Hooks/MCP compatibility, context injection and lifecycle boundaries',
 'openai':'Provider context lifecycle, stable prefixes and measured caching',
 'sqlite':'Per-project FTS index, transactions, checkpoint and recovery correctness',
 'mcp':'Protocol compatibility, tool contracts, transport and authorization',
 'mem0':'Fact capture, project metadata filters, scope and reranking',
 'zep':'Temporal facts, provenance, graph traversal and graph namespaces',
 'letta':'Core versus archival memory, bounded always-present context',
 'basic-memory':'Readable Markdown, wikilinks, separate project graphs',
 'cognee':'Graph ingestion, source provenance, engine isolation',
 'hindsight':'Memory retention, retrieval budget, freshness and consolidation',
 'aider':'Symbol map, graph ranking and staged code context',
 'serena':'Symbol retrieval, project onboarding and indexing lifecycle',
 'joplin':'Local notes, full-text search, encryption and conflict preservation',
 'automerge':'Causal version history, concurrent editing and portable sync',
 'yjs':'Shared-data convergence, offline persistence and ACL boundary',
 'loro':'Version vectors, conflict semantics and incremental synchronization',
 'syncthing':'File manifests, conflict copies, encrypted remote storage',
 'restic':'Authenticated backups, content addressing and recovery',
 'openfga':'Explicit per-project visibility, permissions and access checks',
 'casbin':'Role and tenant scoped authorization',
 'owasp':'Secret separation, authorization before access, untrusted context',
 'go':'Concurrency, DB lifecycle, profiling and correctness tests',
 'bleve':'Local search alternatives, analysis and index design',
 'qdrant':'Hybrid ranking, prefiltering and tenant isolation alternatives',
 'chroma':'Index alternatives and conditional writes',
 'langgraph':'Thread checkpoints, cross-session memory and durable execution',
 'langmem':'Scoped semantic/episodic memory and background capture',
 'graph-rag':'Hierarchical graph context and cached ingestion',
 'llmlingua':'Model-assisted compression tradeoffs and quality evaluation',
 'local-first':'Offline ownership, multi-device collaboration and data longevity',
 'papers':'Memory quality, temporal reasoning, retrieval and compression research',
}
rows=[]
for i,r in enumerate(accepted,1):
 excluded=EXCLUSIONS.get(i)
 rows.append({'id':f'W{i:03}','status':'excluded' if excluded else 'screened_relevant','family':r['family'],'title':r['title'].replace('\n',' '),'url':r['url'],'final_url':r['final_url'],'retrieved_at':r['fetched_at'],'sha256':r['sha256'],'characters':r['characters'],'relevance':excluded or FOCUS[r['family']],'evidence':r.get('screening_evidence',[])})
(ROOT/'catalog.json').write_text(json.dumps(rows,ensure_ascii=False,indent=2),encoding='utf8')
with (ROOT/'catalog.csv').open('w',encoding='utf-8-sig',newline='') as f:
 w=csv.DictWriter(f,fieldnames=[k for k in rows[0] if k!='evidence']);w.writeheader()
 for r in rows:w.writerow({k:v for k,v in r.items() if k!='evidence'})
md=['# Реестр исследования','', 'Просмотр — тематический скрининг прочитанного текста. Это не полный аудит исходного кода всех систем. Подробные сравнительные выводы: `RESEARCH_NOTES.md`.','', '| ID | Статус | Материал | Применение |','|---|---|---|---|']
for r in rows:md.append(f"| {r['id']} | {r['status']} | [{r['title'].replace('|','/')}](<{r['url']}>) | {r['relevance']} |")
(ROOT/'CATALOG.md').write_text('\n'.join(md)+'\n',encoding='utf8')
g={}
for line in (ROOT/'github_pages.jsonl').read_text(encoding='utf8').splitlines():
 r=json.loads(line)
 if r.get('status')==200 and r.get('items'):g[(r['query'],r['page'])]=r
repos={x['name']:x for p in g.values() for x in p['items']}
summary={'github_nonempty_unique_search_pages':len(g),'github_unique_repositories':len(repos),'web_fetched_unique_texts':len(rows),'web_screened_relevant':sum(r['status']=='screened_relevant' for r in rows),'web_excluded':len(EXCLUSIONS),'web_relevant_hosts':len({urlsplit(r['final_url']).hostname for r in rows if r['status']=='screened_relevant'}),'repository_readmes':sum(json.loads(s).get('status')==200 for s in (ROOT/'repositories.jsonl').read_text(encoding='utf8').splitlines()),'gate_passed':len(g)>=200 and sum(r['status']=='screened_relevant' for r in rows)>=300}
(ROOT/'summary.json').write_text(json.dumps(summary,ensure_ascii=False,indent=2),encoding='utf8')
with (ROOT/'github_repositories.csv').open('w',encoding='utf-8-sig',newline='') as f:
 w=csv.DictWriter(f,fieldnames=list(next(iter(repos.values()))));w.writeheader();w.writerows(sorted(repos.values(),key=lambda r:-r['stars']))
md=['# Просмотренные страницы GitHub Search','', 'Страницы публичного поиска через официальный REST API, по 10 результатов на страницу. Непустые страницы считаются уникально по query + page. Релевантность отдельных репозиториев различается; отбор и глубокое сравнение ведутся отдельно.','', '| № | Запрос | Страница | Результатов всего | Найденные репозитории |','|---|---|---|---|---|']
for i,r in enumerate(g.values(),1):md.append(f"| {i} | [{r['query']}](<{r['url']}>) | {r['page']} | {r['total_count']} | {', '.join(x['name'] for x in r['items'])} |")
(ROOT/'GITHUB_SEARCH.md').write_text('\n'.join(md)+'\n',encoding='utf8')
print(json.dumps(summary,ensure_ascii=False))
