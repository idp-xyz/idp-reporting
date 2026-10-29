// Software DOM + real loopback Go HTTP tests of the embed pages (S03-N, ADR 0006); this is NOT browser/visual QA (scripts/browser_embed_s03n.mjs is).
// The embed page is the one the real server serves for a listed parent origin; its parent is a stub window whose postMessage records what the page
// says and to which origin. Messages to the page are dispatched with the source and origin a browser would give them.
import {test,before,after} from 'node:test';
import assert from 'node:assert/strict';
import {readFile,mkdtemp,rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join,resolve} from 'node:path';
import {spawn,execFileSync} from 'node:child_process';
import {createServer} from 'node:net';
import {randomBytes,webcrypto} from 'node:crypto';
import {JSDOM,VirtualConsole} from 'jsdom';

const root=resolve(import.meta.dirname,'..');
const read=name=>readFile(join(root,'console/assets',name),'utf8');
const [indexHtml,embedHtml,appScript,dashboardScript,canvasScript,documentsScript,embedScript]=await Promise.all(['index.html','embed.html','app.js','dashboard.js','canvas.js','documents.js','embed.js'].map(read));
const token=randomBytes(32).toString('hex');
const pdfConfigured=['REPORTING_TYPST','REPORTING_TYPST_SHA256','REPORTING_PDF_FONTS'].every(k=>process.env[k]);
if(process.env.REPORTING_PDF_REQUIRE==='1'&&!pdfConfigured)throw new Error('REPORTING_PDF_REQUIRE=1 but the Typst binary, its checksum or the fonts are not configured');
// The parent origin the demo is told to allow (nothing listens there: the parent is a stub window) and one it is not told about.
const HOST='http://127.0.0.1:7811',STRANGER='http://127.0.0.1:7899',FINANCE='brant-contract-demo',SHIPMENT='shipment-contract-demo';
const DATASET={id:'receivables.aging',version:'1.0.0'};
let temporary,child,url;
const sleep=ms=>new Promise(resolve=>setTimeout(resolve,ms));
async function until(fn,label,limit=6000){const end=Date.now()+limit;while(Date.now()<end){if(fn())return;await sleep(15);}throw new Error('DOM condition not reached: '+label);}

before(async()=>{
  temporary=await mkdtemp(join(tmpdir(),'reporting-s03n-dom-'));
  const go=process.env.REPORTING_GO||'go';
  execFileSync(go,['build','-o',join(temporary,'reporting-demo'),'./cmd/reporting-demo'],{cwd:root,env:{...process.env,GOTOOLCHAIN:'local'},stdio:['ignore','pipe','pipe'],timeout:120000});
  const listener=createServer();await new Promise(resolve=>listener.listen(0,'127.0.0.1',resolve));const port=listener.address().port;await new Promise(resolve=>listener.close(resolve));
  url='http://127.0.0.1:'+port;
  const pdf=pdfConfigured?['-pdf-typst',process.env.REPORTING_TYPST,'-pdf-typst-sha256',process.env.REPORTING_TYPST_SHA256,'-pdf-fonts',process.env.REPORTING_PDF_FONTS]:[];
  child=spawn(join(temporary,'reporting-demo'),['-profile','contract-simulator','-memory','-listen','127.0.0.1:'+port,'-layout-directory',join(temporary,'layouts'),'-dashboard-directory',join(temporary,'dashboards'),'-template-directory',join(temporary,'templates'),'-background-directory',join(temporary,'backgrounds'),'-embed-origin',FINANCE+'='+HOST,'-embed-origin',SHIPMENT+'='+HOST,...pdf],{cwd:root,env:{...process.env,REPORTING_DEMO_TOKEN:token},stdio:['ignore','ignore','pipe']});
  child.stderr.resume();
  for(let i=0;i<100;i++){try{const r=await fetch(url+'/console/');if(r.ok)return;}catch(_){}if(child.exitCode!==null)throw new Error('Go fixture did not start');await sleep(30);}
  throw new Error('Go fixture startup timeout');
});
after(async()=>{if(child&&child.exitCode===null){child.kill('SIGTERM');await new Promise(resolve=>child.once('exit',resolve));}if(temporary)await rm(temporary,{recursive:true});});

const api=(path,options={})=>fetch(url+'/api/reporting/v1'+path,{...options,headers:{Authorization:'Bearer '+token,...(options.body?{'Content-Type':'application/json'}:{}),...options.headers}});
async function seedDashboard(integration=FINANCE){
  const config={schema_version:'1.0',kind:'dashboard',binding:{integration_id:integration,dataset:DATASET},widgets:[{type:'metric',field:'ar.open_balance'}]};
  const r=await api('/dashboards',{method:'POST',body:JSON.stringify({id:'',expected_version:0,config})});assert.equal(r.status,201);return (await r.json()).id;
}
async function seedTemplate(text='Embedded title'){
  const config={schema_version:'1.0',kind:'document',binding:{integration_id:FINANCE,dataset:DATASET},elements:[{id:'n1',source:'literal',field:'',text,x:10,y:25,width:190,height:12,font_size:18,align:'left'}]};
  const r=await api('/document-templates',{method:'POST',body:JSON.stringify({id:'',expected_version:0,config})});assert.equal(r.status,201);return (await r.json()).id;
}
const problem=(status,code)=>new Response(JSON.stringify({error:code}),{status,headers:{'Content-Type':'application/json'}});
// The tokens a page may be given in these tests: the real one, and these that the harness translates to it (any other token is refused by the real server).
const FRESH='fresh-token-1',FRESH2='fresh-token-2';
const accepted=new Map([[token,token],[FRESH,token],[FRESH2,token]]);

// An embed page: served by the real server for the given view and resource, run in jsdom with a stub parent. intercept(path, options, who) may answer a request.
async function embedPage({view='dashboard',resource,integration=FINANCE,parent=HOST,intercept,framed=true,location,observer=true,shrink=false}={}){
  const resourceKey={report:'dataset',dashboard:'dashboard','document-designer':'template'}[view];
  const params=new URLSearchParams({integration,parent});
  for(const [k,v] of Object.entries(resource||{[resourceKey]:resourceKey==='dataset'?DATASET.id+'@'+DATASET.version:'0'.repeat(32)}))params.set(k,v);
  const target='/embed/'+view+'?'+params;
  const served=await fetch(url+target);
  assert.equal(served.status,200,'the real server serves the embed page');
  const html=await served.text();
  const errors=[],sent=[],trace=[],timers=[],observers=[],virtualConsole=new VirtualConsole();
  virtualConsole.on('jsdomError',error=>{if(!error.message.includes('navigation'))errors.push(error.message);});
  const dom=new JSDOM(html,{url:location||url+target,runScripts:'outside-only',pretendToBeVisual:true,virtualConsole});
  const w=dom.window;
  // The parent: another window, whose postMessage records what the page says and to which origin.
  const hostWindow=new JSDOM('',{url:parent+'/'}).window;
  hostWindow.postMessage=(message,targetOrigin)=>sent.push({message:JSON.parse(JSON.stringify(message)),targetOrigin});
  if(framed)Object.defineProperty(w,'parent',{value:hostWindow,configurable:true});
  Object.defineProperty(w,'crypto',{value:webcrypto});
  w.TextDecoder=TextDecoder;w.TextEncoder=TextEncoder;w.Blob=Blob;w.AbortController=AbortController;
  const blobs=new Map(),downloads=[];
  w.URL.createObjectURL=blob=>{const id='blob:fixture-'+blobs.size;blobs.set(id,blob);return id;};
  w.URL.revokeObjectURL=id=>blobs.delete(id);
  w.HTMLAnchorElement.prototype.click=function(){if(this.download)downloads.push({name:this.download,blob:blobs.get(this.href)});};
  const realSet=w.setTimeout.bind(w);
  w.setTimeout=(fn,ms,...rest)=>{timers.push({fn,ms});return realSet(fn,shrink&&ms===10000?30:ms,...rest);};
  if(observer)w.ResizeObserver=class{constructor(callback){observers.push(callback);}observe(){}disconnect(){}};
  w.fetch=async(path,options={})=>{
    const headers={...(options.headers||{})};
    trace.push({method:options.method||'GET',path:String(path),headers,credentials:options.credentials,body:options.body});
    const given=(headers.Authorization||'').replace(/^Bearer /,'');
    if(intercept){const answer=await intercept(String(path),options,given);if(answer)return answer;}
    return fetch(url+path,{...options,headers:{...headers,...(accepted.has(given)?{Authorization:'Bearer '+accepted.get(given)}:{})}});
  };
  w.eval(dashboardScript);w.eval(canvasScript);w.eval(documentsScript);w.eval(embedScript);w.eval(appScript);
  const $=id=>w.document.getElementById(id);
  const input=(id,value)=>{$(id).value=value;$(id).dispatchEvent(new w.Event('input',{bubbles:true}));};
  const click=id=>$(id).click();
  // A message as a browser delivers it: from a window, with that window's origin.
  const post=(data,{origin=parent,source=hostWindow}={})=>w.dispatchEvent(new w.MessageEvent('message',{data,origin,source}));
  const give=(value=token,extra={})=>post({type:'reporting:token',v:1,token:value,...extra});
  const messages=type=>sent.map(s=>s.message).filter(m=>m.type===type);
  async function connected(){await until(()=>$('connection-overlay').hidden&&$('query-dataset').options.length>0&&!$('preview').disabled,'session, catalog and the resource');}
  return {w,$,input,click,post,give,messages,connected,sent,trace,timers,observers,errors,downloads,hostWindow,close:()=>w.close(),dom,target};
}
const PAGES={report:'page-workspace',dashboard:'page-dashboard','document-designer':'page-documents'};
const queried=async a=>{a.input('query-companies','company_demo');a.click('preview');await until(()=>!a.$('result-table-wrap').hidden&&!a.$('preview').disabled,'query result');};
// Everything that left the page as a request or a message, searched for a secret.
const everything=a=>JSON.stringify([a.trace.map(t=>[t.method,t.path,t.body||'']),a.sent]);

test('the embed page shows nothing and says ready to the exact parent origin, once, holding no credential',async()=>{
  for(const view of ['report','dashboard','document-designer']){
    const a=await embedPage({view});try{
      assert.equal(a.errors.length,0,view+': '+a.errors.join('|'));
      assert.equal(a.$('connection-overlay').hidden,false);assert.equal(a.w.document.querySelector('.shell').hidden,true);
      assert.deepEqual(a.sent,[{message:{type:'reporting:ready',v:1},targetOrigin:HOST}]);
      assert.equal(a.trace.length,0,'no request before the parent gives a token');
      // No token form, no logout, no navigation: nothing to type a token into.
      assert.equal(a.$('access-token'),null);assert.equal(a.$('connect-form'),null);assert.equal(a.$('logout'),null);
      assert.equal(a.w.document.querySelector('input[type=password]'),null);assert.equal(a.w.document.querySelector('nav, aside, .sidebar, .topbar'),null);
      assert.equal(a.w.localStorage.length,0);assert.equal(a.w.sessionStorage.length,0);
    }finally{a.close();}
  }
});

test('a token is taken from the parent window and origin only, in the closed form, and nothing else starts a session',async()=>{
  const a=await embedPage();try{
    const stranger=new JSDOM('',{url:HOST+'/'}).window,wrong=new JSDOM('',{url:STRANGER+'/'}).window;
    const good={type:'reporting:token',v:1,token};
    const attempts=[
      ['the right window, another origin',good,{origin:STRANGER}],['another window, the right origin',good,{source:stranger}],['another window, another origin',good,{source:wrong,origin:STRANGER}],
      ['no source',good,{source:null}],['the page itself',good,{source:a.w}],['the origin null',good,{origin:'null'}],['the origin with a path',good,{origin:HOST+'/'}],['a sub-domain lookalike',good,{origin:'http://127.0.0.1.evil.example:7811'}],
      ['version 2',{...good,v:2},{}],['version as text',{...good,v:'1'},{}],['no version',{type:good.type,token},{}],['another type',{...good,type:'reporting:token-x'},{}],['an unknown type',{type:'reporting:login',v:1,token},{}],
      ['the type of the other direction',{...good,type:'reporting:ready'},{}],['a string',JSON.stringify(good),{}],['an array',[good],{}],['null',null,{}],['a number',1,{}],
      ['no token',{type:good.type,v:1},{}],['a number as the token',{...good,token:123},{}],['an object as the token',{...good,token:{x:token}},{}],['an empty token',{...good,token:''},{}],
      ['a token with a space',{...good,token:'abc def'},{}],['a token with a newline',{...good,token:'abc\ndef'},{}],['a token with a semicolon',{...good,token:'abc;def'},{}],['a token with a Bearer prefix',{...good,token:'Bearer '+token},{}],
      ['a token with non-ASCII text',{...good,token:'abcé'},{}],['a token over 32 KiB',{...good,token:'a'.repeat(32769)},{}],['a token with equals in the middle',{...good,token:'abc=def'},{}],
    ];
    for(const [name,data,where] of attempts){a.post(data,where);}
    await sleep(80);
    assert.equal(a.trace.length,0,'a request was made after a message that must be ignored');
    assert.equal(a.$('connection-overlay').hidden,false);
    assert.deepEqual(a.messages('reporting:token-request'),[]);
    // The same token from the parent starts the session.
    a.give();await a.connected();
    assert.equal(a.trace[0].path,'/api/reporting/v1/session');assert.equal(a.trace[0].headers.Authorization,'Bearer '+token);
    assert.equal(a.errors.length,0,a.errors.join('|'));
  }finally{a.close();}
});

test('the token is only ever the Bearer header of requests to the page\'s own origin, and is nowhere else',async()=>{
  const a=await embedPage({view:'report'});try{
    a.give();await a.connected();await queried(a);
    a.click('snapshot');await until(()=>a.$('result-kind').textContent==='冻结快照'&&!a.$('export').disabled,'snapshot');
    a.click('export');await until(()=>a.downloads.length===1,'workbook');
    assert.ok(a.trace.length>=6);
    for(const t of a.trace){
      assert.ok(t.path.startsWith('/api/reporting/v1/'),'a request to another place: '+t.path);
      assert.equal(t.headers.Authorization,'Bearer '+token);assert.equal(t.credentials,'omit');
      assert.equal(Object.entries(t.headers).filter(([k,v])=>String(v).includes(token)).map(([k])=>k).join(),'Authorization','the token is in a header other than Authorization');
      assert.equal(t.path.includes(token)||String(t.body||'').includes(token),false);
    }
    assert.equal(everything(a).includes(token),false);
    assert.equal(a.w.localStorage.length,0);assert.equal(a.w.sessionStorage.length,0);assert.equal(a.w.document.cookie,'');
    assert.equal(a.w.location.href.includes(token),false);assert.equal(a.w.document.documentElement.outerHTML.includes(token),false);
    for(const e of a.w.document.querySelectorAll('input,textarea,select'))assert.equal(String(e.value).includes(token),false);
    assert.equal(a.w.document.body.textContent.includes(token),false);
    assert.equal(a.errors.length,0,a.errors.join('|'));
  }finally{a.close();}
});

test('each view shows only its page, with the integration of its address and the query panel where the view needs one',async()=>{
  const dashboard=await seedDashboard();
  for(const [view,resource,page] of [['report',{dataset:DATASET.id},'page-workspace'],['dashboard',{dashboard},'page-dashboard'],['document-designer',{template:await seedTemplate()},'page-documents']]){
    const a=await embedPage({view,resource});try{
      a.give();await a.connected();
      for(const [name,id] of Object.entries(PAGES))assert.equal(a.$(id).hidden,id!==page,view+': '+name);
      assert.equal(a.w.document.querySelector('.query-panel').closest('.page').id,page);assert.equal(a.$('page-workspace').hidden,page!=='page-workspace');
      assert.deepEqual([...a.$('query-product').options].map(o=>o.value),[FINANCE]);assert.equal(a.$('query-product').value,FINANCE);
      assert.equal(a.w.document.querySelector('.shell').hidden,false);
      assert.equal(a.errors.length,0,view+': '+a.errors.join('|'));
    }finally{a.close();}
  }
});

test('the report viewer selects the dataset its address names, and refuses one that is not the identity\'s or not unique',async()=>{
  const a=await embedPage({view:'report',resource:{dataset:DATASET.id+'@'+DATASET.version}});try{
    a.give();await a.connected();await until(()=>a.$('query-dataset').value===DATASET.id+'@'+DATASET.version,'dataset selected');
    assert.equal(a.messages('reporting:error').length,0);assert.notEqual(a.$('query-date').value,'');
  }finally{a.close();}
  for(const dataset of [DATASET.id+'@9.9.9','unknown.dataset','receivables']){
    const b=await embedPage({view:'report',resource:{dataset}});try{
      b.give();await until(()=>b.messages('reporting:error').length>0,'refusal');
      assert.deepEqual(b.messages('reporting:error'),[{type:'reporting:error',v:1,code:'RESOURCE_UNAVAILABLE'}]);assert.match(b.$('notice').textContent,/未找到/);
    }finally{b.close();}
  }
});

test('a saved dashboard named by the address opens when its authorized result is on the page, and only if it is the identity\'s and its integration\'s',async()=>{
  const id=await seedDashboard();
  const a=await embedPage({view:'dashboard',resource:{dashboard:id}});try{
    a.give();await a.connected();await until(()=>a.$('query-dataset').value===DATASET.id+'@'+DATASET.version,'dataset of the dashboard');
    assert.equal(a.$('board-widgets').children.length,0,'the dashboard is not shown before there is an authorized result');
    await queried(a);await until(()=>a.$('board-widgets').children.length===1&&a.$('board-server-status').textContent.includes(id),'dashboard opened');
    assert.match(a.$('board-widgets').textContent,/9007199254740993\.01/);
    // The same address never opened anything the page did not first read through the API under the Bearer header.
    assert.ok(a.trace.some(t=>t.path==='/api/reporting/v1/dashboards/'+id+'?version=1'));
    assert.equal(a.messages('reporting:error').length,0);assert.equal(a.errors.length,0,a.errors.join('|'));
  }finally{a.close();}
  // Not the identity's: no dashboard of that id in the list.
  const b=await embedPage({view:'dashboard',resource:{dashboard:'a'.repeat(32)}});try{
    b.give();await until(()=>b.messages('reporting:error').length>0,'refusal');
    assert.deepEqual(b.messages('reporting:error')[0],{type:'reporting:error',v:1,code:'RESOURCE_UNAVAILABLE'});
  }finally{b.close();}
  // Bound to another integration than the one the page is embedded for.
  const c=await embedPage({view:'dashboard',integration:SHIPMENT,resource:{dashboard:id}});try{
    c.give();await until(()=>c.messages('reporting:error').length>0,'refusal');
    assert.deepEqual(c.messages('reporting:error')[0],{type:'reporting:error',v:1,code:'RESOURCE_UNAVAILABLE'});assert.equal(c.$('board-widgets').children.length,0);
  }finally{c.close();}
});

test('a saved template named by the address, with or without a version, opens in the designer after the query; saves and publishes are told to the parent by id',async()=>{
  const id=await seedTemplate('Embedded title');
  const a=await embedPage({view:'document-designer',resource:{template:id}});try{
    a.give();await a.connected();await until(()=>a.$('query-dataset').value===DATASET.id+'@'+DATASET.version,'dataset of the template');
    await queried(a);await until(()=>a.$('doc-server-status').textContent.includes('正在编辑模板 '+id),'template opened');
    assert.match(a.$('doc-elements').textContent,/Embedded title/,'the template\'s text is in the designer');
    assert.equal(a.messages('reporting:event').length,0,'opening is not an event');
    a.input('doc-text','Second line');a.click('doc-add-text');
    a.click('doc-server-save');await until(()=>a.$('doc-server-status').textContent.includes('版本 2')&&!a.$('preview').disabled,'saved');
    assert.deepEqual(a.messages('reporting:event'),[{type:'reporting:event',v:1,name:'saved',template_id:id,version:2}]);
    a.click('doc-release-read');await until(()=>a.$('doc-release-status').textContent.includes('修订')&&!a.$('preview').disabled,'release state');
    a.click('doc-release-publish');await until(()=>a.$('doc-release-status').textContent.includes('已发布版本')&&!a.$('preview').disabled,'published');
    assert.deepEqual(a.messages('reporting:event').map(m=>[m.name,m.template_id,m.version]),[['saved',id,2],['published',id,2]]);
    // A withdrawal is not an event, and neither is a refused save: the parent hears of saves and publishes only.
    a.click('doc-release-withdraw');await until(()=>a.$('doc-release-status').textContent.includes('当前未发布')&&!a.$('preview').disabled,'withdrawn');
    assert.equal(a.messages('reporting:event').length,2);
    assert.equal(a.errors.length,0,a.errors.join('|'));
  }finally{a.close();}
  // An address that names a version opens that version, not the latest.
  const b=await embedPage({view:'document-designer',resource:{template:id,version:'1'}});try{
    b.give();await b.connected();await queried(b);await until(()=>b.$('doc-server-status').textContent.includes('正在编辑模板 '+id+' · 版本 1'),'version 1 opened');
    assert.equal(b.messages('reporting:error').length,0);
  }finally{b.close();}
  const c=await embedPage({view:'document-designer',resource:{template:id,version:'20'}});try{
    c.give();await until(()=>c.messages('reporting:error').length>0,'no version 20');assert.equal(c.messages('reporting:error')[0].code,'NOT_FOUND');
  }finally{c.close();}
  const d=await embedPage({view:'document-designer',resource:{template:'b'.repeat(32)}});try{
    d.give();await until(()=>d.messages('reporting:error').length>0,'unknown template');assert.equal(d.messages('reporting:error')[0].code,'RESOURCE_UNAVAILABLE');
  }finally{d.close();}
});

test('an official issue is told to the parent by job id once the job has succeeded',{skip:!pdfConfigured&&'needs the pinned Typst renderer'},async()=>{
  const id=await seedTemplate('Issued title');
  const a=await embedPage({view:'document-designer',resource:{template:id}});try{
    a.give();await a.connected();await queried(a);await until(()=>a.$('doc-server-status').textContent.includes('正在编辑模板 '+id),'opened');
    a.click('snapshot');await until(()=>a.$('result-kind').textContent==='冻结快照'&&!a.$('preview').disabled,'frozen');
    a.click('doc-release-read');await until(()=>a.$('doc-release-status').textContent.includes('修订')&&!a.$('preview').disabled,'release');
    a.click('doc-release-publish');await until(()=>a.$('doc-release-status').textContent.includes('已发布版本 1')&&!a.$('preview').disabled,'published');
    a.click('doc-published-open');await until(()=>a.$('doc-server-status').textContent.includes('已打开发布模板')&&!a.$('preview').disabled,'published open');
    a.input('doc-row','1');a.click('doc-issue');await until(()=>a.messages('reporting:event').some(m=>m.name==='issued'),'issued event',20000);
    const issued=a.messages('reporting:event').find(m=>m.name==='issued');
    assert.deepEqual(Object.keys(issued).sort(),['job_id','name','type','v']);assert.match(issued.job_id,/^[a-f0-9]{32}$/);
    await until(()=>a.downloads.length===1,'issued download',20000);
    assert.equal(a.errors.length,0,a.errors.join('|'));
  }finally{a.close();}
});

test('a 401 asks the parent for a fresh token once, waits for it, and retries the request once with it',async()=>{
  let refused=0;
  const a=await embedPage({view:'report',intercept:async(path,options,who)=>{if(path.endsWith('/queries')&&who===token){refused++;return problem(401,'AUTH_SCOPE_DENIED');}}});try{
    a.give();await a.connected();a.input('query-companies','company_demo');a.click('preview');
    await until(()=>a.messages('reporting:token-request').length===1,'token request');
    await sleep(60);
    assert.equal(a.trace.filter(t=>t.path.endsWith('/queries')).length,1,'the request is not retried before a fresh token has come');
    assert.equal(a.$('connection-overlay').hidden,true,'a 401 does not sign out while a fresh token may come');
    // The same token again is no fresh token; another window's token is none either.
    a.give(token);a.post({type:'reporting:token',v:1,token:FRESH},{source:new JSDOM('',{url:HOST+'/'}).window});a.give(FRESH,{});
    await until(()=>a.$('result-table').tBodies[0].rows.length>0,'retried with the fresh token');
    const queries=a.trace.filter(t=>t.path.endsWith('/queries'));
    assert.deepEqual(queries.map(t=>t.headers.Authorization),['Bearer '+token,'Bearer '+FRESH]);
    assert.equal(a.messages('reporting:token-request').length,1,'one request for one 401');
    assert.equal(a.$('connection-overlay').hidden,true);assert.equal(refused,1);
    // The fresh token is in use from now on, and was checked as the same user's first.
    assert.ok(a.trace.some(t=>t.path==='/api/reporting/v1/session'&&t.headers.Authorization==='Bearer '+FRESH));
    a.click('refresh-result');await until(()=>!a.$('preview').disabled,'again');
    assert.equal(a.trace.filter(t=>t.path.endsWith('/queries')).at(-1).headers.Authorization,'Bearer '+FRESH);
    assert.equal(a.errors.length,0,a.errors.join('|'));
  }finally{a.close();}
});

test('a second 401 is the console\'s signed-out state: data cleared, one error code to the parent, no loop, and a new token starts over',async()=>{
  let queries=0;
  const a=await embedPage({view:'report',intercept:async(path,options,who)=>{if(path.endsWith('/queries')&&(who===token||who===FRESH)){queries++;return problem(401,'AUTH_SCOPE_DENIED');}}});try{
    a.give();await a.connected();
    a.input('query-companies','company_demo');a.click('preview');
    await until(()=>a.messages('reporting:token-request').length===1,'token request');
    a.give(FRESH);
    await until(()=>a.$('connection-overlay').hidden===false,'signed out');
    assert.equal(queries,2,'one request, one retry');
    assert.equal(a.messages('reporting:token-request').length,1);
    assert.deepEqual(a.messages('reporting:error'),[{type:'reporting:error',v:1,code:'UNAUTHENTICATED'}]);
    assert.equal(a.$('connect-error').hidden,false);assert.equal(a.w.document.querySelector('.shell').hidden,true);
    assert.equal(a.$('result-table').tBodies[0].rows.length,0);assert.equal(a.$('query-dataset').options.length,0);assert.equal(a.$('identity-details').children.length,0);
    await sleep(100);assert.equal(queries,2);assert.equal(a.messages('reporting:token-request').length,1,'no loop');
    // The parent can start it again with a token, or the person can ask it to.
    a.click('embed-retry');assert.equal(a.messages('reporting:token-request').length,2);
    a.give(FRESH2);await a.connected();
    assert.equal(a.$('connect-error').hidden,true);
    assert.equal(a.errors.length,0,a.errors.join('|'));
  }finally{a.close();}
});

test('no fresh token in time is signed out, and the token that failed is never taken for a fresh one',async()=>{
  const a=await embedPage({view:'report',shrink:true,intercept:async(path,options,who)=>{if(path.endsWith('/queries')&&who===token)return problem(401,'AUTH_SCOPE_DENIED');}});try{
    a.give();await a.connected();a.input('query-companies','company_demo');a.click('preview');
    await until(()=>a.messages('reporting:token-request').length===1,'token request');
    a.give(token);
    await until(()=>a.$('connection-overlay').hidden===false,'signed out after the wait');
    assert.deepEqual(a.messages('reporting:error'),[{type:'reporting:error',v:1,code:'UNAUTHENTICATED'}]);
    assert.equal(a.trace.filter(t=>t.path.endsWith('/queries')).length,1,'nothing was retried with the token that failed');
  }finally{a.close();}
});

test('a token of another identity is not a refresh: the page starts over and nothing of the previous user stays',async()=>{
  const OTHER='other-identity-token';accepted.set(OTHER,token);
  try{
    let expire=false;
    const a=await embedPage({view:'report',intercept:async(path,options,who)=>{
      if(expire&&path.endsWith('/queries')&&who===token)return problem(401,'AUTH_SCOPE_DENIED');
      if(path.endsWith('/session')&&who===OTHER)return new Response(JSON.stringify({identity:{tenant:'tenant_a',issuer:'issuer',subject:'another-subject'},integrations:[FINANCE]}),{headers:{'Content-Type':'application/json'}});
    }});try{
      a.give();await a.connected();await queried(a);
      assert.ok(a.$('result-table').tBodies[0].rows.length>0,'the previous user\'s result is on the page');
      // A 401 for the first user's next request, and the parent answers with another user's token.
      expire=true;a.click('refresh-result');await until(()=>a.messages('reporting:token-request').length===1,'token request');
      a.give(OTHER);
      await until(()=>a.$('actor-label').textContent==='another-subject','started over as the other identity');
      assert.equal(a.$('result-table').tBodies[0].rows.length,0,'the previous user\'s rows are gone');
      assert.equal(a.$('result-table-wrap').hidden,true);
      assert.equal(a.trace.filter(t=>t.path.endsWith('/queries')&&t.headers.Authorization==='Bearer '+OTHER).length,0,'the request of the previous user was not repeated as the other user');
      assert.equal(a.messages('reporting:error').length,0);assert.equal(a.errors.length,0,a.errors.join('|'));
    }finally{a.close();}
  }finally{accepted.delete(OTHER);}
});

test('a token a parent pushes unasked is adopted only for the same identity, and asked again before it expires',async()=>{
  const jwt=exp=>'e30.'+Buffer.from(JSON.stringify({exp})).toString('base64url')+'.c2ln';
  const expiring=jwt(Math.floor(Date.now()/1000)+90);accepted.set(expiring,token);
  try{
    const a=await embedPage({view:'report'});try{
      a.give(expiring);await a.connected();
      // A timer was set for about 30 s (a minute before the stated expiry); it asks again when it comes due, not before.
      const timer=a.timers.find(t=>t.ms>26000&&t.ms<=31000);assert.ok(timer,'no renewal timer: '+JSON.stringify(a.timers.map(t=>t.ms)));
      const before=a.timers.length;timer.fn();assert.equal(a.messages('reporting:token-request').length,0,'asked too early');
      assert.equal(a.timers.length,before+1,'it waits again');
      a.w.eval('window.__now=Date.now;Date.now=()=>window.__now()+45000');
      a.timers.at(-1).fn();
      assert.equal(a.messages('reporting:token-request').length,1,'asked a minute before the expiry');
      a.w.eval('Date.now=window.__now');
      // The reply, unasked or not, is adopted for the same identity: the next request carries it.
      a.give(FRESH);await until(()=>a.trace.some(t=>t.path.endsWith('/session')&&t.headers.Authorization==='Bearer '+FRESH),'checked');
      await sleep(40);a.input('query-companies','company_demo');a.click('preview');await until(()=>!a.$('result-table-wrap').hidden,'result');
      assert.equal(a.trace.filter(t=>t.path.endsWith('/queries')).at(-1).headers.Authorization,'Bearer '+FRESH);
      // A token that is not a JWT has no expiry to read: nothing is scheduled for it (the first one scheduled the only one).
      const count=a.timers.filter(t=>t.ms>26000&&t.ms<=2147483647).length;a.give(FRESH2);await sleep(60);
      assert.equal(a.timers.filter(t=>t.ms>26000&&t.ms<=2147483647).length,count);
    }finally{a.close();}
  }finally{accepted.delete(expiring);}
});

test('the size is told to the parent in whole pixels whenever it changes, and nothing else about the content',async()=>{
  const a=await embedPage({view:'report'});try{
    let height=412.2;a.w.document.body.getBoundingClientRect=()=>({height});
    a.give();await a.connected();
    await until(()=>a.messages('reporting:resize').length>=1,'first size');
    assert.deepEqual(a.messages('reporting:resize').at(-1),{type:'reporting:resize',v:1,height:413});
    const count=a.messages('reporting:resize').length;a.observers[0]();await sleep(90);assert.equal(a.messages('reporting:resize').length,count,'an unchanged height is not repeated');
    height=980;a.observers[0]();a.observers[0]();await sleep(90);
    assert.deepEqual(a.messages('reporting:resize').slice(count),[{type:'reporting:resize',v:1,height:980}],'coalesced into one');
    height=0;a.observers[0]();await sleep(90);assert.equal(a.messages('reporting:resize').at(-1).height,0);
    for(const s of a.sent)assert.equal(s.targetOrigin,HOST);
  }finally{a.close();}
});

test('the parent can ask for a light or a dark theme, from its window and origin only',async()=>{
  const a=await embedPage({view:'report'});try{
    const root=a.w.document.documentElement;assert.equal(root.getAttribute('data-theme'),null);
    a.post({type:'reporting:theme',v:1,theme:'dark'});assert.equal(root.getAttribute('data-theme'),'dark');
    a.post({type:'reporting:theme',v:1,theme:'light'});assert.equal(root.getAttribute('data-theme'),'light');
    for(const [data,where] of [[{type:'reporting:theme',v:1,theme:'dark'},{origin:STRANGER}],[{type:'reporting:theme',v:1,theme:'dark'},{source:new JSDOM('',{url:HOST+'/'}).window}],[{type:'reporting:theme',v:1,theme:'solarized'},{}],[{type:'reporting:theme',v:1,theme:'<img>'},{}],[{type:'reporting:theme',v:1},{}],[{type:'reporting:theme',v:2,theme:'dark'},{}],[{type:'reporting:theme',theme:'dark'},{}]])a.post(data,where);
    assert.equal(root.getAttribute('data-theme'),'light');
  }finally{a.close();}
});

test('every message to the parent is of the closed list, version 1, with the exact origin as its target, and carries no value',async()=>{
  const id=await seedTemplate('A title that is a business value');
  const a=await embedPage({view:'document-designer',resource:{template:id},shrink:true,intercept:async(path,options,who)=>{if(path.endsWith('/queries')&&who===token)return problem(401,'AUTH_SCOPE_DENIED');}});try{
    a.w.document.body.getBoundingClientRect=()=>({height:700});
    a.give();await a.connected();a.input('query-companies','company_demo');a.click('preview');await until(()=>a.messages('reporting:token-request').length===1,'request');a.give(FRESH);
    await until(()=>a.$('doc-server-status').textContent.includes('正在编辑模板'),'opened',9000);
    a.input('doc-text','Another business value');a.click('doc-add-text');a.click('doc-server-save');await until(()=>a.messages('reporting:event').length===1,'saved');
    a.input('query-companies','company_other');a.click('preview');await until(()=>a.messages('reporting:error').length>0,'an error');
    const shapes={'reporting:ready':['type','v'],'reporting:token-request':['type','v'],'reporting:resize':['height','type','v'],'reporting:error':['code','type','v']};
    const codes=['UNAUTHENTICATED','FORBIDDEN','NOT_FOUND','RESOURCE_UNAVAILABLE','UNAVAILABLE','INVALID_RESPONSE','INTEGRITY_FAILURE','REQUEST_FAILED'];
    assert.ok(a.sent.length>=5);
    for(const {message:m,targetOrigin} of a.sent){
      assert.equal(targetOrigin,HOST);assert.notEqual(targetOrigin,'*');assert.equal(m.v,1);
      if(m.type==='reporting:event'){
        assert.ok(['saved','published','issued'].includes(m.name));
        assert.deepEqual(Object.keys(m).sort(),m.name==='issued'?['job_id','name','type','v']:['name','template_id','type','v','version']);
        for(const key of ['template_id','job_id'])if(key in m)assert.match(m[key],/^[a-f0-9]{32}$/);if('version' in m)assert.ok(Number.isSafeInteger(m.version)&&m.version>=1&&m.version<=20);
      }else{assert.deepEqual(Object.keys(m).sort(),shapes[m.type],m.type);if(m.type==='reporting:error')assert.ok(codes.includes(m.code),m.code);if(m.type==='reporting:resize')assert.ok(Number.isSafeInteger(m.height)&&m.height>=0);}
    }
    const text=JSON.stringify(a.sent);
    for(const secret of [token,FRESH,'business value','company_demo','company_other','9007199254740993','receivables'])assert.equal(text.includes(secret),false,'a message carries '+secret);
    assert.equal(a.errors.length,0,a.errors.join('|'));
  }finally{a.close();}
});

test('an embed page that is not framed, or whose address is not one the server serves, never talks and never takes a token',async()=>{
  const a=await embedPage({view:'report',framed:false});try{
    assert.equal(a.w.parent,a.w);assert.equal(a.sent.length,0);assert.equal(a.$('connect-error').hidden,false);assert.equal(a.$('embed-retry').hidden,true);
    a.post({type:'reporting:token',v:1,token},{origin:HOST,source:a.w});a.post({type:'reporting:token',v:1,token},{origin:url,source:a.w});await sleep(60);
    assert.equal(a.trace.length,0);assert.equal(a.$('connection-overlay').hidden,false);assert.equal(a.errors.length,0,a.errors.join('|'));
  }finally{a.close();}
  for(const location of ['/embed/report?integration='+FINANCE+'&parent='+encodeURIComponent(HOST+'/x')+'&dataset=a','/embed/report?integration='+FINANCE+'&parent=*&dataset=a','/embed/report?integration='+FINANCE+'&parent='+encodeURIComponent(HOST),'/embed/report?integration='+FINANCE+'&parent='+encodeURIComponent(HOST)+'&dataset=a&dashboard='+'a'.repeat(32),'/embed/dashboard?integration='+FINANCE+'&parent='+encodeURIComponent(HOST)+'&dashboard=short','/embed/other?integration='+FINANCE+'&parent='+encodeURIComponent(HOST)+'&dataset=a','/embed/report?integration='+FINANCE+'&parent='+encodeURIComponent('http://example.com')+'&dataset=a']){
    const b=await embedPage({view:'report',location:url+location});try{
      assert.equal(b.sent.length,0,location);assert.equal(b.$('connect-error').hidden,false);
      b.give();await sleep(40);assert.equal(b.trace.length,0);
    }finally{b.close();}
  }
});

test('the token of an identity without the page\'s integration is refused with a coded error, and nothing is shown',async()=>{
  const a=await embedPage({view:'report',location:url+'/embed/report?integration=unlisted-integration&parent='+encodeURIComponent(HOST)+'&dataset=a'});try{
    a.give();await until(()=>a.messages('reporting:error').length>0,'refusal');
    assert.deepEqual(a.messages('reporting:error'),[{type:'reporting:error',v:1,code:'FORBIDDEN'}]);
    assert.equal(a.$('connection-overlay').hidden,false);assert.equal(a.$('query-dataset').options.length,0);assert.equal(a.w.document.querySelector('.shell').hidden,true);
  }finally{a.close();}
});

test('the console page loads none of the embed code, and answers no message',async()=>{
  assert.equal(indexHtml.includes('embed'),false);
  const errors=[],virtualConsole=new VirtualConsole();virtualConsole.on('jsdomError',e=>errors.push(e.message));
  const dom=new JSDOM(indexHtml,{url:url+'/console/',runScripts:'outside-only',virtualConsole});const w=dom.window;
  const calls=[];w.fetch=async p=>{calls.push(p);return new Response('{}');};Object.defineProperty(w,'crypto',{value:webcrypto});w.TextDecoder=TextDecoder;w.TextEncoder=TextEncoder;
  w.eval(dashboardScript);w.eval(canvasScript);w.eval(documentsScript);w.eval(appScript);
  const other=new JSDOM('',{url:HOST+'/'}).window;
  w.dispatchEvent(new w.MessageEvent('message',{data:{type:'reporting:token',v:1,token},origin:HOST,source:other}));
  await sleep(40);
  assert.equal(calls.length,0);assert.equal(w.document.getElementById('connection-overlay').hidden,false);assert.equal(w.createReportingEmbed,undefined);assert.equal(errors.length,0,errors.join('|'));
  w.close();
});

test('the embed page is the console\'s own views and scripts, byte for byte, and its code never writes markup or posts to "*"',async()=>{
  const idsOf=html=>[...html.matchAll(/\sid="([^"]+)"/g)].map(m=>m[1]);
  const section=(html,id)=>{const start=html.indexOf('<section class="page" id="'+id+'"');assert.ok(start>=0,id);return html.slice(start,html.indexOf('</section>',start)+10);};
  for(const id of ['page-workspace','page-dashboard','page-documents'])assert.equal(section(embedHtml,id),section(indexHtml,id),id+' differs from the console\'s');
  const consoleIds=new Set(idsOf(indexHtml)),embedIds=new Set(idsOf(embedHtml));
  assert.equal(new Set(idsOf(embedHtml)).size,idsOf(embedHtml).length,'an id is used twice in the embed page');
  // Everything the shared scripts look up is there, except what the embed must not have: the token form, the logout, the navigation of the pages it leaves out.
  const notInEmbed=['logout','connect-form','access-token','connect-button','page-overview','page-catalog','page-tasks','page-system'];
  assert.deepEqual([...consoleIds].filter(id=>!embedIds.has(id)).sort(),notInEmbed.sort());
  assert.deepEqual([...embedIds].filter(id=>!consoleIds.has(id)).sort(),['embed-inert','embed-retry','embed-state']);
  for(const html of [embedHtml]){const lower=html.toLowerCase();for(const banned of ['<style',' style=',' onclick=',' onload=',' onerror=','javascript:','<iframe','<form','<a '])assert.equal(lower.includes(banned),false,'the embed page contains '+banned);assert.equal([...html.matchAll(/<script/g)].length,[...html.matchAll(/<script src="\/console\/[a-z]+\.js" defer>/g)].length);}
  for(const [name,code] of [['app.js',appScript],['embed.js',embedScript],['dashboard.js',dashboardScript],['documents.js',documentsScript]]){
    for(const pattern of [/\.innerHTML/,/\.outerHTML/,/insertAdjacentHTML/,/document\.write/,/\beval\(/,/new Function/,/\.srcdoc/,/setAttribute\(\s*['"]on/,/\.style\.cssText/,/setAttribute\(\s*['"]style/])assert.equal(pattern.test(code),false,name+' contains '+pattern);
  }
  // One door out, with the origin it was served for: no wildcard target anywhere, and no other posting code.
  assert.equal([...embedScript.matchAll(/postMessage\(/g)].length,1);assert.match(embedScript,/parent\.postMessage\(Object\.assign\(\{type: '', v: 1\}, fields\), origin\)/);
  for(const [name,code] of [['app.js',appScript],['embed.js',embedScript],['dashboard.js',dashboardScript],['documents.js',documentsScript],['canvas.js',canvasScript]]){
    assert.equal(/postMessage\(/.test(code)&&name!=='embed.js',false,name+' posts a message');
    assert.equal(/['"`]\*['"`]\s*\)/.test(code.replace(/\/\*[\s\S]*?\*\//g,'')),false,name+' has a "*" argument');
    assert.equal(/localStorage|sessionStorage|indexedDB|document\.cookie|caches\./.test(code),false,name+' uses storage');
  }
  // The token has one place to go in app.js: the Authorization header of attempt().
  const uses=[...appScript.matchAll(/state\.token/g)].length,headerUses=[...appScript.matchAll(/'Bearer '\+/g)].length;assert.equal(headerUses,1);assert.ok(uses>0);
  assert.equal(/location\.(search|hash)[^;]*token/i.test(appScript+embedScript),false);assert.equal(/history\.(push|replace)State/.test(embedScript),false);
});
