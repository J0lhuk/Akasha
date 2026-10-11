"""Public evidence collector; bounded/resumable, no private repos or secret extraction."""
from __future__ import annotations
import argparse, concurrent.futures, hashlib, json, re, subprocess, threading, time
from collections import deque
from datetime import datetime, timezone
from pathlib import Path
from urllib.parse import urljoin, urlsplit, urlunsplit, urlencode
from urllib.robotparser import RobotFileParser
import requests
from bs4 import BeautifulSoup
from sources import SOURCES, GITHUB_QUERIES

ROOT=Path(__file__).resolve().parent
RAW=ROOT/'raw'
LOCK=threading.Lock()
HOST_LOCKS={}
ROBOTS={}
UA='AkashaIlangResearch/0.1'
TERMS=re.compile(r'\b(prompt\w*|skill\w*|agent\w*|workflow\w*|plan\w*|compil\w*|language\w*|declar\w*|contract\w*|type\w*|constraint\w*|semantic\w*|intent\w*|verif\w*|evaluat\w*|schema\w*|gramma\w*|modul\w*|tool\w*|provenance|assert\w*|polic\w*|symbol\w*|proof\w*|effect\w*|state\w*|optimi\w*|budget\w*|token\w*)\b',re.I)
AXES={
 'meta':r'prompt|optim|signature|metric|example|demonstrat',
 'language':r'language|grammar|constraint|generat|prompt|syntax|structur',
 'workflow':r'workflow|task|agent|step|state|retry|checkpoint|parallel|determin',
 'skills':r'skill|tool|prompt|resource|capabil|protocol|version|permission',
 'types':r'type|contract|schema|validat|unif|constraint|config|merge',
 'compiler':r'grammar|parser|ast|syntax|semantic|scope|link|validat',
 'verification':r'proof|assert|constraint|invariant|verif|lemma|ensures|requires|model',
 'planning':r'plan|action|goal|effect|precondition|hierarch|problem|domain',
 'policy':r'polic|permit|forbid|authorization|principal|resource|schema|effect',
 'eval':r'eval|test|scor|metric|assert|dataset|sample|model|benchmark',
 'knowledge':r'provenance|ontology|entity|activit|graph|relation|semantic|constraint',
}

def stamp(): return datetime.now(timezone.utc).isoformat()
def digest(b): return hashlib.sha256(b).hexdigest()
def records(path):
 if not path.exists(): return []
 return [json.loads(x) for x in path.read_text(encoding='utf8').split('\n') if x.strip()]
def append(path,record):
 with LOCK:
  with path.open('a',encoding='utf8') as f:f.write(json.dumps(record,ensure_ascii=False)+'\n')
def canonical(url):
 p=urlsplit(url)
 return urlunsplit((p.scheme,p.netloc.lower(),p.path.rstrip('/') or '/', '', ''))

def github(target):
 folder=RAW/'github';folder.mkdir(parents=True,exist_ok=True)
 log=ROOT/'github_pages.jsonl'
 done={(r['query'],r['page']):r for r in records(log) if r.get('status')==200}
 good={k for k,r in done.items() if r.get('items') and not r.get('incomplete_results')}
 for page in (1,2,3):
  for query in GITHUB_QUERIES:
   if len(good)>=target:break
   if (query,page) in done:continue
   url='https://api.github.com/search/repositories?'+urlencode({'q':query+' is:public','per_page':30,'page':page,'sort':'stars','order':'desc'})
   for attempt in range(4):
    start=time.monotonic();rec={'query':query,'page':page,'per_page':30,'url':url,'fetched_at':stamp()}
    try:
     p=subprocess.run(['gh','api',url],capture_output=True,timeout=50)
     if p.returncode:raise RuntimeError(p.stderr.decode('utf8','replace')[:280])
     body=p.stdout;data=json.loads(body)
     if any(x.get('private') for x in data.get('items',[])):raise ValueError('private result rejected')
     rec.update(status=200,sha256=digest(body),total_count=data.get('total_count'),incomplete_results=data.get('incomplete_results'),items=[{'name':x['full_name'],'url':x['html_url'],'description':x.get('description'),'stars':x['stargazers_count'],'language':x.get('language'),'default_branch':x['default_branch'],'license':(x.get('license') or {}).get('spdx_id')} for x in data.get('items',[])])
     (folder/(rec['sha256']+'.json')).write_bytes(body)
     done[(query,page)]=rec
     if rec['items'] and not rec['incomplete_results']:good.add((query,page))
    except Exception as e:rec.update(status=0,error=str(e))
    append(log,rec)
    if rec['status']==200:
     print(json.dumps({'pages':len(good),'query':query,'page':page,'items':len(rec['items'])}),flush=True)
     time.sleep(max(0,2.25-(time.monotonic()-start)));break
    print(json.dumps({'retry':attempt+1,'query':query,'error':rec['error']}),flush=True)
    time.sleep(65)
  if len(good)>=target:break
 print(json.dumps({'github_gate':len(good)>=target,'pages':len(good)}),flush=True)

def get_robots(origin,session):
 with LOCK:
  mutex=HOST_LOCKS.setdefault(origin,threading.Lock())
 with mutex:
  if origin in ROBOTS:return ROBOTS[origin]
  parser=RobotFileParser();meta={'origin':origin,'fetched_at':stamp()}
  try:
   r=session.get(origin+'/robots.txt',timeout=(8,15));meta['status']=r.status_code
   if r.status_code==200:
    parser.parse(r.text.splitlines())
   elif r.status_code in (401,403,429):parser.disallow_all=True
   else:parser.allow_all=True
  except requests.RequestException:
   parser.disallow_all=True;meta['status']=0
  ROBOTS[origin]=parser
  append(ROOT/'robots.jsonl',meta)
  return parser

def web(families):
 folder=RAW/'web';folder.mkdir(parents=True,exist_ok=True)
 log=ROOT/'web_sources.jsonl'
 old=records(log)
 seen={r['url']:r for r in old}
 hashes={r['sha256'] for r in old if r.get('accepted')}
 finals={canonical(r['final_url']) for r in old if r.get('accepted')}
 def run(src):
  name,base,paths,pattern,limit,axis=src
  queue=deque(urljoin(base,p) for p in paths);local=set();count=0;attempted=0
  session=requests.Session();session.headers.update({'User-Agent':UA})
  robots=get_robots(base,session)
  while queue and count<limit and attempted<limit*4:
   url=canonical(queue.popleft())
   if url in local:continue
   local.add(url)
   with LOCK:previous=seen.get(url)
   if previous:
    if previous.get('family')==name and previous.get('accepted'):count+=1
    queue.extend(previous.get('links',[]));continue
   attempted+=1
   rec={'family':name,'axis':axis,'url':url,'fetched_at':stamp(),'accepted':False}
   if not robots.can_fetch(UA,url):
    rec.update(status=0,exclusion='robots_disallow');append(log,rec);continue
   try:
    with HOST_LOCKS[base]:
     with session.get(url,timeout=(8,25),stream=True) as response:
      rec.update(status=response.status_code,final_url=response.url)
      response.raise_for_status()
      if not any(t in response.headers.get('Content-Type','') for t in ('html','text/plain','markdown')):raise ValueError('non-text content')
      chunks=[];size=0
      for part in response.iter_content(65536):
       size+=len(part)
       if size>4_500_000:raise ValueError('page exceeds 4.5 MB bound')
       chunks.append(part)
      body=b''.join(chunks)
     time.sleep(max(.35,min(10,robots.crawl_delay(UA) or 0)))
    soup=BeautifulSoup(body,'lxml')
    links=[]
    for a in soup.select('a[href]'):
     link=canonical(urljoin(rec['final_url'],a.get('href','')));p=urlsplit(link)
     if p.scheme=='https' and p.netloc==urlsplit(base).netloc and re.search(pattern,p.path) and not re.search(r'\.(png|jpg|jpeg|svg|zip|pdf|gz|mp4|woff2?)$|/(tags|category|search|releases|api-reference|reference/api)/',p.path):links.append(link)
    rec['links']=list(dict.fromkeys(links));queue.extend(rec['links'])
    title=soup.title.get_text(' ',strip=True) if soup.title else url
    for tag in soup.select('script,style,nav,header,footer,aside,svg,noscript,form'):tag.decompose()
    main=soup.select_one('article') or soup.select_one('main') or soup.select_one('[role="main"]') or soup.body or soup
    headings=[h.get_text(' ',strip=True) for h in main.select('h1,h2,h3')][:35]
    text=re.sub(r'\n{3,}','\n\n',main.get_text('\n',strip=True)).strip()
    sha=digest(text.encode());fingerprint=digest(re.sub(r'\s+',' ',text).encode())
    terms=sorted(set(t.lower() for t in TERMS.findall(text)))
    # Evidence sentences retain links to complete local texts. Ranking is explicit, not a model review.
    paras=[re.sub(r'\s+',' ',x).strip() for x in text.splitlines()]
    evidence=sorted([x for x in paras if 70<len(x)<700],key=lambda x:(len(re.findall(AXES[axis],x,re.I)),len(x)),reverse=True)[:4]
    exclusion=''
    with LOCK:
     if sha in hashes or canonical(rec['final_url']) in finals:exclusion='duplicate_text_or_final_url'
     elif len(text)<850 or len(terms)<3:exclusion='short_or_low_relevance'
     elif re.search(r'page not found|access denied|just a moment|404 not found',title,re.I):exclusion='error_page'
     elif len(re.findall(AXES[axis],text,re.I))<6:exclusion='insufficient_topic_evidence'
     elif urlsplit(rec['final_url']).hostname in ('github.com','raw.githubusercontent.com'):exclusion='repository_page_not_web_document'
     else:hashes.add(sha);finals.add(canonical(rec['final_url']))
    rec.update(title=title,characters=len(text),sha256=sha,normalized_sha256=fingerprint,terms=terms,headings=headings,evidence=evidence,accepted=not exclusion,exclusion=exclusion)
    if not exclusion:
     (folder/(sha+'.txt')).write_text(text,encoding='utf8');count+=1
   except Exception as e:rec.setdefault('status',0);rec['exclusion']=str(e)[:230]
   with LOCK:seen[url]=rec
   append(log,rec)
  print(json.dumps({'family':name,'accepted':count,'attempted':attempted,'total':len(hashes)}),flush=True)
 selected=[s for s in SOURCES if not families or s[0] in families]
 with concurrent.futures.ThreadPoolExecutor(max_workers=5) as pool:list(pool.map(run,selected))
 print(json.dumps({'screened_texts':len(hashes),'preliminary_web_gate':len(hashes)>=500}),flush=True)

if __name__=='__main__':
 p=argparse.ArgumentParser();p.add_argument('mode',choices=['github','web']);p.add_argument('--target',type=int,default=100);p.add_argument('--families',nargs='*')
 a=p.parse_args()
 if a.mode=='github':github(a.target)
 else:web(a.families)
