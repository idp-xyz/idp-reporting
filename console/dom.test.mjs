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
const token=randomBytes(32).toString('hex');
const artifactDir=process.env.REPORTING_S03A_EVIDENCE_DIR;
let temporary,child,url;
const sleep=ms=>new Promise(resolve=>setTimeout(resolve,ms));
async function until(fn,label,limit=6000){const end=Date.now()+limit;while(Date.now()<end){if(fn())return;await sleep(15);}throw new Error('DOM condition not reached: '+label);}

before(async()=>{
  temporary=await mkdtemp(join(tmpdir(),'reporting-s03a-dom-'));
  const go=process.env.REPORTING_GO||'go';
  execFileSync(go,['build','-o',join(temporary,'reporting-demo'),'./cmd/reporting-demo'],{cwd:root,env:{...process.env,GOTOOLCHAIN:'local'},stdio:['ignore','pipe','pipe'],timeout:120000});
  const listener=createServer();await new Promise(resolve=>listener.listen(0,'127.0.0.1',resolve));const port=listener.address().port;await new Promise(resolve=>listener.close(resolve));
  url='http://127.0.0.1:'+port;
  child=spawn(join(temporary,'reporting-demo'),['-profile','contract-simulator','-memory','-listen','127.0.0.1:'+port,'-layout-directory',join(temporary,'layouts')],{cwd:root,env:{...process.env,REPORTING_DEMO_TOKEN:token},stdio:['ignore','ignore','pipe']});
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
  w.eval(dashboardScript);w.eval(script);
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
