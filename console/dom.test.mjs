// Software DOM + real loopback Go HTTP tests; this is NOT browser/visual QA.
import {test,before,after} from 'node:test';
import assert from 'node:assert/strict';
import {readFile,mkdtemp,rm,mkdir,writeFile} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join,resolve} from 'node:path';
import {spawn,execFileSync} from 'node:child_process';
import {createServer} from 'node:net';
import {randomBytes,webcrypto,createHash} from 'node:crypto';
import {JSDOM,VirtualConsole} from 'jsdom';

const root=resolve(import.meta.dirname,'..');
const html=await readFile(join(root,'console/assets/index.html'),'utf8');
const script=await readFile(join(root,'console/assets/app.js'),'utf8');
const dashboardScript=await readFile(join(root,'console/assets/dashboard.js'),'utf8');
const documentsScript=await readFile(join(root,'console/assets/documents.js'),'utf8');
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
  child=spawn(join(temporary,'reporting-demo'),['-profile','contract-simulator','-memory','-listen','127.0.0.1:'+port,'-layout-directory',join(temporary,'layouts'),'-dashboard-directory',join(temporary,'dashboards'),'-template-directory',join(temporary,'templates'),'-background-directory',join(temporary,'backgrounds'),...pdf],{cwd:root,env:{...process.env,REPORTING_DEMO_TOKEN:token},stdio:['ignore','ignore','pipe']});
  child.stderr.resume();
  for(let i=0;i<100;i++){try{const r=await fetch(url+'/console/');if(r.ok)return;}catch(_){}if(child.exitCode!==null)throw new Error('Go fixture did not start');await sleep(30);}
  throw new Error('Go fixture startup timeout');
});
after(async()=>{if(child&&child.exitCode===null){child.kill('SIGTERM');await new Promise(resolve=>child.once('exit',resolve));}if(temporary)await rm(temporary,{recursive:true});});

function app(intercept){
  const errors=[];const downloads=[];const trace=[];const virtualConsole=new VirtualConsole();virtualConsole.on('jsdomError',error=>{if(!error.message.includes('navigation'))errors.push(error.message);});
  const dom=new JSDOM(html,{url:url+'/console/',runScripts:'outside-only',pretendToBeVisual:true,virtualConsole});
  const w=dom.window;
  Object.defineProperty(w,'crypto',{value:webcrypto});
  w.TextDecoder=TextDecoder;w.TextEncoder=TextEncoder;w.Blob=Blob;w.AbortController=AbortController;
  const blobs=new Map();
  w.URL.createObjectURL=blob=>{const id='blob:fixture-'+blobs.size;blobs.set(id,blob);return id;};
  w.URL.revokeObjectURL=id=>blobs.delete(id);
  w.HTMLAnchorElement.prototype.click=function(){if(this.download){const blob=blobs.get(this.href);downloads.push({name:this.download,blob});}};
  w.fetch=async(path,options)=>{trace.push({method:options?.method||'GET',path:String(path),key:options?.headers?.['Idempotency-Key']});if(intercept){const response=await intercept(String(path),options);if(response)return response;}return fetch(url+path,options);};
  w.eval(dashboardScript);w.eval(documentsScript);w.eval(script);
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
 a.input('query-date','2026-10-25');assert.equal(a.$('board-widgets').children.length,0);assert.equal(a.$('board-json').value,'');a.click('board-add-metric');assert.ok(a.$('board-status').textContent.includes('先在工作台'));denied=true;a.click('preview');await until(()=>a.$('notice').textContent.includes('不可用')&&!a.$('preview').disabled,'source outage');assert.equal(a.$('board-widgets').children.length,0);
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
  try{await snapshotReady(a);assert.equal(a.$('export-pdf').hidden,true);assert.equal(a.$('overview-formats').textContent,'XLSX');assert.equal(a.$('doc-pdf').hidden,true);assert.equal(a.$('doc-saved-pdf').hidden,true);}finally{a.close();}
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
 if(process.env.REPORTING_DOCUMENT_EVIDENCE_DIR){await mkdir(process.env.REPORTING_DOCUMENT_EVIDENCE_DIR,{recursive:true});await writeFile(join(process.env.REPORTING_DOCUMENT_EVIDENCE_DIR,'console_issued.pdf'),bytes);await writeFile(join(process.env.REPORTING_DOCUMENT_EVIDENCE_DIR,'console_issued.json'),JSON.stringify({template_id:id,version:1,row_index:2,sha256:digest,record:raw,source:'real Go issue job through the console; the PDF is the artifact the record names'},null,2));}
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
