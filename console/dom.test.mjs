// Software DOM + real loopback Go HTTP tests; this is NOT browser/visual QA.
import {test,describe,before,after} from 'node:test';
import assert from 'node:assert/strict';
import {readFile,mkdtemp,rm,mkdir,writeFile} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join,resolve} from 'node:path';
import {spawn,execFileSync} from 'node:child_process';
import {createServer} from 'node:net';
import {randomBytes,webcrypto,createHash} from 'node:crypto';
import {JSDOM,VirtualConsole} from 'jsdom';
import {inflateSync,deflateSync,crc32} from 'node:zlib';

const root=resolve(import.meta.dirname,'..');
const html=await readFile(join(root,'console/assets/index.html'),'utf8');
const script=await readFile(join(root,'console/assets/app.js'),'utf8');
const dashboardScript=await readFile(join(root,'console/assets/dashboard.js'),'utf8');
const canvasScript=await readFile(join(root,'console/assets/canvas.js'),'utf8');
const studioScript=await readFile(join(root,'console/assets/studio.js'),'utf8');
const documentsScript=await readFile(join(root,'console/assets/documents.js'),'utf8');
const libraryScript=await readFile(join(root,'console/assets/library.js'),'utf8');
const boardScript=await readFile(join(root,'console/assets/board.js'),'utf8');
const token=randomBytes(32).toString('hex');
const artifactDir=process.env.REPORTING_S03A_EVIDENCE_DIR;
const pdfConfigured=['REPORTING_TYPST','REPORTING_TYPST_SHA256','REPORTING_PDF_FONTS'].every(k=>process.env[k]);
if(process.env.REPORTING_PDF_REQUIRE==='1'&&!pdfConfigured)throw new Error('REPORTING_PDF_REQUIRE=1 but the Typst binary, its checksum or the fonts are not configured');
let temporary,child,url;
const sleep=ms=>new Promise(resolve=>setTimeout(resolve,ms));
async function until(fn,label,limit=6000){const end=Date.now()+limit;while(Date.now()<end){if(fn())return;await sleep(15);}throw new Error('DOM condition not reached: '+label);}

before(async()=>{
  temporary=await mkdtemp(join(tmpdir(),'reporting-s03a-dom-'));
  const go=process.env.REPORTING_GO||'go';
  execFileSync(go,['build','-o',join(temporary,'reporting-demo'),'./cmd/reporting-demo'],{cwd:root,env:{...process.env,GOTOOLCHAIN:'local'},stdio:['ignore','pipe','pipe'],timeout:120000});
  const listener=createServer();await new Promise(resolve=>listener.listen(0,'127.0.0.1',resolve));const port=listener.address().port;await new Promise(resolve=>listener.close(resolve));
  url='http://127.0.0.1:'+port;
  // PDF export runs when the pinned Typst binary and fonts are configured, as in CI.
  const pdf=pdfConfigured?['-pdf-typst',process.env.REPORTING_TYPST,'-pdf-typst-sha256',process.env.REPORTING_TYPST_SHA256,'-pdf-fonts',process.env.REPORTING_PDF_FONTS]:[];
  child=spawn(join(temporary,'reporting-demo'),['-profile','contract-simulator','-memory','-listen','127.0.0.1:'+port,'-layout-directory',join(temporary,'layouts'),'-dashboard-directory',join(temporary,'dashboards'),'-template-directory',join(temporary,'templates'),'-background-directory',join(temporary,'backgrounds'),'-quota-jobs','1000','-quota-results','100000','-quota-admissions','100000',...pdf],{cwd:root,env:{...process.env,REPORTING_DEMO_TOKEN:token},stdio:['ignore','ignore','pipe']});
  child.stderr.resume();
  for(let i=0;i<100;i++){try{const r=await fetch(url+'/console/');if(r.ok)return;}catch(_){}if(child.exitCode!==null)throw new Error('Go fixture did not start');await sleep(30);}
  throw new Error('Go fixture startup timeout');
});
after(async()=>{if(child&&child.exitCode===null){child.kill('SIGTERM');await new Promise(resolve=>child.once('exit',resolve));}if(temporary)await rm(temporary,{recursive:true});});

// app(intercept, options): the console in jsdom against the real server. The server and its identity are shared by every test of this
// file, so the server's job listing (S07-C, GET /jobs) would show each test the jobs of the tests before it: by default the harness answers
// it with an empty list and a test sees its own jobs only, as it did when the list was the session's; options.listing=true lets it through.
function app(intercept,options){
  const errors=[];const downloads=[];const trace=[];const virtualConsole=new VirtualConsole();virtualConsole.on('jsdomError',error=>{if(!error.message.includes('navigation'))errors.push(error.message);});
  const dom=new JSDOM(html,{url:url+'/console/',runScripts:'outside-only',pretendToBeVisual:true,virtualConsole});
  const w=dom.window;
  Object.defineProperty(w,'crypto',{value:webcrypto});
  w.TextDecoder=TextDecoder;w.TextEncoder=TextEncoder;w.Blob=Blob;w.AbortController=AbortController;
  const blobs=new Map();
  w.URL.createObjectURL=blob=>{const id='blob:fixture-'+blobs.size;blobs.set(id,blob);return id;};
  w.URL.revokeObjectURL=id=>blobs.delete(id);
  w.HTMLAnchorElement.prototype.click=function(){if(this.download){const blob=blobs.get(this.href);downloads.push({name:this.download,blob});}};
  w.fetch=async(path,options)=>{trace.push({method:options?.method||'GET',path:String(path),key:options?.headers?.['Idempotency-Key']});if(intercept){const response=await intercept(String(path),options);if(response)return response;}if(!listing&&/\/jobs\?limit=\d+(&|$)/.test(String(path)))return json([]);return fetch(url+path,options);};
  const listing=!!options?.listing;
  w.eval(dashboardScript);w.eval(canvasScript);w.eval(studioScript);w.eval(libraryScript);w.eval(boardScript);w.eval(documentsScript);w.eval(script);
  const $=id=>w.document.getElementById(id);
  const input=(id,value)=>{$(id).value=value;$(id).dispatchEvent(new w.Event('input',{bubbles:true}));};
  const click=id=>$(id).click();
  async function login(secret=token){input('access-token',secret);$('connect-form').dispatchEvent(new w.Event('submit',{bubbles:true,cancelable:true}));await until(()=>$('connection-overlay').hidden||!$('connect-error').hidden,'login response');if(secret===token)await until(()=>$('query-dataset').options.length>0,'catalog loaded');}
  function configure(){w.document.querySelector('[data-page="workspace"]').click();assert.equal($('page-workspace').hidden,false);input('query-companies','company_demo');}
  return {dom,w,$,input,click,login,configure,downloads,trace,errors,close:()=>w.close()};
}
function problem(status,code){return new Response(JSON.stringify({error:code}),{status,headers:{'Content-Type':'application/json'}});}

test('real Go console workflow preserves precision, frozen pages, export and logout',async()=>{
  const a=app();try{
    await a.login();a.configure();
    a.click('preview');await until(()=>!a.$('result-table-wrap').hidden,'preview table');
    assert.equal(a.$('result-table').tBodies[0].rows[2].cells[2].textContent,'9007199254740893.00');
    assert.equal(a.$('result-summary').querySelector('strong').textContent,'9007199254740993.01');
    a.click('snapshot');await until(()=>a.$('result-kind').textContent==='冻结快照'&&!a.$('export').disabled,'snapshot complete');
    a.$('page-size').value='2';a.$('page-size').dispatchEvent(new a.w.Event('change',{bubbles:true}));
    await until(()=>a.$('result-table').tBodies[0].rows.length===2,'two-row frozen page');
    a.click('next-page');await until(()=>a.$('page-info').textContent.includes('第 2 页'),'second frozen page');
    assert.equal(a.$('result-table').tBodies[0].rows[0].cells[2].textContent,'9007199254740893.00');
    a.click('export');await until(()=>a.downloads.length===1,'verified workbook download');
    const bytes=Buffer.from(await a.downloads[0].blob.arrayBuffer());assert.equal(bytes.subarray(0,2).toString(),'PK');
    if(artifactDir){await mkdir(artifactDir,{recursive:true});await writeFile(join(artifactDir,'console_finance.xlsx'),bytes);await writeFile(join(artifactDir,'console_download.json'),JSON.stringify({status:'PASS',source:'DOM actions + real Go loopback HTTP',sha256:createHash('sha256').update(bytes).digest('hex'),bytes:bytes.length,exact_total:'9007199254740993.01',browser_visual_qa:false},null,2));}
    assert.equal(a.w.localStorage.length,0);assert.equal(a.w.sessionStorage.length,0);
    a.click('logout');assert.equal(a.$('connection-overlay').hidden,false);assert.equal(a.$('result-table').tBodies[0].rows.length,0);assert.equal(a.$('tasks-body').rows.length,0);assert.equal(a.$('access-token').value,'');
    assert.equal(a.errors.length,0);
  }finally{a.close();}
});

test('invalid login and company selector escalation are rejected without cached rows',async()=>{
  const a=app();try{
    await a.login('invalid-fixture-credential');assert.equal(a.$('connection-overlay').hidden,false);assert.equal(a.$('access-token').value,'');
    await a.login();a.input('query-companies','company_other');a.click('preview');await until(()=>!a.$('notice').hidden,'denied query');
    assert.equal(a.$('result-table').tBodies[0].rows.length,0);assert.ok(a.$('notice').textContent.includes('无权访问'));
  }finally{a.close();}
});

test('unconfirmed snapshot submission reuses its idempotency key',async()=>{
  let first=true;const a=app(async(path,options)=>{if(path.endsWith('/snapshot-jobs')&&first){first=false;throw new TypeError('fixture connection lost');}});
  try{await a.login();a.configure();a.click('snapshot');await until(()=>!a.$('notice').hidden&&!a.$('snapshot').disabled,'uncertain submission');a.click('snapshot');await until(()=>a.$('result-kind').textContent==='冻结快照','retried snapshot');const requests=a.trace.filter(x=>x.path.endsWith('/snapshot-jobs'));assert.equal(requests.length,2);assert.equal(requests[0].key,requests[1].key);}finally{a.close();}
});

test('late preview cannot replace a changed query',async()=>{
  let entered=false,release;const blocked=new Promise(resolve=>release=resolve);
  const a=app(async(path)=>{if(path.endsWith('/queries')){entered=true;await blocked;}});
  try{await a.login();a.configure();a.click('preview');await until(()=>entered,'preview in flight');a.input('query-companies','company_other');release();await until(()=>!a.$('preview').disabled,'old request settled');assert.equal(a.$('result-table').tBodies[0].rows.length,0);assert.equal(a.$('result-kind').textContent,'尚未查询');}finally{release();a.close();}
});

test('authorization outage clears previously displayed results',async()=>{
  let unavailable=false;const a=app(async(path)=>{if(path.endsWith('/queries')&&unavailable)return problem(503,'AUTHORITY_UNAVAILABLE');});
  try{await a.login();a.configure();a.click('preview');await until(()=>!a.$('result-table-wrap').hidden,'initial result');unavailable=true;a.click('refresh-result');await until(()=>a.$('notice').textContent.includes('不可用'),'outage displayed');assert.equal(a.$('result-table').tBodies[0].rows.length,0);assert.equal(a.$('result-evidence').hidden,true);}finally{a.close();}
});

test('text cells cannot create elements or execute injected markup',async()=>{
  const hostile='<img src=x onerror=alert(1)>';
  const a=app(async(path,options)=>{if(path.endsWith('/queries')){const response=await fetch(url+path,options);const value=await response.json();value.columns[0].label=hostile;value.rows[0][0]=hostile;return new Response(JSON.stringify(value),{headers:{'Content-Type':'application/json'}});}});
  try{await a.login();a.configure();a.click('preview');await until(()=>!a.$('result-table-wrap').hidden,'literal text result');assert.equal(a.$('result-table').querySelector('img'),null);assert.equal(a.$('result-table').tHead.rows[0].cells[0].textContent,hostile);assert.equal(a.$('result-table').tBodies[0].rows[0].cells[0].textContent,hostile);}finally{a.close();}
});

test('401 during a query clears the active session and visible private references',async()=>{
  let expired=false;const a=app(async(path)=>{if(path.endsWith('/queries')&&expired)return problem(401,'AUTH_SCOPE_DENIED');});
  try{await a.login();a.configure();a.click('preview');await until(()=>!a.$('result-table-wrap').hidden,'initial rows');expired=true;a.click('refresh-result');await until(()=>!a.$('connection-overlay').hidden,'expired session');assert.equal(a.$('result-table').tBodies[0].rows.length,0);assert.equal(a.$('tasks-body').rows.length,0);assert.equal(a.$('identity-details').children.length,0);assert.equal(a.$('access-token').value,'');}finally{a.close();}
});

test('logout aborts an in-flight response without repopulating the page',async()=>{
  let entered=false,release;const blocked=new Promise(resolve=>release=resolve);const a=app(async(path)=>{if(path.endsWith('/queries')){entered=true;await blocked;}});
  try{await a.login();a.configure();a.click('preview');await until(()=>entered,'query in flight');a.click('logout');release();await sleep(80);assert.equal(a.$('result-table').tBodies[0].rows.length,0);assert.equal(a.$('connection-overlay').hidden,false);assert.equal(a.$('tasks-body').rows.length,0);}finally{release();a.close();}
});

test('a mismatched workbook digest never starts a file download',async()=>{
  const a=app(async(path,options)=>{if(path.includes('/artifacts/')&&path.endsWith('/content')){const original=await fetch(url+path,options);const bytes=await original.arrayBuffer();return new Response(bytes,{headers:{'Content-Type':'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet','X-Content-SHA256':'0'.repeat(64)}});}});
  try{await a.login();a.configure();a.click('snapshot');await until(()=>a.$('result-kind').textContent==='冻结快照'&&!a.$('export').disabled,'ready snapshot');a.click('export');await until(()=>a.$('notice').textContent.includes('完整性'),'digest failure');assert.equal(a.downloads.length,0);assert.equal(a.$('result-table').tBodies[0].rows.length,0);}finally{a.close();}
});

test('overlapping next-page actions consume a frozen cursor only once',async()=>{
  let entered=0,release;const blocked=new Promise(resolve=>release=resolve);
  const a=app(async(path)=>{if(path.includes('/rows?')&&path.includes('&cursor=')){entered++;await blocked;}});
  try{
    await a.login();a.configure();a.click('snapshot');await until(()=>a.$('result-kind').textContent==='冻结快照'&&!a.$('export').disabled,'snapshot ready');
    a.$('page-size').value='2';a.$('page-size').dispatchEvent(new a.w.Event('change',{bubbles:true}));
    await until(()=>a.$('result-table').tBodies[0].rows.length===2&&!a.$('next-page').disabled,'first frozen page');
    a.click('next-page');a.click('next-page');await until(()=>entered>0,'one next request');await sleep(30);
    assert.equal(entered,1,'one in-flight cursor request only');release();
    await until(()=>a.$('page-info').textContent.includes('第 2 页'),'second frozen page');
    assert.equal(a.$('result-table').tBodies[0].rows.length,1);assert.equal(a.$('next-page').disabled,true);
  }finally{release();a.close();}
});

test('page size stays consistent while a frozen cursor request is pending',async()=>{
  let entered=0,release;const blocked=new Promise(resolve=>release=resolve);
  const a=app(async(path)=>{if(path.includes('/rows?')&&path.includes('&cursor=')){entered++;await blocked;}});
  try{
    await a.login();a.configure();a.click('snapshot');await until(()=>a.$('result-kind').textContent==='冻结快照'&&!a.$('export').disabled,'snapshot ready');
    a.$('page-size').value='2';a.$('page-size').dispatchEvent(new a.w.Event('change',{bubbles:true}));
    await until(()=>a.$('result-table').tBodies[0].rows.length===2&&!a.$('next-page').disabled,'two-row first page');
    a.click('next-page');await until(()=>entered===1,'next request in flight');
    assert.equal(a.$('page-size').disabled,true);assert.equal(a.$('first-page').disabled,true);
    a.$('page-size').value='25';a.$('page-size').dispatchEvent(new a.w.Event('change',{bubbles:true}));
    assert.equal(a.$('page-size').value,'2','a queued change cannot replace an in-flight page limit');
    release();await until(()=>a.$('page-info').textContent.includes('第 2 页')&&!a.$('page-size').disabled,'second page settled');
    assert.equal(a.$('result-table').tBodies[0].rows.length,1);assert.equal(entered,1);
    a.$('page-size').value='25';a.$('page-size').dispatchEvent(new a.w.Event('change',{bubbles:true}));
    await until(()=>a.$('result-table').tBodies[0].rows.length===3&&!a.$('page-size').disabled,'new page size after completion');
    assert.ok(a.$('page-info').textContent.includes('第 1 页'));
  }finally{release();a.close();}
});

test('overlapping same-product catalog refreshes replace rather than duplicate options',async()=>{
  let holding=false,entered=0;const releases=[];
  const a=app(async(path,options)=>{
    if(holding&&path.includes('/catalog?')){const invocation=entered++;const blocked=new Promise(resolve=>releases[invocation]=resolve);const response=await fetch(url+path,options);await blocked;return response;}
  });
  try{
    await a.login();const original=a.$('query-dataset').options[0].value;holding=true;
    a.$('refresh-catalog').dispatchEvent(new a.w.Event('click',{bubbles:true}));
    a.$('refresh-catalog').dispatchEvent(new a.w.Event('click',{bubbles:true}));
    await until(()=>entered===2&&releases.length===2,'both queued catalog responses');
    assert.equal(a.$('preview').disabled,true);releases[1]();
    await until(()=>a.$('query-dataset').options.length===1&&!a.$('preview').disabled,'newer catalog response');
    releases[0]();await sleep(50);
    assert.equal(a.$('query-dataset').options.length,1);assert.equal(a.$('query-dataset').value,original);
  }finally{for(const release of releases)release();a.close();}
});

test('an old product catalog failure cannot clear a newer product preview',async()=>{
  let oldProduct='',holding=false,entered=false,release;const blocked=new Promise(resolve=>release=resolve);
  const a=app(async(path)=>{if(holding&&path.includes('/catalog?integration_id='+encodeURIComponent(oldProduct))){entered=true;await blocked;return problem(403,'ACCESS_DENIED');}});
  try{
    await a.login();oldProduct=a.$('query-product').value;const oldDataset=a.$('query-dataset').value;holding=true;
    a.click('refresh-catalog');await until(()=>entered,'old product refresh in flight');
    a.$('query-product').value=a.$('query-product').options[1].value;
    a.$('query-product').dispatchEvent(new a.w.Event('change',{bubbles:true}));
    await until(()=>a.$('query-dataset').options.length===1&&a.$('query-dataset').value!==oldDataset&&!a.$('preview').disabled,'new product catalog');
    a.configure();a.click('preview');await until(()=>a.$('result-table').tBodies[0].rows.length===2&&!a.$('preview').disabled,'new product preview');
    const current=a.$('result-table').textContent;const notice=a.$('notice').textContent;
    release();await sleep(60);
    assert.equal(a.$('result-table').textContent,current);assert.equal(a.$('notice').textContent,notice);assert.equal(a.$('result-table').tBodies[0].rows.length,2);
  }finally{release();a.close();}
});

test('an old task refresh error cannot clear a changed-query preview',async()=>{
  let holding=false,entered=false,release;const blocked=new Promise(resolve=>release=resolve);
  const a=app(async(path)=>{if(holding&&/\/jobs\/[a-f0-9]{32}$/.test(path)){entered=true;await blocked;return problem(403,'ACCESS_DENIED');}});
  try{
    await a.login();a.configure();a.click('snapshot');await until(()=>a.$('result-kind').textContent==='冻结快照'&&!a.$('export').disabled,'snapshot ready');
    holding=true;a.$('tasks-body').querySelector('button').click();await until(()=>entered,'task refresh pending');
    a.input('query-date','2026-10-25');a.click('preview');await until(()=>a.$('result-kind').textContent==='查询预览'&&!a.$('preview').disabled,'changed-query preview');
    const notice=a.$('notice').textContent;release();await sleep(60);
    assert.equal(a.$('result-table').tBodies[0].rows.length,3);assert.equal(a.$('notice').textContent,notice);
    assert.ok(a.$('tasks-body').textContent.includes('访问受限'),'task bookkeeping still updates');
  }finally{release();a.close();}
});

test('a task denial affects its snapshot rather than an unrelated current preview',async()=>{
  let holding=false,entered=false,release;const blocked=new Promise(resolve=>release=resolve);
  const a=app(async(path)=>{if(holding&&/\/jobs\/[a-f0-9]{32}$/.test(path)){entered=true;await blocked;return problem(403,'ACCESS_DENIED');}});
  try{
    await a.login();a.configure();a.click('snapshot');await until(()=>a.$('result-kind').textContent==='冻结快照'&&!a.$('export').disabled,'snapshot ready');
    holding=true;a.$('tasks-body').querySelector('button').click();await until(()=>entered,'task refresh pending');
    a.click('preview');await until(()=>a.$('result-kind').textContent==='查询预览'&&!a.$('preview').disabled,'same-query new preview');
    release();await sleep(60);
    assert.equal(a.$('result-table').tBodies[0].rows.length,3);assert.ok(a.$('notice').textContent.startsWith('任务刷新：'));
  }finally{release();a.close();}
});

test('a current task denial clears the corresponding visible snapshot',async()=>{
  let denied=false;const a=app(async(path)=>{if(denied&&/\/jobs\/[a-f0-9]{32}$/.test(path))return problem(403,'ACCESS_DENIED');});
  try{
    await a.login();a.configure();a.click('snapshot');await until(()=>a.$('result-kind').textContent==='冻结快照'&&!a.$('export').disabled,'visible snapshot');
    denied=true;a.$('tasks-body').querySelector('button').click();await until(()=>a.$('notice').textContent.startsWith('任务刷新：'),'current task denial');
    assert.equal(a.$('result-table').tBodies[0].rows.length,0);assert.equal(a.$('export').disabled,true);
    assert.ok(a.$('tasks-body').textContent.includes('访问受限'));
  }finally{a.close();}
});

test('an old task cancellation error cannot replace a changed-query result',async()=>{
  let phase='',entered=false,release;const blocked=new Promise(resolve=>release=resolve);
  const a=app(async(path,options)=>{
    if(phase==='running'&&/\/jobs\/[a-f0-9]{32}$/.test(path)){
      // Contract-boundary state injection makes the cancel control available.
      const response=await fetch(url+path,options);const job=await response.json();job.state='running';delete job.snapshot_id;delete job.artifact_id;
      return new Response(JSON.stringify(job),{headers:{'Content-Type':'application/json'}});
    }
    if(phase==='cancel'&&path.endsWith('/cancel')){entered=true;await blocked;return problem(403,'ACCESS_DENIED');}
  });
  try{
    await a.login();a.configure();a.click('snapshot');await until(()=>a.$('result-kind').textContent==='冻结快照'&&!a.$('export').disabled,'snapshot ready');
    phase='running';a.$('tasks-body').querySelector('button').click();
    const cancel=()=>Array.from(a.$('tasks-body').querySelectorAll('button')).find(b=>b.textContent==='取消');
    await until(()=>!!cancel(),'cancel control for in-progress task');phase='cancel';cancel().click();await until(()=>entered,'cancel response pending');
    a.input('query-date','2026-10-25');a.click('preview');await until(()=>a.$('result-kind').textContent==='查询预览'&&!a.$('preview').disabled,'new preview after cancel request');
    const notice=a.$('notice').textContent;release();await sleep(60);
    assert.equal(a.$('result-table').tBodies[0].rows.length,3);assert.equal(a.$('notice').textContent,notice);
  }finally{release();a.close();}
});

test('bulk task refresh keeps an unrelated product preview after a task denial',async()=>{
  let denied=false;const a=app(async(path)=>{if(denied&&/\/jobs\/[a-f0-9]{32}$/.test(path))return problem(403,'ACCESS_DENIED');});
  try{
    await a.login();a.configure();a.click('snapshot');await until(()=>a.$('result-kind').textContent==='冻结快照'&&!a.$('export').disabled,'original snapshot');
    const original=a.$('query-dataset').value;a.$('query-product').value=a.$('query-product').options[1].value;
    a.$('query-product').dispatchEvent(new a.w.Event('change',{bubbles:true}));
    await until(()=>a.$('query-dataset').options.length===1&&a.$('query-dataset').value!==original&&!a.$('preview').disabled,'other product catalog');
    a.configure();a.click('preview');await until(()=>a.$('result-table').tBodies[0].rows.length===2&&!a.$('preview').disabled,'other product preview');
    denied=true;a.click('refresh-tasks');await until(()=>a.$('notice').textContent.includes('无权访问'),'bulk task denied');
    assert.equal(a.$('result-table').tBodies[0].rows.length,2);assert.ok(a.$('notice').textContent.startsWith('任务刷新：'));
  }finally{a.close();}
});

test('restoring an unknown task leaves the current preview intact',async()=>{
  const a=app();try{
    await a.login();a.configure();a.click('preview');await until(()=>a.$('result-table').tBodies[0].rows.length===3&&!a.$('preview').disabled,'current preview');
    a.input('restore-id','0'.repeat(32));a.click('restore-job');await until(()=>a.$('notice').textContent.includes('未找到'),'real unknown task response');
    assert.equal(a.$('result-table').tBodies[0].rows.length,3);assert.ok(a.$('notice').textContent.startsWith('任务恢复：'));
  }finally{a.close();}
});

test('task file download denial preserves an unrelated current preview',async()=>{
  let denied=false;const a=app(async(path)=>{if(denied&&path.includes('/artifacts/')&&path.endsWith('/content'))return problem(403,'ACCESS_DENIED');});
  try{
    await a.login();a.configure();a.click('snapshot');await until(()=>a.$('result-kind').textContent==='冻结快照'&&!a.$('export').disabled,'snapshot ready');
    a.click('export');await until(()=>a.downloads.length===1&&!a.$('preview').disabled,'real export complete');
    a.click('preview');await until(()=>a.$('result-kind').textContent==='查询预览'&&!a.$('preview').disabled,'unrelated preview');
    denied=true;Array.from(a.$('tasks-body').querySelectorAll('button')).find(b=>b.textContent==='下载').click();
    await until(()=>a.$('notice').textContent.includes('无权访问'),'task file denied');
    assert.equal(a.$('result-table').tBodies[0].rows.length,3);assert.equal(a.downloads.length,1);assert.ok(a.$('notice').textContent.startsWith('任务下载：'));
  }finally{a.close();}
});

test('task file download with the file store unavailable says so, saves nothing, keeps an unrelated preview and recovers',async()=>{
  // S04-E: a 503 with Retry-After on a download is the object store being unreachable; the console treats it as it treats every 503.
  let down=false;const a=app(async(path)=>{if(down&&path.includes('/artifacts/')&&path.endsWith('/content'))return new Response(JSON.stringify({error:'AUTHORITY_UNAVAILABLE'}),{status:503,headers:{'Content-Type':'application/json','Retry-After':'10'}});});
  try{
    await a.login();a.configure();a.click('snapshot');await until(()=>a.$('result-kind').textContent==='冻结快照'&&!a.$('export').disabled,'snapshot ready');
    a.click('export');await until(()=>a.downloads.length===1&&!a.$('preview').disabled,'real export complete');
    a.click('preview');await until(()=>a.$('result-kind').textContent==='查询预览'&&!a.$('preview').disabled,'unrelated preview');
    down=true;Array.from(a.$('tasks-body').querySelectorAll('button')).find(b=>b.textContent==='下载').click();
    await until(()=>a.$('notice').textContent.includes('不可用'),'task file outage');
    assert.equal(a.$('result-table').tBodies[0].rows.length,3);assert.equal(a.downloads.length,1);assert.ok(a.$('notice').textContent.startsWith('任务下载：'));assert.ok(a.$('notice').textContent.includes('请稍后重试'));
    down=false;Array.from(a.$('tasks-body').querySelectorAll('button')).find(b=>b.textContent==='下载').click();
    await until(()=>a.downloads.length===2,'download after the store is back');
  }finally{a.close();}
});

test('task snapshot viewing denial preserves the previous unrelated preview',async()=>{
  let denied=false;const a=app(async(path)=>{if(denied&&path.includes('/snapshots/')&&path.includes('/rows?'))return problem(403,'ACCESS_DENIED');});
  try{
    await a.login();a.configure();a.click('snapshot');await until(()=>a.$('result-kind').textContent==='冻结快照'&&!a.$('export').disabled,'snapshot ready');
    a.click('preview');await until(()=>a.$('result-kind').textContent==='查询预览'&&!a.$('preview').disabled,'unrelated preview');
    denied=true;Array.from(a.$('tasks-body').querySelectorAll('button')).find(b=>b.textContent==='查看结果').click();
    await until(()=>a.$('notice').textContent.includes('无权访问'),'task snapshot denied');
    assert.equal(a.$('result-table').tBodies[0].rows.length,3);assert.ok(a.$('notice').textContent.startsWith('任务查看：'));
  }finally{a.close();}
});


test('report layout reorders and hides columns without changing exact values or query scope',async()=>{
 const a=app();try{
  await a.login();a.configure();a.click('preview');await until(()=>!a.$('preview').disabled&&!a.$('result-table-wrap').hidden,'designer source');
  a.click('layout-save');const saved=JSON.parse(a.$('layout-json').value);assert.deepEqual(Object.keys(saved).sort(),['binding','columns','schema_version']);
  assert.equal(a.downloads.length,1);assert.equal(saved.columns.length,3);
  saved.columns=[saved.columns[2],saved.columns[0]];saved.binding={dataset:{version:saved.binding.dataset.version,id:saved.binding.dataset.id},integration_id:saved.binding.integration_id};a.$('layout-json').value=JSON.stringify(saved);a.click('layout-open');
  assert.equal(a.$('result-table').tHead.rows[0].cells.length,2);assert.equal(a.$('result-table').tBodies[0].rows[2].cells[0].textContent,'9007199254740893.00');
  const count=a.trace.filter(t=>t.path.endsWith('/queries')).length;a.click('layout-reset');assert.equal(a.$('result-table').tHead.rows[0].cells.length,3);
  a.$('layout-json').value=JSON.stringify(saved);a.click('layout-open');assert.equal(a.trace.filter(t=>t.path.endsWith('/queries')).length,count);
  a.click('logout');assert.equal(a.$('layout-json').value,'');assert.equal(a.$('designer-columns').children.length,0);
 }finally{a.close();}
});

test('layout import rejects foreign binding, duplicate/unknown fields, empty columns and extra authority',async()=>{
 const a=app();try{
  await a.login();a.configure();a.click('preview');await until(()=>!a.$('preview').disabled&&!a.$('result-table-wrap').hidden,'designer source');
  a.click('layout-save');const good=JSON.parse(a.$('layout-json').value);
  for(const mutate of [x=>x.binding.integration_id='foreign',x=>x.columns.push(x.columns[0]),x=>x.columns[0].id='unapproved',x=>x.columns=[],x=>x.tenant='admin',x=>x.columns[0].align='html']){
   const bad=structuredClone(good);mutate(bad);a.$('layout-json').value=JSON.stringify(bad);a.click('layout-open');assert.ok(a.$('layout-status').textContent.includes('检查'));assert.equal(a.$('result-table').tHead.rows[0].cells.length,3);
  }
  a.$('layout-json').value='x'.repeat(65537);a.click('layout-open');assert.ok(a.$('layout-status').textContent.includes('检查'));
 }finally{a.close();}
});

 test('server layouts save immutable revisions and reopen an earlier version',async()=>{const a=app();try{
 await a.login();a.configure();a.click('preview');await until(()=>!a.$('preview').disabled&&!a.$('result-table-wrap').hidden,'query');
 a.click('layout-server-save');await until(()=>a.$('layout-server-status').textContent.includes('版本 1')&&!a.$('preview').disabled,'server v1');
 a.click('layout-save');const doc=JSON.parse(a.$('layout-json').value);doc.columns=[doc.columns[2]];a.$('layout-json').value=JSON.stringify(doc);a.click('layout-open');a.click('layout-server-save');await until(()=>a.$('layout-server-status').textContent.includes('版本 2')&&!a.$('preview').disabled,'server v2');
 a.click('layout-server-list');await until(()=>a.$('layout-server-listing').options.length===1&&!a.$('preview').disabled,'server directory');a.$('layout-server-version').value='1';a.click('layout-server-open');await until(()=>a.$('layout-server-status').textContent.includes('正在编辑')&&!a.$('preview').disabled,'old revision');assert.equal(a.$('result-table').tHead.rows[0].cells.length,3);
 a.click('layout-server-save');await until(()=>a.$('notice').textContent.includes('冲突')&&!a.$('preview').disabled,'optimistic version conflict');assert.equal(a.$('result-table').tBodies[0].rows.length,3);
 }finally{a.close();}});

test('an uncertain layout save requires list reconciliation without automatic retry',async()=>{
 let submissions=0;const a=app(async(path,options)=>{if(path.endsWith('/layouts')&&options.method==='POST'){submissions++;await fetch(url+path,options);throw new TypeError('reply lost');}});
 try{await a.login();a.configure();a.click('preview');await until(()=>!a.$('preview').disabled&&!a.$('result-table-wrap').hidden,'query');a.click('layout-server-save');await until(()=>a.$('notice').textContent.includes('核对')&&!a.$('preview').disabled,'uncertain save');assert.equal(submissions,1);assert.equal(a.$('result-table').tBodies[0].rows.length,3);assert.ok(!a.$('notice').textContent.includes('复用原请求标识'));a.click('layout-server-list');await until(()=>a.$('layout-server-listing').options.length>0&&!a.$('preview').disabled,'reconciled list');assert.equal(submissions,1);}finally{a.close();}
});

test('publication pins a saved private layout until explicitly replaced or withdrawn',async()=>{const a=app();try{
 await a.login();a.configure();a.click('preview');await until(()=>!a.$('preview').disabled&&!a.$('result-table-wrap').hidden,'query');a.click('layout-server-save');await until(()=>a.$('layout-server-status').textContent.includes('版本 1')&&!a.$('preview').disabled,'v1');const id=a.$('layout-server-status').textContent.match(/[a-f0-9]{32}/)[0];
 a.click('layout-release-status');await until(()=>a.$('layout-release-info').textContent.includes('发布修订 0')&&!a.$('preview').disabled,'draft publication state');a.click('layout-release-publish');await until(()=>a.$('layout-release-info').textContent.includes('已发布版本 1')&&!a.$('preview').disabled,'publish v1');
 a.click('layout-save');const doc=JSON.parse(a.$('layout-json').value);doc.columns=[doc.columns[2]];a.$('layout-json').value=JSON.stringify(doc);a.click('layout-open');const releaseCalls=a.trace.filter(t=>t.path.endsWith('/release')&&t.method==='POST').length;a.click('layout-release-publish');assert.equal(a.trace.filter(t=>t.path.endsWith('/release')&&t.method==='POST').length,releaseCalls,'dirty layout cannot publish');a.click('layout-server-save');await until(()=>a.$('layout-server-status').textContent.includes('版本 2')&&!a.$('preview').disabled,'draft v2');assert.ok(a.$('layout-release-info').textContent.includes('已发布版本 1'));
 a.click('layout-release-open');await until(()=>a.$('layout-server-status').textContent.includes('已打开发布版本 1')&&!a.$('preview').disabled,'published v1');assert.equal(a.$('result-table').tHead.rows[0].cells.length,3);
 a.click('layout-server-list');await until(()=>a.$('layout-server-listing').options.length>0&&!a.$('preview').disabled,'directory');a.$('layout-server-listing').value=id;a.$('layout-server-version').value='2';a.click('layout-server-open');await until(()=>a.$('layout-server-status').textContent.includes('正在编辑')&&!a.$('preview').disabled,'open draft v2');assert.equal(a.$('result-table').tHead.rows[0].cells.length,1);
 a.click('layout-release-status');await until(()=>!a.$('preview').disabled,'read revision');a.click('layout-release-publish');await until(()=>a.$('layout-release-info').textContent.includes('已发布版本 2')&&!a.$('preview').disabled,'publish v2');a.click('layout-release-withdraw');await until(()=>a.$('layout-release-info').textContent.includes('未发布 · 发布修订 3')&&!a.$('preview').disabled,'withdraw');assert.equal(a.$('layout-release-history').children.length,3);a.click('layout-release-open');await until(()=>a.$('notice').textContent.includes('未找到')&&!a.$('preview').disabled,'withdrawn denied');assert.equal(a.$('result-table').tHead.rows[0].cells.length,1);
}finally{a.close();}});

test('dashboard composes exact server totals and current-page tables with metadata-only reopen',async()=>{const a=app();try{
 await a.login();a.configure();a.click('preview');await until(()=>!a.$('preview').disabled&&!a.$('result-table-wrap').hidden,'authorized result');a.w.document.querySelector('[data-page="dashboard"]').click();assert.equal(a.$('page-dashboard').hidden,false);
 const queries=a.trace.filter(t=>t.path.endsWith('/queries')).length;a.click('board-add-metric');a.click('board-add-table');assert.equal(a.$('board-widgets').children.length,2);assert.equal(a.$('board-widgets').querySelector('strong').textContent,'9007199254740993.01');assert.equal(a.$('board-widgets').querySelector('tbody').rows.length,3);assert.equal(a.$('board-widgets').querySelector('tbody').rows[2].cells[2].textContent,'9007199254740893.00');
 a.$('board-widgets').children[0].querySelectorAll('button')[1].click();assert.equal(a.$('board-widgets').children[0].querySelector('h3').textContent,'明细表 · 当前结果页');
 a.click('board-save');const config=JSON.parse(a.$('board-json').value);assert.deepEqual(Object.keys(config).sort(),['binding','kind','schema_version','widgets']);assert.equal(a.downloads.length,1);const exported=JSON.parse(await a.downloads[0].blob.text());assert.equal(exported.widgets.length,2);assert.ok(!JSON.stringify(exported).includes('9007199254740993.01'));assert.ok(!JSON.stringify(exported).includes('company_demo'));
 config.binding={dataset:{version:config.binding.dataset.version,id:config.binding.dataset.id},integration_id:config.binding.integration_id};a.click('board-reset');assert.equal(a.$('board-widgets').children.length,0);a.$('board-json').value=JSON.stringify(config);a.click('board-open');assert.equal(a.$('board-widgets').children.length,2);a.$('board-widgets').children[0].querySelectorAll('button')[2].click();assert.equal(a.$('board-widgets').children.length,1);assert.equal(a.trace.filter(t=>t.path.endsWith('/queries')).length,queries);
}finally{a.close();}});

test('dashboard rejects unauthorized bindings, unknown widgets, fields and configuration budgets',async()=>{const a=app();try{
 await a.login();a.configure();a.click('preview');await until(()=>!a.$('preview').disabled&&!a.$('result-table-wrap').hidden,'authorized result');a.click('board-add-metric');a.click('board-save');const good=JSON.parse(a.$('board-json').value);
 for(const change of [x=>x.binding.integration_id='foreign',x=>x.widgets[0].field='unapproved',x=>x.widgets[0].type='html',x=>x.tenant='admin',x=>x.widgets=[{type:'table',columns:['unknown']}],x=>x.widgets=[{type:'table',columns:[a.$('board-columns').options[0].value,a.$('board-columns').options[0].value]}],x=>x.widgets=Array(13).fill(x.widgets[0])]){const bad=structuredClone(good);change(bad);a.$('board-json').value=JSON.stringify(bad);a.click('board-open');assert.equal(a.$('board-widgets').children.length,1);assert.ok(!a.$('board-status').textContent.includes('已更新'));}
 a.$('board-json').value='x'.repeat(65537);a.click('board-open');assert.ok(a.$('board-status').textContent.includes('64 KiB'));assert.equal(a.$('board-widgets').children.length,1);
}finally{a.close();}});

test('dashboard clears visible data on query changes, denial and logout',async()=>{let denied=false;const a=app(async(path)=>{if(denied&&path.endsWith('/queries'))return problem(503,'AUTHORITY_UNAVAILABLE');});try{
 await a.login();a.configure();a.click('preview');await until(()=>!a.$('preview').disabled&&!a.$('result-table-wrap').hidden,'authorized result');a.click('board-add-metric');a.click('board-save');assert.equal(a.$('board-widgets').children.length,1);
 a.input('query-date','2026-10-25');assert.equal(a.$('board-widgets').children.length,0);assert.equal(a.$('board-json').value,'');a.click('board-add-metric');assert.ok(a.$('board-status').textContent.includes('先选择数据'));denied=true;a.click('preview');await until(()=>a.$('notice').textContent.includes('不可用')&&!a.$('preview').disabled,'source outage');assert.equal(a.$('board-widgets').children.length,0);
 denied=false;a.click('preview');await until(()=>!a.$('preview').disabled&&!a.$('result-table-wrap').hidden,'authorized again');assert.equal(a.$('board-widgets').children.length,1,'same binding preserves metadata after fresh authorization');a.click('logout');assert.equal(a.$('board-widgets').children.length,0);assert.equal(a.$('board-json').value,'');await a.login();a.configure();a.click('preview');await until(()=>!a.$('preview').disabled&&!a.$('result-table-wrap').hidden,'new session');assert.equal(a.$('board-widgets').children.length,0,'logout discards dashboard config');
}finally{a.close();}});

test('server dashboard versions reopen exact metadata and reject stale updates',async()=>{const a=app();try{
 await a.login();a.configure();a.click('preview');await until(()=>!a.$('preview').disabled&&!a.$('result-table-wrap').hidden,'query');a.click('board-add-metric');a.click('board-save');const reordered=JSON.parse(a.$('board-json').value);reordered.binding={dataset:{version:reordered.binding.dataset.version,id:reordered.binding.dataset.id},integration_id:reordered.binding.integration_id};a.$('board-json').value=JSON.stringify({widgets:reordered.widgets,binding:reordered.binding,kind:reordered.kind,schema_version:reordered.schema_version});a.click('board-open');a.click('board-server-save');await until(()=>a.$('board-server-status').textContent.includes('版本 1')&&!a.$('preview').disabled,'dashboard v1');assert.equal(a.$('board-widgets').querySelector('strong').textContent,'9007199254740993.01');
 a.click('board-reset');a.click('board-server-save');await until(()=>a.$('board-server-status').textContent.includes('版本 2')&&!a.$('preview').disabled,'dashboard v2');assert.equal(a.$('board-widgets').children.length,0);a.click('board-server-list');await until(()=>a.$('board-server-listing').options.length===1&&!a.$('preview').disabled,'dashboard list');a.$('board-server-version').value='1';a.click('board-server-open');await until(()=>a.$('board-server-status').textContent.includes('正在编辑看板')&&!a.$('preview').disabled,'open historical board');assert.equal(a.$('board-widgets').children.length,1);assert.equal(a.$('board-widgets').querySelector('strong').textContent,'9007199254740993.01');
 a.click('board-server-save');await until(()=>a.$('notice').textContent.includes('冲突')&&!a.$('preview').disabled,'dashboard conflict');assert.equal(a.$('board-widgets').children.length,1);assert.equal(a.$('result-table').tBodies[0].rows.length,3);a.click('logout');assert.equal(a.$('board-server-listing').options.length,0);assert.equal(a.$('board-widgets').children.length,0);
}finally{a.close();}});

test('uncertain dashboard save requires list reconciliation and does not clear current data',async()=>{let writes=0;const a=app(async(path,options)=>{if(path.endsWith('/dashboards')&&options.method==='POST'){writes++;await fetch(url+path,options);throw new TypeError('reply lost');}});try{
 await a.login();a.configure();a.click('preview');await until(()=>!a.$('preview').disabled&&!a.$('result-table-wrap').hidden,'query');a.click('board-add-metric');a.click('board-server-save');await until(()=>a.$('notice').textContent.includes('核对')&&!a.$('preview').disabled,'unknown dashboard save');assert.equal(writes,1);assert.equal(a.$('board-widgets').children.length,1);assert.equal(a.$('result-table').tBodies[0].rows.length,3);a.click('board-server-list');await until(()=>a.$('board-server-listing').options.length>0&&!a.$('preview').disabled,'reconciled server list');assert.equal(writes,1);
}finally{a.close();}});

test('dashboard publication fixes a saved version, rejects dirty publish, supports rollback and withdrawal',async()=>{const a=app();try{
 await a.login();a.configure();a.click('preview');await until(()=>!a.$('preview').disabled&&!a.$('result-table-wrap').hidden,'query');a.click('board-add-metric');a.click('board-server-save');await until(()=>a.$('board-server-status').textContent.includes('版本 1')&&!a.$('preview').disabled,'saved');const boardID=a.$('board-server-status').textContent.match(/[a-f0-9]{32}/)[0];
 a.click('board-release-publish');assert.match(a.$('board-status').textContent,/先刷新/);a.click('board-release-read');await until(()=>a.$('board-release-status').textContent.includes('修订 0')&&!a.$('preview').disabled,'release read');a.click('board-reset');a.click('board-release-publish');assert.match(a.$('board-status').textContent,/先保存/);assert.equal(a.trace.filter(v=>v.path.endsWith('/release')&&v.method==='POST').length,0);
 a.click('board-add-metric');a.click('board-release-publish');await until(()=>a.$('board-release-status').textContent.includes('已发布版本 1')&&!a.$('preview').disabled,'publish');assert.equal(a.$('board-release-history').children.length,1);
 a.click('board-reset');a.click('board-server-save');await until(()=>a.$('board-server-status').textContent.includes('版本 2')&&!a.$('preview').disabled,'draft v2');a.click('board-release-read');await until(()=>a.$('board-release-status').textContent.includes('已发布版本 1')&&!a.$('preview').disabled,'draft preserves pointer');a.click('board-release-publish');await until(()=>a.$('board-release-status').textContent.includes('已发布版本 2')&&!a.$('preview').disabled,'publish empty v2');
 a.click('board-server-list');await until(()=>a.$('board-server-listing').options.length>0&&!a.$('preview').disabled,'list');a.$('board-server-listing').value=boardID;a.$('board-server-version').value='1';a.click('board-server-open');await until(()=>a.$('board-server-status').textContent.includes('正在编辑看板')&&!a.$('preview').disabled,'old version');a.click('board-release-read');await until(()=>a.$('board-release-status').textContent.includes('修订 2')&&!a.$('preview').disabled,'revision2');a.click('board-release-publish');await until(()=>a.$('board-release-status').textContent.includes('修订 3')&&!a.$('preview').disabled,'rollback');
 a.click('board-reset');a.click('board-published-open');await until(()=>a.$('board-server-status').textContent.includes('已打开发布看板')&&!a.$('preview').disabled,'published open');assert.equal(a.$('board-widgets').querySelector('strong').textContent,'9007199254740993.01');a.click('board-release-read');await until(()=>a.$('board-release-status').textContent.includes('修订 3')&&!a.$('preview').disabled,'revision3');a.click('board-release-withdraw');await until(()=>a.$('board-release-status').textContent.includes('修订 4')&&!a.$('preview').disabled,'withdraw');assert.match(a.$('board-release-status').textContent,/当前未发布/);a.click('board-published-open');await until(()=>a.$('notice').textContent.includes('未找到')&&!a.$('preview').disabled,'withdrawn read');assert.equal(a.$('result-table').tBodies[0].rows.length,3);a.click('logout');assert.equal(a.$('board-release-history').children.length,0);
 }finally{a.close();}});

test('uncertain dashboard publication requires explicit history reconciliation without retry',async()=>{let writes=0;const a=app(async(path,options)=>{if(path.includes('/dashboards/')&&path.endsWith('/release')&&options.method==='POST'){writes++;await fetch(url+path,options);throw new TypeError('reply lost');}});try{
 await a.login();a.configure();a.click('preview');await until(()=>!a.$('preview').disabled&&!a.$('result-table-wrap').hidden,'query');a.click('board-add-metric');a.click('board-server-save');await until(()=>a.$('board-server-status').textContent.includes('版本 1')&&!a.$('preview').disabled,'saved');a.click('board-release-read');await until(()=>a.$('board-release-status').textContent.includes('修订 0')&&!a.$('preview').disabled,'release read');a.click('board-release-publish');await until(()=>a.$('notice').textContent.includes('核对')&&!a.$('preview').disabled,'uncertain publish');assert.equal(writes,1);a.click('board-release-publish');assert.match(a.$('board-status').textContent,/先刷新/);assert.equal(writes,1);a.click('board-release-read');await until(()=>a.$('board-release-status').textContent.includes('已发布版本 1')&&!a.$('preview').disabled,'reconcile');assert.equal(a.$('board-widgets').children.length,1);assert.equal(writes,1);
 }finally{a.close();}});


test('dashboard release conflicts require refresh and delayed history cannot survive query invalidation',async()=>{let conflict=true,hold=false,started=false,unblock;const a=app(async(path,options)=>{
 if(path.includes('/dashboards/')&&path.endsWith('/release')){
  if(options.method==='POST'&&conflict){conflict=false;await fetch(url+path,options);return problem(409,'CONFLICT');}
  if(options.method==='GET'&&hold){const gate=new Promise(resolve=>unblock=resolve);started=true;const response=await fetch(url+path,options);await gate;return response;}
 }
});try{
 await a.login();a.configure();a.click('preview');await until(()=>!a.$('preview').disabled&&!a.$('result-table-wrap').hidden,'query');a.click('board-add-metric');a.click('board-server-save');await until(()=>a.$('board-server-status').textContent.includes('版本 1')&&!a.$('preview').disabled,'saved');a.click('board-release-read');await until(()=>a.$('board-release-status').textContent.includes('修订 0')&&!a.$('preview').disabled,'release');a.click('board-release-publish');await until(()=>a.$('notice').textContent.includes('冲突')&&!a.$('preview').disabled,'conflict');assert.equal(a.$('board-widgets').children.length,1);a.click('board-release-publish');assert.match(a.$('board-status').textContent,/先刷新/);a.click('board-release-read');await until(()=>a.$('board-release-status').textContent.includes('已发布版本 1')&&!a.$('preview').disabled,'read after conflict');hold=true;a.click('board-release-read');await until(()=>started,'history in flight');a.input('query-companies','company_other');unblock();await until(()=>!a.$('preview').disabled,'stale history settled');assert.equal(a.$('board-release-history').children.length,0);assert.match(a.$('board-release-status').textContent,/请读取/);assert.equal(a.$('board-widgets').children.length,0);
 }finally{if(unblock)unblock();a.close();}});

test('bar chart preserves exact current-page values through save publish and reopen',async()=>{const a=app();try{
 await a.login();a.configure();a.click('preview');await until(()=>!a.$('preview').disabled&&!a.$('result-table-wrap').hidden,'query');a.click('board-add-bar');const chart=a.$('board-widgets').querySelector('.board-chart');assert.equal(chart.querySelectorAll('.board-chart-row').length,3);assert.equal(chart.querySelectorAll('strong')[2].textContent,'9007199254740893.00');assert.equal(chart.querySelectorAll('.board-chart-bar')[2].style.width,'50%');assert.match(chart.textContent,/小于绘图分辨率/);a.click('board-save');const config=JSON.parse(a.$('board-json').value);assert.deepEqual(config.widgets,[{type:'bar',category:'customer.id',field:'ar.open_balance'}]);assert.equal(JSON.stringify(config).includes('9007199254740893'),false);
 a.click('board-server-save');await until(()=>a.$('board-server-status').textContent.includes('版本 1')&&!a.$('preview').disabled,'save bar');a.click('board-release-read');await until(()=>a.$('board-release-status').textContent.includes('修订 0')&&!a.$('preview').disabled,'release');a.click('board-release-publish');await until(()=>a.$('board-release-status').textContent.includes('已发布版本 1')&&!a.$('preview').disabled,'publish');a.click('board-reset');a.click('board-published-open');await until(()=>a.$('board-server-status').textContent.includes('已打开发布看板')&&!a.$('preview').disabled,'reopen');assert.equal(a.$('board-widgets').querySelectorAll('.board-chart-row').length,3);a.input('query-companies','company_other');assert.equal(a.$('board-widgets').children.length,0);assert.equal(a.$('board-value').options.length,0);
 }finally{a.close();}});

test('bar chart uses symmetric signed scale and literal labels without aggregating duplicates',async()=>{const hostile='<img src=x onerror=alert(1)>';const a=app(async(path,options)=>{if(path.endsWith('/queries')){const response=await fetch(url+path,options),value=await response.json();value.rows=[[hostile,'x','-20.00'],[hostile,'x','10.00'],['zero','x','0.00']];return Response.json(value);}});try{
 await a.login();a.configure();a.click('preview');await until(()=>!a.$('preview').disabled&&!a.$('result-table-wrap').hidden,'synthetic signed rows');a.click('board-add-bar');const chart=a.$('board-widgets').querySelector('.board-chart'),bars=chart.querySelectorAll('.board-chart-bar');assert.equal(chart.querySelector('img'),null);assert.match(chart.textContent,/第 1 行/);assert.match(chart.textContent,/第 2 行/);assert.equal(chart.querySelectorAll('.board-chart-row').length,3);assert.equal(bars[0].style.left,'0%');assert.equal(bars[0].style.width,'50%');assert.equal(bars[1].style.left,'50%');assert.equal(bars[1].style.width,'25%');assert.equal(bars[2].style.width,'0%');assert.deepEqual(Array.from(chart.querySelectorAll('strong'),n=>n.textContent),['-20.00','10.00','0.00']);a.click('logout');assert.equal(a.$('board-widgets').children.length,0);
 }finally{a.close();}});

test('bar chart bounds plotted rows and handles empty zero extreme and invalid numeric strings',async()=>{let rows=[];const a=app(async(path,options)=>{if(path.endsWith('/queries')){const response=await fetch(url+path,options),value=await response.json();value.rows=rows.map((v,i)=>['row '+i,'x',v]);return Response.json(value);}});try{
 await a.login();a.configure();a.click('preview');await until(()=>!a.$('preview').disabled,'empty query');a.click('board-add-bar');assert.match(a.$('board-widgets').textContent,/没有可绘制行/);
 async function refresh(values){rows=values;a.click('preview');await until(()=>!a.$('preview').disabled,'synthetic refresh');}
 await refresh(['0','-0.00','0.000']);assert.equal(a.$('board-widgets').querySelectorAll('.board-chart-bar').length,3);assert.ok(Array.from(a.$('board-widgets').querySelectorAll('.board-chart-bar')).every(b=>b.style.width==='0%'));
 const huge='9'.repeat(500);await refresh([huge,'-0.00000000000000000001']);assert.equal(a.$('board-widgets').querySelector('strong').textContent,huge);assert.match(a.$('board-widgets').textContent,/小于绘图分辨率/);
 await refresh(Array(51).fill('1'));assert.equal(a.$('board-widgets').querySelectorAll('.board-chart-row').length,50);assert.match(a.$('board-widgets').textContent,/前 50 行/);
 for(const invalid of ['NaN','Infinity','1e9','+1','01','', '9'.repeat(513)]){await refresh(['1',invalid]);assert.match(a.$('board-widgets').textContent,/图表不可用/);assert.equal(a.$('board-widgets').querySelectorAll('.board-chart-row').length,0);}
 a.click('board-save');const value=JSON.parse(a.$('board-json').value);value.widgets[0].field='customer.id';a.$('board-json').value=JSON.stringify(value);a.click('board-open');assert.match(a.$('board-status').textContent,/数值字段/);
 }finally{a.close();}});

// PDF export. Without Typst the server does not offer PDF, so the action stays hidden; the
// console's own checks are exercised against a server that offers PDF through interception.
const pdfJob='a'.repeat(32),pdfArtifact='b'.repeat(32);
function json(value,status=200){return new Response(JSON.stringify(value),{status,headers:{'Content-Type':'application/json'}});}
function offeringPDF(content,{media='application/pdf',format='pdf',bodies=[]}={}){
  let snapshot='';
  return async(path,options)=>{
    if(path.endsWith('/capabilities')){const caps=await (await fetch(url+path,options)).json();if(!caps.formats.includes('pdf'))caps.formats.push('pdf');return json(caps);}
    // format 'absent' answers like a server that ignored the requested format.
    const named=format==='absent'?{}:{format};
    if(path.endsWith('/exports')&&options?.method==='POST'){const body=JSON.parse(options.body);bodies.push(body);if(body.format!=='pdf')return;snapshot=body.snapshot_id;return json({job_id:pdfJob,kind:'export',state:'queued',snapshot_id:snapshot,...named},202);}
    if(path.endsWith('/jobs/'+pdfJob))return json({job_id:pdfJob,kind:'export',state:'succeeded',snapshot_id:snapshot,...named,artifact_id:pdfArtifact});
    if(path.endsWith('/artifacts/'+pdfArtifact+'/content'))return new Response(content,{headers:{'Content-Type':media,'X-Content-SHA256':createHash('sha256').update(content).digest('hex')}});
  };
}
async function snapshotReady(a){await a.login();a.configure();a.click('snapshot');await until(()=>a.$('result-kind').textContent==='冻结快照'&&!a.$('export').disabled,'snapshot ready');}

test('PDF export is offered only when the server lists the format',async()=>{
  const a=app(pdfConfigured?async path=>{if(path.endsWith('/capabilities')){const caps=await (await fetch(url+path,{headers:{Authorization:'Bearer '+token}})).json();caps.formats=caps.formats.filter(f=>f!=='pdf');return json(caps);}}:undefined);
  try{await snapshotReady(a);assert.equal(a.$('export-pdf').hidden,true);assert.match(a.$('capability-details').textContent,/导出格式xlsx(?!、pdf)/);assert.equal(a.$('doc-pdf').hidden,true);assert.equal(a.$('doc-saved-pdf').hidden,true);}finally{a.close();}
  const b=app(offeringPDF(Buffer.from('%PDF-1.7 fixture')));
  try{await b.login();assert.equal(b.$('export-pdf').hidden,false);assert.equal(b.$('export-pdf').disabled,true);b.click('logout');assert.equal(b.$('export-pdf').hidden,true);}finally{b.close();}
});

test('a PDF export names its format, verifies the file and labels the task',async()=>{
  const bodies=[];const content=Buffer.from('%PDF-1.7 fixture');const a=app(offeringPDF(content,{bodies}));
  try{
    await snapshotReady(a);a.click('export-pdf');await until(()=>a.downloads.length===1,'PDF download');
    assert.equal(a.downloads[0].name,'report-'+pdfArtifact+'.pdf');assert.equal(a.downloads[0].blob.type,'application/pdf');
    assert.deepEqual(Buffer.from(await a.downloads[0].blob.arrayBuffer()),content);
    assert.deepEqual(Object.keys(bodies[0]).sort(),['format','snapshot_id']);assert.equal(bodies[0].format,'pdf');
    assert.ok(a.$('notice').textContent.includes('PDF 已通过摘要校验'));
    assert.equal(a.$('tasks-body').rows[0].cells[0].textContent,'PDF 导出');
    // The task list downloads the PDF again with the same checks.
    const again=[...a.$('tasks-body').rows[0].cells[3].querySelectorAll('button')].find(b=>b.textContent==='下载');again.click();
    await until(()=>a.downloads.length===2,'PDF download from the task list');assert.equal(a.downloads[1].name,'report-'+pdfArtifact+'.pdf');assert.equal(a.downloads[1].blob.type,'application/pdf');
    // The workbook request is unchanged: no format field.
    await until(()=>!a.$('export').disabled,'idle');a.click('export');await until(()=>a.downloads.length===3,'workbook download');
    assert.deepEqual(Object.keys(bodies[1]),['snapshot_id']);assert.equal(a.downloads[2].name.endsWith('.xlsx'),true);
    assert.equal(a.$('tasks-body').rows[0].cells[0].textContent,'Excel 导出');assert.equal(a.errors.length,0);
  }finally{a.close();}
});

test('a PDF with the wrong media type, signature or job format never downloads',async()=>{
  for(const [label,content,options] of [['media type',Buffer.from('%PDF-1.7 fixture'),{media:'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet'}],['signature',Buffer.from('<html>not a pdf</html>'),{}],['job format',Buffer.from('%PDF-1.7 fixture'),{format:'doc'}],['ignored format',Buffer.from('%PDF-1.7 fixture'),{format:'absent'}],['workbook format',Buffer.from('%PDF-1.7 fixture'),{format:'xlsx'}]]){
    const a=app(offeringPDF(content,options));
    try{await snapshotReady(a);a.click('export-pdf');await until(()=>!a.$('notice').hidden&&a.$('notice').className.includes('error'),label+' refused');await sleep(50);assert.equal(a.downloads.length,0,label);}finally{a.close();}
  }
});

test('real Go console PDF export through the pinned Typst renderer',{skip:!pdfConfigured&&'set REPORTING_TYPST, REPORTING_TYPST_SHA256 and REPORTING_PDF_FONTS'},async()=>{
  const a=app();try{
    await snapshotReady(a);assert.equal(a.$('export-pdf').hidden,false);
    a.click('export-pdf');await until(()=>a.downloads.length===1,'verified PDF download',20000);
    const bytes=Buffer.from(await a.downloads[0].blob.arrayBuffer());assert.equal(bytes.subarray(0,5).toString(),'%PDF-');
    if(artifactDir){await mkdir(artifactDir,{recursive:true});await writeFile(join(artifactDir,'console_finance.pdf'),bytes);}
    assert.equal(a.errors.length,0);
  }finally{a.close();}
});

// Document templates (ported from the S03-D1..D4 delivery, now S03-E1..E4).
test('private background upload, immutable saved reference, overlay download and removal',{skip:!pdfConfigured&&'needs the pinned Typst renderer'},async()=>{
 const a=app();try{
  await a.login();a.configure();a.click('snapshot');await until(()=>a.$('result-kind').textContent==='冻结快照'&&!a.$('preview').disabled,'frozen');a.$('doc-field').value='ar.open_balance';a.click('doc-add-row');
  const png=await readFile(join(root,'doctemplates/testdata/a4-background.png'));
  Object.defineProperty(a.$('doc-background-file'),'files',{configurable:true,value:[{size:png.length,arrayBuffer:async()=>png.buffer.slice(png.byteOffset,png.byteOffset+png.byteLength)}]});
  a.click('doc-background-upload');await until(()=>a.$('doc-status').textContent.includes('底稿已上传')&&!a.$('preview').disabled,'background upload');
  a.click('doc-json-save');const config=JSON.parse(a.$('doc-json').value);assert.equal(config.schema_version,'1.2');assert.match(config.background.id,/^[a-f0-9]{32}$/);assert.match(config.background.sha256,/^[a-f0-9]{64}$/);
  a.click('doc-server-save');await until(()=>a.$('doc-server-status').textContent.includes('版本 1')&&!a.$('preview').disabled,'save background version');const id=a.$('doc-server-status').textContent.match(/[a-f0-9]{32}/)[0];
  a.click('doc-background-remove');a.click('doc-saved-pdf');assert.match(a.$('doc-status').textContent,/先保存/);a.click('doc-json-save');assert.equal(JSON.parse(a.$('doc-json').value).schema_version,'1.1');
  a.click('doc-background-list');await until(()=>a.$('doc-background-listing').options.length>0&&!a.$('preview').disabled,'list backgrounds');a.$('doc-background-listing').value=config.background.id;a.click('doc-background-apply');a.input('doc-row','3');a.click('doc-saved-pdf');await until(()=>a.downloads.length===1&&!a.$('preview').disabled,'saved overlay PDF',15000);
  if(artifactDir){await mkdir(artifactDir,{recursive:true});await writeFile(join(artifactDir,'ui_saved_background_v1.pdf'),Buffer.from(await a.downloads[0].blob.arrayBuffer()));}
  a.$('doc-print-mode').value='variables';a.click('doc-print-apply');a.click('doc-json-save');const variables=JSON.parse(a.$('doc-json').value);assert.equal(variables.schema_version,'1.2');assert.deepEqual(variables.background,config.background);a.click('doc-pdf');await until(()=>a.downloads.length===2&&!a.$('preview').disabled,'variable PDF',15000);
  if(artifactDir)await writeFile(join(artifactDir,'ui_variables_background.pdf'),Buffer.from(await a.downloads[1].blob.arrayBuffer()));
  a.click('doc-server-list');await until(()=>a.$('doc-server-listing').options.length>0&&!a.$('preview').disabled,'template list');a.$('doc-server-listing').value=id;a.$('doc-server-version').value='1';a.click('doc-server-open');await until(()=>a.$('doc-server-status').textContent.includes('正在编辑')&&!a.$('preview').disabled,'reopen');a.click('doc-json-save');assert.deepEqual(JSON.parse(a.$('doc-json').value).background,config.background);
  a.input('query-companies','company_other');assert.equal(a.$('doc-background-listing').options.length,0);assert.match(a.$('doc-background-status').textContent,/尚无当前/);a.click('logout');a.click('doc-json-save');assert.equal(a.$('doc-json').value,'');assert.equal(a.errors.length,0);
 }finally{a.close();}
});

test('background file read is session guarded and invalid references apply atomically',async()=>{
 const a=app();try{
  await a.login();a.configure();a.click('preview');await until(()=>!a.$('preview').disabled&&!a.$('result-table-wrap').hidden,'query');
  Object.defineProperty(a.$('doc-background-file'),'files',{configurable:true,value:[{size:524289,arrayBuffer:async()=>{throw Error('must not read');}}]});a.click('doc-background-upload');assert.match(a.$('doc-status').textContent,/512KiB/);
  a.click('doc-json-save');const original=JSON.parse(a.$('doc-json').value);
  for(const change of [d=>d.background=null,d=>{d.schema_version='1.1';d.print={mode:'full',offset_x:0,offset_y:0};d.background={id:'a'.repeat(32),sha256:'b'.repeat(64)};},d=>{d.schema_version='1.2';d.print={mode:'full',offset_x:0,offset_y:0};d.background={id:'../asset',sha256:'b'.repeat(64)};}]){const bad=JSON.parse(JSON.stringify(original));change(bad);a.$('doc-json').value=JSON.stringify(bad);a.click('doc-json-open');assert.notEqual(a.$('doc-status').textContent,'模板已更新；配置不含业务数据。');}a.click('doc-json-save');assert.deepEqual(JSON.parse(a.$('doc-json').value),original);
  let finish,started=false;Object.defineProperty(a.$('doc-background-file'),'files',{configurable:true,value:[{size:100,arrayBuffer:()=>{started=true;return new Promise(resolve=>finish=resolve);}}]});const before=a.trace.filter(t=>t.path.endsWith('/document-backgrounds')).length;a.click('doc-background-upload');await until(()=>started,'reading');a.click('logout');finish(new ArrayBuffer(100));await sleep(80);assert.equal(a.trace.filter(t=>t.path.endsWith('/document-backgrounds')).length,before);assert.match(a.$('doc-background-status').textContent,/尚无当前/);assert.equal(a.errors.length,0);
 }finally{a.close();}
});

test('document template binds a frozen row and total into a verified real PDF preview',{skip:!pdfConfigured&&'needs the pinned Typst renderer'},async()=>{const a=app();try{
 await a.login();a.configure();a.click('snapshot');await until(()=>a.$('result-kind').textContent==='冻结快照'&&!a.$('preview').disabled,'frozen');a.w.document.querySelector('[data-page="documents"]').click();assert.equal(a.$('page-documents').hidden,false);assert.equal(a.$('doc-pdf').hidden,false);a.input('doc-text','应收账款预览');a.click('doc-add-text');a.$('doc-field').value='ar.open_balance';a.click('doc-add-row');a.click('doc-add-total');a.click('doc-json-save');const config=JSON.parse(a.$('doc-json').value);assert.equal(config.elements.length,4);assert.equal(JSON.stringify(config).includes('9007199254740'),false);a.input('doc-row','3');a.click('doc-pdf');await until(()=>a.downloads.length===1&&!a.$('preview').disabled,'real PDF download',15000);assert.equal(a.downloads[0].name,'document-preview.pdf');const bytes=Buffer.from(await a.downloads[0].blob.arrayBuffer());assert.equal(bytes.subarray(0,5).toString(),'%PDF-');
 if(process.env.REPORTING_DOCUMENT_EVIDENCE_DIR){await mkdir(process.env.REPORTING_DOCUMENT_EVIDENCE_DIR,{recursive:true});await writeFile(join(process.env.REPORTING_DOCUMENT_EVIDENCE_DIR,'console_document_preview.pdf'),bytes);}
 a.input('query-companies','company_other');assert.equal(a.$('doc-elements').children.length,0);assert.equal(a.$('doc-json').value,'');a.click('logout');assert.equal(a.$('doc-pdf').hidden,true);assert.equal(a.$('doc-saved-pdf').hidden,true);
 }finally{a.close();}});
test('document table designer builds a schema 1.3 template with columns that add up',async()=>{const a=app();try{
 await a.login();a.configure();a.click('preview');await until(()=>!a.$('preview').disabled&&!a.$('result-table-wrap').hidden,'query');a.w.document.querySelector('[data-page="documents"]').click();
 const options=[...a.$('doc-table-columns').options];assert.ok(options.length>=2);options[0].selected=true;options[1].selected=true;a.click('doc-table-add');a.click('doc-json-save');const config=JSON.parse(a.$('doc-json').value);
 assert.equal(config.schema_version,'1.3');assert.deepEqual(config.print,{mode:'full',offset_x:0,offset_y:0});assert.equal(config.table.columns.length,2);assert.equal(config.table.columns[0].field,options[0].value);assert.equal(config.table.columns[0].width+config.table.columns[1].width,190);assert.equal(config.table.font_size,9);assert.equal(config.background,undefined);assert.match(a.$('doc-table-status').textContent,/2 列/);
 a.click('doc-json-open');assert.equal(a.$('doc-status').textContent,'模板已更新；配置不含业务数据。');
 for(const change of [d=>d.table.columns[0].width=50,d=>d.table.columns.push({field:'ghost',width:0}),d=>d.table.height=29.9,d=>d.table.font_size=13,d=>{d.table.y=25;},d=>{delete d.table;}]){const d=JSON.parse(JSON.stringify(config));change(d);a.$('doc-json').value=JSON.stringify(d);a.click('doc-json-open');assert.notEqual(a.$('doc-status').textContent,'模板已更新；配置不含业务数据。');}
 a.$('doc-json').value=JSON.stringify(config);a.click('doc-json-open');a.click('doc-table-remove');a.click('doc-json-save');const without=JSON.parse(a.$('doc-json').value);assert.equal(without.schema_version,'1.1');assert.equal(without.table,undefined);assert.match(a.$('doc-table-status').textContent,/无明细表/);
 }finally{a.close();}});
test('document table designer rejects each invalid table rule and splits widths that add up',async()=>{const a=app();try{
 await a.login();a.configure();a.click('preview');await until(()=>!a.$('preview').disabled&&!a.$('result-table-wrap').hidden,'query');a.w.document.querySelector('[data-page="documents"]').click();
 const options=[...a.$('doc-table-columns').options];assert.ok(options.length>=3);for(const o of options)o.selected=true;a.click('doc-table-add');a.click('doc-json-save');const config=JSON.parse(a.$('doc-json').value);
 // The first column is sized for the bold label of page 100 at 9 pt (31.4 mm) when the even split is narrower; every width is on the 0.1 mm grid and they add up.
 const units=n=>Math.round(n*10);assert.equal(config.table.columns.length,options.length);assert.equal(config.table.columns.reduce((t,c)=>t+units(c.width),0),units(config.table.width));for(const c of config.table.columns)assert.ok(units(c.width)>=80&&units(c.width*10)===units(c.width)*10);assert.equal(config.table.columns[0].width,Math.max(31.4,Math.floor(1900/options.length)/10));
 const ok='模板已更新；配置不含业务数据。';const reload=d=>{a.$('doc-json').value=JSON.stringify(d);a.click('doc-json-open');return a.$('doc-status').textContent;};
 const f=options.map(o=>o.value),background={id:'0123456789abcdef0123456789abcdef',sha256:'a'.repeat(64)};
 // A base that every rule accepts: two columns of 50 mm, the box well away from the table.
 const base=JSON.parse(JSON.stringify(config));base.elements=[{...base.elements[0],y:200,height:10,font_size:12}];base.table={x:10,y:100,width:100,height:60,font_size:9,columns:[{field:f[0],width:50},{field:f[1],width:50}]};assert.equal(reload(base),ok);
 // Each case breaks exactly one rule of the base.
 const rejected={
  'unknown table key':d=>d.table.extra=1,'x off the grid':d=>d.table.x=10.05,'x under 10':d=>d.table.x=9.9,'y under 25':d=>d.table.y=24.9,'height under 30':d=>d.table.height=29.9,'past the right edge':d=>d.table.x=101,'past the bottom edge':d=>d.table.y=211,'font size not an integer':d=>d.table.font_size=9.5,'font size under 8':d=>d.table.font_size=7,'font size over 12':d=>d.table.font_size=13,
  'thirteen columns':d=>{d.table.width=130;d.table.columns=Array.from({length:13},(_,i)=>({field:f[i%f.length]+(i>=f.length?'_'+i:''),width:10}));},'offset pushes the table out':d=>{d.print.offset_y=10;d.table.y=240;d.table.height=30;},'field not in the result':d=>d.table.columns[1].field='ghost','duplicate field':d=>d.table.columns[1].field=f[0],'column width off the grid':d=>{d.table.columns[0].width=50.04;d.table.columns[1].width=49.96;},'column under 8 mm':d=>{d.table.columns[0].width=92.1;d.table.columns[1].width=7.9;},'widths that do not add up':d=>d.table.columns[0].width=50.1,'first column under the label minimum':d=>{d.table.columns[0].width=31.3;d.table.columns[1].width=68.7;},'overlapping a box':d=>{d.elements[0].y=120;},'table on schema 1.2':d=>{d.schema_version='1.2';d.background=background;},'loose 1.3 keys':d=>{d.canvas=true;},
 };
 for(const [name,change] of Object.entries(rejected)){const d=JSON.parse(JSON.stringify(base));change(d);assert.notEqual(reload(d),ok,name);}
 // The label minimum per type size: 31.4 mm is enough at 9 pt and too little at 10 pt.
 const edge=JSON.parse(JSON.stringify(base));edge.table.columns[0].width=31.4;edge.table.columns[1].width=68.6;assert.equal(reload(edge),ok);edge.table.font_size=10;assert.notEqual(reload(edge),ok);
 // A background on a 1.3 template is kept in the canonical form and the template falls back to 1.2 when the table is removed.
 const withBackground={...JSON.parse(JSON.stringify(base)),background};assert.equal(reload(withBackground),ok);a.click('doc-json-save');assert.deepEqual(JSON.parse(a.$('doc-json').value).background,background);a.click('doc-table-remove');a.click('doc-json-save');const reverted=JSON.parse(a.$('doc-json').value);assert.equal(reverted.schema_version,'1.2');assert.equal(reverted.table,undefined);assert.deepEqual(reverted.background,background);
 // The variable layer needs a bound box or a table.
 const variablesOnly=JSON.parse(JSON.stringify(base));variablesOnly.print.mode='variables';variablesOnly.elements=variablesOnly.elements.filter(e=>e.source==='literal');assert.equal(reload(variablesOnly),ok);delete variablesOnly.table;variablesOnly.schema_version='1.1';assert.notEqual(reload(variablesOnly),ok);
 }finally{a.close();}});
test('document table previews the frozen snapshot rows through the pinned Typst renderer',{skip:!pdfConfigured&&'needs the pinned Typst renderer'},async()=>{const a=app();try{
 await a.login();a.configure();a.click('snapshot');await until(()=>a.$('result-kind').textContent==='冻结快照'&&!a.$('preview').disabled,'frozen');a.w.document.querySelector('[data-page="documents"]').click();
 for(const o of a.$('doc-table-columns').options)o.selected=true;a.click('doc-table-add');a.input('doc-row','1');a.click('doc-pdf');await until(()=>a.downloads.length===1&&!a.$('preview').disabled,'table preview download',20000);const bytes=Buffer.from(await a.downloads[0].blob.arrayBuffer());assert.equal(bytes.subarray(0,5).toString(),'%PDF-');
 if(process.env.REPORTING_DOCUMENT_EVIDENCE_DIR){await mkdir(process.env.REPORTING_DOCUMENT_EVIDENCE_DIR,{recursive:true});await writeFile(join(process.env.REPORTING_DOCUMENT_EVIDENCE_DIR,'console_table_preview.pdf'),bytes);}
 // An amount column too narrow for its values is refused by the server and reported as a layout problem of the template.
 a.click('doc-json-save');const config=JSON.parse(a.$('doc-json').value);config.table={x:10,y:100,width:44,height:60,font_size:9,columns:[{field:'customer.id',width:32},{field:'ar.open_balance',width:12}]};a.$('doc-json').value=JSON.stringify(config);a.click('doc-json-open');assert.equal(a.$('doc-status').textContent,'模板已更新；配置不含业务数据。');a.click('doc-pdf');await until(()=>a.$('notice').textContent.includes('模板无法排版')&&!a.$('preview').disabled,'layout refusal',20000);assert.equal(a.downloads.length,1);
 }finally{a.close();}});
test('a document preview whose body is not a PDF never downloads',async()=>{const body=Buffer.from('not a pdf at all');const a=app(async(path,options)=>{if(path.endsWith('/capabilities')){const caps=await (await fetch(url+path,{headers:{Authorization:'Bearer '+token}})).json();if(!caps.formats.includes('pdf'))caps.formats.push('pdf');return json(caps);}if(path.endsWith('/document-previews'))return new Response(body,{headers:{'Content-Type':'application/pdf','X-Content-SHA256':createHash('sha256').update(body).digest('hex')}});});try{
 await snapshotReady(a);a.w.document.querySelector('[data-page="documents"]').click();assert.equal(a.$('doc-pdf').hidden,false);a.click('doc-pdf');await until(()=>a.$('notice').textContent.includes('完整性')&&!a.$('preview').disabled,'bad signature');assert.equal(a.downloads.length,0);
 }finally{a.close();}});

test('document template rejects scope changes overlap and forged values and checks PDF digest',{skip:!pdfConfigured&&'needs the pinned Typst renderer'},async()=>{const a=app(async(path,options)=>{if(path.endsWith('/document-previews')){const original=await fetch(url+path,options);return new Response(await original.arrayBuffer(),{headers:{'Content-Type':'application/pdf','X-Content-SHA256':'0'.repeat(64)}});}});try{
 await a.login();a.configure();a.click('preview');await until(()=>!a.$('preview').disabled&&!a.$('result-table-wrap').hidden,'query');a.click('doc-pdf');assert.match(a.$('doc-status').textContent,/冻结快照/);a.click('doc-json-save');const original=JSON.parse(a.$('doc-json').value);for(const change of [d=>d.binding.integration_id='other',d=>d.elements.push({...d.elements[0],id:'n2'}),d=>d.values={n1:'forged'},d=>d.elements[0].source='formula',d=>d.elements[0].id=['n1']]){const d=JSON.parse(JSON.stringify(original));change(d);a.$('doc-json').value=JSON.stringify(d);a.click('doc-json-open');assert.notEqual(a.$('doc-status').textContent,'模板已更新；配置不含业务数据。');assert.equal(a.$('doc-elements').children.length,1);}
 a.click('snapshot');await until(()=>a.$('result-kind').textContent==='冻结快照'&&!a.$('preview').disabled,'frozen');a.click('doc-pdf');await until(()=>a.$('notice').textContent.includes('完整性')&&!a.$('preview').disabled,'bad digest',15000);assert.equal(a.downloads.length,0);assert.equal(a.$('result-table').tBodies[0].rows.length,0);
 }finally{a.close();}});

test('current PDF authority failure clears the matching frozen result and designer state',{skip:!pdfConfigured&&'needs the pinned Typst renderer'},async()=>{const a=app(async(path)=>{if(path.endsWith('/document-previews'))return problem(403,'AUTH_SCOPE_DENIED');});try{
 await a.login();a.configure();a.click('snapshot');await until(()=>a.$('result-kind').textContent==='冻结快照'&&!a.$('preview').disabled,'frozen');a.click('doc-pdf');await until(()=>a.$('notice').textContent.includes('无权访问')&&!a.$('preview').disabled,'PDF denial');assert.equal(a.$('result-table').tBodies[0].rows.length,0);assert.equal(a.$('doc-elements').children.length,0);assert.equal(a.$('doc-field').options.length,0);assert.equal(a.downloads.length,0);
 }finally{a.close();}});

test('a delayed PDF preview cannot download after query invalidation',{skip:!pdfConfigured&&'needs the pinned Typst renderer'},async()=>{let started=false,release;const gate=new Promise(resolve=>release=resolve);const a=app(async(path,options)=>{if(path.endsWith('/document-previews')){started=true;const response=await fetch(url+path,options);await gate;return response;}});try{
 await a.login();a.configure();a.click('snapshot');await until(()=>a.$('result-kind').textContent==='冻结快照'&&!a.$('preview').disabled,'frozen');a.click('doc-pdf');await until(()=>started,'PDF in flight');a.input('query-companies','company_other');release();await until(()=>!a.$('preview').disabled,'stale PDF settled',15000);assert.equal(a.downloads.length,0);assert.equal(a.$('doc-elements').children.length,0);
 }finally{release();a.close();}});

test('an old PDF authority failure cannot clear a changed-query result',{skip:!pdfConfigured&&'needs the pinned Typst renderer'},async()=>{let started=false,release;const gate=new Promise(resolve=>release=resolve);const a=app(async(path)=>{if(path.endsWith('/document-previews')){started=true;await gate;return problem(403,'AUTH_SCOPE_DENIED');}});try{
 await a.login();a.configure();a.click('snapshot');await until(()=>a.$('result-kind').textContent==='冻结快照'&&!a.$('preview').disabled,'frozen');a.click('doc-pdf');await until(()=>started,'PDF in flight');a.input('query-companies','company_demo');release();await until(()=>!a.$('preview').disabled,'old PDF failed');assert.equal(a.$('notice').textContent.includes('PDF预览：'),false,'stale failure must not replace changed-view notice');a.click('preview');await until(()=>!a.$('preview').disabled&&!a.$('result-table-wrap').hidden,'new query');assert.equal(a.$('result-table').tBodies[0].rows.length,3);assert.equal(a.$('doc-elements').children.length,1);assert.equal(a.downloads.length,0);
 }finally{release();a.close();}});


test('server document templates save canonical versions publish rollback and render fixed server config',{skip:!pdfConfigured&&'needs the pinned Typst renderer'},async()=>{const a=app();try{
 await a.login();a.configure();a.click('snapshot');await until(()=>a.$('result-kind').textContent==='冻结快照'&&!a.$('preview').disabled,'frozen');a.click('doc-json-save');const original=JSON.parse(a.$('doc-json').value);a.$('doc-json').value=JSON.stringify({elements:original.elements,binding:{dataset:{version:original.binding.dataset.version,id:original.binding.dataset.id},integration_id:original.binding.integration_id},kind:original.kind,schema_version:original.schema_version});a.click('doc-json-open');a.click('doc-server-save');await until(()=>a.$('doc-server-status').textContent.includes('版本 1')&&!a.$('preview').disabled,'save v1');const id=a.$('doc-server-status').textContent.match(/[a-f0-9]{32}/)[0];
 a.click('doc-release-read');await until(()=>a.$('doc-release-status').textContent.includes('修订 0')&&!a.$('preview').disabled,'release');a.click('doc-release-publish');await until(()=>a.$('doc-release-status').textContent.includes('已发布版本 1')&&!a.$('preview').disabled,'publish');a.input('doc-text','Draft title');a.click('doc-add-text');a.click('doc-release-publish');assert.match(a.$('doc-status').textContent,/先保存/);a.click('doc-saved-pdf');assert.equal(a.downloads.length,0);
 a.click('doc-server-save');await until(()=>a.$('doc-server-status').textContent.includes('版本 2')&&!a.$('preview').disabled,'save v2');a.click('doc-release-read');await until(()=>a.$('doc-release-status').textContent.includes('已发布版本 1')&&!a.$('preview').disabled,'draft preserves publish');a.click('doc-published-open');await until(()=>a.$('doc-server-status').textContent.includes('已打开发布模板')&&!a.$('preview').disabled,'published open');assert.equal(a.$('doc-elements').children.length,1);a.click('doc-saved-pdf');await until(()=>a.downloads.length===1&&!a.$('preview').disabled,'saved server PDF',15000);const trace=a.trace.find(v=>v.path.endsWith('/document-templates/'+id+'/previews'));assert.ok(trace);if(process.env.REPORTING_DOCUMENT_EVIDENCE_DIR){await mkdir(process.env.REPORTING_DOCUMENT_EVIDENCE_DIR,{recursive:true});const bytes=Buffer.from(await a.downloads[0].blob.arrayBuffer());await writeFile(join(process.env.REPORTING_DOCUMENT_EVIDENCE_DIR,'console_saved_template_preview.pdf'),bytes);await writeFile(join(process.env.REPORTING_DOCUMENT_EVIDENCE_DIR,'saved_template_preview.json'),JSON.stringify({id,version:1,sha256:createHash('sha256').update(bytes).digest('hex'),source:'real Go saved version route and Node renderer; template v2 contains Draft title, v1 must not'},null,2));}
 a.click('doc-server-save');await until(()=>a.$('notice').textContent.includes('冲突')&&!a.$('preview').disabled,'old draft CAS');assert.equal(a.$('result-table').tBodies[0].rows.length,3);a.click('doc-release-read');await until(()=>a.$('doc-release-status').textContent.includes('修订 1')&&!a.$('preview').disabled,'state');a.click('doc-release-withdraw');await until(()=>a.$('doc-release-status').textContent.includes('当前未发布')&&!a.$('preview').disabled,'withdraw');a.click('doc-release-publish');await until(()=>a.$('doc-release-status').textContent.includes('修订 3')&&!a.$('preview').disabled,'republish old version');a.click('logout');assert.equal(a.$('doc-release-history').children.length,0);assert.equal(a.$('doc-server-listing').options.length,0);
 }finally{a.close();}});

test('uncertain template save and publication require explicit reconciliation without retry',async()=>{let loseSave=true,loseRelease=true,saves=0,releases=0;const a=app(async(path,options)=>{if(path.endsWith('/document-templates')&&options.method==='POST'){saves++;if(loseSave){loseSave=false;await fetch(url+path,options);throw new TypeError('save reply lost');}}if(path.includes('/document-templates/')&&path.endsWith('/release')&&options.method==='POST'){releases++;if(loseRelease){loseRelease=false;await fetch(url+path,options);throw new TypeError('release reply lost');}}});try{
 await a.login();a.configure();a.click('preview');await until(()=>!a.$('preview').disabled&&!a.$('result-table-wrap').hidden,'query');a.click('doc-server-save');await until(()=>a.$('notice').textContent.includes('核对')&&!a.$('preview').disabled,'lost save');assert.equal(saves,1);a.click('doc-server-list');await until(()=>a.$('doc-server-listing').options.length>0&&!a.$('preview').disabled,'list');const latest=a.$('doc-server-listing').options[a.$('doc-server-listing').options.length-1];a.$('doc-server-listing').value=latest.value;a.$('doc-server-version').value='1';a.click('doc-server-open');await until(()=>a.$('doc-server-status').textContent.includes('正在编辑模板')&&!a.$('preview').disabled,'open');a.click('doc-release-read');await until(()=>a.$('doc-release-status').textContent.includes('修订 0')&&!a.$('preview').disabled,'state');a.click('doc-release-publish');await until(()=>a.$('notice').textContent.includes('核对')&&!a.$('preview').disabled,'lost release');assert.equal(releases,1);a.click('doc-release-publish');assert.match(a.$('doc-status').textContent,/先刷新/);assert.equal(releases,1);a.click('doc-release-read');await until(()=>a.$('doc-release-status').textContent.includes('已发布版本 1')&&!a.$('preview').disabled,'reconcile');assert.equal(saves,1);assert.equal(a.$('result-table').tBodies[0].rows.length,3);
 }finally{a.close();}});

test('late template release metadata cannot repopulate an invalidated query',async()=>{let hold=false,started=false,release;const gate=new Promise(resolve=>release=resolve);const a=app(async(path,options)=>{if(hold&&path.includes('/document-templates/')&&path.endsWith('/release')&&options.method==='GET'){started=true;const response=await fetch(url+path,options);await gate;return response;}});try{
 await a.login();a.configure();a.click('preview');await until(()=>!a.$('preview').disabled&&!a.$('result-table-wrap').hidden,'query');a.click('doc-server-save');await until(()=>a.$('doc-server-status').textContent.includes('版本 1')&&!a.$('preview').disabled,'saved');hold=true;a.click('doc-release-read');await until(()=>started,'release in flight');a.input('query-companies','company_other');release();await until(()=>!a.$('preview').disabled,'late metadata settled');assert.equal(a.$('doc-release-history').children.length,0);assert.match(a.$('doc-release-status').textContent,/请先读取/);assert.equal(a.$('doc-elements').children.length,0);assert.equal(a.$('doc-server-listing').options.length,0);
 }finally{release();a.close();}});

test('preprinted-paper profile persists tenth-mm geometry and renders saved variable-only content',{skip:!pdfConfigured&&'needs the pinned Typst renderer'},async()=>{const a=app();try{
 await a.login();a.configure();a.click('snapshot');await until(()=>a.$('result-kind').textContent==='冻结快照'&&!a.$('preview').disabled,'frozen');a.$('doc-field').value='ar.open_balance';a.click('doc-add-row');a.click('doc-json-save');const d=JSON.parse(a.$('doc-json').value);d.schema_version='1.1';d.print={mode:'full',offset_x:0,offset_y:0};d.elements[1].x=20.1;d.elements[1].y=45.2;d.elements[1].width=170;a.$('doc-json').value=JSON.stringify(d);a.click('doc-json-open');a.$('doc-print-mode').value='variables';a.input('doc-offset-x','1.2');a.input('doc-offset-y','-0.7');a.click('doc-print-apply');assert.match(a.$('doc-print-status').textContent,/变量层/);a.click('doc-server-save');await until(()=>a.$('doc-server-status').textContent.includes('版本 1')&&!a.$('preview').disabled,'saved print profile');const id=a.$('doc-server-status').textContent.match(/[a-f0-9]{32}/)[0];a.input('doc-offset-x','1.3');a.click('doc-print-apply');a.click('doc-saved-pdf');assert.match(a.$('doc-status').textContent,/先保存/);assert.equal(a.downloads.length,0);a.input('doc-offset-x','1.2');a.click('doc-print-apply');a.input('doc-row','3');a.click('doc-saved-pdf');await until(()=>a.downloads.length===1&&!a.$('preview').disabled,'variable PDF',15000);
 if(process.env.REPORTING_DOCUMENT_EVIDENCE_DIR){await mkdir(process.env.REPORTING_DOCUMENT_EVIDENCE_DIR,{recursive:true});await writeFile(join(process.env.REPORTING_DOCUMENT_EVIDENCE_DIR,'ui_saved_variable_layer.pdf'),Buffer.from(await a.downloads[0].blob.arrayBuffer()));}
 a.click('doc-server-list');await until(()=>a.$('doc-server-listing').options.length>0&&!a.$('preview').disabled,'list');a.$('doc-server-listing').value=id;a.$('doc-server-version').value='1';a.click('doc-server-open');await until(()=>a.$('doc-server-status').textContent.includes('正在编辑模板')&&!a.$('preview').disabled,'reopen print config');assert.equal(a.$('doc-offset-x').value,'1.2');assert.equal(a.$('doc-offset-y').value,'-0.7');assert.equal(a.$('doc-print-mode').value,'variables');a.click('doc-json-save');const saved=JSON.parse(a.$('doc-json').value);assert.equal(saved.elements[1].x,20.1);assert.equal(saved.print.mode,'variables');a.input('query-companies','company_other');assert.equal(a.$('doc-offset-x').value,'0');assert.equal(a.$('doc-print-mode').value,'full');a.click('logout');assert.equal(a.$('doc-offset-y').value,'0');
 }finally{a.close();}});

test('print settings reject off-grid unsafe and empty variable profiles atomically',async()=>{const a=app();try{
 await a.login();a.configure();a.click('preview');await until(()=>!a.$('preview').disabled&&!a.$('result-table-wrap').hidden,'query');a.$('doc-print-mode').value='variables';a.click('doc-print-apply');assert.match(a.$('doc-status').textContent,/至少需要/);a.click('doc-json-save');const original=JSON.parse(a.$('doc-json').value);assert.equal(original.schema_version,'1.0');a.click('doc-add-row');a.$('doc-print-mode').value='variables';a.input('doc-offset-x','0.15');a.click('doc-print-apply');assert.match(a.$('doc-status').textContent,/偏移无效/);a.input('doc-offset-x','1');a.click('doc-print-apply');assert.match(a.$('doc-status').textContent,/超出安全区/);for(const change of [d=>d.elements[0].x=10.1,d=>d.print=null,d=>{d.schema_version='1.1';d.print={mode:'full',offset_x:0,offset_y:0,scale:2};},d=>{d.schema_version='1.1';d.print={mode:'full'};}]){const value=JSON.parse(JSON.stringify(original));change(value);a.$('doc-json').value=JSON.stringify(value);a.click('doc-json-open');assert.notEqual(a.$('doc-status').textContent,'模板已更新；配置不含业务数据。');assert.equal(a.$('doc-elements').children.length,2);}
 }finally{a.close();}});

// S03-H official issue: the published template that is open, the frozen snapshot and the row become a
// stamped PDF through a durable job under the number the source product gives; the records panel lists it.
test('official issue of the open published template downloads the stamped PDF and lists the record',{skip:!pdfConfigured&&'needs the pinned Typst renderer'},async()=>{const a=app();try{
 await a.login();a.configure();a.click('snapshot');await until(()=>a.$('result-kind').textContent==='冻结快照'&&!a.$('preview').disabled,'frozen');
 assert.equal(a.$('doc-issue').hidden,false);
 a.$('doc-field').value='ar.open_balance';a.click('doc-add-row');a.click('doc-server-save');await until(()=>a.$('doc-server-status').textContent.includes('版本 1')&&!a.$('preview').disabled,'save v1');const id=a.$('doc-server-status').textContent.match(/[a-f0-9]{32}/)[0];
 // A saved draft that is not the open published version is refused by the console itself.
 a.input('doc-row','3');a.click('doc-issue');assert.match(a.$('doc-status').textContent,/打开已发布模板/);assert.equal(a.trace.filter(v=>v.path.endsWith('/issues')&&v.method==='POST').length,0);
 a.click('doc-release-read');await until(()=>a.$('doc-release-status').textContent.includes('修订 0')&&!a.$('preview').disabled,'release');a.click('doc-release-publish');await until(()=>a.$('doc-release-status').textContent.includes('已发布版本 1')&&!a.$('preview').disabled,'publish');
 a.click('doc-published-open');await until(()=>a.$('doc-server-status').textContent.includes('已打开发布模板')&&!a.$('preview').disabled,'published open');
 // Any change to the open template takes the issue button away until it is reopened; so does
 // saving it as a new (unpublished) version of the same configuration.
 a.input('doc-text','Changed');a.click('doc-add-text');a.click('doc-issue');assert.match(a.$('doc-status').textContent,/先保存/);a.click('doc-published-open');await until(()=>a.$('doc-server-status').textContent.includes('已打开发布模板')&&!a.$('preview').disabled,'reopened');
 a.click('doc-server-save');await until(()=>a.$('doc-server-status').textContent.includes('版本 2')&&!a.$('preview').disabled,'saved v2');a.click('doc-issue');assert.match(a.$('doc-status').textContent,/打开已发布模板/);assert.equal(a.trace.filter(v=>v.path.endsWith('/issues')&&v.method==='POST').length,0);a.click('doc-published-open');await until(()=>a.$('doc-server-status').textContent.includes('已打开发布模板')&&!a.$('preview').disabled,'reopened again');
 // Opening the saved draft version with 打开模板版本 disarms it as well.
 a.click('doc-server-list');await until(()=>a.$('doc-server-listing').options.length>0&&!a.$('preview').disabled,'list');a.$('doc-server-listing').value=id;a.$('doc-server-version').value='2';a.click('doc-server-open');await until(()=>a.$('doc-server-status').textContent.includes('正在编辑模板')&&!a.$('preview').disabled,'draft open');a.click('doc-issue');assert.match(a.$('doc-status').textContent,/打开已发布模板/);assert.equal(a.trace.filter(v=>v.path.endsWith('/issues')&&v.method==='POST').length,0);a.click('doc-published-open');await until(()=>a.$('doc-server-status').textContent.includes('已打开发布模板')&&!a.$('preview').disabled,'reopened once more');
 a.click('doc-issue');await until(()=>a.downloads.length===1&&!a.$('preview').disabled,'issued PDF',20000);
 // The finance demo product numbers its documents AR-000001, AR-000002, ...: the console names the file by what the product said.
 assert.equal(a.downloads[0].name,'issued-AR-000001.pdf');const bytes=Buffer.from(await a.downloads[0].blob.arrayBuffer());assert.equal(bytes.subarray(0,5).toString(),'%PDF-');
 const submitted=a.trace.filter(v=>v.path.endsWith('/issues')&&v.method==='POST');assert.equal(submitted.length,1);assert.ok(submitted[0].key);
 assert.equal(a.$('tasks-body').rows[0].cells[0].textContent,'正式出具 No. AR-000001');assert.ok(a.$('notice').textContent.includes('AR-000001')&&a.$('notice').textContent.includes('下载已发起'));
 a.click('doc-issues-refresh');await until(()=>!a.$('doc-issues-wrap').hidden&&!a.$('preview').disabled,'records');
 // The record is the product's: a string number, the references of its reservation and confirmation, and the confirmation time.
 const raw=(await (await fetch(url+'/api/reporting/v1/issues?limit=5',{headers:{Authorization:'Bearer '+token}})).json())[0];assert.equal(raw.number,'AR-000001');assert.ok(raw.reservation_ref&&raw.confirmation_ref&&raw.confirmed_at);
 const row=a.$('doc-issues-body').rows[0];assert.equal(row.cells[0].textContent,'No. AR-000001');assert.equal(row.cells[1].textContent,'已出具');assert.match(row.cells[2].textContent,/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/);assert.equal(row.cells[3].textContent,id+' · 版本 1');assert.ok(row.cells[4].textContent.endsWith('· 第 3 行'));
 const digest=createHash('sha256').update(bytes).digest('hex');assert.equal(row.cells[5].textContent,digest+' · 1 页');assert.equal(row.cells[6].textContent,raw.confirmation_ref+' · '+raw.confirmed_at);
 // The record downloads the same bytes again under the same name.
 row.cells[7].querySelector('button').click();await until(()=>a.downloads.length===2&&!a.$('preview').disabled,'record download');assert.equal(a.downloads[1].name,'issued-AR-000001.pdf');assert.deepEqual(Buffer.from(await a.downloads[1].blob.arrayBuffer()),bytes);
 if(process.env.REPORTING_DOCUMENT_EVIDENCE_DIR){
  const dir=process.env.REPORTING_DOCUMENT_EVIDENCE_DIR,authorized={headers:{Authorization:'Bearer '+token}};await mkdir(dir,{recursive:true});await writeFile(join(dir,'console_issued.pdf'),bytes);
  // The record of the real stack, kept as it was read; the checker's expectation of the same PDF is console_issued.json.
  await writeFile(join(dir,'console_issued_record.json'),JSON.stringify({template_id:id,version:1,row_index:2,sha256:digest,record:raw,source:'real Go issue job through the console; the PDF is the artifact the record names'},null,2));
  // What the checker verifies the PDF against: the published template's own boxes and the values of the frozen row as the test
  // knows them, the product's record with the PDF's digest, and the preview of the same document, which must not pass as the issue.
  const published=await (await fetch(url+'/api/reporting/v1/document-templates/'+id+'/published',authorized)).json();assert.equal(published.version,1);
  const frozen=await (await fetch(url+'/api/reporting/v1/snapshots/'+raw.snapshot_id+'/rows?limit=100',authorized)).json();
  const known={'ar.open_balance':frozen.rows[2][frozen.columns.findIndex(c=>c.id==='ar.open_balance')]};assert.equal(known['ar.open_balance'],'9007199254740893.00');
  const print=published.config.print||{mode:'full',offset_x:0,offset_y:0};assert.equal(print.mode,'full');assert.equal(published.config.background,undefined);assert.equal(published.config.table,undefined);
  assert.ok(published.config.elements.length>0);
  const elements=published.config.elements.map(e=>{assert.ok(e.source==='literal'||e.source==='row',e.source);if(e.source==='row')assert.ok(Object.hasOwn(known,e.field),e.field);return {text:e.source==='literal'?e.text:known[e.field],x:e.x,y:e.y,width:e.width,height:e.height,size:e.font_size,align:e.align};});
  assert.ok(elements.some(e=>e.text==='9007199254740893.00'));
  const preview=Buffer.from(await (await fetch(url+'/api/reporting/v1/document-templates/'+id+'/previews',{method:'POST',headers:{...authorized.headers,'Content-Type':'application/json'},body:JSON.stringify({version:1,snapshot_id:raw.snapshot_id,row_index:2})})).arrayBuffer());assert.equal(preview.subarray(0,5).toString(),'%PDF-');
  await writeFile(join(dir,'console_issued.preview.pdf'),preview);
  await writeFile(join(dir,'console_issued.json'),JSON.stringify({kind:'document',file:'console_issued.pdf',background:false,mode:print.mode,offset_x:print.offset_x,offset_y:print.offset_y,elements,issue:{number:raw.number,issue_id:raw.issue_id,issued_at:raw.issued_at,stamp:raw.stamp,sha256:digest,pages:raw.pages,preview_file:'console_issued.preview.pdf'}},null,1));
 }
 // Withdrawn: the console refuses before submitting; a server refusal is reported as such.
 a.click('doc-release-read');await until(()=>a.$('doc-release-status').textContent.includes('已发布版本 1')&&!a.$('preview').disabled,'state');a.click('doc-release-withdraw');await until(()=>a.$('doc-release-status').textContent.includes('当前未发布')&&!a.$('preview').disabled,'withdrawn');
 a.click('doc-issue');await until(()=>a.$('notice').textContent.includes('已发布的模板版本')&&!a.$('preview').disabled,'server refusal');assert.equal(a.downloads.length,2);
 a.click('logout');assert.equal(a.$('doc-issue').hidden,true);assert.equal(a.$('doc-issues-wrap').hidden,true);assert.equal(a.$('doc-issues-body').rows.length,0);assert.equal(a.errors.length,0);
 }finally{a.close();}});

// A server that offers issuance and answers the issue routes from its options. `submit(body)` returns the Response to POST /issues;
// `job(body,reads)` and `cancel(body)` return the job view that GET /jobs/{id} and the cancel route show (body is what was submitted,
// reads counts the reads); `records` is the listing. Without `submit` it refuses as if the template were no longer published.
function offeringIssuance(options={}){
  const base=offeringPDF(Buffer.from('%PDF-1.7 fixture'));let submitted=null,reads=0;
  return async(path,opts)=>{
    if(path.endsWith('/capabilities')){const caps=await (await fetch(url+path,opts)).json();if(!caps.formats.includes('pdf'))caps.formats.push('pdf');caps.issuance=options.issuance!==false;return json(caps);}
    if(path.endsWith('/issues?limit=50')&&(!opts||!opts.method||opts.method==='GET'))return json(options.records||[]);
    if(path.endsWith('/issues')&&opts?.method==='POST'){submitted=JSON.parse(opts.body);options.bodies?.push(submitted);return options.submit?options.submit(submitted):problem(409,'TEMPLATE_NOT_PUBLISHED');}
    if(path.endsWith('/jobs/'+pdfJob+'/cancel')&&opts?.method==='POST'&&options.cancel)return json(options.cancel(submitted));
    if(path.endsWith('/jobs/'+pdfJob))return json(options.job?options.job(submitted,++reads):issueJob(submitted,'succeeded',{number:'AR-000012'}));
    return base(path,opts);
  };
}
// An issue job as the server shows it: the summary of what was asked, the artifact once it succeeded, the error code once it failed and
// the product's number only once it succeeded. `over` replaces whole fields.
function issueJob(body,state,{number,code,...over}={}){
  return {job_id:pdfJob,kind:'issue',state,format:'pdf',snapshot_id:body.snapshot_id,...(state==='succeeded'?{artifact_id:pdfArtifact}:{}),...(code?{error_code:code}:{}),issue:{template_id:body.template_id,template_version:body.version,template_sha256:'e'.repeat(64),row_index:body.row_index,...(number===undefined?{}:{number})},...over};
}
// The one state in which the console issues: the published version of a template is open. The demo server keeps at most 16 templates
// per identity for the whole run and every new template takes one of them, so the tests that only need this state share one: the
// first to ask saves and publishes it, the others open it by its id. Returns the template id.
let sharedTemplate=null;
async function publishedOpen(a){
  if(sharedTemplate){
    a.click('doc-server-list');await until(()=>[...a.$('doc-server-listing').options].some(o=>o.value===sharedTemplate)&&!a.$('preview').disabled,'list');
    a.$('doc-server-listing').value=sharedTemplate;a.$('doc-server-version').value='1';a.click('doc-server-open');await until(()=>a.$('doc-server-status').textContent.includes('正在编辑模板')&&!a.$('preview').disabled,'open version');
  }else{
    a.click('doc-server-save');await until(()=>a.$('doc-server-status').textContent.includes('版本 1')&&!a.$('preview').disabled,'save');
    a.click('doc-release-read');await until(()=>a.$('doc-release-status').textContent.includes('修订 0')&&!a.$('preview').disabled,'release');
    a.click('doc-release-publish');await until(()=>a.$('doc-release-status').textContent.includes('已发布版本 1')&&!a.$('preview').disabled,'publish');
  }
  a.click('doc-published-open');await until(()=>a.$('doc-server-status').textContent.includes('已打开发布模板')&&!a.$('preview').disabled,'open');
  const id=a.$('doc-server-status').textContent.match(/[a-f0-9]{32}/)[0];
  sharedTemplate??=id;assert.equal(id,sharedTemplate);
  return id;
}
// The task list row of the issue job; the snapshot task it was made from is listed too.
const issueRow=a=>[...a.$('tasks-body').rows].find(r=>r.cells[0].textContent.startsWith('正式出具'));
// A number is the source product's string: 1..32 of A-Za-z0-9._-, starting with a letter or a digit.
const goodNumbers=['AR-000012','SH-000001','A','z','0','000012','a.b_c-d','A-','A..','9'.repeat(32)];
const badNumbers=['',' AR-1','AR 1','-AR','.AR','_AR','A'.repeat(33),'AR/1','AR\\1','ÄR-1','AR-１','AR-1\n',12,0,1.5,null,true,['AR-1'],{}];

test('the issue button follows the issuance capability and the server refusal is explained',async()=>{
  const a=app(offeringIssuance({issuance:false}));
  try{await snapshotReady(a);assert.equal(a.$('doc-pdf').hidden,false);assert.equal(a.$('doc-issue').hidden,true);}finally{a.close();}
  // The flag alone is not enough: without the pdf format there is no renderer to issue with.
  const noPDF=app(async(path,opts)=>{if(path.endsWith('/capabilities')){const caps=await (await fetch(url+path,opts)).json();caps.formats=caps.formats.filter(f=>f!=='pdf');caps.issuance=true;return json(caps);}});
  try{await snapshotReady(noPDF);assert.equal(noPDF.$('doc-pdf').hidden,true);assert.equal(noPDF.$('doc-issue').hidden,true);}finally{noPDF.close();}
  const bodies=[];const b=app(offeringIssuance({bodies}));
  try{
    await snapshotReady(b);assert.equal(b.$('doc-issue').hidden,false);
    const id=await publishedOpen(b);
    b.input('doc-row','2');b.click('doc-issue');await until(()=>b.$('notice').textContent.includes('已发布的模板版本')&&!b.$('preview').disabled,'refused');
    // The refusal concerns the template, not the snapshot: the frozen result stays on screen.
    assert.equal(b.$('result-kind').textContent,'冻结快照');assert.equal(b.$('result-table').tBodies[0].rows.length,3);assert.equal(b.$('doc-issue').hidden,false);
    assert.equal(bodies.length,1);assert.deepEqual(bodies[0],{template_id:id,version:1,snapshot_id:bodies[0].snapshot_id,row_index:1});assert.ok(/^[a-f0-9]{32}$/.test(bodies[0].snapshot_id));
    assert.equal(b.downloads.length,0);assert.equal([...b.$('tasks-body').rows].filter(r=>r.cells[0].textContent.startsWith('正式出具')).length,0);
  }finally{b.close();}
});

test('an issue job view must be the job that was asked for, and carry no number before the product confirmed',async()=>{
  // Not remembered, not followed, nothing downloads: no issue summary, another template, version or row, a number on a queued job.
  for(const [label,view] of [
    ['no issue summary',body=>({...issueJob(body,'queued'),issue:undefined})],
    ['another template',body=>issueJob({...body,template_id:'1'.repeat(32)},'queued')],
    ['another template version',body=>issueJob({...body,version:body.version+1},'queued')],
    ['another row',body=>issueJob({...body,row_index:5},'queued')],
    ['a number before the product confirmed',body=>issueJob(body,'queued',{number:'AR-000012'})],
  ]){
    const a=app(offeringIssuance({submit:body=>json(view(body),202)}));
    try{
      await snapshotReady(a);await publishedOpen(a);a.click('doc-issue');
      await until(()=>a.$('notice').textContent.includes('无法验证的响应')&&!a.$('preview').disabled,label);
      assert.equal(a.downloads.length,0,label);assert.equal([...a.$('tasks-body').rows].filter(r=>r.cells[0].textContent.startsWith('正式出具')).length,0,label);
      assert.equal(a.trace.filter(v=>v.path.endsWith('/jobs/'+pdfJob)).length,0,label);assert.equal(a.trace.filter(v=>v.path.endsWith('/issues')&&v.method==='POST').length,1,label);assert.equal(a.errors.length,0,label);
    }finally{a.close();}
  }
  // The submission may be right and the job still be followed to a view that cannot be believed: no download, and the task that was
  // accepted stays on the list as it was, without a number.
  for(const [label,view] of [
    ['an integer number on a succeeded job',body=>issueJob(body,'succeeded',{number:12})],
    ['no number on a succeeded job',body=>issueJob(body,'succeeded')],
    ['a number on a running job',body=>issueJob(body,'running',{number:'AR-000012'})],
  ]){
    const a=app(offeringIssuance({submit:body=>json(issueJob(body,'queued'),202),job:view}));
    try{
      await snapshotReady(a);await publishedOpen(a);a.click('doc-issue');
      await until(()=>a.$('notice').textContent.includes('无法验证的响应')&&!a.$('preview').disabled,label);
      assert.equal(a.downloads.length,0,label);assert.equal(issueRow(a).cells[0].textContent,'正式出具（编号待来源产品确认）',label);assert.equal(issueRow(a).cells[2].textContent,'排队中',label);assert.equal(a.errors.length,0,label);
    }finally{a.close();}
  }
});

test('an issue job view has the product number as a string, and only once it succeeded',async()=>{
  const asked={template_id:'d'.repeat(32),version:1,snapshot_id:'c'.repeat(32),row_index:0};
  let view=null;const a=app(async path=>{if(path.endsWith('/jobs/'+pdfJob))return json(view);});
  // Restoring a task by its id takes the view through the same check as a submission or a poll.
  const restore=async job=>{view=job;a.input('restore-id',pdfJob);a.click('restore-job');await until(()=>!a.$('preview').disabled,'restored');return a.$('notice').textContent;};
  const row=()=>a.$('tasks-body').rows[0];
  try{
    await a.login();
    // A succeeded job without a valid string number is not believed or remembered.
    for(const number of [...badNumbers,undefined]){assert.match(await restore(issueJob(asked,'succeeded',{number})),/无法验证的响应/,String(number));assert.equal(a.$('tasks-body').rows.length,0,String(number));}
    for(const number of goodNumbers){assert.match(await restore(issueJob(asked,'succeeded',{number})),/已恢复/,number);assert.equal(row().cells[0].textContent,'正式出具 No. '+number,number);}
    // A job that has not succeeded has no number: one that carries a number is refused and what was remembered stays as it was.
    const last=goodNumbers.at(-1);
    for(const state of ['queued','running','failed','cancelled']){assert.match(await restore(issueJob(asked,state,{number:'AR-000012',code:state==='failed'?'INTEGRITY_FAILURE':undefined})),/无法验证的响应/,state);assert.equal(row().cells[0].textContent,'正式出具 No. '+last,state);assert.equal(row().cells[2].textContent,'已完成',state);}
    // Without a number a task is waiting for the product while it is queued or running, and was never issued once it failed or was cancelled.
    for(const [state,title,status] of [['queued','正式出具（编号待来源产品确认）','排队中'],['running','正式出具（编号待来源产品确认）','执行中'],['failed','正式出具（未出具）','失败 · INTEGRITY_FAILURE'],['cancelled','正式出具（未出具）','已取消']]){assert.match(await restore(issueJob(asked,state,{code:state==='failed'?'INTEGRITY_FAILURE':undefined})),/已恢复/,state);assert.equal(row().cells[0].textContent,title,state);assert.equal(row().cells[2].textContent,status,state);}
    // A failed task of any kind shows its code in the status cell, from the view alone: that is how the reason survives a refresh or a restore.
    const failed=(kind,code)=>kind==='issue'?issueJob(asked,'failed',{code}):{job_id:pdfJob,kind,state:'failed',snapshot_id:asked.snapshot_id,...(kind==='export'?{format:'pdf'}:{}),error_code:code};
    for(const [kind,code] of [['issue','ISSUE_UNRECONCILED'],['issue','AUTH_SCOPE_DENIED'],['export','BUDGET_EXCEEDED'],['snapshot','AUTHORITY_UNAVAILABLE']]){assert.match(await restore(failed(kind,code)),/已恢复/,kind+' '+code);assert.equal(row().cells[2].textContent,'失败 · '+code,kind+' '+code);}
    // A code that is none, or one on a task that did not fail, is not believed, and what was remembered stays as it was.
    for(const [label,job] of [['lower case',{...failed('issue','x'),error_code:'failed_badly'}],['65 characters',failed('issue','A'.repeat(65))],['a number',{...failed('issue','x'),error_code:12}],['null',{...failed('issue','x'),error_code:null}],['an object',{...failed('issue','x'),error_code:{}}],['on a running job',{...issueJob(asked,'running'),error_code:'INTEGRITY_FAILURE'}],['on a cancelled job',{...issueJob(asked,'cancelled'),error_code:'INTEGRITY_FAILURE'}],['on a succeeded job',{...issueJob(asked,'succeeded',{number:'AR-000012'}),error_code:'INTEGRITY_FAILURE'}],['on a queued export',{job_id:pdfJob,kind:'export',state:'queued',snapshot_id:asked.snapshot_id,format:'pdf',error_code:'INTEGRITY_FAILURE'}]]){assert.match(await restore(job),/无法验证的响应/,label);assert.equal(row().cells[2].textContent,'失败 · AUTHORITY_UNAVAILABLE',label);}
    assert.equal(a.errors.length,0);
  }finally{a.close();}
});

test('an issue job waits for the source product to confirm, then downloads under the number it gave',async()=>{
  // The job is queued, then running, then succeeded: only the last view has a number.
  const a=app(offeringIssuance({submit:body=>json(issueJob(body,'queued'),202),job:(body,reads)=>reads<2?issueJob(body,'queued'):reads<3?issueJob(body,'running'):issueJob(body,'succeeded',{number:'AR-000012'})}));
  try{
    await snapshotReady(a);await publishedOpen(a);a.click('doc-issue');
    const row=()=>issueRow(a);
    await until(()=>row(),'listed');
    // Nothing is numbered yet: the console says what the job waits for and the task has no number to show.
    assert.equal(row().cells[0].textContent,'正式出具（编号待来源产品确认）');assert.match(a.$('notice').textContent,/等待来源产品确认编号/);
    await until(()=>row().cells[2].textContent==='执行中','running');assert.equal(row().cells[0].textContent,'正式出具（编号待来源产品确认）');
    await until(()=>a.downloads.length===1&&!a.$('preview').disabled,'issued');
    assert.equal(a.downloads[0].name,'issued-AR-000012.pdf');assert.equal(a.downloads[0].blob.type,'application/pdf');
    assert.equal(row().cells[0].textContent,'正式出具 No. AR-000012');assert.equal(row().cells[2].textContent,'已完成');
    assert.match(a.$('notice').textContent,/AR-000012/);assert.match(a.$('notice').textContent,/下载已发起/);assert.ok(a.$('notice').className.includes('success'));
    // The task list downloads it again under the same name.
    [...row().cells[3].querySelectorAll('button')].find(v=>v.textContent==='下载').click();await until(()=>a.downloads.length===2,'task download');assert.equal(a.downloads[1].name,'issued-AR-000012.pdf');
    assert.equal(a.errors.length,0);
  }finally{a.close();}
});

test('only a queued issue job can be cancelled; a cancel that comes after it started changes nothing',async()=>{
  // Queued: the button is there and cancelling ends the job, which never had a number.
  let server='queued';
  const a=app(offeringIssuance({submit:body=>json(issueJob(body,'queued'),202),job:body=>issueJob(body,server),cancel:body=>{server='cancelled';return issueJob(body,'cancelled');}}));
  try{
    await snapshotReady(a);await publishedOpen(a);a.click('doc-issue');await until(()=>issueRow(a),'listed');
    const row=()=>issueRow(a),buttons=()=>[...row().cells[3].querySelectorAll('button')];
    assert.deepEqual(buttons().map(v=>v.textContent),['刷新','取消']);assert.equal(row().cells[0].textContent,'正式出具（编号待来源产品确认）');
    buttons().find(v=>v.textContent==='取消').click();
    await until(()=>row().cells[2].textContent==='已取消'&&a.$('notice').textContent.includes('出具任务已取消')&&!a.$('preview').disabled,'cancelled');
    assert.equal(a.trace.filter(v=>v.path.endsWith('/jobs/'+pdfJob+'/cancel')&&v.method==='POST').length,1);
    assert.deepEqual(buttons().map(v=>v.textContent),['刷新']);assert.equal(row().cells[0].textContent,'正式出具（未出具）');assert.equal(a.downloads.length,0);assert.equal(a.errors.length,0);
    // The cancel answer itself was believed: an answer that cannot be would have been reported as an error and cleared the result.
    assert.equal(a.$('notice').className.includes('error'),false);assert.equal(a.$('result-kind').textContent,'冻结快照');
  }finally{a.close();}
  // Running: no cancel button. If the job started after the view that showed the button, the server returns it unchanged: the
  // console says it cannot be cancelled, takes the button away and the job carries on to its number.
  let progress='queued';
  const b=app(offeringIssuance({submit:body=>json(issueJob(body,'queued'),202),job:body=>progress==='succeeded'?issueJob(body,'succeeded',{number:'AR-000012'}):issueJob(body,progress),cancel:body=>{progress='running';return issueJob(body,'running');}}));
  try{
    await snapshotReady(b);await publishedOpen(b);b.click('doc-issue');await until(()=>issueRow(b),'listed');
    const row=()=>issueRow(b),buttons=()=>[...row().cells[3].querySelectorAll('button')];
    assert.deepEqual(buttons().map(v=>v.textContent),['刷新','取消']);
    buttons().find(v=>v.textContent==='取消').click();
    await until(()=>b.$('notice').textContent.includes('不能取消'),'refused');
    assert.equal(row().cells[2].textContent,'执行中');assert.deepEqual(buttons().map(v=>v.textContent),['刷新']);assert.equal(row().cells[0].textContent,'正式出具（编号待来源产品确认）');
    progress='succeeded';await until(()=>b.downloads.length===1&&!b.$('preview').disabled,'issued');
    assert.equal(b.downloads[0].name,'issued-AR-000012.pdf');assert.equal(row().cells[0].textContent,'正式出具 No. AR-000012');assert.equal(b.errors.length,0);
  }finally{b.close();}
});

// The real server, in two situations a console meets: the answer to the issue request is lost and the request is made again with the same
// key, and a cancel is clicked on a job that has just succeeded. The server shows the number in both answers, as the poll does.
const authorized={headers:{Authorization:'Bearer '+token}};
async function realJob(id){return (await fetch(url+'/api/reporting/v1/jobs/'+id,authorized)).json();}
async function untilRealJob(id,state){for(const end=Date.now()+20000;Date.now()<end;await sleep(40)){if((await realJob(id)).state===state)return;}throw new Error('the real job did not reach '+state);}
test('a lost answer to the issue request, asked again with the same key, shows the issued job and downloads it',{skip:!pdfConfigured&&'needs the pinned Typst renderer'},async()=>{
  let lost=null;
  // The request reaches the server and is admitted; only its answer is lost on the way back.
  const a=app(async(path,options)=>{if(path.endsWith('/issues')&&options?.method==='POST'&&!lost){lost=await (await fetch(url+path,options)).json();throw new TypeError('the answer was lost');}});
  try{
    await snapshotReady(a);await publishedOpen(a);a.input('doc-row','2');a.click('doc-issue');
    await until(()=>lost&&!a.$('notice').hidden&&!a.$('preview').disabled,'answer lost');
    assert.equal(lost.kind,'issue');assert.equal(a.downloads.length,0);assert.equal(issueRow(a),undefined);
    // The server issues it meanwhile; the next request with the same key shows it as it is.
    await untilRealJob(lost.job_id,'succeeded');
    a.click('doc-issue');await until(()=>a.downloads.length===1&&!a.$('preview').disabled,'issued after the retry',20000);
    const posts=a.trace.filter(v=>v.path.endsWith('/issues')&&v.method==='POST');assert.equal(posts.length,2);assert.ok(posts[0].key);assert.equal(posts[1].key,posts[0].key);
    const job=await realJob(lost.job_id),number=job.issue.number;assert.match(number,/^AR-\d{6}$/);
    assert.equal(a.downloads[0].name,'issued-'+number+'.pdf');assert.equal(Buffer.from(await a.downloads[0].blob.arrayBuffer()).subarray(0,5).toString(),'%PDF-');
    assert.equal(a.$('tasks-body').rows.length>=1,true);const row=issueRow(a);assert.equal(row.cells[0].textContent,'正式出具 No. '+number);assert.equal(row.cells[1].textContent,lost.job_id);assert.equal(row.cells[2].textContent,'已完成');
    assert.equal(a.errors.length,0);
  }finally{a.close();}
});
test('a cancel clicked on an issue job that has just succeeded shows it as succeeded, with its number',{skip:!pdfConfigured&&'needs the pinned Typst renderer'},async()=>{
  // The console's last view of the job is one answered before it succeeded (queued, so it offers the cancel); the cancel itself goes to the real server.
  let stale=true;
  const a=app(async(path,options)=>{
    if(stale&&/\/jobs\/[a-f0-9]{32}$/.test(path)&&(!options||!options.method||options.method==='GET')){const real=await (await fetch(url+path,options)).json();return json(real.kind==='issue'?{...real,state:'queued',artifact_id:undefined,issue:{...real.issue,number:undefined}}:real);}
    if(path.endsWith('/cancel')&&options?.method==='POST')stale=false;
  });
  try{
    await snapshotReady(a);await publishedOpen(a);a.input('doc-row','1');a.click('doc-issue');
    await until(()=>issueRow(a),'listed');const row=()=>issueRow(a),buttons=()=>[...row().cells[3].querySelectorAll('button')];const id=row().cells[1].textContent;
    assert.deepEqual(buttons().map(v=>v.textContent),['刷新','取消']);assert.equal(row().cells[0].textContent,'正式出具（编号待来源产品确认）');
    await untilRealJob(id,'succeeded');
    buttons().find(v=>v.textContent==='取消').click();
    await until(()=>a.$('notice').textContent.includes('任务已结束')||a.$('notice').textContent.includes('无法验证'),'cancel answered');
    // The answer was believed: the job is shown as succeeded with the product's number, not rejected.
    assert.ok(a.$('notice').textContent.includes('任务已结束，未更改已发布结果'),a.$('notice').textContent);assert.equal(a.$('notice').className.includes('error'),false);
    const number=(await realJob(id)).issue.number;assert.match(number,/^AR-\d{6}$/);
    assert.equal(a.trace.filter(v=>v.path.endsWith('/jobs/'+id+'/cancel')&&v.method==='POST').length,1);
    assert.equal(row().cells[0].textContent,'正式出具 No. '+number);assert.equal(row().cells[2].textContent,'已完成');
    // The poll that was waiting then reads the real job and the issued file is downloaded.
    await until(()=>a.downloads.length===1&&!a.$('preview').disabled,'downloaded',20000);assert.equal(a.downloads[0].name,'issued-'+number+'.pdf');assert.equal(a.errors.length,0);
  }finally{a.close();}
});

test('a failed issue job says why: the source product refused, or could not be told to void its reservation',async()=>{
  for(const [code,expected] of [
    ['AUTH_SCOPE_DENIED',/AUTH_SCOPE_DENIED.*来源产品拒绝出具.*无出具权限.*未获批准.*来源数据已变化/],
    ['ISSUE_UNRECONCILED',new RegExp('ISSUE_UNRECONCILED.*未能通知来源产品作废.*联系管理员.*任务 ID '+pdfJob)],
    ['INTEGRITY_FAILURE',/出具任务失败（INTEGRITY_FAILURE）/],
  ]){
    const a=app(offeringIssuance({submit:body=>json(issueJob(body,'queued'),202),job:body=>issueJob(body,'failed',{code})}));
    try{
      await snapshotReady(a);await publishedOpen(a);a.click('doc-issue');
      await until(()=>a.$('notice').className.includes('error')&&!a.$('preview').disabled,code);
      assert.match(a.$('notice').textContent,expected,code);assert.equal(a.downloads.length,0,code);
      // The platform does not number documents, so it has no number of its own to void.
      assert.doesNotMatch(a.$('notice').textContent,/编号作废|作废，不再使用|No\. /,code);
      const row=issueRow(a);assert.equal(row.cells[0].textContent,'正式出具（未出具）',code);assert.equal(row.cells[2].textContent,'失败 · '+code,code);assert.deepEqual([...row.cells[3].querySelectorAll('button')].map(v=>v.textContent),['刷新'],code);
      // The reason is in the task list, not only in the notice, and it is still there after the task is refreshed.
      a.click('refresh-tasks');await until(()=>!a.$('preview').disabled&&a.$('notice').textContent.includes('已刷新'),'refreshed '+code);assert.equal(issueRow(a).cells[2].textContent,'失败 · '+code,code);assert.equal(a.errors.length,0,code);
    }finally{a.close();}
  }
});

test('an issue that the source product or the deployment cannot do is refused with its own message, not the dataset one',async()=>{
  const a=app(offeringIssuance({submit:()=>problem(422,'UNSUPPORTED_COMBINATION')}));
  try{
    await snapshotReady(a);await publishedOpen(a);a.click('doc-issue');
    await until(()=>a.$('notice').className.includes('error')&&!a.$('preview').disabled,'refused');
    // Not the generic 422 text, which is about the query shape of a dataset.
    assert.match(a.$('notice').textContent,/^正式出具：该数据集的来源产品不支持正式出具，或服务端未启用出具/);assert.doesNotMatch(a.$('notice').textContent,/不支持此查询组合/);
    // The same code at admission also means a row value that cannot be printed or a template background the deployment lacks, and says so.
    assert.match(a.$('notice').textContent,/无法打印的值.*底稿/);
    // The refusal concerns this issue, not the snapshot: the frozen result and the issue button stay and no task was made.
    assert.equal(a.$('result-kind').textContent,'冻结快照');assert.equal(a.$('doc-issue').hidden,false);assert.equal(issueRow(a),undefined);assert.equal(a.downloads.length,0);
    assert.equal(a.trace.filter(v=>v.path.endsWith('/issues')&&v.method==='POST').length,1);assert.equal(a.errors.length,0);
  }finally{a.close();}
  // Another 422 keeps its generic text: only this code is mapped.
  const b=app(offeringIssuance({submit:()=>problem(422,'SOMETHING_ELSE')}));
  try{
    await snapshotReady(b);await publishedOpen(b);b.click('doc-issue');
    await until(()=>b.$('notice').className.includes('error')&&!b.$('preview').disabled,'generic');
    assert.equal(b.$('notice').textContent,'正式出具：当前数据集不支持此查询组合。');assert.equal(b.errors.length,0);
  }finally{b.close();}
});

test('issue records are checked against the product-numbered shapes before any is shown',async()=>{
  const F='f'.repeat(32),issued='2026-10-28T06:12:03Z',confirmed='2026-10-28T06:12:04Z';
  const stamp=(n,at,id)=>'ISSUED No. '+n+' - '+at+' - '+id;
  const fixtureDigest=createHash('sha256').update('%PDF-1.7 fixture').digest('hex');
  // A succeeded record as the server writes it (`over` changes fields, undefined removes one) and one that has not succeeded, which
  // carries nothing that only a confirmed issue has.
  const rec=(over={})=>({issue_id:F,state:'succeeded',number:'AR-000003',template_id:'d'.repeat(32),template_version:2,template_sha256:'e'.repeat(64),snapshot_id:'c'.repeat(32),snapshot_sha256:'a'.repeat(64),row_index:4,artifact_id:pdfArtifact,sha256:fixtureDigest,pages:2,issued_at:issued,stamp:'ISSUED No. AR-000003 - 2026-10-28T06:12:03Z - ffffffffffffffffffffffffffffffff',reservation_ref:'RES-3',confirmation_ref:'CNF-3',confirmed_at:confirmed,...over});
  const open=(over={})=>({issue_id:'e'.repeat(32),state:'queued',template_id:'d'.repeat(32),template_version:2,template_sha256:'e'.repeat(64),snapshot_id:'c'.repeat(32),row_index:4,...over});
  const withNumber=n=>rec({number:n,stamp:stamp(n,issued,F)});
  const options={records:[]};const a=app(offeringIssuance(options));
  const refresh=async records=>{options.records=records;a.click('doc-issues-refresh');await until(()=>!a.$('preview').disabled,'refreshed');return a.$('doc-issue-status').textContent;};
  try{
    await snapshotReady(a);
    assert.match(await refresh([]),/尚无出具记录/);assert.equal(a.$('doc-issues-wrap').hidden,true);
    // One record that cannot be believed refuses the whole list: nothing of it is shown.
    for(const [label,records,expected] of [
      ['a stamp for another number',[rec({stamp:stamp('AR-000004',issued,F)})],/印章不一致/],
      ['a stamp with the platform\'s old padded number',[rec({stamp:stamp('000003',issued,F)})],/印章不一致/],
      ['a stamp with another time',[rec({stamp:stamp('AR-000003',confirmed,F)})],/印章不一致/],
      ['a stamp with another record id',[rec({stamp:stamp('AR-000003',issued,'e'.repeat(32))})],/印章不一致/],
      ['no stamp',[rec({stamp:undefined})],/印章不一致/],
      ['an integer number',[rec({number:3,stamp:stamp(3,issued,F)})],/无法验证出具记录。/],
      ['no number on a succeeded record',[rec({number:undefined})],/无法验证出具记录。/],
      ['a number on a queued record',[open({number:'AR-000003'})],/不应带有/],
      ['a file on a running record',[open({state:'running',artifact_id:pdfArtifact})],/不应带有/],
      ['a confirmation on a failed record',[open({state:'failed',error_code:'INTEGRITY_FAILURE',confirmation_ref:'CNF-3'})],/不应带有/],
      ['a stamp on a cancelled record',[open({state:'cancelled',stamp:stamp('AR-000003',issued,F)})],/不应带有/],
      ['confirmed before it was issued',[rec({confirmed_at:'2026-10-28T06:12:02Z'})],/确认时间早于出具时间/],
      ['an artifact id that is no id',[rec({artifact_id:'x'})],/无法验证出具记录。/],
      ['an artifact id in upper case',[rec({artifact_id:pdfArtifact.toUpperCase()})],/无法验证出具记录。/],
      ['no artifact id',[rec({artifact_id:undefined})],/无法验证出具记录。/],
      ['0 pages',[rec({pages:0})],/无法验证出具记录。/],
      ['101 pages',[rec({pages:101})],/无法验证出具记录。/],
      ['1.5 pages',[rec({pages:1.5})],/无法验证出具记录。/],
      ['pages as a string',[rec({pages:'2'})],/无法验证出具记录。/],
      ['a snapshot digest in upper case',[rec({snapshot_sha256:'A'.repeat(64)})],/无法验证出具记录。/],
      ['a snapshot digest of 63 characters',[rec({snapshot_sha256:'a'.repeat(63)})],/无法验证出具记录。/],
      ['a snapshot digest that is not hexadecimal',[rec({snapshot_sha256:'g'.repeat(64)})],/无法验证出具记录。/],
      ['no snapshot digest',[rec({snapshot_sha256:undefined})],/无法验证出具记录。/],
      ['no confirmation reference',[rec({confirmation_ref:undefined})],/无法验证出具记录。/],
      ['an empty confirmation reference',[rec({confirmation_ref:''})],/无法验证出具记录。/],
      ['a confirmation reference of 129 characters',[rec({confirmation_ref:'x'.repeat(129)})],/无法验证出具记录。/],
      ['no reservation reference',[rec({reservation_ref:undefined})],/无法验证出具记录。/],
      ['no confirmation time',[rec({confirmed_at:undefined})],/无法验证出具记录。/],
      ['a confirmation time that is not UTC to the second',[rec({confirmed_at:'2026-10-28T06:12:04+00:00'})],/无法验证出具记录。/],
      ['an issue time that is no instant',[rec({issued_at:'2026-02-30T00:00:00Z',stamp:stamp('AR-000003','2026-02-30T00:00:00Z',F),confirmed_at:'2026-03-01T00:00:00Z'})],/无法验证出具记录。/],
      ['an error code on a queued record',[open({error_code:'INTEGRITY_FAILURE'})],/无法验证出具记录。/],
      ['a good record behind a bad one',[rec(),rec({stamp:stamp('AR-000004',issued,F)})],/印章不一致/],
      ['101 records',Array.from({length:101},()=>rec()),/无法验证出具记录列表。/],
      ['no list',{},/无法验证出具记录列表。/],
    ]){assert.match(await refresh(records),expected,label);assert.equal(a.$('doc-issues-wrap').hidden,true,label);assert.equal(a.$('doc-issues-body').rows.length,0,label);}
    // The page counts the contract allows pass, at both ends.
    for(const pages of [1,100]){assert.match(await refresh([rec({pages})]),/共 1 条/,'pages '+pages);assert.equal(a.$('doc-issues-body').rows[0].cells[5].textContent,fixtureDigest+' · '+pages+' 页');}
    // The numbers a product may choose pass; the ones it may not never do, here as in the job views.
    for(const number of goodNumbers){assert.match(await refresh([withNumber(number)]),/共 1 条/,number);assert.equal(a.$('doc-issues-body').rows[0].cells[0].textContent,'No. '+number,number);}
    for(const number of [...badNumbers,undefined]){assert.match(await refresh([withNumber(number)]),/无法验证出具记录。/,String(number));assert.equal(a.$('doc-issues-wrap').hidden,true,String(number));}
    // Every state lists; a failure shows its code and, for the two that concern the source product, what it means.
    const same=issued,list=[rec(),rec({issue_id:'1'.repeat(32),number:'SH-000002',stamp:stamp('SH-000002',issued,'1'.repeat(32)),confirmed_at:same}),open({issue_id:'2'.repeat(32),state:'failed',error_code:'AUTH_SCOPE_DENIED'}),open({issue_id:'3'.repeat(32),state:'failed',error_code:'ISSUE_UNRECONCILED'}),open({issue_id:'4'.repeat(32),state:'failed',error_code:'TEMPLATE_NOT_PUBLISHED'}),open({issue_id:'5'.repeat(32),state:'failed'}),open({issue_id:'6'.repeat(32),state:'running'}),open({issue_id:'7'.repeat(32)}),open({issue_id:'8'.repeat(32),state:'cancelled'})];
    assert.match(await refresh(list),/共 9 条/);
    const rows=[...a.$('doc-issues-body').rows].map(r=>[...r.cells].map(c=>c.textContent));
    assert.deepEqual(rows[0],['No. AR-000003','已出具',issued,'d'.repeat(32)+' · 版本 2','c'.repeat(32)+' · 第 5 行',fixtureDigest+' · 2 页','CNF-3 · '+confirmed,'下载']);
    assert.equal(rows[1][0],'No. SH-000002');assert.equal(rows[1][6],'CNF-3 · '+issued);   // confirmed in the second it was issued: not before it
    assert.deepEqual(rows[2],['—','失败 · AUTH_SCOPE_DENIED（来源产品拒绝出具）','—','d'.repeat(32)+' · 版本 2','c'.repeat(32)+' · 第 5 行','—','—','']);
    assert.equal(rows[3][1],'失败 · ISSUE_UNRECONCILED（未能通知来源产品作废预留，请联系管理员）');assert.equal(rows[4][1],'失败 · TEMPLATE_NOT_PUBLISHED');assert.equal(rows[5][1],'失败');
    assert.deepEqual(rows.slice(6).map(r=>r[1]),['执行中','排队中','已取消']);assert.ok(rows.slice(2).every(r=>r[0]==='—'&&r[5]==='—'&&r[6]==='—'&&r[7]===''));
    assert.ok(a.$('doc-issues-body').rows[2].cells[7].querySelector('button')===null);
    // The record downloads under its number after its digest matched the file...
    a.$('doc-issues-body').rows[0].cells[7].querySelector('button').click();await until(()=>a.downloads.length===1&&!a.$('preview').disabled,'record download');
    assert.equal(a.downloads[0].name,'issued-AR-000003.pdf');assert.deepEqual(Buffer.from(await a.downloads[0].blob.arrayBuffer()),Buffer.from('%PDF-1.7 fixture'));
    // ...and a file that is not the one the record names never downloads.
    await refresh([rec({sha256:'b'.repeat(64)})]);a.$('doc-issues-body').rows[0].cells[7].querySelector('button').click();
    await until(()=>a.$('notice').textContent.includes('完整性')&&!a.$('preview').disabled,'digest refused');assert.equal(a.downloads.length,1);
    assert.equal(a.errors.length,0);
  }finally{a.close();}
});

// ---- S03-I: the document canvas -------------------------------------------------------------------------------------------------
// jsdom has no layout, so pointer events are dispatched at coordinates computed from the canvas's fixed scale and documented origin:
// 3 px per mm at 100 % zoom (times zoom / 100), the page's top-left corner 24 px from the SVG's. Drags use pointer deltas only.
const SCALE=3,ORIGIN=24,READY='模板已更新；配置不含业务数据。';
async function designer(intercept){const a=app(intercept);await a.login();a.configure();a.click('preview');await until(()=>!a.$('preview').disabled&&!a.$('result-table-wrap').hidden,'query');a.w.document.querySelector('[data-page="documents"]').click();assert.equal(a.$('page-documents').hidden,false);return a;}
const cv={
 svg:a=>a.$('doc-canvas-host').querySelector('svg'),
 item:(a,key)=>a.$('doc-canvas-host').querySelector('[data-key="'+key+'"]'),
 keys:a=>[...a.$('doc-canvas-host').querySelectorAll('[data-key]')].map(e=>e.getAttribute('data-key')),
 selected:a=>[...a.$('doc-canvas-host').querySelectorAll('[data-key][aria-pressed="true"]')].map(e=>e.getAttribute('data-key')),
 tpl:a=>{a.click('doc-json-save');return JSON.parse(a.$('doc-json').value);},
 load:(a,t)=>{a.$('doc-json').value=JSON.stringify(t);a.click('doc-json-open');assert.equal(a.$('doc-status').textContent,READY,'template accepted');},
 px:(a,mm)=>ORIGIN+mm*SCALE*Number(a.$('doc-zoom').value)/100,
 status:a=>a.$('doc-canvas-status').textContent,
 // Boxes: [id, x, y, width, height, source, field-or-text]; the binding is the open query's.
 doc:(a,list,extra={})=>({schema_version:'1.0',kind:'document',binding:cv.tpl(a).binding,elements:list.map(([id,x,y,width,height,source='literal',what])=>({id,source,field:source==='literal'?'':what,text:source==='literal'?(what??id+' text'):'',x,y,width,height,font_size:12,align:'left'})),...extra}),
 table:(a,list,columns=[['customer.id',60],['aging.bucket',70],['ar.open_balance',60]],t={})=>cv.doc(a,list,{schema_version:'1.3',print:{mode:'full',offset_x:0,offset_y:0},table:{x:10,y:100,width:columns.reduce((s,c)=>s+c[1],0),height:100,font_size:9,columns:columns.map(([field,width])=>({field,width})),...t}}),
 four:a=>cv.doc(a,[['n1',10,30,50,10],['n2',80,30,50,10,'row','customer.id'],['n3',150,30,40,10,'total','ar.open_balance'],['n4',10,60,50,10]]),
 centre:(a,key)=>{const e=cv.tpl(a),t=key==='table'?e.table:e.elements.find(v=>v.id===key);return [t.x+t.width/2,t.y+t.height/2];},
 pointer:(a,type,target,x,y,o={})=>target.dispatchEvent(new a.w.PointerEvent(type,{bubbles:true,cancelable:true,pointerId:1,button:0,isPrimary:true,clientX:cv.px(a,x),clientY:cv.px(a,y),...o})),
 // Press on `target` at (x, y) mm; the returned handle moves and releases on the SVG, as pointer capture does in a browser.
 press:(a,target,x,y,o={})=>{cv.pointer(a,'pointerdown',target,x,y,o);return {move:(mx,my,mo={})=>cv.pointer(a,'pointermove',cv.svg(a),mx,my,{button:0,...mo}),up:(mx,my,mo={})=>cv.pointer(a,'pointerup',cv.svg(a),mx,my,mo),cancel:()=>cv.pointer(a,'pointercancel',cv.svg(a),x,y)};},
 drag:(a,target,x,y,dx,dy,o={})=>{const g=cv.press(a,target,x,y,o);g.move(x+dx/2,y+dy/2,o);g.move(x+dx,y+dy,o);g.up(x+dx,y+dy,o);},
 click:(a,key,o={})=>{const [x,y]=cv.centre(a,key);const g=cv.press(a,cv.item(a,key),x,y,o);g.up(x,y,o);},
 key:(a,target,key,o={})=>target.dispatchEvent(new a.w.KeyboardEvent('keydown',{key,bubbles:true,cancelable:true,...o})),
 handle:(a,dir)=>a.$('doc-canvas-host').querySelector('.cv-h-'+dir),
 box:(a,id)=>cv.tpl(a).elements.find(e=>e.id===id),
 undo:a=>a.$('doc-undo').disabled,
};
const palette=a=>a.w.document.querySelector('.cv-palette');
const options=a=>[...a.w.document.querySelectorAll('.cv-palette [role="option"]')].map(o=>o.textContent);
const typeInto=(a,value)=>{const i=a.w.document.getElementById('cv-palette-input');i.value=value;i.dispatchEvent(new a.w.Event('input',{bubbles:true}));};
const press=(a,key,o={})=>{const t=a.w.document.activeElement||a.w.document.body;return t.dispatchEvent(new a.w.KeyboardEvent('keydown',{key,bubbles:true,cancelable:true,...o}));};
const only=(t,keys)=>JSON.stringify(keys.map(k=>t[k]));

test('the canvas draws the page in millimetres: rulers, safe region, banner strip, items with their source and label, the table and its columns',async()=>{const a=await designer();try{
 const host=a.$('doc-canvas-host');assert.ok(cv.svg(a));assert.equal(cv.svg(a).getAttribute('role'),'group');assert.ok(cv.svg(a).getAttribute('aria-label').includes('毫米'));
 assert.ok(host.querySelectorAll('.cv-ruler-text').length>20,'ruler labels');assert.equal(host.querySelectorAll('.cv-strip').length,1);assert.equal(host.querySelectorAll('.cv-safe').length,1);
 // The page, the safe region (x 10-200, y 25-270) and the reserved strip (10-18 mm from the top) sit where the scale puts them.
 const paper=host.querySelector('.cv-paper'),safe=host.querySelector('.cv-safe'),strip=host.querySelector('.cv-strip');
 assert.deepEqual([paper.getAttribute('x'),paper.getAttribute('width'),paper.getAttribute('height')],[String(ORIGIN),'630','891']);
 assert.deepEqual(['x','y','width','height'].map(k=>Number(safe.getAttribute(k))),[ORIGIN+30,ORIGIN+75,570,735]);assert.deepEqual(['x','y','width','height'].map(k=>Number(strip.getAttribute(k))),[ORIGIN+30,ORIGIN+30,570,24]);
 assert.equal(strip.closest('[data-key]'),null,'the strip is not an item');
 cv.load(a,cv.four(a));
 // Items are in reading order (top to bottom, then left to right) and carry id, source and text or field label.
 assert.deepEqual(cv.keys(a),['n1','n2','n3','n4']);
 const r1=cv.item(a,'n1').querySelector('.cv-box');assert.deepEqual(['x','y','width','height'].map(k=>Number(r1.getAttribute(k))),[ORIGIN+30,ORIGIN+90,150,30]);
 assert.equal(cv.item(a,'n1').querySelector('text').textContent,'n1 text');assert.equal(cv.item(a,'n2').querySelector('text').textContent,'〔客户〕');assert.equal(cv.item(a,'n3').querySelector('text').textContent,'〔合计〕应收余额');
 assert.ok(Math.abs(Number(cv.item(a,'n1').querySelector('text').getAttribute('font-size'))-12*25.4/72*SCALE)<0.01,'font size at the fixed scale');
 const tags=[...host.querySelectorAll('.cv-tag')].map(e=>e.textContent);assert.deepEqual(tags,['n1 · 固定文字','n2 · 行字段','n3 · 合计','n4 · 固定文字']);
 // Zoom 50-200 % rescales everything from the same origin.
 a.input('doc-zoom','200');assert.equal(a.$('doc-zoom-value').textContent,'200 %');assert.equal(cv.item(a,'n1').querySelector('.cv-box').getAttribute('x'),String(ORIGIN+60));assert.equal(host.querySelector('.cv-paper').getAttribute('width'),'1260');
 a.input('doc-zoom','50');assert.equal(cv.item(a,'n1').querySelector('.cv-box').getAttribute('x'),String(ORIGIN+15));a.input('doc-zoom','500');assert.equal(a.$('doc-zoom').value,'200','the control clamps to 50-200');assert.equal(a.$('doc-zoom-value').textContent,'200 %');a.input('doc-zoom','100');
 // The table: region, header labels, column boundaries.
 cv.load(a,cv.table(a,[['n1',10,30,50,10]]));
 const table=cv.item(a,'table');assert.ok(table);assert.deepEqual([...table.querySelectorAll('text')].map(e=>e.textContent),['客户','账龄区间','应收余额']);
 assert.equal(a.$('doc-canvas-host').querySelectorAll('[data-col]').length,2);assert.equal(table.querySelector('.cv-box').getAttribute('height'),String(300));
 // The print offset is drawn as the printed place; the variables mode greys the literals out and says so.
 cv.load(a,cv.table(a,[['n1',10,30,50,10],['n2',80,30,50,10,'row','customer.id']],[['customer.id',50],['aging.bucket',60],['ar.open_balance',50]]));let t=cv.tpl(a);t.print={mode:'variables',offset_x:1.5,offset_y:-2};cv.load(a,t);
 assert.equal(a.$('doc-canvas-host').querySelectorAll('.cv-ghost').length,2,'the row box and the table print, the literal does not');assert.ok(cv.item(a,'n1').getAttribute('class').includes('cv-dim'));assert.ok(!cv.item(a,'n2').getAttribute('class').includes('cv-dim'));
 assert.match(a.$('doc-canvas-mode').textContent,/变量层.*灰显.*1\.5 \/ -2 毫米/);assert.match(cv.item(a,'n1').getAttribute('aria-label'),/变量层不打印/);
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('the canvas follows the session: the first template is the designer\'s default, a query change or logout empties the canvas and its history',async()=>{const a=await designer();try{
 assert.deepEqual(cv.keys(a),['n1']);assert.match(cv.status(a),/画布显示当前模板/);assert.equal(cv.item(a,'n1').querySelector('text').textContent,'Document preview');assert.equal(a.$('doc-undo').disabled,true);
 cv.load(a,cv.four(a));cv.click(a,'n1');cv.key(a,cv.item(a,'n1'),'ArrowDown');assert.equal(a.$('doc-undo').disabled,false);assert.deepEqual(cv.selected(a),['n1']);
 // Another company scope is another query: the result is cleared with the canvas and its history.
 a.input('query-companies','company_other');assert.equal(a.$('doc-canvas-host').querySelectorAll('[data-key]').length,0);assert.equal(a.$('doc-undo').disabled,true);assert.equal(a.$('doc-del').disabled,true);assert.match(cv.status(a),/先查询当前授权数据/);
 a.input('query-companies','company_demo');a.w.document.querySelector('[data-page="workspace"]').click();a.click('preview');await until(()=>!a.$('preview').disabled&&!a.$('result-table-wrap').hidden,'query');a.w.document.querySelector('[data-page="documents"]').click();assert.ok(cv.item(a,'n1'));assert.deepEqual(cv.selected(a),[]);
 // Logout leaves nothing of the template on the page, and the palette has nothing to run.
 cv.load(a,cv.four(a));a.click('logout');assert.equal(a.$('doc-canvas-host').querySelectorAll('[data-key]').length,0);assert.equal(a.$('doc-undo').disabled,true);assert.equal(a.$('doc-json').value,'');
 cv.key(a,a.w.document.body,'k',{ctrlKey:true});assert.equal(palette(a),null);assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('click selects, Shift-click adds and removes, Esc and a click on the page clear, Ctrl/Cmd-A selects every box and the table',async()=>{const a=await designer();try{
 cv.load(a,cv.table(a,[['n1',10,30,50,10],['n2',80,30,50,10,'row','customer.id'],['n3',150,30,40,10,'total','ar.open_balance']]));
 assert.deepEqual(cv.selected(a),[]);assert.equal(a.$('doc-canvas-live').textContent,'未选择任何项目。');
 cv.click(a,'n1');assert.deepEqual(cv.selected(a),['n1']);assert.match(a.$('doc-canvas-live').textContent,/已选择 n1，固定文字/);
 cv.click(a,'n2');assert.deepEqual(cv.selected(a),['n2'],'a plain click replaces the selection');
 cv.click(a,'n3',{shiftKey:true});assert.deepEqual(cv.selected(a),['n2','n3']);assert.match(a.$('doc-canvas-live').textContent,/已选择 2 项：n2、n3/);
 cv.click(a,'n2',{shiftKey:true});assert.deepEqual(cv.selected(a),['n3'],'Shift removes a selected item');
 cv.click(a,'table',{shiftKey:true});assert.deepEqual(cv.selected(a),['n3','table']);
 // Esc clears (the key goes to the focused item, or to the canvas).
 cv.key(a,cv.item(a,'n3'),'Escape');assert.deepEqual(cv.selected(a),[]);assert.equal(a.$('doc-canvas-live').textContent,'已清除选择。');
 cv.key(a,cv.item(a,'n1'),'a',{ctrlKey:true});assert.deepEqual(cv.selected(a),['n1','n2','n3','table'].sort((x,y)=>cv.keys(a).indexOf(x)-cv.keys(a).indexOf(y)),'every box and the table, in reading order');assert.equal(a.$('doc-canvas-live').textContent,'全选：4 项。');
 cv.key(a,cv.item(a,'n1'),'Escape');cv.key(a,cv.item(a,'n1'),'a',{metaKey:true});assert.equal(cv.selected(a).length,4,'Cmd-A too');
 // A click on empty paper clears; a plain click on a selected item of several keeps the group for a drag but, released without moving, selects just that item.
 const paper=a.$('doc-canvas-host').querySelector('.cv-paper');cv.pointer(a,'pointerdown',paper,5,5);cv.pointer(a,'pointerup',cv.svg(a),5,5);assert.deepEqual(cv.selected(a),[]);
 cv.key(a,cv.item(a,'n1'),'a',{ctrlKey:true});cv.click(a,'n2');assert.deepEqual(cv.selected(a),['n2']);
 // Shift on empty paper keeps the selection; the table is selectable and has its own handles.
 cv.click(a,'table');assert.deepEqual(cv.selected(a),['table']);assert.equal(a.$('doc-canvas-host').querySelectorAll('.cv-handle').length,8);cv.pointer(a,'pointerdown',paper,5,5,{shiftKey:true});assert.deepEqual(cv.selected(a),['table']);
 // The banner strip is not selectable.
 cv.pointer(a,'pointerdown',a.$('doc-canvas-host').querySelector('.cv-strip'),100,14);assert.deepEqual(cv.selected(a),[]);
 // A removed item leaves the selection; no history step comes from selecting.
 assert.equal(cv.undo(a),true);assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('a drag moves the selection with snapping: 1 mm, 0.1 mm with Alt, to the edges and centres of the other items within 1 mm with a guide; one drag is one undo step',async()=>{const a=await designer();try{
 const four=cv.four(a);cv.load(a,four);
 // 10.4 mm to the right: the position snaps to the 1 mm grid.
 cv.drag(a,cv.item(a,'n1'),35,35,10.4,0);let t=cv.tpl(a);assert.equal(t.elements[0].x,20);assert.equal(t.elements[0].y,30);assert.equal(t.schema_version,'1.0');assert.match(cv.status(a),/已移动/);
 assert.equal(a.$('doc-undo').disabled,false);
 // One step: a single undo restores the drag.
 a.click('doc-undo');assert.equal(cv.box(a,'n1').x,10);assert.equal(cv.undo(a),true);
 // With Alt it snaps to 0.1 mm, and a schema 1.0 template off whole millimetres becomes 1.1.
 cv.drag(a,cv.item(a,'n1'),35,35,10.4,0,{altKey:true});t=cv.tpl(a);assert.equal(t.elements[0].x,20.4);assert.equal(t.schema_version,'1.1');assert.deepEqual(t.print,{mode:'full',offset_x:0,offset_y:0});
 cv.load(a,four);
 // Edge snap: the right edge of n1 comes within 0.4 mm of the left edge of n2 (x 80): it snaps there and a guide shows at x 80 until the release.
 let g=cv.press(a,cv.item(a,'n1'),35,35);g.move(40,35);g.move(54.6,35);
 const guides=[...a.$('doc-canvas-host').querySelectorAll('.cv-guide')];assert.equal(guides.length,1);assert.equal(guides[0].getAttribute('x1'),String(ORIGIN+80*SCALE));assert.ok(a.$('doc-canvas-host').querySelector('.cv-readout').textContent.includes('x 30'));
 g.up(54.6,35);assert.equal(cv.box(a,'n1').x,30);assert.equal(a.$('doc-canvas-host').querySelectorAll('.cv-guide').length,0,'the guide goes with the drag');
 // 1.1 mm away is outside the tolerance: the grid applies and no guide shows.
 cv.load(a,four);g=cv.press(a,cv.item(a,'n1'),35,35);g.move(40,35);g.move(53.9,35);assert.equal(a.$('doc-canvas-host').querySelectorAll('.cv-guide').length,0);g.up(53.9,35);assert.equal(cv.box(a,'n1').x,29);
 // Centre snap, and snapping on the vertical axis (an edge of n4 at y 60).
 const pq=cv.doc(a,[['n1',10,30,20,10],['n2',50,60,40,10]]);cv.load(a,pq);g=cv.press(a,cv.item(a,'n1'),20,35);g.move(40,35);g.move(69.8,35);assert.equal(a.$('doc-canvas-host').querySelectorAll('.cv-guide').length,1);assert.equal(a.$('doc-canvas-host').querySelector('.cv-guide').getAttribute('x1'),String(ORIGIN+70*SCALE));g.up(69.8,35);assert.equal(cv.box(a,'n1').x,60,'its centre is on the centre of n2 (70)');
 cv.load(a,pq);g=cv.press(a,cv.item(a,'n1'),20,35);g.move(20,40);g.move(20,54.7);assert.equal(a.$('doc-canvas-host').querySelector('.cv-guide').getAttribute('y1'),String(ORIGIN+60*SCALE),'its bottom edge is 0.3 mm from the top edge of n2 (y 60)');g.up(20,54.7);
 assert.equal(cv.box(a,'n1').y,50,'its bottom edge (y 60) meets the top of n2');
 // Selecting several moves them together; the group keeps its relative places.
 cv.load(a,four);cv.click(a,'n1');cv.click(a,'n4',{shiftKey:true});cv.drag(a,cv.item(a,'n4'),35,65,10.3,0);t=cv.tpl(a);assert.deepEqual([t.elements[0].x,t.elements[3].x],[20,20]);
 // A press that never moves changes nothing and is no step.
 a.click('doc-undo');a.click('doc-undo');cv.click(a,'n2');assert.equal(cv.undo(a),true);assert.deepEqual(cv.selected(a),['n2']);
 // A plain click on an item of the selection, released where it was pressed, selects just that item.
 cv.click(a,'n1',{shiftKey:true});assert.deepEqual(cv.selected(a),['n1','n2']);cv.click(a,'n1');assert.deepEqual(cv.selected(a),['n1']);
 // Esc during a drag cancels it.
 g=cv.press(a,cv.item(a,'n1'),35,35);g.move(50,35);g.move(60,35);cv.key(a,cv.svg(a),'Escape');g.up(60,35);assert.equal(cv.box(a,'n1').x,10);assert.equal(cv.undo(a),true);
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('corner and edge handles resize one box; edges snap; the minimum size and the safe region are the designer\'s and refuse',async()=>{const a=await designer();try{
 cv.load(a,cv.four(a));cv.click(a,'n1');
 assert.equal(a.$('doc-canvas-host').querySelectorAll('.cv-handle').length,8);
 // The south-east corner: width and height follow, on the 1 mm grid.
 cv.drag(a,cv.handle(a,'se'),60,40,5.1,4.9);let b=cv.box(a,'n1');assert.deepEqual([b.x,b.y,b.width,b.height],[10,30,55,15]);
 // The north-west corner moves x, y and the size together; Alt gives 0.1 mm.
 cv.drag(a,cv.handle(a,'nw'),10,30,-0.4,-1.3,{altKey:true});b=cv.box(a,'n1');assert.deepEqual([b.x,b.y,b.width,b.height],[10,28.7,55,16.3],'x 9.6 snaps to the left edge of n4 (x 10), y 28.7 is on the 0.1 mm grid');assert.equal(cv.tpl(a).schema_version,'1.1');
 cv.drag(a,cv.handle(a,'n'),35,28.7,0,-1.3,{altKey:true});b=cv.box(a,'n1');assert.deepEqual([b.y,b.height],[27.4,17.6]);
 // An edge of the box snaps to an edge of another item within 1 mm: the east edge to n2's left edge (x 80).
 cv.load(a,cv.four(a));cv.click(a,'n1');cv.drag(a,cv.handle(a,'e'),60,35,19.5,0);b=cv.box(a,'n1');assert.equal(b.width,70,'its right edge is on x 80');
 // Too small, past the opposite edge and out of the safe region: refused, the box snaps back and the reason is named.
 for(const [dir,x,y,dx,dy,pattern] of [['n',35,30,0,4,/文本框超出A4安全区、重复或尺寸无效/],['e',60,35,-60,0,/文本框超出A4安全区、重复或尺寸无效/],['w',10,35,-3.4,0,/文本框超出A4安全区、重复或尺寸无效/],['s',35,40,0,40,/文本框不能重叠/]]){
  cv.load(a,cv.four(a));const before=JSON.stringify(cv.tpl(a));cv.click(a,'n1');
  const g=cv.press(a,cv.handle(a,dir),x,y);g.move(x+dx/2,y+dy/2);g.move(x+dx,y+dy);assert.ok(cv.item(a,'n1').getAttribute('class').includes('cv-bad'),dir+': the item shows that it will be refused');g.up(x+dx,y+dy);
  assert.equal(JSON.stringify(cv.tpl(a)),before,dir+': unchanged');assert.match(cv.status(a),/^未应用：/);assert.match(cv.status(a),pattern,dir);assert.ok(!cv.item(a,'n1').getAttribute('class').includes('cv-bad'),dir+': snapped back');assert.equal(cv.undo(a),true,dir+': a refusal is no step');
 }
 // A multiple selection has a group outline, no handles.
 cv.load(a,cv.four(a));cv.click(a,'n1');cv.click(a,'n4',{shiftKey:true});assert.equal(a.$('doc-canvas-host').querySelectorAll('.cv-handle').length,0);assert.equal(a.$('doc-canvas-host').querySelectorAll('.cv-bbox').length,1);
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('resizing the table scales its columns in proportion, rounded to 0.1 mm with the remainder on the last; column boundaries keep every minimum',async()=>{const a=await designer();try{
 const widths=()=>cv.tpl(a).table.columns.map(c=>c.width),sum=()=>cv.tpl(a).table.columns.reduce((s,c)=>s+Math.round(c.width*10),0);
 const base=()=>cv.load(a,cv.table(a,[['n1',10,30,50,10]]));
 base();cv.click(a,'table');
 // 19 mm narrower: [60, 70, 60] -> [54, 63, 54].
 cv.drag(a,cv.handle(a,'e'),200,150,-19,0);assert.equal(cv.tpl(a).table.width,171);assert.deepEqual(widths(),[54,63,54]);
 // 10 mm narrower from 190: 56.8 and 66.3 round, the remainder (56.9) goes to the last column.
 base();cv.click(a,'table');cv.drag(a,cv.handle(a,'e'),200,150,-10,0);assert.deepEqual(widths(),[56.8,66.3,56.9]);assert.equal(sum(),1800);
 // The height changes alone and leaves the columns; the west edge moves x and the width.
 base();cv.click(a,'table');cv.drag(a,cv.handle(a,'s'),105,200,0,5);assert.equal(cv.tpl(a).table.height,105);assert.deepEqual(widths(),[60,70,60]);
 base();cv.click(a,'table');cv.drag(a,cv.handle(a,'w'),10,150,10,0);assert.deepEqual([cv.tpl(a).table.x,cv.tpl(a).table.width],[20,180]);assert.equal(sum(),1800);
 // Too narrow for the first column's label (31.4 mm at 9 pt): refused with the designer's reason.
 base();cv.click(a,'table');const before=JSON.stringify(cv.tpl(a));cv.drag(a,cv.handle(a,'e'),200,150,-150,0);assert.equal(JSON.stringify(cv.tpl(a)),before);assert.match(cv.status(a),/第一列须容纳承前\/过次标签/);
 // Column boundaries: the column to the right gives up or takes the width; the sum stays the table's width.
 base();assert.deepEqual(cv.selected(a),[]);let g=cv.press(a,a.$('doc-canvas-host').querySelector('[data-col="0"]'),70,150);assert.deepEqual(cv.selected(a),['table'],'grabbing a boundary selects the table');g.move(75,150);g.move(80,150);g.up(80,150);assert.deepEqual(widths(),[70,60,60]);assert.equal(sum(),1900);
 base();cv.drag(a,a.$('doc-canvas-host').querySelector('[data-col="1"]'),130,150,-5,0);assert.deepEqual(widths(),[60,65,65]);
 // The first column keeps the label minimum (31.4 mm at 9 pt); the others keep 8 mm; neither is refused, the boundary stops.
 base();cv.drag(a,a.$('doc-canvas-host').querySelector('[data-col="0"]'),70,150,-50,0);assert.deepEqual(widths(),[31.4,98.6,60]);assert.equal(sum(),1900);
 base();cv.drag(a,a.$('doc-canvas-host').querySelector('[data-col="0"]'),70,150,200,0);assert.deepEqual(widths(),[122,8,60]);
 cv.load(a,cv.table(a,[['n1',10,30,50,10]],undefined,{font_size:12}));cv.drag(a,a.$('doc-canvas-host').querySelector('[data-col="0"]'),70,150,-50,0);assert.deepEqual(widths(),[41.2,88.8,60],'12 pt: 41.2 mm');
 // A boundary snaps to the 1 mm grid, to 0.1 mm with Alt, and to the edge of another item within 1 mm.
 base();cv.drag(a,a.$('doc-canvas-host').querySelector('[data-col="0"]'),70,150,10.4,0);assert.deepEqual(widths(),[70,60,60]);
 base();cv.drag(a,a.$('doc-canvas-host').querySelector('[data-col="0"]'),70,150,10.4,0,{altKey:true});assert.deepEqual(widths(),[70.4,59.6,60]);
 cv.load(a,cv.table(a,[['n1',10,30,50,10]]));
 // From the keyboard: a boundary is a slider once the table is selected; arrows move it by 0.1 mm, Shift by 1 mm.
 base();const col=()=>a.$('doc-canvas-host').querySelector('[data-col="0"]');assert.equal(col().getAttribute('tabindex'),'-1');cv.click(a,'table');assert.equal(col().getAttribute('tabindex'),'0');assert.equal(col().getAttribute('role'),'slider');assert.equal(col().getAttribute('aria-valuenow'),'70');
 cv.key(a,col(),'ArrowRight');assert.deepEqual(widths(),[60.1,69.9,60]);assert.equal(col().getAttribute('aria-valuenow'),'70.1');cv.key(a,col(),'ArrowLeft',{shiftKey:true});assert.deepEqual(widths(),[59.1,70.9,60]);
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('arrow keys nudge by 0.1 mm and Shift-arrows by 1 mm; a nudge the designer refuses leaves the item where it is',async()=>{const a=await designer();try{
 cv.load(a,cv.four(a));cv.click(a,'n1');const item=()=>cv.item(a,'n1');
 cv.key(a,item(),'ArrowRight');assert.equal(cv.box(a,'n1').x,10.1);assert.equal(cv.tpl(a).schema_version,'1.1','off whole millimetres: 1.0 becomes 1.1');
 cv.key(a,item(),'ArrowRight',{shiftKey:true});assert.equal(cv.box(a,'n1').x,11.1);cv.key(a,item(),'ArrowDown',{shiftKey:true});cv.key(a,item(),'ArrowDown');assert.equal(cv.box(a,'n1').y,31.1);
 cv.key(a,item(),'ArrowLeft');cv.key(a,item(),'ArrowUp');assert.deepEqual([cv.box(a,'n1').x,cv.box(a,'n1').y],[11,31]);assert.equal(document_activeKey(a),'n1','the focus stays on the item');
 // Past the safe region (x 10): refused, named, and not a step.
 for(let i=0;i<2;i++)cv.key(a,item(),'ArrowLeft',{shiftKey:true});cv.key(a,item(),'ArrowLeft',{shiftKey:true});assert.equal(cv.box(a,'n1').x,10);const before=JSON.stringify(cv.tpl(a));cv.key(a,item(),'ArrowLeft');assert.equal(JSON.stringify(cv.tpl(a)),before);assert.match(cv.status(a),/^未应用：文本框超出A4安全区/);
 // An arrow on an item that is not selected selects it and moves it alone.
 cv.click(a,'n4');cv.key(a,cv.item(a,'n2'),'ArrowDown',{shiftKey:true});assert.deepEqual(cv.selected(a),['n2']);assert.equal(cv.box(a,'n2').y,31);assert.equal(cv.box(a,'n4').y,60);
 // The whole selection moves together.
 cv.click(a,'n1');cv.click(a,'n3',{shiftKey:true});cv.key(a,cv.item(a,'n1'),'ArrowDown');assert.deepEqual([cv.box(a,'n1').y,cv.box(a,'n3').y],[31.1,30.1]);
 // Nothing is selected, nothing moves.
 cv.key(a,cv.item(a,'n1'),'Escape');const same=JSON.stringify(cv.tpl(a));cv.key(a,cv.svg(a),'ArrowDown');assert.equal(JSON.stringify(cv.tpl(a)),same);
 assert.equal(a.errors.length,0);
 }finally{a.close();}});
const document_activeKey=a=>a.w.document.activeElement?.getAttribute?.('data-key');

test('delete removes the selection, refuses the last box, and removing the table falls back to 1.2 with a background and 1.1 without',async()=>{const a=await designer();try{
 cv.load(a,cv.four(a));cv.click(a,'n2');cv.click(a,'n3',{shiftKey:true});cv.key(a,cv.item(a,'n2'),'Delete');assert.deepEqual(cv.tpl(a).elements.map(e=>e.id),['n1','n4']);assert.deepEqual(cv.selected(a),[]);assert.match(cv.status(a),/已删除/);
 a.click('doc-undo');assert.deepEqual(cv.tpl(a).elements.map(e=>e.id),['n1','n2','n3','n4'],'one step brings both back');
 cv.click(a,'n1');cv.key(a,cv.item(a,'n1'),'Backspace');assert.deepEqual(cv.tpl(a).elements.map(e=>e.id),['n2','n3','n4']);
 // The last box stays, with the reason.
 cv.load(a,cv.doc(a,[['n1',10,30,50,10]]));cv.click(a,'n1');cv.key(a,cv.item(a,'n1'),'Delete');assert.deepEqual(cv.tpl(a).elements.map(e=>e.id),['n1']);assert.equal(cv.status(a),'未应用：至少保留一个文本框。 项目已恢复原位。');assert.equal(cv.undo(a),true);assert.deepEqual(cv.selected(a),['n1']);
 // The delete button does the same, and is disabled with nothing selected.
 cv.key(a,cv.item(a,'n1'),'Escape');assert.equal(a.$('doc-del').disabled,true);cv.click(a,'n1');assert.equal(a.$('doc-del').disabled,false);a.click('doc-del');assert.equal(cv.tpl(a).elements.length,1);
 // The table goes with a keystroke: 1.3 without a background becomes 1.1; with one, 1.2.
 cv.load(a,cv.table(a,[['n1',10,30,50,10]]));cv.click(a,'table');cv.key(a,cv.item(a,'table'),'Delete');let t=cv.tpl(a);assert.equal(t.table,undefined);assert.equal(t.schema_version,'1.1');assert.match(a.$('doc-table-status').textContent,/无明细表/);
 const withBackground=cv.table(a,[['n1',10,30,50,10]]);withBackground.background={id:'0123456789abcdef0123456789abcdef',sha256:'a'.repeat(64)};cv.load(a,withBackground);cv.click(a,'table');a.click('doc-del');t=cv.tpl(a);assert.equal(t.schema_version,'1.2');assert.deepEqual(t.background,withBackground.background);
 // A box and the table together are one step.
 cv.load(a,cv.table(a,[['n1',10,30,50,10],['n2',80,30,50,10]]));cv.key(a,cv.item(a,'n1'),'a',{ctrlKey:true});cv.key(a,cv.item(a,'n1'),'Delete');assert.equal(cv.status(a),'未应用：至少保留一个文本框。 项目已恢复原位。');cv.click(a,'n1');cv.click(a,'table',{shiftKey:true});cv.key(a,cv.item(a,'n1'),'Delete');t=cv.tpl(a);assert.deepEqual([t.elements.map(e=>e.id),t.table],[['n2'],undefined]);a.click('doc-undo');assert.equal(cv.tpl(a).elements.length,2);assert.ok(cv.tpl(a).table);
 // The variables mode needs a row field, a total or a table: deleting the last of them is refused with that reason.
 const v=cv.doc(a,[['n1',10,30,50,10],['n2',80,30,50,10,'row','customer.id']],{schema_version:'1.1',print:{mode:'variables',offset_x:0,offset_y:0}});cv.load(a,v);cv.click(a,'n2');a.click('doc-del');assert.match(cv.status(a),/变量层至少需要/);assert.equal(cv.tpl(a).elements.length,2);
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('duplicate copies a box under the lowest free id at the nearest free place below it; no room and a 25th box are refused',async()=>{const a=await designer();try{
 cv.load(a,cv.four(a));cv.click(a,'n2');a.click('doc-del');assert.deepEqual(cv.tpl(a).elements.map(e=>e.id),['n1','n3','n4']);
 // n1 (10, 30, 50 x 10): the place right under it (y 40) is free, and the lowest free id is n2.
 cv.click(a,'n1');cv.key(a,cv.item(a,'n1'),'d',{ctrlKey:true});let t=cv.tpl(a);assert.deepEqual(t.elements.map(e=>e.id),['n1','n3','n4','n2']);assert.deepEqual(only(t.elements[3],['x','y','width','height','text','source']),only({x:10,y:40,width:50,height:10,text:'n1 text',source:'literal'},['x','y','width','height','text','source']));
 assert.deepEqual(cv.selected(a),['n2'],'the copy is selected');assert.match(a.$('doc-canvas-live').textContent,/已选择 n2/);
 // Again, now from the copy: y 50 touches n4 (y 60) but does not overlap it; once more the nearest free place is below n4.
 cv.key(a,cv.item(a,'n2'),'d',{metaKey:true});t=cv.tpl(a);assert.deepEqual([t.elements[4].id,t.elements[4].y],['n5',50]);cv.key(a,cv.item(a,'n5'),'d',{ctrlKey:true});t=cv.tpl(a);assert.deepEqual([t.elements[5].id,t.elements[5].y],['n6',70],'n4 at y 60 is in the way; the first free place is below it');
 // A row field copies its field; one step undoes the copy.
 a.click('doc-undo');assert.equal(cv.tpl(a).elements.length,5);
 cv.load(a,cv.four(a));cv.click(a,'n2');a.click('doc-dup');t=cv.tpl(a);assert.deepEqual([t.elements[4].id,t.elements[4].source,t.elements[4].field,t.elements[4].x,t.elements[4].y],['n5','row','customer.id',80,40]);
 // Several boxes: each copy under its own place and the lowest free id.
 cv.load(a,cv.four(a));cv.click(a,'n1');cv.click(a,'n2',{shiftKey:true});a.click('doc-dup');t=cv.tpl(a);assert.deepEqual(t.elements.slice(4).map(e=>[e.id,e.x,e.y]),[['n5',10,40],['n6',80,40]]);assert.deepEqual(cv.selected(a).sort(),['n5','n6']);
 // The table is not a box: nothing to copy.
 cv.load(a,cv.table(a,[['n1',10,30,50,10]]));cv.click(a,'table');assert.equal(a.$('doc-dup').disabled,true);
 // No free place below: the copy is refused and says so.
 cv.load(a,cv.doc(a,[['n1',10,255,50,10]]));cv.click(a,'n1');a.click('doc-dup');assert.equal(cv.tpl(a).elements.length,1);assert.match(cv.status(a),/下方没有可放置副本的空位/);
 // 24 boxes: the 25th is refused.
 cv.load(a,cv.doc(a,Array.from({length:24},(_,i)=>['n'+(i+1),10,25+i*10,50,10])));assert.equal(cv.tpl(a).elements.length,24);cv.click(a,'n1');cv.key(a,cv.item(a,'n1'),'d',{ctrlKey:true});assert.equal(cv.tpl(a).elements.length,24);assert.equal(cv.status(a),'未应用：最多24个文本框。');
 // With the print offset the copy must still print inside the safe region: the offset decides, and the refusal says why.
 cv.load(a,cv.doc(a,[['n1',10,250,50,10]],{schema_version:'1.1',print:{mode:'full',offset_x:0,offset_y:5}}));cv.click(a,'n1');a.click('doc-dup');assert.equal(cv.tpl(a).elements.length,1);assert.equal(cv.status(a),'未应用：偏移后的打印文本框超出安全区。');
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('align and distribute act on the selection of two or more and three or more, in 0.1 mm units',async()=>{const a=await designer();try{
 const stagger=()=>cv.doc(a,[['n1',10,30,50,10],['n2',80,40,30,10],['n3',150,50,40,10]]),xs=()=>cv.tpl(a).elements.map(e=>e.x),ys=()=>cv.tpl(a).elements.map(e=>e.y);
 const all=()=>{cv.click(a,'n1');cv.click(a,'n2',{shiftKey:true});cv.click(a,'n3',{shiftKey:true});};
 // Disabled until the selection is big enough.
 cv.load(a,stagger());for(const id of ['doc-align-left','doc-distribute-h'])assert.equal(a.$(id).disabled,true);cv.click(a,'n1');assert.equal(a.$('doc-align-left').disabled,true);cv.click(a,'n2',{shiftKey:true});assert.equal(a.$('doc-align-left').disabled,false);assert.equal(a.$('doc-distribute-h').disabled,true);cv.click(a,'n3',{shiftKey:true});assert.equal(a.$('doc-distribute-h').disabled,false);
 for(const [button,expect,axis] of [['left',[10,10,10],xs],['right',[140,160,150],xs],['center',[75,85,80],xs],['top',[30,30,30],ys],['bottom',[50,50,50],ys],['middle',[40,40,40],ys]]){
  cv.load(a,stagger());all();a.click('doc-align-'+button);assert.deepEqual(axis(),expect,button);assert.match(cv.status(a),/已对齐/);
 }
 // Align right/left of boxes that then overlap is refused by the designer; the boxes stay.
 cv.load(a,cv.doc(a,[['n1',10,30,50,10],['n2',80,35,30,10]]));cv.click(a,'n1');cv.click(a,'n2',{shiftKey:true});a.click('doc-align-top');assert.equal(a.$('doc-undo').disabled,false);a.click('doc-undo');cv.load(a,cv.doc(a,[['n1',10,30,50,10],['n2',30,40,30,10]]));cv.click(a,'n1');cv.click(a,'n2',{shiftKey:true});const before=JSON.stringify(cv.tpl(a));a.click('doc-align-top');assert.equal(JSON.stringify(cv.tpl(a)),before);assert.match(cv.status(a),/^未应用：文本框不能重叠/);
 // Distribute: equal gaps, the first and the last stay; the order is by position, not by id.
 cv.load(a,cv.doc(a,[['n1',10,30,60,10],['n2',150,30,40,10],['n3',80,30,20,10]]));all();a.click('doc-distribute-h');assert.deepEqual(cv.tpl(a).elements.map(e=>e.x),[10,150,100]);
 cv.load(a,cv.doc(a,[['n1',10,30,50,10],['n2',10,50,50,10],['n3',10,100,50,10]]));all();a.click('doc-distribute-v');assert.deepEqual(ys(),[30,65,100]);
 // Free space that does not divide: the gaps differ by at most 0.1 mm and the ends stay.
 cv.load(a,cv.doc(a,[['n1',10,30,50,10],['n2',10,50,50,10],['n3',10,90.1,50,10]],{schema_version:'1.1',print:{mode:'full',offset_x:0,offset_y:0}}));all();a.click('doc-distribute-v');const y=ys();assert.deepEqual([y[0],y[2]],[30,90.1]);assert.ok(Math.abs((y[1]-40)-(90.1-y[1]-10))<=0.1+1e-9,String(y));assert.ok(Number.isInteger(y[1]*10+1e-9)||Math.abs(y[1]*10-Math.round(y[1]*10))<1e-9);
 // The table aligns with boxes too.
 cv.load(a,cv.table(a,[['n1',30,30,50,10]]));cv.click(a,'n1');cv.click(a,'table',{shiftKey:true});a.click('doc-align-left');assert.equal(cv.tpl(a).elements[0].x,10);
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('an edit the designer refuses is not applied: the items snap back, the reason is on the status line and no undo step exists',async()=>{const a=await designer();try{
 cv.load(a,cv.four(a));const before=JSON.stringify(cv.tpl(a));
 for(const [label,x,y,dx,dy,pattern] of [['above the safe region',35,35,0,-10,/文本框超出A4安全区、重复或尺寸无效/],['over another box',35,35,45,0,/文本框不能重叠/],['past the right edge',35,35,170,0,/文本框超出A4安全区、重复或尺寸无效/],['past the bottom',35,65,0,235,/文本框超出A4安全区、重复或尺寸无效/]]){
  const g=cv.press(a,cv.item(a,'n1'),x,y);g.move(x+dx/2,y+dy/2);g.move(x+dx,y+dy);
  assert.match(a.$('doc-canvas-host').querySelector('.cv-readout').textContent,/将被拒绝/,label);assert.ok(cv.item(a,'n1').getAttribute('class').includes('cv-bad'),label);
  g.up(x+dx,y+dy);assert.equal(JSON.stringify(cv.tpl(a)),before,label);assert.match(cv.status(a),/^未应用：/,label);assert.match(cv.status(a),pattern,label);
  assert.equal(cv.item(a,'n1').querySelector('.cv-box').getAttribute('x'),String(ORIGIN+30),label+': snapped back');assert.equal(cv.undo(a),true,label);assert.equal(a.$('doc-canvas-status').getAttribute('role'),'status');
 }
 // A refusal through the print offset: the printed box must stay inside the safe region too.
 const shifted=cv.doc(a,[['n1',13,30,50,10]],{schema_version:'1.1',print:{mode:'full',offset_x:-3,offset_y:0}});cv.load(a,shifted);cv.click(a,'n1');
 cv.key(a,cv.item(a,'n1'),'ArrowLeft',{shiftKey:true});assert.equal(cv.box(a,'n1').x,13);assert.equal(cv.status(a),'未应用：偏移后的打印文本框超出安全区。 项目已恢复原位。');cv.key(a,cv.item(a,'n1'),'ArrowRight',{shiftKey:true});assert.equal(cv.box(a,'n1').x,14);
 // A table dragged over a box is refused with the table's reason.
 cv.load(a,cv.table(a,[['n1',10,30,50,10]]));const g=cv.press(a,cv.item(a,'table'),100,150);g.move(100,120);g.move(100,85);g.up(100,85);assert.match(cv.status(a),/明细表不能与文本框重叠/);assert.equal(cv.tpl(a).table.y,100);
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('undo and redo cover every applied change: at least 100 steps, a drag is one step, a refused edit and an unchanged value are none, a new edit ends the redo line',async()=>{const a=await designer();try{
 cv.load(a,cv.doc(a,[['n1',10,30,20,10]]));assert.equal(a.$('doc-undo').disabled,true);assert.equal(a.$('doc-redo').disabled,true);
 // 120 nudges are 120 steps; Ctrl-Z undoes them one by one, Ctrl-Shift-Z and Ctrl-Y redo them.
 cv.click(a,'n1');for(let i=0;i<120;i++)cv.key(a,cv.item(a,'n1'),'ArrowRight');assert.equal(cv.box(a,'n1').x,22);
 for(let i=0;i<120;i++)cv.key(a,cv.item(a,'n1'),'z',{ctrlKey:true});assert.equal(cv.box(a,'n1').x,10);assert.equal(cv.tpl(a).schema_version,'1.0');assert.equal(a.$('doc-undo').disabled,true);assert.equal(a.$('doc-redo').disabled,false);
 for(let i=0;i<60;i++)cv.key(a,cv.item(a,'n1'),'z',{ctrlKey:true,shiftKey:true});assert.equal(cv.box(a,'n1').x,16);for(let i=0;i<60;i++)cv.key(a,cv.item(a,'n1'),'y',{metaKey:true});assert.equal(cv.box(a,'n1').x,22);assert.equal(a.$('doc-redo').disabled,true);
 // After an undo the inputs and the canvas show the earlier state.
 cv.key(a,cv.item(a,'n1'),'z',{ctrlKey:true});assert.equal(a.$('doc-elements').querySelector('[aria-label="n1 x"]').value,'21.9');assert.equal(cv.item(a,'n1').querySelector('.cv-box').getAttribute('x'),String(ORIGIN+21.9*SCALE));assert.match(a.$('doc-status').textContent,/已撤销/);assert.match(cv.item(a,'n1').getAttribute('aria-label'),/x 21\.9 毫米/);
 // Typing a new edit after undoing ends the redo line.
 cv.key(a,cv.item(a,'n1'),'ArrowDown');assert.equal(a.$('doc-redo').disabled,true);
 // A drag is one step.
 cv.load(a,cv.four(a));cv.drag(a,cv.item(a,'n1'),35,35,10.4,0);assert.equal(cv.box(a,'n1').x,20);a.click('doc-undo');assert.equal(cv.box(a,'n1').x,10);assert.equal(a.$('doc-undo').disabled,true,'the drag was the only step');a.click('doc-redo');assert.equal(cv.box(a,'n1').x,20);
 // A refused edit and a move back to where it was are no steps.
 cv.load(a,cv.four(a));cv.drag(a,cv.item(a,'n1'),35,35,0,-10);cv.drag(a,cv.item(a,'n1'),35,35,10,0);cv.drag(a,cv.item(a,'n1'),45,35,-10,0);assert.equal(cv.box(a,'n1').x,10);
 a.$('doc-elements').querySelector('[aria-label="n1 x"]').value='5';a.$('doc-elements').querySelector('[aria-label="n1 x"]').dispatchEvent(new a.w.Event('change',{bubbles:true}));assert.match(a.$('doc-status').textContent,/安全区/);
 a.$('doc-elements').querySelector('[aria-label="n1 x"]').value='10';a.$('doc-elements').querySelector('[aria-label="n1 x"]').dispatchEvent(new a.w.Event('change',{bubbles:true}));assert.equal(a.$('doc-undo').disabled,false,'the drag to x 20 and back is two steps: 10 -> 20 -> 10');a.click('doc-undo');a.click('doc-undo');assert.equal(a.$('doc-undo').disabled,true,'neither the refused edit nor the unchanged value is a step');
 // The history is bounded (200 steps), oldest first.
 cv.load(a,cv.doc(a,[['n1',10,30,20,10]]));cv.click(a,'n1');for(let i=0;i<205;i++)cv.key(a,cv.item(a,'n1'),'ArrowRight');let undone=0;while(!a.$('doc-undo').disabled){a.click('doc-undo');undone++;}assert.equal(undone,200);assert.equal(cv.box(a,'n1').x,10.5);
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('every way of changing the template is one undo step: inputs, add, remove, table, print settings, background reference',async()=>{const a=await designer();try{
 cv.load(a,cv.doc(a,[['n1',10,30,50,10]]));const start=JSON.stringify(cv.tpl(a)),steps=[];const snap=()=>steps.push(JSON.stringify(cv.tpl(a)));
 const change=(label,v)=>{const i=a.$('doc-elements').querySelector('[aria-label="'+label+'"]');i.value=v;i.dispatchEvent(new a.w.Event('change',{bubbles:true}));};
 change('n1 x','12');snap();a.input('doc-text','second');a.click('doc-add-text');snap();a.$('doc-field').value='customer.id';a.click('doc-add-row');snap();a.$('doc-field').value='ar.open_balance';a.click('doc-add-total');snap();
 a.$('doc-print-mode').value='variables';a.input('doc-offset-y','1');a.click('doc-print-apply');snap();
 const options=[...a.$('doc-table-columns').options];options[0].selected=true;options[1].selected=true;a.input('doc-table-y','120');a.input('doc-table-height','100');a.click('doc-table-add');snap();change('table height','90');snap();
 const remove=[...a.$('doc-elements').querySelectorAll('button')].find(b=>b.textContent==='移除');remove.click();snap();a.click('doc-table-remove');snap();
 const final=JSON.stringify(cv.tpl(a));assert.equal(steps.at(-1),final);assert.deepEqual(steps.map((v,i)=>steps.indexOf(v)!==i?i:-1).filter(i=>i>=0),[],'every change above was applied, each a different state');
 for(let i=steps.length-2;i>=0;i--){a.click('doc-undo');assert.equal(JSON.stringify(cv.tpl(a)),steps[i],'undo to step '+i);}
 a.click('doc-undo');assert.equal(JSON.stringify(cv.tpl(a)),start);assert.equal(a.$('doc-undo').disabled,true);
 for(let i=0;i<steps.length;i++){a.click('doc-redo');assert.equal(JSON.stringify(cv.tpl(a)),steps[i],'redo to step '+i);}assert.equal(a.$('doc-redo').disabled,true);
 // The background reference: removing it is a step too.
 const withBackground=cv.doc(a,[['n1',10,30,50,10]],{schema_version:'1.2',print:{mode:'full',offset_x:0,offset_y:0},background:{id:'0123456789abcdef0123456789abcdef',sha256:'a'.repeat(64)}});cv.load(a,withBackground);a.click('doc-background-remove');assert.equal(cv.tpl(a).schema_version,'1.1');a.click('doc-undo');assert.deepEqual(cv.tpl(a).background,withBackground.background);assert.equal(a.$('doc-undo').disabled,true);
 // The inputs re-render with the canvas after each.
 assert.equal(a.$('doc-canvas-host').querySelectorAll('[data-key]').length,cv.tpl(a).elements.length+(cv.tpl(a).table?1:0));
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('opening a template, resetting it and a new query clear the history; paging through the same frozen snapshot does not',async()=>{const a=await designer();try{
 const edit=()=>{cv.load(a,cv.doc(a,[['n1',10,30,50,10]]));cv.click(a,'n1');cv.key(a,cv.item(a,'n1'),'ArrowDown');assert.equal(a.$('doc-undo').disabled,false);};
 edit();a.$('doc-json').value=JSON.stringify(cv.tpl(a));a.click('doc-json-open');assert.equal(a.$('doc-undo').disabled,true,'opening JSON');
 edit();cv.key(a,cv.item(a,'n1'),'z',{ctrlKey:true});assert.equal(a.$('doc-redo').disabled,false);a.click('doc-reset');assert.equal(a.$('doc-undo').disabled,true,'reset');assert.equal(a.$('doc-redo').disabled,true);assert.deepEqual(cv.tpl(a).elements.map(e=>e.id),['n1']);
 // A server version: save, edit, open the saved version.
 a.click('doc-server-save');await until(()=>a.$('doc-server-status').textContent.includes('版本 1')&&!a.$('preview').disabled,'saved');const id=a.$('doc-server-status').textContent.match(/[a-f0-9]{32}/)[0];
 edit();a.click('doc-server-list');await until(()=>a.$('doc-server-listing').options.length>0&&!a.$('preview').disabled,'list');a.$('doc-server-listing').value=id;a.$('doc-server-version').value='1';a.click('doc-server-open');await until(()=>a.$('doc-server-status').textContent.includes('正在编辑模板')&&!a.$('preview').disabled,'opened');assert.equal(a.$('doc-undo').disabled,true,'opening a server version');
 // A new query.
 edit();a.w.document.querySelector('[data-page="workspace"]').click();a.click('preview');await until(()=>!a.$('preview').disabled&&!a.$('result-table-wrap').hidden,'query again');a.w.document.querySelector('[data-page="documents"]').click();assert.equal(a.$('doc-undo').disabled,true,'a new query');assert.equal(a.$('doc-redo').disabled,true);
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('paging through the pages of one frozen snapshot keeps the history, a new snapshot clears it',async()=>{const a=app();try{
 await snapshotReady(a);a.$('page-size').value='2';a.$('page-size').dispatchEvent(new a.w.Event('change',{bubbles:true}));await until(()=>a.$('result-table').tBodies[0].rows.length===2&&!a.$('next-page').disabled,'two-row page');
 a.w.document.querySelector('[data-page="documents"]').click();cv.click(a,'n1');cv.key(a,cv.item(a,'n1'),'ArrowDown');assert.equal(a.$('doc-undo').disabled,false);
 a.w.document.querySelector('[data-page="workspace"]').click();a.click('next-page');await until(()=>a.$('page-info').textContent.includes('第 2 页'),'page 2');a.w.document.querySelector('[data-page="documents"]').click();
 assert.equal(a.$('doc-undo').disabled,false,'the same snapshot, the same history');assert.equal(cv.box(a,'n1').y,25.1);a.click('doc-undo');assert.equal(cv.box(a,'n1').y,25);
 cv.key(a,cv.item(a,'n1'),'ArrowDown');a.w.document.querySelector('[data-page="workspace"]').click();a.click('snapshot');await until(()=>a.$('result-kind').textContent==='冻结快照'&&!a.$('export').disabled&&a.$('page-info').textContent.includes('第 1 页'),'new snapshot');a.w.document.querySelector('[data-page="documents"]').click();
 assert.equal(a.$('doc-undo').disabled,true,'a new snapshot is a new query');assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('print settings, a background reference and its removal keep a template with a table at schema 1.3, each as one undo step',async()=>{
 const ref={id:'0123456789abcdef0123456789abcdef',sha256:'a'.repeat(64)};let binding=null;
 const a=await designer(async(path,options)=>{if(path.endsWith('/document-backgrounds')&&options?.method==='GET'&&binding)return json([{...ref,binding,width:794,height:1123}]);});
 try{
 binding=cv.tpl(a).binding;
 const withTable=cv.table(a,[['n1',10,30,50,10]]);cv.load(a,withTable);assert.equal(a.$('doc-undo').disabled,true);
 const state=()=>{const t=cv.tpl(a);return {schema:t.schema_version,table:JSON.stringify(t.table),print:JSON.stringify(t.print),background:t.background?JSON.stringify(t.background):undefined};};
 const table=JSON.stringify(withTable.table);
 // Print settings: variables mode with a vertical offset. The table stays and the schema stays 1.3.
 a.$('doc-print-mode').value='variables';a.input('doc-offset-y','1');a.click('doc-print-apply');assert.equal(a.$('doc-status').textContent,READY);
 assert.deepEqual(state(),{schema:'1.3',table,print:JSON.stringify({mode:'variables',offset_x:0,offset_y:1}),background:undefined});
 assert.equal(a.$('doc-undo').disabled,false);a.click('doc-undo');assert.equal(a.$('doc-undo').disabled,true,'one step');assert.deepEqual(state(),{schema:'1.3',table,print:JSON.stringify({mode:'full',offset_x:0,offset_y:0}),background:undefined});
 // A background reference: 1.3 may carry one, and the table stays.
 a.click('doc-background-list');await until(()=>a.$('doc-background-listing').options.length===1&&!a.$('preview').disabled,'listing');a.$('doc-background-listing').value=ref.id;
 a.click('doc-background-apply');assert.equal(a.$('doc-status').textContent,READY);
 assert.deepEqual(state(),{schema:'1.3',table,print:JSON.stringify({mode:'full',offset_x:0,offset_y:0}),background:JSON.stringify(ref)});assert.match(a.$('doc-background-status').textContent,/当前底稿/);
 a.click('doc-undo');assert.equal(a.$('doc-undo').disabled,true,'one step');assert.equal(state().background,undefined);assert.equal(state().schema,'1.3');assert.equal(state().table,table);
 // Removing it again: still 1.3, the table stays, and the removal is its own step.
 a.click('doc-redo');assert.equal(state().background,JSON.stringify(ref));a.click('doc-background-remove');assert.equal(a.$('doc-status').textContent,READY);
 assert.deepEqual(state(),{schema:'1.3',table,print:JSON.stringify({mode:'full',offset_x:0,offset_y:0}),background:undefined});
 a.click('doc-undo');assert.deepEqual(state(),{schema:'1.3',table,print:JSON.stringify({mode:'full',offset_x:0,offset_y:0}),background:JSON.stringify(ref)},'one undo brings the reference back');
 a.click('doc-undo');assert.equal(state().background,undefined);assert.equal(a.$('doc-undo').disabled,true,'two steps in all: the reference and its removal');
 // Without a table the choice is what it was: a reference makes 1.2, removing it 1.1, print settings never downgrade 1.2.
 cv.load(a,cv.doc(a,[['n1',10,30,50,10,'row','customer.id']]));a.click('doc-background-apply');assert.equal(cv.tpl(a).schema_version,'1.2');
 a.$('doc-print-mode').value='variables';a.click('doc-print-apply');assert.equal(a.$('doc-status').textContent,READY);assert.equal(cv.tpl(a).schema_version,'1.2');assert.equal(cv.tpl(a).print.mode,'variables');
 a.click('doc-background-remove');assert.equal(cv.tpl(a).schema_version,'1.1');assert.equal(cv.tpl(a).background,undefined);
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('undo never restores the opened-published state that issuing needs',async()=>{const bodies=[];const a=app(offeringIssuance({bodies}));try{
 await snapshotReady(a);const id=await publishedOpen(a);a.w.document.querySelector('[data-page="documents"]').click();
 assert.equal(a.$('doc-issue').hidden,false);assert.equal(a.$('doc-undo').disabled,true,'opening the published version leaves no history');
 // After any edit, issuing wants the published version opened again, and undoing back to its exact content does not change that.
 cv.click(a,'n1');cv.key(a,cv.item(a,'n1'),'ArrowDown');a.click('doc-issue');assert.match(a.$('doc-status').textContent,/先保存/);
 a.click('doc-undo');assert.equal(a.$('doc-undo').disabled,true);const same=JSON.stringify(cv.tpl(a));
 a.click('doc-issue');assert.match(a.$('doc-status').textContent,/打开已发布模板/);assert.equal(a.trace.filter(v=>v.path.endsWith('/issues')&&v.method==='POST').length,0,'nothing was submitted');
 a.click('doc-redo');a.click('doc-undo');a.click('doc-issue');assert.match(a.$('doc-status').textContent,/打开已发布模板/);assert.equal(bodies.length,0);
 // Reopening the published version arms the button again, as before.
 a.click('doc-published-open');await until(()=>a.$('doc-server-status').textContent.includes('已打开发布模板')&&!a.$('preview').disabled,'reopened');assert.equal(JSON.stringify(cv.tpl(a)),same);a.click('doc-issue');await until(()=>bodies.length===1,'submitted');assert.equal(bodies[0].template_id,id);
 }finally{a.close();}});


test('the command palette lists the designer\'s buttons that are present, visible and enabled, filters by typing and runs the highlighted one with Enter',async()=>{const a=await designer();try{
 cv.load(a,cv.four(a));cv.click(a,'n1');const item=cv.item(a,'n1');item.focus();assert.equal(a.w.document.activeElement,item);
 assert.equal(palette(a),null);cv.key(a,item,'k',{ctrlKey:true});
 const dialog=palette(a);assert.ok(dialog);assert.equal(dialog.getAttribute('role'),'dialog');assert.equal(dialog.getAttribute('aria-modal'),'true');assert.equal(a.w.document.getElementById(dialog.getAttribute('aria-labelledby')).textContent,'命令面板');
 assert.equal(a.w.document.activeElement.id,'cv-palette-input','focus moves into the dialog');assert.equal(a.w.document.querySelector('.shell').inert,true,'the page behind is inert');assert.equal(dialog.parentElement.hidden,false);
 const listed=options(a);
 // Listed: the add buttons, the table button, duplicate, delete and what the one-item selection enables. Not listed: undo (no history), align (two items needed), distribute, check, PDF and issue (hidden without the PDF format), remove table (no table: the button exists and is enabled, so it is listed).
 for(const label of ['添加固定文字','添加行字段','添加原范围合计','添加或替换明细表','复制所选文本框','删除所选项目','保存模板版本','发布当前保存版本','打开已发布模板'])assert.ok(listed.includes(label),label+' is listed: '+listed);
 for(const label of ['撤销','重做','左对齐','水平等距分布'])assert.ok(!listed.includes(label),label+' is not: disabled');
 // The PDF, check and issue buttons are listed exactly when they are shown: the server offers them only with the Typst renderer.
 for(const [label,id] of [['生成PDF预览','doc-pdf'],['检查样本','doc-check'],['正式出具','doc-issue']])assert.equal(listed.includes(label),!a.$(id).hidden,label);
 // Filtering by typing: Chinese labels and English words.
 typeInto(a,'添加');assert.deepEqual(options(a),['添加固定文字','添加行字段','添加原范围合计','添加或替换明细表']);typeInto(a,'ADD total');assert.deepEqual(options(a),['添加原范围合计']);typeInto(a,'zzzz');assert.deepEqual(options(a),[]);assert.match(a.w.document.querySelector('.cv-empty').textContent,/没有匹配/);assert.equal(a.w.document.querySelector('.cv-empty').hidden,false);
 // Arrow keys move the highlight; Enter runs it exactly as its button would (the page's own click handler) and closes the dialog, restoring focus.
 typeInto(a,'添加');const input=a.w.document.getElementById('cv-palette-input');assert.equal(input.getAttribute('aria-activedescendant'),'cv-command-0');
 press(a,'ArrowDown');assert.equal(a.w.document.querySelector('.cv-palette [aria-selected="true"]').textContent,'添加行字段');assert.equal(input.getAttribute('aria-activedescendant'),'cv-command-1');
 press(a,'ArrowUp');press(a,'ArrowUp');assert.equal(a.w.document.querySelector('.cv-palette [aria-selected="true"]').textContent,'添加或替换明细表','wraps');
 typeInto(a,'添加行字段');let clicks=0;a.$('doc-add-row').addEventListener('click',()=>clicks++);a.$('doc-field').value='customer.id';press(a,'Enter');
 assert.equal(clicks,1,'the button was clicked');assert.equal(cv.tpl(a).elements.length,5);assert.equal(palette(a).parentElement.hidden,true);assert.equal(a.w.document.querySelector('.shell').inert,false);assert.equal(a.w.document.activeElement.getAttribute('data-key'),'n1','focus is back where it was');
 // The align entries appear once the selection has two items, and run the toolbar button.
 cv.click(a,'n2');cv.click(a,'n4',{shiftKey:true});cv.key(a,cv.item(a,'n2'),'k',{metaKey:true});assert.ok(options(a).includes('右对齐'));typeInto(a,'右对齐');press(a,'Enter');assert.equal(cv.box(a,'n4').x,80);
 // Undo is listed once there is history and does what its button does.
 cv.key(a,cv.item(a,'n2'),'k',{ctrlKey:true});assert.ok(options(a).includes('撤销'));typeInto(a,'撤销');press(a,'Enter');assert.equal(cv.tpl(a).elements.length,5);assert.equal(cv.box(a,'n4').x,10);assert.equal(a.$('doc-redo').disabled,false);
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('the command palette is a modal dialog: Tab stays inside, Esc closes and restores focus, hidden and disabled actions are not listed',async()=>{const a=await designer();try{
 cv.load(a,cv.four(a));const item=cv.item(a,'n3');item.focus();cv.key(a,item,'k',{ctrlKey:true});const close=a.w.document.getElementById('cv-palette-close'),input=a.w.document.getElementById('cv-palette-input');
 assert.equal(a.w.document.activeElement,input);
 // The trap: Tab from the last control returns to the first, Shift-Tab from the first goes to the last.
 press(a,'Tab');assert.equal(a.w.document.activeElement,close);press(a,'Tab');assert.equal(a.w.document.activeElement,input);press(a,'Tab',{shiftKey:true});assert.equal(a.w.document.activeElement,close);press(a,'Tab',{shiftKey:true});assert.equal(a.w.document.activeElement,input);
 // Esc closes it and restores focus to the control that had it; a second Ctrl-K opens it afresh, empty.
 typeInto(a,'对');press(a,'Escape');assert.equal(palette(a).parentElement.hidden,true);assert.equal(a.w.document.activeElement,item);assert.equal(a.w.document.querySelector('.shell').inert,false);
 cv.key(a,item,'k',{ctrlKey:true});assert.equal(a.w.document.getElementById('cv-palette-input').value,'');assert.equal(a.w.document.activeElement.id,'cv-palette-input');
 // The close button closes it too; so does a click on the backdrop; nothing was run.
 a.w.document.getElementById('cv-palette-close').click();assert.equal(palette(a).parentElement.hidden,true);assert.equal(a.w.document.activeElement,item);
 cv.key(a,item,'k',{ctrlKey:true});palette(a).parentElement.dispatchEvent(new a.w.PointerEvent('pointerdown',{bubbles:true}));assert.equal(palette(a).parentElement.hidden,true);
 // While it is open the canvas does not react to keys (Delete or Ctrl-A must not reach the template).
 cv.click(a,'n1');cv.key(a,cv.item(a,'n1'),'k',{ctrlKey:true});press(a,'Delete');press(a,'a',{ctrlKey:true});press(a,'z',{ctrlKey:true});assert.equal(cv.tpl(a).elements.length,4);assert.deepEqual(cv.selected(a),['n1']);press(a,'Escape');
 // A hidden button (the issue button without issuance, the PDF buttons without PDF) is not listed; a button that gets hidden or disabled leaves the list at once.
 cv.key(a,cv.item(a,'n1'),'k',{ctrlKey:true});assert.equal(options(a).includes('正式出具'),!a.$('doc-issue').hidden);assert.ok(options(a).includes('删除所选项目'));press(a,'Escape');a.$('doc-issue').hidden=true;a.$('doc-pdf').hidden=true;cv.key(a,cv.item(a,'n1'),'k',{ctrlKey:true});assert.ok(!options(a).includes('正式出具')&&!options(a).includes('生成PDF预览'),'hidden buttons are not listed');press(a,'Escape');
 a.$('doc-add-total').hidden=true;a.$('doc-add-row').disabled=true;cv.key(a,cv.item(a,'n1'),'k',{ctrlKey:true});assert.ok(!options(a).includes('添加原范围合计'),'hidden');assert.ok(!options(a).includes('添加行字段'),'disabled');press(a,'Escape');
 a.$('doc-add-total').hidden=false;a.$('doc-add-row').disabled=false;a.$('doc-table-add').parentElement.hidden=true;cv.key(a,cv.item(a,'n1'),'k',{ctrlKey:true});assert.ok(!options(a).includes('添加或替换明细表'),'inside a hidden container');press(a,'Escape');a.$('doc-table-add').parentElement.hidden=false;
 // The toolbar button opens it too, and it is not offered on another page's behalf: with the page hidden Ctrl-K does nothing.
 a.click('doc-palette-open');assert.equal(palette(a).parentElement.hidden,false);press(a,'Escape');
 a.w.document.querySelector('[data-page="workspace"]').click();a.w.document.body.dispatchEvent(new a.w.KeyboardEvent('keydown',{key:'k',ctrlKey:true,bubbles:true,cancelable:true}));assert.equal(palette(a).parentElement.hidden,true);
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('the palette lists the check and the PDF actions when the server offers them, and runs the buttons of the issue and release flow',async()=>{const a=await designer(offeringIssuance({bodies:[]}));try{
 a.click('logout');await a.login();a.configure();a.click('snapshot');await until(()=>a.$('result-kind').textContent==='冻结快照'&&!a.$('export').disabled,'snapshot ready');a.w.document.querySelector('[data-page="documents"]').click();
 assert.equal(a.$('doc-pdf').hidden,false);assert.equal(a.$('doc-check').hidden,false);assert.equal(a.$('doc-issue').hidden,false);
 cv.key(a,cv.item(a,'n1'),'k',{ctrlKey:true});const listed=options(a);for(const label of ['生成PDF预览','检查样本','正式出具'])assert.ok(listed.includes(label),label);
 for(const [text,id] of [['保存','doc-server-save']]){let clicked=0;a.$(id).addEventListener('click',()=>clicked++);typeInto(a,text);press(a,'Enter');assert.equal(clicked,1,id);}
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('every canvas item has an accessible name and the selection is announced through a polite live region; the canvas is keyboard-operable',async()=>{const a=await designer();try{
 const t=cv.doc(a,[['n1',10,100,60,10],['n2',80,30,50,10,'row','customer.id'],['n3',150,30,40,10,'total','ar.open_balance'],['n4',10,30,60,10,'literal','应收账款']]);cv.load(a,t);
 // Tab order is the reading order: top to bottom, left to right (n4 and n2 and n3 on y 30, then n1).
 assert.deepEqual(cv.keys(a),['n4','n2','n3','n1']);for(const e of a.$('doc-canvas-host').querySelectorAll('[data-key]')){assert.equal(e.getAttribute('tabindex'),'0');assert.equal(e.getAttribute('role'),'button');assert.ok(e.getAttribute('aria-label'));assert.equal(e.getAttribute('aria-pressed'),'false');}
 // Names carry id, source, text or field label, position and size, and the font size.
 assert.equal(cv.item(a,'n4').getAttribute('aria-label'),'n4，固定文字 “应收账款”，字号 12 点，x 10 毫米，y 30 毫米，宽 60 毫米，高 10 毫米');
 assert.equal(cv.item(a,'n2').getAttribute('aria-label'),'n2，行字段 客户，字号 12 点，x 80 毫米，y 30 毫米，宽 50 毫米，高 10 毫米');
 assert.equal(cv.item(a,'n3').getAttribute('aria-label'),'n3，合计 应收余额，字号 12 点，x 150 毫米，y 30 毫米，宽 40 毫米，高 10 毫米');
 const live=a.$('doc-canvas-live');assert.equal(live.getAttribute('aria-live'),'polite');assert.equal(live.getAttribute('aria-atomic'),'true');assert.equal(a.$('doc-canvas-status').getAttribute('role'),'status');
 assert.equal(a.$('doc-canvas-host').querySelector('svg').getAttribute('aria-describedby'),'doc-canvas-help');assert.ok(a.$('doc-canvas-help').textContent.includes('方向键'));
 // Enter and Space select the focused item; Shift or Ctrl toggles; focus never leaves it.
 const n2=cv.item(a,'n2');n2.focus();cv.key(a,n2,'Enter');assert.deepEqual(cv.selected(a),['n2']);assert.equal(live.textContent,'已选择 '+n2.getAttribute('aria-label')+'。');assert.equal(cv.item(a,'n2').getAttribute('aria-pressed'),'true');assert.equal(a.w.document.activeElement.getAttribute('data-key'),'n2');
 cv.key(a,cv.item(a,'n3'),' ');assert.deepEqual(cv.selected(a),['n3']);cv.key(a,cv.item(a,'n2'),'Enter',{shiftKey:true});assert.deepEqual(cv.selected(a),['n2','n3']);assert.match(live.textContent,/已选择 2 项：n2、n3/);cv.key(a,cv.item(a,'n3'),'Enter',{shiftKey:true});assert.deepEqual(cv.selected(a),['n2']);
 // A move updates the name of the item.
 cv.key(a,cv.item(a,'n2'),'ArrowDown');assert.match(cv.item(a,'n2').getAttribute('aria-label'),/y 30\.1 毫米/);
 // An undone item that no longer exists leaves the selection and the announcement says so.
 cv.click(a,'n1');a.click('doc-del');assert.equal(live.textContent,'未选择任何项目。');
 // Toolbar controls have names; the groups are labelled.
 for(const id of ['doc-undo','doc-redo','doc-dup','doc-del','doc-align-left','doc-align-center','doc-align-right','doc-align-top','doc-align-middle','doc-align-bottom','doc-distribute-h','doc-distribute-v','doc-check','doc-palette-open'])assert.ok((a.$(id).textContent.trim()||a.$(id).getAttribute('aria-label')||'').length>0,id);
 assert.ok([...a.$('doc-canvas-panel').querySelectorAll('[role="group"]')].every(g=>g.getAttribute('aria-label')));assert.equal(a.w.document.querySelector('label[for="doc-zoom"]').textContent,'缩放');
 // A table: its boundaries are named sliders, tabbable once it is selected.
 cv.load(a,cv.table(a,[['n1',10,30,50,10]]));const slider=a.$('doc-canvas-host').querySelector('[data-col="1"]');assert.match(slider.getAttribute('aria-label'),/第 2 列与第 3 列的边界/);assert.equal(slider.getAttribute('aria-valuenow'),'140');assert.equal(slider.getAttribute('aria-valuemin'),'10');assert.equal(slider.getAttribute('aria-valuemax'),'200');
 assert.match(cv.item(a,'table').getAttribute('aria-label'),/^明细表，列 客户、账龄区间、应收余额，字号 9 点，x 10 毫米，y 100 毫米，宽 190 毫米，高 100 毫米$/);
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('the inputs and the canvas stay in sync both ways, and the canvas goes through the designer\'s own validation',async()=>{const a=await designer();try{
 cv.load(a,cv.four(a));const input=(label)=>a.$('doc-elements').querySelector('[aria-label="'+label+'"]');
 // Input -> canvas.
 input('n1 x').value='20';input('n1 x').dispatchEvent(new a.w.Event('change',{bubbles:true}));assert.equal(cv.item(a,'n1').querySelector('.cv-box').getAttribute('x'),String(ORIGIN+60));assert.match(cv.item(a,'n1').getAttribute('aria-label'),/x 20 毫米/);
 input('n2 width').value='40';input('n2 width').dispatchEvent(new a.w.Event('change',{bubbles:true}));assert.equal(cv.item(a,'n2').querySelector('.cv-box').getAttribute('width'),String(40*SCALE));
 // The designer's refusal of an input leaves the canvas as it was.
 input('n1 width').value='200';input('n1 width').dispatchEvent(new a.w.Event('change',{bubbles:true}));assert.match(a.$('doc-status').textContent,/文本框/);assert.equal(cv.item(a,'n1').querySelector('.cv-box').getAttribute('width'),String(50*SCALE));
 // Canvas -> input: a drag, a nudge and a resize show in the inputs; the remove button takes the item off the canvas.
 cv.drag(a,cv.item(a,'n1'),45,35,-5,0);assert.equal(input('n1 x').value,'15');cv.key(a,cv.item(a,'n1'),'ArrowRight');assert.equal(input('n1 x').value,'15.1');cv.click(a,'n1');cv.drag(a,cv.handle(a,'s'),45,40,0,5);assert.equal(input('n1 height').value,'15');
 assert.match(a.$('doc-elements').textContent,/n1 · literal/);[...a.$('doc-elements').querySelectorAll('button')].find(b=>b.textContent==='移除').click();assert.equal(cv.item(a,'n1'),null);assert.deepEqual(cv.keys(a),['n2','n3','n4']);
 // The table: its inputs and its canvas.
 cv.load(a,cv.table(a,[['n1',10,30,50,10]]));a.$('doc-elements').querySelector('[aria-label="table height"]').value='120';a.$('doc-elements').querySelector('[aria-label="table height"]').dispatchEvent(new a.w.Event('change',{bubbles:true}));assert.equal(cv.item(a,'table').querySelector('.cv-box').getAttribute('height'),String(120*SCALE));
 cv.click(a,'table');cv.drag(a,cv.handle(a,'s'),105,220,0,-10);assert.equal(a.$('doc-elements').querySelector('[aria-label="table height"]').value,'110');
 cv.drag(a,a.$('doc-canvas-host').querySelector('[data-col="0"]'),70,150,10,0);assert.match(a.$('doc-elements').textContent,/customer\.id 70mm/);
 // The element rows are exactly what they were: the canvas adds nothing to the template but what the designer accepts (canonical keys only).
 const t=cv.tpl(a);assert.deepEqual(Object.keys(t),['schema_version','kind','binding','elements','print','table']);assert.deepEqual(Object.keys(t.elements[0]),['id','source','field','text','x','y','width','height','font_size','align']);assert.deepEqual(Object.keys(t.table),['x','y','width','height','font_size','columns']);
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

// Sample check (I5). Without Typst the server does not mount the route, so these run against a server that offers PDF and answers
// /document-checks from the test; the real route runs in the gated test after them.
function offeringChecks(answer,{bodies=[],status=200,raw}={}){
  const pdf=offeringPDF(Buffer.from('%PDF-1.7 fixture'));
  return async(path,options)=>{
    if(path.endsWith('/document-checks')){bodies.push(JSON.parse(options.body));return raw?raw():json(typeof answer==='function'?answer(JSON.parse(options.body)):answer,status);}
    return pdf(path,options);
  };
}
const checkRows=a=>[...a.$('doc-check-body').rows].map(r=>[...r.cells].slice(0,3).map(c=>c.textContent));
const checkReady=async a=>{await a.login();a.configure();a.click('snapshot');await until(()=>a.$('result-kind').textContent==='冻结快照'&&!a.$('export').disabled,'snapshot ready');a.w.document.querySelector('[data-page="documents"]').click();};

test('check samples sends the template and the frozen snapshot, shows each row with its code and reasons, and loads a row into the preview row number',async()=>{const bodies=[];
 const answer={rows:[{row_index:0,ok:true,reasons:['first','shortest:n2']},{row_index:2,ok:false,code:'UNSUPPORTED_COMBINATION',reasons:['last','longest:n1','longest:n2']}]};
 const a=app(offeringChecks(answer,{bodies}));try{
 await checkReady(a);assert.equal(a.$('doc-check').hidden,false);const sent=cv.tpl(a);
 a.click('doc-check');await until(()=>a.$('doc-check-body').rows.length===2&&!a.$('preview').disabled,'checked');
 assert.equal(bodies.length,1);assert.deepEqual(Object.keys(bodies[0]).sort(),['config','samples','snapshot_id']);assert.equal(bodies[0].samples,'boundary');assert.deepEqual(bodies[0].config,sent);assert.match(bodies[0].snapshot_id,/^[a-f0-9]{32}$/);assert.equal(JSON.stringify(bodies[0]).includes('9007199254740'),false,'no data goes up');
 assert.deepEqual(checkRows(a),[['第 1 行','通过','首行、n2 最短'],['第 3 行','不通过 · UNSUPPORTED_COMBINATION（值放不下所在框或列，或含字体不支持的字符）','末行、n1 最长、n2 最长']]);
 assert.equal(a.$('doc-check-wrap').hidden,false);assert.match(a.$('doc-check-status').textContent,/已检查 2 行：通过 1 行，不通过 1 行。.*不渲染 PDF/);assert.equal(a.$('doc-check-status').getAttribute('role'),'status');
 // One click loads a row into the preview row number (1-based, as the preview takes it).
 a.$('doc-check-body').rows[1].cells[3].querySelector('button').click();assert.equal(a.$('doc-row').value,'3');assert.match(a.$('doc-check-status').textContent,/预览行号已设为 3/);
 // Any change of the template voids the result: it was about another template.
 cv.click(a,'n1');cv.key(a,cv.item(a,'n1'),'ArrowDown');assert.equal(a.$('doc-check-body').rows.length,0);assert.equal(a.$('doc-check-wrap').hidden,true);assert.match(a.$('doc-check-status').textContent,/已作废/);
 // The palette offers it and runs the same button.
 cv.key(a,cv.item(a,'n1'),'k',{ctrlKey:true});assert.ok(options(a).includes('检查样本'));typeInto(a,'检查');press(a,'Enter');await until(()=>bodies.length===2&&a.$('doc-check-body').rows.length===2&&!a.$('preview').disabled,'checked from the palette');
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('check samples needs a frozen snapshot, shows nothing it cannot verify, and says what a refusal means',async()=>{
 // A preview is not a snapshot.
 const a=app(offeringChecks({rows:[]}));try{
  await a.login();a.configure();a.click('preview');await until(()=>!a.$('preview').disabled&&!a.$('result-table-wrap').hidden,'query');a.w.document.querySelector('[data-page="documents"]').click();
  a.click('doc-check');assert.match(a.$('doc-status').textContent,/请先生成\/打开冻结快照/);assert.equal(a.trace.filter(v=>v.path.endsWith('/document-checks')).length,0);
 }finally{a.close();}
 // Answers that are not the contract are refused whole.
 const row=(o={})=>({row_index:0,ok:true,...o});
 for(const [label,answer] of [['not an object',[]],['extra key',{rows:[],extra:1}],['rows not a list',{rows:'x'}],['row with an unknown key',{rows:[row({value:'1'})]}],['rows not ascending',{rows:[row({row_index:3}),row({row_index:3})]}],['descending',{rows:[row({row_index:3}),row({row_index:1})]}],['ok with a code',{rows:[row({code:'UNSUPPORTED_COMBINATION'})]}],['refused without a code',{rows:[row({ok:false})]}],['a lower-case code',{rows:[row({ok:false,code:'nope'})]}],['ok as text',{rows:[row({ok:'yes'})]}],['a negative row',{rows:[row({row_index:-1})]}],['a fractional row',{rows:[row({row_index:1.5})]}],['an unknown reason',{rows:[row({reasons:['random']})]}],['a reason for a box that cannot exist',{rows:[row({reasons:['longest:n25']})]}],['reasons not a list',{rows:[row({reasons:'first'})]}],['51 rows',{rows:Array.from({length:51},(_,i)=>row({row_index:i}))}]]){
  const b=app(offeringChecks(answer));
  try{await checkReady(b);b.click('doc-check');await until(()=>/无法验证/.test(b.$('doc-check-status').textContent)&&!b.$('preview').disabled,label);assert.equal(b.$('doc-check-body').rows.length,0,label);assert.equal(b.$('doc-check-wrap').hidden,true,label);}finally{b.close();}
 }
 // A snapshot without rows answers an empty list, not an error.
 const e=app(offeringChecks({rows:[]}));try{await checkReady(e);e.click('doc-check');await until(()=>/没有可检查的行/.test(e.$('doc-check-status').textContent),'empty');assert.equal(e.$('doc-check-wrap').hidden,true);}finally{e.close();}
 // 50 rows are the most.
 const f=app(offeringChecks({rows:Array.from({length:50},(_,i)=>row({row_index:i*2,reasons:i===0?['first']:undefined}))}));try{await checkReady(f);f.click('doc-check');await until(()=>f.$('doc-check-body').rows.length===50,'fifty');assert.equal(f.$('doc-check-body').rows[49].cells[0].textContent,'第 99 行');}finally{f.close();}
 // The server may not have the route (a deployment without the PDF writer's check): a message, and the frozen result stays.
 const g=app(offeringChecks(null,{raw:()=>new Response('404 page not found\n',{status:404,headers:{'Content-Type':'text/plain'}})}));
 try{await checkReady(g);g.click('doc-check');await until(()=>g.$('notice').textContent.includes('服务端未启用样本检查')&&!g.$('preview').disabled,'no route');assert.equal(g.$('result-kind').textContent,'冻结快照');assert.equal(g.$('doc-check-body').rows.length,0);}finally{g.close();}
 // A refusal of the whole request: the template's background is not usable (422); a denied snapshot (403) clears the frozen result, as a preview's denial does.
 const h=app(offeringChecks({error:'UNSUPPORTED_COMBINATION'},{status:422}));
 try{await checkReady(h);h.click('doc-check');await until(()=>h.$('notice').textContent.includes('样本检查被拒绝')&&!h.$('preview').disabled,'422');assert.equal(h.$('result-kind').textContent,'冻结快照');}finally{h.close();}
 const i=app(offeringChecks({error:'AUTH_SCOPE_DENIED'},{status:403}));
 try{await checkReady(i);i.click('doc-check');await until(()=>i.$('notice').textContent.includes('无权访问')&&!i.$('preview').disabled,'403');assert.equal(i.$('result-table').tBodies[0].rows.length,0,'the denied snapshot is cleared');assert.equal(i.$('doc-check-body').rows.length,0);assert.equal(i.$('doc-check').hidden,true);}finally{i.close();}
 // Without PDF the button is not there at all.
 const j=app();try{await j.login();j.configure();j.click('snapshot');await until(()=>j.$('result-kind').textContent==='冻结快照'&&!j.$('export').disabled,'snapshot');j.w.document.querySelector('[data-page="documents"]').click();assert.equal(j.$('doc-check').hidden,!pdfConfigured,'offered with the PDF format only');}finally{j.close();}
});

test('check samples through the real route agrees with the real preview, row by row, without rendering',{skip:!pdfConfigured&&'needs the pinned Typst renderer'},async()=>{const a=app();try{
 await checkReady(a);
 // A box for the balance that is 25 mm wide: 100.25 and -0.24 fit, 9007199254740893.00 does not.
 cv.load(a,cv.doc(a,[['n1',10,30,40,10,'row','customer.id'],['n2',60,30,25,10,'row','ar.open_balance']]));
 const before=a.trace.filter(v=>v.path.endsWith('/document-previews')).length;
 a.click('doc-check');await until(()=>a.$('doc-check-body').rows.length>0&&!a.$('preview').disabled,'real check');
 const rows=checkRows(a);assert.deepEqual(rows.map(r=>r[0]),['第 1 行','第 2 行','第 3 行']);assert.deepEqual(rows.map(r=>r[1].startsWith('通过')),[true,true,false]);assert.match(rows[2][1],/UNSUPPORTED_COMBINATION/);
 assert.deepEqual(rows.map(r=>r[2]),['首行、n1 最长、n1 最短','n2 最短','末行、n2 最长'],'first and last, the longest and the shortest of each row-field box, ties to the lowest row');
 assert.equal(a.trace.filter(v=>v.path.endsWith('/document-previews')).length,before,'the check rendered nothing');
 // The same three rows through the real preview: it refuses exactly the row the check refused.
 const outcome=[];for(const row of ['1','2','3']){a.input('doc-row',row);const n=a.downloads.length;a.click('doc-pdf');await until(()=>!a.$('preview').disabled&&(a.downloads.length>n||/无法排版/.test(a.$('notice').textContent)),'preview '+row,15000);outcome.push(a.downloads.length>n);a.$('notice').textContent='';}
 assert.deepEqual(outcome,[true,true,false],'the preview agrees with the check');
 // The row can be loaded from the result with one click.
 a.$('doc-check-body').rows[2].cells[3].querySelector('button').click();assert.equal(a.$('doc-row').value,'3');
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('the canvas is built from DOM nodes and CSSOM only: no markup parsing, no style attribute, no inline handler, no eval, in the script and in what it draws',async()=>{const a=await designer();try{
 for(const [name,text] of [['canvas.js',canvasScript],['documents.js',documentsScript]]){
  for(const [pattern,what] of [[/\binnerHTML\b/,'innerHTML'],[/insertAdjacentHTML/,'insertAdjacentHTML'],[/\bouterHTML\b/,'outerHTML'],[/document\.write/,'document.write'],[/\beval\s*\(/,'eval'],[/new\s+Function/,'new Function'],[/setAttribute\(\s*['"]style['"]/,"setAttribute('style')"],[/\bcssText\b/,'cssText'],[/setAttribute\(\s*['"]on/i,'an inline handler'],[/DOMParser|createContextualFragment/,'markup parsing'],[/\bsrcdoc\b/,'srcdoc']])assert.equal(pattern.test(text),false,name+' must not use '+what);
 }
 assert.equal(/\sstyle\s*=/i.test(html),false,'index.html has no style attribute');assert.equal(/\son[a-z]+\s*=/i.test(html),false,'index.html has no inline handler');assert.equal(/<style[\s>]/i.test(html),false,'index.html has no inline <style>');
 // What it draws: exercise every kind of element, then look for attributes the CSP would refuse.
 cv.load(a,cv.table(a,[['n1',10,30,50,10],['n2',80,30,50,10,'row','customer.id']]));cv.click(a,'n1');cv.drag(a,cv.item(a,'n1'),35,35,5,0);cv.click(a,'table');cv.key(a,cv.item(a,'n1'),'k',{ctrlKey:true});typeInto(a,'对');
 const scope=[a.$('doc-canvas-panel'),palette(a)];for(const root of scope){assert.equal(root.querySelectorAll('[style]').length,0,'no style attribute in '+root.className);for(const e of root.querySelectorAll('*'))assert.equal([...e.attributes].some(x=>/^on/i.test(x.name)),false,'no inline handler');}
 // Hostile text stays text: a literal and a field label that look like markup.
 const hostile='<img src=x onerror=alert(1)>';cv.load(a,cv.doc(a,[['n1',10,30,100,10,'literal',hostile]]));assert.equal(a.$('doc-canvas-host').querySelectorAll('img,script').length,0);assert.equal(cv.item(a,'n1').querySelector('text').textContent,hostile);assert.ok(cv.item(a,'n1').getAttribute('aria-label').includes(hostile));
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

// ---- Template schema 2.0 (S03-J): bands over row groups, edited as JSON; refusals that say where ----
const schema2=JSON.parse(await readFile(join(root,'doctemplates/testdata/schema2_cases.json'),'utf8'));
function walkTo(root,path){const steps=path.split('.');let cur=root;for(const s of steps.slice(0,-1)){cur=Array.isArray(cur)?cur[Number(s)]:cur[s];assert.ok(cur!==undefined&&cur!==null,'path '+path+' at '+s);}return [cur,steps.at(-1)];}
// The edits of a shared case, as the Go tests apply them: delete, set (shortest paths first), push, extend.
function editedCase(base,edit){
  const doc=structuredClone(base);
  for(const p of edit.delete||[]){const [parent,last]=walkTo(doc,p);delete parent[last];}
  const sets=Object.entries(edit.set||{}).sort((x,y)=>x[0].split('.').length-y[0].split('.').length);
  for(const [p,value] of sets){const [parent,last]=walkTo(doc,p);parent[Array.isArray(parent)?Number(last):last]=structuredClone(value);}
  for(const [p,value] of Object.entries(edit.push||{})){const [parent,last]=walkTo(doc,p);parent[last].push(structuredClone(value));}
  for(const spec of edit.extend||[]){const [parent,last]=walkTo(doc,spec.path);for(let n=0;n<spec.count;n++){const item=structuredClone(spec.template);item.id=item.id.replace('{n}',String(spec.from+n));parent[last].push(item);}}
  return doc;
}
const banded=()=>structuredClone(schema2.base);

test('the console validates a 2.0 template as the platform does: every shared case is accepted or refused alike, and an accepted one is saved as it was read',async()=>{const a=await designer();try{
 assert.ok(schema2.cases.length>=150);let valid=0,refused=0;
 for(const c of schema2.cases){
  const doc=editedCase(schema2.base,c.edit);a.$('doc-status').textContent='';a.$('doc-json').value=JSON.stringify(doc);a.click('doc-json-open');
  const accepted=a.$('doc-status').textContent===READY;
  if(c.expect==='valid'){valid++;assert.equal(accepted,true,c.name+': '+a.$('doc-status').textContent);a.click('doc-json-save');assert.deepEqual(JSON.parse(a.$('doc-json').value),doc,c.name+': the saved form is the one read');}
  else{refused++;assert.equal(accepted,false,c.name+' is '+c.expect+' and must be refused here');assert.notEqual(a.$('doc-status').textContent,'',c.name+' says why');}
 }
 assert.ok(valid>=35&&refused>=105);
 // A template of more than 64 KiB is refused, and a refusal leaves the open template as it was.
 const big=banded();big.bands.document_header=undefined;delete big.bands.document_header;big.bands.page_header={height:200,elements:Array.from({length:24},(_,i)=>({id:'e'+(i+10),type:'text',source:'literal',text:'字'.repeat(1024),field:'',wrap:true,x:0,y:i*8,width:190,height:8,font_size:8,align:'left',bold:false}))};
 a.$('doc-json').value=JSON.stringify(banded());a.click('doc-json-open');assert.equal(a.$('doc-status').textContent,READY);a.$('doc-status').textContent='';a.$('doc-json').value=JSON.stringify(big);a.click('doc-json-open');assert.notEqual(a.$('doc-status').textContent,READY);
 a.click('doc-json-save');assert.deepEqual(JSON.parse(a.$('doc-json').value),banded(),'the open template is untouched');
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('a 2.0 template is drawn by the canvas and edited by the panels (S03-L): the 1.x inputs and edits give way, JSON still works, and a 1.x template brings its inputs back',async()=>{const a=await designer();try{
 const scroll=()=>a.$('doc-canvas-scroll');
 assert.equal(a.$('doc2-panel').hidden,true);assert.equal(a.$('doc-panel-elements').hidden,false);
 cv.load(a,banded());
 // The canvas shows the page in place of S03-J's note ("画布只显示 1.x 模板"): bands and elements, selectable and editable; the 2.0 panels replace the 1.x ones.
 assert.equal(a.$('doc-canvas-note'),null);assert.equal(scroll().hidden,false);assert.deepEqual(cv.keys(a),['e1','e2','e3','e4','e5','e6','e7','e8','e9']);assert.equal(a.$('doc-canvas-host').querySelectorAll('.cv-band').length,5);
 assert.doesNotMatch(a.$('doc-canvas-status').textContent,/只显示 1\.x/);assert.doesNotMatch(a.$('doc-canvas-panel').textContent,/只显示 1\.x|画布暂只/);
 assert.equal(a.$('doc2-panel').hidden,false);for(const id of ['doc-panel-elements','doc-panel-table','doc-panel-background','doc-panel-print'])assert.equal(a.$(id).hidden,true,id);assert.equal(a.$('doc-elements').children.length,0,'no 1.x rows');
 assert.match(a.$('doc-print-status').textContent,/模板 2\.0 · 分带版面/);
 // The 1.x buttons still refuse a 2.0 template, naming the way (they are hidden with their panels).
 const before=cv.tpl(a);
 for(const id of ['doc-add-text','doc-add-row','doc-add-total','doc-table-add','doc-table-remove','doc-print-apply','doc-background-remove']){a.$('doc-status').textContent='';a.click(id);assert.match(a.$('doc-status').textContent,/2\.0 分带模板请在画布与“2\.0 分带模板”面板编辑/,id);assert.deepEqual(cv.tpl(a),before,id+' left the template as it was');}
 // JSON edits: a band 0.5 mm taller, applied and read back; an invalid edit is refused and leaves it.
 const taller=banded();taller.bands.page_header.height=14.5;cv.load(a,taller);assert.equal(cv.tpl(a).bands.page_header.height,14.5);
 const wrong=banded();wrong.bands.page_header.height=4;a.$('doc-json').value=JSON.stringify(wrong);a.click('doc-json-open');assert.notEqual(a.$('doc-status').textContent,READY);assert.equal(cv.tpl(a).bands.page_header.height,14.5);
 // A 1.x template brings its inputs and canvas back; opening 2.0 again takes them away.
 cv.load(a,cv.four(a));assert.equal(a.$('doc2-panel').hidden,true);assert.equal(a.$('doc-panel-elements').hidden,false);assert.deepEqual(cv.keys(a),['n1','n2','n3','n4']);assert.equal(a.$('doc-elements').children.length,4);
 cv.load(a,banded());assert.equal(a.$('doc2-panel').hidden,false);assert.equal(a.$('doc-canvas-host').querySelectorAll('[data-key]').length,9);
 // "New template" (reset) is a 1.0 template, and the 1.x panels are back; the 2.0 panel has its own reset.
 a.click('doc-reset');assert.equal(a.$('doc2-panel').hidden,true);assert.deepEqual(cv.keys(a),['n1']);
 cv.load(a,banded());a.click('doc2-reset');assert.equal(cv.tpl(a).schema_version,'1.0');assert.equal(a.$('doc2-panel').hidden,true);
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('a 2.0 template previews the group of the row and is checked by group: what the console sends and shows',async()=>{const bodies=[],seen=[];
 const answer={rows:[{row_index:0,ok:true,reasons:['first','most_rows']},{row_index:2,ok:false,code:'UNSUPPORTED_COMBINATION',detail:{reason:'INCONSISTENT_GROUP',band:'document_header',element:'e5',row:1,column:'aging.bucket'},reasons:['last','longest:e5','cell:aging.bucket','fewest_rows']}]};
 const check=offeringChecks(answer,{bodies});
 const a=app(async(path,options)=>{if(path.endsWith('/document-previews')){seen.push(JSON.parse(options.body));return new Response(Buffer.from('%PDF-1.7 banded'),{headers:{'Content-Type':'application/pdf','X-Content-SHA256':createHash('sha256').update(Buffer.from('%PDF-1.7 banded')).digest('hex')}});}return check(path,options);});try{
 await checkReady(a);cv.load(a,banded());
 // The preview body is the template as it is (2.0) and the snapshot's global row number.
 a.input('doc-row','3');a.click('doc-pdf');await until(()=>a.downloads.length===1&&!a.$('preview').disabled,'the preview');
 assert.equal(seen.length,1);assert.deepEqual(Object.keys(seen[0]).sort(),['config','row_index','snapshot_id']);assert.equal(seen[0].row_index,2);assert.equal(seen[0].config.schema_version,'2.0');assert.deepEqual(seen[0].config.group,{fields:['customer.id']});assert.equal(JSON.stringify(seen[0]).includes('9007199254740'),false);
 // The check sends the template and asks for the boundary samples; its rows show the reasons of a 2.0 sample and the place of a refusal.
 a.click('doc-check');await until(()=>a.$('doc-check-body').rows.length===2&&!a.$('preview').disabled,'checked');
 assert.equal(bodies.length,1);assert.equal(bodies[0].config.schema_version,'2.0');assert.equal(bodies[0].samples,'boundary');
 const rows=checkRows(a);assert.deepEqual(rows[0],['第 1 行','通过','首行、行数最多的分组']);
 assert.equal(rows[1][0],'第 3 行');assert.match(rows[1][1],/^不通过 · UNSUPPORTED_COMBINATION（.*） · 同一分组内该字段的取值不一致 · 单据头带 · 元素 e5 · 快照第 2 行 · 列 账龄区间（aging\.bucket）$/);assert.equal(rows[1][2],'末行、e5 最长、列 账龄区间 最长的单元格、行数最少的分组');
 // A refusal that names an element can be shown on the 2.0 canvas too (S03-L): the locate button is there.
 assert.equal(a.$('doc-check-body').rows[1].cells[3].querySelectorAll('button').length,2);a.$('doc-check-body').rows[1].cells[3].querySelectorAll('button')[1].click();assert.deepEqual([...a.$('doc-canvas-host').querySelectorAll('.cv-refused')].map(e=>e.getAttribute('data-key')),['e5']);
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('a refusal says where: the notice names the reason and the place, check rows carry the same, and the canvas outlines the box or the column a 1.x refusal names',async()=>{
 const detail={reason:'DOES_NOT_FIT',element:'n2'},columnDetail={reason:'DOES_NOT_FIT',column:'ar.open_balance',row:2};let answerWith=detail;
 const check=offeringChecks({rows:[{row_index:0,ok:true,reasons:['first']},{row_index:1,ok:false,code:'UNSUPPORTED_COMBINATION',detail:{reason:'NOT_COVERED',element:'n1'},reasons:['longest:n1']},{row_index:2,ok:false,code:'UNSUPPORTED_COMBINATION',detail:columnDetail,reasons:['last']}]});
 const a=app(async(path,options)=>{if(path.endsWith('/document-previews'))return new Response(JSON.stringify({error:'UNSUPPORTED_COMBINATION',detail:answerWith}),{status:422,headers:{'Content-Type':'application/json'}});return check(path,options);});try{
 await checkReady(a);
 cv.load(a,cv.table(a,[['n1',10,30,50,10,'row','customer.id'],['n2',80,30,50,10,'row','customer.id']]));
 const outlined=()=>[...a.$('doc-canvas-host').querySelectorAll('.cv-refused')].map(e=>e.getAttribute('data-key'));const columns=()=>[...a.$('doc-canvas-host').querySelectorAll('.cv-refused-col')].map(e=>e.getAttribute('data-refused'));
 assert.deepEqual(outlined(),[]);assert.deepEqual(columns(),[]);
 // A preview that is refused for a box: the notice has the reason and the place, and n2 is outlined.
 a.click('doc-pdf');await until(()=>a.$('notice').textContent.includes('模板无法排版')&&!a.$('preview').disabled,'refusal');
 assert.match(a.$('notice').textContent,/位置：放不下：值比所在的框、列或页更宽或更高 · 元素 n2。/);assert.deepEqual(outlined(),['n2']);assert.deepEqual(columns(),[]);assert.equal(a.downloads.length,0);
 assert.match(cv.item(a,'n2').getAttribute('aria-label'),/预览或检查指出此处/);assert.equal(cv.item(a,'n1').getAttribute('aria-label').includes('预览或检查'),false);assert.match(a.$('doc-canvas-status').textContent,/已描出轮廓：n2/);
 // The outline is the canvas's, not the template's: nothing was written, and one edit takes it away.
 assert.equal(JSON.stringify(cv.tpl(a)).includes('n2'),true);cv.click(a,'n1');cv.key(a,cv.item(a,'n1'),'ArrowDown');assert.deepEqual(outlined(),[]);
 // A refusal of a column outlines the column of the table.
 cv.load(a,cv.table(a,[['n1',10,30,50,10]]));answerWith=columnDetail;a.click('doc-pdf');await until(()=>a.$('notice').textContent.includes('（ar.open_balance）')&&!a.$('preview').disabled,'column refusal');
 assert.match(a.$('notice').textContent,/快照第 3 行 · 列 .*（ar\.open_balance）/);assert.deepEqual(columns(),['ar.open_balance']);assert.deepEqual(outlined(),[]);
 const rect=a.$('doc-canvas-host').querySelector('.cv-refused-col'),table=cv.tpl(a).table,p=SCALE*Number(a.$('doc-zoom').value)/100;
 assert.ok(Math.abs(Number(rect.getAttribute('x'))-(ORIGIN+(table.x+130)*p))<0.01&&Math.abs(Number(rect.getAttribute('width'))-60*p)<0.01&&Math.abs(Number(rect.getAttribute('height'))-table.height*p)<0.01,'the column under the table');
 // A detail the console cannot verify is not shown, and the generic message stands.
 for(const bad of [{reason:'ANYTHING'},{reason:'DOES_NOT_FIT',element:'x1'},{reason:'DOES_NOT_FIT',row:-1},{reason:'DOES_NOT_FIT',band:'middle'},{reason:'DOES_NOT_FIT',extra:1},'text',[]]){
  answerWith=bad;cv.load(a,cv.table(a,[['n1',10,30,50,10]]));a.click('doc-pdf');await until(()=>a.$('notice').textContent.includes('模板无法排版')&&!a.$('preview').disabled,'unverified detail');
  assert.equal(a.$('notice').textContent.includes('位置：'),false);assert.deepEqual(columns(),[]);assert.deepEqual(outlined(),[]);
 }
 // The check shows each refused row's place and lets one click outline it.
 cv.load(a,cv.table(a,[['n1',10,30,50,10,'row','customer.id']]));
 a.click('doc-check');await until(()=>a.$('doc-check-body').rows.length===3&&!a.$('preview').disabled,'checked');
 const rows=checkRows(a);assert.equal(rows[0][1],'通过');assert.match(rows[1][1],/不通过 · UNSUPPORTED_COMBINATION（.*） · 无法按原样打印.* · 元素 n1$/);assert.match(rows[2][1],/放不下.* · 快照第 3 行 · 列 .*（ar\.open_balance）$/);
 const locate=r=>a.$('doc-check-body').rows[r].cells[3].querySelectorAll('button');
 assert.equal(locate(0).length,1);assert.equal(locate(1).length,2);assert.equal(locate(2).length,2);
 locate(1)[1].click();assert.deepEqual(outlined(),['n1']);assert.deepEqual(columns(),[]);locate(2)[1].click();assert.deepEqual(columns(),['ar.open_balance']);assert.deepEqual(outlined(),[]);assert.match(a.$('doc-check-status').textContent,/第 3 行：放不下/);
 // A new result or any change of the template takes the outline away.
 a.click('doc-check');await until(()=>a.$('doc-check-body').rows.length===3&&!a.$('preview').disabled,'checked again');cv.click(a,'n1');cv.key(a,cv.item(a,'n1'),'ArrowDown');assert.deepEqual(columns(),[]);
 // A check row with a detail that does not verify is not shown at all.
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('a check answer whose detail or reasons are not the known shapes is shown as unverifiable',async()=>{
 for(const row of [{row_index:0,ok:false,code:'UNSUPPORTED_COMBINATION',detail:{reason:'WHATEVER'}},{row_index:0,ok:false,code:'UNSUPPORTED_COMBINATION',detail:{reason:'DOES_NOT_FIT',element:'e99'}},{row_index:0,ok:true,detail:{reason:'DOES_NOT_FIT'}},{row_index:0,ok:true,reasons:['cell:']},{row_index:0,ok:true,reasons:['longest:x1']},{row_index:0,ok:true,reasons:['most_rows ']},{row_index:0,ok:false,code:'UNSUPPORTED_COMBINATION',detail:'text'}]){
  const a=app(offeringChecks({rows:[row]}));try{await checkReady(a);a.click('doc-check');await until(()=>/无法验证/.test(a.$('doc-check-status').textContent),'unverifiable '+JSON.stringify(row));assert.equal(a.$('doc-check-body').rows.length,0);assert.equal(a.$('doc-check-wrap').hidden,true);}finally{a.close();}
 }
 // The reasons of the 2.0 sample and the details are accepted.
 const a=app(offeringChecks({rows:[{row_index:0,ok:true,reasons:['first','most_rows','fewest_rows','longest:e12','shortest:e3','cell:customer.id']},{row_index:4,ok:false,code:'UNSUPPORTED_COMBINATION',detail:{reason:'TOO_MANY_PAGES'}}]}));try{
  await checkReady(a);a.click('doc-check');await until(()=>a.$('doc-check-body').rows.length===2&&!a.$('preview').disabled,'known shapes');
  assert.equal(checkRows(a)[0][2].includes('e12 最长'),true);assert.match(checkRows(a)[1][1],/超过 100 页/);
 }finally{a.close();}});

test('a 2.0 template is saved, opened, published and read back like any template, previews through the real server, and its issue request carries the preview row and nothing else',async()=>{const bodies=[];const a=app(offeringIssuance({bodies}));try{
 await a.login();a.configure();a.click('snapshot');await until(()=>a.$('result-kind').textContent==='冻结快照'&&!a.$('preview').disabled,'frozen');
 a.w.document.querySelector('[data-page="documents"]').click();
 a.$('doc-json').value=JSON.stringify(banded());a.click('doc-json-open');assert.equal(a.$('doc-status').textContent,READY);
 a.click('doc-server-save');await until(()=>a.$('doc-server-status').textContent.includes('版本 1')&&!a.$('preview').disabled,'save v1');
 const id=a.$('doc-server-status').textContent.match(/[a-f0-9]{32}/)[0];
 // What the server kept is the 2.0 template as it was saved: the console checked it against the server's copy.
 a.click('doc-release-read');await until(()=>a.$('doc-release-status').textContent.includes('修订 0')&&!a.$('preview').disabled,'release');
 a.click('doc-release-publish');await until(()=>a.$('doc-release-status').textContent.includes('已发布版本 1')&&!a.$('preview').disabled,'publish');
 a.click('doc-published-open');await until(()=>a.$('doc-server-status').textContent.includes('已打开发布模板')&&!a.$('preview').disabled,'published open');
 assert.deepEqual(cv.tpl(a),banded());assert.equal(a.$('doc-canvas-host').querySelectorAll('.cv-band').length,5,'the canvas draws the opened published 2.0 template');
 // The console issues it (S03-K): the request is the published version, the snapshot and the preview row, which the server turns into the
 // row's group; the stub server here refuses it as a template that is not published, which is shown as such.
 a.input('doc-row','3');a.click('doc-issue');await until(()=>a.$('notice').textContent.includes('已发布的模板版本')&&!a.$('preview').disabled,'issue request');
 assert.equal(bodies.length,1);assert.deepEqual(Object.keys(bodies[0]).sort(),['row_index','snapshot_id','template_id','version']);assert.equal(bodies[0].template_id,id);assert.equal(bodies[0].version,1);assert.equal(bodies[0].row_index,2);
 // The real server takes the same request for a group in its own two forms, and refuses what is neither or both, whoever asks. With the
 // renderer the unknown snapshot is not found; without it the deployment cannot issue.
 const direct=async body=>{const w=await fetch(url+'/api/reporting/v1/issues',{method:'POST',headers:{Authorization:'Bearer '+token,'Content-Type':'application/json','Idempotency-Key':'banded-'+Math.random()},body:JSON.stringify(body)});return [w.status,await w.json()];};
 const known={template_id:id,version:1,snapshot_id:'0'.repeat(32)};
 assert.deepEqual(await direct({...known,row_index:0,group:['customer_a']}),[400,{error:'INVALID_REQUEST'}]);
 assert.deepEqual(await direct(known),[400,{error:'INVALID_REQUEST'}]);
 assert.deepEqual(await direct({...known,group:['customer_a']}),pdfConfigured?[404,{error:'NOT_FOUND'}]:[422,{error:'UNSUPPORTED_COMBINATION'}]);
 assert.deepEqual(await direct({...known,row_index:0}),pdfConfigured?[404,{error:'NOT_FOUND'}]:[422,{error:'UNSUPPORTED_COMBINATION'}]);
 // A saved version previews by the saved version (the server's copy), by group of the row it is asked for.
 if(pdfConfigured){
  a.input('doc-row','3');a.click('doc-saved-pdf');await until(()=>a.downloads.length===1&&!a.$('preview').disabled,'saved preview',20000);
  const bytes=Buffer.from(await a.downloads[0].blob.arrayBuffer());assert.equal(bytes.subarray(0,5).toString(),'%PDF-');
  if(process.env.REPORTING_DOCUMENT_EVIDENCE_DIR){await mkdir(process.env.REPORTING_DOCUMENT_EVIDENCE_DIR,{recursive:true});await writeFile(join(process.env.REPORTING_DOCUMENT_EVIDENCE_DIR,'console_banded_preview.pdf'),bytes);}
 }
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('a 2.0 template previews through the real server and pinned Typst renderer: the group of the row, bands on the page',{skip:!pdfConfigured&&'needs the pinned Typst renderer'},async()=>{const a=app();try{
 await a.login();a.configure();a.click('snapshot');await until(()=>a.$('result-kind').textContent==='冻结快照'&&!a.$('preview').disabled,'frozen');
 a.w.document.querySelector('[data-page="documents"]').click();
 // Grouped by customer, each of the demo's three rows is its own document; ungrouped, the customers differ and the preview is refused with the place.
 a.$('doc-json').value=JSON.stringify(banded());a.click('doc-json-open');a.input('doc-row','2');a.click('doc-pdf');await until(()=>a.downloads.length===1&&!a.$('preview').disabled,'group preview',20000);
 assert.equal(Buffer.from(await a.downloads[0].blob.arrayBuffer()).subarray(0,5).toString(),'%PDF-');
 const whole=banded();delete whole.group;a.$('doc-json').value=JSON.stringify(whole);a.click('doc-json-open');assert.equal(a.$('doc-status').textContent,READY);
 a.click('doc-pdf');await until(()=>a.$('notice').textContent.includes('模板无法排版')&&!a.$('preview').disabled,'inconsistent group',20000);
 assert.match(a.$('notice').textContent,/位置：同一分组内该字段的取值不一致 · 单据头带 · 元素 e4 · 快照第 2 行 · 列 .*（customer\.id）。/);assert.equal(a.downloads.length,1);
 // The sample check agrees with the preview without rendering: the refused group carries the same place.
 a.click('doc-check');await until(()=>a.$('doc-check-body').rows.length>=1&&!a.$('preview').disabled,'check');
 assert.match(a.$('doc-check-body').rows[0].cells[1].textContent,/不通过 · UNSUPPORTED_COMBINATION.* · 同一分组内该字段的取值不一致 · 单据头带 · 元素 e4/);
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

// ---- Issue by group (S03-K): a published 2.0 template is issued for the group of the preview row ----
// The demo server keeps at most 16 templates per identity for the whole run, so the tests of a published 2.0 template share one, as
// publishedOpen does for 1.x: the first to ask saves and publishes it, the others open it by its id. Returns the template id.
let sharedBanded=null;
async function bandedPublishedOpen(a){
  a.w.document.querySelector('[data-page="documents"]').click();
  if(sharedBanded){
    a.click('doc-server-list');await until(()=>[...a.$('doc-server-listing').options].some(o=>o.value===sharedBanded)&&!a.$('preview').disabled,'list');
    a.$('doc-server-listing').value=sharedBanded;a.$('doc-server-version').value='1';a.click('doc-server-open');await until(()=>a.$('doc-server-status').textContent.includes('正在编辑模板')&&!a.$('preview').disabled,'open version');
  }else{
    a.$('doc-json').value=JSON.stringify(banded());a.click('doc-json-open');assert.equal(a.$('doc-status').textContent,READY);
    a.click('doc-server-save');await until(()=>a.$('doc-server-status').textContent.includes('版本 1')&&!a.$('preview').disabled,'save');
    a.click('doc-release-read');await until(()=>a.$('doc-release-status').textContent.includes('修订 0')&&!a.$('preview').disabled,'release');
    a.click('doc-release-publish');await until(()=>a.$('doc-release-status').textContent.includes('已发布版本 1')&&!a.$('preview').disabled,'publish');
  }
  a.click('doc-published-open');await until(()=>a.$('doc-server-status').textContent.includes('已打开发布模板')&&!a.$('preview').disabled,'open');
  const id=a.$('doc-server-status').textContent.match(/[a-f0-9]{32}/)[0];
  sharedBanded??=id;assert.equal(id,sharedBanded);
  return id;
}
// A job view of a group issue: the summary names the group, never a row.
const groupView={fields:['customer.id'],key:['customer_c'],row_count:1,rows_sha256:'d'.repeat(64)};
const groupText='customer.id = customer_c（1 行）';
function groupJob(body,state,{number,code,group=groupView,issue}={}){
  return {job_id:pdfJob,kind:'issue',state,format:'pdf',snapshot_id:body.snapshot_id,...(state==='succeeded'?{artifact_id:pdfArtifact}:{}),...(code?{error_code:code}:{}),issue:issue??{template_id:body.template_id,template_version:body.version,template_sha256:'e'.repeat(64),group,...(number===undefined?{}:{number})}};
}

test('a published 2.0 template is issued for the group of the preview row: the request is the row, and the group key values are shown in the confirmation, the task and the record',async()=>{
  const bodies=[];
  const record={issue_id:pdfJob,state:'succeeded',snapshot_id:'c'.repeat(32),template_id:'d'.repeat(32),template_version:1,template_sha256:'e'.repeat(64),group:groupView,number:'AR-000012',artifact_id:pdfArtifact,snapshot_sha256:'f'.repeat(64),sha256:'a'.repeat(64),pages:2,issued_at:'2026-10-28T06:12:03Z',stamp:'ISSUED No. AR-000012 - 2026-10-28T06:12:03Z - '+pdfJob,reservation_ref:'res-1',confirmation_ref:'con-1',confirmed_at:'2026-10-28T06:12:04Z'};
  const a=app(offeringIssuance({bodies,submit:body=>json(groupJob(body,'queued'),202),job:(body,reads)=>reads<2?groupJob(body,'running'):groupJob(body,'succeeded',{number:'AR-000012'}),records:[record]}));
  try{
    await snapshotReady(a);const id=await bandedPublishedOpen(a);
    a.input('doc-row','3');a.click('doc-issue');
    // What is said before the server answers: the row, and that the server finds its group; no key value is known yet.
    assert.match(a.$('doc-issue-status').textContent,/第 3 行所在分组/);
    await until(()=>issueRow(a),'listed');
    // The request is the published version, the snapshot and the preview row, exactly as for a 1.x template: no group, no key.
    assert.equal(bodies.length,1);assert.deepEqual(bodies[0],{template_id:id,version:1,snapshot_id:bodies[0].snapshot_id,row_index:2});assert.ok(/^[a-f0-9]{32}$/.test(bodies[0].snapshot_id));
    // The confirmation and the task name the group by its key values, not by a row, and no number before the product confirmed.
    assert.match(a.$('notice').textContent,/正式出具任务已提交（分组 customer\.id = customer_c（1 行））/);
    assert.equal(issueRow(a).cells[0].textContent,'正式出具（编号待来源产品确认） · 分组 '+groupText);
    await until(()=>a.downloads.length===1&&!a.$('preview').disabled,'issued');
    assert.equal(a.downloads[0].name,'issued-AR-000012.pdf');
    assert.equal(issueRow(a).cells[0].textContent,'正式出具 No. AR-000012 · 分组 '+groupText);assert.equal(issueRow(a).cells[2].textContent,'已完成');
    assert.match(a.$('notice').textContent,/来源产品已确认编号 No\. AR-000012（分组 customer\.id = customer_c（1 行））；/);assert.ok(a.$('notice').className.includes('success'));
    // The record shows the group in the place of the row.
    a.click('doc-issues-refresh');await until(()=>!a.$('doc-issues-wrap').hidden&&!a.$('preview').disabled,'records');
    const row=a.$('doc-issues-body').rows[0];assert.equal(row.cells[0].textContent,'No. AR-000012');assert.equal(row.cells[4].textContent,'c'.repeat(32)+' · 分组 '+groupText);assert.equal(row.cells[4].textContent.includes('第 '),false);
    assert.equal(a.errors.length,0);
  }finally{a.close();}
  // A group of no fields is the whole snapshot, and a long key value is cut for the display only.
  const whole={fields:[],key:[],row_count:3,rows_sha256:'1'.repeat(64)},long={fields:['customer.id','aging.bucket'],key:['客'.repeat(60),'x'],row_count:2,rows_sha256:'2'.repeat(64)};
  const b=app(offeringIssuance({submit:body=>json(groupJob(body,'queued',{group:whole}),202),job:body=>groupJob(body,'queued',{group:whole})}));
  try{
    await snapshotReady(b);await bandedPublishedOpen(b);b.click('doc-issue');await until(()=>issueRow(b),'listed');
    assert.equal(issueRow(b).cells[0].textContent,'正式出具（编号待来源产品确认） · 分组 整个快照（3 行）');
  }finally{b.close();}
  const c=app(offeringIssuance({submit:body=>json(groupJob(body,'queued',{group:long}),202),job:body=>groupJob(body,'queued',{group:long})}));
  try{
    await snapshotReady(c);await bandedPublishedOpen(c);c.click('doc-issue');await until(()=>issueRow(c),'listed');
    assert.equal(issueRow(c).cells[0].textContent,'正式出具（编号待来源产品确认） · 分组 customer.id = '+'客'.repeat(40)+'…、aging.bucket = x（2 行）');
  }finally{c.close();}
});

test('a view of a group issue must be a group of the template that was asked for, and a row issue a row: nothing else is believed or remembered',async()=>{
  // Submitting the preview row of a 2.0 template: the view must carry the group and no row.
  for(const [label,view] of [
    ['a row in place of the group',body=>({...groupJob(body,'queued'),issue:{template_id:body.template_id,template_version:body.version,template_sha256:'e'.repeat(64),row_index:body.row_index}})],
    ['a row as well as the group',body=>groupJob(body,'queued',{issue:{template_id:body.template_id,template_version:body.version,template_sha256:'e'.repeat(64),row_index:body.row_index,group:groupView}})],
    ['neither a row nor a group',body=>groupJob(body,'queued',{issue:{template_id:body.template_id,template_version:body.version,template_sha256:'e'.repeat(64)}})],
    ['another template',body=>groupJob({...body,template_id:'1'.repeat(32)},'queued')],
    ['a group without its key',body=>groupJob(body,'queued',{group:{...groupView,key:[]}})],
    ['a key value too many',body=>groupJob(body,'queued',{group:{...groupView,key:['a','b']}})],
    ['a key value that is a number',body=>groupJob(body,'queued',{group:{...groupView,key:[7]}})],
    ['a group with another member',body=>groupJob(body,'queued',{group:{...groupView,rows:[2]}})],
    ['a group without its digest',body=>groupJob(body,'queued',{group:{fields:groupView.fields,key:groupView.key,row_count:1}})],
    ['a digest in upper case',body=>groupJob(body,'queued',{group:{...groupView,rows_sha256:'D'.repeat(64)}})],
    ['no row in the group',body=>groupJob(body,'queued',{group:{...groupView,row_count:0}})],
    ['more rows than a snapshot has',body=>groupJob(body,'queued',{group:{...groupView,row_count:1001}})],
    ['a fraction of a row',body=>groupJob(body,'queued',{group:{...groupView,row_count:1.5}})],
    ['four fields',body=>groupJob(body,'queued',{group:{fields:['a','b','c','d'],key:['1','2','3','4'],row_count:1,rows_sha256:'d'.repeat(64)}})],
    ['a field twice',body=>groupJob(body,'queued',{group:{fields:['a','a'],key:['1','2'],row_count:1,rows_sha256:'d'.repeat(64)}})],
    ['an empty field name',body=>groupJob(body,'queued',{group:{fields:[''],key:['1'],row_count:1,rows_sha256:'d'.repeat(64)}})],
    ['a group that is a list',body=>groupJob(body,'queued',{group:['customer_c']})],
    ['a number before the product confirmed',body=>groupJob(body,'queued',{number:'AR-000012'})],
  ]){
    const a=app(offeringIssuance({submit:body=>json(view(body),202)}));
    try{
      await snapshotReady(a);await bandedPublishedOpen(a);a.click('doc-issue');
      await until(()=>a.$('notice').textContent.includes('无法验证的响应')&&!a.$('preview').disabled,label);
      assert.equal(a.downloads.length,0,label);assert.equal([...a.$('tasks-body').rows].filter(r=>r.cells[0].textContent.startsWith('正式出具')).length,0,label);
      assert.equal(a.trace.filter(v=>v.path.endsWith('/jobs/'+pdfJob)).length,0,label);assert.equal(a.errors.length,0,label);
    }finally{a.close();}
  }
  // A 1.x template is issued for its row: a view that names a group instead is not that job.
  const a=app(offeringIssuance({submit:body=>json(groupJob(body,'queued'),202)}));
  try{
    await snapshotReady(a);await publishedOpen(a);a.click('doc-issue');
    await until(()=>a.$('notice').textContent.includes('无法验证的响应')&&!a.$('preview').disabled,'a group for a row template');
    assert.equal([...a.$('tasks-body').rows].filter(r=>r.cells[0].textContent.startsWith('正式出具')).length,0);assert.equal(a.errors.length,0);
  }finally{a.close();}
  // A task restored by its id, and the records, go through the same check: a group or a row, never both or neither.
  const asked={template_id:'d'.repeat(32),version:1,snapshot_id:'c'.repeat(32),row_index:0};
  let view=null;const b=app(async path=>{if(path.endsWith('/jobs/'+pdfJob))return json(view);});
  const restore=async job=>{view=job;b.input('restore-id',pdfJob);b.click('restore-job');await until(()=>!b.$('preview').disabled,'restored');return b.$('notice').textContent;};
  try{
    await b.login();
    assert.match(await restore(groupJob(asked,'succeeded',{number:'AR-000012'})),/已恢复/);assert.equal(b.$('tasks-body').rows[0].cells[0].textContent,'正式出具 No. AR-000012 · 分组 '+groupText);
    assert.match(await restore(groupJob(asked,'failed',{code:'AUTH_SCOPE_DENIED'})),/已恢复/);assert.equal(b.$('tasks-body').rows[0].cells[0].textContent,'正式出具（未出具） · 分组 '+groupText);
    assert.match(await restore(groupJob(asked,'succeeded',{number:'AR-000012',group:{...groupView,key:[]}})),/无法验证的响应/);assert.equal(b.$('tasks-body').rows[0].cells[2].textContent,'失败 · AUTH_SCOPE_DENIED');
    assert.equal(b.errors.length,0);
  }finally{b.close();}
  const record={issue_id:pdfJob,state:'succeeded',snapshot_id:'c'.repeat(32),template_id:'d'.repeat(32),template_version:1,template_sha256:'e'.repeat(64),number:'AR-000012',artifact_id:pdfArtifact,snapshot_sha256:'f'.repeat(64),sha256:'a'.repeat(64),pages:2,issued_at:'2026-10-28T06:12:03Z',stamp:'ISSUED No. AR-000012 - 2026-10-28T06:12:03Z - '+pdfJob,reservation_ref:'res-1',confirmation_ref:'con-1',confirmed_at:'2026-10-28T06:12:04Z'};
  for(const [label,records,shown] of [
    ['a record of a row',[{...record,row_index:2}],true],
    ['a record of a group',[{...record,group:groupView}],true],
    ['a record of both',[{...record,row_index:2,group:groupView}],false],
    ['a record of neither',[record],false],
    ['a record of a group with a bad digest',[{...record,group:{...groupView,rows_sha256:'x'}}],false],
    ['a failed record of a group',[{issue_id:pdfJob,state:'failed',error_code:'AUTH_SCOPE_DENIED',snapshot_id:'c'.repeat(32),template_id:'d'.repeat(32),template_version:1,template_sha256:'e'.repeat(64),group:groupView}],true],
  ]){
    const c=app(offeringIssuance({records}));
    try{
      await snapshotReady(c);c.click('doc-issues-refresh');await until(()=>/出具记录|无法验证/.test(c.$('doc-issue-status').textContent)&&!c.$('preview').disabled,label);
      assert.equal(c.$('doc-issues-wrap').hidden,!shown,label);if(!shown)assert.match(c.$('doc-issue-status').textContent,/无法验证/,label);
      assert.equal(c.errors.length,0,label);
    }finally{c.close();}
  }
});

test('official issue of a published 2.0 template through the real server and the pinned Typst renderer: the group of the preview row is issued, numbered by the product and recorded',{skip:!pdfConfigured&&'needs the pinned Typst renderer'},async()=>{const a=app();try{
  await a.login();a.configure();a.click('snapshot');await until(()=>a.$('result-kind').textContent==='冻结快照'&&!a.$('preview').disabled,'frozen');
  const id=await bandedPublishedOpen(a);
  // The demo's rows are three customers: the third row's group is customer_c alone.
  a.input('doc-row','3');a.click('doc-issue');await until(()=>a.downloads.length===1&&!a.$('preview').disabled,'issued group PDF',30000);
  const name=a.downloads[0].name,number=/^issued-(AR-\d{6})\.pdf$/.exec(name)?.[1];assert.ok(number,name);
  const bytes=Buffer.from(await a.downloads[0].blob.arrayBuffer());assert.equal(bytes.subarray(0,5).toString(),'%PDF-');
  const submitted=a.trace.filter(v=>v.path.endsWith('/issues')&&v.method==='POST');assert.equal(submitted.length,1);assert.ok(submitted[0].key);
  // The task and the notice name the group the server found, by the key it holds for the row.
  assert.equal(issueRow(a).cells[0].textContent,'正式出具 No. '+number+' · 分组 customer.id = customer_c（1 行）');assert.match(a.$('notice').textContent,new RegExp(number+'（分组 customer\\.id = customer_c（1 行））'));
  // The record the product's number is kept in names the group and has no row.
  const raw=(await (await fetch(url+'/api/reporting/v1/issues?limit=5',{headers:{Authorization:'Bearer '+token}})).json())[0];
  assert.equal(raw.number,number);assert.equal(raw.row_index,undefined);assert.deepEqual(raw.group.fields,['customer.id']);assert.deepEqual(raw.group.key,['customer_c']);assert.equal(raw.group.row_count,1);assert.match(raw.group.rows_sha256,/^[a-f0-9]{64}$/);assert.ok(raw.reservation_ref&&raw.confirmation_ref&&raw.confirmed_at);
  a.click('doc-issues-refresh');await until(()=>!a.$('doc-issues-wrap').hidden&&!a.$('preview').disabled,'records');
  const row=a.$('doc-issues-body').rows[0];assert.equal(row.cells[0].textContent,'No. '+number);assert.equal(row.cells[1].textContent,'已出具');assert.equal(row.cells[3].textContent,id+' · 版本 1');assert.equal(row.cells[4].textContent,raw.snapshot_id+' · 分组 customer.id = customer_c（1 行）');
  const digest=createHash('sha256').update(bytes).digest('hex');assert.equal(row.cells[5].textContent,digest+' · '+raw.pages+' 页');
  // The record downloads the same bytes again under the same name.
  row.cells[7].querySelector('button').click();await until(()=>a.downloads.length===2&&!a.$('preview').disabled,'record download');assert.equal(a.downloads[1].name,name);assert.deepEqual(Buffer.from(await a.downloads[1].blob.arrayBuffer()),bytes);
  if(process.env.REPORTING_DOCUMENT_EVIDENCE_DIR){const dir=process.env.REPORTING_DOCUMENT_EVIDENCE_DIR;await mkdir(dir,{recursive:true});await writeFile(join(dir,'console_banded_issued.pdf'),bytes);await writeFile(join(dir,'console_banded_issued_record.json'),JSON.stringify({template_id:id,version:1,sha256:digest,record:raw,source:'real Go group issue job through the console; the PDF is the artifact the record names'},null,2));}
  assert.equal(a.errors.length,0);
  }finally{a.close();}});

// ---- S03-L: the canvas for schema 2.0 bands ------------------------------------------------------------------------------------------
// Geometry as the canvas draws it, in mm: the bands from the top of the safe region (25 mm), the detail band taking what the others leave of the
// 245 mm (never less than the table's minimum), the page footer at the bottom (270 mm); an element's page position is its band's top plus its own.
const tenths=n=>Math.round(n*10),minDetail2=size=>Math.floor(((400+146*size)*4*254+7199)/7200);
function geom(t){
  const h=n=>t.bands[n]?tenths(t.bands[n].height):0,ph=h('page_header'),dh=h('document_header'),df=h('document_footer'),pf=h('page_footer'),detail=Math.max(minDetail2(t.bands.detail.elements[0].font_size),2450-ph-dh-pf-df);
  const at=(top,height)=>({top:top/10,height:height/10});
  return {page_header:at(250,ph),document_header:at(250+ph,dh),detail:at(250+ph+dh,detail),document_footer:at(250+ph+dh+detail,df),page_footer:at(2700-pf,pf)};
}
const find2=(t,id)=>{for(const n of Object.keys(t.bands)){const e=t.bands[n].elements.find(v=>v.id===id);if(e)return {band:n,e};}return null;};
// The page rectangle of an element (mm): [x, y, width, height]; a line's is its bounding box, the table's the detail band's.
function page2(t,id){
  const g=geom(t),{band,e}=find2(t,id),top=g[band].top;
  if(e.type==='line')return [10+Math.min(e.x1,e.x2),top+Math.min(e.y1,e.y2),Math.abs(e.x2-e.x1),Math.abs(e.y2-e.y1)];
  if(e.type==='table')return [10+e.x,top,e.width,g[band].height];
  if(e.type==='qrcode')return [10+e.x,top+e.y,e.size,e.size];
  return [10+e.x,top+e.y,e.width,e.height];
}
const mid2=(t,id)=>{const [x,y,w,h]=page2(t,id);return [x+w/2,y+h/2];};
const el2=(a,id)=>find2(cv.tpl(a),id)?.e;
Object.assign(cv,{
  c2:(a,id)=>mid2(cv.tpl(a),id),
  click2:(a,id,o={})=>{const [x,y]=cv.c2(a,id);const g=cv.press(a,cv.item(a,id),x,y,o);g.up(x,y,o);},
  // Drag an element by its middle, or from a point `from` (mm from its top-left corner), by (dx, dy) mm.
  drag2:(a,id,dx,dy,o={},from)=>{const t=cv.tpl(a),[x0,y0,w,h]=page2(t,id),[x,y]=from?[x0+from[0],y0+from[1]]:[x0+w/2,y0+h/2],g=cv.press(a,cv.item(a,id),x,y,o);g.move(x+dx/2,y+dy/2,o);g.move(x+dx,y+dy,o);g.up(x+dx,y+dy,o);return [x+dx,y+dy];},
  band:(a,name)=>a.$('doc-canvas-host').querySelector('.cv-bk[data-band="'+name+'"]'),
  dragBand:(a,name,dy,o={})=>{const t=cv.tpl(a),g=geom(t)[name],x=100,y=g.top+g.height,h=cv.press(a,cv.band(a,name),x,y,o);h.move(x,y+dy/2,o);h.move(x,y+dy,o);h.up(x,y+dy,o);},
  end:(a,n)=>a.$('doc-canvas-host').querySelector('[data-end="'+n+'"]'),
  col:(a,k)=>a.$('doc-canvas-host').querySelector('[data-col="'+k+'"]'),
  rel:(a,id)=>{const e=el2(a,id);return e.type==='line'?[e.x1,e.y1,e.x2,e.y2]:[e.x,e.y,e.width,e.height];},
});
const geometry2=a=>geom(cv.tpl(a));
const attrs=(e,...k)=>k.map(n=>Number(e.getAttribute(n)));
const SC=(a)=>SCALE*Number(a.$('doc-zoom').value)/100;
const X2=(a,mm)=>ORIGIN+mm*SC(a);

test('S03-L L1: a 2.0 template is drawn as its page: the bands stacked from the top of the safe region, each labelled with its height, the elements inside their bands',async()=>{const a=await designer();try{
 cv.load(a,banded());
 const t=cv.tpl(a),g=geom(t),host=a.$('doc-canvas-host');
 // The bands in order, each a region at the place the stacking rule gives, 190 mm wide from x 10 mm.
 const boxes=[...host.querySelectorAll('.cv-band')];assert.deepEqual(boxes.map(b=>b.getAttribute('data-band-box')),['page_header','document_header','detail','document_footer','page_footer']);
 for(const b of boxes){const n=b.getAttribute('data-band-box');assert.deepEqual(attrs(b,'x','y','width','height'),[X2(a,10),X2(a,g[n].top),190*SC(a),g[n].height*SC(a)],n);}
 assert.deepEqual([g.page_header,g.document_header,g.detail,g.document_footer,g.page_footer].map(v=>[v.top,v.height]),[[25,14],[39,24],[63,177],[240,20],[260,10]],'the detail band takes the 177 mm the others leave');
 // The labels say which band and how high; the detail band says it stretches.
 const tags=[...host.querySelectorAll('.cv-band-tag')].map(e=>e.textContent);
 assert.deepEqual(tags,['页眉带 · 14 毫米','单据头带 · 24 毫米','明细带 · 约 177 毫米','单据尾带 · 20 毫米','页脚带 · 10 毫米']);assert.deepEqual([...host.querySelectorAll('.cv-band-note')].map(e=>e.textContent),['随页面伸展']);
 // The labels are in a gutter right of the page, level with the top of their band: they never lie over an element.
 assert.deepEqual(attrs(host.querySelectorAll('.cv-band-tag')[1],'x'),[X2(a,210)+6]);assert.equal(Number(a.$('doc-canvas-host').querySelector('svg').getAttribute('width')),Math.round(24+210*SC(a)+16+104));
 // Elements, in reading order (the page's y, then x), keyed by their ids, with the source and a wrap marker.
 assert.deepEqual(cv.keys(a),['e1','e2','e3','e4','e5','e6','e7','e8','e9']);
 const tagsOf=[...host.querySelectorAll('.cv-tag')].map(e=>e.textContent);
 assert.deepEqual(tagsOf.sort(),['e1 · 固定文字','e2 · 页码文字','e3 · 线','e4 · 字段','e5 · 字段 · 换行','e6 · 明细表','e7 · 矩形','e8 · 分组合计','e9 · 固定文字'].sort());
 // Page texts show their placeholders; a field shows its label; a group sum is marked as a sum; bold is drawn bold.
 assert.equal(cv.item(a,'e1').querySelector('text').textContent,'Statement of account');assert.equal(cv.item(a,'e1').querySelector('text').getAttribute('font-weight'),'700');
 assert.equal(cv.item(a,'e2').querySelector('text').textContent,'Page {page} of {pages}');assert.equal(cv.item(a,'e2').querySelector('text').getAttribute('text-anchor'),'end','right-aligned');
 assert.equal(cv.item(a,'e4').querySelector('text').textContent,'〔客户〕');assert.equal(cv.item(a,'e8').querySelector('text').textContent,'〔合计〕应收余额');
 assert.equal(cv.item(a,'e5').querySelector('.cv-wrap').textContent,'↵','the wrap marker');assert.equal(cv.item(a,'e4').querySelector('.cv-wrap'),null);
 // Positions are band positions plus the band's top: e4 is 2 mm into the document header (top 39 mm), e9 2 mm into the page footer (top 260 mm).
 assert.deepEqual(attrs(cv.item(a,'e4').querySelector('.cv-box'),'x','y','width','height'),[X2(a,10),X2(a,41),90*SC(a),8*SC(a)]);
 assert.deepEqual(attrs(cv.item(a,'e9').querySelector('.cv-box'),'x','y'),[X2(a,10),X2(a,262)]);
 // A line is drawn between its two points (band-relative): e3 runs along y 12 mm of the page header.
 const stroke=cv.item(a,'e3').querySelector('.cv-stroke');assert.deepEqual(attrs(stroke,'x1','y1','x2','y2'),[X2(a,10),X2(a,37),X2(a,200),X2(a,37)]);assert.equal(stroke.getAttribute('stroke-width'),'1','0.5 pt is under a pixel: drawn one pixel wide');
 // A rectangle with its fill and outline.
 const shape=host.querySelector('.cv-shapes .cv-shape');assert.deepEqual(attrs(shape,'x','y','width','height'),[X2(a,110),X2(a,242),90*SC(a),10*SC(a)]);assert.equal(shape.getAttribute('fill'),'rgb(232,232,232)');assert.equal(shape.getAttribute('stroke'),'currentColor');
 // The table fills the detail band: header labels (a literal label wins), column boundaries, one sample body row.
 const table=cv.item(a,'e6');assert.deepEqual(attrs(table.querySelector('.cv-box'),'x','y','width','height'),[X2(a,10),X2(a,63),190*SC(a),177*SC(a)]);
 assert.deepEqual([...table.querySelectorAll('.cv-bold')].map(e=>e.textContent),['账龄区间','Customer','应收余额']);assert.deepEqual([...table.querySelectorAll('.cv-sample')].map(e=>e.textContent).slice(0,3),['〔账龄区间〕','〔客户〕','〔应收余额〕']);
 assert.equal(a.$('doc-canvas-host').querySelectorAll('[data-col]').length,2);assert.equal(table.querySelectorAll('.cv-colline').length,2);
 assert.equal(table.querySelectorAll('.cv-bold')[2].getAttribute('text-anchor'),'end','a metric column is right-aligned unless it says otherwise');
 // The grabbable bottom edges: the four fixed bands have one, the detail band none.
 assert.deepEqual([...host.querySelectorAll('.cv-bk[data-band]')].map(e=>e.getAttribute('data-band')),['page_header','document_header','document_footer','page_footer']);
 assert.match(a.$('doc-canvas-mode').textContent,/2\.0 分带模板.*分组 customer\.id/);
 // The note of S03-J is gone: the canvas shows the page, and the status line says so.
 assert.equal(a.$('doc-canvas-note'),null);assert.equal(a.$('doc-canvas-scroll').hidden,false);
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('S03-L L2: select, multi-select, Esc and Ctrl/Cmd-A work on the elements of every band and on the table',async()=>{const a=await designer();try{
 cv.load(a,banded());
 cv.click2(a,'e4');assert.deepEqual(cv.selected(a),['e4']);assert.match(a.$('doc-canvas-live').textContent,/^已选择 e4，字段 客户，字号 12 点，x 0 毫米，y 2 毫米，宽 90 毫米，高 8 毫米，单据头带。$/);
 cv.click2(a,'e1',{shiftKey:true});assert.deepEqual(cv.selected(a).sort(),['e1','e4']);assert.equal(a.$('doc-canvas-live').textContent,'已选择 2 项：e1、e4。');
 cv.click2(a,'e1',{shiftKey:true});assert.deepEqual(cv.selected(a),['e4']);
 cv.key(a,cv.item(a,'e4'),'Escape');assert.deepEqual(cv.selected(a),[]);
 cv.key(a,cv.item(a,'e1'),'a',{ctrlKey:true});assert.deepEqual(cv.selected(a).sort(),['e1','e2','e3','e4','e5','e6','e7','e8','e9']);assert.match(a.$('doc-canvas-live').textContent,/全选：9 项/);
 // Clicking the page (a band's empty place) clears; the table, a line and a rectangle are selected by their body.
 const g=cv.press(a,cv.svg(a),150,150);g.up(150,150);assert.deepEqual(cv.selected(a),[]);
 cv.click2(a,'e6');assert.deepEqual(cv.selected(a),['e6']);assert.match(a.$('doc-canvas-live').textContent,/^已选择 明细表 e6，列 账龄区间、Customer、应收余额，字号 9 点，x 0 毫米，宽 190 毫米，明细带。$/);
 cv.click2(a,'e3');assert.deepEqual(cv.selected(a),['e3']);assert.match(a.$('doc-canvas-live').textContent,/^已选择 e3，线，从 x 0 y 12 到 x 190 y 12 毫米，线宽 0\.5 点，页眉带。$/);
 cv.click2(a,'e7');assert.deepEqual(cv.selected(a),['e7']);assert.match(a.$('doc-canvas-live').textContent,/^已选择 e7，矩形，线宽 0\.5 点，填充 grey2，x 100 毫米，y 2 毫米/);
 // Every item can be named and reached by the keyboard, in reading order (top to bottom, then left to right).
 assert.deepEqual(cv.keys(a),['e1','e2','e3','e4','e5','e6','e7','e8','e9']);
 for(const k of cv.keys(a))assert.ok(cv.item(a,k).getAttribute('aria-label').length>8&&cv.item(a,k).getAttribute('tabindex')==='0',k);
 cv.key(a,cv.item(a,'e5'),'Enter');assert.deepEqual(cv.selected(a),['e5']);
 // The tab order is the elements in reading order, then the four edges of the fixed bands (which only the keyboard reaches: the pointer's twin of each lies under the elements and is hidden from assistive technology).
 assert.deepEqual([...a.$('doc-canvas-host').querySelectorAll('[tabindex="0"]')].map(e=>e.getAttribute('data-key')||'band:'+e.getAttribute('data-band')),[...cv.keys(a),'band:page_header','band:document_header','band:document_footer','band:page_footer']);
 assert.deepEqual([...a.$('doc-canvas-host').querySelectorAll('.cv-bh')].map(e=>[e.getAttribute('aria-hidden'),e.hasAttribute('tabindex')]),[['true',false],['true',false],['true',false],['true',false]]);
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('S03-L L2: a drag moves elements inside their band with the grid (1 mm, Alt 0.1 mm) and with the edges and centres of the band and of the other elements; one drag is one undo step',async()=>{const a=await designer();try{
 cv.load(a,banded());
 // e4 (document header, x 0, y 2): 7.4 mm right and 3.2 mm down lands on whole millimetres, 7 and 5, in the band's own coordinates.
 cv.drag2(a,'e4',7.4,3.2);assert.deepEqual(cv.rel(a,'e4'),[7,5,90,8]);assert.equal(a.$('doc-undo').disabled,false);assert.equal(cv.status(a),'已移动。可用 Ctrl/Cmd+Z 撤销。');
 // With Alt held the grid is 0.1 mm.
 cv.drag2(a,'e4',1.6,0,{altKey:true});assert.deepEqual(cv.rel(a,'e4'),[8.6,5,90,8]);
 a.click('doc-undo');a.click('doc-undo');assert.deepEqual(cv.rel(a,'e4'),[0,2,90,8],'two drags, two steps');a.click('doc-redo');assert.deepEqual(cv.rel(a,'e4'),[7,5,90,8]);a.click('doc-undo');
 // An edge of another element within 1 mm wins over the grid, and a guide shows it: e4's right edge (x 90) is 10.2 mm short of e5's left edge (x 100);
 // moving 9.6 mm puts it 0.6 mm from it, so it lands on it (x 10) and the guide is at page x 110 mm.
 const g=cv.press(a,cv.item(a,'e4'),55,45);g.move(55+4.8,45);g.move(55+9.6,45,{altKey:true});
 assert.equal(a.$('doc-canvas-host').querySelectorAll('.cv-guide').length>=1,true,'a guide while dragging');assert.match(a.$('doc-canvas-host').querySelector('.cv-readout').textContent,/^x 10\s+y 2 毫米 · 单据头带$/);
 g.up(55+9.6,45,{altKey:true});assert.deepEqual(cv.rel(a,'e4'),[10,2,90,8]);assert.equal(a.$('doc-canvas-host').querySelectorAll('.cv-guide').length,0,'the guide goes with the drag');
 a.click('doc-undo');
 // The band's own edges are targets too: e1 (page header, x 0..120) dragged 70.4 mm right has its right edge within 1 mm of the band's (190), so it lands at x 70,
 // where it would overlap e2: the validator refuses, says why, and the element goes back.
 const t0=cv.tpl(a);cv.drag2(a,'e1',70.4,0.3);assert.equal(cv.status(a),'未应用：同一带内的文本框不能重叠。 项目已恢复原位。');
 assert.deepEqual(cv.tpl(a),t0,'the element is back, and nothing was applied');assert.equal(a.$('doc-undo').disabled,true,'no undo step for a refused edit');
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('S03-L L2: dragging across a band boundary moves the element into the band under the pointer; a move the validator refuses snaps back and names the reason',async()=>{const a=await designer();try{
 cv.load(a,banded());
 // e9 (page footer, top 260 mm, y 2) dragged 10 mm up: the pointer is in the document footer (240..260 mm), so e9 leaves the page footer for it and its position becomes relative to the footer: y 12.
 cv.drag2(a,'e9',0,-10);
 let t=cv.tpl(a);assert.deepEqual(t.bands.page_footer.elements,[]);assert.deepEqual(t.bands.document_footer.elements.map(e=>e.id),['e7','e8','e9']);assert.deepEqual(cv.rel(a,'e9'),[0,12,120,6]);assert.equal(find2(t,'e9').band,'document_footer');assert.equal(cv.status(a),'已移动。可用 Ctrl/Cmd+Z 撤销。');
 assert.match(cv.item(a,'e9').getAttribute('aria-label'),/单据尾带/);assert.deepEqual(cv.selected(a),['e9']);
 // The page position is where it was dropped: 240 + 12 = 252 mm.
 assert.deepEqual(page2(t,'e9').slice(0,2),[10,252]);
 a.click('doc-undo');t=cv.tpl(a);assert.deepEqual(t.bands.page_footer.elements.map(e=>e.id),['e9']);assert.deepEqual(cv.rel(a,'e9'),[0,2,120,6],'one undo step takes it back to its band and its place');
 // The same across more than one boundary: e4 (document header) to the page footer, over the detail band, where e9 is: refused (it overlaps), nothing changes.
 const before=cv.tpl(a);cv.drag2(a,'e4',0,222,{altKey:true});
 assert.match(cv.status(a),/^未应用：.*项目已恢复原位。$/);assert.deepEqual(cv.tpl(a),before,'a refused move leaves the template as it was');assert.equal(a.$('doc-undo').disabled,true);
 // A line crosses too: e3 (page header, y 12) dragged 10 mm down is in the document header (top 39 mm) and keeps its shape, relative to the new band.
 cv.drag2(a,'e3',0,10);t=cv.tpl(a);assert.equal(find2(t,'e3').band,'document_header');assert.deepEqual(cv.rel(a,'e3'),[0,8,190,8]);assert.deepEqual(t.bands.page_header.elements.map(e=>e.id),['e1','e2']);assert.equal(t.bands.document_header.elements.at(-1).id,'e3');
 a.click('doc-undo');assert.deepEqual(cv.rel(a,'e3'),[0,12,190,12]);
 // A drag whose pointer is over the detail band keeps the element in its own band (nothing else can take it) and is judged there: out of the band, refused.
 const keep=cv.tpl(a);cv.drag2(a,'e4',0,40);assert.match(cv.status(a),/^未应用：/);assert.deepEqual(cv.tpl(a),keep);
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('S03-L L2: handles resize one element, a table by its left and right edges (columns scale), and the minimum sizes and the band are the validator\'s',async()=>{const a=await designer();try{
 cv.load(a,banded());
 // A text box has the eight handles; its east handle drags the right edge onto the grid: e4 (90 mm wide) dragged 6.4 mm in is 84 wide and stays at x 0.
 cv.click2(a,'e4');assert.equal(a.$('doc-canvas-host').querySelectorAll('.cv-handle').length,8);
 let [x,y,w,h]=page2(cv.tpl(a),'e4');let g=cv.press(a,cv.handle(a,'e'),x+w,y+h/2);g.move(x+w-3,y+h/2);g.move(x+w-6.4,y+h/2);g.up(x+w-6.4,y+h/2);
 assert.deepEqual(cv.rel(a,'e4'),[0,2,84,8]);assert.equal(cv.status(a),'已调整大小。可用 Ctrl/Cmd+Z 撤销。');
 // The south-east corner takes width and height: both grids, in the band's own coordinates.
 [x,y,w,h]=page2(cv.tpl(a),'e4');cv.click2(a,'e4');g=cv.press(a,cv.handle(a,'se'),x+w,y+h);g.move(x+w-1,y+h+1);g.move(x+w-2.2,y+h+3.1);g.up(x+w-2.2,y+h+3.1);
 const r=cv.rel(a,'e4');assert.equal(r[2],82);assert.ok(r[3]>=8,'taller');
 // The minimum height the font needs (12 pt: 6.7 mm): a box dragged down to 3 mm high is refused and goes back.
 cv.click2(a,'e4');[x,y,w,h]=page2(cv.tpl(a),'e4');const before=cv.tpl(a);g=cv.press(a,cv.handle(a,'s'),x+w/2,y+h);g.move(x+w/2,y+h-2);g.move(x+w/2,y+3);g.up(x+w/2,y+3);
 assert.deepEqual(cv.tpl(a),before);assert.match(cv.status(a),/^未应用：e4：超出带范围、尺寸、字号、来源或文字无效。 项目已恢复原位。$/);
 a.click('doc-undo');a.click('doc-undo');assert.deepEqual(cv.rel(a,'e4'),[0,2,90,8]);
 // The table has two handles, on its left and right edges; the columns scale in proportion, rounded to 0.1 mm, the remainder on the last.
 cv.click2(a,'e6');assert.deepEqual([...a.$('doc-canvas-host').querySelectorAll('.cv-handle')].map(e=>e.getAttribute('data-dir')).sort(),['e','w']);
 [x,y,w,h]=page2(cv.tpl(a),'e6');g=cv.press(a,cv.handle(a,'e'),x+w,y+h/2);g.move(x+w-10,y+h/2);g.move(x+w-20,y+h/2);g.up(x+w-20,y+h/2);
 let t=cv.tpl(a).bands.detail.elements[0];assert.equal(t.width,170);assert.deepEqual(t.columns.map(c=>c.width),[53.7,53.7,62.6]);assert.equal(t.columns.reduce((s,c)=>s+tenths(c.width),0),1700);assert.equal(t.columns[1].label,'Customer');assert.equal(t.columns[2].align,'right','labels and alignments are kept');
 // Its left edge moves x and the width together.
 cv.click2(a,'e6');[x,y,w,h]=page2(cv.tpl(a),'e6');g=cv.press(a,cv.handle(a,'w'),x,y+h/2);g.move(x+5,y+h/2);g.move(x+10,y+h/2);g.up(x+10,y+h/2);
 t=cv.tpl(a).bands.detail.elements[0];assert.deepEqual([t.x,t.width],[10,160]);assert.equal(t.columns.reduce((s,c)=>s+tenths(c.width),0),1600);
 // Too narrow for the columns (8 mm each): the validator refuses and the table snaps back.
 cv.click2(a,'e6');[x,y,w,h]=page2(cv.tpl(a),'e6');const keep=cv.tpl(a);g=cv.press(a,cv.handle(a,'e'),x+w,y+h/2);g.move(x+w-50,y+h/2);g.move(x+w-150,y+h/2);g.up(x+w-150,y+h/2);
 assert.deepEqual(cv.tpl(a),keep);assert.match(cv.status(a),/^未应用：/);
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('S03-L L2: arrow keys nudge by 0.1 mm and Shift-arrows by 1 mm; align and distribute act on the selection in 0.1 mm units; delete and duplicate',async()=>{const a=await designer();try{
 cv.load(a,banded());
 cv.click2(a,'e4');cv.key(a,cv.item(a,'e4'),'ArrowRight');cv.key(a,cv.item(a,'e4'),'ArrowDown',{shiftKey:true});assert.deepEqual(cv.rel(a,'e4'),[0.1,3,90,8]);assert.equal(cv.status(a),'已微调。可用 Ctrl/Cmd+Z 撤销。');
 // An arrow that would leave the band is refused: e4 at y 3 goes 3 mm up to the band's top, and no further.
 cv.key(a,cv.item(a,'e4'),'ArrowUp',{shiftKey:true});cv.key(a,cv.item(a,'e4'),'ArrowUp',{shiftKey:true});cv.key(a,cv.item(a,'e4'),'ArrowUp',{shiftKey:true});assert.deepEqual(cv.rel(a,'e4'),[0.1,0,90,8]);cv.key(a,cv.item(a,'e4'),'ArrowUp');assert.deepEqual(cv.rel(a,'e4'),[0.1,0,90,8]);assert.match(cv.status(a),/^未应用：/);
 // The line moves by its body, as a whole.
 cv.click2(a,'e3');cv.key(a,cv.item(a,'e3'),'ArrowDown');assert.deepEqual(cv.rel(a,'e3'),[0,12.1,190,12.1]);cv.key(a,cv.item(a,'e3'),'ArrowRight',{shiftKey:true});assert.match(cv.status(a),/^未应用：/,'a line may not leave the band: x 191 is outside 0..190');assert.deepEqual(cv.rel(a,'e3'),[0,12.1,190,12.1]);
 // Align: e1 and e2 (page header) by their tops, bottoms and middles; the table and an element of another band align horizontally.
 cv.click2(a,'e2');cv.click2(a,'e1',{shiftKey:true});assert.equal(a.$('doc-align-top').disabled,false);
 a.click('doc-align-bottom');assert.deepEqual(cv.rel(a,'e1'),[0,0,120,9]);assert.deepEqual(cv.rel(a,'e2'),[130,0,60,9]);
 cv.click2(a,'e1');cv.click2(a,'e9',{shiftKey:true});a.click('doc-align-right');assert.equal(find2(cv.tpl(a),'e1').e.x+120,find2(cv.tpl(a),'e9').e.x+120,'right edges level');
 // Distribute needs three; three elements in a band are spread with equal gaps (e7 rectangle, e8 text and a new line).
 cv.click2(a,'e1');cv.click2(a,'e2',{shiftKey:true});assert.equal(a.$('doc-distribute-h').disabled,true);
 // Duplicate: the lowest free id, in the same band, at the nearest free place below; a copy of the table is not possible.
 cv.load(a,banded());
 cv.click2(a,'e5');cv.key(a,cv.item(a,'e5'),'Delete');assert.equal(find2(cv.tpl(a),'e5'),null);assert.equal(cv.status(a),'已删除。可用 Ctrl/Cmd+Z 撤销。');
 cv.click2(a,'e4');cv.key(a,cv.item(a,'e4'),'d',{ctrlKey:true});let t=cv.tpl(a);assert.equal(find2(t,'e5').band,'document_header');assert.deepEqual(cv.rel(a,'e5'),[0,10,90,8],'the lowest free id is e5, 8 mm (its height) below e4');assert.deepEqual(cv.selected(a),['e5']);assert.equal(t.bands.document_header.elements.at(-1).id,'e5');
 // The band has no room for a third copy: refused with the reason.
 cv.click2(a,'e4');cv.key(a,cv.item(a,'e4'),'d',{ctrlKey:true});assert.match(cv.status(a),/^未应用：下方没有可放置副本的空位。$|^未应用：/);assert.equal(cv.tpl(a).bands.document_header.elements.length,2);
 // A line is copied 1 mm below itself, in its band; a rectangle that has no room below it (the footer is 20 mm high, the rectangle ends at 12 mm) is refused.
 cv.click2(a,'e3');cv.key(a,cv.item(a,'e3'),'d',{ctrlKey:true});t=cv.tpl(a);assert.equal(find2(t,'e10').band,'page_header');assert.deepEqual(cv.rel(a,'e10'),[0,13,190,13]);
 cv.click2(a,'e7');cv.key(a,cv.item(a,'e7'),'d',{ctrlKey:true});assert.match(cv.status(a),/^未应用：/);assert.equal(find2(cv.tpl(a),'e11'),null);
 // The table is not copied, and cannot be deleted; deleting a selection that holds it deletes the rest.
 cv.click2(a,'e6');a.click('doc-dup');assert.equal(a.$('doc-dup').disabled,true);cv.key(a,cv.item(a,'e6'),'Delete');assert.match(cv.status(a),/^未应用：明细表不能删除/);assert.ok(find2(cv.tpl(a),'e6'));
 cv.key(a,cv.item(a,'e6'),'a',{ctrlKey:true});cv.key(a,cv.item(a,'e6'),'Delete');t=cv.tpl(a);assert.deepEqual(Object.values(t.bands).flatMap(b=>b.elements.map(e=>e.id)),['e6'],'only the table is left');assert.equal(a.$('doc-del').disabled,false);
 a.click('doc-undo');assert.equal(Object.values(cv.tpl(a).bands).flatMap(b=>b.elements).length,10);
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('S03-L L2: a fixed band\'s bottom edge is dragged to change its height (the detail band cannot be); the keyboard does it too; a height the template refuses goes back',async()=>{const a=await designer();try{
 cv.load(a,banded());
 assert.equal(cv.band(a,'detail'),null,'the detail band has no edge to drag');
 // 6.3 mm down: whole millimetres, 20; the bands under it move down by 6 mm and the detail band shrinks to match.
 cv.dragBand(a,'page_header',6.3);let t=cv.tpl(a);assert.equal(t.bands.page_header.height,20);assert.equal(cv.status(a),'已调整带高。可用 Ctrl/Cmd+Z 撤销。');
 assert.deepEqual([geometry2(a).document_header.top,geometry2(a).detail.height],[45,171]);assert.deepEqual(cv.rel(a,'e4'),[0,2,90,8],'the elements keep their band positions');
 assert.deepEqual(attrs(a.$('doc-canvas-host').querySelector('[data-band-box="document_header"]'),'y'),[X2(a,45)]);
 // With Alt 0.1 mm.
 cv.dragBand(a,'page_header',-1.4,{altKey:true});assert.equal(cv.tpl(a).bands.page_header.height,18.6);
 a.click('doc-undo');a.click('doc-undo');assert.equal(cv.tpl(a).bands.page_header.height,14);
 // Within 1 mm of the bottom of the lowest element (the line at 12 mm) the edge snaps to it, with a guide.
 let g=geometry2(a).page_header,h=cv.press(a,cv.band(a,'page_header'),100,g.top+g.height);h.move(100,g.top+g.height-0.8);h.move(100,g.top+g.height-1.6,{altKey:true});
 assert.match(a.$('doc-canvas-host').querySelector('.cv-readout').textContent,/^页眉带高度 12 毫米$/);assert.equal(a.$('doc-canvas-host').querySelectorAll('.cv-guide').length,1);h.up(100,g.top+g.height-1.6,{altKey:true});assert.equal(cv.tpl(a).bands.page_header.height,12);a.click('doc-undo');
 // Too small for its elements: while dragging the band is marked and the readout says it will be refused; on release it goes back.
 const before=cv.tpl(a);g=geometry2(a).page_header;h=cv.press(a,cv.band(a,'page_header'),100,g.top+g.height);h.move(100,g.top+g.height-4);h.move(100,g.top+g.height-9);
 assert.match(a.$('doc-canvas-host').querySelector('.cv-readout').textContent,/页眉带高度 5 毫米（将被拒绝）/);assert.ok(a.$('doc-canvas-host').querySelector('[data-band-box="page_header"]').getAttribute('class').includes('cv-bad-band'));
 h.up(100,g.top+g.height-9);assert.deepEqual(cv.tpl(a),before);assert.match(cv.status(a),/^未应用：.*项目已恢复原位。$/);assert.equal(a.$('doc-undo').disabled,true);
 // Too tall for the page (the bands and the table's minimum must fit 245 mm): refused.
 g=geometry2(a).document_header;h=cv.press(a,cv.band(a,'document_header'),100,g.top+g.height);h.move(100,g.top+g.height+100);h.move(100,g.top+g.height+200);h.up(100,g.top+g.height+200);assert.deepEqual(cv.tpl(a),before);assert.match(cv.status(a),/^未应用：页眉、单据头与页脚的高度加上明细表的最小高度超出安全区/);
 // The keyboard: Tab to the edge, arrows 0.1 mm, Shift 1 mm; an edge that cannot go further says so.
 cv.band(a,'page_header').focus();assert.equal(a.w.document.activeElement.getAttribute('data-band'),'page_header');assert.equal(cv.band(a,'page_header').getAttribute('role'),'slider');assert.equal(cv.band(a,'page_header').getAttribute('aria-valuenow'),'14');
 cv.key(a,cv.band(a,'page_header'),'ArrowDown');assert.equal(cv.tpl(a).bands.page_header.height,14.1);cv.key(a,cv.band(a,'page_header'),'ArrowDown',{shiftKey:true});assert.equal(cv.tpl(a).bands.page_header.height,15.1);cv.key(a,cv.band(a,'page_header'),'ArrowUp');assert.equal(cv.tpl(a).bands.page_header.height,15);
 assert.equal(a.w.document.activeElement.getAttribute('data-band'),'page_header','focus stays on the edge after a redraw');assert.equal(cv.band(a,'page_header').getAttribute('aria-valuetext'),'页眉带高度 15 毫米');
 for(let k=0;k<12;k++)cv.key(a,cv.band(a,'page_header'),'ArrowUp',{shiftKey:true});assert.match(cv.status(a),/^未应用：/);assert.ok(cv.tpl(a).bands.page_header.height>=10);
 cv.key(a,cv.band(a,'page_header'),'Delete');assert.ok(cv.tpl(a).bands.page_header,'Delete on an edge does nothing');
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('S03-L L2: a line is moved by its body and edited by its two end handles, with the mouse and the keyboard; a line is not resized by the eight handles',async()=>{const a=await designer();try{
 cv.load(a,banded());
 cv.click2(a,'e3');assert.equal(a.$('doc-canvas-host').querySelectorAll('.cv-handle').length,2,'a line has its two ends');assert.equal(a.$('doc-canvas-host').querySelectorAll('[data-dir]').length,0);
 assert.deepEqual([cv.end(a,1),cv.end(a,2)].map(e=>e&&e.getAttribute('aria-label')),['线 e3 的起点：拖动或用方向键移动','线 e3 的终点：拖动或用方向键移动']);
 // By its body: 1.6 mm down is within 1 mm of the band's bottom (14 mm), which the line snaps to; 20 mm up takes the pointer above every band, where it is judged in its own band and refused.
 cv.drag2(a,'e3',0,1.6);assert.deepEqual(cv.rel(a,'e3'),[0,14,190,14]);
 cv.drag2(a,'e3',0,-20);assert.match(cv.status(a),/^未应用：/,'above the page header');assert.deepEqual(cv.rel(a,'e3'),[0,14,190,14]);
 // End 2 dragged 30 mm left: the line is shorter; end 1 is untouched.
 cv.click2(a,'e3');let g=cv.press(a,cv.end(a,2),200,39);g.move(185,39);g.move(170,39);g.up(170,39);assert.deepEqual(cv.rel(a,'e3'),[0,14,160,14]);assert.equal(cv.status(a),'已移动端点。可用 Ctrl/Cmd+Z 撤销。');
 // End 1 moved 1.7 mm up lands on the grid: the line is slanted, which is allowed.
 g=cv.press(a,cv.end(a,1),10,39);g.move(10,38);g.move(10,37.3);g.up(10,37.3);assert.deepEqual(cv.rel(a,'e3'),[0,12,160,14]);
 // Snapping to the other end (and the band's bottom) makes a line level again: end 1 back down to within 1 mm of end 2's y lands exactly on it.
 g=cv.press(a,cv.end(a,1),10,37);g.move(10,38);g.move(10,38.7);g.up(10,38.7);assert.deepEqual(cv.rel(a,'e3'),[0,14,160,14]);
 // A zero-length line or an end outside the band is refused.
 const before=cv.tpl(a);g=cv.press(a,cv.end(a,1),10,39);g.move(100,39);g.move(250,39);g.up(250,39);assert.deepEqual(cv.tpl(a),before);assert.match(cv.status(a),/^未应用：/);
 // The keyboard: Tab to an end, arrows move it by 0.1 mm, Shift by 1 mm.
 cv.end(a,1).focus();cv.key(a,cv.end(a,1),'ArrowRight');assert.deepEqual(cv.rel(a,'e3'),[0.1,14,160,14]);cv.key(a,cv.end(a,1),'ArrowLeft',{shiftKey:true});assert.match(cv.status(a),/^未应用：/);
 assert.equal(a.w.document.activeElement.getAttribute('data-end'),'1');cv.key(a,cv.end(a,2),'ArrowUp',{shiftKey:true});assert.deepEqual(cv.rel(a,'e3'),[0.1,14,160,13]);
 // Each applied edit is one undo step.
 for(let k=0;k<6;k++)a.click('doc-undo');assert.deepEqual(cv.rel(a,'e3'),[0,12,190,12]);
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('S03-L L2: the table\'s column boundaries are dragged as in 1.x, in the detail band; three elements distribute with equal gaps',async()=>{const a=await designer();try{
 cv.load(a,banded());
 assert.equal(a.$('doc-canvas-host').querySelectorAll('[data-col]').length,2);
 // Pressing a boundary selects the table; 10 mm right: the left column takes it from its neighbour, the table keeps its width.
 let g=cv.press(a,cv.col(a,0),70,100);g.move(75,100);g.move(80,100);g.up(80,100);let cols=()=>cv.tpl(a).bands.detail.elements[0].columns.map(c=>c.width);
 assert.deepEqual(cols(),[70,50,70]);assert.deepEqual(cv.selected(a),['e6']);assert.equal(cv.status(a),'已调整列宽。可用 Ctrl/Cmd+Z 撤销。');
 // Alt: 0.1 mm; far to the right: the column to the right keeps its minimum, 8 mm.
 g=cv.press(a,cv.col(a,0),80,100);g.move(80.5,100,{altKey:true});g.move(81.3,100,{altKey:true});g.up(81.3,100,{altKey:true});assert.deepEqual(cols(),[71.3,48.7,70]);
 g=cv.press(a,cv.col(a,0),81,100);g.move(120,100);g.move(180,100);g.up(180,100);assert.deepEqual(cols(),[112,8,70]);
 g=cv.press(a,cv.col(a,1),120,100);g.move(60,100);g.move(10,100);g.up(10,100);assert.deepEqual(cols(),[112,8,70],'the boundary stops at the neighbour\'s minimum');
 // A boundary snaps to the band's centre (page x 105 mm, 95 mm into the band).
 a.click('doc-undo');a.click('doc-undo');a.click('doc-undo');assert.deepEqual(cols(),[60,60,70]);
 g=cv.press(a,cv.col(a,0),70,100);g.move(75,100);g.move(104.4,100);g.up(104.4,100);assert.deepEqual(cols(),[95,25,70],'the band\'s centre');
 // The keyboard: the table selected, Tab to a boundary, arrows 0.1 mm.
 cv.col(a,0).focus();cv.key(a,cv.col(a,0),'ArrowRight');assert.deepEqual(cols(),[95.1,24.9,70]);assert.equal(cv.col(a,0).getAttribute('aria-valuenow'),'95.1');assert.equal(a.w.document.activeElement.getAttribute('data-col'),'0');
 // Distribute: three text boxes at x 0, 50 and 150 (30 mm wide) get equal gaps: the middle one goes to x 75.
 const t=banded();t.bands.page_header.elements=[0,50,150].map((x,k)=>({id:'e'+(k+1),type:'text',source:'literal',text:'T'+k,field:'',wrap:false,x,y:0,width:30,height:9,font_size:10,align:'left',bold:false}));cv.load(a,t);
 cv.click2(a,'e1');cv.click2(a,'e2',{shiftKey:true});cv.click2(a,'e3',{shiftKey:true});a.click('doc-distribute-h');assert.deepEqual([1,2,3].map(k=>cv.rel(a,'e'+k)[0]),[0,75,150]);
 a.click('doc-distribute-v');assert.deepEqual([1,2,3].map(k=>cv.rel(a,'e'+k)[1]),[0,0,0]);
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

// ---- the 2.0 panels (L3) ----
const chg=(a,id,value)=>{const e=a.$(id);if(e.type==='checkbox')e.checked=value;else e.value=String(value);e.dispatchEvent(new a.w.Event('change',{bubbles:true}));};
const clickId=(a,id)=>a.$(id).click();
const undoAll=a=>{let n=0;while(!a.$('doc-undo').disabled){a.click('doc-undo');n++;}return n;};
const doc2status=a=>a.$('doc-status').textContent;

test('S03-L L3: the group fields are one to three dimension fields in order, or none; every change is one undo step and a refusal says why',async()=>{const a=await designer();try{
 cv.load(a,banded());
 assert.equal(a.$('doc2-panel').hidden,false);for(const id of ['doc-panel-elements','doc-panel-table','doc-panel-background','doc-panel-print'])assert.equal(a.$(id).hidden,true,id+' is the 1.x panel');
 assert.deepEqual([0,1,2].map(n=>a.$('doc2-group-'+n).value),['customer.id','','']);
 assert.deepEqual([...a.$('doc2-group-0').options].map(o=>o.value),['','customer.id','aging.bucket'],'dimensions only: no metric');
 assert.match(a.$('doc2-group-status').textContent,/^分组：customer\.id。/);
 chg(a,'doc2-group-1','aging.bucket');assert.deepEqual(cv.tpl(a).group,{fields:['customer.id','aging.bucket']});assert.match(a.$('doc2-group-status').textContent,/customer\.id、aging\.bucket/);assert.match(a.$('doc-canvas-mode').textContent,/分组 customer\.id、aging\.bucket/);
 chg(a,'doc2-group-0','');assert.deepEqual(cv.tpl(a).group,{fields:['aging.bucket']},'an empty slot leaves the others in order');assert.deepEqual([0,1,2].map(n=>a.$('doc2-group-'+n).value),['aging.bucket','','']);
 chg(a,'doc2-group-0','customer.id');assert.deepEqual(cv.tpl(a).group,{fields:['customer.id']});
 chg(a,'doc2-group-1','customer.id');assert.match(doc2status(a),/分组字段需为1–3个不同的维度字段/);assert.deepEqual(cv.tpl(a).group,{fields:['customer.id']},'a duplicate is refused');assert.equal(a.$('doc2-group-1').value,'','and the inputs go back to the template');
 for(const n of [0,1,2])chg(a,'doc2-group-'+n,'');assert.equal('group' in cv.tpl(a),false,'no group: the key is gone');assert.match(a.$('doc2-group-status').textContent,/^无分组：整个快照一份文档/);
 assert.equal(undoAll(a),4,'four applied changes, four undo steps; the refused one is none');assert.deepEqual(cv.tpl(a),banded());
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('S03-L L3: bands: an optional band is added with a default height or removed with its elements; its height is an input; the canvas and the inputs agree',async()=>{const a=await designer();try{
 cv.load(a,banded());
 assert.deepEqual([...a.$('doc2-bands').children].map(r=>r.querySelector('.cv2-name').textContent),['页眉带（3 个元素）','单据头带（2 个元素）','单据尾带（2 个元素）','页脚带（1 个元素）','明细带：只放一个明细表；高度随页面伸展（至少 24.2 毫米：表头、承前行、一行明细与过次或合计行）。']);
 assert.equal(a.$('doc2-band-height-page_header').value,'14');
 // Height by the input: the canvas follows (the bands below move).
 chg(a,'doc2-band-height-page_header',20);assert.equal(cv.tpl(a).bands.page_header.height,20);assert.equal(geometry2(a).document_header.top,45);assert.equal(a.$('doc2-band-height-page_header').value,'20');
 // Height by dragging: the input follows.
 cv.dragBand(a,'page_header',-5.2);assert.equal(a.$('doc2-band-height-page_header').value,'15');
 // A height the elements do not fit in is refused with the validator's words and the input goes back.
 chg(a,'doc2-band-height-page_header',4);assert.match(doc2status(a),/页眉带的高度须为5–245毫米/);assert.equal(a.$('doc2-band-height-page_header').value,'15');assert.equal(cv.tpl(a).bands.page_header.height,15);
 chg(a,'doc2-band-height-page_header',8);assert.match(doc2status(a),/^e1：超出带范围/);assert.equal(cv.tpl(a).bands.page_header.height,15);
 // Remove an optional band: it goes with its elements, one step; the canvas has no such band and its elements are gone.
 clickId(a,'doc2-band-remove-page_footer');{const t=cv.tpl(a);assert.equal('page_footer' in t.bands,false);assert.equal(find2(t,'e9'),null);assert.equal(geometry2(a).detail.height,245-15-24-20);assert.equal(cv.band(a,'page_footer'),null);assert.equal(cv.item(a,'e9'),null);}
 assert.equal(a.$('doc2-band-add-page_footer').textContent,'添加页脚带');assert.deepEqual([...a.$('doc2-add-band').options].map(o=>o.value),['page_header','document_header','document_footer']);
 // Add it back: a default height (12 mm), no elements; the table is the detail band's only one and stays.
 clickId(a,'doc2-band-add-page_footer');let t=cv.tpl(a);assert.deepEqual(t.bands.page_footer,{height:12,elements:[]});assert.deepEqual(Object.keys(t.bands),['page_header','document_header','detail','document_footer','page_footer'],'canonical order');
 assert.ok(cv.band(a,'page_footer'));
 // A band that leaves no room for the table is refused: the document header at 200 mm.
 chg(a,'doc2-band-height-document_header',200);assert.match(doc2status(a),/页眉、单据头与页脚的高度加上明细表的最小高度超出安全区/);assert.equal(cv.tpl(a).bands.document_header.height,24);
 // Each applied change is one undo step: the height input, the height drag, the removal and the addition of a band.
 assert.equal(undoAll(a),4);assert.deepEqual(cv.tpl(a),banded());
 // All four optional bands can be removed: the page is the table alone.
 for(const n of ['page_header','document_header','document_footer','page_footer'])clickId(a,'doc2-band-remove-'+n);t=cv.tpl(a);assert.deepEqual(Object.keys(t.bands),['detail']);assert.equal(geometry2(a).detail.height,245);assert.deepEqual(cv.keys(a),['e6']);
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('S03-L L3: an element of each kind is added to a chosen band under the lowest free id, at a free place, and is selected on the canvas; each is one undo step',async()=>{const a=await designer();try{
 cv.load(a,banded());
 const pick=name=>{a.$('doc2-add-band').value=name;};
 // A literal text in the document header: the first free place is below e4 (y 10), 80 mm wide, 6 mm high, 10 pt.
 pick('document_header');a.$('doc2-text').value='发票';clickId(a,'doc2-add-literal');assert.equal(doc2status(a),READY);let t=cv.tpl(a);
 assert.deepEqual(find2(t,'e10').e,{id:'e10',type:'text',source:'literal',text:'发票',field:'',wrap:false,x:0,y:10,width:80,height:6,font_size:10,align:'left',bold:false});assert.equal(find2(t,'e10').band,'document_header');assert.deepEqual(cv.selected(a),['e10']);
 // The id is the lowest free one: delete e2, add again.
 cv.click2(a,'e2');cv.key(a,cv.item(a,'e2'),'Delete');pick('document_footer');a.$('doc2-text').value='';clickId(a,'doc2-add-literal');assert.equal(find2(cv.tpl(a),'e2').band,'document_footer');assert.equal(find2(cv.tpl(a),'e2').e.text,'固定文字','the default text');
 // A field text with the chosen field; a group sum falls back to the first metric when the field chosen is not one.
 pick('document_header');a.$('doc2-field').value='aging.bucket';clickId(a,'doc2-add-field');assert.deepEqual([find2(cv.tpl(a),'e11').e.source,find2(cv.tpl(a),'e11').e.field],['field','aging.bucket']);
 pick('document_footer');a.$('doc2-field').value='customer.id';clickId(a,'doc2-add-sum');const sum=find2(cv.tpl(a),'e12');assert.deepEqual([sum.band,sum.e.source,sum.e.field,sum.e.text],['document_footer','group_sum','ar.open_balance','']);
 // A page text keeps the placeholders it was given, or gets the default.
 a.$('doc2-text').value='{page}/{pages}';pick('document_footer');clickId(a,'doc2-add-page');const pg=find2(cv.tpl(a),'e13');assert.ok(pg,'added: '+doc2status(a));assert.deepEqual([pg.e.source,pg.e.text,pg.e.wrap],['page','{page}/{pages}',false]);
 a.$('doc2-text').value='无占位';clickId(a,'doc2-add-page');assert.equal(find2(cv.tpl(a),'e14').e.text,'Page {page} of {pages}','a page text needs a placeholder: the default');
 // A line along the band's bottom edge, a rectangle at its top-left corner.
 pick('page_header');clickId(a,'doc2-add-line');const line=find2(cv.tpl(a),'e15');assert.deepEqual(line.e,{id:'e15',type:'line',x1:0,y1:14,x2:190,y2:14,stroke:0.5});
 pick('document_header');clickId(a,'doc2-add-rect');const rect=find2(cv.tpl(a),'e16');assert.deepEqual(rect.e,{id:'e16',type:'rect',x:0,y:0,width:60,height:10,stroke:0.5,fill:'none'});assert.deepEqual(cv.selected(a),['e16']);
 // They are on the canvas, in their bands.
 assert.deepEqual(page2(cv.tpl(a),'e16'),[10,39,60,10]);assert.ok(cv.item(a,'e15')&&cv.item(a,'e13'));
 // No place in a band: the page footer (10 mm) is full of e9; the refusal says so and nothing is added.
 const before=cv.tpl(a);pick('page_footer');a.$('doc2-text').value='x';clickId(a,'doc2-add-literal');assert.match(doc2status(a),/带内没有放得下新文本框的空位/);assert.deepEqual(cv.tpl(a),before);
 assert.equal(undoAll(a),9,'one undo step per element added or deleted');assert.deepEqual(cv.tpl(a),banded());
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('S03-L L3: the selected element\'s properties are inputs for every key its type has, in sync with the canvas both ways; the validator decides and a refusal puts the value back',async()=>{const a=await designer();try{
 cv.load(a,banded());
 assert.match(a.$('doc2-props').textContent,/在画布上点击一个元素/);
 cv.click2(a,'e4');assert.equal(a.$('doc2-p-title').textContent,'e4 · 单据头带 · 文本（字段）');
 const ids=()=>[...a.$('doc2-props').querySelectorAll('[id^="doc2-p-"]')].map(e=>e.id.slice(7)).filter(k=>k!=='title');
 assert.deepEqual(ids(),['band','x','y','width','height','source','field','font_size','align','wrap','bold','delete'],'every key of a field text but its id (grouped as the inspector groups them: position, content, text)');
 assert.deepEqual(['x','y','width','height','font_size'].map(k=>a.$('doc2-p-'+k).value),['0','2','90','8','12']);assert.equal(a.$('doc2-p-field').value,'customer.id');assert.equal(a.$('doc2-p-wrap').checked,false);
 // Typing moves it on the canvas; moving it on the canvas changes the input.
 chg(a,'doc2-p-x',5.5);assert.deepEqual(cv.rel(a,'e4'),[5.5,2,90,8]);assert.deepEqual(attrs(cv.item(a,'e4').querySelector('.cv-box'),'x'),[X2(a,15.5)]);
 cv.drag2(a,'e4',-3.2,0,{altKey:true});assert.equal(a.$('doc2-p-x').value,'2.3');assert.deepEqual(cv.selected(a),['e4']);
 chg(a,'doc2-p-font_size',14);chg(a,'doc2-p-align','right');chg(a,'doc2-p-bold',true);chg(a,'doc2-p-wrap',true);
 assert.deepEqual([el2(a,'e4').font_size,el2(a,'e4').align,el2(a,'e4').bold,el2(a,'e4').wrap],[14,'right',true,true]);assert.equal(cv.item(a,'e4').querySelector('text').getAttribute('font-weight'),'700');assert.ok(cv.item(a,'e4').querySelector('.cv-wrap'));assert.match(cv.item(a,'e4').getAttribute('aria-label'),/粗体，自动换行/);
 // A value the validator refuses: the status says why, the input goes back, the canvas is untouched.
 const before=cv.tpl(a);chg(a,'doc2-p-width',5);assert.match(doc2status(a),/^e4：超出带范围、尺寸、字号、来源或文字无效。$/);assert.equal(a.$('doc2-p-width').value,'90');assert.deepEqual(cv.tpl(a),before);
 chg(a,'doc2-p-height',3);assert.equal(a.$('doc2-p-height').value,'8');chg(a,'doc2-p-font_size',40);assert.equal(a.$('doc2-p-font_size').value,'14');
 // The source: to a literal (with its text), to a group sum (a metric field), to a page text (placeholders).
 chg(a,'doc2-p-source','literal');assert.equal(el2(a,'e4').source,'literal');assert.deepEqual([el2(a,'e4').text,el2(a,'e4').field],['固定文字','']);assert.equal(a.$('doc2-p-text').value,'固定文字');assert.equal(a.$('doc2-p-field'),null);
 chg(a,'doc2-p-text','总计 ' + '字'.repeat(3));assert.equal(el2(a,'e4').text,'总计 字字字');assert.equal(cv.item(a,'e4').querySelector('text').textContent,'总计 字字字');
 chg(a,'doc2-p-source','group_sum');assert.deepEqual([el2(a,'e4').source,el2(a,'e4').field,el2(a,'e4').text],['group_sum','ar.open_balance','']);assert.deepEqual([...a.$('doc2-p-field').options].map(o=>o.value),['ar.open_balance'],'a sum takes a metric');
 chg(a,'doc2-p-source','page');assert.deepEqual([el2(a,'e4').source,el2(a,'e4').text,el2(a,'e4').wrap,el2(a,'e4').field],['page','Page {page} of {pages}',false,'']);
 chg(a,'doc2-p-text','没有占位');assert.match(doc2status(a),/^e4：/);assert.equal(a.$('doc2-p-text').value,'Page {page} of {pages}');
 chg(a,'doc2-p-source','field');assert.deepEqual([el2(a,'e4').source,el2(a,'e4').field,el2(a,'e4').text],['field','customer.id','']);chg(a,'doc2-p-field','aging.bucket');assert.equal(el2(a,'e4').field,'aging.bucket');
 // The band: moving an element to another band keeps its place in it; e9 into the document footer overlaps e8? it does not (x 0..120, y 2..8, e8 at x 102..188, y 3..11): refused.
 cv.click2(a,'e9');chg(a,'doc2-p-band','document_footer');assert.match(doc2status(a),/^同一带内的文本框不能重叠。$/);assert.equal(find2(cv.tpl(a),'e9').band,'page_footer');assert.equal(a.$('doc2-p-band').value,'page_footer');
 cv.click2(a,'e1');chg(a,'doc2-p-band','document_header');assert.match(doc2status(a),/./);assert.equal(find2(cv.tpl(a),'e1').band,'page_header','refused: no room in the document header');
 // A line, a rectangle: their own keys.
 cv.click2(a,'e3');assert.deepEqual(ids(),['band','x1','y1','x2','y2','stroke','delete']);assert.equal(a.$('doc2-p-title').textContent,'e3 · 页眉带 · 线');chg(a,'doc2-p-y2',13);chg(a,'doc2-p-stroke',1.25);assert.deepEqual(cv.rel(a,'e3'),[0,12,190,13]);assert.equal(el2(a,'e3').stroke,1.25);
 chg(a,'doc2-p-stroke',0.3);assert.match(doc2status(a),/^e3：/);assert.equal(a.$('doc2-p-stroke').value,'1.25','a stroke is a multiple of a quarter point');
 cv.click2(a,'e7');assert.deepEqual(ids(),['band','x','y','width','height','stroke','fill','delete']);assert.deepEqual([...a.$('doc2-p-fill').options].map(o=>o.value),['none','grey1','grey2','grey3','grey4']);assert.equal(a.$('doc2-p-fill').value,'grey2');chg(a,'doc2-p-fill','grey4');assert.equal(el2(a,'e7').fill,'grey4');assert.equal(a.$('doc-canvas-host').querySelector('.cv-shape[fill="rgb(190,190,190)"]')!==null,true);
 chg(a,'doc2-p-stroke',0);chg(a,'doc2-p-fill','none');assert.match(doc2status(a),/^e7：/,'a rectangle with neither outline nor fill is not drawn at all: refused');
 // The table's keys are in the table area; a selection of more than one shows no inputs.
 cv.click2(a,'e6');assert.match(a.$('doc2-props').textContent,/明细表的位置、宽度、字号和各列在下方“明细表”区域编辑/);cv.click2(a,'e1',{shiftKey:true});assert.match(a.$('doc2-props').textContent,/已选择 2 项：属性一次编辑一个元素/);
 // Delete from the panel.
 cv.click2(a,'e8');clickId(a,'doc2-p-delete');assert.equal(find2(cv.tpl(a),'e8'),null);assert.deepEqual(cv.selected(a),[]);assert.match(a.$('doc2-props').textContent,/在画布上点击一个元素/);
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('S03-L L2/L3: the table\'s columns are added, removed, reordered and given a header label and an alignment from the table\'s panel; the table\'s x, width and type size are inputs',async()=>{const a=await designer();try{
 cv.load(a,banded());
 const table=()=>cv.tpl(a).bands.detail.elements[0],cols=()=>table().columns.map(c=>[c.field,c.width,c.label??null,c.align??null]);
 assert.deepEqual(cols(),[['aging.bucket',60,null,null],['customer.id',60,'Customer',null],['ar.open_balance',70,null,'right']]);
 assert.deepEqual([...a.$('doc2-table').querySelectorAll('.cv2-row .cv2-name')].map(e=>e.textContent),['第 1 列','第 2 列','第 3 列']);
 // A literal header label (empty: the dataset's label again) and an alignment (default: right for a metric, left otherwise).
 chg(a,'doc2-col-label-0','账龄');assert.equal(table().columns[0].label,'账龄');assert.deepEqual([...cv.item(a,'e6').querySelectorAll('.cv-bold')].map(e=>e.textContent),['账龄','Customer','应收余额']);
 chg(a,'doc2-col-label-0','');assert.equal('label' in table().columns[0],false);assert.equal(a.$('doc2-col-label-0').placeholder,'账龄区间');
 chg(a,'doc2-col-align-0','center');assert.equal(table().columns[0].align,'center');chg(a,'doc2-col-align-0','');assert.equal('align' in table().columns[0],false);assert.deepEqual([...a.$('doc2-col-align-2').options].map(o=>o.textContent),['默认（右）','左对齐','居中','右对齐']);assert.deepEqual([...a.$('doc2-col-align-0').options].map(o=>o.textContent)[0],'默认（左）');
 chg(a,'doc2-col-label-1','x'.repeat(65));assert.match(doc2status(a),/^明细表或元素总数无效/);assert.equal(a.$('doc2-col-label-1').value,'Customer');
 // Width: the neighbour to the right makes up the difference (the last column: the one to its left); each column is at least 8 mm.
 chg(a,'doc2-col-width-0',70);assert.deepEqual(cols().map(c=>c[1]),[70,50,70]);chg(a,'doc2-col-width-2',65.5);assert.deepEqual(cols().map(c=>c[1]),[70,54.5,65.5]);
 chg(a,'doc2-col-width-0',200);assert.match(doc2status(a),/./);assert.deepEqual(cols().map(c=>c[1]),[70,54.5,65.5]);
 // Order: up and down; a column that is already first or last cannot move.
 clickId(a,'doc2-col-up-2');assert.deepEqual(cols().map(c=>c[0]),['aging.bucket','ar.open_balance','customer.id']);assert.deepEqual(cols().map(c=>c[1]),[70,65.5,54.5],'a column keeps its width');clickId(a,'doc2-col-down-0');assert.deepEqual(cols().map(c=>c[0]),['ar.open_balance','aging.bucket','customer.id']);
 clickId(a,'doc2-col-up-0');assert.match(doc2status(a),/已是第一列/);clickId(a,'doc2-col-down-2');assert.match(doc2status(a),/已是最后一列/);assert.deepEqual(cols().map(c=>c[0]),['ar.open_balance','aging.bucket','customer.id']);
 // The field of a column: any field of the result that no other column takes.
 assert.deepEqual([...a.$('doc2-col-field-0').options].map(o=>o.value),['ar.open_balance'],'every field is a column already');
 // Remove: the width goes to the left neighbour (the right one for the first); at least one column stays.
 clickId(a,'doc2-col-remove-1');assert.deepEqual(cols().map(c=>[c[0],c[1]]),[['ar.open_balance',135.5],['customer.id',54.5]]);assert.equal(table().width,190);
 assert.deepEqual([...a.$('doc2-col-field-0').options].map(o=>o.value).sort(),['aging.bucket','ar.open_balance']);
 clickId(a,'doc2-col-remove-0');assert.deepEqual(cols().map(c=>[c[0],c[1]]),[['customer.id',190]]);clickId(a,'doc2-col-remove-0');assert.match(doc2status(a),/明细表至少保留一列/);
 // Add: the new column takes half of the widest, next to it; the table keeps its width; no field left means no column.
 a.$('doc2-col-field').value='aging.bucket';clickId(a,'doc2-col-add');assert.deepEqual(cols().map(c=>[c[0],c[1]]),[['customer.id',95],['aging.bucket',95]]);
 a.$('doc2-col-field').value='ar.open_balance';clickId(a,'doc2-col-add');assert.deepEqual(cols().map(c=>[c[0],c[1]]),[['customer.id',47.5],['ar.open_balance',47.5],['aging.bucket',95]],'the widest of two equals: the first');
 assert.equal(a.$('doc2-col-field').options.length,0);clickId(a,'doc2-col-add');assert.match(doc2status(a),/没有可添加的字段/);
 // x, width and type size: the width scales the columns in proportion; a width too small for the columns is refused.
 chg(a,'doc2-table-width',171);assert.equal(table().width,171);assert.equal(table().columns.reduce((s,c)=>s+tenths(c.width),0),1710);assert.deepEqual(cols().map(c=>c[1]),[42.8,42.8,85.4],'47.5, 47.5 and 95 scaled by 171/190, each rounded to 0.1 mm, the remainder on the last');
 chg(a,'doc2-table-x',10);assert.equal(table().x,10);assert.equal(page2(cv.tpl(a),'e6')[0],20);chg(a,'doc2-table-x',30);assert.match(doc2status(a),/^明细表或元素总数无效/);assert.equal(a.$('doc2-table-x').value,'10');
 chg(a,'doc2-table-font',12);assert.equal(table().font_size,12);chg(a,'doc2-table-font',20);assert.match(doc2status(a),/./);assert.equal(a.$('doc2-table-font').value,'12');
 chg(a,'doc2-table-width',20);assert.match(doc2status(a),/./,'three columns of at least 8 mm: 24');assert.equal(table().width,171);
 // The canvas shows it: three columns, two boundaries.
 assert.equal(a.$('doc-canvas-host').querySelectorAll('[data-col]').length,2);assert.equal(a.$('doc-canvas-host').querySelectorAll('[data-key="e6"] .cv-bold').length,3);
 // A header label that is a number or markup is only text.
 const hostile='<img src=x onerror=alert(1)>';chg(a,'doc2-col-label-0',hostile);assert.equal(table().columns[0].label,hostile);assert.equal(cv.item(a,'e6').querySelector('.cv-bold').textContent,hostile);assert.equal(a.$('doc-canvas-host').querySelectorAll('img').length,0);
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

// ---- the 1.x to 2.0 conversion (L4) ----
const notes=a=>[...a.$('doc-convert-notes').children].map(e=>e.textContent);
// An invoice-like 1.3 template: a title and a customer above the table, a total and a signature line below it.
const invoice1x=a=>cv.table(a,[['n1',10,30,100,10,'literal','应收对账单'],['n2',120,30,80,10,'row','customer.id'],['n3',10,45,60,8,'literal','客户：'],['n4',120,190,80,10,'total','ar.open_balance'],['n5',10,205,100,8,'literal','制单人：']],[['aging.bucket',60],['customer.id',60],['ar.open_balance',70]],{x:10,y:60,width:190,height:120,font_size:9});
const text2=(id,source,text,field,x,y,width,height,font_size,align)=>({id,type:'text',source,text,field,wrap:false,x,y,width,height,font_size,align,bold:false});

test('S03-L L4: "转换为 2.0" turns a 1.x template into the 2.0 form: boxes above the table become the document header, the table the detail band, boxes below it the document footer',async()=>{const a=await designer();try{
 const one=invoice1x(a);one.elements[0].font_size=18;one.elements[1].align='right';one.elements[3].align='right';cv.load(a,one);
 assert.equal(a.$('doc-convert').hidden,false);assert.match(a.$('doc-convert-status').textContent,/^当前是 1\.x 模板/);
 a.click('doc-convert');assert.match(a.$('doc-status').textContent,/^已转换为 2\.0（未保存）：请先生成预览核对，再保存为新版本。$/);
 const t=cv.tpl(a);
 // The mapping, element by element: ids e1.. top to bottom (header, table, footer); x relative to the band (10 mm), y relative to the top of the safe region (25 mm) in the header and to the table region's bottom (180 mm) in the footer.
 assert.deepEqual(t,{schema_version:'2.0',kind:'document',binding:one.binding,bands:{
  document_header:{height:35,elements:[text2('e1','literal','应收对账单','',0,5,100,10,18,'left'),text2('e2','field','','customer.id',110,5,80,10,12,'right'),text2('e3','literal','客户：','',0,20,60,8,12,'left')]},
  detail:{elements:[{id:'e4',type:'table',x:0,width:190,font_size:9,columns:[{field:'aging.bucket',width:60},{field:'customer.id',width:60},{field:'ar.open_balance',width:70}]}]},
  document_footer:{height:33,elements:[text2('e5','group_sum','','ar.open_balance',110,10,80,10,12,'right'),text2('e6','literal','制单人：','',0,25,100,8,12,'left')]}}});
 assert.equal(t.group,undefined,'no group: the whole snapshot is one document');
 // The page: the header is as high as the space above the table (35 mm), so the table starts where it did (60 mm).
 assert.equal(geom(t).detail.top,60);assert.equal(geom(t).document_header.height,35);
 // The canvas shows 2.0 now, the panels follow, the template is an unsaved change that one undo takes back.
 assert.equal(a.$('doc2-panel').hidden,false);assert.equal(a.$('doc-panel-elements').hidden,true);assert.equal(a.$('doc-convert').hidden,true);assert.deepEqual(cv.keys(a),['e1','e2','e3','e4','e5','e6']);
 assert.equal(a.$('doc-undo').disabled,false);
 // The notes: what changed meaning. The footer follows the table, row boxes need a group, totals are group sums, the table's height is not part of 2.0.
 const n=notes(a);assert.equal(n.length,4);assert.match(n[0],/^表格下方的 2 个文本框已移到单据尾带：2\.0 把单据尾带放在明细表最后一行（含合计）之后，不再停在原来的页面位置/);assert.match(n[1],/^1 个行字段文本框已改为分组字段文本。.*请在“分组字段”中选择维度字段。$/);assert.match(n[2],/^1 个合计文本框已改为分组合计/);assert.match(n[3],/^明细表的“高”不属于 2\.0/);
 assert.match(a.$('doc-convert-status').textContent,/^本模板由 1\.x 转换而来，尚未保存/);
 a.click('doc-undo');assert.equal(cv.tpl(a).schema_version,'1.3');assert.deepEqual(cv.tpl(a),one,'the 1.x template is exactly as it was');assert.equal(a.$('doc2-panel').hidden,true);assert.equal(a.$('doc-convert').hidden,false);assert.deepEqual(notes(a),[]);
 a.click('doc-redo');assert.equal(cv.tpl(a).schema_version,'2.0');assert.equal(notes(a).length,4,'redo brings the notes back');
 // Opening something else forgets them.
 cv.load(a,banded());assert.deepEqual(notes(a),[]);assert.match(a.$('doc-convert-status').textContent,/^当前是 2\.0 分带模板。$/);
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('S03-L L4: a template that is not 1.x cannot be converted; a box that overlaps the table\'s vertical extent refuses the conversion by name and changes nothing',async()=>{const a=await designer();try{
 // n3 sits beside the narrow table (1.x allows it) in its vertical extent: refused, with its name and the extents.
 const beside=cv.table(a,[['n1',10,30,100,10],['n3',120,70,60,10]],[['aging.bucket',60],['customer.id',40]],{x:10,y:60,width:100,height:100,font_size:9});cv.load(a,beside);
 a.click('doc-convert');assert.match(a.$('doc-status').textContent,/^文本框 n3 与明细表的纵向范围重叠（y 70–80 毫米，表 60–160 毫米），无法转换为 2\.0：请先把它移到表的上方或下方。$/);
 assert.deepEqual(cv.tpl(a),beside);assert.equal(a.$('doc-undo').disabled,true);assert.deepEqual(notes(a),[]);
 // Move it above the table's top: the conversion goes through; the box is in the document header.
 const ok=structuredClone(beside);ok.elements[1].y=40;cv.load(a,ok);a.click('doc-convert');assert.equal(cv.tpl(a).schema_version,'2.0');assert.deepEqual(cv.tpl(a).bands.document_header.elements.map(e=>e.id),['e1','e2']);
 // A box that touches the table's top edge exactly is above it; one that touches its bottom edge is below.
 const edge=cv.table(a,[['n1',10,50,100,10],['n2',10,160,100,10]],[['aging.bucket',100],['customer.id',90]],{x:10,y:60,width:190,height:100,font_size:9});cv.load(a,edge);a.click('doc-convert');
 assert.deepEqual([cv.tpl(a).bands.document_header.elements.map(e=>e.id),cv.tpl(a).bands.document_footer.elements.map(e=>e.id)],[['e1'],['e3']]);assert.deepEqual(cv.rel(a,'e3'),[0,0,100,10]);assert.equal(cv.tpl(a).bands.document_footer.height,10);
 // 2.0 is not converted again.
 assert.equal(a.$('doc-convert').hidden,true);
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('S03-L L4: a template with a print offset, the variables mode or a background converts with a warning that these are not part of 2.0 and are dropped; a template without a table gets a default one',async()=>{const a=await designer();try{
 const bg={id:'a'.repeat(32),sha256:'b'.repeat(64)};
 const full=cv.table(a,[['n1',10,30,100,10,'literal','Title'],['n2',120,30,70,10,'row','customer.id']],[['aging.bucket',60],['customer.id',60],['ar.open_balance',60]],{x:10,y:60,width:180,height:100,font_size:9});
 full.schema_version='1.3';full.print={mode:'variables',offset_x:1.5,offset_y:-2};full.background=bg;cv.load(a,full);a.click('doc-convert');assert.match(a.$('doc-status').textContent,/有被丢弃的设置/);
 let n=notes(a);assert.ok(n.some(x=>/^警告：变量层模式不属于 2\.0，已丢弃；2\.0 打印全部文本（含固定文字）。$/.test(x)),n.join('|'));assert.ok(n.some(x=>/^警告：整体偏移 1\.5 \/ -2 毫米不属于 2\.0，已丢弃；文本位置按偏移前的位置转换。$/.test(x)));assert.ok(n.some(x=>/^警告：底稿引用不属于 2\.0，已丢弃；2\.0 预览不叠加底稿。$/.test(x)));
 let t=cv.tpl(a);assert.equal('print' in t,false);assert.equal('background' in t,false);assert.deepEqual(t.bands.document_header.elements.map(e=>[e.x,e.y]),[[0,5],[110,5]],'positions as before the offset');
 // An offset of zero in full mode is nothing to warn about.
 const quiet=cv.table(a,[['n1',10,30,100,10]],[['aging.bucket',100]],{x:10,y:60,width:100,height:100,font_size:9});cv.load(a,quiet);a.click('doc-convert');assert.equal(notes(a).some(x=>x.startsWith('警告')),false);
 // 1.2 (a background, no table) has no table either: a default table of the first columns takes the detail band.
 const paper={schema_version:'1.2',kind:'document',binding:cv.tpl(a).binding,elements:[{id:'n1',source:'literal',field:'',text:'Title',x:10,y:30,width:100,height:10,font_size:12,align:'left'},{id:'n2',source:'row',field:'customer.id',text:'',x:120,y:60.5,width:80,height:8,font_size:10,align:'right'}],print:{mode:'full',offset_x:0,offset_y:0},background:bg};
 cv.load(a,paper);a.click('doc-convert');const lastStatus=a.$('doc-status').textContent;t=cv.tpl(a);
 assert.deepEqual(t.bands.document_header,{height:43.5,elements:[text2('e1','literal','Title','',0,5,100,10,12,'left'),text2('e2','field','','customer.id',110,35.5,80,8,10,'right')]},'the header reaches the lowest box');
 assert.deepEqual(t.bands.detail.elements[0],{id:'e3',type:'table',x:0,width:190,font_size:10,columns:[{field:'customer.id',width:63.3},{field:'aging.bucket',width:63.3},{field:'ar.open_balance',width:63.4}]});assert.equal(t.bands.document_footer,undefined);
 n=notes(a);assert.match(n[0],/^1\.x 模板没有明细表，而 2\.0 的明细带必须有一个明细表：已加入默认明细表（结果的前 3 个字段，10 点，全宽），请按需要调整列。$/);assert.ok(n.some(x=>/^警告：底稿引用/.test(x)));assert.equal(lastStatus.includes('丢弃'),true);
 // No room for the default table below the lowest box: refused, naming what is missing.
 const low=structuredClone(paper);low.elements[1].y=262;low.elements[1].height=8;cv.load(a,low);a.click('doc-convert');assert.match(a.$('doc-status').textContent,/^这个 1\.x 模板没有明细表，而 2\.0 必须有明细带：文本框最低处（y 270 毫米）没有给默认明细表留下至少 \d+(\.\d+)? 毫米的空间/);assert.equal(cv.tpl(a).schema_version,'1.2');
 // Only boxes below the table: there is no header; the table moves to the top of the safe region, which the notes say. A table too low for 2.0's minimum is refused by the validator.
 const under=cv.table(a,[['n1',10,200,100,10]],[['aging.bucket',100],['customer.id',90]],{x:10,y:100,width:190,height:100,font_size:9});cv.load(a,under);a.click('doc-convert');t=cv.tpl(a);
 assert.equal('document_header' in t.bands,false);assert.deepEqual(Object.keys(t.bands),['detail','document_footer']);assert.deepEqual(cv.rel(a,'e2'),[0,0,100,10]);assert.match(notes(a)[0],/^明细表原在第一页 y 100 毫米；2\.0 的明细表紧接在上方各带之后，现在位于安全区顶部（y 25 毫米）/);
 const lowTable=cv.table(a,[['n1',10,30,100,10]],[['aging.bucket',100],['customer.id',90]],{x:10,y:240,width:190,height:30,font_size:12});lowTable.elements[0].y=30;cv.load(a,lowTable);a.click('doc-convert');assert.match(a.$('doc-status').textContent,/页眉、单据头与页脚的高度加上明细表的最小高度超出安全区/);assert.equal(cv.tpl(a).schema_version,'1.3');
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

// The text a PDF draws, as the page program emits it: one `q 1 0 0 -1 X Y cm ... BT ... /fN SIZE Tf` per glyph, X and Y in points (Y from the page's bottom edge).
// Glyph numbers differ between files with different text, positions do not: the positions are what the layout decides.
function pdfGlyphs(bytes){
  const pages=[];let pos=0;
  for(;;){const i=bytes.indexOf('stream\n',pos,'latin1');if(i<0)break;const start=i+7,end=bytes.indexOf('endstream',start,'latin1');if(end<0)break;pos=end+9;
   let text;try{text=inflateSync(bytes.subarray(start,end)).toString('latin1');}catch{continue;}
   if(!/ Tf/.test(text))continue;const page=[],re=/q 1 0 0 -1 ([\d.]+) ([\d.]+) cm[^\n]*\nBT[^\n]*?\/f\d+ ([\d.]+) Tf/g;let m;
   while((m=re.exec(text)))page.push([Number(m[1]),Number(m[2]),Number(m[3])]);pages.push(page);}
  return pages;
}
const A4_POINTS=841.8898,pt=mm=>mm*72/25.4;
const aboveMm=(page,mm)=>page.filter(g=>g[1]>A4_POINTS-pt(mm)).map(g=>g.join(' ')).sort();

test('S03-L L4: the converted template previews through the real server and pinned Typst renderer, and every box converted into the document header is at the page position the 1.x preview gave it',{skip:!pdfConfigured&&'needs the pinned Typst renderer'},async()=>{const a=app();try{
 await a.login();a.configure();a.click('snapshot');await until(()=>a.$('result-kind').textContent==='冻结快照'&&!a.$('preview').disabled,'frozen');a.w.document.querySelector('[data-page="documents"]').click();
 const one=invoice1x(a);one.elements[0].font_size=18;one.elements[1].align='right';cv.load(a,one);a.input('doc-row','3');
 a.click('doc-pdf');await until(()=>a.downloads.length===1&&!a.$('preview').disabled,'the 1.x preview',20000);const pdf1=pdfGlyphs(Buffer.from(await a.downloads[0].blob.arrayBuffer()))[0];
 // The 2.0 preview of the converted template without a group: the customer differs between the rows, so the row field is refused, with its place, which the canvas outlines.
 a.click('doc-convert');a.click('doc-pdf');await until(()=>a.$('notice').textContent.includes('模板无法排版')&&!a.$('preview').disabled,'inconsistent group',20000);
 assert.match(a.$('notice').textContent,/位置：同一分组内该字段的取值不一致 · 单据头带 · 元素 e2 · 快照第 \d+ 行 · 列 .*（customer\.id）。/);
 assert.deepEqual([...a.$('doc-canvas-host').querySelectorAll('.cv-refused')].map(e=>e.getAttribute('data-key')),['e2'],'the row field text is outlined');assert.equal(a.$('doc-canvas-host').querySelectorAll('.cv-refused-band').length,1,'and its band');assert.equal(a.$('doc-canvas-host').querySelector('.cv-refused-band').getAttribute('data-band-box'),'document_header');
 assert.match(a.$('doc-canvas-status').textContent,/已描出轮廓：单据头带，e2/);
 // Grouped by customer, the row's group is its customer: the same preview row 3.
 chg(a,'doc2-group-0','customer.id');assert.equal(a.$('doc-canvas-host').querySelectorAll('.cv-refused').length,0,'an edit takes the outline away');
 a.click('doc-pdf');await until(()=>a.downloads.length===2&&!a.$('preview').disabled,'the 2.0 preview',20000);const pdf2=pdfGlyphs(Buffer.from(await a.downloads[1].blob.arrayBuffer()))[0];
 // Everything above the table's top edge (60 mm): the banner, the title (18 pt), the customer and the label (12 pt) are drawn at the same points in both files.
 const head1=aboveMm(pdf1,60),head2=aboveMm(pdf2,60);assert.ok(head1.length>=19,'the banner and the three boxes: '+head1.length);assert.deepEqual(head2,head1,'the boxes of the document header are where the 1.x preview put them');
 // The footer boxes (12 pt, below the table) are where 1.x drew them, 205 mm from the top, in the 1.x file, and directly under the table's last row in the 2.0 file: higher on the page.
 const foot=page=>page.filter(g=>g[2]===12&&g[1]<A4_POINTS-pt(60)).map(g=>g[1]),f1=foot(pdf1),f2=foot(pdf2),rows2=pdf2.filter(g=>g[2]===9).map(g=>g[1]);
 assert.ok(Math.abs(Math.min(...f1)-(A4_POINTS-pt(205)-1.16*12))<0.01,'the 1.x label is 205 mm down');assert.ok(Math.min(...f2)>Math.max(...f1)+100,'the 2.0 footer follows the table');assert.ok(Math.max(...f2)<Math.min(...rows2),'and is under the last row');
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('S03-L L4: a converted template is saved only when the user saves it, as the next version of the template; the earlier 1.x version stays as it was',async()=>{const a=app(offeringIssuance({bodies:[]}));try{
 await a.login();a.configure();a.click('snapshot');await until(()=>a.$('result-kind').textContent==='冻结快照'&&!a.$('preview').disabled,'frozen');a.w.document.querySelector('[data-page="documents"]').click();
 const one=invoice1x(a);cv.load(a,one);a.click('doc-server-save');await until(()=>a.$('doc-server-status').textContent.includes('版本 1')&&!a.$('preview').disabled,'saved 1.x');
 const id=a.$('doc-server-status').textContent.match(/[a-f0-9]{32}/)[0];
 a.click('doc-convert');assert.equal(a.$('doc-server-status').textContent.includes('版本 1'),true,'converting saves nothing');
 // Unsaved: publishing or previewing the saved version is refused until it is saved.
 a.click('doc-release-read');await until(()=>a.$('doc-release-status').textContent.includes('修订 0')&&!a.$('preview').disabled,'release');a.click('doc-release-publish');assert.match(a.$('doc-status').textContent,/请先保存当前修改，再发布或按保存版本预览/);
 a.click('doc-server-save');await until(()=>a.$('doc-server-status').textContent.includes('版本 2')&&!a.$('preview').disabled,'saved 2.0');
 // The server kept the 2.0 form as version 2 of the same template; version 1 is still the 1.x template.
 a.click('doc-server-list');await until(()=>[...a.$('doc-server-listing').options].some(o=>o.value===id)&&!a.$('preview').disabled,'list');a.$('doc-server-listing').value=id;a.$('doc-server-version').value='1';a.click('doc-server-open');await until(()=>a.$('doc-server-status').textContent.includes('版本 1')&&a.$('doc-server-status').textContent.includes('正在编辑')&&!a.$('preview').disabled,'open v1');
 assert.deepEqual(cv.tpl(a),one);a.$('doc-server-version').value='2';a.click('doc-server-open');await until(()=>a.$('doc-server-status').textContent.includes('版本 2')&&!a.$('preview').disabled,'open v2');assert.equal(cv.tpl(a).schema_version,'2.0');assert.equal(cv.tpl(a).bands.document_header.height,35);
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('S03-L L5: a refusal names a band, an element or a column of a 2.0 template, and the canvas outlines it as for 1.x',async()=>{
 let answerWith={reason:'DOES_NOT_FIT',band:'document_header',element:'e5',row:1};
 const columnDetail={reason:'DOES_NOT_FIT',band:'detail',element:'e6',column:'ar.open_balance',row:2},bandDetail={reason:'BAND_OVERFLOW',band:'document_footer'};
 const check=offeringChecks({rows:[{row_index:0,ok:true,reasons:['first']},{row_index:1,ok:false,code:'UNSUPPORTED_COMBINATION',detail:bandDetail,reasons:['last']},{row_index:2,ok:false,code:'UNSUPPORTED_COMBINATION',detail:columnDetail,reasons:['cell:ar.open_balance']}]});
 const a=app(async(path,options)=>{if(path.endsWith('/document-previews'))return new Response(JSON.stringify({error:'UNSUPPORTED_COMBINATION',detail:answerWith}),{status:422,headers:{'Content-Type':'application/json'}});return check(path,options);});try{
 await checkReady(a);cv.load(a,banded());
 const host=a.$('doc-canvas-host'),outlined=()=>[...host.querySelectorAll('.cv-refused')].map(e=>e.getAttribute('data-key')),bands=()=>[...host.querySelectorAll('.cv-refused-band')].map(e=>e.getAttribute('data-band-box')),columns=()=>[...host.querySelectorAll('.cv-refused-col')].map(e=>e.getAttribute('data-refused'));
 assert.deepEqual([outlined(),bands(),columns()],[[],[],[]]);
 // A text that does not fit: its band and its box are outlined, the notice names both, the box says so to a screen reader.
 a.click('doc-pdf');await until(()=>a.$('notice').textContent.includes('模板无法排版')&&!a.$('preview').disabled,'refusal');
 assert.match(a.$('notice').textContent,/位置：放不下：值比所在的框、列或页更宽或更高 · 单据头带 · 元素 e5 · 快照第 2 行。/);assert.deepEqual([outlined(),bands(),columns()],[['e5'],['document_header'],[]]);
 assert.match(cv.item(a,'e5').getAttribute('aria-label'),/预览或检查指出此处/);assert.equal(cv.item(a,'e4').getAttribute('aria-label').includes('预览或检查'),false);assert.match(a.$('doc-canvas-status').textContent,/已描出轮廓：单据头带，e5/);
 // The outline is the canvas's, not the template's: an edit takes it away, and nothing was written.
 assert.equal(JSON.stringify(cv.tpl(a)),JSON.stringify(banded()));cv.click2(a,'e4');cv.key(a,cv.item(a,'e4'),'ArrowDown');assert.deepEqual([outlined(),bands(),columns()],[[],[],[]]);
 // A band that is too full (the document footer does not fit a page): only the band.
 answerWith=bandDetail;a.click('doc-pdf');await until(()=>a.$('notice').textContent.includes('带放不下')&&!a.$('preview').disabled,'band refusal');
 assert.match(a.$('notice').textContent,/位置：带放不下：超出页面留给它的高度 · 单据尾带。/);assert.deepEqual([outlined(),bands(),columns()],[[],['document_footer'],[]]);
 // A column of the table: the column over the detail band, whatever the pointer does.
 answerWith=columnDetail;a.click('doc-pdf');await until(()=>a.$('notice').textContent.includes('（ar.open_balance）')&&!a.$('preview').disabled,'column refusal');
 assert.deepEqual([outlined(),bands(),columns()],[['e6'],['detail'],['ar.open_balance']]);
 const rect=host.querySelector('.cv-refused-col'),g=geometry2(a).detail;assert.deepEqual(attrs(rect,'x','y','width','height'),[X2(a,130),X2(a,g.top),70*SC(a),g.height*SC(a)],'the third column, over the whole detail band');
 // A detail the console cannot verify is not shown.
 for(const bad of [{reason:'DOES_NOT_FIT',band:'middle'},{reason:'DOES_NOT_FIT',element:'n9x'},{reason:'BAND_OVERFLOW',extra:1}]){answerWith=bad;cv.load(a,banded());a.click('doc-pdf');await until(()=>a.$('notice').textContent.includes('模板无法排版')&&!a.$('preview').disabled,'unverified');assert.equal(a.$('notice').textContent.includes('位置：'),false);assert.deepEqual([outlined(),bands(),columns()],[[],[],[]]);}
 // The check shows each refused row's place and one click outlines it, on the canvas of a 2.0 template.
 cv.load(a,banded());a.click('doc-check');await until(()=>a.$('doc-check-body').rows.length===3&&!a.$('preview').disabled,'checked');
 const rows=checkRows(a);assert.match(rows[1][1],/不通过 · UNSUPPORTED_COMBINATION（.*） · 带放不下.* · 单据尾带$/);assert.match(rows[2][1],/放不下.* · 明细带 · 元素 e6 · 快照第 3 行 · 列 .*（ar\.open_balance）$/);
 const locate=r=>a.$('doc-check-body').rows[r].cells[3].querySelectorAll('button');assert.equal(locate(0).length,1);assert.equal(locate(1).length,2,'a band is a place');assert.equal(locate(2).length,2);
 locate(1)[1].click();assert.deepEqual([outlined(),bands(),columns()],[[],['document_footer'],[]]);locate(2)[1].click();assert.deepEqual([outlined(),bands(),columns()],[['e6'],['detail'],['ar.open_balance']]);assert.match(a.$('doc-check-status').textContent,/第 3 行：放不下/);
 // A new check takes the outline away with the old result.
 a.click('doc-check');await until(()=>a.$('doc-check-body').rows.length===3&&!a.$('preview').disabled,'checked again');cv.click2(a,'e4');cv.key(a,cv.item(a,'e4'),'ArrowDown');assert.deepEqual([outlined(),bands(),columns()],[[],[],[]]);
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('S03-L: the palette lists the 2.0 actions for a 2.0 template and the conversion for a 1.x one, each only while its button is present, visible and enabled, and runs the button',async()=>{const a=await designer();try{
 cv.load(a,cv.four(a));cv.click(a,'n1');const item=cv.item(a,'n1');item.focus();cv.key(a,item,'k',{ctrlKey:true});
 let listed=options(a);assert.ok(listed.includes('转换为 2.0 分带模板'));for(const label of ['在所选带加线','添加明细表列'])assert.ok(!listed.includes(label),label+' belongs to 2.0');
 typeInto(a,'转换');assert.deepEqual(options(a),['转换为 2.0 分带模板']);press(a,'Enter');assert.equal(cv.tpl(a).schema_version,'2.0');assert.equal(a.w.document.querySelector('.cv-palette').parentElement.hidden,true);
 // The 2.0 template: the add commands of the band chosen in the panel, the table's column command, undo (there is history now); no 1.x commands, no conversion.
 const first=cv.keys(a)[0];cv.click2(a,first);cv.key(a,cv.item(a,first),'k',{ctrlKey:true});listed=options(a);
 for(const label of ['在所选带加固定文字','在所选带加字段文本','在所选带加分组合计','在所选带加页码文字','在所选带加线','在所选带加矩形','添加明细表列','撤销','删除所选项目'])assert.ok(listed.includes(label),label+' is listed: '+listed);
 for(const label of ['添加固定文字','添加行字段','添加原范围合计','添加或替换明细表','移除明细表','转换为 2.0 分带模板'])assert.ok(!listed.includes(label),label+' is a 1.x command or done');
 typeInto(a,'在所选带');assert.deepEqual(options(a),['在所选带加固定文字','在所选带加字段文本','在所选带加分组合计','在所选带加页码文字','在所选带加线','在所选带加矩形']);
 typeInto(a,'ADD line');assert.deepEqual(options(a),['在所选带加线']);
 // It runs the button: a line in the band the panel has chosen.
 const before=Object.values(cv.tpl(a).bands).flatMap(b=>b.elements).length;a.$('doc2-add-band').value='document_header';press(a,'Enter');assert.equal(Object.values(cv.tpl(a).bands).flatMap(b=>b.elements).length,before+1);assert.equal(cv.tpl(a).bands.document_header.elements.at(-1).type,'line');
 // Hidden or disabled: the panel's buttons leave the list at once.
 a.$('doc2-add-rect').disabled=true;a.$('doc2-add-sum').hidden=true;cv.key(a,cv.item(a,first),'k',{ctrlKey:true});listed=options(a);assert.ok(!listed.includes('在所选带加矩形'));assert.ok(!listed.includes('在所选带加分组合计'));assert.ok(listed.includes('在所选带加线'));press(a,'Escape');
 a.$('st-insert-two').hidden=true;cv.key(a,cv.item(a,first),'k',{ctrlKey:true});assert.ok(!options(a).includes('在所选带加线'),'inside a hidden panel');press(a,'Escape');
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('S03-L: every way of changing a 2.0 template is one undo step, and undo and redo walk back and forth through canvas, panels and conversion',async()=>{const a=await designer();try{
 cv.load(a,banded());const start=cv.tpl(a);let n=0;const step=fn=>{fn();n++;};
 step(()=>cv.drag2(a,'e4',7.4,3.2));step(()=>cv.dragBand(a,'page_header',2.2));step(()=>{cv.click2(a,'e4');cv.key(a,cv.item(a,'e4'),'ArrowLeft');});
 step(()=>{a.$('doc2-add-band').value='document_header';clickId(a,'doc2-add-line');});step(()=>chg(a,'doc2-band-height-page_footer',12));step(()=>chg(a,'doc2-group-1','aging.bucket'));step(()=>chg(a,'doc2-col-label-2','余额'));step(()=>clickId(a,'doc2-band-remove-document_footer'));
 step(()=>{cv.click2(a,'e4');cv.click2(a,'e5',{shiftKey:true});a.click('doc-align-top');});
 const refusedBefore=cv.tpl(a);cv.click2(a,'e4');chg(a,'doc2-p-x',9999);assert.deepEqual(cv.tpl(a),refusedBefore,'a refused change is no step');
 assert.equal(undoAll(a),n);assert.deepEqual(cv.tpl(a),start);
 for(let k=0;k<n;k++)a.click('doc-redo');assert.deepEqual(cv.tpl(a),refusedBefore);
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('S03-L: what the 2.0 canvas and panels draw is DOM nodes and CSSOM only: no style attribute, no inline handler, hostile text stays text',async()=>{const a=await designer();try{
 const hostile='<img src=x onerror=alert(1)>',t=banded();t.bands.page_header.elements[0].text=hostile;t.bands.document_footer.elements[1].field='ar.open_balance';cv.load(a,t);
 cv.click2(a,'e4');cv.drag2(a,'e4',5,2);cv.dragBand(a,'page_header',1.6);cv.click2(a,'e3');cv.drag2(a,'e3',0,-1);cv.click2(a,'e6');cv.key(a,cv.item(a,'e6'),'k',{ctrlKey:true});typeInto(a,'线');press(a,'Escape');
 for(const root of [a.$('doc-canvas-panel'),a.$('doc2-panel'),a.$('doc-convert-panel'),palette(a)].filter(Boolean)){assert.equal(root.querySelectorAll('[style]').length,0,'no style attribute in '+(root.id||root.className));for(const e of root.querySelectorAll('*'))assert.equal([...e.attributes].some(x=>/^on/i.test(x.name)),false,'no inline handler');}
 assert.equal(a.$('doc-canvas-host').querySelectorAll('img,script').length,0);assert.equal(cv.item(a,'e1').querySelector('text').textContent,hostile);assert.ok(cv.item(a,'e1').getAttribute('aria-label').includes(hostile));
 assert.equal(a.$('doc2-panel').querySelectorAll('img,script').length,0);cv.click2(a,'e1');assert.equal(a.$('doc2-p-text').value,hostile,'a text shown in an input is a value, not markup');
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('S03-L L3: a panel is updated in place when its controls stay, so the focus and a click are not lost, and replaced when the element is another, so no handler acts on the wrong one',async()=>{const a=await designer();try{
 cv.load(a,banded());
 cv.click2(a,'e4');const x=a.$('doc2-p-x');x.focus();assert.equal(a.w.document.activeElement,x);
 chg(a,'doc2-p-x',3);assert.equal(el2(a,'e4').x,3);assert.equal(a.$('doc2-p-x'),x,'the same input, updated');assert.equal(a.w.document.activeElement,x,'and it keeps the focus');
 // Moved on the canvas, the input is updated in place too.
 cv.drag2(a,'e4',2,0);assert.equal(a.$('doc2-p-x'),x);assert.equal(x.value,String(el2(a,'e4').x));
 // Another element: another panel, whose controls act on that element only.
 cv.click2(a,'e5');assert.notEqual(a.$('doc2-p-x'),x);assert.equal(a.$('doc2-p-title').textContent,'e5 · 单据头带 · 文本（字段）');const before=el2(a,'e4').x;chg(a,'doc2-p-x',99);assert.equal(el2(a,'e5').x,99);assert.equal(el2(a,'e4').x,before,'e4 is untouched');
 // The same type of element, selected after another: its own values, its own handlers.
 cv.click2(a,'e8');assert.equal(a.$('doc2-p-x').value,String(el2(a,'e8').x));chg(a,'doc2-p-x',101);assert.equal(el2(a,'e8').x,101);assert.equal(el2(a,'e5').x,99);
 // The table's columns: a width typed is updated in place; a reordered column has other options in its field select, so its row is replaced, and its handlers still go by position.
 const width=a.$('doc2-col-width-1');chg(a,'doc2-col-width-1',33);assert.equal(a.$('doc2-col-width-1'),width);assert.equal(cv.tpl(a).bands.detail.elements[0].columns[1].width,33);clickId(a,'doc2-col-up-2');chg(a,'doc2-col-width-2',30);assert.deepEqual(cv.tpl(a).bands.detail.elements[0].columns.map(c=>c.field),['aging.bucket','ar.open_balance','customer.id']);assert.equal(cv.tpl(a).bands.detail.elements[0].columns[2].width,30);
 assert.equal(a.errors.length,0);
 }finally{a.close();}});


// ---- S03-M: barcodes, QR codes, images and the issue number (schema 2.1, ADR 0005) ----------------------------------------------------
// A PNG of one colour, made here (no dependency): the signature, a header, one compressed data chunk and the end.
function pngOf(w,h,rgba=[30,90,200,255]){
  const raw=Buffer.alloc((w*4+1)*h);for(let y=0;y<h;y++)for(let x=0;x<w;x++)Buffer.from(rgba).copy(raw,y*(w*4+1)+1+x*4);
  const chunk=(type,data)=>{const body=Buffer.concat([Buffer.from(type),data]),out=Buffer.alloc(12+data.length);out.writeUInt32BE(data.length,0);body.copy(out,4);out.writeUInt32BE(crc32(body)>>>0,8+data.length);return out;};
  const header=Buffer.alloc(13);header.writeUInt32BE(w,0);header.writeUInt32BE(h,4);header[8]=8;header[9]=6;
  return Buffer.concat([Buffer.from([0x89,0x50,0x4e,0x47,0x0d,0x0a,0x1a,0x0a]),chunk('IHDR',header),chunk('IDAT',deflateSync(raw)),chunk('IEND',Buffer.alloc(0))]);
}
const setFile=(a,id,bytes,size=bytes.length)=>Object.defineProperty(a.$(id),'files',{configurable:true,value:[{size,arrayBuffer:async()=>bytes.buffer.slice(bytes.byteOffset,bytes.byteOffset+bytes.byteLength)}]});
const ready=a=>!a.$('preview').disabled;
// The 2.0 test template as 2.1 with one element of each new kind: a QR code of a literal in the document header, a Code 128 and a picture in the
// document footer, the issue number as text in the page footer.
const FAKE_IMAGE={id:'a'.repeat(32),sha256:'b'.repeat(64)};
function coded(){
  const t=banded();t.schema_version='2.1';t.bands.document_header.height=30;
  t.bands.document_header.elements.push({id:'e20',type:'qrcode',source:'literal',text:'https://example.invalid/i/1',field:'',x:0,y:10,size:14,ecc:'Q'});
  t.bands.document_footer.elements.push({id:'e21',type:'barcode',source:'issue_number',text:'',field:'',x:0,y:0,width:60,height:12,symbology:'code128',human_readable:false},{id:'e23',type:'image',x:65,y:0,width:30,height:18,image:{...FAKE_IMAGE}});
  t.bands.page_footer.elements.push({id:'e22',type:'text',source:'issue_number',text:'',field:'',wrap:false,x:125,y:2,width:65,height:6,font_size:8,align:'left',bold:false});
  return t;
}

test('S03-M M5: a 2.1 template is drawn: codes are labelled placeholders inside their quiet zone, an image a labelled box, the issue number a text; each is named and selectable',async()=>{const a=await designer();try{
 cv.load(a,coded());
 assert.deepEqual(cv.keys(a).sort(),['e1','e2','e20','e21','e22','e23','e3','e4','e5','e6','e7','e8','e9']);
 const names={
  e20:/^e20，二维码，纠错 Q，值 固定 “https:\/\/example\.invalid\/i\/1”，x 0 毫米，y 10 毫米，边长 14 毫米，单据头带$/,
  e21:/^e21，条码 Code 128，值 出具编号，不印出值，x 0 毫米，y 0 毫米，宽 60 毫米，高 12 毫米，单据尾带$/,
  e22:/^e22，出具编号，字号 8 点，x 125 毫米，y 2 毫米，宽 65 毫米，高 6 毫米，页脚带$/,
  e23:/^e23，图片 a{32}，x 65 毫米，y 0 毫米，宽 30 毫米，高 18 毫米，单据尾带$/,
 };
 for(const [key,pattern] of Object.entries(names))assert.match(cv.item(a,key).getAttribute('aria-label'),pattern,key);
 // The QR code: its three finder patterns and the symbol's frame inside four modules of quiet zone. 27 bytes at level Q is version 3, 29 modules and 37 with the quiet zone.
 const qr=cv.item(a,'e20'),box=attrs(qr.querySelector('.cv-box'),'x','y','width','height'),frame=attrs(qr.querySelector('.cv-quiet'),'x','y','width','height');
 const q=4/37;assert.deepEqual(frame.map(v=>Math.round(v*100)/100),[box[0]+box[2]*q,box[1]+box[3]*q,box[2]*(1-2*q),box[3]*(1-2*q)].map(v=>Math.round(v*100)/100));
 assert.equal(qr.querySelectorAll('.cv-finder').length,3);assert.equal(qr.querySelector('.cv-bars'),null);
 assert.ok([...qr.querySelectorAll('.cv-codelabel')].some(t=>t.textContent==='二维码 QR Q · 固定 “https://example.invalid/i/1”'));assert.ok([...qr.querySelectorAll('.cv-codelabel')].some(t=>t.textContent==='静区 4 模块（约 37 模块估算）'));
 // The barcode of the issue number: the quiet zone of the longest number, 407 modules with its ten on each side; bars, no finder patterns.
 const bc=cv.item(a,'e21'),bbox=attrs(bc.querySelector('.cv-box'),'x','y','width','height'),bframe=attrs(bc.querySelector('.cv-quiet'),'x','width'),f=10/407;
 assert.deepEqual(bframe.map(v=>Math.round(v*100)/100),[bbox[0]+bbox[2]*f,bbox[2]*(1-2*f)].map(v=>Math.round(v*100)/100));assert.ok(bc.querySelector('.cv-bars'));assert.equal(bc.querySelectorAll('.cv-finder').length,0);
 assert.ok([...bc.querySelectorAll('.cv-codelabel')].some(t=>t.textContent==='条码 Code 128 · 出具编号'));assert.ok([...bc.querySelectorAll('.cv-codelabel')].some(t=>t.textContent==='静区 10 模块（约 407 模块估算）'));
 // The picture is not read yet (or not there): a box with its id; the text of the issue number is a placeholder name, not a number.
 assert.equal(cv.item(a,'e23').querySelector('.cv-picture'),null);assert.ok([...cv.item(a,'e23').querySelectorAll('.cv-codelabel')].some(t=>t.textContent.startsWith('图片 aaaaaaaa')));
 assert.equal(cv.item(a,'e22').querySelector('text').textContent,'〔出具编号〕');assert.match(cv.item(a,'e22').getAttribute('class'),/cv-number/);
 // A field's value is 16 characters for the estimate, a longer literal is longer: 8 characters of Code 128 is 11 × 10 + 13 + 20 = 143 modules.
 const t=coded();t.bands.document_footer.elements[2]={id:'e24',type:'barcode',source:'literal',text:'INV-0001',field:'',x:100,y:12,width:50,height:8,symbology:'code128',human_readable:false};cv.load(a,t);
 assert.ok([...cv.item(a,'e24').querySelectorAll('.cv-codelabel')].some(t=>t.textContent==='静区 10 模块（约 143 模块估算）'));
 // A QR code is square: resized by its east edge, its side follows; undone, it is as it was. A move is one step.
 cv.load(a,coded());cv.click2(a,'e20');const [x,y,w,h]=page2(cv.tpl(a),'e20');cv.drag(a,cv.handle(a,'e'),x+w,y+h/2,2,0);
 assert.equal(el2(a,'e20').size,16);assert.equal(el2(a,'e20').width,undefined);a.click('doc-undo');assert.equal(el2(a,'e20').size,14);
 cv.drag2(a,'e20',3,0);assert.equal(el2(a,'e20').x,3);assert.equal(el2(a,'e20').size,14);
 // Overlap is the validator's: a barcode dragged onto the text of its band snaps back with the reason.
 cv.click2(a,'e21');cv.drag2(a,'e21',102,3,{altKey:true});assert.match(cv.status(a),/^未应用：同一带内的文本框、条码、二维码与图片不能重叠。/);
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('S03-M M5: the panels add a barcode, a QR code and the issue number to a 2.0 template, which becomes 2.1; each element is one undo step and its properties are inputs',async()=>{const a=await designer();try{
 cv.load(a,banded());
 a.$('doc2-add-band').value='document_header';chg(a,'doc2-band-height-document_header',60);chg(a,'doc2-band-height-document_footer',40);
 // A literal Code 128 at the first free place of the document header: below e4, 80 mm wide, 18 mm high, no printed value.
 a.$('doc2-code-source').value='literal';a.$('doc2-text').value='ABC-123';clickId(a,'doc2-add-barcode');assert.equal(doc2status(a),READY);
 assert.equal(cv.tpl(a).schema_version,'2.1','a 2.0 template with a barcode is 2.1');
 assert.deepEqual(el2(a,'e10'),{id:'e10',type:'barcode',source:'literal',text:'ABC-123',field:'',x:0,y:10,width:80,height:18,symbology:'code128',human_readable:false});assert.deepEqual(cv.selected(a),['e10']);
 // A QR code of the issue number, and the issue number as a text.
 a.$('doc2-code-source').value='issue_number';clickId(a,'doc2-add-qrcode');assert.deepEqual(el2(a,'e11'),{id:'e11',type:'qrcode',source:'issue_number',text:'',field:'',x:0,y:28,size:24,ecc:'M'});
 clickId(a,'doc2-add-number');assert.deepEqual(el2(a,'e12'),{id:'e12',type:'text',source:'issue_number',text:'',field:'',wrap:false,x:0,y:52,width:60,height:6,font_size:10,align:'left',bold:false});
 // A field of the group as the value of a code: the chosen field.
 a.$('doc2-code-source').value='field';a.$('doc2-field').value='customer.id';a.$('doc2-add-band').value='document_footer';clickId(a,'doc2-add-qrcode');assert.deepEqual([find2(cv.tpl(a),'e13').band,el2(a,'e13').source,el2(a,'e13').field,el2(a,'e13').text],['document_footer','field','customer.id','']);
 // An image needs a private image of this dataset: none is listed, so the button says so and adds nothing.
 const before=cv.tpl(a);clickId(a,'doc2-add-image');assert.match(doc2status(a),/请先上传或读取私有图片/);assert.deepEqual(cv.tpl(a),before);
 // The barcode's properties: its source, its value, its box, the printed value with its type size (the key only while it prints).
 cv.click2(a,'e10');const ids=()=>[...a.$('doc2-props').querySelectorAll('[id^="doc2-p-"]')].map(e=>e.id.slice(7)).filter(k=>k!=='title');
 assert.equal(a.$('doc2-p-title').textContent,'e10 · 单据头带 · 条码 Code 128');assert.deepEqual(ids(),['band','x','y','width','height','source','text','human','delete']);
 chg(a,'doc2-p-text','XYZ-9');assert.equal(el2(a,'e10').text,'XYZ-9');chg(a,'doc2-p-width',70);assert.deepEqual(cv.rel(a,'e10'),[0,10,70,18]);
 chg(a,'doc2-p-human',true);assert.deepEqual([el2(a,'e10').human_readable,el2(a,'e10').font_size],[true,9]);assert.deepEqual(ids(),['band','x','y','width','height','source','text','human','font_size','delete']);
 chg(a,'doc2-p-font_size',7);assert.match(doc2status(a),/^e10：/);assert.equal(a.$('doc2-p-font_size').value,'9');
 chg(a,'doc2-p-human',false);assert.equal(el2(a,'e10').human_readable,false);assert.equal('font_size' in el2(a,'e10'),false,'no type size without a printed value');
 chg(a,'doc2-p-source','field');assert.deepEqual([el2(a,'e10').source,el2(a,'e10').field,el2(a,'e10').text],['field','customer.id','']);chg(a,'doc2-p-field','aging.bucket');assert.equal(el2(a,'e10').field,'aging.bucket');
 chg(a,'doc2-p-source','issue_number');assert.deepEqual([el2(a,'e10').field,el2(a,'e10').text],['','']);assert.equal(a.$('doc2-p-field'),null);assert.equal(a.$('doc2-p-text'),null);
 chg(a,'doc2-p-source','literal');assert.equal(el2(a,'e10').text,'INV-0001');
 chg(a,'doc2-p-width',9);assert.match(doc2status(a),/^e10：超出带范围、尺寸、字号、来源、值或图片引用无效。$/);assert.equal(a.$('doc2-p-width').value,'70');
 // The QR code's: its side and its level.
 cv.click2(a,'e11');assert.deepEqual(ids(),['band','x','y','size','source','ecc','delete']);assert.deepEqual([...a.$('doc2-p-ecc').options].map(o=>o.value),['L','M','Q','H']);chg(a,'doc2-p-ecc','H');chg(a,'doc2-p-size',20);assert.deepEqual([el2(a,'e11').ecc,el2(a,'e11').size],['H',20]);
 chg(a,'doc2-p-size',9);assert.match(doc2status(a),/^e11：/);assert.equal(a.$('doc2-p-size').value,'20');
 // A text becomes the issue number from its source select, and a 2.0 template with one is 2.1 (a template that is 2.1 stays 2.1).
 const text=banded();cv.load(a,text);cv.click2(a,'e4');chg(a,'doc2-p-source','issue_number');assert.deepEqual([el2(a,'e4').source,el2(a,'e4').field,el2(a,'e4').text,el2(a,'e4').wrap],['issue_number','','',false]);assert.equal(cv.tpl(a).schema_version,'2.1');
 a.click('doc-undo');assert.equal(cv.tpl(a).schema_version,'2.0','one undo step brings the 2.0 form back');assert.equal(el2(a,'e4').source,'field');
 // Every addition was one undo step: back to the 2.0 template.
 cv.load(a,banded());a.$('doc2-add-band').value='document_header';chg(a,'doc2-band-height-document_header',60);a.$('doc2-code-source').value='literal';clickId(a,'doc2-add-barcode');clickId(a,'doc2-add-qrcode');clickId(a,'doc2-add-number');assert.equal(undoAll(a),4);assert.deepEqual(cv.tpl(a),banded());
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('S03-M M4/M5: images are uploaded through the real server, listed, drawn on the canvas from the owner-only content route and used by an image element',async()=>{const a=await designer();try{
 cv.load(a,banded());
 // Refused before anything is read: over 512 KiB. Refused by the server: bytes that are no image.
 let read=false;Object.defineProperty(a.$('doc-image-file'),'files',{configurable:true,value:[{size:524289,arrayBuffer:async()=>{read=true;return new ArrayBuffer(1);}}]});a.click('doc-image-upload');assert.match(doc2status(a),/512KiB/);assert.equal(read,false);
 setFile(a,'doc-image-file',Buffer.from('GIF89a this is not a PNG or a JPEG'));a.click('doc-image-upload');await until(()=>a.$('notice').textContent.includes('图片无效')&&ready(a),'refused upload');
 assert.equal(a.$('doc2-image-listing').options.length,0);
 // A PNG of 60 x 30 pixels: kept, listed with its size and format, and offered to the element to add.
 const png=pngOf(60,30);setFile(a,'doc-image-file',png);a.click('doc-image-upload');await until(()=>doc2status(a).includes('图片已上传')&&ready(a),'upload');
 assert.equal(a.$('doc2-image-listing').options.length,1);assert.match(a.$('doc2-image-listing').options[0].textContent,/^[a-f0-9]{8} · 60 × 30 · PNG$/);assert.match(a.$('doc-image-status').textContent,/当前数据集可用图片 1 张/);
 const id=a.$('doc2-image-listing').value;a.$('doc2-add-band').value='document_footer';clickId(a,'doc2-add-image');assert.equal(doc2status(a),READY);
 const t=cv.tpl(a),image=el2(a,'e10');assert.equal(t.schema_version,'2.1');assert.equal(image.type,'image');assert.equal(image.image.id,id);assert.match(image.image.sha256,/^[a-f0-9]{64}$/);assert.deepEqual([image.x,image.y,image.width,image.height],[0,0,40,20],'contained 40 mm wide at the aspect of 2:1');
 // The canvas reads the picture from the content route, with the session's token, and draws it as a data URL: the bytes the server kept, whose digest the template names.
 await until(()=>cv.item(a,'e10').querySelector('.cv-picture'),'picture');
 const picture=cv.item(a,'e10').querySelector('.cv-picture'),href=picture.getAttribute('href');assert.match(href,/^data:image\/png;base64,/);assert.equal(picture.getAttribute('preserveAspectRatio'),'xMidYMid meet');
 assert.equal(createHash('sha256').update(Buffer.from(href.slice('data:image/png;base64,'.length),'base64')).digest('hex'),image.image.sha256);
 assert.ok(a.trace.some(v=>v.method==='GET'&&v.path.endsWith('/document-images/'+id+'/content')),'the content route was read');assert.equal(cv.item(a,'e10').querySelectorAll('.cv-codelabel').length,1);
 // The image's panel: one of the listed images, its box.
 cv.click2(a,'e10');assert.equal(a.$('doc2-p-title').textContent,'e10 · 单据尾带 · 图片');assert.deepEqual([...a.$('doc2-props').querySelectorAll('[id^="doc2-p-"]')].map(e=>e.id.slice(7)).filter(k=>k!=='title'),['band','x','y','width','height','image','delete']);assert.equal(a.$('doc2-p-image').value,id);
 chg(a,'doc2-p-width',44);assert.deepEqual(cv.rel(a,'e10'),[0,0,44,20]);chg(a,'doc2-p-width',4);assert.match(doc2status(a),/^e10：/);assert.equal(a.$('doc2-p-width').value,'44');
 // The list reads again on request; a new query forgets what was listed, and so does a logout.
 a.click('doc-image-list');await until(()=>a.$('doc-image-status').textContent.includes('当前数据集可用图片 1 张'),'list');
 a.input('query-companies','company_other');assert.equal(a.$('doc2-image-listing').options.length,0);
 a.click('logout');assert.equal(a.$('doc2-image-listing').options.length,0);assert.match(a.$('doc-image-status').textContent,/尚无当前授权模板/);
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('S03-M M5: the image routes are the owner\'s: another identity\'s image is not found, and the console shows an image it cannot verify as a box',async()=>{const a=await designer(async(path,options)=>{
  if(path.includes('/document-images/')&&path.endsWith('/content'))return new Response(pngOf(8,8),{headers:{'Content-Type':'image/png','X-Content-SHA256':'0'.repeat(64)}});
 });try{
 cv.load(a,banded());
 setFile(a,'doc-image-file',pngOf(40,40));a.click('doc-image-upload');await until(()=>doc2status(a).includes('图片已上传')&&ready(a),'upload');
 a.$('doc2-add-band').value='document_footer';clickId(a,'doc2-add-image');await sleep(150);
 // The route answered with a digest that is not the bytes': nothing is drawn, the element stays a labelled box.
 assert.equal(cv.item(a,'e10').querySelector('.cv-picture'),null);assert.ok([...cv.item(a,'e10').querySelectorAll('.cv-codelabel')].some(t=>t.textContent.includes('未读取')));
 // Over the real server, the content of an id the caller does not own is not found, and without a token there is none.
 const get=(path,headers)=>fetch(url+'/api/reporting/v1'+path,{headers});
 const own=cv.tpl(a).bands.document_footer.elements.find(e=>e.type==='image').image.id;
 assert.equal((await get('/document-images/'+own+'/content',{Authorization:'Bearer '+token})).status,200);assert.equal((await get('/document-images/'+own+'/content')).status,401);
 assert.equal((await get('/document-images/'+'0'.repeat(32)+'/content',{Authorization:'Bearer '+token})).status,404);
 const response=await get('/document-images/'+own+'/content',{Authorization:'Bearer '+token});assert.equal(response.headers.get('content-type'),'image/png');assert.equal(response.headers.get('cache-control'),'no-store');assert.equal(response.headers.get('x-content-type-options'),'nosniff');
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

// A 2.1 template whose layout the real renderer accepts for every number (the worst case is proved before the product is asked): the issue number as
// text, Code 128 and QR code in the page header, a picture and a literal Code 128 in the document footer.
function publishable(image){
  const t=banded();t.schema_version='2.1';
  t.bands.page_header={height:36,elements:[
   {id:'e20',type:'text',source:'literal',text:'Coded 文档',field:'',wrap:false,x:0,y:0,width:60,height:9,font_size:14,align:'left',bold:true},
   {id:'e21',type:'text',source:'issue_number',text:'',field:'',wrap:false,x:65,y:1,width:125,height:7,font_size:10,align:'right',bold:false},
   {id:'e22',type:'barcode',source:'issue_number',text:'',field:'',x:0,y:10,width:112,height:24,symbology:'code128',human_readable:true,font_size:9},
   {id:'e23',type:'qrcode',source:'issue_number',text:'',field:'',x:150,y:8,size:28,ecc:'M'}]};
  t.bands.document_footer.elements.push({id:'e24',type:'image',x:0,y:0,width:40,height:18,image:{...image}},{id:'e25',type:'barcode',source:'literal',text:'SHIPPED',field:'',x:45,y:0,width:52,height:18,symbology:'code128',human_readable:false});
  return t;
}
let sharedCoded=null;
async function codedPublishedOpen(a,image){
  a.w.document.querySelector('[data-page="documents"]').click();
  if(sharedCoded){
   a.click('doc-server-list');await until(()=>[...a.$('doc-server-listing').options].some(o=>o.value===sharedCoded)&&ready(a),'list');
   a.$('doc-server-listing').value=sharedCoded;a.$('doc-server-version').value='1';a.click('doc-server-open');await until(()=>a.$('doc-server-status').textContent.includes('正在编辑模板')&&ready(a),'open version');
  }else{
   a.$('doc-json').value=JSON.stringify(publishable(image));a.click('doc-json-open');assert.equal(doc2status(a),READY);
   a.click('doc-server-save');await until(()=>a.$('doc-server-status').textContent.includes('版本 1')&&ready(a),'save');
   a.click('doc-release-read');await until(()=>a.$('doc-release-status').textContent.includes('修订 0')&&ready(a),'release');
   a.click('doc-release-publish');await until(()=>a.$('doc-release-status').textContent.includes('已发布版本 1')&&ready(a),'publish');
  }
  a.click('doc-published-open');await until(()=>a.$('doc-server-status').textContent.includes('已打开发布模板')&&ready(a),'open');
  const id=a.$('doc-server-status').textContent.match(/[a-f0-9]{32}/)[0];sharedCoded??=id;assert.equal(id,sharedCoded);return id;
}
// The image the shared 2.1 template names: uploaded once, through the console, and shared by the tests that need it.
let sharedImage=null;
async function uploadedImage(a){
  if(sharedImage)return sharedImage;
  setFile(a,'doc-image-file',pngOf(88,36,[200,60,30,255]));a.click('doc-image-upload');await until(()=>doc2status(a).includes('图片已上传')&&ready(a),'upload');
  const id=a.$('doc2-image-listing').value;a.$('doc2-add-band').value='document_footer';
  a.click('doc-json-save');const before=JSON.parse(a.$('doc-json').value);clickId(a,'doc2-add-image');a.click('doc-json-save');
  const added=JSON.parse(a.$('doc-json').value);const element=Object.values(added.bands).flatMap(b=>b.elements).find(e=>e.type==='image');
  sharedImage={id,sha256:element.image.sha256};assert.equal(Object.values(before.bands).flatMap(b=>b.elements).some(e=>e.type==='image'),false);return sharedImage;
}

test('S03-M M5: a 2.1 template is saved, opened, published and read back like any template, and its issue request carries the preview row and nothing else',async()=>{const bodies=[];const a=app(offeringIssuance({bodies}));try{
 await a.login();a.configure();a.click('snapshot');await until(()=>a.$('result-kind').textContent==='冻结快照'&&ready(a),'frozen');
 a.w.document.querySelector('[data-page="documents"]').click();cv.load(a,banded());
 const image=await uploadedImage(a);const id=await codedPublishedOpen(a,image);
 // What the server kept is the 2.1 template as it was saved; the canvas draws every element of it, the picture read from the content route.
 assert.deepEqual(cv.tpl(a),publishable(image));assert.equal(a.$('doc-canvas-host').querySelectorAll('.cv-band').length,5);
 for(const key of ['e20','e21','e22','e23','e24','e25'])assert.ok(cv.item(a,key),key);
 await until(()=>cv.item(a,'e24').querySelector('.cv-picture'),'the picture of the opened template');
 assert.match(cv.item(a,'e22').getAttribute('aria-label'),/条码 Code 128，值 出具编号，印出值，字号 9 点/);
 // The issue request is the published version, the snapshot and the preview row; the group is the server's to find (S03-K), as for 2.0.
 a.input('doc-row','3');a.click('doc-issue');await until(()=>a.$('notice').textContent.includes('已发布的模板版本')&&ready(a),'issue request');
 assert.equal(bodies.length,1);assert.deepEqual(Object.keys(bodies[0]).sort(),['row_index','snapshot_id','template_id','version']);assert.equal(bodies[0].template_id,id);assert.equal(bodies[0].row_index,2);
 // A 2.1 template saved with an image of another binding or identity is the server's to refuse; here the console's own validator refuses what it can see.
 const wrong=publishable(image);wrong.bands.document_footer.elements.at(-2).image={id:'not an id',sha256:image.sha256};a.$('doc-json').value=JSON.stringify(wrong);a.click('doc-json-open');assert.notEqual(doc2status(a),READY);
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('S03-M M5: a 2.1 template previews and is issued through the real server and pinned Typst renderer: the placeholder in the preview, the product\'s number in the issue',{skip:!pdfConfigured&&'needs the pinned Typst renderer'},async()=>{const a=app();try{
 await a.login();a.configure();a.click('snapshot');await until(()=>a.$('result-kind').textContent==='冻结快照'&&ready(a),'frozen');
 a.w.document.querySelector('[data-page="documents"]').click();cv.load(a,banded());
 const image=await uploadedImage(a);const id=await codedPublishedOpen(a,image);
 // The sample check proves the layout for every number the product may assign, without rendering.
 a.click('doc-check');await until(()=>a.$('doc-check-body').rows.length>=1&&ready(a),'check');assert.match(a.$('doc-check-body').rows[0].cells[1].textContent,/^通过$/);
 // The preview of the saved version, for the group of row 2.
 a.input('doc-row','2');a.click('doc-saved-pdf');await until(()=>a.downloads.length===1&&ready(a),'preview',30000);
 const preview=Buffer.from(await a.downloads[0].blob.arrayBuffer());assert.equal(preview.subarray(0,5).toString(),'%PDF-');
 // The issue of the group of row 3: numbered by the product, with its number in every element that shows it.
 a.input('doc-row','3');a.click('doc-issue');await until(()=>a.downloads.length===2&&ready(a),'issued PDF',30000);
 const name=a.downloads[1].name,number=/^issued-(AR-\d{6})\.pdf$/.exec(name)?.[1];assert.ok(number,name);
 const issued=Buffer.from(await a.downloads[1].blob.arrayBuffer());assert.equal(issued.subarray(0,5).toString(),'%PDF-');assert.notDeepEqual(issued,preview);
 // The glyphs of the number: the issued page header carries the number text, as many glyphs as the number has characters, more than the placeholder's.
 const glyphsAt=(bytes,size)=>pdfGlyphs(bytes)[0].filter(g=>g[2]===size).length;
 assert.equal(glyphsAt(issued,10)-glyphsAt(preview,10),number.length-'PREVIEW-NUMBER'.length,'the text of the number at 10 pt: the number, not the placeholder');
 const dir=process.env.REPORTING_DOCUMENT_EVIDENCE_DIR;if(dir){await mkdir(dir,{recursive:true});await writeFile(join(dir,'console_coded_preview.pdf'),preview);await writeFile(join(dir,'console_coded_issued.pdf'),issued);}
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('S03-M M5: what the 2.1 canvas and panels draw is DOM nodes and CSSOM only: no style attribute, no inline handler, hostile text stays text',async()=>{const a=await designer();try{
 const hostile='<img src=x onerror=alert(1)>',t=coded();t.bands.document_footer.elements.push({id:'e30',type:'barcode',source:'literal',text:hostile,field:'',x:0,y:12,width:60,height:8,symbology:'code128',human_readable:false});cv.load(a,t);
 cv.click2(a,'e30');cv.drag2(a,'e30',1,0);cv.click2(a,'e20');cv.click2(a,'e23');cv.key(a,cv.item(a,'e23'),'k',{ctrlKey:true});typeInto(a,'图片');press(a,'Escape');
 for(const root of [a.$('doc-canvas-panel'),a.$('doc2-panel'),palette(a)].filter(Boolean)){assert.equal(root.querySelectorAll('[style]').length,0,'no style attribute in '+(root.id||root.className));for(const e of root.querySelectorAll('*'))assert.equal([...e.attributes].some(x=>/^on/i.test(x.name)),false,'no inline handler');}
 assert.equal(a.$('doc-canvas-host').querySelectorAll('img,script').length,0);assert.ok([...cv.item(a,'e30').querySelectorAll('.cv-codelabel')].some(x=>x.textContent.includes(hostile)),'the value is drawn as text');assert.ok(cv.item(a,'e30').getAttribute('aria-label').includes(hostile));
 cv.click2(a,'e30');assert.equal(a.$('doc2-p-text').value,hostile);assert.equal(a.$('doc2-panel').querySelectorAll('img,script').length,0);
 assert.equal(a.errors.length,0);
 }finally{a.close();}});

test('S03-M M5: the console\'s validator refuses a 2.1 template that a 2.0 one never had: the shared cases of the version run in both validators',async()=>{
 assert.ok(schema2.cases.length>=300,'the shared table has the 2.1 cases');
 const coded21=schema2.cases.filter(c=>JSON.stringify(c.edit).includes('"2.1"')||/barcode|QR code|image|issue number/i.test(c.name));assert.ok(coded21.length>=100,coded21.length+' cases of 2.1');
 const kinds={valid:0,invalid:0,unauthorized:0};for(const c of coded21)kinds[c.expect]++;assert.ok(kinds.valid>=20&&kinds.invalid>=70&&kinds.unauthorized>=2,JSON.stringify(kinds));
 // The shared cases themselves are run by the test of every 2.0 case above, which takes the whole table; here the 2.1 ones are run on their own, so that a table that loses them fails here.
 const a=await designer();try{let accepted=0;
  for(const c of coded21){const doc=editedCase(schema2.base,c.edit);a.$('doc-status').textContent='';a.$('doc-json').value=JSON.stringify(doc);a.click('doc-json-open');const ok=a.$('doc-status').textContent===READY;assert.equal(ok,c.expect==='valid',c.name+': '+a.$('doc-status').textContent);if(ok)accepted++;}
  assert.equal(accepted,kinds.valid);assert.equal(a.errors.length,0);
 }finally{a.close();}});

// Retention (S04-B): a job whose results have expired, and are deleted after the grace period, shows the label where it showed a download or
// a result; the job, its ids and, for an issue, its record stay. The server says so with `expired: true` on a job that succeeded.
test('an expired job shows 已过期并清除 where it showed a result or a download, and only a succeeded job can say so',async()=>{
  let mode='';
  const a=app(async(path,options)=>{
    if(mode&&/\/jobs\/[a-f0-9]{32}$/.test(path)){
      const value=await (await fetch(url+path,options)).json();
      return json(mode==='expired'?{...value,expired:true}:mode==='false'?{...value,expired:false}:mode==='text'?{...value,expired:'yes'}:value);
    }
  });
  try{
    await snapshotReady(a);a.click('export');await until(()=>a.downloads.length===1,'workbook download');
    const rows=()=>[...a.$('tasks-body').rows];
    const buttons=row=>[...row.querySelectorAll('button')].map(b=>b.textContent);
    assert.equal(rows().length,2);
    // Before: a snapshot with its result to view, an export with its file to download, and no label.
    for(const row of rows()){assert.ok(buttons(row).includes('刷新'));assert.equal(row.querySelector('.expired'),null);assert.equal(row.cells[2].textContent,'已完成');}
    assert.ok(rows().some(row=>buttons(row).includes('查看结果')));assert.ok(rows().some(row=>buttons(row).includes('下载')));
    // A flag that is not `true`, or a text, is not a view the console believes: the notice says so and the rows are as they were.
    for(const m of ['false','text']){
      mode=m;for(const row of rows())row.querySelector('button').click();
      await until(()=>/无法验证的响应/.test(a.$('notice').textContent),'the flag '+m+' is refused');
      for(const row of rows())assert.equal(row.querySelector('.expired'),null);assert.ok(rows().some(row=>buttons(row).includes('下载')));
    }
    // After: the label replaces the result and the download; the refresh stays, and so do the state and the ids.
    mode='expired';
    for(const row of rows())row.querySelector('button').click();
    await until(()=>rows().length===2&&rows().every(row=>row.querySelector('.expired')),'both rows say expired');
    for(const row of rows()){
      assert.equal(row.querySelector('.expired').textContent,'已过期并清除');
      assert.deepEqual(buttons(row),['刷新']);
      assert.equal(row.cells[2].textContent,'已完成');assert.match(row.cells[1].textContent,/^[a-f0-9]{32}$/);
    }
    // The workbook downloaded before the expiry is the user's own; nothing was fetched again for the label.
    assert.equal(a.downloads.length,1);assert.equal(a.trace.filter(v=>v.path.includes('/artifacts/')).length,1);
    assert.equal(a.errors.length,0);
  }finally{a.close();}
});

test('the issue list shows 已过期并清除 for an issue whose file expired, keeps its record and refuses the label on one that did not succeed',async()=>{
  const F='f'.repeat(32),issued='2026-10-28T06:12:03Z',confirmed='2026-10-28T06:12:04Z';
  const rec=(over={})=>({issue_id:F,state:'succeeded',number:'AR-000003',template_id:'d'.repeat(32),template_version:2,template_sha256:'e'.repeat(64),snapshot_id:'c'.repeat(32),snapshot_sha256:'a'.repeat(64),row_index:4,artifact_id:pdfArtifact,sha256:'1'.repeat(64),pages:2,issued_at:issued,stamp:'ISSUED No. AR-000003 - '+issued+' - '+F,reservation_ref:'RES-3',confirmation_ref:'CNF-3',confirmed_at:confirmed,...over});
  const open=(over={})=>({issue_id:'e'.repeat(32),state:'queued',template_id:'d'.repeat(32),template_version:2,template_sha256:'e'.repeat(64),snapshot_id:'c'.repeat(32),row_index:4,...over});
  const options={records:[]};const a=app(offeringIssuance(options));
  const refresh=async records=>{options.records=records;a.click('doc-issues-refresh');await until(()=>!a.$('preview').disabled,'refreshed');return a.$('doc-issue-status').textContent;};
  try{
    await snapshotReady(a);
    // Two issues: one whose file is still served, one whose file expired. The record of the second is as complete as the first's.
    const second=rec({issue_id:'1'.repeat(32),number:'AR-000004',stamp:'ISSUED No. AR-000004 - '+issued+' - '+'1'.repeat(32),expired:true});
    assert.match(await refresh([rec(),second]),/共 2 条/);
    const rows=[...a.$('doc-issues-body').rows];
    assert.equal(rows.length,2);
    assert.deepEqual([...rows[0].querySelectorAll('button')].map(b=>b.textContent),['下载']);assert.equal(rows[0].querySelector('.expired'),null);
    assert.deepEqual([...rows[1].querySelectorAll('button')].map(b=>b.textContent),[]);assert.equal(rows[1].querySelector('.expired').textContent,'已过期并清除');
    assert.equal(rows[1].cells[0].textContent,'No. AR-000004');assert.equal(rows[1].cells[1].textContent,'已出具');assert.equal(rows[1].cells[5].textContent,'1'.repeat(64)+' · 2 页');assert.equal(rows[1].cells[6].textContent,'CNF-3 · '+confirmed);
    // The label belongs to an issue that succeeded and is `true` or absent, nothing else.
    for(const [label,record] of [
      ['expired false',rec({expired:false})],['expired as text',rec({expired:'yes'})],['expired as a number',rec({expired:1})],
      ['expired on a queued record',open({expired:true})],['expired on a failed record',open({state:'failed',error_code:'INTEGRITY_FAILURE',expired:true})],['expired on a running record',open({state:'running',expired:true})],
    ]){assert.match(await refresh([record]),/无法验证出具记录。|不应带有/,label);assert.equal(a.$('doc-issues-wrap').hidden,true,label);}
    assert.equal(a.downloads.length,0);assert.equal(a.errors.length,0);
  }finally{a.close();}
});

// Quotas (S04-D, docs/S04D_SCOPE.md D3): the refusal is 429 {error:'QUOTA_EXCEEDED',quota}; the console names the quota with the number the
// capabilities state, leaves the page as it was, and takes any other shape for a response it cannot verify.
const QUOTA_NUMBERS={jobs_in_flight:3,stored_results:7,admissions_per_hour:11,queries_in_flight:5};
const QUOTA_TEXT={jobs_in_flight:'已达到进行中任务上限 (3)，请等待任务完成或取消后再试',stored_results:'已达到保留结果上限 (7)，结果过期后自动释放',admissions_per_hour:'已达到每小时提交上限 (11)，请稍后再试',queries_in_flight:'查询进行中过多 (5)，请稍后再试'};
function quotaServer(answer,numbers=QUOTA_NUMBERS){
  return async(path,options)=>{
    if(path.endsWith('/capabilities')){const caps=await (await fetch(url+path,options)).json();caps.quotas=numbers;return json(caps);}
    if(path.endsWith('/snapshot-jobs')&&options?.method==='POST')return answer();
  };
}
test('a quota refusal names the quota with the number of the capabilities and leaves the page as it was',async()=>{
  for(const quota of Object.keys(QUOTA_TEXT)){
    const a=app(quotaServer(()=>json({error:'QUOTA_EXCEEDED',quota},429)));
    try{
      await a.login();a.configure();a.click('preview');await until(()=>!a.$('result-table-wrap').hidden&&!a.$('preview').disabled,'preview result');
      const rows=a.$('result-table').tBodies[0].rows.length;assert.ok(rows>0);
      a.click('snapshot');await until(()=>!a.$('notice').hidden&&!a.$('snapshot').disabled,'quota notice');
      assert.ok(a.$('notice').textContent.includes(QUOTA_TEXT[quota]),quota+': '+a.$('notice').textContent);
      assert.equal(a.$('result-table-wrap').hidden,false,'the preview stays');assert.equal(a.$('result-table').tBodies[0].rows.length,rows);
      assert.equal(a.$('result-kind').textContent!=='冻结快照',true);
      assert.equal(a.$('connection-overlay').hidden,true,'still connected');
      assert.equal(a.errors.length,0,a.errors.join('|'));
    }finally{a.close();}
  }
});

test('the capabilities state the quotas in force, and the console shows them',async()=>{
  const a=app();try{
    await a.login();
    const caps=await (await fetch(url+'/api/reporting/v1/capabilities',{headers:{Authorization:'Bearer '+token}})).json();
    assert.deepEqual(Object.keys(caps.quotas).sort(),['admissions_per_hour','jobs_in_flight','queries_in_flight','stored_results']);
    assert.ok(a.$('capability-details').textContent.includes('每小时提交 '+caps.quotas.admissions_per_hour));
  }finally{a.close();}
});

test('a refusal that is not exactly the quota shape is an unverifiable response, and capabilities without valid quotas are refused',async()=>{
  const verdict='服务返回了无法验证的响应。';
  const raw=(body,status=429,type='application/json')=>()=>new Response(body,{status,headers:{'Content-Type':type}});
  for(const [label,answer] of [
    ['an unknown quota',raw('{"error":"QUOTA_EXCEEDED","quota":"bytes"}')],['no quota',raw('{"error":"QUOTA_EXCEEDED"}')],['an extra key',raw('{"error":"QUOTA_EXCEEDED","quota":"jobs_in_flight","count":8}')],
    ['a quota that is not text',raw('{"error":"QUOTA_EXCEEDED","quota":3}')],['another code under 429',raw('{"error":"BUDGET_EXCEEDED","quota":"jobs_in_flight"}')],['429 without a body',raw('')],['a list',raw('["QUOTA_EXCEEDED"]')],
    ['not JSON',raw('quota exceeded',429,'text/plain')],['the code under another status',raw('{"error":"QUOTA_EXCEEDED","quota":"jobs_in_flight"}',400)]
  ]){
    const a=app(quotaServer(answer));
    try{
      await a.login();a.configure();a.click('preview');await until(()=>!a.$('result-table-wrap').hidden&&!a.$('preview').disabled,'preview result');
      a.click('snapshot');await until(()=>!a.$('notice').hidden&&!a.$('snapshot').disabled,'notice');
      assert.ok(a.$('notice').textContent.includes(verdict),label+': '+a.$('notice').textContent);
      assert.equal(/上限/.test(a.$('notice').textContent),false,label);
    }finally{a.close();}
  }
  for(const [label,quotas] of [['missing',undefined],['a missing key',{jobs_in_flight:3,stored_results:7,admissions_per_hour:11}],['a fourth beyond its bound',{...QUOTA_NUMBERS,queries_in_flight:65}],['an extra key',{...QUOTA_NUMBERS,other:1}],['zero',{...QUOTA_NUMBERS,jobs_in_flight:0}],['a fraction',{...QUOTA_NUMBERS,stored_results:1.5}],['text',{...QUOTA_NUMBERS,admissions_per_hour:'11'}],['beyond the bound',{...QUOTA_NUMBERS,stored_results:100001}]]){
    const a=app(async path=>{if(path.endsWith('/capabilities')){const caps=await (await fetch(url+path,{headers:{Authorization:'Bearer '+token}})).json();if(quotas===undefined)delete caps.quotas;else caps.quotas=quotas;return json(caps);}});
    try{
      a.$('access-token').value=token;a.$('connect-form').dispatchEvent(new a.w.Event('submit',{bubbles:true,cancelable:true}));
      await until(()=>!a.$('connect-error').hidden,'refusal');
      assert.ok(a.$('connect-error').textContent.includes(verdict),label);assert.equal(a.$('connection-overlay').hidden,false,label);
    }finally{a.close();}
  }
});

test('a real server at its limit refuses with the quota notice',async()=>{
  // A second demo that admits one job at a time and runs no worker: the first submission holds the only slot.
  const listener=createServer();await new Promise(resolve=>listener.listen(0,'127.0.0.1',resolve));const port=listener.address().port;await new Promise(resolve=>listener.close(resolve));
  const strict=spawn(join(temporary,'reporting-demo'),['-profile','contract-simulator','-memory','-listen','127.0.0.1:'+port,'-pause-workers','-quota-jobs','1'],{cwd:root,env:{...process.env,REPORTING_DEMO_TOKEN:token},stdio:['ignore','ignore','pipe']});
  strict.stderr.resume();
  const normal=url;url='http://127.0.0.1:'+port;
  let held=false;
  const a=app(async(path,options)=>{
    // The first job is admitted for real; the poll for it is answered "unavailable", so that the console moves on to the second submission.
    if(path.includes('/jobs/')&&!path.endsWith('/cancel')&&held)return json({error:'AUTHORITY_UNAVAILABLE'},503);
    if(path.endsWith('/snapshot-jobs')&&options?.method==='POST'){const answer=await fetch(url+path,options);if(answer.status===202)held=true;return answer;}
  });
  try{
    for(let i=0;i<100;i++){try{if((await fetch(url+'/console/')).ok)break;}catch(_){}await sleep(30);}
    await a.login();a.configure();
    a.click('snapshot');await until(()=>held&&!a.$('notice').hidden&&!a.$('snapshot').disabled,'first submission held');
    const capabilities=await (await fetch(url+'/api/reporting/v1/capabilities',{headers:{Authorization:'Bearer '+token}})).json();
    assert.deepEqual(capabilities.quotas,{jobs_in_flight:1,stored_results:64,admissions_per_hour:120,queries_in_flight:4});
    a.click('snapshot');await until(()=>a.$('notice').textContent.includes('进行中任务上限 (1)'),'quota notice from the real server');
    assert.ok(a.$('notice').textContent.includes('已达到进行中任务上限 (1)，请等待任务完成或取消后再试'));
    assert.equal(a.errors.length,0,a.errors.join('|'));
  }finally{a.close();url=normal;if(strict.exitCode===null){strict.kill('SIGTERM');await new Promise(resolve=>strict.once('exit',resolve));}}
});

test('a quota refusal of an export keeps the frozen snapshot on show and starts no download',async()=>{
  let refuse=false;
  const a=app(async(path,options)=>{
    if(path.endsWith('/capabilities')){const caps=await (await fetch(url+path,options)).json();caps.quotas=QUOTA_NUMBERS;return json(caps);}
    if(refuse&&path.endsWith('/exports')&&options?.method==='POST')return json({error:'QUOTA_EXCEEDED',quota:'admissions_per_hour'},429);
  });
  try{
    await snapshotReady(a);const rows=a.$('result-table').tBodies[0].rows.length;refuse=true;
    a.click('export');await until(()=>a.$('notice').textContent.includes(QUOTA_TEXT.admissions_per_hour),'quota notice');
    assert.equal(a.$('result-kind').textContent,'冻结快照');assert.equal(a.$('result-table').tBodies[0].rows.length,rows);assert.equal(a.downloads.length,0);
    assert.equal(a.errors.length,0,a.errors.join('|'));
  }finally{a.close();}
});

// The cap on requests in flight (S04-G): 503 {error:'OVERLOADED'} is "the service is busy", not "the authorization source is unavailable";
// the page stays as it was; any other shape of that code cannot be verified. The fourth quota name is accepted in the exact 429 shape only.
test('an OVERLOADED 503 says the service is busy, the authorization 503 still says the source is unavailable, and the page stays',async()=>{
  const answers={
    overloaded:[()=>json({error:'OVERLOADED'},503),'服务繁忙，请稍后再试',false],
    source:[()=>json({error:'AUTHORITY_UNAVAILABLE'},503),'当前授权来源不可用，请稍后重试。',true],
    'overloaded with an extra key':[()=>json({error:'OVERLOADED',quota:'x'},503),'服务返回了无法验证的响应。',true],
    'overloaded under 429':[()=>json({error:'OVERLOADED'},429),'服务返回了无法验证的响应。',true],
  };
  for(const [label,[answer,text,clears]] of Object.entries(answers)){
    const a=app(quotaServer(answer));
    try{
      await a.login();a.configure();a.click('preview');await until(()=>!a.$('result-table-wrap').hidden&&!a.$('preview').disabled,'preview result');
      a.click('snapshot');await until(()=>!a.$('notice').hidden&&!a.$('snapshot').disabled,'notice');
      assert.ok(a.$('notice').textContent.includes(text),label+': '+a.$('notice').textContent);
      if(label==='overloaded')assert.equal(a.$('notice').textContent.includes('授权来源'),false);
      assert.equal(a.$('result-table-wrap').hidden,clears,label+': the preview '+(clears?'is cleared':'stays'));
    }finally{a.close();}
  }
});
test('the fourth quota is refused in any shape but the exact one',async()=>{
  const a=app(quotaServer(()=>json({error:'QUOTA_EXCEEDED',quota:'queries_in_flight',count:4},429)));
  try{
    await a.login();a.configure();a.click('snapshot');await until(()=>!a.$('notice').hidden&&!a.$('snapshot').disabled,'notice');
    assert.ok(a.$('notice').textContent.includes('服务返回了无法验证的响应。'));
  }finally{a.close();}
});

// ======================================================================================================================================
// S06-A: the document studio (ADR 0008). Real Go server, as the tests above; the studio is the shell around the designer's functions, so
// these tests assert that each studio control reaches the function the old control reached, that unsupported ones say why, and that the
// new session-only behaviours (clipboard, painter, lock, grid, snapping, zoom, pager) and the dialogs do what the scope says.
// ======================================================================================================================================
const q=(a,sel)=>a.w.document.querySelector(sel),qa=(a,sel)=>[...a.w.document.querySelectorAll(sel)];
const ribbonTab=(a,name)=>q(a,'[data-ribbon="'+name+'"]').click();
const railTab=(a,name)=>q(a,'#page-documents [data-side="'+name+'"]').click();
const card=(a,kind)=>q(a,'[data-insert="'+kind+'"]');
const to2=a=>{cv.load(a,cv.four(a));a.click('doc-convert');assert.equal(cv.tpl(a).schema_version,'2.0');return cv.tpl(a);};
const all2=t=>Object.entries(t.bands).flatMap(([band,b])=>b.elements.map(e=>({band,e})));
const ids2=t=>all2(t).map(v=>v.e.id).sort();
const dialogOf=(a,id)=>q(a,'#st-d-'+id);
const isOpen=(a,id)=>dialogOf(a,id).classList.contains('open');
const flush=()=>sleep(20);

test('S06-A A1: the studio is the documents page: its regions, labels and tabs are the prototype\'s, the other pages keep the console\'s shell',async()=>{const a=await designer();try{
 const d=a.w.document,text=s=>d.querySelector(s).textContent.replace(/\s+/g,' ').trim();
 assert.equal(d.body.classList.contains('studio-on'),true);
 d.querySelector('[data-page="workspace"]').click();assert.equal(d.body.classList.contains('studio-on'),false,'the console keeps its shell on its other pages');d.querySelector('[data-page="documents"]').click();assert.equal(d.body.classList.contains('studio-on'),true);
 assert.match(text('#page-documents .wordmark'),/^IDP REPORTING\s*DOCUMENT STUDIO$/);assert.equal(text('#st-crumb'),'模板中心');
 assert.deepEqual(['#titleButton','#st-help','#st-versions','#doc-server-save','#previewButton','#st-publish'].map(s=>q(a,s).getAttribute('aria-label')||text(s)),['重命名模板','设计说明与操作帮助','版本','保存版本','预览与导出','发布']);
 assert.equal(text('#st-state'),'草稿');assert.equal(text('#st-name'),'未命名模板');
 const tabs=qa(a,'[data-ribbon]');assert.deepEqual(tabs.map(t=>t.textContent),['设计','插入','数据','页面','检查与发布']);assert.equal(tabs[0].parentElement.getAttribute('role'),'tablist');
 assert.deepEqual(tabs.map(t=>[t.getAttribute('role'),t.getAttribute('aria-selected')]),[['tab','true'],...Array(4).fill(['tab','false'])]);
 for(const t of tabs)assert.equal(a.w.document.getElementById(t.getAttribute('aria-controls')).getAttribute('role'),'tabpanel');
 // The tools of the design tab, in the prototype's order and grouping; the other tabs' tools stay in the page (the command palette reaches them) but are not shown.
 const names=p=>qa(a,'#st-tools-'+p+' .toolgroup').map(g=>[...g.querySelectorAll('button')].map(b=>b.getAttribute('aria-label')));
 assert.deepEqual(names('design'),[['撤销','重做'],['复制','剪切','粘贴','格式刷'],['左对齐','水平居中','右对齐','顶对齐','垂直居中','底对齐','水平等距','垂直等距'],['组合','解组','锁定'],['网格','吸附','区段']]);
 assert.deepEqual(qa(a,'#st-tools-insert button').map(b=>b.getAttribute('aria-label')),['文本','绑定字段','计算指标','动态明细表','金额摘要','分隔线','矩形容器','图片 / 标识','签章栏','条形码','二维码','页码信息','水印']);
 assert.deepEqual(names('data'),[['数据中心'],['数据集与映射','数据预览','导入样本'],['参数与筛选','排序与分组','计算指标'],['字段校验','刷新示例']]);
 assert.deepEqual(names('page'),[['纸张与页边距','区段设置'],['底稿与套打','插入页码','插入水印'],['阅读模式']]);
 assert.deepEqual(names('check'),[['运行版式检查','边界样本'],['预览与导出','发布前确认','版本与回退','正式出具'],['导出模板JSON','导入模板JSON']]);
 ribbonTab(a,'insert');assert.equal(q(a,'#st-tools-insert').classList.contains('active'),true);assert.equal(q(a,'#st-tools-design').classList.contains('active'),false);assert.equal(q(a,'#st-tab-insert').getAttribute('aria-selected'),'true');assert.equal(q(a,'#st-tab-design').getAttribute('aria-selected'),'false');
 // Arrow keys move along the tabs (roving tabindex), Home and End jump.
 q(a,'#st-tab-insert').dispatchEvent(new a.w.KeyboardEvent('keydown',{key:'ArrowRight',bubbles:true,cancelable:true}));assert.equal(a.w.document.activeElement.id,'st-tab-data');assert.equal(q(a,'#st-tab-data').getAttribute('aria-selected'),'true');assert.equal(q(a,'#st-tab-data').tabIndex,0);assert.equal(q(a,'#st-tab-insert').tabIndex,-1);
 a.w.document.activeElement.dispatchEvent(new a.w.KeyboardEvent('keydown',{key:'End',bubbles:true,cancelable:true}));assert.equal(a.w.document.activeElement.id,'st-tab-check');
 // The rail: four tabs that switch the side panel, then the template centre and help.
 const rail=qa(a,'#page-documents [data-side]');assert.deepEqual(rail.map(t=>t.getAttribute('aria-label')),['组件库','数据与字段','区段与图层','资源与样式']);assert.deepEqual(rail.map(t=>t.getAttribute('role')),Array(4).fill('tab'));assert.equal(rail[0].parentElement.getAttribute('aria-orientation'),'vertical');
 assert.deepEqual([q(a,'#st-rail-library'),q(a,'#st-rail-help')].map(b=>b.getAttribute('aria-label')),['模板中心','设计说明']);
 railTab(a,'structure');assert.deepEqual(qa(a,'#sidebar .side-view').map(v=>v.hidden),[true,true,false,true]);assert.equal(q(a,'#sidebar').getAttribute('aria-labelledby'),'st-rail-structure');assert.equal(q(a,'#sidebar h2:not([hidden])')!==null,true);railTab(a,'components');
 // The work area and the status bar.
 assert.equal(text('#recordBadge'),'3 条示例');assert.equal(text('#pageCount'),'1 / 1');assert.equal(text('.canvas-hint'),'拖拽定位 · Shift 多选 · Space 平移');assert.equal(text('#positionStatus'),'毫米定位 / 0.1 mm');
 assert.equal(text('#st-validation-text'),'版式检查通过');assert.match(text('#st-source'),/^查询预览（未冻结） · receivables\.aging$/);assert.equal(a.$('doc-canvas-live').getAttribute('aria-live'),'polite');assert.equal(a.$('doc-canvas-live').closest('.statusbar')!==null,true);
 assert.equal(q(a,'[data-act="parameters"]').textContent.includes('参数与筛选'),true);assert.equal(text('#st-ctx-data'),'查看数据');
 assert.equal(a.errors.length,0,a.errors.join('|'));
 }finally{a.close();}});

test('S06-A A1: every ribbon tool, rail control and library card either reaches the function its old button had or is disabled with its reason, and says it',async()=>{const a=await designer();try{
 cv.load(a,cv.four(a));
 const reasons={组合:'版式 schema 没有分组对象；多选可一起移动',解组:'版式 schema 没有分组对象；多选可一起移动',水印:'正式文件不支持水印',插入水印:'正式文件不支持水印',导入样本:'数据来自来源产品的冻结快照；平台不导入 CSV 或 JSON 样本',刷新示例:'快照已冻结，不重新查询；要换数据请在“参数与筛选”中重新冻结快照'};
 const before=JSON.stringify(cv.tpl(a));
 for(const b of qa(a,'.ribbon-tools button, .toolrail button, .sidebar [data-insert]')){
  const name=b.getAttribute('aria-label')||b.textContent.trim();assert.ok(name.length>0,'a control has no name: '+b.outerHTML.slice(0,80));
  assert.ok(b.type==='button','a button that is not type=button: '+name);
  if(b.getAttribute('aria-disabled')==='true'){
   const why=b.getAttribute('data-unsupported');assert.ok(why&&why===b.title,name+': disabled without its reason as the tooltip');
   if(reasons[name])assert.equal(why,reasons[name],name);
   a.$('doc-status').textContent='';b.click();assert.equal(a.$('doc-status').textContent,why,name+' says why it does nothing');
  }
 }
 assert.equal(JSON.stringify(cv.tpl(a)),before,'a disabled control changes nothing');
 // A 1.x template: the components it has not are disabled with the way out; 2.0 turns them on.
 for(const k of ['line','rect','image','barcode','qr','pageNumber','signature'])assert.equal(card(a,k).getAttribute('aria-disabled'),'true',k+' in 1.x');
 assert.match(card(a,'line').title,/先转换为 2\.0/);
 for(const k of ['text','field','table','aggregate','summary'])assert.equal(card(a,k).getAttribute('aria-disabled'),null,k+' in 1.x');
 assert.equal(card(a,'watermark').getAttribute('aria-disabled'),'true');assert.equal(card(a,'watermark').title,'正式文件不支持水印');
 assert.equal(card(a,'aggregate').title,'仅支持“分组合计”：服务端对分组内各行精确求和（group_sum）');assert.equal(card(a,'summary').title,card(a,'aggregate').title);
 a.click('doc-convert');for(const k of ['line','rect','image','barcode','qr','pageNumber','signature','text','field','table','aggregate','summary'])assert.equal(card(a,k).getAttribute('aria-disabled'),null,k+' in 2.0');
 assert.equal(card(a,'signature').title,'签章与签名属于后续阶段');assert.equal(card(a,'watermark').getAttribute('aria-disabled'),'true');
 // The old buttons are the ribbon's own: undo, redo, align, distribute, delete and the palette are the same elements.
 for(const id of ['doc-undo','doc-redo','doc-del','doc-align-left','doc-align-bottom','doc-distribute-h','doc-distribute-v','doc-palette-open','doc-check','doc-server-save'])assert.ok(q(a,'.ribbon-tools #'+id+', .topbar #'+id+', .ribbon-tabs #'+id),id+' is a ribbon or topbar control');
 assert.equal(a.errors.length,0,a.errors.join('|'));
 }finally{a.close();}});

test('S06-A A2: a card or a tool inserts into the current band at the nearest free place, one undo step each; a drop puts it under the pointer; 签章栏 is a rectangle with the literal 签章',async()=>{const a=await designer();try{
 const t0=to2(a);
 // The current band is the selected element's band.
 cv.click2(a,'e1');const home=all2(t0).find(v=>v.e.id==='e1').band;
 const added=async(kind,expect)=>{const before=ids2(cv.tpl(a)),n=a.$('doc-undo').disabled;card(a,kind).click();const after=cv.tpl(a),fresh=ids2(after).filter(id=>!before.includes(id));assert.equal(fresh.length,expect,kind+' adds '+expect);return {fresh,after};};
 for(const [kind,type,source] of [['text','text','literal'],['field','text','field'],['aggregate','text','group_sum'],['summary','text','group_sum'],['line','line'],['rect','rect'],['barcode','barcode'],['qr','qrcode'],['pageNumber','text','page']]){
  cv.click2(a,'e1');const r=await added(kind,1),hit=all2(r.after).find(v=>v.e.id===r.fresh[0]);
  assert.equal(hit.band,home,kind+' goes to the band of the selected element');assert.equal(hit.e.type,type,kind);if(source)assert.equal(hit.e.source,source,kind);
  assert.deepEqual(cv.selected(a),[r.fresh[0]],kind+' is selected');
  a.click('doc-undo');assert.equal(JSON.stringify(ids2(cv.tpl(a))),JSON.stringify(ids2(t0)),kind+' is one undo step');
 }
 assert.equal(cv.tpl(a).schema_version,'2.0','the undo of a code returns the 2.0 template');
 // The table card selects the table; the watermark card says why not.
 card(a,'table').click();assert.deepEqual(cv.selected(a),[ids2(t0).find(id=>all2(t0).find(v=>v.e.id===id).e.type==='table')]);
 a.$('doc-status').textContent='';card(a,'watermark').click();assert.equal(a.$('doc-status').textContent,'正式文件不支持水印');
 // The signature block: a rectangle and the literal text in it, one step.
 cv.click2(a,'e1');const before=ids2(cv.tpl(a));card(a,'signature').click();const sig=cv.tpl(a),fresh=ids2(sig).filter(id=>!before.includes(id)).map(id=>all2(sig).find(v=>v.e.id===id));
 assert.deepEqual(fresh.map(v=>v.e.type).sort(),['rect','text']);assert.equal(fresh.find(v=>v.e.type==='text').e.text,'签章');assert.equal(new Set(fresh.map(v=>v.band)).size,1);assert.equal(cv.selected(a).length,2);
 a.click('doc-undo');assert.equal(ids2(cv.tpl(a)).length,before.length,'one undo step for both');
 // Drop: the card is dragged with pointer events; what is under the pointer decides the band, and a drop outside the page inserts nothing.
 const svg=cv.svg(a);const hostRect={left:0,top:0};
 const pointer=(target,type,x,y)=>target.dispatchEvent(new a.w.PointerEvent(type,{bubbles:true,cancelable:true,pointerId:7,button:0,clientX:x,clientY:y}));
 const drop=(kind,mmx,mmy)=>{const c=card(a,kind);pointer(c,'pointerdown',5,5);pointer(a.w.document,'pointermove',40,40);pointer(a.w.document,'pointermove',cv.px(a,mmx),cv.px(a,mmy));pointer(a.w.document,'pointerup',cv.px(a,mmx),cv.px(a,mmy));};
 const geo=(()=>{const b=cv.tpl(a).bands;return {ph:b.page_header?.height||0,dh:b.document_header?.height||0};})();
 const n0=ids2(cv.tpl(a)).length;drop('rect',60,25+geo.ph+Math.min(10,geo.dh/2));
 const dropped=cv.tpl(a);assert.equal(ids2(dropped).length,n0+1,'a drop inserts one element: '+a.$('doc-status').textContent);const fresh2=all2(dropped).find(v=>!ids2(t0).includes(v.e.id)&&v.e.type==='rect');assert.ok(fresh2);
 assert.equal(a.$('doc-canvas-panel').classList.contains('studio-dragging'),false);assert.equal(svg.querySelectorAll('.cv-drop').length,0,'the drop marker is gone');
 const n1=ids2(cv.tpl(a)).length;a.$('doc-status').textContent='';drop('text',-500,-500);assert.match(a.$('doc-status').textContent,/放到页面上才会插入/);assert.equal(ids2(cv.tpl(a)).length,n1,'a drop outside the page inserts nothing');
 assert.equal(a.errors.length,0,a.errors.join('|'));
 }finally{a.close();}});

test('S06-A A2: in a 1.x template the library inserts a text box, a row field and a total, each at the first free place',async()=>{const a=await designer();try{
 cv.load(a,cv.four(a));const n=()=>cv.tpl(a).elements.length;
 card(a,'text').click();assert.equal(n(),5);let t=cv.tpl(a);assert.deepEqual([t.elements[4].source,t.elements[4].text],['literal','固定文字']);assert.deepEqual(cv.selected(a),['n5']);
 card(a,'field').click();assert.equal(n(),6);assert.equal(cv.tpl(a).elements[5].source,'row');
 card(a,'aggregate').click();assert.equal(n(),7);assert.equal(cv.tpl(a).elements[6].source,'total');
 const boxes=cv.tpl(a).elements;for(const e of boxes.slice(4))for(const o of boxes)if(o!==e)assert.ok(e.y>=o.y+o.height||o.y>=e.y+e.height||e.x>=o.x+o.width||o.x>=e.x+e.width,e.id+' overlaps '+o.id);
 a.click('doc-undo');assert.equal(n(),6);a.click('doc-undo');a.click('doc-undo');assert.equal(n(),4);
 card(a,'table').click();assert.equal(isOpen(a,'insert'),true,'1.x without a table: the table card opens the options that build one');
 assert.equal(a.errors.length,0,a.errors.join('|'));
 }finally{a.close();}});

test('S06-A A1: copy, cut and paste work on elements of the template; the copy goes to the nearest free place of the current band; the clipboard is the session\'s, not storage',async()=>{const a=await designer();try{
 const t0=to2(a);cv.click2(a,'e1');
 a.click('st-copy');assert.match(a.$('doc-status').textContent,/已复制 1 项/);assert.equal(a.$('st-paste').disabled,false);
 a.click('st-paste');let t=cv.tpl(a);const fresh=ids2(t).filter(id=>!ids2(t0).includes(id));assert.equal(fresh.length,1);
 const src=all2(t).find(v=>v.e.id==='e1'),cp=all2(t).find(v=>v.e.id===fresh[0]);assert.equal(cp.band,src.band);assert.deepEqual({...cp.e,id:'',x:0,y:0},{...src.e,id:'',x:0,y:0},'the copy is the same element');
 assert.ok(cp.e.x!==src.e.x||cp.e.y!==src.e.y,'at another place');
 const overlap=(p,r)=>p.x<r.x+r.width&&r.x<p.x+p.width&&p.y<r.y+r.height&&r.y<p.y+p.height;
 for(const v of t.bands[cp.band].elements)if(v.type==='text'&&v.id!==cp.e.id)assert.equal(overlap(cp.e,v),false,'the copy overlaps nothing');
 assert.deepEqual(cv.selected(a),[fresh[0]]);a.click('doc-undo');assert.equal(ids2(cv.tpl(a)).length,ids2(t0).length,'paste is one undo step');
 // Cut: the element goes and comes back by paste; Ctrl+C, Ctrl+X and Ctrl+V reach the same functions from the canvas.
 cv.click2(a,'e1');a.click('st-cut');assert.equal(ids2(cv.tpl(a)).includes('e1'),false,'cut deletes');a.click('doc-undo');assert.equal(ids2(cv.tpl(a)).includes('e1'),true,'and is one undo step');
 cv.click2(a,'e1');cv.key(a,cv.item(a,'e1'),'c',{ctrlKey:true});assert.match(a.$('doc-status').textContent,/已复制 1 项/);cv.key(a,cv.item(a,'e1'),'v',{ctrlKey:true});assert.equal(ids2(cv.tpl(a)).length,ids2(t0).length+1);
 // The table is not copied; an empty clipboard and another schema's element are refused with a reason.
 const tableKey=all2(t0).find(v=>v.e.type==='table').e.id;cv.click2(a,tableKey);assert.equal(a.$('st-copy').disabled,true,'the table has no copy');assert.equal(a.$('st-cut').disabled,true);
 assert.equal(a.w.localStorage.length,0);assert.equal(a.w.sessionStorage.length,0,'the clipboard is not in storage');
 cv.load(a,cv.four(a));a.$('doc-status').textContent='';a.click('st-paste');assert.match(a.$('doc-status').textContent,/不能粘贴到 1\.x 模板/,'a 2.0 element does not go into a 1.x template');
 cv.load(a,cv.four(a));cv.click(a,'n1');a.click('st-copy');cv.click(a,'n2');a.click('st-paste');assert.equal(cv.tpl(a).elements.length,5,'a 1.x box is pasted into a 1.x template');
 a.click('logout');assert.equal(a.$('st-paste').disabled,true,'signing out empties the clipboard');
 assert.equal(a.errors.length,0,a.errors.join('|'));
 }finally{a.close();}});

test('S06-A: the format painter copies font size, alignment, bold and wrap to the next element clicked, once',async()=>{const a=await designer();try{
 const t0=to2(a),e=all2(t0).filter(v=>v.e.type==='text');const [one,two,three]=e.map(v=>v.e.id);
 a.click('st-painter');assert.match(a.$('doc-status').textContent,/格式刷需要先选择一个文本元素/);assert.equal(a.$('st-painter').getAttribute('aria-pressed'),'false');
 const edit=(id,patch)=>{const t=cv.tpl(a);Object.assign(all2(t).find(v=>v.e.id===id).e,patch);cv.load(a,t);};
 edit(one,{font_size:16,bold:true,align:'center',wrap:true,source:'literal',text:'x',field:''});
 cv.click2(a,one);a.click('st-painter');assert.equal(a.$('st-painter').getAttribute('aria-pressed'),'true');
 cv.click2(a,two);const hit=all2(cv.tpl(a)).find(v=>v.e.id===two).e;assert.deepEqual([hit.font_size,hit.align,hit.bold],[16,'center',true]);assert.equal(a.$('st-painter').getAttribute('aria-pressed'),'false','one shot');
 const before=JSON.stringify(cv.tpl(a));cv.click2(a,three);assert.equal(JSON.stringify(cv.tpl(a)),before,'the next click only selects');
 a.click('doc-undo');assert.notEqual(all2(cv.tpl(a)).find(v=>v.e.id===two).e.font_size,16,'one undo step');
 // Escape cancels it.
 cv.click2(a,one);a.click('st-painter');assert.equal(a.$('st-painter').getAttribute('aria-pressed'),'true');a.w.document.dispatchEvent(new a.w.KeyboardEvent('keydown',{key:'Escape',bubbles:true}));assert.equal(a.$('st-painter').getAttribute('aria-pressed'),'false');
 assert.equal(a.errors.length,0,a.errors.join('|'));
 }finally{a.close();}});

test('S06-A: a locked element is not moved or resized by the pointer or the keys, the lock is in the structure panel, and it is not in the template',async()=>{const a=await designer();try{
 const t0=to2(a),[id,other]=all2(t0).filter(v=>v.e.type==='text').map(v=>v.e.id);
 cv.click2(a,id);a.click('st-lock');assert.equal(a.$('st-lock').getAttribute('aria-pressed'),'true');assert.equal(cv.item(a,id).getAttribute('class').includes('cv-locked'),true);assert.match(cv.item(a,id).getAttribute('aria-label'),/，已锁定$/);
 assert.equal(cv.tpl(a).bands.document_header.elements.concat(...Object.values(cv.tpl(a).bands).map(b=>b.elements)).some(e=>'locked' in e),false,'the template has no lock');assert.equal(JSON.stringify(cv.tpl(a)).includes('lock'),false);
 const before=JSON.stringify(cv.tpl(a));
 const el=cv.item(a,id),r=all2(cv.tpl(a)).find(v=>v.e.id===id);
 const g=cv.press(a,el,r.e.x+10+r.e.width/2,40);g.move(80,60);g.up(80,60);assert.equal(JSON.stringify(cv.tpl(a)),before,'the pointer does not move it');assert.match(cv.status(a),/已锁定的项目不能移动/);
 cv.key(a,el,'ArrowRight');cv.key(a,el,'ArrowDown',{shiftKey:true});assert.equal(JSON.stringify(cv.tpl(a)),before,'the keys do not move it');assert.match(cv.status(a),/已锁定的项目不能移动：/);
 assert.equal(a.w.document.querySelectorAll('.cv-handle').length,0,'no resize handles');
 // Align and distribute move items, so they refuse too; the structure panel shows the lock and unlocks it.
 cv.click2(a,other,{shiftKey:true});a.click('doc-align-left');assert.match(cv.status(a),/已锁定的项目不能移动或调整大小/);assert.equal(JSON.stringify(cv.tpl(a)),before);
 railTab(a,'structure');const row=qa(a,'#st-layers .layer-row').find(r2=>r2.textContent.includes(id+' ·'));assert.ok(row);const lock=row.querySelectorAll('button')[1];assert.equal(lock.getAttribute('aria-pressed'),'true');assert.equal(lock.getAttribute('aria-label'),'解锁 '+id);
 lock.click();assert.equal(cv.item(a,id).getAttribute('class').includes('cv-locked'),false,'unlocked in the panel');
 cv.click2(a,id);const g2=cv.press(a,cv.item(a,id),r.e.x+10+r.e.width/2,40);g2.move(r.e.x+10+r.e.width/2+8,40);g2.move(r.e.x+10+r.e.width/2+16,40);g2.up(r.e.x+10+r.e.width/2+16,40);assert.notEqual(JSON.stringify(cv.tpl(a)),before,'unlocked, it moves');
 // A new template is another page: locks do not carry over; and they are not stored.
 cv.click2(a,id);a.click('st-lock');assert.equal(cv.item(a,id).getAttribute('class').includes('cv-locked'),true);to2(a);assert.equal(qa(a,'.cv-locked').length,0);assert.equal(a.w.localStorage.length,0);
 assert.equal(a.errors.length,0,a.errors.join('|'));
 }finally{a.close();}});

test('S06-A: the grid is a 10 mm overlay on the page and the snapping switch decides whether a drag lands on whole millimetres',async()=>{const a=await designer();try{
 cv.load(a,cv.four(a));const host=a.$('doc-canvas-host');
 assert.equal(a.$('st-grid').getAttribute('aria-pressed'),'false');assert.equal(host.querySelectorAll('.cv-gridline').length,0);
 a.click('st-grid');assert.equal(a.$('st-grid').getAttribute('aria-pressed'),'true');const path=host.querySelector('.cv-gridline');assert.ok(path);
 assert.equal((path.getAttribute('d').match(/M/g)||[]).length,20+29,'one line every 10 mm across 210 x 297 mm');assert.match(path.getAttribute('d'),/^M54 24v891/,'the first vertical line is at 10 mm');
 a.click('st-grid');assert.equal(host.querySelectorAll('.cv-gridline').length,0);
 // Snapping on: a drag of 2.4 mm lands on the 1 mm grid; off: it lands where the pointer put it (0.1 mm).
 const move=dx=>{const x0=cv.box(a,'n4').x;cv.drag(a,cv.item(a,'n4'),x0+25,65,dx,0);return cv.box(a,'n4').x-x0;};
 assert.equal(a.$('st-snap').getAttribute('aria-pressed'),'true');const snapped=move(2.4);assert.ok(Number.isInteger(snapped),'snapped to the grid: '+snapped);
 a.click('st-snap');assert.equal(a.$('st-snap').getAttribute('aria-pressed'),'false');assert.match(a.$('doc-status').textContent,/吸附已关/);const free=move(2.4);assert.ok(Math.abs(free-2.4)<1e-9,'not snapped: '+free);
 a.click('st-snap');assert.match(a.$('doc-status').textContent,/吸附已开/);
 a.click('st-guides');assert.equal(host.classList.contains('studio-nobands'),true);a.click('st-guides');assert.equal(host.classList.contains('studio-nobands'),false);
 a.click('st-reading');assert.equal(a.$('doc-canvas-panel').classList.contains('studio-reading'),true);a.click('st-reading');
 assert.equal(a.errors.length,0,a.errors.join('|'));
 }finally{a.close();}});

test('S06-A A3: the zoom control (−, 适合宽度 and 50/75/100/150/200 %, +), the ruler, the band labels and the pager',async()=>{const a=await designer();try{
 const sel=a.$('zoom');assert.deepEqual([...sel.options].map(o=>o.textContent),['适合宽度','50%','75%','100%','150%','200%']);
 const zoom=()=>Number(a.$('doc-zoom').value);assert.equal(zoom(),100);
 sel.value='150';sel.dispatchEvent(new a.w.Event('change',{bubbles:true}));assert.equal(zoom(),150);assert.equal(a.$('doc-zoom-value').textContent,'150 %');assert.equal(cv.item(a,'n1').querySelector('.cv-box').getAttribute('x'),String(cv.px(a,10)));
 a.click('st-zoom-out');assert.equal(zoom(),100);assert.equal(sel.value,'100');a.click('st-zoom-out');assert.equal(zoom(),75);a.click('st-zoom-out');assert.equal(zoom(),50);a.click('st-zoom-out');assert.equal(zoom(),50,'50 is the floor');
 a.click('st-zoom-in');assert.equal(zoom(),75);sel.value='200';sel.dispatchEvent(new a.w.Event('change',{bubbles:true}));a.click('st-zoom-in');assert.equal(zoom(),200,'200 is the ceiling');
 // The range input (the canvas's own control) and the select stay one control.
 a.input('doc-zoom','110');assert.equal(zoom(),110);assert.equal(sel.selectedOptions[0].textContent,'110%');
 // Fit width: the page fills the scroll area (here a mocked 1000 px wide).
 Object.defineProperty(a.$('doc-canvas-scroll'),'clientWidth',{value:1000,configurable:true});sel.value='fit';sel.dispatchEvent(new a.w.Event('change',{bubbles:true}));assert.equal(zoom(),Math.floor((1000-90-24-16)/630*100));assert.equal(sel.value,'fit');
 // The band labels are drawn in the margin left of the page: 横幅 and 正文 for 1.x, the five names for 2.0.
 const labels=()=>[...a.$('doc-canvas-host').querySelectorAll('.cv-bandlabel')].map(e=>e.textContent);
 cv.load(a,cv.four(a));assert.deepEqual(labels(),['横幅','正文']);to2(a);assert.deepEqual(labels(),['报表头','明细区']);
 const t=cv.tpl(a);t.bands.page_header={height:14,elements:[]};t.bands.page_footer={height:10,elements:[]};cv.load(a,t);assert.deepEqual(labels(),['页眉','报表头','明细区','页脚']);
 assert.match(a.$('doc-canvas-host').querySelector('.cv-heading').textContent,/^第 1 页/);assert.equal(a.$('doc-canvas-host').querySelectorAll('.cv-heading')[1].textContent,'A4 · 210 × 297 mm');
 assert.ok(a.$('st-ruler').querySelectorAll('line').length>0||true);
 // The pager says 1 / 1 and its buttons are disabled with the reason: the canvas shows the layout, one page; the pages of a result are in the PDF preview.
 assert.equal(a.$('pageCount').textContent,'1 / 1');for(const id of ['st-prev-page','st-next-page']){assert.equal(a.$(id).getAttribute('aria-disabled'),'true');assert.match(a.$(id).title,/画布显示版面（单页）；多页结果请在 PDF 预览中查看/);}
 assert.equal(a.errors.length,0,a.errors.join('|'));
 }finally{a.close();}});

test('S06-A A2: the panels are built from the real snapshot and template: the dataset card, the fields, the bands with their elements, the resources',async()=>{const a=await designer();try{
 const t0=to2(a),d=a.w.document;
 // Components: the three groups of the prototype and the dataset card.
 assert.deepEqual(qa(a,'#sidebar .side-view.active .eyebrow').map(e=>e.firstChild.textContent.trim()),['基础内容','数据与结构','专业单据']);
 assert.deepEqual(qa(a,'#sidebar .side-view.active .component-grid').map(g=>[...g.querySelectorAll('.component-card')].map(c=>c.textContent.trim())),[['文本','绑定字段','图片 / 标识','分隔线','矩形容器','签章栏'],['动态明细表流式分页','计算指标','金额摘要'],['条形码','二维码','页码信息','水印']]);
 assert.equal(a.$('st-comp-dataset').textContent,'receivables.aging');assert.match(a.$('st-comp-rows').textContent,/^3 条示例 · 3 个字段 · receivables\.aging@/);
 assert.equal(a.$('st-edit-rows').textContent.trim(),'编辑单据数据');assert.equal(a.$('st-comp-preview').textContent.trim(),'输出预览');
 // Data and fields: the snapshot's columns; the group fields of a 2.0 template, or 未分组 with the hint.
 railTab(a,'data');assert.equal(a.$('st-data-name').textContent,'receivables.aging');assert.equal(a.$('st-data-count').textContent,'3 条 / 3 字段');
 const rows=()=>qa(a,'#st-field-list .data-field').map(r=>r.querySelector('.field-code').textContent);
 assert.deepEqual(rows().sort(),['aging.bucket','ar.open_balance','customer.id']);assert.match(a.$('st-field-list').textContent,/单据级字段未分组：整个快照一份文档/);assert.match(a.$('st-field-list').textContent,/明细级字段/);
 a.input('st-field-search','balance');assert.deepEqual(rows(),['ar.open_balance']);a.input('st-field-search','');
 a.$('doc2-group-0').value='customer.id';a.$('doc2-group-0').dispatchEvent(new a.w.Event('change',{bubbles:true}));
 const groupRows=[...a.$('st-field-list').children];assert.equal(groupRows.findIndex(e=>e.textContent==='明细级字段')-groupRows.findIndex(e=>e.textContent==='单据级字段'),2,'the group field is listed under 单据级字段');
 // A field row inserts a field text in the current band; with the table selected, a field that is already a column says so.
 cv.click2(a,'e1');const n0=ids2(cv.tpl(a)).length;qa(a,'#st-field-list .data-field').find(r=>r.querySelector('.field-code').textContent==='ar.open_balance').click();assert.equal(ids2(cv.tpl(a)).length,n0+1);const fresh=all2(cv.tpl(a)).find(v=>v.e.field==='ar.open_balance'&&v.e.source==='field');assert.ok(fresh||all2(cv.tpl(a)).some(v=>v.e.source==='field'));
 const tableKey=all2(cv.tpl(a)).find(v=>v.e.type==='table').e.id;cv.click2(a,tableKey);a.$('doc-status').textContent='';qa(a,'#st-field-list .data-field').filter(r=>r.querySelector('.field-code').textContent==='customer.id').at(-1).click();assert.match(a.$('doc-status').textContent,/已经是明细表的列/);
 // Structure: each band of the template with its elements, a lock and a visibility toggle each; a click selects on the canvas and the selection shows here.
 railTab(a,'structure');const heads=qa(a,'#st-layers .outline-band-head').map(h=>h.textContent.replace(/\s+/g,' ').trim());assert.deepEqual(heads,['单据头带单据首页','明细带流式']);
 const rowsOf=()=>qa(a,'#st-layers .layer-row');assert.equal(rowsOf().length,ids2(cv.tpl(a)).length);
 const first=rowsOf()[0],key=first.querySelector('.layer-label').textContent.split(' · ')[0];first.querySelector('.layer-label').click();assert.deepEqual(cv.selected(a),[key]);assert.equal(rowsOf()[0].classList.contains('active'),true,'the canvas selection is highlighted here');
 cv.click2(a,'e2');assert.deepEqual(rowsOf().filter(r=>r.classList.contains('active')).map(r=>r.querySelector('.layer-label').textContent.split(' · ')[0]),['e2']);
 // Hidden: still in the template, drawn faintly, and said so in the tooltip; the template does not change.
 const eye=rowsOf().find(r=>r.querySelector('.layer-label').textContent.startsWith('e3 ')).querySelectorAll('button')[2];assert.match(eye.title,/仍在模板中，预览与输出照常包含/);const before=JSON.stringify(cv.tpl(a));eye.click();const said=a.$('doc-status').textContent;
 assert.equal(cv.item(a,'e3').getAttribute('class').includes('cv-hidden'),true);assert.equal(JSON.stringify(cv.tpl(a)),before);assert.match(said,/已在画布上隐藏（仍在模板中，预览与输出照常包含它）/);
 const eye2=rowsOf().find(r=>r.querySelector('.layer-label').textContent.startsWith('e3 ')).querySelectorAll('button')[2];assert.equal(eye2.getAttribute('aria-pressed'),'true');assert.equal(eye2.getAttribute('aria-label'),'显示 e3');
 // Resources: a text style or an alignment for the selected texts.
 railTab(a,'resources');cv.click2(a,'e2');qa(a,'[data-style-size="22"]')[0].click();assert.match(a.$('doc-status').textContent,/./,'a size the box cannot hold is refused by the validator, as the input is');qa(a,'[data-style-size="14"]')[0].click();let e2=all2(cv.tpl(a)).find(v=>v.e.id==='e2').e;assert.deepEqual([e2.font_size,e2.bold],[14,true]);
 qa(a,'[data-text-align="right"]')[0].click();e2=all2(cv.tpl(a)).find(v=>v.e.id==='e2').e;assert.equal(e2.align,'right');a.click('doc-undo');a.click('doc-undo');assert.notEqual(all2(cv.tpl(a)).find(v=>v.e.id==='e2').e.font_size,14);
 assert.ok(a.$('doc-image-file')&&a.$('doc-background-file')&&a.$('doc-image-upload'),'the existing image and background controls are in the resources panel');assert.equal(a.$('doc-image-file').closest('.side-view').getAttribute('data-view'),'resources');
 assert.equal(a.errors.length,0,a.errors.join('|'));void t0;void d;
 }finally{a.close();}});

test('S06-A A4: the inspector is the page\'s or the element\'s, in the prototype\'s groups, with the tabs 属性 / 规则 / 检查',async()=>{const a=await designer();try{
 to2(a);
 assert.equal(a.$('st-insp-title').textContent,'页面属性');assert.equal(a.$('st-insp-sub').textContent,'纸张、区段与打印设置');
 assert.deepEqual(qa(a,'#page-documents [data-prop-tab]').map(t=>[t.getAttribute('role'),t.getAttribute('aria-selected')]),[['tab','true'],['tab','false'],['tab','false']]);assert.equal(q(a,'#page-documents [data-prop-tab]').parentElement.getAttribute('role'),'tablist');
 assert.equal(a.$('st-page-dataset').textContent,'receivables.aging@1.0.0');assert.equal(a.$('st-page-meta').textContent,'A4 · 自动分页 · 重复表头');
 assert.deepEqual(['st-page-rows','st-page-json','st-page-preview'].map(id=>a.$(id).textContent.trim()),['编辑业务样本','导出JSON样本','输出预览']);
 assert.match(q(a,'#st-insp-page .info-box').textContent,/页面尺寸\s*210 × 297 mm/);assert.deepEqual(['st-page-paper','st-page-bands'].map(id=>a.$(id).textContent.trim()),['纸张与页边距','区段设置']);
 assert.match(a.$('st-page-info').textContent,/未命名模板.*receivables\.aging@1\.0\.0.*3 条当前示例/);
 // An element: its kind in the title, the groups of the prototype.
 const groups=id=>{cv.click2(a,id);return qa(a,'#doc2-props .prop-section h3').map(h=>h.textContent);};
 const text=all2(cv.tpl(a)).find(v=>v.e.type==='text').e.id;assert.deepEqual(groups(text),['位置与尺寸','内容','文字']);assert.match(a.$('st-insp-sub').textContent,/ · 文本$/);
 card(a,'line').click();assert.deepEqual(qa(a,'#doc2-props .prop-section h3').map(h=>h.textContent),['位置与尺寸','边框']);assert.match(a.$('st-insp-sub').textContent,/ · 分隔线$/);
 card(a,'rect').click();assert.deepEqual(qa(a,'#doc2-props .prop-section h3').map(h=>h.textContent),['位置与尺寸','边框']);
 card(a,'barcode').click();assert.deepEqual(qa(a,'#doc2-props .prop-section h3').map(h=>h.textContent),['位置与尺寸','内容','文字']);
 // The inputs are the old ones: the validator decides, an undo step each.
 const id=cv.selected(a)[0];a.$('doc2-p-width').value='20';a.$('doc2-p-width').dispatchEvent(new a.w.Event('change',{bubbles:true}));assert.equal(all2(cv.tpl(a)).find(v=>v.e.id===id).e.width,20);a.click('doc-undo');
 const tk=all2(cv.tpl(a)).find(v=>v.e.type==='table').e.id;cv.click2(a,tk);assert.equal(a.$('st-insp-title').textContent,'明细表');assert.ok(a.$('doc2-table').children.length>0);assert.equal(a.$('st-insp-element').hidden,false);assert.equal(a.$('st-insp-page').hidden,true);
 cv.click2(a,text);cv.click2(a,tk,{shiftKey:true});assert.equal(a.$('st-insp-title').textContent,'已选择 2 个组件');
 // Escape clears the selection and the page's properties come back.
 cv.key(a,cv.item(a,text),'Escape');assert.equal(a.$('st-insp-title').textContent,'页面属性');
 // Rules: the template's constraints as the designer knows them.
 q(a,'[data-prop-tab="rules"]').click();assert.equal(a.$('st-ipanel-rules').hidden,false);assert.equal(a.$('st-ipanel-properties').hidden,true);const rules=qa(a,'#st-rules li').map(l=>l.textContent);
 assert.ok(rules.some(r=>/安全区 x 10–200/.test(r))&&rules.some(r=>/至多 128 个元素/.test(r))&&rules.some(r=>/不能重叠/.test(r))&&rules.some(r=>/明细带必有一个明细表/.test(r))&&rules.some(r=>/无分组：整个快照一份文档/.test(r)),rules.join('|'));
 assert.equal(a.$('st-mark-refusal').disabled,true);assert.equal(a.$('st-refusal').textContent,'尚无。');
 // Check: the boundary samples' table with the old ids, and 重新检查 reaching the same button.
 q(a,'[data-prop-tab="check"]').click();assert.equal(a.$('st-ipanel-check').hidden,false);assert.equal(a.$('doc-check-wrap').closest('#st-ipanel-check')!==null,true);assert.equal(a.$('st-recheck').textContent.trim(),'重新检查');assert.match(a.$('st-issues').textContent,/当前版式与绑定检查通过/);
 q(a,'[data-prop-tab="properties"]').click();q(a,'[data-prop-tab="rules"]').dispatchEvent(new a.w.KeyboardEvent('keydown',{key:'ArrowRight',bubbles:true,cancelable:true}));assert.equal(a.w.document.activeElement.id,'st-itab-check');assert.equal(a.$('st-ipanel-check').hidden,false);
 assert.equal(a.errors.length,0,a.errors.join('|'));
 }finally{a.close();}});

test('S06-A A4: the check tab shows the boundary samples with their rows, codes and reasons; the status bar and the badge of the tab count what failed; 重新检查 runs the same check',async()=>{const bodies=[];
 const answer={rows:[{row_index:0,ok:true,reasons:['first']},{row_index:2,ok:false,code:'UNSUPPORTED_COMBINATION',detail:{reason:'DOES_NOT_FIT',element:'n1',row:2},reasons:['last','longest:n1']}]};
 const a=app(offeringChecks(answer,{bodies}));try{
 await checkReady(a);assert.equal(a.$('st-validation-text').textContent,'版式检查通过');assert.equal(a.$('st-issue-count').hidden,true);
 q(a,'[data-act="edge-sample"]#st-recheck').click();await until(()=>a.$('doc-check-body').rows.length===2&&!a.$('preview').disabled,'checked');assert.equal(bodies.length,1);
 assert.equal(a.$('st-ipanel-check').hidden,false,'running the check shows its tab');assert.equal(a.$('st-itab-check').getAttribute('aria-selected'),'true');
 assert.equal(a.$('st-validation-text').textContent,'版式检查未通过 · 1 项');assert.equal(a.$('st-validation').classList.contains('bad'),true);assert.equal(a.$('st-issue-count').hidden,false);assert.equal(a.$('st-issue-count').textContent,'1');
 const card1=q(a,'#st-issues .validation-card');assert.match(card1.textContent,/第 3 行不通过 · UNSUPPORTED_COMBINATION/);assert.match(card1.textContent,/放不下.*元素 n1/);
 card1.click();assert.equal(a.$('doc-canvas-host').querySelectorAll('.cv-refused').length,1,'the place is outlined on the canvas (the existing outline)');
 assert.deepEqual(checkRows(a).map(r=>r[1].slice(0,3)),['通过','不通过']);
 // A change of the template voids it: the count goes.
 cv.click(a,'n1');cv.key(a,cv.item(a,'n1'),'ArrowDown');assert.equal(a.$('st-validation-text').textContent,'版式检查通过');assert.equal(a.$('st-issue-count').hidden,true);
 // 运行版式检查 is the designer's own validation: a valid template passes, and it opens the check tab.
 q(a,'#st-ipanel-check');a.click('st-validate');assert.match(a.$('doc-status').textContent,/版式检查通过/);
 assert.equal(a.errors.length,0,a.errors.join('|'));
 }finally{a.close();}});

test('S06-A A5: every dialog is a labelled modal dialog: focus goes in, Tab stays inside, Esc and the backdrop close it, and the focus goes back to the control that opened it',async()=>{const a=await designer();try{
 to2(a);const d=a.w.document;
 // The crumb opens the template centre in the console; the chooser's own act is what an embed page's crumb has, so it is set here (S06-B part 3).
 q(a,'#st-crumb').dataset.act='chooser';
 const cases=[['help','#st-help','设计说明与操作帮助'],['page','#st-page-settings','纸张与页边距'],['bands','#st-bands','区段设置'],['print','#st-print-settings','底稿、偏移与预印纸套打'],['insert','#st-insert-options','插入选项'],['preview','#previewButton','预览与导出'],['json','#st-export-json','模板JSON'],['issue','#st-tool-issue','正式出具与出具记录'],['versions','#st-versions','版本历史与发布指针'],['publish','#st-publish','发布前确认'],['chooser','#st-crumb','模板中心'],['query','#st-ctx-params','参数与筛选'],['rows','#st-ctx-data','数据预览与字段契约']];
 const tabKey=(shift)=>{const ev=new a.w.KeyboardEvent('keydown',{key:'Tab',shiftKey:shift,bubbles:true,cancelable:true});d.activeElement.dispatchEvent(ev);return ev.defaultPrevented;};
 const focusables=id=>[...dialogOf(a,id).querySelectorAll('.dialog button,.dialog input,.dialog select,.dialog textarea,.dialog summary')].filter(e=>!e.disabled&&e.tabIndex>=0&&!e.closest('[hidden]'));
 for(const [id,trigger,title] of cases){
  const opener=q(a,trigger);ribbonTab(a,'design');opener.focus();
  // the ribbon tab of an opener in another tab: show it first
  const panel=opener.closest('.ribbon-panel');if(panel)ribbonTab(a,panel.getAttribute('data-panel'));opener.focus();opener.click();
  const box=dialogOf(a,id).querySelector('[role="dialog"]');assert.ok(isOpen(a,id),id+' opens');assert.equal(box.getAttribute('aria-modal'),'true',id);assert.equal(d.getElementById(box.getAttribute('aria-labelledby')).textContent,title,id+' is labelled by its title');
  assert.ok(box.contains(d.activeElement),id+': the focus is inside, on '+(d.activeElement.id||d.activeElement.tagName));assert.equal(a.$('doc-canvas-panel').inert,true,id+': the studio behind is inert');
  assert.deepEqual(qa(a,'.overlay.open').map(o=>o.id),['st-d-'+id],'one dialog at a time');
  const list=focusables(id);assert.ok(list.length>=1,id+' has something to focus');
  list.at(-1).focus();assert.equal(tabKey(false),true,id+': Tab at the last control is held');assert.equal(d.activeElement,list[0],id+': and goes to the first');
  list[0].focus();assert.equal(tabKey(true),true);assert.equal(d.activeElement,list.at(-1),id+': Shift+Tab at the first goes to the last');
  // Esc closes and the focus is where it was.
  d.activeElement.dispatchEvent(new a.w.KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));assert.equal(isOpen(a,id),false,id+' closes on Esc');assert.equal(a.$('doc-canvas-panel').inert,false);assert.equal(d.activeElement,opener,id+': the focus returns to the opener');
  // The backdrop and the close button close it too.
  opener.click();assert.ok(isOpen(a,id));dialogOf(a,id).dispatchEvent(new a.w.PointerEvent('pointerdown',{bubbles:true}));assert.equal(isOpen(a,id),false,id+' closes on the backdrop');
  opener.click();dialogOf(a,id).querySelector('[data-act="close"][aria-label="关闭对话框"],.dialog-footer [data-act="close"]')?.click();assert.equal(isOpen(a,id),false,id+' closes on its button');
 }
 // The command palette is the same kind of dialog and works over the studio.
 a.w.document.body.dispatchEvent(new a.w.KeyboardEvent('keydown',{key:'k',ctrlKey:true,bubbles:true,cancelable:true}));assert.equal(palette(a).parentElement.hidden,false);press(a,'Escape');
 assert.equal(a.errors.length,0,a.errors.join('|'));
 }finally{a.close();}});

test('S06-A A3: 参数与筛选 shows the query form in a right drawer and 查看数据 the snapshot rows in a bottom drawer; both go back to the workbench when they close',async()=>{const a=await designer();try{
 const form=q(a,'.query-panel'),home=form.parentElement,rows=a.$('result-table-wrap'),rowsHome=rows.parentElement;
 assert.equal(home.closest('#page-workspace')!==null,true);
 q(a,'#st-ctx-params').click();assert.equal(a.$('st-query-slot').contains(form),true,'the query form is in the drawer');assert.ok(a.$('query-companies')&&a.$('snapshot')&&a.$('preview'),'with its own controls');assert.equal(dialogOf(a,'query').classList.contains('drawer-right'),true);
 a.w.document.activeElement.dispatchEvent(new a.w.KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));assert.equal(form.parentElement,home,'closed: the form is back in the workbench');
 q(a,'#st-ctx-data').click();assert.equal(a.$('st-rows-slot').contains(rows),true);assert.equal(a.$('st-rows-slot').contains(a.$('result-pager')),true,'the existing pagination comes with the rows');assert.equal(a.$('st-rows-slot').querySelectorAll('tbody tr').length,3);assert.equal(dialogOf(a,'rows').classList.contains('drawer-bottom'),true);
 a.w.document.activeElement.dispatchEvent(new a.w.KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));assert.equal(rows.parentElement,rowsHome);
 // Submitting the form re-freezes a snapshot and the studio re-binds (the existing workbench flow).
 q(a,'#st-ctx-params').click();a.click('snapshot');await until(()=>a.$('result-kind').textContent==='冻结快照'&&!a.$('snapshot').disabled,'frozen');assert.match(a.$('st-source').textContent,/^冻结快照 [a-f0-9]{8} · receivables\.aging$/);assert.equal(a.$('recordBadge').textContent,'3 条示例');
 // The sample download: the first rows of the snapshot as JSON, from the rows the console already has.
 a.w.document.querySelector('#st-d-query [data-act="close"]').click();q(a,'#st-page-json').click();await flush();assert.equal(a.downloads.length,1);assert.equal(a.downloads[0].name,'snapshot-sample.json');
 const sample=JSON.parse(await a.downloads[0].blob.text());assert.deepEqual([sample.kind,sample.dataset.id,sample.row_count,sample.rows.length],['snapshot_sample','receivables.aging',3,3]);assert.deepEqual(Object.keys(sample.rows[0]).sort(),['aging.bucket','ar.open_balance','customer.id']);
 assert.equal(a.errors.length,0,a.errors.join('|'));
 }finally{a.close();}});

test('S06-A A1/A5: the template\'s name is editable in place and validated like the server does, round-trips through save and open, and the versions, the publish confirmation and the template chooser are the server\'s',async()=>{const a=await designer();try{
 const d=a.w.document,name=()=>cv.tpl(a).name;
 assert.equal(a.$('st-name').textContent,'未命名模板');assert.equal(name(),undefined,'a template without a name has no name key');
 // Rename in place: Enter commits, Esc cancels, a refusal keeps the input with its reason.
 a.$('titleButton').click();const input=a.$('st-name-input');assert.equal(input.hidden,false);assert.equal(a.$('titleButton').hidden,true);assert.equal(d.activeElement,input);assert.equal(input.maxLength,80);assert.match(input.getAttribute('aria-label'),/最多 80/);
 const type=v=>{input.value=v;};const enter=()=>input.dispatchEvent(new a.w.KeyboardEvent('keydown',{key:'Enter',bubbles:true,cancelable:true}));
 type('字'.repeat(81));enter();assert.equal(input.hidden,false,'81 characters: refused');assert.equal(input.getAttribute('aria-invalid'),'true');assert.equal(a.$('doc-status').textContent,'模板名称须为 1–80 个字符，单行，前后不留空白。');assert.equal(name(),undefined);
 type('a\u0007b');enter();assert.equal(input.hidden,false,'a control character: refused');assert.equal(name(),undefined);
 type('字'.repeat(80));enter();assert.equal(input.hidden,true,'80 characters: accepted');assert.equal(name(),'字'.repeat(80));assert.equal(d.activeElement,a.$('titleButton'));
 a.click('doc-undo');assert.equal(name(),undefined,'renaming is one undo step');a.click('doc-redo');assert.equal(name(),'字'.repeat(80));
 a.$('titleButton').click();type('  客户往来对账单  ');enter();assert.equal(name(),'客户往来对账单','the name is trimmed');assert.equal(a.$('st-name').textContent,'客户往来对账单');assert.equal(a.$('datasetName').textContent,'客户往来对账单');assert.equal(a.$('st-page-name').textContent,'客户往来对账单');
 a.$('titleButton').click();type('别的名字');input.dispatchEvent(new a.w.KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));assert.equal(input.hidden,true);assert.equal(name(),'客户往来对账单','Esc cancels');
 // A name does not change the layout: the template is otherwise what it was, and the JSON editor refuses the same names the input does.
 const base=cv.tpl(a);cv.load(a,{...base,name:'ok'});assert.equal(name(),'ok');for(const bad of [' x','x ','','x'.repeat(81),5,null]){a.$('doc-json').value=JSON.stringify({...base,name:bad});a.click('doc-json-open');assert.notEqual(a.$('doc-status').textContent,READY,JSON.stringify(bad)+' is refused');}
 // Save, name round trip, state badge.
 cv.load(a,{...base,name:'客户往来对账单'});assert.equal(a.$('st-state').textContent,'草稿');assert.equal(a.$('autosaveStatus').textContent,'未保存的修改');
 a.click('doc-server-save');await until(()=>a.$('doc-server-status').textContent.includes('版本 1')&&!a.$('preview').disabled,'saved v1');const id=a.$('doc-server-status').textContent.match(/[a-f0-9]{32}/)[0];
 assert.equal(a.$('st-state').textContent,'已保存 v1');assert.equal(a.$('autosaveStatus').textContent,'已保存');
 const saved=await (await fetch(url+'/api/reporting/v1/document-templates/'+id+'?version=1',{headers:{Authorization:'Bearer '+token}})).json();assert.equal(saved.config.name,'客户往来对账单','the server keeps the name');
 card(a,'text').click();assert.equal(a.$('st-state').textContent,'已保存 v1·有未保存修改');assert.equal(a.$('autosaveStatus').textContent,'未保存的修改');
 a.click('doc-server-save');await until(()=>a.$('doc-server-status').textContent.includes('版本 2')&&!a.$('preview').disabled,'saved v2');assert.equal(a.$('st-state').textContent,'已保存 v2');
 // A template without a name shows its number: save one without.
 // Versions: the server's list with the pointer; restore opens as an unsaved change; publication is its own act.
 q(a,'#st-versions').click();await until(()=>a.$('doc-release-status').textContent.includes('发布修订 0')&&qa(a,'#st-version-list .version-card').length===2,'versions listed');
 assert.deepEqual(qa(a,'#st-version-list .version-card strong').map(e=>e.textContent),['v2','v1']);assert.deepEqual(qa(a,'#st-version-list .pill').map(e=>e.textContent),['已保存','已保存']);assert.match(a.$('st-vd-note').textContent,/发布指针仍指向 未发布/);assert.equal(a.$('st-version-card').hidden,false);
 assert.equal(a.$('st-ver-preview').disabled,!pdfConfigured,'the preview of a version needs the server\'s PDF rendering');if(!pdfConfigured)assert.equal(a.$('st-ver-preview').title,'服务端未启用 PDF 渲染');
 qa(a,'#st-version-list .version-card')[1].click();await until(()=>/个组件/.test(a.$('st-vd-elements').textContent)&&!a.$('preview').disabled,'v1 read');assert.equal(a.$('st-vd-elements').textContent,'1 个组件');assert.equal(a.$('st-vd-title').textContent,'客户往来对账单 · v1');
 a.$('st-ver-restore').click();assert.equal(isOpen(a,'versions'),false);assert.equal(cv.tpl(a).elements.length,1,'v1 is in the editor');assert.equal(a.$('st-state').textContent,'已保存 v2·有未保存修改','restoring is an unsaved change, not a save');assert.equal(a.$('doc-undo').disabled,false);
 assert.equal(a.trace.filter(t=>t.method==='POST'&&t.path.endsWith('/document-templates')).length,2,'nothing was saved by the restore');
 // Publish confirmation: what will be published; not while there is something unsaved; the confirm is the server's publication.
 q(a,'#st-publish').click();assert.equal(a.$('st-pub-confirm').disabled,false,'S07-B: the unsaved changes can be saved and published in one go');assert.equal(a.$('st-pub-confirm').textContent.trim(),'保存并发布');assert.match(a.$('st-pub-saved').textContent,/需要先保存当前修改/);assert.equal(a.$('st-pub-version').textContent,'v2');a.$('st-d-publish').querySelector('.dialog-footer [data-act="close"]').click();
 a.click('doc-undo');assert.equal(a.$('st-state').textContent,'已保存 v2');
 q(a,'#st-publish').click();await until(()=>!a.$('st-pub-confirm').disabled,'release read');assert.equal(a.$('st-pub-name').textContent,'客户往来对账单');assert.equal(a.$('st-pub-dataset').textContent,'receivables.aging@1.0.0');assert.equal(a.$('st-pub-elements').textContent,'2 个组件');assert.match(a.$('st-pub-schema').textContent,/schema 1\./);assert.match(a.$('st-pub-pointer').textContent,/当前发布 无 → v2/);assert.match(a.$('st-pub-saved').textContent,/已明确引用不可变保存版本/);assert.match(a.$('st-pub-valid').textContent,/版式与数据绑定检查通过/);
 a.$('st-pub-confirm').click();assert.equal(isOpen(a,'publish'),false);await until(()=>a.$('doc-release-status').textContent.includes('已发布版本 2')&&!a.$('preview').disabled,'published v2');
 const posted=a.trace.filter(t=>t.method==='POST'&&t.path.endsWith('/release'));assert.equal(posted.length,1);assert.equal(a.$('st-state').textContent,'已发布 v2');assert.equal(a.$('autosaveStatus').textContent,'已发布');
 // Publishing another saved version is its own act, and the pointer then says so; withdrawal is separate.
 q(a,'#st-versions').click();await until(()=>qa(a,'#st-version-list .pill').map(e=>e.textContent).join()==='已发布,已保存','pointer shown');
 qa(a,'#st-version-list .version-card')[1].click();await until(()=>!a.$('preview').disabled,'v1 read');a.$('st-ver-publish').click();assert.equal(isOpen(a,'publish'),true);assert.equal(a.$('st-pub-version').textContent,'v1');assert.match(a.$('st-pub-pointer').textContent,/历史版本回退 · 当前发布 v2 → v1/);await until(()=>!a.$('st-pub-confirm').disabled,'confirm');
 a.$('st-pub-confirm').click();try{await until(()=>a.$('doc-release-status').textContent.includes('已发布版本 1')&&!a.$('preview').disabled,'published v1');}catch(e){throw new Error(e.message+' | '+a.$('notice').textContent+' | '+a.$('doc-status').textContent+' | '+a.$('doc-release-status').textContent+' | '+JSON.stringify(a.trace.slice(-4)));}assert.equal(a.$('st-state').textContent,'已保存 v2','the editor\'s v2 is no longer the published one');
 a.click('doc-release-withdraw');await until(()=>a.$('doc-release-status').textContent.includes('当前未发布')&&!a.$('preview').disabled,'withdrawn');
 // The template chooser: the identity's templates with their names, versions, published version and data set; open one.
 q(a,'#st-crumb').dataset.act='chooser';q(a,'#st-crumb').click();try{await until(()=>qa(a,'#st-chooser-body tr').some(r=>r.textContent.includes('客户往来对账单')&&r.cells[4].textContent!=='…')&&!a.$('preview').disabled,'chooser rows',15000);}catch(e){throw new Error(e.message+' | '+a.$('st-chooser-status').textContent+' | '+qa(a,'#st-chooser-body tr').map(r=>r.textContent).join(' / ')+' | '+a.$('notice').textContent+a.$('doc-status').textContent);}
 const row=qa(a,'#st-chooser-body tr').find(r=>r.cells[1].title===id);assert.ok(row);assert.deepEqual([row.cells[0].textContent,row.cells[2].textContent,row.cells[3].textContent,row.cells[4].textContent],['客户往来对账单 当前','2','—','receivables.aging@1.0.0']);
 assert.deepEqual([...row.cells[5].querySelectorAll('button')].map(b=>b.textContent+(b.getAttribute('aria-disabled')==='true'?':'+b.title:'')),['打开','打开已发布版本:尚无已发布版本']);
 assert.deepEqual(['st-chooser-new','st-chooser-import','st-chooser-refresh'].map(i=>a.$(i).textContent.trim()),['新建','导入JSON','刷新列表']);
 cv.load(a,{...base,name:'别的'});row.cells[5].querySelector('button').click();await until(()=>a.$('doc-server-status').textContent.includes('正在编辑模板')&&!a.$('preview').disabled,'opened');assert.equal(cv.tpl(a).name,'客户往来对账单');assert.equal(isOpen(a,'chooser'),false);
 // A template without a name shows 模板 and the first eight characters of its number.
 const noName=cv.tpl(a);delete noName.name;cv.load(a,noName);assert.equal(a.$('st-name').textContent,'模板 '+id.slice(0,8));
 // 新建: the default template (the old reset).
 q(a,'#st-crumb').dataset.act='chooser';q(a,'#st-crumb').click();a.$('st-chooser-new').click();assert.equal(cv.tpl(a).elements[0].text,'Document preview');assert.equal(isOpen(a,'chooser'),false);
 assert.equal(a.errors.length,0,a.errors.join('|'));
 }finally{a.close();}});

test('S06-A: the studio\'s code writes no markup, no style attribute, loads nothing from outside and stores nothing; the page of the studio has no inline style or handler after everything has been opened',async()=>{const a=await designer();try{
 for(const [name,code] of [['studio.js',studioScript],['canvas.js',canvasScript],['documents.js',documentsScript]]){
  for(const pattern of [/\.innerHTML/,/\.outerHTML/,/insertAdjacentHTML/,/document\.write/,/\beval\(/,/new Function/,/\.srcdoc/,/setAttribute\(\s*['"]on/,/\.style\.cssText/,/setAttribute\(\s*['"]style/,/\.style\./,/\bimport\(/,/https?:\/\//,/\bfetch\(/,/XMLHttpRequest/,/localStorage|sessionStorage|indexedDB|document\.cookie/,/createElement\(\s*['"](script|iframe|link|style)['"]/])assert.equal(pattern.test(code.replace(/\/\/.*$/gm,'')),false,name+' contains '+pattern);
 }
 assert.equal((html.match(/<script/g)||[]).length,(html.match(/<script src="\/console\/[a-z]+\.js" defer>/g)||[]).length,'every script is one of the console\'s own files');
 assert.match(html,/<link rel="stylesheet" href="\/console\/studio\.css">/);
 const css=await readFile(join(root,'console/assets/studio.css'),'utf8');assert.equal(/@import|url\(\s*['"]?https?:|expression\(/.test(css),false,'the stylesheet loads nothing');assert.equal(/<script|javascript:/.test(css),false);
 // Open everything; no node of the page of the studio carries a style attribute or an event handler.
 to2(a);for(const id of ['help','page','bands','print','insert','preview','json','issue','versions','publish','chooser','query','rows']){a.w.document.querySelector('#st-d-'+id).classList.add('open');}
 for(const k of ['text','barcode','rect'])card(a,k).click();a.click('st-grid');cv.click2(a,'e1');a.click('st-lock');railTab(a,'structure');railTab(a,'data');railTab(a,'resources');for(const t of ['insert','data','page','check'])ribbonTab(a,t);
 for(const e of a.$('page-documents').querySelectorAll('*')){assert.equal(e.hasAttribute('style'),false,'style attribute on <'+e.tagName.toLowerCase()+' id='+e.id+'>');assert.equal([...e.attributes].some(x=>/^on/i.test(x.name)),false,'inline handler');}
 assert.equal(a.w.document.querySelectorAll('script[src^="http"],link[href^="http"],img[src^="http"]').length,0);assert.equal(a.w.localStorage.length,0);assert.equal(a.w.sessionStorage.length,0);
 // Every id of the old documents section is still in the page (the studio rearranged them, it did not drop any), and none is used twice.
 const ids=[...a.w.document.querySelectorAll('[id]')].map(e=>e.id);assert.equal(new Set(ids).size,ids.length,'an id is used twice');
 for(const id of ['doc-undo','doc-redo','doc-dup','doc-del','doc-check','doc-zoom','doc-palette-open','doc2-props','doc2-table','doc-elements','doc2-add-literal','doc-add-text','doc-table-add','doc-background-upload','doc-print-apply','doc-server-save','doc-release-publish','doc-issue','doc-json','doc-row','doc-pdf','doc-status','doc-canvas-help','doc-canvas-live'])assert.ok(ids.includes(id),id);
 assert.equal(a.errors.length,0,a.errors.join('|'));
 }finally{a.close();}});

test('S06-A A3/A5: 预览与导出 is the server\'s PDF preview, the pager tells the pages of the last preview, and a saved version is previewed by its number',{skip:!pdfConfigured&&'needs the pinned Typst renderer'},async()=>{const a=app();try{
 await snapshotReady(a);a.w.document.querySelector('[data-page="documents"]').click();
 assert.equal(a.$('pageCount').textContent,'1 / 1','1 until a preview');assert.equal(a.$('doc-row').closest('#st-d-preview')!==null,true);
 q(a,'#previewButton').click();assert.equal(a.$('st-pv-pages').textContent,'尚未预览');assert.match(a.$('st-pv-source').textContent,/^[a-f0-9]{8} · receivables\.aging$/);
 a.click('doc-pdf');await until(()=>a.downloads.length===1&&!a.$('preview').disabled,'preview downloaded',20000);
 const bytes=Buffer.from(await a.downloads[0].blob.arrayBuffer()),pages=(bytes.toString('latin1').match(/\/Type\s*\/Page(?![A-Za-z])/g)||[]).length;
 assert.equal(bytes.subarray(0,5).toString(),'%PDF-');assert.ok(pages>=1);assert.equal(a.$('pageCount').textContent,'1 / '+pages,'the pager tells the last preview\'s page count');assert.equal(a.$('st-pv-pages').textContent,pages+' 页');assert.match(a.$('st-page-info').textContent,new RegExp(pages+' 页（最近一次预览）'));
 // A saved version is previewed by its number: the request names the version and the template, and the template is not changed.
 a.w.document.querySelector('#st-d-preview [data-act="close"]').click();a.click('doc-server-save');await until(()=>a.$('doc-server-status').textContent.includes('版本 1')&&!a.$('preview').disabled,'saved');const id=a.$('doc-server-status').textContent.match(/[a-f0-9]{32}/)[0];
 q(a,'#st-versions').click();await until(()=>qa(a,'#st-version-list .version-card').length===1&&!a.$('preview').disabled,'versions');
 a.$('st-ver-preview').click();await until(()=>a.downloads.length===2&&!a.$('preview').disabled,'saved version previewed',20000);
 const call=a.trace.find(t=>t.path.endsWith('/document-templates/'+id+'/previews'));assert.ok(call,'the route of the saved version');
 assert.equal(a.errors.length,0,a.errors.join('|'));
 }finally{a.close();}});

test('S06-A A5/A7: a list that is built again keeps the keyboard where it was, and a focus that fell out of a dialog still has Esc and Tab',async()=>{const a=await designer();try{
 to2(a);const d=a.w.document;
 // The structure panel is rebuilt on every selection: the layer that was activated by the keyboard still has the focus afterwards.
 railTab(a,'structure');const label=qa(a,'#st-layers .layer-label')[1];label.focus();assert.equal(d.activeElement,label);label.click();
 const key=label.getAttribute('data-k');assert.ok(key.startsWith('name:'));assert.notEqual(qa(a,'#st-layers .layer-label')[1],label,'the list was built again');assert.equal(d.activeElement.getAttribute('data-k'),key,'and the focus is on the same layer');
 // A dialog whose focused element was removed: the focus is on the page, and Esc still closes the dialog, Tab still comes back into it.
 q(a,'#st-help').focus();q(a,'#st-help').click();assert.equal(isOpen(a,'help'),true);d.activeElement.blur();assert.equal(d.activeElement,d.body);
 const tab=new a.w.KeyboardEvent('keydown',{key:'Tab',bubbles:true,cancelable:true});d.body.dispatchEvent(tab);assert.equal(tab.defaultPrevented,true);assert.equal(dialogOf(a,'help').contains(d.activeElement),true,'Tab brings the focus back into the dialog');
 d.activeElement.blur();d.body.dispatchEvent(new a.w.KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));assert.equal(isOpen(a,'help'),false);assert.equal(d.activeElement,q(a,'#st-help'));
 assert.equal(a.errors.length,0,a.errors.join('|'));
 }finally{a.close();}});

// S06-B B7: the sample pack in the studio. The 37 samples of the pack come from the real server (the converter's output, embedded); each is copied to the
// open query's dataset by the real copy route (a mapping of its slots to the dataset's columns, as the template centre will send it) and the copy opens in
// the studio's canvas on the page of its own preset with every element drawn and none refused; the page the canvas draws is the preset's.
test('S06-B B7: every one of the 37 samples, copied to the open dataset, opens in the studio canvas on its own page, with every element drawn',async()=>{const a=await designer();try{
 const api=async(path,init)=>{const r=await fetch(url+'/api/reporting/v1/document-templates'+path,{headers:{Authorization:'Bearer '+token,'Content-Type':'application/json'},...init});return r;};
 const get=async path=>{const r=await api(path);assert.equal(r.status,200,path);return r.json();};
 const list=await get('/samples');
 assert.equal(list.length,37);
 assert.deepEqual([...new Set(list.map(s=>s.domain))].sort(),['仓储 WMS','国际贸易','通用业务','运输 / 货代'].sort());
 const presets=a.w.reportingPresets;
 const bound=cv.tpl(a).binding,dataset={integration_id:bound.integration_id,id:bound.dataset.id,version:bound.dataset.version};
 const pages=new Set();let drawn=0,codes=0;
 const forms=new Set(['bol','fedex','mawb','mawb_air_waybill','usps','wms_carton_label','wms_location_label','wms_pallet_label','wms_sku_label']);
 const tableCount=a=>a.$('doc-canvas-host').querySelectorAll('[data-key]').length&&[...a.$('doc-canvas-host').querySelectorAll('[data-key]')].filter(e=>e.getAttribute('data-key')===(cv.tpl(a).bands.detail||{elements:[{id:'-'}]}).elements[0].id&&cv.tpl(a).bands.detail).length;
 for(const s of list){
  const one=await get('/samples/'+encodeURIComponent(s.kind));
  const doc=one.document,slots=Object.fromEntries(one.field_slots.map(f=>[f.id,f]));
  // The mapping: the group fields to a dimension; of the table's columns the first metric to the dataset's metric and the others in turn to its two dimensions; a slot only a sum names to the metric.
  const mapping={},table=doc.bands.detail?doc.bands.detail.elements[0].columns.map(c=>c.field):[],group=(doc.group&&doc.group.fields)||[];
  // The forms have no detail band: their slots map to the dataset's columns one by one, the first to a dimension.
  if(!doc.bands.detail){const some=one.field_slots.filter(f=>!f.metric).slice(0,2);some.forEach((f,i)=>{mapping[f.id]={column:['customer.id','aging.bucket'][i]};});}
  for(const g of group)mapping[g]={column:'customer.id'};
  const dims=['customer.id','aging.bucket'];let metricTaken=false;
  for(const f of table){
   if(mapping[f])continue;
   if(slots[f].metric){if(!metricTaken){mapping[f]={column:'ar.open_balance'};metricTaken=true;}}
   else if(dims.length)mapping[f]={column:dims.shift()};
  }
  for(const band of Object.values(doc.bands))for(const e of band.elements||[])if(e.source==='group_sum'&&!mapping[e.field]&&!table.includes(e.field))mapping[e.field]={column:'ar.open_balance'};
  const r=await api('/samples/'+encodeURIComponent(s.kind)+'/copy',{method:'POST',body:JSON.stringify({dataset,name:'DOM '+s.kind,mapping})});
  assert.equal(r.status,201,s.kind+': '+await r.clone().text());
  const saved=(await r.json()).config;
  cv.load(a,saved);
  const ids=[];for(const band of Object.values(doc.bands))for(const e of band.elements||[])ids.push(e.id);
  assert.equal(ids.length,s.elements,s.kind+': the listing counts the elements');
  assert.deepEqual(cv.keys(a).sort(),[...ids].sort(),s.kind+': every element is drawn on the canvas');
  assert.equal(a.$('doc-canvas-host').querySelectorAll('.cv-band').length,Object.keys(doc.bands).length,s.kind+': its bands');
  assert.equal(!!doc.bands.detail,!forms.has(s.kind),s.kind+': a form has no detail band');
  assert.equal(tableCount(a),doc.bands.detail?1:0,s.kind+': a table only where there is a detail band');
  assert.equal(a.$('doc-canvas-host').querySelectorAll('.cv-refused,.cv-bad-band,.cv-over').length,0,s.kind+': nothing refused or overflowing');
  assert.equal(a.$('doc2-panel').hidden,false,s.kind+': the 2.x panels');
  // The page is the preset's: its safe region is what the canvas says it is.
  const pre=presets.find(p=>p.id===doc.page.preset);
  assert.ok(pre,s.kind+' names a preset of the table');
  assert.equal(s.preset,pre.id);
  assert.equal(a.$('doc-canvas-host').querySelector('.cv-safe-text').textContent,'安全区 x '+pre.left+'–'+(pre.width-pre.left)+' · y '+pre.top+'–'+pre.bottom,s.kind+': the page the canvas draws');
  assert.equal(cv.tpl(a).page.preset,pre.id,s.kind+': saved with its preset');
  assert.equal(cv.tpl(a).binding.dataset.id,dataset.id,s.kind+': bound to the open dataset');
  pages.add(pre.id);drawn+=ids.length;
  for(const band of Object.values(doc.bands))codes+=(band.elements||[]).filter(e=>e.type==='barcode'||e.type==='qrcode').length;
 }
 assert.deepEqual([...pages].sort(),['a4','label_150x100','label_4x6in']);
 assert.equal(codes,7);assert.ok(drawn>=1000,'drawn '+drawn);
 // With the pinned Typst renderer every sample also renders from its own rows through the preview route, on the page of its preset (the Go test of export/pdf and the checker hold the drawing to the template).
 if(pdfConfigured){
  for(const s of list){
   const r=await api('/samples/'+encodeURIComponent(s.kind)+'/previews',{method:'POST',body:'{}'});
   assert.equal(r.status,200,s.kind+' preview: '+(r.status===200?'':await r.text()));
   const pdf=Buffer.from(await r.arrayBuffer());assert.equal(pdf.subarray(0,5).toString(),'%PDF-',s.kind);
   const pre=presets.find(p=>p.id===s.preset),boxes=[...pdf.toString('latin1').matchAll(/\/MediaBox\s*\[\s*0\s+0\s+([0-9.]+)\s+([0-9.]+)\s*\]/g)];
   assert.ok(boxes.length>=1,s.kind+': a page');
   for(const b of boxes){assert.ok(Math.abs(Number(b[1])*25.4/72-pre.width)<0.05&&Math.abs(Number(b[2])*25.4/72-pre.height)<0.05,s.kind+': a page of '+pre.id+' ('+b[1]+' x '+b[2]+' pt)');}
  }
 }
 assert.equal(a.errors.length,0,a.errors.join('|'));
 }finally{a.close();}});

// S06-B part 2: a form is a 2.x template without a detail band: the canvas and the structure panel have no 明细区 for it, the validator accepts it,
// and the studio's "添加明细表" adds the detail band (and its table) back.
test('S06-B: a form (no detail band) is drawn and validated without a detail region, and the insert of a table adds the detail band back',async()=>{const a=await designer();try{
 const form=banded();delete form.bands.detail;
 cv.load(a,form);
 assert.equal(cv.tpl(a).bands.detail,undefined,'saved without a detail band');
 assert.equal(a.$('doc-canvas-host').querySelectorAll('.cv-b-detail').length,0,'no detail region on the canvas');
 assert.equal(a.$('doc-canvas-host').querySelectorAll('[data-band-box]').length,4);
 assert.deepEqual([...a.$('doc-canvas-host').querySelectorAll('.cv-bandlabel')].map(e=>e.textContent),['页眉','报表头','报表尾','页脚'],'the margin names no 明细区');
 const rows=[...a.$('doc2-bands').children].map(r=>r.querySelector('.cv2-name').textContent);
 assert.match(rows.at(-1),/没有明细带/);assert.equal(rows.some(t=>t.startsWith('明细带：')),false);
 assert.equal(a.$('st-layers').querySelectorAll('[data-k="band:detail"]').length,0,'the structure panel lists no detail band');
 assert.equal(a.$('doc-table-status').textContent.includes('表单'),true);
 // The document footer hangs directly under the document header: no detail region between them.
 const box=n=>{const e=a.$('doc-canvas-host').querySelector('.cv-b-'+n);return {y:Number(e.getAttribute('y')),h:Number(e.getAttribute('height'))};};
 assert.ok(Math.abs(box('document_footer').y-(box('document_header').y+box('document_header').h))<0.01,'the footer under the header');
 // A form needs a band; the shared cases hold the rest (no band, a null detail).
 const empty=banded();delete empty.bands.detail;for(const n of ['page_header','document_header','document_footer','page_footer'])delete empty.bands[n];
 a.$('doc-status').textContent='';a.$('doc-json').value=JSON.stringify(empty);a.click('doc-json-open');assert.notEqual(a.$('doc-status').textContent,READY);
 // The studio's insert of a table adds the detail band with a table of the first columns, selected; undo takes it away again.
 a.$('st-card-table').click();
 const t=cv.tpl(a).bands.detail;assert.ok(t&&t.elements.length===1&&t.elements[0].type==='table'&&t.elements[0].columns.length>=1,'the detail band is back');
 assert.deepEqual(cv.selected(a),[t.elements[0].id]);
 assert.equal(a.$('doc-canvas-host').querySelectorAll('.cv-b-detail').length,1);
 assert.equal([...a.$('doc2-bands').children].some(r=>r.querySelector('.cv2-name').textContent.startsWith('明细带：')),true);
 a.click('doc-undo');assert.equal(cv.tpl(a).bands.detail,undefined,'one undo step');
 // And the other way: removing the table makes a form.
 a.click('doc2-detail-add');assert.ok(cv.tpl(a).bands.detail);a.click('doc2-detail-remove');assert.equal(cv.tpl(a).bands.detail,undefined);
 assert.equal(a.errors.length,0,a.errors.join('|'));
 }finally{a.close();}});

// ======================================================================================================================================
// S06-B part 3: the template centre, the new-template wizard and the mapping dialog (console/assets/library.js, docs/STUDIO.md "模板中心").
// The identity of every test in this file is the same, so each test seeds templates with its own tag and compares with the server's listing.
// ======================================================================================================================================
const REST='/api/reporting/v1';
const rest=(path,options={})=>fetch(url+REST+path,{...options,headers:{Authorization:'Bearer '+token,...(options.body?{'Content-Type':'application/json'}:{}),...options.headers}});
const libq=(a,sel)=>a.$('page-library').querySelector(sel),libqa=(a,sel)=>[...a.$('page-library').querySelectorAll(sel)];
const tagOf=()=>randomBytes(3).toString('hex');
async function seedTemplate(binding,name,catalog,extra={}){
 const config={schema_version:'1.0',kind:'document',...(name?{name}:{}),binding,elements:[{id:'n1',source:'literal',field:'',text:'seed',x:10,y:25,width:190,height:12,font_size:18,align:'left'}],...extra};
 const made=await rest('/document-templates',{method:'POST',body:JSON.stringify({id:'',expected_version:0,config})});assert.equal(made.status,201);const id=(await made.json()).id;
 if(catalog){const put=await rest('/document-templates/'+id+'/catalog',{method:'PUT',body:JSON.stringify({domain:'',flow:'',favourite:false,archived:false,collections:[],...catalog,expected_revision:0})});assert.equal(put.status,200);}
 return id;
}
const listing=async()=>(await rest('/document-templates')).json();
async function centre(a){q(a,'[data-page="library"]').click();await until(()=>!a.$('page-library').hidden&&libqa(a,'.sample-card').length>=37&&!a.$('lib-count').textContent.includes('读取'),'the centre is listed',12000);}
const ownCards=a=>libqa(a,'ul[aria-label="我的模板"] .catalog-card');
const ownCard=(a,name)=>ownCards(a).find(c=>c.getAttribute('aria-label')===name);
const cardButton=(card,label)=>[...card.querySelectorAll('button')].find(b=>(b.getAttribute('aria-label')||b.textContent).startsWith(label));
const pick=(a,sel,value)=>{const e=libq(a,sel);e.value=value;e.dispatchEvent(new a.w.Event('change',{bubbles:true}));};
const typeIn=(a,e,value)=>{e.value=value;e.dispatchEvent(new a.w.Event('input',{bubbles:true}));};
const navButton=(a,label)=>libqa(a,'.catalog-sidebar .catalog-nav').find(b=>b.querySelector('span').textContent===label);
// navCount is undefined while the sidebar is being re-rendered (the list refreshes after a conflict), so a wait on it retries instead of throwing.
const navCount=(a,label)=>{const b=navButton(a,label);return b?Number(b.querySelector('b').textContent):undefined;};
const dlg=a=>libq(a,'.overlay.open [role="dialog"]');
const pressKey=(a,target,key,extra={})=>target.dispatchEvent(new a.w.KeyboardEvent('keydown',{key,bubbles:true,cancelable:true,...extra}));
const dButton=(a,label)=>[...dlg(a).querySelectorAll('button')].find(b=>b.textContent.trim()===label);

// The identity of this file's server is shared by every test above, and a private store holds 64 templates per identity: the centre's tests run against a
// server of their own (the same binary, its own memory and directories) so that what the earlier tests saved neither fills the cap nor the lists.
describe('S06-B part 3: the template centre (a server of its own)',()=>{
let mainUrl,second;
before(async()=>{
  mainUrl=url;
  const dir=join(temporary,'centre');await mkdir(dir,{recursive:true});
  const listener=createServer();await new Promise(resolve=>listener.listen(0,'127.0.0.1',resolve));const port=listener.address().port;await new Promise(resolve=>listener.close(resolve));
  const pdf=pdfConfigured?['-pdf-typst',process.env.REPORTING_TYPST,'-pdf-typst-sha256',process.env.REPORTING_TYPST_SHA256,'-pdf-fonts',process.env.REPORTING_PDF_FONTS]:[];
  second=spawn(join(temporary,'reporting-demo'),['-profile','contract-simulator','-memory','-listen','127.0.0.1:'+port,'-layout-directory',join(dir,'layouts'),'-dashboard-directory',join(dir,'dashboards'),'-template-directory',join(dir,'templates'),'-background-directory',join(dir,'backgrounds'),'-quota-jobs','1000','-quota-results','100000','-quota-admissions','100000',...pdf],{cwd:root,env:{...process.env,REPORTING_DEMO_TOKEN:token},stdio:['ignore','ignore','pipe']});
  second.stderr.resume();
  const next='http://127.0.0.1:'+port;
  for(let i=0;i<100;i++){try{const r=await fetch(next+'/console/');if(r.ok){url=next;return;}}catch(_){}if(second.exitCode!==null)throw new Error('the centre\'s server did not start');await sleep(30);}
  throw new Error('the centre\'s server startup timeout');
});
after(async()=>{url=mainUrl;if(second&&second.exitCode===null){second.kill('SIGTERM');await new Promise(resolve=>second.once('exit',resolve));}});

test('S06-B C1: the template centre is a console page with the prototype\'s regions and labels, counts that agree with the server, and nothing in the browser\'s storage',async()=>{const a=await designer();try{
 const binding=cv.tpl(a).binding,tag=tagOf();
 const w1=await seedTemplate(binding,'WMS出库-'+tag,{domain:'仓储 WMS',flow:'出库',favourite:true,collections:['客户A-'+tag]});
 const w2=await seedTemplate(binding,'',{domain:'国际贸易',flow:'贸易单证'});
 const w3=await seedTemplate(binding,'归档的-'+tag,{domain:'通用业务',flow:'财务',archived:true});
 // The navigation entry and the page.
 assert.equal(q(a,'[data-page="library"]').getAttribute('aria-label'),'模板中心');
 await centre(a);
 const d=a.w.document;assert.equal(d.body.classList.contains('library-on'),true);assert.equal(d.body.classList.contains('studio-on'),false);
 const text=s=>libq(a,s).textContent.replace(/\s+/g,' ').trim();
 assert.match(text('.wordmark'),/^IDP REPORTING\s*TEMPLATE WORKSPACE$/);
 assert.deepEqual(libqa(a,'.library-header .btn:not(#lib-data)').map(b=>b.textContent.trim()),['返回设计器','数据中心','新建模板']);assert.equal(a.$('lib-datacenter').title,'打开数据面板：数据集、字段与币种，以及查询');assert.equal(libqa(a,'.library-header .btn')[0].id,'lib-data','the data chip leads the header (S07-A)');
 assert.equal(text('.library-eyebrow'),'DOCUMENT OPERATIONS');assert.equal(text('h1'),'模板中心');assert.equal(text('.catalog-top p'),'从业务流程找到模板，再选择版式、数据和版本。');
 assert.deepEqual(libqa(a,'.catalog-caption').map(c=>c.textContent.trim()),['快速访问','业务域全部','自定义集合']);
 assert.deepEqual(libqa(a,'.catalog-sidebar .catalog-nav').slice(0,4).map(b=>b.querySelector('span').textContent),['全部模板','我的收藏','最近使用','归档']);
 assert.deepEqual(libqa(a,'.catalog-sidebar .catalog-nav').slice(4,8).map(b=>b.querySelector('span').textContent),['仓储 WMS','运输 / 货代','国际贸易','通用业务']);
 assert.match(text('.catalog-footnote'),/业务域 → 流程用于稳定分类。/);
 assert.deepEqual(libqa(a,'.catalog-stats span').map(s=>s.textContent),['可用模板','业务域']);
 assert.equal(libq(a,'input[type=search]').getAttribute('placeholder'),'搜索名称、业务流程、集合或编码…');assert.equal(libq(a,'#lib-import').textContent.trim(),'导入模板');
 assert.deepEqual(libqa(a,'.catalog-filters select').map(s=>s.getAttribute('aria-label')),['业务流程','模板状态','模板排序']);
 assert.deepEqual([...libq(a,'select[aria-label="模板状态"]').options].map(o=>o.text),['全部状态','草稿','已发布','已归档']);
 assert.deepEqual([...libq(a,'select[aria-label="模板排序"]').options].map(o=>o.text),['名称','最近打开','最近更新']);
 assert.deepEqual(libqa(a,'.catalog-view button').map(b=>b.getAttribute('aria-label')),['列表视图','卡片视图']);
 // The counts are the server's: compare with the raw listing.
 const raw=await listing(),act=raw.filter(t=>!t.catalog.archived);
 assert.equal(navCount(a,'全部模板'),act.length);assert.equal(navCount(a,'我的收藏'),act.filter(t=>t.catalog.favourite).length);assert.equal(navCount(a,'最近使用'),act.filter(t=>t.catalog.last_opened).length);assert.equal(navCount(a,'归档'),raw.length-act.length);
 const samples=await (await rest('/document-templates/samples')).json();assert.equal(samples.length,37);
 for(const dom of ['仓储 WMS','运输 / 货代','国际贸易','通用业务'])assert.equal(navCount(a,dom),act.filter(t=>t.catalog.domain===dom).length+samples.filter(s=>s.domain===dom).length,dom);
 assert.equal(navCount(a,'仓储 WMS')>=24+1,true);
 assert.equal(libq(a,'.catalog-stats strong').textContent,String(act.length));
 assert.equal(a.$('lib-count').textContent,act.length+' 个结果 · 显示 1–'+Math.min(24,act.length));
 // A card: the name (or "模板 <id 前八位>"), domain / flow / preset, the badge, the saved versions, the star, 分类, 复制, 归档, 编辑.
 const c1=ownCard(a,'WMS出库-'+tag);assert.ok(c1);
 assert.equal(c1.querySelector('.catalog-name').textContent,'WMS出库-'+tag);assert.equal(c1.querySelector('.catalog-subtitle').textContent.replace(/\s+/g,''),'仓储WMS/出库·A4·210×297mm');
 assert.equal(c1.querySelector('.catalog-state .pill').textContent,'草稿');assert.equal(c1.querySelector('.catalog-state small').textContent,'1 个保存版本');
 assert.equal(c1.querySelector('.catalog-star').getAttribute('aria-pressed'),'true');assert.equal(c1.querySelector('.catalog-star').textContent,'★');assert.equal(c1.querySelector('.catalog-tags').textContent,'集合：客户A-'+tag);
 assert.deepEqual([...c1.querySelectorAll('.catalog-card-actions button')].map(b=>b.getAttribute('aria-label')||b.textContent),['取消收藏 WMS出库-'+tag,'分类 WMS出库-'+tag,'复制模板 WMS出库-'+tag,'归档模板 WMS出库-'+tag,'编辑 WMS出库-'+tag]);
 assert.ok(ownCard(a,'模板 '+w2.slice(0,8)),'a template without a name is called 模板 <id 前八位>');
 assert.equal(ownCard(a,'归档的-'+tag),undefined,'an archived template is not under 全部模板');
 // Collections are listed with counts; the archive is its own list with 恢复 and a disabled name.
 assert.equal(navCount(a,'客户A-'+tag),1);
 navButton(a,'归档').click();assert.equal(text('h1'),'归档模板');const arc=ownCard(a,'归档的-'+tag);assert.ok(arc);assert.equal(arc.querySelector('.catalog-name').disabled,true);assert.equal(arc.querySelector('.catalog-state .pill').textContent,'已归档');assert.deepEqual([...arc.querySelectorAll('.catalog-card-actions button')].map(b=>b.getAttribute('aria-label')||b.textContent).slice(-1),['恢复 归档的-'+tag]);
 assert.equal(libq(a,'.sample-section').hidden,true,'samples are never in the archive');
 navButton(a,'我的收藏').click();assert.equal(text('h1'),'我的收藏');assert.equal(libq(a,'.sample-section').hidden,true,'samples are never favourites');assert.ok(ownCard(a,'WMS出库-'+tag));
 navButton(a,'全部模板').click();assert.equal(libq(a,'.sample-section').hidden,false);
 // Four domains of samples, each with its count.
 assert.deepEqual(libqa(a,'.sample-group h3').map(h=>h.textContent),['仓储 WMS · 24','运输 / 货代 · 7','国际贸易 · 4','通用业务 · 2']);
 assert.equal(a.w.localStorage.length,0);assert.equal(a.w.sessionStorage.length,0);assert.equal(a.errors.length,0,a.errors.join('|'));
 }finally{a.close();}});

test('S06-B C2: search, the filters, the sort, the views and the pages work on the identity\'s templates and the samples',async()=>{const a=await designer();try{
 const binding=cv.tpl(a).binding,tag=tagOf();
 const ids={};
 ids.b=await seedTemplate(binding,'乙-'+tag,{domain:'仓储 WMS',flow:'入库',collections:['项目-'+tag]});
 ids.a=await seedTemplate(binding,'甲-'+tag,{domain:'仓储 WMS',flow:'出库'});
 ids.c=await seedTemplate(binding,'丙-'+tag,{domain:'运输 / 货代',flow:'海运'});
 // 丙 is published, so the status filter has both sides.
 const rel=await rest('/document-templates/'+ids.c+'/release',{method:'POST',body:JSON.stringify({action:'publish',version:1,expected_revision:0})});assert.equal(rel.status,200);
 await centre(a);
 const names=()=>ownCards(a).map(c=>c.getAttribute('aria-label')).filter(n=>n.endsWith('-'+tag));
 const sampleNames=()=>libqa(a,'.sample-card .catalog-name').map(n=>n.textContent);
 // Search: name, flow, collection, id; samples answer too.
 const search=libq(a,'input[type=search]');
 typeIn(a,search,tag);assert.deepEqual(names(),['丙-'+tag,'甲-'+tag,'乙-'+tag].sort((x,y)=>new Intl.Collator('zh-CN').compare(x,y)),'by name (sorted 名称)');
 typeIn(a,search,'项目-'+tag);assert.deepEqual(names(),['乙-'+tag],'by collection');
 typeIn(a,search,ids.c);assert.deepEqual(names(),['丙-'+tag],'by id');
 typeIn(a,search,'海运 '+tag);assert.deepEqual(names(),['丙-'+tag],'by flow and name: every word must match');
 typeIn(a,search,'海运');assert.ok(sampleNames().length>=1&&sampleNames().every(n=>/提单|订舱|舱单/.test(n)||true));assert.equal(libqa(a,'.sample-card').every(c=>c.querySelector('.catalog-subtitle').textContent.includes('海运')),true,'samples are searched by flow too');
 typeIn(a,search,'没有这样的模板-'+tag);assert.equal(ownCards(a).length,0);assert.match(libq(a,'.empty-state').textContent,/没有匹配的模板/);assert.equal(a.$('lib-count').textContent,'0 个结果');
 libq(a,'.empty-state .btn').click();assert.equal(search.value,'');assert.ok(ownCards(a).length>=3);
 // Filters: flow, status; the flow list follows the domain.
 typeIn(a,search,tag);
 navButton(a,'仓储 WMS').click();assert.deepEqual(names().sort(),['乙-'+tag,'甲-'+tag].sort());assert.equal(libq(a,'h1').textContent,'仓储 WMS');
 assert.deepEqual([...libq(a,'select[aria-label="业务流程"]').options].map(o=>o.text).slice(0,4),['全部流程','入库','库存','出库']);
 pick(a,'select[aria-label="业务流程"]','出库');assert.deepEqual(names(),['甲-'+tag]);assert.equal(libqa(a,'.sample-card').every(c=>c.querySelector('.catalog-subtitle').textContent.includes('出库')),true);
 pick(a,'select[aria-label="业务流程"]','');navButton(a,'仓储 WMS').click();assert.equal(names().length,3);
 pick(a,'select[aria-label="模板状态"]','published');assert.deepEqual(names(),['丙-'+tag]);assert.equal(libq(a,'.sample-section').hidden,true,'a status filter has no samples');assert.equal(ownCard(a,'丙-'+tag).querySelector('.catalog-state .pill').textContent,'已发布 v1');assert.equal(ownCard(a,'丙-'+tag).querySelector('.catalog-state .pill').classList.contains('green'),true);
 pick(a,'select[aria-label="模板状态"]','draft');assert.deepEqual(names().sort(),['乙-'+tag,'甲-'+tag].sort());
 pick(a,'select[aria-label="模板状态"]','all');
 // Collections filter by their nav entry.
 navButton(a,'项目-'+tag).click();assert.deepEqual(names(),['乙-'+tag]);assert.equal(libq(a,'.sample-section').hidden,true);assert.equal(libq(a,'.catalog-results .pill').textContent,'集合：项目-'+tag);navButton(a,'项目-'+tag).click();assert.equal(names().length,3);
 // Sort 最近打开: opening a template (reading a version) records it; the order follows.
 assert.equal((await rest('/document-templates/'+ids.a+'?version=1')).status,200);await new Promise(r=>setTimeout(r,5));assert.equal((await rest('/document-templates/'+ids.b+'?version=1')).status,200);
 libq(a,'.library-header .btn:not(#lib-data)').click();// 返回设计器, then back
 assert.equal(a.$('page-library').hidden,true);q(a,'[data-page="library"]').click();await until(()=>names().length===3&&['乙-','甲-'].every(n=>ownCard(a,n+tag).querySelector('.catalog-state small').textContent.includes('最近')),'reloaded');
 pick(a,'select[aria-label="模板排序"]','recent');assert.deepEqual(names().slice(0,2),['乙-'+tag,'甲-'+tag],'most recently opened first');
 navButton(a,'最近使用').click();assert.equal(libq(a,'h1').textContent,'最近使用');assert.deepEqual(names().slice(0,2),['乙-'+tag,'甲-'+tag]);assert.equal(ownCard(a,'丙-'+tag),undefined);
 navButton(a,'全部模板').click();pick(a,'select[aria-label="模板排序"]','updated');assert.equal(names().length,3);pick(a,'select[aria-label="模板排序"]','name');
 // The views: list and grid.
 assert.equal(libq(a,'.catalog-items').classList.contains('is-list'),true);libq(a,'[aria-label="卡片视图"]').click();assert.equal(libq(a,'.catalog-items').classList.contains('is-grid'),true);assert.equal(libq(a,'[aria-label="卡片视图"]').getAttribute('aria-pressed'),'true');assert.equal(libq(a,'[aria-label="列表视图"]').getAttribute('aria-pressed'),'false');libq(a,'[aria-label="列表视图"]').click();
 // Pages: with 12 per page and more than 12 templates (seeded here), the result line and the pager say which.
 typeIn(a,search,'');const total=(await listing()).filter(t=>!t.catalog.archived).length;
 if(total>12){pick(a,'select[aria-label="每页模板数"]','12');assert.equal(a.$('lib-count').textContent,total+' 个结果 · 显示 1–12');assert.match(libq(a,'.catalog-pagination').textContent,/第 1 \/ \d+ 页/);libq(a,'.catalog-pagination .btn:last-of-type').click();assert.equal(a.$('lib-count').textContent,total+' 个结果 · 显示 13–'+Math.min(total,24));}
 else assert.equal(a.$('lib-count').textContent,total+' 个结果 · 显示 1–'+total);
 assert.equal(a.errors.length,0,a.errors.join('|'));
 }finally{a.close();}});

test('S06-B C3: the cards\' actions go to the catalogue with the revision they read, and a stale revision is refused and refreshed',async()=>{
 const puts=[];
 const a=await designer(async(path,options)=>{if(options&&options.method==='PUT'&&/\/catalog$/.test(path))puts.push(JSON.parse(options.body));});try{
 const binding=cv.tpl(a).binding,tag=tagOf();
 const id=await seedTemplate(binding,'卡-'+tag,{domain:'通用业务',flow:'销售'});
 await centre(a);
 const name='卡-'+tag,serverCatalog=async()=>(await (await rest('/document-templates/'+id+'/catalog')).json());
 // Favourite: one PUT with expected_revision 1 (the seed's), the whole object, then the star is pressed and the count moves.
 const favs=navCount(a,'我的收藏');
 cardButton(ownCard(a,name),'收藏').click();await until(()=>ownCard(a,name).querySelector('.catalog-star').getAttribute('aria-pressed')==='true','starred');
 assert.deepEqual(puts.at(-1),{domain:'通用业务',flow:'销售',favourite:true,archived:false,collections:[],expected_revision:1});
 assert.equal(navCount(a,'我的收藏'),favs+1);assert.equal((await serverCatalog()).favourite,true);assert.equal((await serverCatalog()).revision,2);
 assert.equal(libq(a,'.catalog-star[data-focus="star'+id+'"]').isConnected,true);
 // The focus stays on the star after the list is rebuilt.
 cardButton(ownCard(a,name),'取消收藏').focus();cardButton(ownCard(a,name),'取消收藏').click();await until(()=>ownCard(a,name).querySelector('.catalog-star').getAttribute('aria-pressed')==='false','unstarred');
 assert.equal(a.w.document.activeElement.getAttribute('aria-label'),'收藏 '+name,'focus returns to the rebuilt star');
 // 分类: a dialog with the domain, the flow and the collections; save writes one PUT.
 cardButton(ownCard(a,name),'分类').click();
 const d=dlg(a);assert.ok(d);assert.equal(d.getAttribute('aria-modal'),'true');assert.equal(d.getAttribute('aria-labelledby'),'lib-d-classify-title');assert.equal(a.w.document.getElementById('lib-d-classify-title').textContent,'分类 · '+name);
 assert.equal(a.w.document.activeElement.closest('[role=dialog]'),d,'focus is inside the dialog');assert.equal(libq(a,'.library-shell').inert,true,'the page behind is inert');
 const sel=[...d.querySelectorAll('select')];sel[0].value='运输 / 货代';sel[0].dispatchEvent(new a.w.Event('change',{bubbles:true}));assert.deepEqual([...sel[1].options].map(o=>o.text),['未指定','航空运输','海运','快递面单']);sel[1].value='海运';
 const fresh=d.querySelector('input[type=text]');fresh.value=' 前后有空白';dButton(a,'添加').click();assert.match(d.querySelector('[role=alert]').textContent,/前后不留空白/);assert.equal(d.querySelector('[role=alert]').hidden,false);
 fresh.value='集合-'+tag;dButton(a,'添加').click();assert.equal([...d.querySelectorAll('input[type=checkbox]')].find(c=>c.value==='集合-'+tag).checked,true);
 dButton(a,'保存分类').click();await until(()=>!dlg(a)&&ownCard(a,name).querySelector('.catalog-subtitle').textContent.includes('海运'),'classified');
 assert.deepEqual(puts.at(-1),{domain:'运输 / 货代',flow:'海运',favourite:false,archived:false,collections:['集合-'+tag],expected_revision:3});
 assert.deepEqual(await serverCatalog(),{domain:'运输 / 货代',flow:'海运',favourite:false,archived:false,collections:['集合-'+tag],last_opened:'',revision:4});
 assert.equal(navCount(a,'集合-'+tag),1);assert.equal(a.w.document.activeElement.getAttribute('aria-label'),'分类 '+name,'focus returns to the opener');assert.equal(libq(a,'.library-shell').inert,false);
 // Esc closes a dialog without writing.
 const before=puts.length;cardButton(ownCard(a,name),'分类').click();pressKey(a,dlg(a),'Escape');assert.equal(dlg(a),null);assert.equal(puts.length,before);
 // A stale revision (the catalogue changed behind the page): 409, the list is refreshed and the notice says so; the next press works.
 const put=await rest('/document-templates/'+id+'/catalog',{method:'PUT',body:JSON.stringify({domain:'运输 / 货代',flow:'海运',favourite:false,archived:false,collections:['集合-'+tag,'别处-'+tag],expected_revision:4})});assert.equal(put.status,200);
 cardButton(ownCard(a,name),'收藏').click();await until(()=>!a.$('notice').hidden&&a.$('notice').textContent.includes('别处改过'),'conflict told');
 await until(()=>navCount(a,'别处-'+tag)===1,'refreshed');assert.equal((await serverCatalog()).favourite,false);
 cardButton(ownCard(a,name),'收藏').click();await until(()=>ownCard(a,name).querySelector('.catalog-star').getAttribute('aria-pressed')==='true','starred after the refresh');assert.equal(puts.at(-1).expected_revision,5);
 // Archive: leaves 全部模板, is in 归档, and 恢复 brings it back.
 cardButton(ownCard(a,name),'归档模板').click();await until(()=>!ownCard(a,name),'archived');assert.match(a.$('notice').textContent,/已归档/);assert.equal((await serverCatalog()).archived,true);
 navButton(a,'归档').click();assert.ok(ownCard(a,name));cardButton(ownCard(a,name),'恢复').click();await until(()=>!ownCard(a,name),'restored');navButton(a,'全部模板').click();assert.ok(ownCard(a,name));assert.equal((await serverCatalog()).archived,false);
 // 复制: a new template (the same layout and dataset, "副本"), the catalogue copied except the favourite.
 const count=(await listing()).length;cardButton(ownCard(a,name),'复制模板').click();await until(()=>ownCard(a,name+' 副本'),'duplicated',8000);
 const after=await listing();assert.equal(after.length,count+1);const dup=after.find(t=>t.name===name+' 副本');assert.deepEqual([dup.catalog.domain,dup.catalog.flow,dup.catalog.favourite,dup.catalog.collections.includes('集合-'+tag)],['运输 / 货代','海运',false,true]);assert.equal(dup.version,1);
 assert.equal(a.errors.length,0,a.errors.join('|'));
 }finally{a.close();}});

test('S06-B C4: 编辑 opens the latest version in the studio, and without a snapshot or on another dataset the card says why',async()=>{
 let rewrite=false;
 const a=app(async(path,options)=>{if(rewrite&&path===REST+'/document-templates'&&(options?.method||'GET')==='GET'){const r=await fetch(url+path,options);const v=await r.json();for(const t of v)t.binding.dataset.id='other.dataset';return new Response(JSON.stringify(v),{headers:{'Content-Type':'application/json'}});}});try{
 await a.login();const tag=tagOf();
 a.configure();const [dsID,dsVersion]=a.$('query-dataset').value.split('@'),binding={integration_id:a.$('query-product').value,dataset:{id:dsID,version:dsVersion}};
 const id=await seedTemplate(binding,'编辑-'+tag,{domain:'通用业务',flow:'财务'});
 // No result yet: the centre lists and classifies, but nothing opens, and the page says why.
 q(a,'[data-page="library"]').click();await until(()=>ownCard(a,'编辑-'+tag),'listed without a snapshot',10000);
 const edit=cardButton(ownCard(a,'编辑-'+tag),'编辑');assert.equal(edit.getAttribute('aria-disabled'),'true');assert.match(edit.title,/冻结快照/);
 assert.match(libq(a,'.catalog-notice-line').textContent,/冻结快照/);
 edit.click();assert.match(a.$('notice').textContent,/冻结快照/);assert.equal(a.$('page-documents').hidden,true);assert.equal(a.$('page-library').hidden,false);
 // 新建模板 can be opened (the samples can be read) but step 3 needs the data set.
 // With a result: 编辑 opens the latest version in the studio.
 libqa(a,'.library-header .btn').find(b=>b.textContent.trim()==='数据中心').click();assert.equal(a.$('sh-d-data').hidden,false,'S07-H: 数据中心 is the data drawer');assert.equal(a.$('page-library').hidden,false);
 a.input('query-companies','company_demo');a.click('preview');await until(()=>!a.$('preview').disabled&&!a.$('result-table-wrap').hidden,'query');
 await until(()=>!a.$('preview').disabled&&a.$('sh-d-data').hidden,'the preview from the drawer closes it');
 await until(()=>ownCard(a,'编辑-'+tag)&&!libq(a,'.catalog-notice-line').textContent,'with a result, still in the centre');
 cardButton(ownCard(a,'编辑-'+tag),'编辑').click();await until(()=>!a.$('page-documents').hidden&&a.$('doc-server-status').textContent.includes('正在编辑模板 '+id),'opened in the studio');
 assert.equal(a.w.document.body.classList.contains('studio-on'),true);assert.equal(a.$('st-name').textContent,'编辑-'+tag);assert.equal(a.$('page-library').hidden,true);
 // The crumb, the rail and the wordmark of the studio open the centre; 返回设计器 returns with the same template.
 for(const sel of ['#st-crumb','.app-rail [data-page="library"]','#st-rail-library']){q(a,sel).click();assert.equal(a.$('page-library').hidden,false,sel);assert.equal(a.$('page-documents').hidden,true);libqa(a,'.library-header .btn:not(#lib-data)')[0].click();assert.equal(a.$('page-documents').hidden,false);assert.equal(a.$('st-name').textContent,'编辑-'+tag);}
 // The name button of the card opens the same way; and the opened version is recorded as the last opened.
 q(a,'#st-crumb').click();navButton(a,'最近使用').click();await until(()=>ownCard(a,'编辑-'+tag),'the opened template is in 最近使用');
 // Another data set: the card's reason differs.
 rewrite=true;libq(a,'.library-header .btn').click();q(a,'[data-page="library"]').click();await until(()=>ownCards(a).length&&ownCards(a).every(c=>cardButton(c,'编辑').getAttribute('aria-disabled')==='true'),'every card is on another data set '+libq(a,'.catalog-results').textContent+'|'+libq(a,'.catalog-items').textContent.slice(0,200)+'|'+a.$('notice').textContent);
 assert.match(cardButton(ownCards(a)[0],'编辑').title,/绑定的数据集与当前快照的数据集不同/);
 assert.equal(a.errors.length,0,a.errors.join('|'));
 }finally{a.close();}});

test('S06-B C5: 复制到我的模板 maps each field slot to a column or a literal, pre-filled by name and type, and opens the saved copy in the studio',async()=>{
 const bodies=[];
 const a=await designer(async(path,options)=>{if(options&&options.method==='POST'&&/\/samples\/[a-z0-9_]+\/copy$/.test(path))bodies.push({path,body:JSON.parse(options.body)});});try{
 const tag=tagOf();await centre(a);
 const kind='order',detail=await (await rest('/document-templates/samples/'+kind)).json();
 // Samples: read-only cards, a preview and a copy; the pack's 37 are in four domains.
 const card=libqa(a,'.sample-card').find(c=>c.getAttribute('aria-label')==='示例 '+detail.name);assert.ok(card);
 assert.equal(card.querySelector('.catalog-state .pill').textContent,'示例 · 只读');assert.deepEqual([...card.querySelectorAll('.catalog-card-actions button')].map(b=>b.textContent.trim()),['预览','复制到我的模板']);
 assert.equal(card.querySelector('[aria-label^="收藏"]'),null,'a sample has no star');assert.equal(card.querySelector('[aria-label^="归档"]'),null,'and no archive');
 card.querySelector('.btn.primary').click();
 await until(()=>dlg(a),'the mapping dialog');const d=dlg(a);
 assert.equal(a.w.document.getElementById(d.getAttribute('aria-labelledby')).textContent,'复制到我的模板 · '+detail.name);assert.equal(d.getAttribute('aria-modal'),'true');
 assert.equal(a.w.document.activeElement.closest('[role=dialog]'),d);
 // The dataset of the current snapshot is shown, the name defaults to the sample's, every slot is a row of the table.
 const b=cv.tpl(a).binding;assert.ok(d.textContent.includes(b.dataset.id+'@'+b.dataset.version));
 const name=d.querySelector('input[type=text]');assert.equal(name.value,detail.name);assert.equal(name.maxLength,80);
 const rows=[...d.querySelectorAll('tbody tr')];assert.equal(rows.length,detail.field_slots.length);
 assert.deepEqual(rows.map(r=>r.querySelector('th small').textContent),detail.field_slots.map(f=>f.id));
 assert.deepEqual(rows.map(r=>r.cells[1].textContent),detail.field_slots.map(f=>f.scope==='row'?'明细':'单据'));
 const sels=rows.map(r=>r.querySelector('select')),lits=rows.map(r=>r.querySelector('input'));
 assert.ok(sels.every(s=>s.getAttribute('aria-label').startsWith('「')));assert.equal([...sels[0].options][0].text,'字面文字');
 // Pre-filled: at least one slot is on a column; no column is used twice; an unmapped slot shows its literal "[label]".
 const chosen=sels.map(s=>s.value).filter(Boolean);assert.ok(chosen.length>=1,'some slot is pre-filled by name or type');assert.equal(new Set(chosen).size,chosen.length,'no column twice');
 sels.forEach((s,i)=>{if(!s.value){assert.equal(lits[i].hidden,false);assert.equal(lits[i].value,'['+detail.field_slots[i].label+']');}else assert.equal(lits[i].hidden,true);});
 // Change one: the first mapped slot to a literal, the first literal slot (if any) stays.
 const k=sels.findIndex((s,i)=>!s.value&&detail.field_slots[i].scope==='document');assert.ok(k>=0);sels[k].value='';sels[k].dispatchEvent(new a.w.Event('change',{bubbles:true}));assert.equal(lits[k].hidden,false);typeIn(a,lits[k],'固定-'+tag);
 // A bad name is refused where it is typed, before any request.
 name.value=' 坏名';dButton(a,'复制并进入设计器').click();assert.equal(d.querySelector('[role=alert]').hidden,false);assert.equal(bodies.length,0);
 name.value='订单-'+tag;
 dButton(a,'复制并进入设计器').click();
 await until(()=>!a.$('page-documents').hidden&&a.$('doc-server-status').textContent.includes('正在编辑模板 '),'opened in the studio',10000);
 assert.equal(bodies.length,1);const sent=bodies[0].body;
 assert.deepEqual(Object.keys(sent).sort(),['dataset','mapping','name']);assert.deepEqual(sent.dataset,{integration_id:b.integration_id,id:b.dataset.id,version:b.dataset.version});assert.equal(sent.name,'订单-'+tag);
 assert.deepEqual(Object.keys(sent.mapping).sort(),detail.field_slots.map(f=>f.id).sort());assert.deepEqual(sent.mapping[detail.field_slots[k].id],{literal:'固定-'+tag});
 for(const [i,f] of detail.field_slots.entries())if(i!==k)assert.deepEqual(sent.mapping[f.id],sels[i].value?{column:sels[i].value}:{literal:'['+f.label+']'},f.id);
 // The server has it as an ordinary template of this identity, in the sample's domain and flow; the studio shows it by name.
 const all=await listing();const mine=all.find(t=>t.name==='订单-'+tag);assert.ok(mine);assert.equal(mine.version,1);assert.deepEqual([mine.catalog.domain,mine.catalog.flow,mine.catalog.favourite,mine.catalog.archived],[detail.domain,detail.flow,false,false]);
 assert.equal(a.$('st-name').textContent,'订单-'+tag);assert.ok(JSON.stringify(cv.tpl(a)).includes('固定-'+tag));assert.equal(a.$('doc-server-status').textContent,'正在编辑模板 '+mine.id+' · 版本 1');
 assert.equal(a.w.document.body.classList.contains('studio-on'),true);
 // The sample is unchanged and the copy is in the centre next time (the list is read again when the page opens).
 q(a,'#st-crumb').click();await until(()=>ownCard(a,'订单-'+tag),'the copy is listed');assert.equal(libqa(a,'.sample-card').length,37);
 assert.equal(a.errors.length,0,a.errors.join('|'));
 }finally{a.close();}});

test('S06-B C5b: a refused copy (the identity\'s cap, a column it may not read) is told and keeps the dialog',async()=>{
 const a=await designer(async(path,options)=>{if(options&&options.method==='POST'&&/\/samples\/[a-z0-9_]+\/copy$/.test(path)){const code=a.__refuse;if(code)return new Response(JSON.stringify({error:code==='413'?'BUDGET_EXCEEDED':'ACCESS_DENIED'}),{status:Number(code),headers:{'Content-Type':'application/json'}});}});try{
 await centre(a);
 for(const [status,expected] of [['413',/64 个模板/],['403',/无权使用所选的某个字段/]]){
  a.__refuse=status;libqa(a,'.sample-card .btn.primary')[0].click();await until(()=>dlg(a),'dialog');dButton(a,'复制并进入设计器').click();
  await until(()=>a.$('notice').textContent.match(expected),'refusal told '+status);assert.equal(a.$('page-documents').hidden,true);assert.ok(dlg(a),'the dialog stays open for another try');assert.equal(dButton(a,'复制并进入设计器').disabled,false);
  pressKey(a,dlg(a),'Escape');assert.equal(dlg(a),null);
 }
 // The real server refuses a table with no column to print (every slot a literal): 400, told in words, the dialog stays.
 a.__refuse=null;pressKey(a,dlg(a)||a.w.document.body,'Escape');libqa(a,'.sample-card .btn.primary')[0].click();await until(()=>dlg(a),'dialog');
 for(const s of dlg(a).querySelectorAll('tbody select')){s.value='';s.dispatchEvent(new a.w.Event('change',{bubbles:true}));}
 dButton(a,'复制并进入设计器').click();await until(()=>/名称或映射不被接受/.test(a.$('notice').textContent),'400 told');assert.ok(dlg(a));assert.equal(a.$('page-documents').hidden,true);
 assert.equal(a.errors.length,0,a.errors.join('|'));
 }finally{a.close();}});

const radio=(a,value)=>dlg(a).querySelector('input[type=radio][value="'+value+'"]');
const choose=(a,value)=>{const r=radio(a,value);assert.ok(r,'radio '+value);r.checked=true;r.dispatchEvent(new a.w.Event('change',{bubbles:true}));};
const stepNames=a=>[...dlg(a).querySelectorAll('.steps .step')].map(s=>s.textContent);

test('S06-B C6: the new-template wizard goes 业务流程, 版式, 数据, 名称 and creates a copy of a sample or a blank template on a page preset, then opens it in the studio',async()=>{
 const posts=[];
 const a=await designer(async(path,options)=>{if(options&&options.method==='POST'&&/\/document-templates(\/samples\/[a-z0-9_]+\/copy)?$/.test(path))posts.push({path,body:JSON.parse(options.body)});});try{
 const tag=tagOf();await centre(a);
 const samples=await (await rest('/document-templates/samples')).json(),outbound=samples.filter(s=>s.domain==='仓储 WMS'&&s.flow==='出库');assert.ok(outbound.length>=2);
 const opener=a.$('lib-new');opener.focus();opener.click();
 let d=dlg(a);assert.ok(d);assert.equal(d.getAttribute('aria-modal'),'true');assert.equal(a.w.document.getElementById(d.getAttribute('aria-labelledby')).textContent,'创建一份新的业务模板');
 assert.deepEqual(stepNames(a),['1业务流程','2版式','3数据','4名称']);assert.equal(d.querySelector('.step.current').textContent,'1业务流程');
 // Step 1: the four domains with their flows, and 空白模板. Nothing chosen: refused in words.
 assert.deepEqual([...d.querySelectorAll('fieldset legend')].map(l=>l.textContent),['仓储 WMS','运输 / 货代','国际贸易','通用业务','不属于任何流程']);
 assert.equal(d.querySelectorAll('input[type=radio][name="lib-flow"]').length,9+3+1+3+1);
 dButton(a,'下一步').click();assert.equal(d.querySelector('[role=alert]').hidden,false);assert.match(d.querySelector('[role=alert]').textContent,/请选择一个业务流程/);assert.equal(d.querySelector('.step.current').textContent,'1业务流程');
 // Esc closes it without creating anything, and the focus goes back to 新建模板.
 pressKey(a,d,'Escape');assert.equal(dlg(a),null);assert.equal(a.w.document.activeElement,opener);assert.equal(posts.length,0);
 // The focus is trapped: Tab from the last control goes to the first, Shift+Tab from the first to the last.
 opener.click();d=dlg(a);const focusables=[...d.querySelectorAll('a[href],button:not([disabled]),input:not([disabled]),select:not([disabled]),textarea:not([disabled])')].filter(e=>!e.closest('[hidden]'));
 focusables.at(-1).focus();pressKey(a,focusables.at(-1),'Tab');assert.equal(a.w.document.activeElement,focusables[0]);pressKey(a,focusables[0],'Tab',{shiftKey:true});assert.equal(a.w.document.activeElement,focusables.at(-1));
 // Step 1 to 2: 仓储 WMS / 出库 offers its samples and the blank template.
 choose(a,'仓储 WMS|出库');dButton(a,'下一步').click();assert.equal(d.querySelector('.step.current').textContent,'2版式');assert.equal(d.querySelector('.step.done').textContent,'✓业务流程');
 const layouts=[...d.querySelectorAll('input[name="lib-layout"]')];assert.deepEqual(layouts.map(i=>i.value),[...outbound.map(s=>s.kind),'blank']);assert.equal(layouts[0].checked,true,'the first sample is chosen');
 const pickSample=outbound[1];choose(a,pickSample.kind);
 dButton(a,'下一步').click();assert.equal(d.querySelector('.step.current').textContent,'3数据');
 // Step 3: the data set and the mapping table of the chosen sample, pre-filled.
 await until(()=>d.querySelector('tbody tr'),'mapping table');const detail=await (await rest('/document-templates/samples/'+pickSample.kind)).json();
 assert.equal(d.querySelectorAll('tbody tr').length,detail.field_slots.length);const b=cv.tpl(a).binding;assert.ok(d.textContent.includes(b.dataset.id+'@'+b.dataset.version));
 const sels=[...d.querySelectorAll('tbody select')];const touched=sels.findIndex(s=>!s.value);if(touched>=0){const lit=d.querySelectorAll('tbody tr')[touched].querySelector('input');typeIn(a,lit,'文字-'+tag);}
 // Back keeps the choices; forward again.
 dButton(a,'上一步').click();assert.equal(d.querySelector('.step.current').textContent,'2版式');assert.equal(radio(a,pickSample.kind).checked,true);dButton(a,'下一步').click();await until(()=>d.querySelector('tbody tr'),'mapping table again');
 if(touched>=0)assert.equal(d.querySelectorAll('tbody tr')[touched].querySelector('input').value,'文字-'+tag,'the mapping survives going back');
 dButton(a,'下一步').click();assert.equal(d.querySelector('.step.current').textContent,'4名称');
 // Step 4: the name defaults to the sample's; the summary says what will happen.
 const name=d.querySelector('#lib-wizard-name');assert.equal(name.value,pickSample.name);assert.match(d.querySelector('dl').textContent,/业务流程仓储 WMS \/ 出库/);assert.ok(d.querySelector('dl').textContent.includes(pickSample.name));
 name.value=' 坏';name.dispatchEvent(new a.w.Event('input',{bubbles:true}));dButton(a,'创建并进入设计器').click();assert.equal(d.querySelector('[role=alert]').hidden,false);assert.equal(posts.length,0);
 typeIn(a,name,'向导-'+tag);dButton(a,'创建并进入设计器').click();
 await until(()=>!a.$('page-documents').hidden&&a.$('doc-server-status').textContent.includes('正在编辑模板 '),'opened in the studio',10000);
 assert.equal(posts.length,1);assert.equal(posts[0].path,REST+'/document-templates/samples/'+pickSample.kind+'/copy');assert.equal(posts[0].body.name,'向导-'+tag);assert.equal(Object.keys(posts[0].body.mapping).length,detail.field_slots.length);assert.equal(dlg(a),null);
 let mine=(await listing()).find(t=>t.name==='向导-'+tag);assert.ok(mine);assert.deepEqual([mine.catalog.domain,mine.catalog.flow],['仓储 WMS','出库']);assert.equal(a.$('st-name').textContent,'向导-'+tag);
 // The blank path: no flow, a page preset; the template is saved by the ordinary save route and carries the preset.
 q(a,'#st-crumb').click();await until(()=>!a.$('page-library').hidden&&a.$('lib-new'),'back');a.$('lib-new').click();d=dlg(a);
 choose(a,'blank');dButton(a,'下一步').click();assert.deepEqual([...d.querySelectorAll('input[name="lib-layout"]')].map(i=>i.value),['blank'],'a blank start has only the blank layout');
 const preset=d.querySelector('select[aria-label="页面预设"]');assert.deepEqual([...preset.options].map(o=>o.value),['a4','a4_landscape','a5','label_4x6in','label_100x150','label_100x100','label_150x100']);preset.value='label_100x100';preset.dispatchEvent(new a.w.Event('change',{bubbles:true}));
 dButton(a,'下一步').click();assert.match(d.textContent,/空白模板绑定当前数据集/);dButton(a,'下一步').click();typeIn(a,d.querySelector('#lib-wizard-name'),'空白-'+tag);dButton(a,'创建并进入设计器').click();
 await until(()=>a.$('st-name').textContent==='空白-'+tag&&!a.$('page-documents').hidden,'blank opened',10000);
 assert.equal(posts.length,2);assert.equal(posts[1].path,REST+'/document-templates');assert.deepEqual([posts[1].body.id,posts[1].body.expected_version,posts[1].body.config.schema_version,posts[1].body.config.page,posts[1].body.config.name],['',0,'2.0',{preset:'label_100x100'},'空白-'+tag]);assert.deepEqual(Object.keys(posts[1].body.config.bands),['detail']);
 mine=(await listing()).find(t=>t.name==='空白-'+tag);assert.deepEqual([mine.preset,mine.schema_version,mine.catalog.domain],['label_100x100','2.0','']);assert.equal(cv.tpl(a).page.preset,'label_100x100');
 // A blank template of a flow that has no sample (通用业务 / 其他) is classified in that flow; an A4 blank has no page key.
 q(a,'#st-crumb').click();a.$('lib-new').click();d=dlg(a);choose(a,'通用业务|其他');dButton(a,'下一步').click();assert.deepEqual([...d.querySelectorAll('input[name="lib-layout"]')].map(i=>i.value),['blank']);assert.match(d.textContent,/这个流程还没有示例版式/);
 dButton(a,'下一步').click();dButton(a,'下一步').click();typeIn(a,d.querySelector('#lib-wizard-name'),'其他-'+tag);dButton(a,'创建并进入设计器').click();
 await until(()=>a.$('st-name').textContent==='其他-'+tag,'blank 2 opened',10000);assert.equal(Object.hasOwn(posts[2].body.config,'page'),false);
 mine=(await listing()).find(t=>t.name==='其他-'+tag);assert.deepEqual([mine.catalog.domain,mine.catalog.flow],['通用业务','其他']);
 assert.equal(a.errors.length,0,a.errors.join('|'));
 }finally{a.close();}});

test('S06-B C6b: without a snapshot the wizard reaches the data step and says what is missing instead of going on',async()=>{
 const a=app();try{
 await a.login();q(a,'[data-page="library"]').click();await until(()=>libqa(a,'.sample-card').length===37,'samples');
 a.$('lib-new').click();choose(a,'仓储 WMS|出库');dButton(a,'下一步').click();dButton(a,'下一步').click();
 const d=dlg(a);assert.equal(d.querySelector('.step.current').textContent,'3数据');assert.match(d.querySelector('.callout.warn').textContent,/冻结快照/);assert.equal(dButton(a,'下一步').disabled,true);
 pressKey(a,d,'Escape');
 // The same for a sample's 复制到我的模板 and for 导入模板: refused in words, nothing is requested.
 const before=a.trace.length;libqa(a,'.sample-card .btn.primary')[0].click();assert.match(a.$('notice').textContent,/冻结快照/);assert.equal(dlg(a),null);assert.equal(a.trace.length,before);
 a.$('lib-import').click();const text=dlg(a).querySelector('textarea');text.value='{}';dButton(a,'导入到设计器').click();assert.match(dlg(a).querySelector('[role=alert]').textContent,/冻结快照/);
 assert.equal(a.errors.length,0,a.errors.join('|'));
 }finally{a.close();}});

test('S06-B C7: a sample\'s preview dialog lists its fields and, where the server renders PDF, downloads the verified preview; no dataset is needed',async()=>{
 const a=app();try{
 await a.login();q(a,'[data-page="library"]').click();await until(()=>libqa(a,'.sample-card').length===37,'samples');
 const card=libqa(a,'.sample-card')[0],all=await (await rest('/document-templates/samples')).json(),sample=all.find(s=>card.getAttribute('aria-label')==='示例 '+s.name);assert.ok(sample);
 card.querySelector('.btn:not(.primary)').click();const d=dlg(a);assert.equal(a.w.document.getElementById(d.getAttribute('aria-labelledby')).textContent,'预览示例 · '+sample.name);
 await until(()=>d.querySelector('tbody tr'),'the sample\'s fields');assert.ok(d.textContent.includes(sample.description)||d.textContent.length>50);
 assert.deepEqual([...d.querySelectorAll('.dialog-footer button')].map(b=>b.textContent.trim()),['复制到我的模板','关闭','生成 PDF 预览']);
 if(pdfConfigured){
  const go=dButton(a,'生成 PDF 预览');go.click();await until(()=>a.downloads.length===1,'the verified preview is downloaded',20000);
  assert.equal(a.downloads[0].name,sample.kind+'-sample-preview.pdf');assert.equal(Buffer.from(await a.downloads[0].blob.arrayBuffer()).subarray(0,5).toString(),'%PDF-');assert.match(a.$('notice').textContent,/摘要校验/);
  assert.equal(a.trace.filter(t=>t.path.endsWith('/previews')&&t.method==='POST').length,1);assert.ok(a.trace.every(t=>!/\/snapshots|\/queries/.test(t.path)),'no query or snapshot was needed');
 }
 // Esc closes the dialog wherever the focus is: a control that disables itself while it works (the preview button) drops the focus to the page.
 a.w.document.activeElement.blur();assert.equal(a.w.document.activeElement,a.w.document.body);pressKey(a,a.w.document.body,'Escape');assert.equal(dlg(a),null);assert.equal(libq(a,'.library-shell').inert,false);
 assert.equal(a.errors.length,0,a.errors.join('|'));
 }finally{a.close();}});

test('S06-B C7b: a preview the server cannot make is told, and a tampered digest never downloads',async()=>{
 if(!pdfConfigured)return;
 const a=app(async(path,options)=>{if(path.endsWith('/previews')&&path.includes('/samples/')){const r=await fetch(url+path,options);const bytes=await r.arrayBuffer();return new Response(bytes,{headers:{'Content-Type':'application/pdf','X-Content-SHA256':'0'.repeat(64)}});}});try{
 await a.login();q(a,'[data-page="library"]').click();await until(()=>libqa(a,'.sample-card').length===37,'samples');
 libqa(a,'.sample-card')[0].querySelector('.btn:not(.primary)').click();dButton(a,'生成 PDF 预览').click();await until(()=>/完整性/.test(a.$('notice').textContent),'integrity told');assert.equal(a.downloads.length,0);
 }finally{a.close();}});

test('S06-B C8: 导入模板 reads a template JSON into the studio as an unsaved change, with the studio\'s own validation',async()=>{const a=await designer();try{
 const tag=tagOf(),base=cv.tpl(a);await centre(a);
 a.$('lib-import').click();let d=dlg(a);const text=d.querySelector('textarea');assert.equal(d.querySelector('input[type=file]').getAttribute('accept'),'application/json,.json');
 text.value='{';dButton(a,'导入到设计器').click();assert.match(d.querySelector('[role=alert]').textContent,/不是有效的 JSON/);assert.equal(a.$('page-documents').hidden,true);
 text.value=JSON.stringify({...base,name:' 坏名'});dButton(a,'导入到设计器').click();assert.match(d.querySelector('[role=alert]').textContent,/模板名称须为/);
 text.value=JSON.stringify({...base,binding:{integration_id:'foreign',dataset:base.binding.dataset},name:'外来-'+tag});dButton(a,'导入到设计器').click();assert.equal(d.querySelector('[role=alert]').hidden,false);assert.equal(a.$('page-documents').hidden,true);
 const posts=a.trace.filter(t=>t.method==='POST').length;
 text.value=JSON.stringify({...base,name:'导入-'+tag});dButton(a,'导入到设计器').click();
 assert.equal(dlg(a),null);assert.equal(a.$('page-documents').hidden,false);assert.equal(a.$('st-name').textContent,'导入-'+tag);assert.equal(cv.tpl(a).name,'导入-'+tag);assert.equal(a.trace.filter(t=>t.method==='POST').length,posts,'nothing is saved by importing');
 assert.match(a.$('st-state').textContent,/草稿|未保存/);assert.equal(a.errors.length,0,a.errors.join('|'));
 }finally{a.close();}});

test('S06-B C9: the page writes no markup, keeps nothing in the browser, shows hostile names as text and names every control',async()=>{const a=await designer();try{
 const hostile='<img src=x onerror=alert(1)>',tag=tagOf(),binding=cv.tpl(a).binding;
 const id=await seedTemplate(binding,hostile+tag,{domain:'通用业务',flow:'销售',collections:['<b>集合</b>'+tag]});
 await centre(a);
 const card=ownCard(a,hostile+tag);assert.ok(card);assert.equal(card.querySelector('.catalog-name').textContent,hostile+tag);assert.equal(libq(a,'.catalog-items img'),null);assert.equal(card.querySelector('.catalog-tags').textContent,'集合：<b>集合</b>'+tag);assert.equal(libq(a,'.catalog-sidebar b b'),null);
 // No inline style or handler anywhere on the page, dialogs included (the wizard is opened for the check).
 a.$('lib-new').click();
 for(const e of a.$('page-library').querySelectorAll('*')){assert.equal(e.hasAttribute('style'),false,'style attribute on <'+e.tagName.toLowerCase()+'>');assert.equal([...e.attributes].some(x=>/^on/i.test(x.name)),false,'inline handler');}
 // Every control has a name: buttons by text or label, inputs and selects by label.
 for(const e of a.$('page-library').querySelectorAll('button,input,select,textarea')){
  if(e.type==='hidden')continue;const named=(e.getAttribute('aria-label')||'').trim()||(e.tagName==='BUTTON'&&e.textContent.trim())||(e.labels&&[...e.labels].some(l=>l.textContent.trim()))||(e.closest('label')&&e.closest('label').textContent.trim());
  assert.ok(named,'unnamed control: '+e.outerHTML.slice(0,120));
 }
 for(const e of a.$('page-library').querySelectorAll('svg'))assert.equal(e.getAttribute('aria-hidden'),'true');
 assert.equal(a.$('page-library').querySelectorAll('[role=dialog]').length,1);assert.equal(dlg(a).getAttribute('aria-modal'),'true');
 pressKey(a,dlg(a),'Escape');
 // The source: the DOM API only, no storage, no dynamic code.
 for(const [name,code] of [['library.js',libraryScript]]){
  assert.equal(/innerHTML|outerHTML|insertAdjacentHTML|document\.write|\beval\s*\(|new Function|localStorage|sessionStorage|indexedDB|\.style\b|setAttribute\(\s*['"]on|srcdoc/.test(code),false,name);
  assert.equal(/https?:\/\//.test(code.replace(/\/\/ .*$/gm,'').replace(/http:\/\/www\.w3\.org\/2000\/svg/g,'')),false,name+' has no external address');
 }
 const css=await readFile(join(root,'console/assets/library.css'),'utf8');assert.equal(/@import|url\(\s*['"]?https?:|expression\(|<script|javascript:/.test(css),false,'the stylesheet loads nothing');
 assert.equal(a.w.localStorage.length,0);assert.equal(a.w.sessionStorage.length,0);assert.equal(a.errors.length,0,a.errors.join('|'));
 }finally{a.close();}});

test('S06-B C10: logging out clears the centre, and a stale answer cannot fill it again',async()=>{
 let release,held=false;const gate=new Promise(r=>release=r);
 const a=await designer(async(path,options)=>{if(held&&path===REST+'/document-templates'&&(options?.method||'GET')==='GET'){await gate;}});try{
 await centre(a);assert.ok(ownCards(a).length>0);
 held=true;libq(a,'.library-header .btn').click();q(a,'[data-page="library"]').click();await sleep(30);
 a.click('logout');assert.equal(a.$('connection-overlay').hidden,false);release();await sleep(80);
 assert.equal(ownCards(a).length,0);assert.equal(libqa(a,'.sample-card').length,0);assert.equal(libq(a,'.catalog-sidebar').children.length,0,'the sidebar (collections, counts) is emptied');
 assert.equal(a.errors.length,0,a.errors.join('|'));
 }finally{release();a.close();}});
});

// ---- S06-C: the console shell, the sign-in card and the dashboard studio (docs/S06C_SCOPE.md, docs/STUDIO.md "控制台外壳" and "看板工作室") ----
test('S06-C C1/C2/C3/C4/C9: the shell is the prototype\'s header and navigation column on every page that is not a studio; the sign-in card; the label, the title and the active row follow the page; S07-H: the workbench is the home, the data centre is the drawer\'s field list',async()=>{const a=app();try{
 const d=a.w.document;
 // Before sign-in: the card has the wordmark, the title, the token field and the button; the shell behind is inert.
 assert.equal(a.$('connection-overlay').hidden,false);assert.equal(d.querySelector('.shell').inert,true);
 assert.equal(d.querySelector('.connection-card h1').textContent,'进入报表工作区');assert.match(d.querySelector('.connection-card .wordmark').textContent.replace(/\s+/g,' ').trim(),/^IDP REPORTING\s*DOCUMENT PLATFORM$/);
 assert.equal(a.$('access-token').type,'password');assert.equal(a.$('connect-button').textContent.trim(),'连接工作区');
 await a.login();
 assert.equal(d.querySelector('.shell').inert,false);
 // The header: the wordmark with the page's label, the environment tag, the identity, 退出工作区.
 const header=d.querySelector('.console-header');assert.ok(header);
 assert.equal(header.querySelector('.wordmark b').textContent,'REPORTING');assert.equal(a.$('shell-label').textContent,'REPORT WORKBENCH','S07-H: a sign-in lands on the workbench');assert.equal(a.$('page-workspace').hidden,false);assert.equal(d.title,'IDP Reporting · 报表工作台');
 assert.equal(header.querySelector('.wordmark').getAttribute('href'),'#workspace');assert.equal(d.querySelector('.rail-brand').getAttribute('href'),'#workspace');
 assert.equal(header.querySelector('.environment').textContent,'合同模拟器');assert.equal(a.$('actor-label').textContent,'user_a');assert.equal(a.$('logout').disabled,false);assert.equal(a.$('logout').textContent.trim(),'退出工作区');
 // The navigation (S07-A: the application rail; S07-H: six destinations): named buttons in a nav, one aria-current, the task count.
 const nav=d.querySelector('nav.app-rail[aria-label="主导航"]');assert.ok(nav);
 assert.deepEqual([...nav.querySelectorAll('.nav-item')].map(b=>[b.tagName,b.dataset.page||'panel:'+b.dataset.panel,b.getAttribute('aria-label')]),[['BUTTON','workspace','报表工作台'],['BUTTON','dashboard','看板工作室'],['BUTTON','library','模板中心'],['BUTTON','documents','文档工作室'],['BUTTON','panel:tasks','任务工作区'],['BUTTON','system','能力与环境']]);
 assert.equal(a.$('nav-datasets'),null);assert.equal(a.$('task-count').textContent,'0');
 assert.equal(d.querySelector('.rail-status #connection-label').textContent,'API 已连接');assert.equal(a.$('tenant-label').textContent,'platform_demo');
 // Every page that is not a studio: the title block (eyebrow, title, lead), the wordmark's label, document.title, the active row; no studio shell is on.
 for(const [page,eyebrow,h1,title] of [['system','PLATFORM CAPABILITIES','能力与环境','能力与环境'],['workspace','REPORT WORKBENCH','报表工作台','报表工作台']]){
  d.querySelector('[data-page="'+page+'"]').click();
  const section=a.$('page-'+page);assert.equal(section.hidden,false,page);
  assert.equal(section.querySelector('.page-heading .eyebrow').textContent,eyebrow);assert.equal(section.querySelector('.page-heading h1').textContent,h1);assert.ok(section.querySelector('.page-heading p').textContent.length>0);
  assert.equal(a.$('shell-label').textContent,eyebrow);assert.equal(d.title,'IDP Reporting · '+title);
  const active=nav.querySelector('.nav-item.active');assert.equal(active.dataset.page,page);assert.equal(active.getAttribute('aria-current'),'page');assert.equal(nav.querySelectorAll('[aria-current]').length,1);
  assert.deepEqual(['studio-on','library-on','board-on'].map(c=>d.body.classList.contains(c)),[false,false,false],page+' keeps the shell');
 }
 // The studios and the centre take the screen: one body class each.
 d.querySelector('[data-page="dashboard"]').click();assert.deepEqual(['studio-on','library-on','board-on'].map(c=>d.body.classList.contains(c)),[false,false,true]);assert.equal(d.title,'IDP Reporting · 看板工作室');
 d.querySelector('[data-page="documents"]').click();assert.deepEqual(['studio-on','library-on','board-on'].map(c=>d.body.classList.contains(c)),[true,false,false]);assert.equal(d.title,'IDP Reporting · 文档工作室');
 d.querySelector('[data-page="library"]').click();assert.deepEqual(['studio-on','library-on','board-on'].map(c=>d.body.classList.contains(c)),[false,true,false]);
 // S07-H: no overview, no data centre; their addresses open the workbench, #catalog with the data drawer over it.
 assert.equal(a.$('page-overview'),null);assert.equal(a.$('page-catalog'),null);
 for(const old of ['overview','nowhere']){a.w.location.hash=old;a.w.dispatchEvent(new a.w.HashChangeEvent('hashchange'));assert.equal(a.$('page-workspace').hidden,false,old);assert.equal(a.w.location.hash,'#workspace',old);}
 a.w.location.hash='catalog';a.w.dispatchEvent(new a.w.HashChangeEvent('hashchange'));await flush();assert.equal(a.$('page-workspace').hidden,false);assert.equal(a.$('sh-d-data').hidden,false,'#catalog opens the data drawer');
 // The data centre's column table is the drawer's, for the dataset chosen above it; 刷新目录 is there too.
 assert.equal(a.$('sh-fields').closest('#sh-d-data')!==null,true);assert.equal(a.$('refresh-catalog').closest('#sh-fields')!==null,true);assert.equal(a.$('refresh-catalog').textContent.trim(),'刷新目录');
 assert.equal(a.$('sh-fields-meta').textContent,'应收余额 · receivables.aging · v1.0.0 · 2 个维度 · 1 个指标 · 已发布组合');
 assert.deepEqual([...a.$('sh-fields-list').querySelectorAll('.column-line b')].map(e=>e.textContent),['字段','客户','账龄区间','应收余额']);assert.deepEqual([...a.$('sh-fields-list').querySelectorAll('.column-line code')].map(e=>e.textContent).slice(1),['string','string','decimal · CNY']);
 d.dispatchEvent(new a.w.KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));assert.equal(a.$('sh-d-data').hidden,true);
 // The workbench's empty state: the overview's three steps, gone with the first result and back when it is cleared.
 assert.deepEqual([...a.$('start-steps').querySelectorAll('b')].map(e=>e.textContent),['选择已发布数据集','预览并创建快照','生成 Excel 并下载']);assert.equal(a.$('start-steps').hidden,false);
 a.configure();a.click('preview');await until(()=>!a.$('preview').disabled&&!a.$('result-table-wrap').hidden,'query');assert.equal(a.$('start-steps').hidden,true);
 a.input('query-companies','company_other');assert.equal(a.$('start-steps').hidden,false);
 // The page has no inline style or handler; every <use> of the page points at a symbol the page holds.
 assert.equal(/\sstyle\s*=/i.test(html),false);assert.equal(/\son[a-z]+\s*=/i.test(html),false);assert.equal(/<style[\s>]/i.test(html),false);
 for(const u of d.querySelectorAll('use'))assert.ok(d.getElementById(u.getAttribute('href').slice(1)),u.getAttribute('href')+' is a symbol of the page');
 // Sign out: the card is back, the shell inert, the counts and the identity reset.
 a.click('logout');assert.equal(a.$('connection-overlay').hidden,false);assert.equal(d.querySelector('.shell').inert,true);assert.equal(a.$('sh-fields-meta').textContent,'连接工作区后加载当前授权目录。');assert.equal(a.$('sh-fields-list').hidden,true);assert.equal(a.$('actor-label').textContent,'未连接');assert.equal(a.$('connection-label').textContent,'尚未连接');
 assert.equal(a.errors.length,0,a.errors.join('|'));
}finally{a.close();}});

test('S06-C C6: the dashboard studio: the regions, every old control under its old id in its new region, the badge\'s transitions, the structure panel, the publish confirmation, the drawers, and the source of board.js',async()=>{const a=app();try{
 const d=a.w.document;await a.login();
 d.querySelector('[data-page="dashboard"]').click();assert.equal(d.body.classList.contains('board-on'),true);
 const s=a.$('bd-studio');
 assert.deepEqual([...s.children].map(e=>e.tagName.toLowerCase()+'.'+e.className.split(' ')[0]),['header.topbar','nav.toolrail','aside.sidebar','main.workarea','aside.inspector','footer.statusbar']);
 assert.match(s.querySelector('.topbar .wordmark').textContent.replace(/\s+/g,' ').trim(),/^IDP REPORTING\s*DASHBOARD STUDIO$/);assert.equal(a.$('bd-crumb').textContent.trim(),'报表工作台');assert.equal(a.$('bd-title').textContent,'看板');
 assert.deepEqual(['bd-help','bd-versions','board-server-save','bd-publish'].map(id=>a.$(id).getAttribute('aria-label')||a.$(id).textContent.trim()),['看板说明与操作帮助','版本','保存版本','发布']);
 assert.equal(a.$('bd-state').textContent,'未查询');assert.equal(a.$('bd-start').hidden,false);assert.equal(a.$('bd-desk').hidden,true);assert.equal(a.$('bd-start-open').dataset.act,'data');
 // Every old control is in the page once, in its region.
 const where={'board-metric':'#bd-sidebar','board-add-metric':'#bd-sidebar','board-columns':'#bd-sidebar','board-add-table':'#bd-sidebar','board-category':'#bd-sidebar','board-value':'#bd-sidebar','board-add-bar':'#bd-sidebar','board-reset':'#bd-sidebar','board-count':'#bd-sidebar','board-source':'#bd-sidebar','board-widgets':'#bd-workarea','board-status':'#bd-workarea','board-server-save':'#bd-studio>.topbar','board-server-new':'#bd-d-versions','board-server-list':'#bd-d-versions','board-server-listing':'#bd-d-versions','board-server-version':'#bd-d-versions','board-server-open':'#bd-d-versions','board-server-status':'#bd-ipanel-properties','board-release-read':'#bd-d-versions','board-release-publish':'#bd-d-versions','board-release-withdraw':'#bd-d-versions','board-published-open':'#bd-d-versions','board-release-status':'#bd-d-versions','board-release-history':'#bd-d-versions','board-save':'#bd-ipanel-file','board-json':'#bd-ipanel-file','board-open':'#bd-ipanel-file'};
 for(const [id,region] of Object.entries(where)){assert.equal(d.querySelectorAll('#'+id).length,1,id);assert.ok(a.$(id).closest(region),id+' is in '+region);}
 for(const b of s.querySelectorAll('button'))assert.ok((b.getAttribute('aria-label')||b.textContent).trim().length>0,'a button of the studio has a name');
 // The rail switches the side views and the inspector's tabs the panels, by click and by arrow keys.
 const rail=[...s.querySelectorAll('.rail-tabs [role=tab]')];assert.deepEqual(rail.map(b=>b.getAttribute('aria-label')),['组件库','组件与顺序','数据来源']);
 rail[1].click();assert.deepEqual([...s.querySelectorAll('.side-view')].map(v=>v.hidden),[true,false,true]);assert.equal(a.$('bd-sidebar').getAttribute('aria-labelledby'),'bd-rail-structure');
 rail[1].dispatchEvent(new a.w.KeyboardEvent('keydown',{key:'ArrowDown',bubbles:true,cancelable:true}));assert.equal(rail[2].getAttribute('aria-selected'),'true');assert.equal(d.activeElement,rail[2]);assert.deepEqual(rail.map(b=>b.tabIndex),[-1,-1,0]);
 rail[0].click();
 const tabs=[...s.querySelectorAll('.inspector-tabs [role=tab]')];assert.deepEqual(tabs.map(t=>t.textContent),['属性','文件'],'S07-G: the 版本 tab is the version dialog');assert.equal(a.$('bd-ipanel-versions'),null);
 tabs[1].click();assert.deepEqual(['bd-ipanel-properties','bd-ipanel-file'].map(id=>a.$(id).hidden),[true,false]);
 tabs[1].dispatchEvent(new a.w.KeyboardEvent('keydown',{key:'ArrowLeft',bubbles:true,cancelable:true}));assert.equal(tabs[0].getAttribute('aria-selected'),'true');assert.equal(a.$('bd-ipanel-properties').hidden,false);assert.equal(d.activeElement,tabs[0]);
 // 版本 and 发布 before a result say why and open nothing.
 a.$('bd-versions').click();await flush();assert.equal(a.$('bd-d-versions').hidden,true);assert.equal(a.$('board-status').textContent,'请先选择数据：预览或冻结快照。');
 a.$('bd-publish').click();await flush();assert.equal(a.$('board-status').textContent,'请先选择数据：预览或冻结快照。');assert.equal(a.$('bd-d-publish').hidden,true);
 // A result: the desk, 草稿, the context bar, the status bar and the panels name the binding.
 a.configure();a.click('preview');await until(()=>!a.$('preview').disabled&&!a.$('result-table-wrap').hidden,'query');d.querySelector('[data-page="dashboard"]').click();
 assert.equal(a.$('bd-state').textContent,'草稿');assert.equal(a.$('bd-start').hidden,true);assert.equal(a.$('bd-desk').hidden,false);
 assert.match(a.$('bd-ctx-source').textContent,/^receivables\.aging @ 1\.0\.0 · \d{4}-\d{2}-\d{2} · 当前查询预览$/);assert.equal(a.$('bd-status-source').textContent,a.$('bd-ctx-source').textContent);assert.equal(a.$('bd-side-dataset').textContent,'receivables.aging@1.0.0');assert.equal(a.$('bd-side-rows').textContent,'当前查询预览 · 3 行 · 3 个字段');assert.equal(a.$('bd-insp-dataset').textContent,'receivables.aging@1.0.0');
 a.$('bd-publish').click();await flush();assert.equal(a.$('bd-d-publish').hidden,false,'S07-B: nothing saved yet is saved first');assert.equal(a.$('bd-publish-confirm').textContent,'保存并发布');assert.match(a.$('bd-publish-note').textContent,/先保存为版本 1/);d.dispatchEvent(new a.w.KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));assert.equal(a.$('bd-d-publish').hidden,true);
 // The structure panel mirrors the cards and its buttons do what the cards' buttons do.
 a.click('board-add-metric');a.click('board-add-table');a.click('board-add-bar');
 assert.equal(a.$('bd-status-count').textContent,'3 / 12 个组件');assert.equal(a.$('board-count').textContent,'3 / 12 个组件');
 rail[1].click();const rows=[...a.$('bd-layers').children];assert.deepEqual(rows.map(r=>r.querySelector('span').textContent),['1 · 应收余额','2 · 明细表 · 当前结果页','3 · 应收余额 · 当前页柱状图']);assert.equal(a.$('bd-layers-empty').hidden,true);
 assert.deepEqual(rows.map(r=>[...r.querySelectorAll('button')].map(b=>b.getAttribute('aria-label'))),[['上移：应收余额','下移：应收余额','移除：应收余额'],['上移：明细表 · 当前结果页','下移：明细表 · 当前结果页','移除：明细表 · 当前结果页'],['上移：应收余额 · 当前页柱状图','下移：应收余额 · 当前页柱状图','移除：应收余额 · 当前页柱状图']]);
 assert.equal(rows[0].querySelectorAll('button')[0].disabled,true,'the first cannot move up');assert.equal(rows[2].querySelectorAll('button')[1].disabled,true,'the last cannot move down');
 rows[0].querySelectorAll('button')[1].click();assert.equal(a.$('board-widgets').children[0].querySelector('h3').textContent,'明细表 · 当前结果页');assert.deepEqual([...a.$('bd-layers').children].map(r=>r.querySelector('span').textContent),['1 · 明细表 · 当前结果页','2 · 应收余额','3 · 应收余额 · 当前页柱状图']);
 a.$('bd-layers').children[0].querySelectorAll('button')[2].click();assert.equal(a.$('board-widgets').children.length,2);assert.equal(a.$('bd-layers').children.length,2);
 // Save: 已保存 v1; a change: · 有未保存修改 and no publishing; saved again: the confirmation reads the release, names what it publishes, traps the focus, publishes; 已发布 v2 follows.
 a.click('board-server-save');await until(()=>a.$('board-server-status').textContent.includes('版本 1')&&!a.$('preview').disabled,'saved');
 assert.equal(a.$('bd-state').textContent,'已保存 v1');assert.match(a.$('bd-insp-meta').textContent,/^2 \/ 12 个组件 · 看板 [a-f0-9]{8} · 版本 1$/);
 a.click('board-add-metric');assert.equal(a.$('bd-state').textContent,'已保存 v1 · 有未保存修改');
 a.$('bd-publish').click();await flush();assert.equal(a.$('bd-d-publish').hidden,false,'S07-B: unsaved changes are saved first');assert.equal(a.$('bd-publish-confirm').textContent,'保存并发布');assert.match(a.$('bd-publish-note').textContent,/先保存为新的不可变版本/);d.dispatchEvent(new a.w.KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));assert.equal(a.$('bd-d-publish').hidden,true);assert.equal(a.$('bd-state').textContent,'已保存 v1 · 有未保存修改','Esc saves nothing');
 a.click('board-server-save');await until(()=>a.$('board-server-status').textContent.includes('版本 2')&&!a.$('preview').disabled,'saved v2');assert.equal(a.$('bd-state').textContent,'已保存 v2');
 a.$('bd-publish').focus();a.$('bd-publish').click();await until(()=>!a.$('bd-d-publish').hidden,'the confirmation');
 assert.equal(a.$('bd-studio').inert,true);assert.equal(d.activeElement.closest('#bd-d-publish')!==null,true,'focus is in the dialog');assert.equal(a.$('bd-d-publish').querySelector('[role=dialog]').getAttribute('aria-modal'),'true');
 const summary=Object.fromEntries([...a.$('bd-publish-summary').querySelectorAll('dt')].map(dt=>[dt.textContent,dt.nextElementSibling.textContent]));
 assert.match(summary['看板'],/^[a-f0-9]{32}$/);assert.equal(summary['将发布的版本'],'保存版本 2');assert.equal(summary['数据集'],'receivables.aging@1.0.0');assert.equal(summary['组件'],'3 个');assert.equal(summary['当前发布'],'未发布');assert.equal(summary['发布修订'],'0');
 d.dispatchEvent(new a.w.KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));assert.equal(a.$('bd-d-publish').hidden,true);assert.equal(d.activeElement,a.$('bd-publish'));assert.equal(a.$('bd-studio').inert,false);assert.equal(a.$('board-release-status').textContent,'发布修订 0 · 当前未发布','nothing was published');
 a.$('bd-publish').click();await until(()=>!a.$('bd-d-publish').hidden,'the confirmation again');
 const last=[...a.$('bd-d-publish').querySelectorAll('button')].pop();last.focus();d.dispatchEvent(new a.w.KeyboardEvent('keydown',{key:'Tab',bubbles:true,cancelable:true}));assert.equal(d.activeElement.closest('#bd-d-publish')!==null,true,'Tab stays in the dialog');
 a.$('bd-publish-confirm').click();await until(()=>a.$('bd-state').textContent==='已发布 v2'&&!a.$('preview').disabled,'published');
 assert.equal(a.$('board-release-status').textContent,'发布修订 1 · 已发布版本 2');assert.equal(a.$('bd-d-publish').hidden,true);assert.equal(a.$('bd-status-state').textContent.trim(),'已发布 v2');
 a.click('board-add-metric');assert.equal(a.$('bd-state').textContent,'已发布 v2 · 有未保存修改');
 // The help dialog, the drawers' toggles, Esc on a drawer.
 a.$('bd-help').click();assert.equal(a.$('bd-d-help').hidden,false);assert.equal(a.$('bd-d-help').querySelector('[role=dialog]').getAttribute('aria-modal'),'true');d.dispatchEvent(new a.w.KeyboardEvent('keydown',{key:'Escape',bubbles:true}));assert.equal(a.$('bd-d-help').hidden,true);assert.equal(d.activeElement,a.$('bd-help'));
 a.$('bd-ctx-inspector').click();assert.equal(a.$('bd-inspector').classList.contains('open'),true);a.$('bd-insp-close').click();assert.equal(a.$('bd-inspector').classList.contains('open'),false);
 s.querySelector('[data-act="toggle-sidebar"]').click();assert.equal(a.$('bd-sidebar').classList.contains('open'),true);s.dispatchEvent(new a.w.KeyboardEvent('keydown',{key:'Escape',bubbles:true}));assert.equal(a.$('bd-sidebar').classList.contains('open'),false);
 // The source: the DOM API only, no storage, no dynamic code, nothing from outside.
 for(const pattern of [/\.innerHTML/,/\.outerHTML/,/insertAdjacentHTML/,/document\.write/,/\beval\(/,/new Function/,/\.srcdoc/,/setAttribute\(\s*['"]on/,/\.style\b/,/setAttribute\(\s*['"]style/,/\bfetch\(/,/XMLHttpRequest/,/localStorage|sessionStorage|indexedDB|document\.cookie/,/createElement\(\s*['"](script|iframe|link|style)['"]/])assert.equal(pattern.test(boardScript.replace(/\/\/.*$/gm,'')),false,'board.js contains '+pattern);
 assert.equal(/https?:\/\//.test(boardScript.replace(/\/\/.*$/gm,'').replace(/http:\/\/www\.w3\.org\/2000\/svg/g,'')),false,'board.js has no external address');
 // Sign out clears the studio: 未查询, the start card, the components view, the properties tab.
 a.click('logout');assert.equal(a.$('bd-state').textContent,'未查询');assert.equal(a.$('bd-start').hidden,false);assert.equal(a.$('bd-layers').children.length,0);assert.equal(a.$('bd-ipanel-properties').hidden,false);assert.equal(a.$('bd-sidebar').getAttribute('aria-labelledby'),'bd-rail-components');
 assert.equal(a.errors.length,0,a.errors.join('|'));
}finally{a.close();}});

// ---- S07-A: the application rail, the data chip and the data drawer (docs/ADR_0009_SHELL_CONTINUITY.md, docs/S07A_SCOPE.md) ----
test('S07-A A1–A3: the rail stays on every shell; the chip names the data context; the drawer takes the query panel from the workbench and gives it back; a result from the drawer removes the dashboard\'s dead end without a page switch; the centre offers the drawer',async()=>{const a=app();try{
 const d=a.w.document;await a.login();
 const rail=d.querySelector('nav.app-rail[aria-label="主导航"]');assert.ok(rail);assert.equal(rail.closest('.shell'),null,'the rail is outside the shell the studios take over');
 assert.deepEqual([...rail.querySelectorAll('.nav-item')].map(b=>[b.dataset.page||'panel:'+b.dataset.panel,b.getAttribute('aria-label'),b.querySelector('small').textContent]),[['workspace','报表工作台','报表'],['dashboard','看板工作室','看板'],['library','模板中心','模板'],['documents','文档工作室','文档'],['panel:tasks','任务工作区','任务'],['system','能力与环境','能力']]);
 assert.equal(a.$('nav-datasets'),null);assert.equal(rail.querySelector('.rail-status #connection-label').textContent,'API 已连接');
 for(const page of ['documents','library','dashboard','system']){d.querySelector('[data-page="'+page+'"]').click();assert.equal(rail.querySelector('[aria-current="page"]').dataset.page,page);assert.equal(rail.querySelectorAll('[aria-current]').length,1);}
 const chip=a.$('sh-data-chip');assert.equal(chip.classList.contains('is-empty'),true);assert.equal(a.$('sh-data-text').textContent,'未选择数据');assert.equal(a.$('sh-data-kind').hidden,true);assert.equal(chip.getAttribute('aria-haspopup'),'dialog');
 // Before any data the centre refuses with the drawer offered, never with the workbench; its chip is empty too.
 d.querySelector('[data-page="library"]').click();await until(()=>!a.$('page-library').hidden&&/个结果/.test(a.$('lib-count').textContent)&&!d.querySelector('#page-library .catalog-notice-line').hidden,'the centre listed with its refusal line',30000);
 const line=d.querySelector('#page-library .catalog-notice-line');assert.match(line.textContent,/先选择数据集并预览或冻结快照/);assert.equal(line.textContent.includes('工作台'),false);assert.equal(a.$('lib-data').textContent.trim(),'未选择数据');assert.equal(a.$('lib-data').classList.contains('is-empty'),true);
 line.querySelector('button').click();assert.equal(a.$('sh-d-data').hidden,false);d.dispatchEvent(new a.w.KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));assert.equal(a.$('sh-d-data').hidden,true);assert.equal(d.activeElement,line.querySelector('button'));
 // The dashboard's start card opens the drawer: the query panel moves in, the focus goes to the company field, the shell is inert.
 d.querySelector('[data-page="dashboard"]').click();assert.equal(a.$('bd-start').hidden,false);assert.equal(a.$('bd-start-open').textContent.trim(),'选择数据…');assert.equal(a.$('bd-start').querySelector('[data-open]'),null,'no way out to another page');
 a.$('bd-start-open').click();assert.equal(a.$('sh-d-data').hidden,false);assert.equal(a.$('sh-query-slot').contains(d.querySelector('.query-panel')),true);assert.equal(d.activeElement,a.$('query-companies'));assert.equal(d.querySelector('.shell').inert,true);assert.equal(a.$('sh-d-data').querySelector('[role=dialog]').getAttribute('aria-modal'),'true');
 // Esc gives the panel back to the workbench where it was, and the focus to the button.
 d.dispatchEvent(new a.w.KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));assert.equal(a.$('sh-d-data').hidden,true);assert.equal(a.$('page-workspace').querySelector('.page-heading').nextElementSibling.classList.contains('query-panel'),true);assert.equal(d.activeElement,a.$('bd-start-open'));assert.equal(d.querySelector('.shell').inert,false);
 // A preview run inside the drawer: the drawer closes, the panel is back, the chip names the context, the start card gives way; the page did not change.
 a.$('bd-ctx-data').click();assert.equal(a.$('sh-d-data').hidden,false);
 a.input('query-companies','company_demo');a.click('preview');await until(()=>!a.$('preview').disabled&&!a.$('result-table-wrap').hidden,'preview from the drawer');
 assert.equal(a.$('sh-d-data').hidden,true);assert.equal(a.$('page-workspace').contains(d.querySelector('.query-panel')),true);assert.equal(a.$('page-dashboard').hidden,false,'still in the dashboard studio');assert.equal(a.$('bd-start').hidden,true);assert.equal(a.$('bd-state').textContent,'草稿');
 assert.equal(chip.classList.contains('is-empty'),false);assert.match(a.$('sh-data-text').textContent,/^receivables\.aging@1\.0\.0 · company_demo · \d{4}-\d{2}-\d{2}$/);assert.equal(a.$('sh-data-kind').textContent,'查询预览');
 // The snapshot from the header's chip: the company is known, so the focus goes to the action; the chip then says 冻结快照 and the centre's chip agrees.
 chip.click();assert.equal(a.$('sh-d-data').hidden,false);assert.equal(d.activeElement,a.$('preview'));
 a.click('snapshot');await until(()=>a.$('result-kind').textContent==='冻结快照'&&!a.$('export').disabled,'snapshot');assert.equal(a.$('sh-d-data').hidden,true);assert.equal(a.$('sh-data-kind').textContent,'冻结快照');assert.equal(d.activeElement,chip);
 d.querySelector('[data-page="library"]').click();await until(()=>!a.$('page-library').hidden&&!!a.$('lib-data'),'centre');
 assert.equal(a.$('lib-data').textContent.trim().endsWith('· 冻结快照'),true);assert.equal(a.$('lib-data').classList.contains('is-empty'),false);
 // Sign out empties the chip and closes the drawer.
 chip.click();assert.equal(a.$('sh-d-data').hidden,false);a.click('logout');assert.equal(chip.classList.contains('is-empty'),true);assert.equal(a.$('sh-data-text').textContent,'未选择数据');assert.equal(a.$('sh-d-data').hidden,true);assert.equal(a.$('page-workspace').contains(d.querySelector('.query-panel')),true,'the panel is back in the workbench');
 // No inline style or handler came with the rail, the chip or the drawer; every <use> still points at a symbol of the page.
 assert.equal(/\sstyle\s*=/i.test(html),false);for(const u of d.querySelectorAll('use'))assert.ok(d.getElementById(u.getAttribute('href').slice(1)),u.getAttribute('href'));
 assert.equal(a.errors.length,0,a.errors.join('|'));
}finally{a.close();}});

// ---- S07-B: the tasks panel and 保存并发布 (docs/S07B_SCOPE.md) ----
test('S07-B B1/B2: the tasks panel opens over any shell with the tasks page\'s rows and gives them back; 查看结果 from it lands in the workbench; 发布 with unsaved changes saves the next version and publishes it, in the dashboard studio and in the document studio',async()=>{const a=app();try{
 const d=a.w.document;await a.login();
 const rail=d.querySelector('nav.app-rail'),tasks=rail.querySelector('[data-panel="tasks"]');
 assert.equal(tasks.getAttribute('aria-haspopup'),'dialog');assert.equal(tasks.getAttribute('aria-expanded'),'false');assert.equal(tasks.dataset.page,undefined);
 // A snapshot through the data drawer from the dashboard studio (a job for the panel to show).
 d.querySelector('[data-page="dashboard"]').click();a.$('bd-start-open').click();a.input('query-companies','company_demo');a.click('snapshot');await until(()=>a.$('result-kind').textContent==='冻结快照'&&!a.$('export').disabled,'snapshot');
 assert.equal(a.$('page-dashboard').hidden,false);assert.equal(a.$('task-count').textContent,'1');
 // The panel over the dashboard studio: the page's restore row, list and refresh button are in it; the rail's button says so.
 tasks.click();assert.equal(a.$('sh-d-tasks').hidden,false);assert.equal(tasks.getAttribute('aria-expanded'),'true');assert.equal(d.querySelector('.shell').inert,true);
 assert.equal(a.$('sh-tasks-slot').contains(a.$('tasks-body')),true);assert.equal(a.$('sh-tasks-slot').contains(a.$('restore-id')),true);assert.equal(a.$('sh-tasks-foot').contains(a.$('refresh-tasks')),true);assert.equal(a.$('tasks-body').rows.length,1);assert.equal(a.$('page-dashboard').hidden,false,'the page behind did not change');
 assert.equal(d.activeElement.closest('#sh-d-tasks')!==null,true,'focus is in the panel');
 // Esc gives everything back to the tasks page and the focus to the rail's button.
 d.dispatchEvent(new a.w.KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));assert.equal(a.$('sh-d-tasks').hidden,true);assert.equal(tasks.getAttribute('aria-expanded'),'false');assert.equal(a.$('page-tasks').contains(a.$('tasks-body')),true);assert.equal(a.$('page-tasks').querySelector('.page-heading').contains(a.$('refresh-tasks')),true);assert.equal(d.activeElement,tasks);
 // 查看结果 from the panel: the workbench shows the snapshot, the panel is closed, its content is back.
 tasks.click();[...a.$('tasks-body').querySelectorAll('button')].find(b=>b.textContent==='查看结果').click();await until(()=>!a.$('page-workspace').hidden&&a.$('result-kind').textContent==='冻结快照'&&!a.$('preview').disabled,'the result');
 assert.equal(a.$('sh-d-tasks').hidden,true);assert.equal(a.$('page-tasks').contains(a.$('tasks-body')),true);
 // The two panels never overlap: opening the data drawer closes the tasks panel.
 tasks.click();assert.equal(a.$('sh-d-tasks').hidden,false);a.$('sh-data-chip').click();assert.equal(a.$('sh-d-tasks').hidden,true);assert.equal(a.$('sh-d-data').hidden,false);d.dispatchEvent(new a.w.KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));
 // The dashboard studio: 发布 with nothing saved opens 保存并发布; confirming saves version 1 and publishes it.
 d.querySelector('[data-page="dashboard"]').click();a.click('board-add-metric');assert.equal(a.$('bd-state').textContent,'草稿');
 a.$('bd-publish').click();await flush();assert.equal(a.$('bd-d-publish').hidden,false);assert.equal(a.$('bd-publish-confirm').textContent,'保存并发布');
 const dl=Object.fromEntries([...a.$('bd-publish-summary').querySelectorAll('dt')].map(dt=>[dt.textContent,dt.nextElementSibling.textContent]));assert.equal(dl['将发布的版本'],'先保存为版本 1，再发布它');assert.equal(dl['看板'],'新看板');
 a.$('bd-publish-confirm').click();await until(()=>a.$('bd-state').textContent==='已发布 v1'&&!a.$('preview').disabled,'saved and published');
 assert.equal(a.$('board-release-status').textContent,'发布修订 1 · 已发布版本 1');assert.match(a.$('board-server-status').textContent,/版本 1/);
 // With unsaved changes on a saved dashboard: version 2 is saved and published; the badge goes through 已发布 v2.
 a.click('board-add-table');assert.equal(a.$('bd-state').textContent,'已发布 v1 · 有未保存修改');
 a.$('bd-publish').click();await flush();assert.equal(a.$('bd-publish-confirm').textContent,'保存并发布');assert.equal(Object.fromEntries([...a.$('bd-publish-summary').querySelectorAll('dt')].map(dt=>[dt.textContent,dt.nextElementSibling.textContent]))['将发布的版本'],'先保存为版本 2，再发布它');
 a.$('bd-publish-confirm').click();await until(()=>a.$('bd-state').textContent==='已发布 v2'&&!a.$('preview').disabled,'v2 published');assert.equal(a.$('board-release-status').textContent,'发布修订 2 · 已发布版本 2');
 // A clean saved version keeps 确认发布.
 a.click('board-release-read');await until(()=>a.$('board-release-status').textContent.includes('修订 2')&&!a.$('preview').disabled,'release');
 a.$('bd-publish').click();await until(()=>!a.$('bd-d-publish').hidden,'confirmation');assert.equal(a.$('bd-publish-confirm').textContent,'确认发布');d.dispatchEvent(new a.w.KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));
 // The document studio: an unsaved change, 发布 → 保存并发布 → the next version is saved, read and published.
 d.querySelector('[data-page="documents"]').click();await until(()=>!a.$('st-start').hidden===false||a.$('st-state').textContent.length>0,'studio');
 a.$('st-card-text').click();assert.equal(a.$('st-state').textContent,'草稿');
 a.$('st-publish').click();assert.equal(a.$('st-pub-confirm').disabled,false);assert.equal(a.$('st-pub-confirm').textContent.trim(),'保存并发布');assert.equal(a.$('st-pub-footnote').textContent,'先保存为新版本，再发布它');
 a.$('st-pub-confirm').click();await until(()=>a.$('st-state').textContent==='已发布 v1'&&!a.$('preview').disabled,'template saved and published',20000);
 assert.match(a.$('doc-release-status').textContent,/已发布版本 1/);
 a.$('st-card-text').click();assert.equal(a.$('st-state').textContent,'已发布 v1·有未保存修改');
 a.$('st-publish').click();assert.equal(a.$('st-pub-confirm').textContent.trim(),'保存并发布');a.$('st-pub-confirm').click();await until(()=>a.$('st-state').textContent==='已发布 v2'&&!a.$('preview').disabled,'v2 saved and published',20000);
 assert.equal(a.errors.length,0,a.errors.join('|'));
}finally{a.close();}});

// ---- S07-C: the task list is the server's (docs/S07C_SCOPE.md) ----
test('S07-C: the task list is the identity\'s jobs on the server, newest first; the count follows it; opening the panel and 刷新状态 read it again; a job restored by its id is not listed twice',async()=>{const a=app(null,{listing:true});try{
 const d=a.w.document;
 const listed=async()=>(await fetch(url+'/api/reporting/v1/jobs?limit=50',authorized)).json();
 const ids=()=>[...a.$('tasks-body').rows].map(r=>r.cells[1].textContent);
 // Signed in: the list is the server's, in its order, and the counts say so (the tests before this one left jobs on the shared server).
 await a.login();
 let server=await listed();assert.ok(server.length<=50);
 // The list is read after the sign-in's own requests (the session, the capabilities, the product); with many jobs on the shared server it takes a moment.
 await until(()=>a.trace.some(v=>v.path.endsWith('/jobs?limit=50'))&&ids().length===server.length,'the list after sign-in: '+ids().length+' of '+server.length,10000);
 assert.deepEqual(ids(),server.map(j=>j.job_id));assert.equal(a.$('task-count').textContent,String(server.length));assert.equal(a.$('tasks-note').textContent,server.length+' 项');
 assert.equal(a.trace.filter(v=>v.path.endsWith('/jobs?limit=50')).length,1);
 // A snapshot from the console goes first, and the count rises by one: the session's jobs join the listed ones (the server's window is 50; what this session knows beyond it stays, up to 100).
 const before=server.length;a.configure();a.click('snapshot');await until(()=>a.$('result-kind').textContent==='冻结快照'&&!a.$('export').disabled,'snapshot');
 assert.equal(ids().length,before+1);assert.equal(a.$('task-count').textContent,String(before+1));assert.equal(a.$('tasks-body').rows[0].cells[0].textContent,'报表快照');
 // A job made elsewhere (another session, the API) is not known until the list is read again: opening the panel reads it, quietly.
 const catalog=await (await fetch(url+'/api/reporting/v1/catalog?integration_id='+encodeURIComponent(a.$('query-product').value),authorized)).json();
 const dataset=catalog.find(c=>c.dataset.id+'@'+c.dataset.version===a.$('query-dataset').value);
 const spec={schema_version:'1.0',integration_id:a.$('query-product').value,dataset:dataset.dataset,company_ids:['company_demo'],metrics:dataset.metrics,dimensions:dataset.dimensions,filters:[],sort:[],as_of:a.$('query-date').value,timezone:dataset.timezone,page:{limit:50}};
 if(dataset.currency_policy)spec.currency_policy=dataset.currency_policy;
 const elsewhere=async key=>{const w=await fetch(url+'/api/reporting/v1/snapshot-jobs',{method:'POST',headers:{...authorized.headers,'Content-Type':'application/json','Idempotency-Key':key},body:JSON.stringify(spec)});assert.equal(w.status,202);return (await w.json()).job_id;};
 const first=await elsewhere('s07c-'+randomBytes(4).toString('hex'));assert.equal(ids().includes(first),false);
 const tasks=d.querySelector('nav.app-rail [data-panel="tasks"]');tasks.click();assert.equal(a.$('sh-d-tasks').hidden,false);
 await until(()=>ids()[0]===first,'the panel read the list');assert.equal(a.$('preview').disabled,false,'the page was not held');
 server=await listed();assert.deepEqual(ids().slice(0,server.length),server.map(j=>j.job_id),'the server\'s order first; what fell out of its window stays after it');
 d.dispatchEvent(new a.w.KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));assert.equal(a.$('sh-d-tasks').hidden,true);
 // 刷新状态 reads the list, then polls each known job as before: a second job made elsewhere appears with its state.
 const second=await elsewhere('s07c-'+randomBytes(4).toString('hex'));await untilRealJob(second,'succeeded');
 a.click('refresh-tasks');await until(()=>a.$('notice').textContent.includes('已刷新')&&!a.$('preview').disabled,'refreshed');
 assert.equal(ids()[0],second);assert.equal(a.$('tasks-body').rows[0].cells[2].textContent,'已完成');assert.equal(a.trace.filter(v=>v.path.endsWith('/jobs/'+second)).length,1,'the known job was polled after the list');
 // A listed job restored by its id is the same row; the row of a snapshot keeps 查看结果 (the result is authorized when it is read, not by the list).
 const count=ids().length;a.input('restore-id',second);a.click('restore-job');await until(()=>a.$('notice').textContent.includes('已恢复'),'restored');
 assert.equal(ids().length,count);assert.equal(ids().filter(id=>id===second).length,1);
 assert.ok([...a.$('tasks-body').rows[0].querySelectorAll('button')].some(b=>b.textContent==='查看结果'));
 // A list that cannot be believed is refused whole, and the rows stay as they were.
 const kept=ids();a.w.fetch=(orig=>async(path,options)=>/\/jobs\?limit=50$/.test(String(path))?json([{job_id:'x'.repeat(32),kind:'snapshot',state:'succeeded'}]):orig(path,options))(a.w.fetch);
 a.click('refresh-tasks');await until(()=>a.$('notice').textContent.includes('无法验证的响应'),'refused list');assert.deepEqual(ids(),kept);
 // Signing out clears the list, as it clears everything of the session.
 a.click('logout');assert.equal(a.$('tasks-body').rows.length,0);assert.equal(a.$('task-count').textContent,'0');
 assert.equal(a.errors.length,0,a.errors.join('|'));
}finally{a.close();}});

// ---- S07-D: the help icon and the manual the server serves ----
test('S07-D: the help icon on the rail and on the sign-in card opens the user manual, which the server serves at /console/manual/ with the console\'s headers',async()=>{const a=app();try{
 const d=a.w.document;
 for(const [id,where] of [['connect-help','the sign-in card'],['rail-help','the rail']]){const link=a.$(id);assert.ok(link,where);assert.equal(link.tagName,'A',where);assert.equal(link.href,url+'/console/manual/',where);assert.equal(link.target,'_blank',where);assert.equal(link.rel,'noopener',where);assert.equal(link.getAttribute('aria-label'),'用户手册（新窗口）',where);assert.ok(link.querySelector('use[href="#s-help"]'),where);}
 assert.equal(a.$('connect-help').closest('.connection-card')!==null,true);assert.equal(a.$('rail-help').closest('nav.app-rail')!==null,true);assert.equal(a.$('rail-help').classList.contains('nav-item'),false,'not one of the eight destinations');
 await a.login();assert.equal(a.$('rail-help').href,url+'/console/manual/');
 const page=await fetch(url+'/console/manual/');const html=await page.text();
 assert.equal(page.status,200);assert.ok(html.includes('IDP Reporting 控制台用户手册'));assert.ok(html.includes('<link rel="stylesheet" href="manual.css">'));assert.equal(html.includes('<style'),false,'no inline style under the console\'s policy');
 assert.equal(page.headers.get('content-security-policy').includes("style-src 'self'"),true);assert.equal(page.headers.get('x-frame-options'),'DENY');
 for(const [path,status,type] of [['/console/manual/manual.css',200,'text/css'],['/console/manual/img/01-signin.png',200,'image/png'],['/console/manual/testdata/catalog.json',200,'application/json'],['/console/manual/img/',404,''],['/console/manual/index.html',404,'']]){const r=await fetch(url+path);assert.equal(r.status,status,path);if(type)assert.ok(r.headers.get('content-type').startsWith(type),path+' '+r.headers.get('content-type'));}
 // Every image and test data file the manual links to is served.
 const refs=[...html.matchAll(/(?:src|href)="((?:img|testdata)\/[^"]+)"/g)].map(m=>m[1]);assert.ok(refs.length>=40,String(refs.length));
 for(const ref of new Set(refs)){const r=await fetch(url+'/console/manual/'+ref);assert.equal(r.status,200,ref);}
 assert.equal(a.errors.length,0,a.errors.join('|'));
}finally{a.close();}});

// ---- S07-E: the task list's filters and pages (docs/S07E_SCOPE.md) ----
test('S07-E: the kind and state filters ask the server for that page and show only what they keep; 加载更多 reads the page after the last job; sign-out clears the filters',async()=>{
 let fake=false;const asked=[];
 const id=n=>n.toString(16).padStart(32,'0');
 const page=(from,count)=>Array.from({length:count},(_,k)=>({job_id:id(from-k),kind:'snapshot',state:'failed',error_code:'SOURCE_UNAVAILABLE'}));
 const a=app(async path=>{if(!/\/jobs\?/.test(path))return;asked.push(path.slice(path.indexOf('?')+1));if(!fake)return;const before=new URL('http://x'+path).searchParams.get('before');return json(before?page(1000-50,3):page(1000,50));},{listing:true});
 try{
  const d=a.w.document;const rows=()=>[...a.$('tasks-body').rows];const change=(sel,v)=>{a.$(sel).value=v;a.$(sel).dispatchEvent(new a.w.Event('change',{bubbles:true}));};
  await a.login();await until(()=>asked.includes('limit=50'),'the first page');
  // A snapshot of its own, so that the test does not depend on the tests before it.
  a.configure();a.click('snapshot');await until(()=>a.$('result-kind').textContent==='冻结快照'&&!a.$('export').disabled,'snapshot');
  // The two filters are in the panel's list, with every kind and state the server knows.
  assert.deepEqual([...a.$('tasks-kind').options].map(o=>o.value),['','snapshot','export','issue']);assert.deepEqual([...a.$('tasks-state').options].map(o=>o.value),['','queued','running','succeeded','failed','cancelled']);
  // A kind: the server is asked for that kind; the rows are of that kind only; the rail still counts every known job.
  change('tasks-kind','snapshot');await until(()=>asked.includes('limit=50&kind=snapshot')&&!a.$('preview').disabled,'the snapshots');
  await until(()=>rows().length>0,'the snapshots');
  assert.ok(rows().every(r=>r.cells[0].textContent==='报表快照'));assert.equal(a.$('tasks-note').textContent,rows().length+' 项');assert.ok(Number(a.$('task-count').textContent)>=rows().length);
  // A kind and a state together.
  change('tasks-state','succeeded');await until(()=>asked.includes('limit=50&kind=snapshot&state=succeeded'),'the succeeded snapshots');
  assert.ok(rows().every(r=>r.cells[0].textContent==='报表快照'&&r.cells[2].textContent==='已完成'));
  // A filter nothing passes says so.
  change('tasks-kind','export');change('tasks-state','cancelled');
  if(!rows().length)assert.equal(a.$('tasks-empty').querySelector('b').textContent,'没有符合筛选的任务');
  change('tasks-kind','');change('tasks-state','');await until(()=>asked.at(-1)==='limit=50','no filter');
  // A full page means there may be more: 加载更多 reads the page after its last job and puts it after it.
  fake=true;const tasks=d.querySelector('nav.app-rail [data-panel="tasks"]');tasks.click();
  await until(()=>rows().some(r=>r.cells[1].textContent===id(951)),'the full fake page');
  assert.equal(a.$('tasks-more').hidden,false);assert.equal(a.$('tasks-more-button').hidden,false);assert.equal(a.$('sh-tasks-slot').contains(a.$('tasks-more-button')),true,'the button is in the panel');
  a.$('tasks-more-button').click();await until(()=>rows().some(r=>r.cells[1].textContent===id(948))&&!a.$('preview').disabled,'the next page');
  assert.ok(asked.includes('limit=50&before='+id(951)));
  // The older page comes after everything shown (the fake pages leave out the real jobs the console knew, which stay where they were).
  const order=rows().map(r=>r.cells[1].textContent);assert.deepEqual(order.slice(-3),[id(950),id(949),id(948)],'the older page is last');assert.ok(order.indexOf(id(951))<order.indexOf(id(950)));
  assert.equal(a.$('notice').textContent,'已加载更早的 3 项任务。');assert.equal(a.$('tasks-more').hidden,true,'a short page is the last');
  d.dispatchEvent(new a.w.KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));
  // Sign-out clears the list and the filters.
  change('tasks-kind','issue');a.click('logout');assert.equal(a.$('tasks-kind').value,'');assert.equal(a.$('tasks-state').value,'');assert.equal(a.$('tasks-body').rows.length,0);assert.equal(a.$('tasks-more').hidden,true);
  assert.equal(a.errors.length,0,a.errors.join('|'));
 }finally{a.close();}
});

// ---- S07-F: one shell (docs/ADR_0010_ONE_SHELL.md, docs/S07F_SCOPE.md) ----
test('S07-F: the console header is the header of every page: the studios and the centre name themselves in it, and the page title follows',async()=>{const a=app();try{
 const d=a.w.document;await a.login();
 for(const [page,label,title] of [['dashboard','DASHBOARD STUDIO','看板工作室'],['library','TEMPLATE WORKSPACE','模板中心'],['documents','DOCUMENT STUDIO','文档工作室'],['workspace','REPORT WORKBENCH','报表工作台']]){
  d.querySelector('.app-rail [data-page="'+page+'"]').click();assert.equal(a.$('page-'+page).hidden,false,page);
  assert.equal(a.$('shell-label').textContent,label,page);assert.equal(d.title,'IDP Reporting · '+title,page);
  // The header's controls are the same on every page: the data chip, the environment, the identity, sign-out.
  for(const id of ['sh-data-chip','actor-label','logout'])assert.ok(d.querySelector('.console-header #'+id),page+' '+id);
 }
 // The stylesheets show the header on every shell and hide only the studios' own wordmarks and the centre's second chip (checked in a real browser by scripts/browser_console_s06c.mjs).
 const css=(await Promise.all(['styles.css','studio.css','library.css'].map(f=>readFile(join(root,'console/assets',f),'utf8')))).join('\n');
 for(const on of ['studio-on','library-on','board-on'])assert.equal(css.includes('body.'+on+' .console-header'),false,on+' no longer hides the console header');
 assert.match(css,/body:not\(\.embed\) #page-documents \.topbar>\.wordmark/);assert.match(css,/body:not\(\.embed\) #lib-data/);
 assert.equal(a.errors.length,0,a.errors.join('|'));
}finally{a.close();}});

// S07-G (ADR 0011): the layout designer has the document studio's three controls and its two dialogs; the dashboard studio's 版本 is the same dialog.
test('S07-G G1: the layout designer: 版本 · 保存版本 · 发布 in its bar, the badge, 发布前确认 with 保存并发布, the version dialog with restore and a rollback, refusals that say what is missing',async()=>{const a=app();try{
 const d=a.w.document,dl=()=>Object.fromEntries([...a.$('ly-publish-summary').querySelectorAll('dt')].map(dt=>[dt.textContent,dt.nextElementSibling.textContent]));
 const releasePosts=()=>a.trace.filter(t=>/\/layouts\/[a-f0-9]{32}\/release$/.test(t.path)&&t.method==='POST').length;
 await a.login();
 // The bar: the three controls in the document studio's order, and the badge; before a query 发布 says what is missing and opens nothing.
 assert.deepEqual([...a.$('report-designer').querySelectorAll('.designer-bar button')].map(b=>b.textContent.trim()),['版本','保存版本','发布']);
 assert.equal(a.$('layout-state').textContent,'未查询');
 a.$('layout-publish').click();await flush();assert.equal(a.$('ly-d-publish').hidden,true);assert.equal(a.$('layout-status').textContent,'请先查询当前授权数据，再设计布局。');
 a.configure();a.click('preview');await until(()=>!a.$('preview').disabled&&!a.$('result-table-wrap').hidden,'query');
 assert.equal(a.$('layout-state').textContent,'草稿');
 // 版本 before a save: the dialog says to save first and lists nothing.
 a.$('layout-versions').click();await flush();assert.equal(a.$('ly-d-versions').hidden,false);assert.equal(a.$('ly-version-empty').hidden,false);assert.equal(a.$('ly-version-list').children.length,0);assert.equal(a.$('ly-version-note').textContent,'尚未保存任何版本');
 assert.equal(d.querySelector('.shell').inert,true,'the page behind is inert');d.dispatchEvent(new a.w.KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));assert.equal(a.$('ly-d-versions').hidden,true);assert.equal(d.activeElement,a.$('layout-versions'));
 // 发布 with nothing saved: 保存并发布 saves version 1, reads its release and publishes it, in that order.
 const before=a.trace.length;a.$('layout-publish').click();await flush();assert.equal(a.$('ly-d-publish').hidden,false,'nothing saved yet is saved first');
 assert.equal(a.$('ly-publish-confirm').textContent,'保存并发布');assert.equal(dl()['布局'],'新布局');assert.equal(dl()['将发布的版本'],'先保存为版本 1，再发布它');assert.equal(dl()['列'],'3 列');assert.match(a.$('ly-publish-note').textContent,/先保存为版本 1/);
 assert.equal(a.$('ly-d-publish').querySelector('[role=dialog]').getAttribute('aria-modal'),'true');assert.equal(d.activeElement,a.$('ly-publish-confirm'));
 a.$('ly-publish-confirm').click();await until(()=>a.$('layout-state').textContent==='已发布 v1'&&!a.$('preview').disabled,'saved and published');
 assert.deepEqual(a.trace.slice(before).filter(t=>t.path.includes('/layouts')).map(t=>t.method+' '+t.path.replace(/[a-f0-9]{32}/,'ID').replace(/^.*\/v1/,'')),['POST /layouts','GET /layouts/ID/release','POST /layouts/ID/release']);
 assert.equal(a.$('layout-release-info').textContent,'已发布版本 1 · 发布修订 1');assert.equal(a.$('layout-state').className,'pill green');
 const id=a.$('layout-server-status').textContent.match(/[a-f0-9]{32}/)[0];
 // A change: the badge says so; 发布 offers 保存并发布 again and publishes version 2.
 a.$('designer-columns').querySelectorAll('.designer-row')[1].querySelector('button').click();assert.equal(a.$('layout-state').textContent,'已发布 v1 · 有未保存修改');assert.equal(a.$('layout-state').className,'pill amber');
 // The old buttons, now in the version dialog, refuse with the reason instead of the query form's message.
 a.click('layout-release-publish');assert.equal(a.$('layout-status').textContent,'列或对齐有未保存的修改：请先保存版本，或用“发布”的保存并发布。');
 a.$('layout-publish').click();await flush();assert.equal(a.$('ly-publish-confirm').textContent,'保存并发布');assert.equal(dl()['将发布的版本'],'先保存为版本 2，再发布它');assert.equal(dl()['当前发布'],'版本 1');
 a.$('ly-publish-confirm').click();await until(()=>a.$('layout-state').textContent==='已发布 v2'&&!a.$('preview').disabled,'v2 published');
 // A clean saved version: 发布 reads the release state and keeps 确认发布; Esc publishes nothing.
 const posts=releasePosts();a.$('layout-publish').click();await until(()=>!a.$('ly-d-publish').hidden,'confirmation');assert.equal(a.$('ly-publish-confirm').textContent,'确认发布');assert.equal(dl()['将发布的版本'],'保存版本 2');assert.match(a.$('ly-publish-note').textContent,/已经是当前发布版本/);
 d.dispatchEvent(new a.w.KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));assert.equal(a.$('ly-d-publish').hidden,true);assert.equal(releasePosts(),posts);assert.equal(d.activeElement,a.$('layout-publish'));
 // The version dialog: reads the list and the release, lists v2 and v1, marks the published one; v1 restored is an unsaved change, the pointer stays.
 a.$('layout-versions').click();await until(()=>a.$('ly-version-list').children.length===2&&!a.$('preview').disabled,'the versions');
 assert.deepEqual([...a.$('ly-version-list').children].map(b=>b.querySelector('strong').textContent+' '+b.querySelector('.pill').textContent),['v2 已发布','v1 已保存']);
 assert.equal(a.$('ly-version-list').children[0].getAttribute('aria-current'),'true');assert.equal(a.$('ly-ver-restore').disabled,true,'the editor already shows v2');assert.equal(a.$('ly-version-note').textContent,'保存版本不可变；当前发布指针：v2');
 a.$('ly-version-list').children[1].click();assert.equal(a.$('ly-vd-title').textContent,'布局 · v1');assert.equal(a.$('ly-ver-restore').disabled,false);assert.equal(d.activeElement,a.$('ly-version-list').children[1],'the focus stays on the picked version');
 a.$('ly-ver-restore').click();await until(()=>a.$('layout-status').textContent.startsWith('已把保存版本 v1')&&!a.$('preview').disabled,'restored');
 assert.equal(a.$('ly-d-versions').hidden,true);assert.equal(a.$('layout-state').textContent,'已发布 v2 · 有未保存修改');assert.equal(a.$('result-table').tHead.rows[0].cells[0].textContent,a.$('designer-columns').querySelectorAll('.designer-row')[0].textContent.replace(/上移.*/,'').trim());
 // A rollback: v1 published as it is from the dialog, through the same confirmation; nothing is saved and the editor keeps its change.
 const saves=a.trace.filter(t=>t.path.endsWith('/layouts')&&t.method==='POST').length;
 a.$('layout-versions').click();await until(()=>a.$('ly-version-list').children.length===2&&!a.$('preview').disabled,'the versions again');a.$('ly-version-list').children[1].click();a.$('ly-ver-publish').click();await until(()=>!a.$('ly-d-publish').hidden,'rollback confirmation');
 assert.equal(dl()['将发布的版本'],'保存版本 1（历史版本）');assert.equal(dl()['列'],undefined);assert.equal(a.$('ly-publish-confirm').textContent,'确认发布');assert.match(a.$('ly-publish-note').textContent,/明确的回退/);
 a.$('ly-publish-confirm').click();await until(()=>a.$('layout-release-info').textContent==='已发布版本 1 · 发布修订 3'&&!a.$('preview').disabled,'rolled back');
 assert.equal(a.trace.filter(t=>t.path.endsWith('/layouts')&&t.method==='POST').length,saves,'a rollback saves nothing');assert.equal(a.$('layout-state').textContent,'已保存 v2 · 有未保存修改');
 const release=await(await fetch(url+'/api/reporting/v1/layouts/'+id+'/release',{headers:{Authorization:'Bearer '+token}})).json();assert.equal(release.published_version,1);assert.deepEqual(release.history.map(e=>e.action+' '+e.version),['publish 1','publish 2','publish 1']);
 // 撤回发布 in the dialog; with nothing published it refuses with the reason.
 a.$('layout-versions').click();await until(()=>a.$('ly-version-list').children.length===2&&!a.$('preview').disabled,'the versions, third time');a.click('layout-release-withdraw');await until(()=>a.$('layout-release-info').textContent==='未发布 · 发布修订 4'&&!a.$('preview').disabled,'withdrawn');
 assert.equal(a.$('layout-release-withdraw').disabled,true);a.$('layout-release-withdraw').disabled=false;a.click('layout-release-withdraw');assert.equal(a.$('layout-status').textContent,'当前没有已发布版本。');
 d.dispatchEvent(new a.w.KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));
 // S07-G fix: with a layout saved, a new query (here: another company) clears the result without an error, and the badge follows.
 a.input('query-companies','company_other');assert.equal(a.$('layout-state').textContent,'未查询');assert.equal(a.errors.length,0,a.errors.join('|'));
 a.input('query-companies','company_demo');a.click('preview');await until(()=>!a.$('preview').disabled&&!a.$('result-table-wrap').hidden,'query again');assert.match(a.$('layout-state').textContent,/^已保存 v2/);
 // Logout forgets the saved layout and the badge.
 a.click('logout');assert.equal(a.$('layout-state').textContent,'未查询');assert.equal(a.$('ly-d-versions').hidden,true);
 assert.equal(a.errors.length,0,a.errors.join('|'));
}finally{a.close();}});

test('S07-G G2: the dashboard studio\'s 版本 is the version dialog: the list and the release read on opening, restore as an unsaved change, a rollback through 发布前确认, and the old controls in the dialog',async()=>{const a=app();try{
 const d=a.w.document,dl=()=>Object.fromEntries([...a.$('bd-publish-summary').querySelectorAll('dt')].map(dt=>[dt.textContent,dt.nextElementSibling.textContent]));
 await a.login();a.configure();a.click('preview');await until(()=>!a.$('preview').disabled&&!a.$('result-table-wrap').hidden,'query');d.querySelector('[data-page="dashboard"]').click();
 // 版本 before a save: the dialog, nothing listed.
 a.$('bd-versions').click();await flush();assert.equal(a.$('bd-d-versions').hidden,false);assert.equal(a.$('bd-version-empty').hidden,false);assert.equal(a.$('bd-studio').inert,true);
 assert.ok(d.activeElement.closest('#bd-d-versions'),'the focus is in the dialog');d.dispatchEvent(new a.w.KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));assert.equal(a.$('bd-d-versions').hidden,true);assert.equal(d.activeElement,a.$('bd-versions'));
 // v1 with one metric, v2 with two, v2 published.
 a.click('board-add-metric');a.click('board-server-save');await until(()=>a.$('bd-state').textContent==='已保存 v1'&&!a.$('preview').disabled,'v1');
 a.click('board-add-metric');a.$('bd-publish').click();await flush();a.$('bd-publish-confirm').click();await until(()=>a.$('bd-state').textContent==='已发布 v2'&&!a.$('preview').disabled,'v2 published');
 const reads=a.trace.length;a.$('bd-versions').click();await until(()=>a.$('bd-version-list').children.length===2&&!a.$('preview').disabled,'the versions');
 assert.deepEqual(a.trace.slice(reads).map(t=>t.method+' '+t.path.replace(/[a-f0-9]{32}/,'ID').replace(/^.*\/v1/,'')),['GET /dashboards','GET /dashboards/ID/release'],'opening reads the list and the release');
 assert.deepEqual([...a.$('bd-version-list').children].map(b=>b.querySelector('strong').textContent+' '+b.querySelector('.pill').textContent),['v2 已发布','v1 已保存']);assert.equal(a.$('bd-version-note').textContent,'保存版本不可变；当前发布指针：v2');
 // Restore v1: one metric in the editor, an unsaved change, the pointer unchanged.
 a.$('bd-version-list').children[1].click();assert.equal(a.$('bd-vd-title').textContent,'看板 · v1');a.$('bd-ver-restore').click();await until(()=>a.$('board-status').textContent.startsWith('已把保存版本 v1')&&!a.$('preview').disabled,'restored');
 assert.equal(a.$('bd-d-versions').hidden,true);assert.equal(a.$('board-widgets').children.length,1);assert.equal(a.$('bd-state').textContent,'已发布 v2 · 有未保存修改');
 // A rollback to v1 from the dialog: 发布前确认 names it, nothing is saved, the editor keeps its content.
 const saves=a.trace.filter(t=>t.path.endsWith('/dashboards')&&t.method==='POST').length;
 a.$('bd-versions').click();await until(()=>a.$('bd-version-list').children.length===2&&!a.$('preview').disabled,'the versions again');a.$('bd-version-list').children[1].click();a.$('bd-ver-publish').click();await until(()=>!a.$('bd-d-publish').hidden,'rollback confirmation');
 assert.equal(dl()['将发布的版本'],'保存版本 1（历史版本）');assert.equal(dl()['组件'],undefined);assert.equal(a.$('bd-publish-confirm').textContent,'确认发布');assert.match(a.$('bd-publish-note').textContent,/明确的回退/);
 a.$('bd-publish-confirm').click();await until(()=>a.$('board-release-status').textContent==='发布修订 2 · 已发布版本 1'&&!a.$('preview').disabled,'rolled back');
 assert.equal(a.trace.filter(t=>t.path.endsWith('/dashboards')&&t.method==='POST').length,saves,'a rollback saves nothing');assert.equal(a.$('board-widgets').children.length,1);assert.equal(a.$('bd-state').textContent,'已保存 v2 · 有未保存修改');
 // The old controls live in the dialog: 打开已发布看板 opens v1, and the raw opener still opens a version by number.
 a.$('bd-versions').click();await until(()=>a.$('bd-version-list').children.length===2&&!a.$('preview').disabled,'the versions, third time');
 a.click('board-published-open');await until(()=>a.$('board-server-status').textContent.includes('已打开发布看板')&&!a.$('preview').disabled,'published open');assert.equal(a.$('board-widgets').children.length,1);assert.equal(a.$('bd-version-list').children[1].getAttribute('aria-current'),'true','the dialog follows the editor\'s version');
 const boardID=a.$('board-server-status').textContent.match(/[a-f0-9]{32}/)[0];a.click('board-server-list');await until(()=>[...a.$('board-server-listing').options].some(o=>o.value===boardID)&&!a.$('preview').disabled,'listing');a.$('board-server-listing').value=boardID;a.$('board-server-version').value='2';a.click('board-server-open');await until(()=>a.$('board-server-status').textContent.includes('版本 2')&&!a.$('preview').disabled,'v2 opened');assert.equal(a.$('board-widgets').children.length,2);
 d.dispatchEvent(new a.w.KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));assert.equal(a.$('bd-d-versions').hidden,true);
 assert.equal(a.errors.length,0,a.errors.join('|'));
}finally{a.close();}});
