'use strict';
// Dashboard consumes the currently authorized result; its files contain metadata only.
window.createReportingDashboard = function (ports) {
  const $=id=>document.getElementById(id);
  const node=(tag,text)=>{const n=document.createElement(tag);if(text!==undefined)n.textContent=text;return n;};
  let result=null,query=null,config=null,saved=null;
  const binding=()=>({integration_id:query.integration_id,dataset:{id:query.dataset.id,version:query.dataset.version}});
  const matches=b=>b&&b.integration_id===query.integration_id&&b.dataset?.id===query.dataset.id&&b.dataset?.version===query.dataset.version;
  const keys=(value,names)=>value&&typeof value==='object'&&!Array.isArray(value)&&Object.keys(value).sort().join(',')===names.slice().sort().join(',');
  const field=id=>result.columns.find(c=>c.id===id);
  const metric=id=>{const c=field(id);return c&&['decimal','integer'].includes(c.type)&&Object.hasOwn(result.totals,id);};
  const ready=()=>{if(!ports.active()||!result||!query||typeof query.integration_id!=='string')throw Error('请先在工作台完成当前授权查询，并等待操作结束。');};
  function validate(value) {
    if(!keys(value,['schema_version','kind','binding','widgets'])||value.schema_version!=='1.0'||value.kind!=='dashboard'||!keys(value.binding,['integration_id','dataset'])||!keys(value.binding.dataset,['id','version'])||!matches(value.binding)||!Array.isArray(value.widgets)||value.widgets.length>12)throw Error('看板配置或数据集绑定无效。');
    for(const w of value.widgets){
      if(w?.type==='metric'){if(!keys(w,['type','field'])||typeof w.field!=='string'||!metric(w.field))throw Error('指标不在当前授权结果的服务端合计中。');}
      else if(w?.type==='table'){if(!keys(w,['type','columns'])||!Array.isArray(w.columns)||!w.columns.length||w.columns.length>100||new Set(w.columns).size!==w.columns.length||!w.columns.every(id=>typeof id==='string'&&field(id)))throw Error('表格包含未知或重复字段。');}
      else throw Error('不支持此看板组件。');
    }
    if(new TextEncoder().encode(JSON.stringify(value)).length>65536)throw Error('配置超过 64 KiB 限额。');
  }
  function canonical(value){return {schema_version:value.schema_version,kind:value.kind,binding:{integration_id:value.binding.integration_id,dataset:{id:value.binding.dataset.id,version:value.binding.dataset.version}},widgets:value.widgets.map(w=>w.type==='metric'?{type:'metric',field:w.field}:{type:'table',columns:w.columns.slice()})};}
  function action(fn){try{ready();fn();}catch(e){$('board-status').textContent=e.message;}}
  function apply(value){validate(value);config=canonical(value);render();$('board-status').textContent='看板已更新；数据仍来自当前授权查询。';}
  function render(){
    const target=$('board-widgets');target.replaceChildren();
    if(!result||!config)return;
    config.widgets.forEach((widget,index)=>{
      const card=node('article');card.className='panel board-widget';const controls=node('div');controls.className='board-controls';
      for(const [label,delta] of [['上移',-1],['下移',1],['移除',0]]){const b=node('button',label);b.type='button';b.className='button quiet small';b.disabled=delta!==0&&(index+delta<0||index+delta>=config.widgets.length);b.addEventListener('click',()=>action(()=>{const next=JSON.parse(JSON.stringify(config));if(delta===0)next.widgets.splice(index,1);else [next.widgets[index],next.widgets[index+delta]]=[next.widgets[index+delta],next.widgets[index]];apply(next);}));controls.append(b);}
      card.append(controls);
      if(widget.type==='metric'){const c=field(widget.field);card.append(node('h3',c.label),node('p','服务端原范围合计'+(c.currency?' · '+c.currency:c.unit?' · '+c.unit:'')));const value=node('strong',result.totals[widget.field]);value.className='board-value';card.append(value);}
      else {card.append(node('h3','明细表 · 当前结果页'));const wrap=node('div');wrap.className='table-wrap';const table=node('table'),head=node('thead'),tr=node('tr'),body=node('tbody');for(const id of widget.columns){const c=field(id);tr.append(node('th',c.label+(c.currency?' ('+c.currency+')':c.unit?' ('+c.unit+')':'')));}head.append(tr);for(const values of result.rows){const row=node('tr');for(const id of widget.columns){const cell=node('td',values[result.columns.findIndex(c=>c.id===id)]);cell.className='precise';row.append(cell);}body.append(row);}table.append(head,body);wrap.append(table);card.append(wrap);}
      target.append(card);
    });
    $('board-count').textContent=config.widgets.length+' / 12 个组件';
  }
  function add(widget){const next=JSON.parse(JSON.stringify(config));next.widgets.push(widget);apply(next);}
  $('board-add-metric').addEventListener('click',()=>action(()=>add({type:'metric',field:$('board-metric').value})));
  $('board-add-table').addEventListener('click',()=>action(()=>add({type:'table',columns:Array.from($('board-columns').selectedOptions).map(o=>o.value)})));
  $('board-reset').addEventListener('click',()=>action(()=>apply({schema_version:'1.0',kind:'dashboard',binding:binding(),widgets:[]})));
  $('board-open').addEventListener('click',()=>action(()=>{const text=$('board-json').value;if(new TextEncoder().encode(text).length>65536)throw Error('配置超过 64 KiB 限额。');let value;try{value=JSON.parse(text);}catch(_){throw Error('请输入有效的看板 JSON。');}apply(value);}));
  $('board-save').addEventListener('click',()=>action(()=>{validate(config);const text=JSON.stringify(config,null,2);if(new TextEncoder().encode(text).length>65536)throw Error('配置超过 64 KiB 限额。');$('board-json').value=text;const url=URL.createObjectURL(new Blob([text],{type:'application/json'}));const link=node('a');link.href=url;link.download='dashboard-layout.json';document.body.append(link);link.click();link.remove();setTimeout(()=>URL.revokeObjectURL(url),1000);$('board-status').textContent='已生成元数据配置文件，不含业务数据或访问权限。';}));
  const validID=id=>typeof id==='string'&&/^[a-f0-9]{32}$/.test(id);
  function entry(value,id,version){if(!value||!validID(value.id)||!Number.isSafeInteger(value.version)||value.version<1||value.version>20||(id&&value.id!==id)||(version&&value.version!==version))throw Error('无法验证服务器看板版本。');validate(value.config);return value;}
  function saveServer(fresh){action(()=>{validate(config);const prior=!fresh&&saved&&matches(saved.config.binding)?saved:null;const document=JSON.parse(JSON.stringify(config));ports.exchange('/dashboards',{method:'POST',body:{id:prior?.id||'',expected_version:prior?.version||0,config:document}},value=>{entry(value,prior?.id,(prior?.version||0)+1);if(JSON.stringify(canonical(value.config))!==JSON.stringify(canonical(document)))throw Error('服务器返回的配置与保存内容不一致。');saved=value;$('board-server-status').textContent='已保存看板 '+value.id+' · 版本 '+value.version;},'看板保存');});}
  $('board-server-save').addEventListener('click',()=>saveServer(false));$('board-server-new').addEventListener('click',()=>saveServer(true));
  $('board-server-list').addEventListener('click',()=>action(()=>ports.exchange('/dashboards',undefined,values=>{if(!Array.isArray(values)||values.length>16||!values.every(v=>validID(v.id)&&Number.isSafeInteger(v.version)&&v.version>=1&&v.version<=20))throw Error('无法验证服务器看板列表。');$('board-server-listing').replaceChildren();for(const value of values){const option=node('option',value.id+' · 最新版本 '+value.version);option.value=value.id;option.dataset.version=String(value.version);$('board-server-listing').append(option);}$('board-server-version').value=String(values[0]?.version||1);$('board-server-status').textContent='当前身份可访问 '+values.length+' 个服务器看板。';},'看板目录')));
  $('board-server-listing').addEventListener('change',()=>{$('board-server-version').value=$('board-server-listing').selectedOptions[0]?.dataset.version||'1';});
  $('board-server-open').addEventListener('click',()=>action(()=>{const id=$('board-server-listing').value,version=Number($('board-server-version').value);if(!validID(id)||!Number.isSafeInteger(version)||version<1||version>20)throw Error('请选择有效看板和版本。');ports.exchange('/dashboards/'+id+'?version='+version,undefined,value=>{entry(value,id,version);apply(value.config);saved=value;$('board-server-status').textContent='正在编辑看板 '+id+' · 版本 '+version+'；旧版本保存可能冲突。';},'看板打开');}));
  return {
    clear(logout=false){result=null;query=null;if(logout){config=null;saved=null;}$('board-server-listing').replaceChildren();$('board-server-version').value='1';$('board-server-status').textContent='查询当前授权结果后可使用服务器看板。';$('board-widgets').replaceChildren();$('board-metric').replaceChildren();$('board-columns').replaceChildren();$('board-json').value='';$('board-count').textContent='0 / 12 个组件';$('board-source').textContent='尚无当前授权结果';$('board-status').textContent='请先在工作台查询或打开快照。';},
    refresh(value,q,snapshot){
      result=value;query=q;if(saved&&!matches(saved.config.binding))saved=null;
      try{if(!config)throw Error();validate(config);}catch(_){config={schema_version:'1.0',kind:'dashboard',binding:binding(),widgets:[]};}
      $('board-metric').replaceChildren();$('board-columns').replaceChildren();for(const c of result.columns){if(metric(c.id)){const o=node('option',c.label);o.value=c.id;$('board-metric').append(o);}const o=node('option',c.label);o.value=c.id;o.selected=true;$('board-columns').append(o);}
      $('board-source').textContent=query.dataset.id+' @ '+query.dataset.version+' · '+query.as_of+' · '+(snapshot?'冻结快照当前页':'当前查询预览');$('board-status').textContent='指标使用服务端原范围合计；表格仅显示当前结果页。';$('board-server-status').textContent=saved?'正在编辑看板 '+saved.id+' · 版本 '+saved.version:'未关联服务器看板；首次保存创建新看板。';render();
    }
  };
};
