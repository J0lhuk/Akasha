"""Integrity-checked catalog. Review exclusions never edit acquisition history."""
import collections,csv,hashlib,json,re,zlib
from datetime import datetime,timezone
from pathlib import Path
from urllib.parse import urlsplit
ROOT=Path(__file__).resolve().parent
def records(name):
 p=ROOT/name
 if not p.exists():return []
 data=p.read_text(encoding='utf8');lines=data.split('\n')
 if data and not data.endswith('\n'):lines=lines[:-1]
 return [json.loads(s) for s in lines if s]
def norm(s):return re.sub(r'\s+',' ',s).strip()
def shingles(body):
 words=re.findall(r'\w+',body.lower())
 return set(sorted({zlib.crc32(' '.join(words[j:j+5]).encode()) for j in range(len(words)-4)})[:4096])
AXIS_USE={
 'meta':'разделение задачи, параметров промпта и измеримого критерия оптимизации',
 'language':'синтаксис, ограничения генерации, типизированный результат и переносимость адаптеров',
 'workflow':'явные зависимости, передача результатов, состояние и возобновление цепочки',
 'skills':'описание способности, протокол вызова, обнаружение и версионирование',
 'types':'контракты входов/выходов, объединение ограничений и диагностика конфликта',
 'compiler':'парсер, модель языка, ссылки, диагностика и инструменты редактора',
 'verification':'предусловия, постусловия, инварианты и границы доказуемости',
 'planning':'декомпозиция цели, допустимость действия, эффекты и выбор плана',
 'policy':'права, ограничения эффектов и независимая проверка политики',
 'eval':'внешние критерии, наборы задач, негативные примеры и стоимость эксперимента',
 'knowledge':'связи идей, происхождение свидетельств, версии и семантические ограничения',
}
rows=[];errors=[];normalized=set();titles={};semantic_urls=set()
decisions=json.loads((ROOT/'review_decisions.json').read_text(encoding='utf8')) if (ROOT/'review_decisions.json').exists() else {}
for i,r in enumerate([x for x in records('web_sources.jsonl') if x.get('accepted')],1):
 rid=f'W{i:03d}';reason='';raw=ROOT/'raw'/'web'/(r['sha256']+'.txt')
 body=raw.read_text(encoding='utf8')
 # write_text on Windows added CR before LF. Recover original extracted text
 # only when its declared content hash matches; keep byte hash independently.
 if hashlib.sha256(body.encode()).hexdigest()!=r['sha256']:
  recovered=raw.read_bytes().replace(b'\r\n',b'\n').decode('utf8')
  if hashlib.sha256(recovered.encode()).hexdigest()==r['sha256']:body=recovered
 if hashlib.sha256(body.encode()).hexdigest()!=r['sha256']:errors.append(rid+' raw_hash_mismatch')
 url=r['final_url'];path=urlsplit(url).path
 family=r['family'].replace('unifiedplanning','unified-planning')
 normhash=hashlib.sha256(norm(body).encode()).hexdigest()
 title=norm(r['title']).lower()
 if family=='unified-planning':title=re.sub(r' — unified-planning .* documentation$','',title)
 semantic_url=re.sub(r'\.(md|txt)$','',url).rstrip('/')
 titlekey=(family,title)
 if '/_sources/' in path or path.endswith(('.ipynb.txt','.md','.rst')):reason='alternate_source_format'
 elif semantic_url in semantic_urls or normhash in normalized:reason='normalized_duplicate'
 elif titlekey in titles and len(title)>18:
  # Distinct pages may both be named Overview. Compare word shingles before excluding.
  grams=shingles(body)
  prev=titles[titlekey]
  overlap=len(grams&prev)/max(1,min(len(grams),len(prev)))
  if overlap>=.82:reason='near_duplicate_same_subject'
 elif re.search(r'/installation|/get-started/quick-start-guide|/contributing|/how-to-contribute|/docs/overview/new-chapter|/llamaparse/',path,re.I):reason='setup_or_news_not_language_design'
 reason=decisions.get(rid,reason)
 if not reason:
  normalized.add(normhash);semantic_urls.add(semantic_url)
  titles[titlekey]=shingles(body)
 r={**r,'id':rid,'raw_file_sha256':hashlib.sha256(raw.read_bytes()).hexdigest(),'status':'excluded' if reason else 'screened_relevant','review_exclusion':reason,'application':AXIS_USE[r['axis']],'analysis_level':'full_text_automated_screening_and_review_card','content_path':str(raw.relative_to(ROOT)).replace('\\','/')}
 rows.append(r)
g={}
for r in records('github_pages.jsonl'):
 if r.get('status')==200 and r.get('items') and not r.get('incomplete_results'):
  raw=ROOT/'raw'/'github'/(r['sha256']+'.json')
  if hashlib.sha256(raw.read_bytes()).hexdigest()!=r['sha256']:errors.append(r['url']+' hash_mismatch')
  g[(r['query'],r['page'])]=r
repos={x['name']:x for r in g.values() for x in r['items']}
relevant=[r for r in rows if r['status']=='screened_relevant']
deep={r['name']:r for r in records('deep_sources.jsonl') if r.get('status')==200}
for r in deep.values():
 raw=ROOT/'raw'/'deep'/(r['sha256']+'.txt')
 if hashlib.sha256(raw.read_text(encoding='utf8').encode()).hexdigest()!=r['sha256']:errors.append(r['name']+' hash_mismatch')
summary={'generated_at':datetime.now(timezone.utc).isoformat(),'github_nonempty_complete_unique_search_pages':len(g),'github_results_per_full_page':30,'github_unique_repository_references':len(repos),'web_preliminary_screened':len(rows),'web_relevant_unique_pages':len(relevant),'review_excluded':len(rows)-len(relevant),'web_relevant_hosts':len({urlsplit(r['final_url']).hostname for r in relevant}),'web_source_families':len({r['family'] for r in relevant}),'by_axis':collections.Counter(r['axis'] for r in relevant),'readmes_acquired':sum(r['kind']=='repository' for r in deep.values()),'papers_acquired':sum(r['kind']=='paper' for r in deep.values()),'integrity_errors':errors,'acquisition_and_relevance_gate_passed':len(g)>=100 and len(relevant)>=500 and not errors,'limitations':['100 API search pages are not 100 repository audits','500 documentation pages are not 500 independent research studies','Detailed system comparisons are separate from per-page screening','No coding-quality or token-savings experiment on ilang has yet been run']}
(ROOT/'summary.json').write_text(json.dumps(summary,ensure_ascii=False,indent=2)+'\n',encoding='utf8')
(ROOT/'catalog.json').write_text(json.dumps(rows,ensure_ascii=False,indent=2)+'\n',encoding='utf8')
with (ROOT/'catalog.csv').open('w',encoding='utf-8-sig',newline='') as f:
 keys=['id','family','axis','status','title','url','final_url','characters','sha256','application','review_exclusion']
 w=csv.DictWriter(f,keys,extrasaction='ignore');w.writeheader();w.writerows(rows)
md=['# Реестр страниц ilang','', 'Карточки основаны на извлечении полного текста и тематическом скрининге. Углублённый сравнительный анализ — в SYNTHESIS.md. Сырой корпус хранится локально в raw; цитируем первичные публичные URL.','', '| ID | Тема | Источник | Применение / причина исключения |','|---|---|---|---|']
for r in rows:md.append(f"| {r['id']} | {r['axis']} | [{r['title'].replace('|','/')}](<{r['url']}>) | {r['application'] if r['status']=='screened_relevant' else 'Исключено: '+r['review_exclusion']} |")
(ROOT/'CATALOG.md').write_text('\n'.join(md)+'\n',encoding='utf8')
md=['# GitHub: 100 страниц поиска ilang','', 'REST Repository Search, до 30 результатов, сортировка по stars. Полные результаты находятся в github_pages.jsonl; SHA в исходном журнале. Широкие запросы содержат нерелевантные совпадения. Их наличие не означает, что такие репозитории рекомендованы.','', '| ID | Запрос и страница | Результатов | Первые кандидаты |','|---|---|---|---|']
for i,r in enumerate(g.values(),1):md.append(f"| G{i:03d} | [{r['query']}, {r['page']}](<{r['url']}>) | {len(r['items'])} | {', '.join(x['name'] for x in r['items'][:5])} |")
(ROOT/'GITHUB_SEARCH.md').write_text('\n'.join(md)+'\n',encoding='utf8')
with (ROOT/'github_repositories.csv').open('w',encoding='utf-8-sig',newline='') as f:
 w=csv.DictWriter(f,list(next(iter(repos.values()))));w.writeheader();w.writerows(sorted(repos.values(),key=lambda r:r['name'].lower()))
print(json.dumps(summary,ensure_ascii=False,indent=2))
