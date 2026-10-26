'use strict';
(function () {
  const base = '/api/reporting/v1';
  const $ = (id) => document.getElementById(id);
  const pages = {overview:'工作区概览',catalog:'数据目录',workspace:'报表工作台',tasks:'任务工作区',system:'能力与环境'};
  const state = {token:'',epoch:0,view:0,catalogOperation:0,catalogLoading:false,pageLimit:'25',session:null,capabilities:null,product:'',datasets:[],catalogs:new Map(),jobs:new Map(),controllers:new Set(),snapshot:null,query:null,page:0,cursor:'',lastResult:null,pending:null,busy:0};
  const statuses = {queued:'排队中',running:'执行中',succeeded:'已完成',failed:'失败',cancelled:'已取消',restricted:'访问受限'};
  class Failure extends Error { constructor(status,code) { super(code); this.status=status; this.code=code; } }
  class Stale extends Error {}
  function node(tag,text,className) { const n=document.createElement(tag); if(text!==undefined)n.textContent=text; if(className)n.className=className; return n; }
  function button(text,fn,className) { const b=node('button',text,'button '+(className||'quiet small')); b.type='button'; b.addEventListener('click',fn); return b; }
  function copy(value) { return JSON.parse(JSON.stringify(value)); }
  function isID(id) { return typeof id==='string' && /^[a-f0-9]{32}$/.test(id); }
  function current(mark) { return mark.epoch===state.epoch && mark.view===state.view; }
  function mark() { return {epoch:state.epoch,view:state.view}; }
  function notify(message,type) { $('notice').textContent=message; $('notice').className='notice'+(type?' '+type:''); $('notice').hidden=false; }
  function busy(change) {
    state.busy=Math.max(0,state.busy+change);
    const blocked=!state.session||state.busy>0||state.catalogLoading;
    document.querySelectorAll('.action').forEach(b=>b.disabled=blocked);
    $('export').disabled=blocked||!state.snapshot;
    $('next-page').disabled=blocked||!state.cursor;$('first-page').disabled=blocked||state.page<=1;$('page-size').disabled=blocked||!state.snapshot;
    for(const id of ['query-dataset','query-companies','query-date','refresh-catalog'])$(id).disabled=!state.session||state.catalogLoading;
  }
  function clearResult() {
    state.snapshot=null;state.query=null;state.page=0;state.cursor='';state.lastResult=null;
    $('result-table').tHead.replaceChildren();$('result-table').tBodies[0].replaceChildren();
    $('result-summary').replaceChildren();$('result-summary').hidden=true;
    $('result-table-wrap').hidden=true;$('result-empty').hidden=false;$('result-empty').replaceChildren(node('span','▤','empty-icon'),node('b','从一个已发布的数据集开始'),node('p','金额与大整数按原始精度显示。'));$('result-pager').hidden=true;$('result-evidence').replaceChildren();$('result-evidence').hidden=true;
    $('result-kind').textContent='尚未查询';$('result-kind').className='badge neutral';$('result-title').textContent='查询结果';
    $('result-subtitle').textContent='选择数据集并填写公司范围后开始查询。';$('refresh-result').disabled=true;$('export').disabled=true;
  }
  function disconnect(message) {
    state.epoch++;state.view++;state.catalogOperation++;state.catalogLoading=false;state.pageLimit='25';$('page-size').value='25';for(const c of state.controllers)c.abort();state.controllers.clear();
    state.token='';state.session=null;state.capabilities=null;state.product='';state.datasets=[];state.catalogs.clear();state.jobs.clear();state.pending=null;state.busy=0;
    clearResult();renderJobs();$('catalog-grid').replaceChildren();$('overview-catalog').replaceChildren(node('div','连接工作区后加载当前授权目录。','empty compact'));
    $('identity-details').replaceChildren();$('capability-details').replaceChildren();$('overview-products').textContent='—';$('overview-datasets').textContent='—';$('overview-formats').textContent='—';
    for(const id of ['catalog-product','query-product','query-dataset'])$(id).replaceChildren();
    $('query-companies').value='';$('query-date').value='';$('query-timezone').textContent='—';$('query-currency').textContent='—';$('query-shape').textContent='选择数据集后显示批准组合';
    $('restore-id').value='';$('access-token').value='';$('actor-label').textContent='未连接';$('tenant-label').textContent='访问令牌仅保留在本次会话';$('connection-label').textContent='尚未连接';$('connection-dot').className='status-dot';$('logout').disabled=true;
    $('connection-overlay').hidden=false;document.querySelector('.shell').inert=true;$('connect-button').disabled=false;
    $('notice').hidden=true;$('connect-error').hidden=!message;$('connect-error').textContent=message||'';busy(0);$('access-token').focus();
  }
  function errorMessage(error) {
    if(error.status===401)return '访问令牌无效或已失效，请重新连接。';
    if(error.status===403)return error.code==='INSUFFICIENT_TOKEN_SCOPE'?'令牌缺少报表访问范围。':'当前身份无权访问，或原数据范围已被撤销。';
    if(error.status===404)return '未找到当前身份有权访问的资源；它也可能已过期。';
    if(error.status===503)return '当前授权来源不可用，请稍后重试。';
    if(error.code==='INTEGRITY_FAILURE')return '资源完整性校验失败，内容已停止显示。';
    if(error.status===409)return '请求冲突、资源失效或完整性检查未通过，请重新查询。';
    if(error.status===413)return '结果超过当前限额，请缩小范围。';
    if(error.status===422)return '当前数据集不支持此查询组合。';
    if(error.status===400)return '请检查数据集、公司范围及数据时点。';
    if(error.code==='INVALID_RESPONSE')return '服务返回了无法验证的响应。';
    return '请求结果尚未确认，请稍后重试；同一待确认提交会复用原请求标识。';
  }
  function fail(error,m,context) {
    if(error instanceof Stale||error.name==='AbortError'||(m&&!current(m)))return;
    const affectsResult=!context||(context.snapshot&&context.snapshot===state.snapshot);
    if(affectsResult&&([403,404,409,503].includes(error.status)||error.code==='INVALID_RESPONSE'))clearResult();
    notify((context?context.label+'：':'')+errorMessage(error),'error');
  }
  async function readBounded(response,limit) {
    const declared=response.headers.get('Content-Length');
    if(declared && (!/^\d+$/.test(declared)||Number(declared)>limit))throw new Failure(0,'INVALID_RESPONSE');
    if(!response.body)throw new Failure(0,'INVALID_RESPONSE');
    const reader=response.body.getReader();const chunks=[];let length=0;
    try { while(true) { const next=await reader.read();if(next.done)break;length+=next.value.length;if(length>limit)throw new Failure(0,'INVALID_RESPONSE');chunks.push(next.value); } }
    finally { await reader.cancel().catch(()=>{});reader.releaseLock(); }
    const bytes=new Uint8Array(length);let offset=0;for(const chunk of chunks){bytes.set(chunk,offset);offset+=chunk.length;}return bytes;
  }
  async function request(path,options) {
    if(!state.token)throw new Stale();
    const epoch=state.epoch;const controller=new AbortController();state.controllers.add(controller);
    const timer=setTimeout(()=>controller.abort(),25000);
    const headers={'Authorization':'Bearer '+state.token,'Accept':'application/json'};
    const settings=options||{};if(settings.body!==undefined)headers['Content-Type']='application/json';
    if(settings.key)headers['Idempotency-Key']=settings.key;
    try {
      const response=await fetch(base+path,{method:settings.method||'GET',headers:headers,body:settings.body===undefined?undefined:JSON.stringify(settings.body),signal:controller.signal,credentials:'same-origin',cache:'no-store'});
      const bytes=await readBounded(response,settings.binary?(8<<20):(2<<20));
      if(epoch!==state.epoch)throw new Stale();
      if(!response.ok) {
        let code='REQUEST_FAILED';try{const problem=JSON.parse(new TextDecoder().decode(bytes));if(typeof problem.error==='string')code=problem.error;}catch(_){}
        if(response.status===401){disconnect('访问令牌无效或已失效，请重新连接。');throw new Stale();}
        throw new Failure(response.status,code);
      }
      if(settings.binary) {
        if(response.headers.get('Content-Type')!=='application/vnd.openxmlformats-officedocument.spreadsheetml.sheet')throw new Failure(409,'INTEGRITY_FAILURE');
        const expected=response.headers.get('X-Content-SHA256');
        if(!/^[a-f0-9]{64}$/.test(expected||'')||!window.crypto.subtle)throw new Failure(409,'INTEGRITY_FAILURE');
        const actual=Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256',bytes))).map(b=>b.toString(16).padStart(2,'0')).join('');
        if(epoch!==state.epoch)throw new Stale();
        if(actual!==expected)throw new Failure(409,'INTEGRITY_FAILURE');
        return bytes;
      }
      if(!response.headers.get('Content-Type')?.includes('application/json'))throw new Failure(0,'INVALID_RESPONSE');
      try{return JSON.parse(new TextDecoder().decode(bytes));}catch(_){throw new Failure(0,'INVALID_RESPONSE');}
    } finally { clearTimeout(timer);state.controllers.delete(controller); }
  }
  function openPage(page) {
    if(!pages[page])page='overview';
    for(const name of Object.keys(pages))$('page-'+name).hidden=name!==page;
    document.querySelectorAll('.nav-item').forEach(b=>{const active=b.dataset.page===page;b.classList.toggle('active',active);if(active)b.setAttribute('aria-current','page');else b.removeAttribute('aria-current');});
    $('breadcrumb-title').textContent=pages[page];history.replaceState(null,'','#'+page);
  }
  function selected() { return state.datasets.find(d=>d.dataset.id+'@'+d.dataset.version===$('query-dataset').value); }
  function datasetTitle(d) { const labels=d.columns.filter(c=>['decimal','integer'].includes(c.type)).map(c=>c.label);return labels.length?labels.join(' / '):d.dataset.id; }
  function validDataset(d) { return d && typeof d.dataset?.id==='string'&&typeof d.dataset.version==='string'&&Array.isArray(d.columns)&&d.columns.length<=100&&d.columns.every(c=>typeof c.id==='string'&&typeof c.label==='string'&&typeof c.type==='string')&&Array.isArray(d.metrics)&&Array.isArray(d.dimensions)&&typeof d.timezone==='string'; }
  function dateInZone(zone) { try {const parts=new Intl.DateTimeFormat('en',{timeZone:zone,year:'numeric',month:'2-digit',day:'2-digit'}).formatToParts(new Date());const get=t=>parts.find(p=>p.type===t).value;return get('year')+'-'+get('month')+'-'+get('day');}catch(_){return '';} }
  function queryChanged() {if(state.catalogLoading)return;state.view++;clearResult();const d=selected();$('query-timezone').textContent=d?.timezone||'—';$('query-currency').textContent=d?.currency_policy?d.currency_policy.id+' @ '+d.currency_policy.version:'无换算政策';$('query-shape').textContent=d?d.dimensions.length+' 个维度 / '+d.metrics.length+' 个指标':'选择数据集后显示批准组合';}
  function renderCatalog() {
    $('overview-datasets').textContent=state.catalogLoading?'—':String(state.datasets.length);
    const overview=$('overview-catalog');overview.replaceChildren();const catalog=$('catalog-grid');catalog.replaceChildren();
    if(!state.datasets.length){const message=state.catalogLoading?'正在读取当前授权目录…':'当前身份尚无可用数据集。';overview.append(node('div',message,'empty compact'));catalog.append(node('div',message,'empty compact'));return;}
    for(const d of state.datasets) {
      const row=node('div',undefined,'dataset-row');const info=node('div',undefined,'dataset-info');info.append(node('span','▤','dataset-icon'));
      const label=node('div');label.append(node('h3',datasetTitle(d)),node('p',d.dataset.id+' · v'+d.dataset.version));info.append(label);
      row.append(info,button('打开 →',()=>chooseDataset(d),'quiet small'));overview.append(row);
      const card=node('article',undefined,'panel catalog-card');const top=node('div',undefined,'card-top');top.append(node('span','▤','dataset-icon'),node('span','已发布组合','badge'));card.append(top,node('h2',datasetTitle(d)),node('p',d.dataset.id+' · v'+d.dataset.version,'description'));
      const columns=node('div',undefined,'column-list');
      for(const c of d.columns) { const line=node('div',undefined,'column-line');line.append(node('b',c.label),node('code',c.type+(c.currency?' · '+c.currency:'')+(c.unit?' · '+c.unit:'')));columns.append(line); }
      card.append(columns);const bottom=node('div',undefined,'card-bottom');bottom.append(node('small',d.dimensions.length+' 个维度 · '+d.metrics.length+' 个指标'),button('打开工作台 →',()=>chooseDataset(d),'secondary small'));card.append(bottom);catalog.append(card);
    }
  }
  function chooseDataset(d) {$('query-dataset').value=d.dataset.id+'@'+d.dataset.version;queryChanged();$('query-date').value=dateInZone(d.timezone);openPage('workspace');}
  async function loadProduct(id,refresh) {
    const epoch=state.epoch,operation=++state.catalogOperation;state.product=id;state.view++;state.catalogLoading=true;clearResult();busy(0);$('catalog-product').value=id;$('query-product').value=id;state.datasets=[];renderCatalog();$('query-dataset').replaceChildren();
    const active=()=>epoch===state.epoch&&operation===state.catalogOperation&&id===state.product;
    try {
      let datasets=state.catalogs.get(id);
      if(!datasets||refresh)datasets=await request('/catalog?integration_id='+encodeURIComponent(id));
      if(!active())throw new Stale();
      if(!Array.isArray(datasets)||datasets.length>100||!datasets.every(validDataset))throw new Failure(0,'INVALID_RESPONSE');
      state.catalogs.set(id,datasets);state.datasets=datasets;$('query-dataset').replaceChildren();
      for(const d of datasets){const option=node('option',datasetTitle(d)+' · v'+d.dataset.version);option.value=d.dataset.id+'@'+d.dataset.version;$('query-dataset').append(option);}
      state.catalogLoading=false;renderCatalog();queryChanged();$('query-date').value=selected()?dateInZone(selected().timezone):'';
    } catch(error) {if(!active())throw new Stale();throw error;}
    finally {if(active()){state.catalogLoading=false;busy(0);}}
  }
  function addDetails(id,pairs) {const target=$(id);target.replaceChildren();for(const [label,value] of pairs){const row=node('div');row.append(node('dt',label),node('dd',String(value)));target.append(row);}}
  async function connect(event) {
    event.preventDefault();const token=$('access-token').value.trim();if(!token)return;
    disconnect();state.token=token;$('connect-error').hidden=true;$('connect-button').disabled=true;const epoch=state.epoch;
    try {
      const session=await request('/session');const caps=await request('/capabilities');
      if(epoch!==state.epoch)throw new Stale();
      if(!session.identity||!['tenant','issuer','subject'].every(k=>typeof session.identity[k]==='string'&&session.identity[k])||!Array.isArray(session.integrations)||!session.integrations.length||session.integrations.length>32||!session.integrations.every(i=>typeof i==='string'&&i.length<=128)||!Array.isArray(caps.formats))throw new Failure(0,'INVALID_RESPONSE');
      state.session=session;state.capabilities=caps;
      for(const id of ['catalog-product','query-product'])for(const integration of session.integrations){const option=node('option',integration);option.value=integration;$(id).append(option);}
      $('overview-products').textContent=String(session.integrations.length);$('overview-formats').textContent=caps.formats.map(f=>String(f).toUpperCase()).join(' / ');
      $('actor-label').textContent=session.identity.subject;$('tenant-label').textContent=session.identity.tenant;$('connection-label').textContent='API 已连接';$('connection-dot').className='status-dot connected';$('logout').disabled=false;
      addDetails('identity-details',[['平台租户',session.identity.tenant],['当前主体',session.identity.subject],['发行方',session.identity.issuer],['产品连接',session.integrations.join('、')]]);
      addDetails('capability-details',[['来源配置',caps.profile],['数据类别','合同模拟器 / 合成数据'],['查询组合',caps.query_shapes],['导出格式',caps.formats.join('、')],['生产准入',caps.production_approved===true?'已准入':'尚未准入']]);
      $('connection-overlay').hidden=true;document.querySelector('.shell').inert=false;busy(0);openPage(location.hash.slice(1));$('main').focus();
      try{await loadProduct(session.integrations[0]);}catch(error){fail(error);}
    }catch(error){if(!(error instanceof Stale)){disconnect(errorMessage(error));}}
    finally{if(!state.session)$('connect-button').disabled=false;}
  }
  function buildQuery() {
    const d=selected();const companies=$('query-companies').value.split(/[,，]/).map(s=>s.trim()).filter(Boolean);
    if(!d||!companies.length||companies.length>16||new Set(companies).size!==companies.length||companies.some(s=>s.length>128)||!$('query-date').value)throw new Failure(400,'INVALID_REQUEST');
    const q={schema_version:'1.0',integration_id:state.product,dataset:copy(d.dataset),company_ids:companies,metrics:copy(d.metrics),dimensions:copy(d.dimensions),filters:[],sort:[],as_of:$('query-date').value,timezone:d.timezone,page:{limit:50}};
    if(d.currency_policy)q.currency_policy=copy(d.currency_policy);return q;
  }
  function checkResult(result) {
    if(!result||!Array.isArray(result.columns)||result.columns.length>100||!result.columns.every(c=>typeof c.id==='string'&&typeof c.label==='string'&&typeof c.type==='string')||!Array.isArray(result.rows)||result.rows.length>1000||!result.rows.every(row=>Array.isArray(row)&&row.length===result.columns.length&&row.every(value=>typeof value==='string'))||!result.totals||typeof result.totals!=='object'||!Object.values(result.totals).every(v=>typeof v==='string'))throw new Failure(0,'INVALID_RESPONSE');
  }
  function renderResult(result,query,meta) {
    checkResult(result);state.lastResult=result;state.query=copy(query);
    const header=node('tr');for(const c of result.columns)header.append(node('th',c.label+(c.currency?' ('+c.currency+')':c.unit?' ('+c.unit+')':'')));$('result-table').tHead.replaceChildren(header);
    const body=$('result-table').tBodies[0];body.replaceChildren();
    for(const values of result.rows){const row=node('tr');values.forEach((v,i)=>{const c=result.columns[i];const cell=node('td',v,['decimal','integer'].includes(c.type)?'precise':'');cell.title=v;row.append(cell);});body.append(row);}
    const summary=$('result-summary');summary.replaceChildren();
    for(const [id,value] of Object.entries(result.totals).slice(0,20)){const column=result.columns.find(c=>c.id===id);const card=node('div',undefined,'summary-card');card.append(node('small',(column?.label||id)+' · 原范围合计'+(column?.currency?' / '+column.currency:column?.unit?' / '+column.unit:'')),node('strong',value));summary.append(card);}
    summary.hidden=!summary.children.length;$('result-table-wrap').hidden=!result.rows.length;$('result-empty').hidden=!!result.rows.length;
    if(!result.rows.length){$('result-empty').replaceChildren(node('b','当前授权范围没有结果'),node('p','请检查数据时点与公司选择。'));}
    $('result-title').textContent=datasetTitle({columns:result.columns,dataset:query.dataset});
    $('result-kind').textContent=meta?'冻结快照':'查询预览';$('result-kind').className='badge'+(meta?'':' neutral');
    $('result-subtitle').textContent=meta?'同一快照分页 · '+query.as_of+' · '+query.timezone:'当前显示 '+result.rows.length+' 行 · '+(result.completeness==='preview'?'有界预览，不能作为完整导出':'当前批准组合的完整结果');
    const evidence=$('result-evidence');evidence.replaceChildren();if(result.evidence)for(const [label,key] of [['查询摘要','query_hash'],['来源引用','source_snapshot_ref'],['来源水位','source_watermark']]){const value=result.evidence[key];if(typeof value==='string'){const span=node('span',label+' '+(value.length>40?value.slice(0,36)+'…':value));span.title=value;evidence.append(span);}}
    evidence.hidden=!evidence.children.length;$('refresh-result').disabled=false;
    $('result-pager').hidden=!meta;
    if(meta){$('page-info').textContent='第 '+state.page+' 页 · 共 '+meta.total+' 行';$('next-page').disabled=!state.cursor;$('first-page').disabled=state.page<=1;}
    $('export').disabled=!state.snapshot||state.busy>0;
  }
  function renderJobs() {
    const values=Array.from(state.jobs.values()).reverse();$('task-count').textContent=String(values.length);$('overview-jobs').textContent=String(values.length);$('tasks-note').textContent=values.length+' 项';
    $('tasks-empty').hidden=!!values.length;$('tasks-table-wrap').hidden=!values.length;const body=$('tasks-body');body.replaceChildren();
    for(const j of values){const row=node('tr');const title=node('td');title.append(node('b',j.kind==='export'?'Excel 导出':j.kind==='snapshot'?'报表快照':'私有任务'));const id=node('td',j.job_id,'task-id');id.title=j.job_id;const status=node('td');status.append(node('span',statuses[j.state]||'待刷新','badge'+(j.state==='failed'||j.state==='restricted'?' danger':j.state==='succeeded'?'':' neutral')));const actions=node('td');
      actions.append(button('刷新',()=>refreshTask(j.job_id)));
      if(j.snapshot_id&&j.state==='succeeded')actions.append(button('查看结果',()=>perform(async m=>{await loadSnapshot(j.snapshot_id,j.query||null,m,true);if(current(m))openPage('workspace');},taskContext(j.job_id,'任务查看'))));
      if(j.artifact_id&&j.state==='succeeded')actions.append(button('下载',()=>perform(m=>download(j.artifact_id,m),taskContext(j.job_id,'任务下载'))));
      if(['queued','running'].includes(j.state))actions.append(button('取消',()=>cancelJob(j.job_id),'danger'));
      row.append(title,id,status,actions);body.append(row);
    }
  }
  function checkJob(j) {if(!j||!isID(j.job_id)||!['snapshot','export'].includes(j.kind)||!Object.hasOwn(statuses,j.state)||(j.snapshot_id&&!isID(j.snapshot_id))||(j.artifact_id&&!isID(j.artifact_id)))throw new Failure(0,'INVALID_RESPONSE');}
  function rememberJob(j,context) {checkJob(j);const previous=state.jobs.get(j.job_id)||{};state.jobs.set(j.job_id,Object.assign({},previous,context||{},j));if(state.jobs.size>100)state.jobs.delete(state.jobs.keys().next().value);renderJobs();return state.jobs.get(j.job_id);}
  async function refreshJob(id) {const epoch=state.epoch;try{const j=await request('/jobs/'+encodeURIComponent(id));if(epoch!==state.epoch)throw new Stale();return rememberJob(j);}catch(error){if(epoch!==state.epoch)throw new Stale();const j=state.jobs.get(id);if(j&&[403,404,503].includes(error.status)){j.state='restricted';delete j.snapshot_id;delete j.artifact_id;renderJobs();}throw error;}}
  function taskContext(id,label) {return {snapshot:state.jobs.get(id)?.snapshot_id,label:label};}
  async function refreshTask(id,m=mark()) {const context=taskContext(id,'任务刷新');try{await refreshJob(id);return true;}catch(error){fail(error,m,context);return false;}}
  async function cancelJob(id) {const m=mark(),context=taskContext(id,'任务取消');try{const j=await request('/jobs/'+encodeURIComponent(id)+'/cancel',{method:'POST'});rememberJob(j);if(current(m))notify(j.state==='cancelled'?'任务已取消。':'任务已结束，未更改已发布结果。');}catch(error){fail(error,m,context);}}
  async function submit(kind,payload,context) {
    const fingerprint=kind+JSON.stringify(payload);
    if(!state.pending||state.pending.fingerprint!==fingerprint)state.pending={fingerprint:fingerprint,key:crypto.randomUUID()};
    const key=state.pending.key;
    try {const job=await request(kind==='snapshot'?'/snapshot-jobs':'/exports',{method:'POST',body:payload,key:key});rememberJob(job,context);state.pending=null;return job;}
    catch(error){if(error.status>=400&&error.status<500)state.pending=null;throw error;}
  }
  async function poll(id,m) {
    for(let i=0;i<100;i++){if(!current(m))throw new Stale();const job=await refreshJob(id);if(!current(m))throw new Stale();if(!['queued','running'].includes(job.state))return job;await new Promise(resolve=>setTimeout(resolve,300));}
    if(!current(m))throw new Stale();notify('任务仍在服务器执行，可在任务工作区继续查看。');return null;
  }
  async function perform(fn,context) {if(state.busy||state.catalogLoading)return;const m=mark();busy(1);try{await fn(m);}catch(error){fail(error,m,context);}finally{if(m.epoch===state.epoch)busy(-1);}}
  async function preview(m) {const q=buildQuery();clearResult();const result=await request('/queries',{method:'POST',body:q});if(!current(m))throw new Stale();renderResult(result,q,null);notify('预览已更新。完整冻结导出请先创建快照。','success');}
  async function loadSnapshot(id,query,m,first) {
    if(!isID(id))throw new Failure(400,'INVALID_REQUEST');
    if(!first&&(state.snapshot!==id||!state.cursor))throw new Failure(400,'INVALID_REQUEST');
    const next=first?'':state.cursor,targetPage=first?1:state.page+1;
    const limit=$('page-size').value;if(!['2','25','50'].includes(limit))throw new Failure(400,'INVALID_REQUEST');state.pageLimit=limit;
    const page=await request('/snapshots/'+id+'/rows?limit='+encodeURIComponent(limit)+(next?'&cursor='+encodeURIComponent(next):''));
    if(!current(m))throw new Stale();checkResult(page);if(page.snapshot_id!==id||!Number.isSafeInteger(page.total_row_count)||page.total_row_count<0||page.total_row_count>1000||(page.next_cursor&&typeof page.next_cursor!=='string'))throw new Failure(0,'INVALID_RESPONSE');
    if(!query){const known=Array.from(state.jobs.values()).find(j=>j.snapshot_id===id);query=known?.query;}
    if(!query){query={dataset:{id:'已保存的快照',version:''},as_of:'原数据时点',timezone:'原快照时区'};}
    state.snapshot=id;state.cursor=page.next_cursor||'';state.page=targetPage;renderResult(page,query,{total:page.total_row_count});busy(0);
  }
  async function createSnapshot(m) {
    const q=buildQuery();clearResult();const job=await submit('snapshot',q,{query:copy(q),integration:q.integration_id});if(!current(m))throw new Stale();
    notify('快照任务已提交，正在后台执行。');const done=await poll(job.job_id,m);if(!done)return;
    if(done.state==='succeeded'&&done.snapshot_id){await loadSnapshot(done.snapshot_id,q,m,true);if(current(m))notify('冻结快照已生成，可分页查看或导出。','success');}
    else notify(done.state==='cancelled'?'快照任务已取消。':'快照任务失败，请检查当前授权与查询范围。',done.state==='cancelled'?'':'error');
  }
  async function createExport(m) {
    const id=state.snapshot;const query=copy(state.query);if(!id)throw new Failure(400,'INVALID_REQUEST');
    const job=await submit('export',{snapshot_id:id},{query:query,integration:query.integration_id});if(!current(m))throw new Stale();
    notify('Excel 导出任务已提交。');const done=await poll(job.job_id,m);if(!done)return;
    if(done.state==='succeeded'&&done.artifact_id)await download(done.artifact_id,m);
    else notify(done.state==='cancelled'?'导出任务已取消。':'导出任务失败，未生成可下载文件。',done.state==='cancelled'?'':'error');
  }
  async function download(id,m) {
    if(!isID(id))throw new Failure(400,'INVALID_REQUEST');const bytes=await request('/artifacts/'+id+'/content',{binary:true});if(!current(m))throw new Stale();
    const url=URL.createObjectURL(new Blob([bytes],{type:'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet'}));const link=node('a');link.href=url;link.download='report-'+id+'.xlsx';document.body.append(link);link.click();link.remove();setTimeout(()=>URL.revokeObjectURL(url),1000);notify('Excel 已通过摘要校验，下载已发起。','success');
  }
  document.querySelectorAll('[data-page]').forEach(b=>b.addEventListener('click',()=>openPage(b.dataset.page)));
  document.querySelectorAll('[data-open]').forEach(b=>b.addEventListener('click',()=>openPage(b.dataset.open)));
  $('connect-form').addEventListener('submit',connect);$('logout').addEventListener('click',()=>disconnect());
  for(const id of ['catalog-product','query-product'])$(id).addEventListener('change',()=>loadProduct($(id).value).catch(error=>fail(error)));
  $('query-dataset').addEventListener('change',()=>{queryChanged();$('query-date').value=selected()?dateInZone(selected().timezone):'';});
  for(const id of ['query-companies','query-date'])$(id).addEventListener('input',queryChanged);
  $('refresh-catalog').addEventListener('click',()=>{if(state.product)loadProduct(state.product,true).then(()=>notify('数据目录已刷新。','success')).catch(error=>fail(error));});
  $('preview').addEventListener('click',()=>perform(preview));$('snapshot').addEventListener('click',()=>perform(createSnapshot));$('export').addEventListener('click',()=>perform(createExport));
  $('refresh-result').addEventListener('click',()=>perform(m=>state.snapshot?loadSnapshot(state.snapshot,state.query,m,true):preview(m)));
  $('first-page').addEventListener('click',()=>perform(m=>loadSnapshot(state.snapshot,state.query,m,true)));$('next-page').addEventListener('click',()=>perform(m=>loadSnapshot(state.snapshot,state.query,m,false)));
  $('page-size').addEventListener('change',()=>{if(state.busy||state.catalogLoading){$('page-size').value=state.pageLimit;return;}if(state.snapshot){state.view++;perform(m=>loadSnapshot(state.snapshot,state.query,m,true));}});
  $('restore-job').addEventListener('click',()=>{const id=$('restore-id').value.trim();perform(async m=>{if(!isID(id))throw new Failure(400,'INVALID_REQUEST');await refreshJob(id);if(current(m))notify('已恢复当前身份可访问的任务。','success');},taskContext(id,'任务恢复'));});
  $('refresh-tasks').addEventListener('click',()=>perform(async m=>{let complete=true;for(const id of Array.from(state.jobs.keys())){if(!current(m))throw new Stale();if(!await refreshTask(id,m))complete=false;}if(complete&&current(m))notify('已知任务状态已刷新。','success');},{label:'任务刷新'}));
  $('connection-overlay').addEventListener('keydown',event=>{if(event.key!=='Tab')return;const first=$('access-token'),last=$('connect-button');if(event.shiftKey&&document.activeElement===first){event.preventDefault();last.focus();}else if(!event.shiftKey&&document.activeElement===last){event.preventDefault();first.focus();}});
  window.addEventListener('hashchange',()=>openPage(location.hash.slice(1)));
  openPage(location.hash.slice(1));disconnect();
})();
