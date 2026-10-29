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
import {inflateSync} from 'node:zlib';

const root=resolve(import.meta.dirname,'..');
const html=await readFile(join(root,'console/assets/index.html'),'utf8');
const script=await readFile(join(root,'console/assets/app.js'),'utf8');
const dashboardScript=await readFile(join(root,'console/assets/dashboard.js'),'utf8');
const canvasScript=await readFile(join(root,'console/assets/canvas.js'),'utf8');
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
  w.eval(dashboardScript);w.eval(canvasScript);w.eval(documentsScript);w.eval(script);
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
 for(const id of ['doc-undo','doc-redo','doc-dup','doc-del','doc-align-left','doc-align-center','doc-align-right','doc-align-top','doc-align-middle','doc-align-bottom','doc-distribute-h','doc-distribute-v','doc-check','doc-palette-open'])assert.ok(a.$(id).textContent.trim().length>0,id);
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
 assert.deepEqual([...a.$('doc2-bands').children].map(r=>r.querySelector('.cv2-name').textContent),['页眉带（3 个元素）','单据头带（2 个元素）','单据尾带（2 个元素）','页脚带（1 个元素）','明细带：必有，只放一个明细表；高度随页面伸展（至少 24.2 毫米：表头、承前行、一行明细与过次或合计行）。']);
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
 assert.deepEqual(ids(),['band','source','field','x','y','width','height','font_size','align','wrap','bold','delete'],'every key of a field text but its id');
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
 a.$('doc2-panel').hidden=true;cv.key(a,cv.item(a,first),'k',{ctrlKey:true});assert.ok(!options(a).includes('在所选带加线'),'inside a hidden panel');press(a,'Escape');
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
