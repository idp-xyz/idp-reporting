'use strict';
// Dashboard consumes the currently authorized result; its files contain metadata only.
window.createReportingDashboard = function (ports) {
  const $=id=>document.getElementById(id);
  const node=(tag,text)=>{const n=document.createElement(tag);if(text!==undefined)n.textContent=text;return n;};
  let result=null,query=null,config=null;
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
  function action(fn){try{ready();fn();}catch(e){$('board-status').textContent=e.message;}}
  function apply(value){validate(value);config=JSON.parse(JSON.stringify(value));render();$('board-status').textContent='看板已更新；数据仍来自当前授权查询。';}
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
  return {
    clear(logout=false){result=null;query=null;if(logout)config=null;$('board-widgets').replaceChildren();$('board-metric').replaceChildren();$('board-columns').replaceChildren();$('board-json').value='';$('board-count').textContent='0 / 12 个组件';$('board-source').textContent='尚无当前授权结果';$('board-status').textContent='请先在工作台查询或打开快照。';},
    refresh(value,q,snapshot){
      result=value;query=q;
      try{if(!config)throw Error();validate(config);}catch(_){config={schema_version:'1.0',kind:'dashboard',binding:binding(),widgets:[]};}
      $('board-metric').replaceChildren();$('board-columns').replaceChildren();for(const c of result.columns){if(metric(c.id)){const o=node('option',c.label);o.value=c.id;$('board-metric').append(o);}const o=node('option',c.label);o.value=c.id;o.selected=true;$('board-columns').append(o);}
      $('board-source').textContent=query.dataset.id+' @ '+query.dataset.version+' · '+query.as_of+' · '+(snapshot?'冻结快照当前页':'当前查询预览');$('board-status').textContent='指标使用服务端原范围合计；表格仅显示当前结果页。';render();
    }
  };
};
