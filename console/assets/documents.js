'use strict';
// What a layout or binding refusal says (S03-J): a reason and, where they apply, the band, the element, the snapshot row and the
// column's field id: names and indices, never values. The server adds it as `detail` to a problem answer and to a check's rows.
window.reportingRefusal=(()=>{
 const reasons={DOES_NOT_FIT:'放不下：值比所在的框、列或页更宽或更高',NOT_COVERED:'无法按原样打印：含字体不支持的字符、控制字符或预览横幅文字',INCONSISTENT_GROUP:'同一分组内该字段的取值不一致',TOO_MANY_PAGES:'超过 100 页',BAND_OVERFLOW:'带放不下：超出页面留给它的高度'};
 const bandLabels={page_header:'页眉带',document_header:'单据头带',detail:'明细带',document_footer:'单据尾带',page_footer:'页脚带'};
 const valid=v=>!!v&&typeof v==='object'&&!Array.isArray(v)&&Object.keys(v).every(k=>['reason','band','element','row','column'].includes(k))&&Object.hasOwn(reasons,v.reason)&&(v.band===undefined||Object.hasOwn(bandLabels,v.band))&&(v.element===undefined||(typeof v.element==='string'&&/^(?:n(?:[1-9]|1[0-9]|2[0-4])|e(?:[1-9]|[1-5][0-9]|6[0-4]))$/.test(v.element)))&&(v.row===undefined||(Number.isSafeInteger(v.row)&&v.row>=0&&v.row<1000000))&&(v.column===undefined||(typeof v.column==='string'&&v.column.length>=1&&v.column.length<=128&&!/[\x00-\x1f\x7f]/.test(v.column)));
 // labels: the field ids of the current result and their labels, to name a column.
 const text=(v,labels)=>[reasons[v.reason],v.band?bandLabels[v.band]:'',v.element?'元素 '+v.element:'',v.row!==undefined?'快照第 '+(v.row+1)+' 行':'',v.column?'列 '+(labels&&Object.hasOwn(labels,v.column)?labels[v.column]+'（'+v.column+'）':v.column):''].filter(Boolean).join(' · ');
 return {valid,text,reasons,bandLabels};
})();
// The group a schema 2.0 issue names in place of a row (S03-K): the template's group fields, the key values in their order,
// how many rows the group has and the digest of those rows. The key values are the caller's own data, shown as text.
window.reportingIssueGroup=(()=>{
 const valid=g=>!!g&&typeof g==='object'&&!Array.isArray(g)&&Object.keys(g).sort().join()==='fields,key,row_count,rows_sha256'&&Array.isArray(g.fields)&&g.fields.length<=3&&g.fields.every((f,i)=>typeof f==='string'&&f.length>=1&&f.length<=128&&g.fields.indexOf(f)===i)&&Array.isArray(g.key)&&g.key.length===g.fields.length&&g.key.every(v=>typeof v==='string'&&v.length<=4096)&&Number.isSafeInteger(g.row_count)&&g.row_count>=1&&g.row_count<=1000&&typeof g.rows_sha256==='string'&&/^[a-f0-9]{64}$/.test(g.rows_sha256);
 const clip=v=>{const c=Array.from(v);return c.length>40?c.slice(0,40).join('')+'…':v;};
 // Without fields the group is the whole snapshot.
 const text=g=>(g.fields.length?g.fields.map((f,i)=>f+' = '+clip(g.key[i])).join('、'):'整个快照')+'（'+g.row_count+' 行）';
 return {valid,text};
})();
window.createReportingDocuments=function(ports){
 const $=id=>document.getElementById(id),node=(tag,text)=>{const e=document.createElement(tag);if(text!==undefined)e.textContent=text;return e;};
 let result=null,query=null,config=null,saved=null,release=null,backgrounds=[],publishedOpen=false,past=[],future=[],scope='',checks=null;
 const HISTORY=200;
 const keys=(v,k)=>v&&typeof v==='object'&&!Array.isArray(v)&&Object.keys(v).sort().join(',')===k.slice().sort().join(',');
 const binding=()=>({integration_id:query.integration_id,dataset:{id:query.dataset.id,version:query.dataset.version}});
 function validate(d){
  if(d?.schema_version==='2.0')return validate2(d);
  if(Array.isArray(d?.elements)&&d.elements.length<1)throw Error('至少保留一个文本框。');
  if(Array.isArray(d?.elements)&&d.elements.length>24)throw Error('最多24个文本框。');
  const expected=d?.schema_version==='1.3'?(d.background===undefined?['schema_version','kind','binding','elements','print','table']:['schema_version','kind','binding','elements','print','background','table']):d?.schema_version==='1.2'?['schema_version','kind','binding','elements','print','background']:d?.schema_version==='1.1'?['schema_version','kind','binding','elements','print']:['schema_version','kind','binding','elements'];
  if(!keys(d,expected)||!['1.0','1.1','1.2','1.3'].includes(d.schema_version)||d.kind!=='document'||!keys(d.binding,['integration_id','dataset'])||!keys(d.binding.dataset,['id','version'])||d.binding.integration_id!==query.integration_id||d.binding.dataset.id!==query.dataset.id||d.binding.dataset.version!==query.dataset.version||!Array.isArray(d.elements)||d.elements.length<1||d.elements.length>24||new TextEncoder().encode(JSON.stringify(d)).length>65536)throw Error('模板或当前数据集绑定无效。');
  const grid=n=>typeof n==='number'&&Number.isFinite(n)&&Math.abs(n*10-Math.round(n*10))<1e-7,units=n=>Math.round(n*10);
  if(d.schema_version!=='1.0'&&(!keys(d.print,['mode','offset_x','offset_y'])||!['full','variables'].includes(d.print.mode)||!grid(d.print.offset_x)||!grid(d.print.offset_y)||Math.abs(d.print.offset_x)>10||Math.abs(d.print.offset_y)>10))throw Error('套打模式或偏移无效；偏移限正负10毫米、步长0.1毫米。');
  if((d.schema_version==='1.2'||(d.schema_version==='1.3'&&d.background!==undefined))&&!validBackground(d.background))throw Error('底稿引用无效。');
  if(d.schema_version==='1.3')validateTable(d);
  const ids=new Set();for(const [i,e] of d.elements.entries()){
   if(!keys(e,['id','source','field','text','x','y','width','height','font_size','align'])||typeof e.id!=='string'||!/^n(?:[1-9]|1[0-9]|2[0-4])$/.test(e.id)||ids.has(e.id)||!Number.isSafeInteger(e.font_size)||!['x','y','width','height'].every(k=>grid(e[k])&&(d.schema_version!=='1.0'||Number.isSafeInteger(e[k])))||e.x<10||e.y<25||e.width<10||e.height<5||units(e.x)+units(e.width)>2000||units(e.y)+units(e.height)>2700||e.font_size<8||e.font_size>28||units(e.height)*72<e.font_size*400||!['left','center','right'].includes(e.align))throw Error('文本框超出A4安全区、重复或尺寸无效。');ids.add(e.id);
   if(e.source==='literal'){if(e.field!==''||typeof e.text!=='string'||[...e.text].length>256||/[\x00-\x1f]/.test(e.text))throw Error('固定文字需要单行，最多256字符。');}
   else if(['row','total'].includes(e.source)){if(e.text!==''||typeof e.field!=='string'||!result.columns.some(c=>c.id===e.field)||(e.source==='total'&&!Object.hasOwn(result.totals,e.field)))throw Error('绑定字段不在当前授权结果中。');}
   else throw Error('不支持的文本来源。');
   if(d.print&&(d.print.mode==='full'||e.source!=='literal')){const x=units(e.x)+units(d.print.offset_x),y=units(e.y)+units(d.print.offset_y);if(x<100||y<250||x+units(e.width)>2000||y+units(e.height)>2700)throw Error('偏移后的打印文本框超出安全区。');}
   for(const p of d.elements.slice(0,i))if(units(e.x)<units(p.x)+units(p.width)&&units(p.x)<units(e.x)+units(e.width)&&units(e.y)<units(p.y)+units(p.height)&&units(p.y)<units(e.y)+units(e.height))throw Error('文本框不能重叠。');
  }
  if(d.print?.mode==='variables'&&!d.elements.some(e=>e.source!=='literal')&&!d.table)throw Error('变量层至少需要一个行字段、合计字段或明细表。');
 }
 const labelMinimum={8:28.2,9:31.4,10:34.7,11:38,12:41.2};
 function validateTable(d){
  const t=d.table,grid=n=>typeof n==='number'&&Number.isFinite(n)&&Math.abs(n*10-Math.round(n*10))<1e-7,units=n=>Math.round(n*10);
  if(!keys(t,['x','y','width','height','font_size','columns'])||!['x','y','width','height'].every(k=>grid(t[k]))||t.x<10||t.y<25||t.width<8||t.height<30||units(t.x)+units(t.width)>2000||units(t.y)+units(t.height)>2700||!Number.isSafeInteger(t.font_size)||t.font_size<8||t.font_size>12||!Array.isArray(t.columns)||t.columns.length<1||t.columns.length>12)throw Error('明细表区域、字号或列数无效（区域在安全区内、至少30毫米高、8–12点、1–12列）。');
  const x=units(t.x)+units(d.print.offset_x),y=units(t.y)+units(d.print.offset_y);if(x<100||y<250||x+units(t.width)>2000||y+units(t.height)>2700)throw Error('偏移后的明细表超出安全区。');
  const fields=new Set();let total=0;for(const c of t.columns){if(!keys(c,['field','width'])||typeof c.field!=='string'||!result.columns.some(v=>v.id===c.field)||fields.has(c.field)||!grid(c.width)||c.width<8)throw Error('明细表列必须是当前授权结果的不同字段，每列至少8毫米。');fields.add(c.field);total+=units(c.width);}
  if(total!==units(t.width))throw Error('明细表各列宽度之和必须等于表宽。');
  if(units(t.columns[0].width)<units(labelMinimum[t.font_size]))throw Error('第一列须容纳承前/过次标签：'+t.font_size+' 点字号至少 '+labelMinimum[t.font_size]+' 毫米。');
  for(const e of d.elements)if(units(t.x)<units(e.x)+units(e.width)&&units(e.x)<units(t.x)+units(t.width)&&units(t.y)<units(e.y)+units(e.height)&&units(e.y)<units(t.y)+units(t.height))throw Error('明细表不能与文本框重叠。');
 }

 function canonical(d){if(d.schema_version==='2.0')return canonical2(d);const value={schema_version:d.schema_version,kind:d.kind,binding:{integration_id:d.binding.integration_id,dataset:{id:d.binding.dataset.id,version:d.binding.dataset.version}},elements:d.elements.map(e=>({id:e.id,source:e.source,field:e.field,text:e.text,x:e.x,y:e.y,width:e.width,height:e.height,font_size:e.font_size,align:e.align}))};if(d.schema_version!=='1.0')value.print={mode:d.print.mode,offset_x:d.print.offset_x,offset_y:d.print.offset_y};if(d.schema_version==='1.2'||(d.schema_version==='1.3'&&d.background))value.background={id:d.background.id,sha256:d.background.sha256};if(d.schema_version==='1.3')value.table={x:d.table.x,y:d.table.y,width:d.table.width,height:d.table.height,font_size:d.table.font_size,columns:d.table.columns.map(c=>({field:c.field,width:c.width}))};return value;}
 const bandLabels=window.reportingRefusal.bandLabels;
 // ---- schema 2.0 (S03-J): bands over row groups. The same closed form as the server's validator (doctemplates.validateBanded) and the same shared cases. ----
 const bandNames=['page_header','document_header','detail','document_footer','page_footer'];
 const fixedBands=['page_header','document_header','document_footer','page_footer'];
 const fillNames=['grey1','grey2','grey3','grey4'];
 const itemKeys={text:['id','type','source','text','field','wrap','x','y','width','height','font_size','align','bold'],line:['id','type','x1','y1','x2','y2','stroke'],rect:['id','type','x','y','width','height','stroke','fill'],table:['id','type','x','width','font_size','columns']};
 const inRange=(n,lo,hi)=>typeof n==='number'&&n>=lo&&n<=hi,onGrid=n=>typeof n==='number'&&Number.isFinite(n)&&Math.abs(n*10-Math.round(n*10))<1e-7,tenths=n=>Math.round(n*10);
 const quarter=(n,lo,hi)=>inRange(n,lo,hi)&&Math.abs(n*4-Math.round(n*4))<1e-9;
 const runes=s=>[...s].length;
 // One line takes no control; a wrapped text may hold line breaks. Limits: 256 characters, wrapped 1024, a column label 64.
 const printable=(s,wrap,limit)=>typeof s==='string'&&runes(s)<=limit&&!(wrap?/[\x00-\x09\x0b-\x1f\x7f]/:/[\x00-\x1f\x7f]/).test(s);
 const placeholders=s=>{const found=s.match(/\{pages\}|\{page\}/g)||[];return found.length>=1&&!/[{}]/.test(s.replace(/\{pages\}|\{page\}/g,''));};
 const minDetail=size=>Math.floor(((400+146*size)*4*254+7199)/7200);
 function validate2(d){
  const fail=m=>{throw Error(m);};
  if(!keys(d,d.group===undefined?['schema_version','kind','binding','bands']:['schema_version','kind','binding','group','bands'])||d.kind!=='document'||!keys(d.binding,['integration_id','dataset'])||!keys(d.binding.dataset,['id','version'])||d.binding.integration_id!==query.integration_id||d.binding.dataset.id!==query.dataset.id||d.binding.dataset.version!==query.dataset.version||new TextEncoder().encode(JSON.stringify(d)).length>65536)fail('模板或当前数据集绑定无效，或超过64KiB。');
  const columns=new Set(result.columns.map(c=>c.id)),metrics=new Set((query.metrics||[]).map(m=>m.id)),dimensions=new Set(query.dimensions||[]);
  if(d.group!==undefined){
   const f=d.group?.fields;
   if(!keys(d.group,['fields'])||!Array.isArray(f)||f.length<1||f.length>3||!f.every(v=>typeof v==='string'&&v.length>=1&&v.length<=128)||new Set(f).size!==f.length)fail('分组字段需为1–3个不同的维度字段。');
   if(!f.every(v=>columns.has(v)))fail('分组字段不在当前授权结果中。');
   if(!f.every(v=>dimensions.has(v)))fail('分组字段必须是维度，不能是指标。');
  }
  const b=d.bands;
  if(!b||typeof b!=='object'||Array.isArray(b)||!Object.keys(b).every(k=>bandNames.includes(k))||b.detail===undefined)fail('带只能是页眉、单据头、明细、单据尾、页脚，且必须有明细带。');
  let stack=0,total=0;const ids=new Set();
  for(const name of fixedBands){
   const band=b[name];if(band===undefined)continue;
   if(!keys(band,['height','elements'])||!onGrid(band.height)||!inRange(band.height,5,245)||!Array.isArray(band.elements)||band.elements.length>24)fail(bandLabels[name]+'的高度须为5–245毫米（0.1毫米步长），至多24个元素。');
   if(name!=='document_footer')stack+=tenths(band.height);
   total+=band.elements.length;
   for(const [i,e] of band.elements.entries()){
    if(!e||typeof e!=='object'||Array.isArray(e)||typeof e.id!=='string'||!/^e(?:[1-9]|[1-5][0-9]|6[0-4])$/.test(e.id)||ids.has(e.id))fail('元素编号须为 e1–e64 且不重复。');
    ids.add(e.id);
    if(!Object.hasOwn(itemKeys,e.type)||e.type==='table')fail('元素类型只能是 text、line、rect；条码、二维码与图片尚未提供。');
    if(!keys(e,itemKeys[e.type]))fail(e.id+'：键缺失或多余。');
    if(!validItem2(e,tenths(band.height)))fail(e.id+'：超出带范围、尺寸、字号、来源或文字无效。');
    if(e.type==='text')for(const p of band.elements.slice(0,i))if(p.type==='text'&&tenths(e.x)<tenths(p.x)+tenths(p.width)&&tenths(p.x)<tenths(e.x)+tenths(e.width)&&tenths(e.y)<tenths(p.y)+tenths(p.height)&&tenths(p.y)<tenths(e.y)+tenths(e.height))fail('同一带内的文本框不能重叠。');
    if(e.type==='text'&&e.source==='field'&&!columns.has(e.field))fail(e.id+'：字段不在当前授权结果中。');
    if(e.type==='text'&&e.source==='group_sum'&&(!columns.has(e.field)||!metrics.has(e.field)))fail(e.id+'：分组合计需要当前结果中的指标字段。');
   }
  }
  const detail=b.detail;
  if(!keys(detail,['elements'])||!Array.isArray(detail.elements)||detail.elements.length!==1||detail.elements[0]?.type!=='table')fail('明细带必须恰有一个明细表，且不含其他元素。');
  const t=detail.elements[0];total+=1;
  if(total>64||!keys(t,itemKeys.table)||typeof t.id!=='string'||!/^e(?:[1-9]|[1-5][0-9]|6[0-4])$/.test(t.id)||ids.has(t.id)||!validTable2(t))fail('明细表或元素总数无效（至多64个元素；表宽、字号8–12点、1–12个不同的列，各列至少8毫米且之和等于表宽）。');
  if(!t.columns.every(c=>columns.has(c.field)))fail('明细表列必须是当前授权结果的字段。');
  if(stack+minDetail(t.font_size)>2450)fail('页眉、单据头与页脚的高度加上明细表的最小高度超出安全区。');
 }
 function validItem2(e,height){
  const H=height/10;
  if(e.type==='line')return[e.x1,e.y1,e.x2,e.y2].every(onGrid)&&inRange(e.x1,0,190)&&inRange(e.x2,0,190)&&inRange(e.y1,0,H)&&inRange(e.y2,0,H)&&(tenths(e.x1)!==tenths(e.x2)||tenths(e.y1)!==tenths(e.y2))&&quarter(e.stroke,0.25,2);
  if(e.type==='rect')return[e.x,e.y,e.width,e.height].every(onGrid)&&inRange(e.x,0,190)&&inRange(e.y,0,H)&&inRange(e.width,0.1,190)&&inRange(e.height,0.1,H)&&tenths(e.x)+tenths(e.width)<=1900&&tenths(e.y)+tenths(e.height)<=height&&quarter(e.stroke,0,2)&&(fillNames.includes(e.fill)||e.fill==='none')&&(e.stroke>0||fillNames.includes(e.fill));
  if(!['x','y','width','height'].every(k=>onGrid(e[k]))||!inRange(e.x,0,190)||!inRange(e.y,0,H)||!inRange(e.width,10,190)||!inRange(e.height,5,H)||tenths(e.x)+tenths(e.width)>1900||tenths(e.y)+tenths(e.height)>height||!Number.isSafeInteger(e.font_size)||e.font_size<8||e.font_size>28||tenths(e.height)*72<e.font_size*400||!['left','center','right'].includes(e.align)||typeof e.wrap!=='boolean'||typeof e.bold!=='boolean'||typeof e.text!=='string'||typeof e.field!=='string')return false;
  if(e.source==='literal')return e.field===''&&printable(e.text,e.wrap,e.wrap?1024:256);
  if(e.source==='page')return e.field===''&&!e.wrap&&placeholders(e.text)&&printable(e.text,false,256);
  if(e.source==='field'||e.source==='group_sum')return e.text===''&&e.field.length>=1&&e.field.length<=128;
  return false;
 }
 function validTable2(t){
  if(!onGrid(t.x)||!onGrid(t.width)||!inRange(t.x,0,190)||!inRange(t.width,8,190)||tenths(t.x)+tenths(t.width)>1900||!Number.isSafeInteger(t.font_size)||t.font_size<8||t.font_size>12||!Array.isArray(t.columns)||t.columns.length<1||t.columns.length>12)return false;
  const seen=new Set();let sum=0;
  for(const c of t.columns){
   if(!c||typeof c!=='object'||Array.isArray(c)||!Object.keys(c).every(k=>['field','width','align','label'].includes(k))||typeof c.field!=='string'||typeof c.width!=='number'||c.field.length<1||c.field.length>128||seen.has(c.field)||!onGrid(c.width)||!inRange(c.width,8,190))return false;
   if(c.align!==undefined&&!['left','center','right'].includes(c.align))return false;
   if(c.label!==undefined&&(c.label===''||!printable(c.label,false,64)))return false;
   seen.add(c.field);sum+=tenths(c.width);
  }
  return sum===tenths(t.width);
 }
 // The canonical form is the server's: the keys of each type in the server's order, an optional key only when set.
 function canonical2(d){
  const item=e=>e.type==='text'?{id:e.id,type:e.type,source:e.source,text:e.text,field:e.field,wrap:e.wrap,x:e.x,y:e.y,width:e.width,height:e.height,font_size:e.font_size,align:e.align,bold:e.bold}:e.type==='line'?{id:e.id,type:e.type,x1:e.x1,y1:e.y1,x2:e.x2,y2:e.y2,stroke:e.stroke}:e.type==='rect'?{id:e.id,type:e.type,x:e.x,y:e.y,width:e.width,height:e.height,stroke:e.stroke,fill:e.fill}:{id:e.id,type:e.type,x:e.x,width:e.width,font_size:e.font_size,columns:e.columns.map(c=>{const o={field:c.field,width:c.width};if(c.align!==undefined)o.align=c.align;if(c.label!==undefined)o.label=c.label;return o;})};
  const bands={};
  for(const name of bandNames){const band=d.bands[name];if(band===undefined)continue;bands[name]=name==='detail'?{elements:band.elements.map(item)}:{height:band.height,elements:band.elements.map(item)};}
  const value={schema_version:d.schema_version,kind:d.kind,binding:{integration_id:d.binding.integration_id,dataset:{id:d.binding.dataset.id,version:d.binding.dataset.version}}};
  if(d.group!==undefined)value.group={fields:[...d.group.fields]};
  value.bands=bands;return value;
 }
 // What the designer shows of a 2.0 template: its bands and elements, read-only; it is edited as JSON.
 function describe2(d){
  const out=[];
  for(const name of bandNames){const band=d.bands[name];if(band===undefined)continue;
   out.push((name==='detail'?bandLabels[name]:bandLabels[name]+' '+band.height+' 毫米')+'：'+band.elements.map(e=>e.id+' '+(e.type==='text'?'文本（'+({literal:'固定文字',field:'字段 '+e.field,group_sum:'分组合计 '+e.field,page:'页码'}[e.source])+(e.wrap?'，自动换行':'')+'）':e.type==='line'?'线':e.type==='rect'?'矩形':'明细表 '+e.columns.length+' 列')).join('；'));}
  return out;
 }

 const sameBinding=d=>d?.binding?.integration_id===query?.integration_id&&d.binding.dataset.id===query.dataset.id&&d.binding.dataset.version===query.dataset.version;
 const validBackground=b=>keys(b,['id','sha256'])&&typeof b.id==='string'&&/^[a-f0-9]{32}$/.test(b.id)&&typeof b.sha256==='string'&&/^[a-f0-9]{64}$/.test(b.sha256);
 const validID=id=>typeof id==='string'&&/^[a-f0-9]{32}$/.test(id);
 function resetRelease(){release=null;$('doc-release-status').textContent='请先读取发布状态。';$('doc-release-history').replaceChildren();}
 function serverEntry(v,id,version){if(!v||!validID(v.id)||!Number.isSafeInteger(v.version)||v.version<1||v.version>20||(id&&v.id!==id)||(version&&v.version!==version))throw Error('无法验证服务器模板版本。');validate(v.config);return v;}
 function savedID(){if(!saved||!sameBinding(saved.config))throw Error('请先保存或打开当前数据集的服务器模板。');return saved.id;}
 function cleanSaved(){const id=savedID();if(JSON.stringify(canonical(config))!==JSON.stringify(canonical(saved.config)))throw Error('请先保存当前修改，再发布或按保存版本预览。');return id;}
 function showRelease(v){
  if(!keys(v,['revision','published_version','history'])||!Number.isSafeInteger(v.revision)||v.revision<0||v.revision>64||!Number.isSafeInteger(v.published_version)||v.published_version<0||v.published_version>20||!Array.isArray(v.history)||v.history.length!==v.revision)throw Error('无法验证模板发布状态。');
  let published=0;for(const [i,e] of v.history.entries()){if(!keys(e,['revision','action','version','at'])||e.revision!==i+1||!['publish','withdraw'].includes(e.action)||!Number.isSafeInteger(e.version)||(e.action==='publish'?(e.version<1||e.version>20):e.version!==0)||typeof e.at!=='string'||!Number.isFinite(Date.parse(e.at)))throw Error('无法验证发布历史。');published=e.action==='publish'?e.version:0;}
  if(published!==v.published_version)throw Error('发布历史与指针不一致。');release=v;$('doc-release-status').textContent='发布修订 '+v.revision+' · '+(v.published_version?'已发布版本 '+v.published_version:'当前未发布');$('doc-release-history').replaceChildren();for(const e of v.history)$('doc-release-history').append(node('li','修订 '+e.revision+' · '+(e.action==='publish'?'发布版本 '+e.version:'撤回')+' · '+e.at));
 }
 function saveServer(fresh){action(()=>{validate(config);const prior=!fresh&&saved&&sameBinding(saved.config)?saved:null,document=canonical(config);resetRelease();ports.exchange('/document-templates',{method:'POST',body:{id:prior?.id||'',expected_version:prior?.version||0,config:document}},v=>{serverEntry(v,prior?.id,(prior?.version||0)+1);if(JSON.stringify(canonical(v.config))!==JSON.stringify(document))throw Error('服务器保存内容不一致。');saved=v;publishedOpen=false;resetRelease();$('doc-server-status').textContent='已保存模板 '+v.id+' · 版本 '+v.version;},'模板保存');});}
 $('doc-server-save').addEventListener('click',()=>saveServer(false));$('doc-server-new').addEventListener('click',()=>saveServer(true));
 $('doc-server-list').addEventListener('click',()=>action(()=>ports.exchange('/document-templates',undefined,values=>{if(!Array.isArray(values)||values.length>16||!values.every(v=>validID(v.id)&&Number.isSafeInteger(v.version)&&v.version>=1&&v.version<=20))throw Error('无法验证模板列表。');$('doc-server-listing').replaceChildren();for(const v of values){const o=node('option',v.id+' · 最新版本 '+v.version);o.value=v.id;o.dataset.version=String(v.version);$('doc-server-listing').append(o);}$('doc-server-version').value=String(values[0]?.version||1);$('doc-server-status').textContent='当前身份可访问 '+values.length+' 个模板。';},'模板列表')));
 $('doc-server-listing').addEventListener('change',()=>{$('doc-server-version').value=$('doc-server-listing').selectedOptions[0]?.dataset.version||'1';});
 $('doc-server-open').addEventListener('click',()=>action(()=>{const id=$('doc-server-listing').value,version=Number($('doc-server-version').value);if(!validID(id)||!Number.isSafeInteger(version)||version<1||version>20)throw Error('请选择有效模板版本。');resetRelease();ports.exchange('/document-templates/'+id+'?version='+version,undefined,v=>{serverEntry(v,id,version);replaceTemplate(v.config);saved=v;resetRelease();$('doc-server-status').textContent='正在编辑模板 '+id+' · 版本 '+version;},'模板打开');}));
 $('doc-release-read').addEventListener('click',()=>action(()=>{const id=savedID();resetRelease();ports.exchange('/document-templates/'+id+'/release',undefined,showRelease,'模板发布状态');}));
 function changeRelease(name){action(()=>{const id=name==='publish'?cleanSaved():savedID();if(!release)throw Error('请先刷新发布状态，冲突或结果未确认时必须重新读取。');if(name==='withdraw'&&!release.published_version)throw Error('当前没有发布版本。');const expected=release.revision,version=name==='publish'?saved.version:0;resetRelease();ports.exchange('/document-templates/'+id+'/release',{method:'POST',body:{action:name,version,expected_revision:expected}},v=>{if(v.revision!==expected+1||v.published_version!==version)throw Error('意外发布修订，请刷新核对。');showRelease(v);},'模板发布变更');});}
 $('doc-release-publish').addEventListener('click',()=>changeRelease('publish'));$('doc-release-withdraw').addEventListener('click',()=>changeRelease('withdraw'));
 $('doc-published-open').addEventListener('click',()=>action(()=>{const id=savedID();resetRelease();ports.exchange('/document-templates/'+id+'/published',undefined,v=>{serverEntry(v,id);replaceTemplate(v.config);saved=v;publishedOpen=true;resetRelease();$('doc-server-status').textContent='已打开发布模板 '+id+' · 版本 '+v.version;},'发布模板打开');}));
 // The number is the source product's (1..32 of A-Za-z0-9._-, starting with a letter or digit): a string, present only on a succeeded record.
 const validNumber=n=>typeof n==='string'&&/^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$/.test(n);
 function issuance(){previews();if(!ports.issuance())throw Error('服务端未启用正式出具。');}
 $('doc-issue').addEventListener('click',()=>action(()=>{issuance();const id=cleanSaved();if(!publishedOpen)throw Error('正式出具只接受当前发布版本：请先“打开已发布模板”，不改动后再出具。');const row=Number($('doc-row').value);if(!ports.snapshot()||!Number.isSafeInteger(row)||row<1||row>1000)throw Error('请先打开冻结快照并填写有效行号。');
  // A 2.0 template is issued for the row group that holds the row (S03-K): the server finds the group by the row and freezes it.
  const banded=config?.schema_version==='2.0';
  $('doc-issue-status').textContent='正在提交正式出具：模板 '+id+' · 版本 '+saved.version+(banded?' · 第 '+row+' 行所在分组（由服务端按模板的分组字段在冻结快照中确定）。':' · 第 '+row+' 行。');
  ports.issue({template_id:id,version:saved.version,snapshot_id:ports.snapshot(),row_index:row-1},banded);}));
 const issueStates={queued:'排队中',running:'执行中',succeeded:'已出具',failed:'失败',cancelled:'已取消'};
 // A failed record shows its code, and for these two what it means; other codes stand as they are.
 const issueFailures={AUTH_SCOPE_DENIED:'来源产品拒绝出具',ISSUE_UNRECONCILED:'未能通知来源产品作废预留，请联系管理员'};
 function showIssues(values){
  try{checkIssues(values);}catch(e){$('doc-issues-body').replaceChildren();$('doc-issues-wrap').hidden=true;$('doc-issue-status').textContent=e.message;return;}
  const body=$('doc-issues-body');body.replaceChildren();
  for(const v of values){
   const state=issueStates[v.state]+(v.error_code?' · '+v.error_code+(Object.hasOwn(issueFailures,v.error_code)?'（'+issueFailures[v.error_code]+'）':''):'');
   const row=node('tr');
   // Number, state, issue time, template, snapshot and row, file digest and pages, the product's confirmation, then the action.
   for(const text of [v.state==='succeeded'?'No. '+v.number:'—',state,v.issued_at||'—',v.template_id+' · 版本 '+v.template_version,v.snapshot_id+' · '+(v.group?'分组 '+window.reportingIssueGroup.text(v.group):'第 '+(v.row_index+1)+' 行'),v.sha256?v.sha256+' · '+v.pages+' 页':'—',v.confirmation_ref?v.confirmation_ref+' · '+v.confirmed_at:'—'])row.append(node('td',text));
   const actions=node('td');if(v.state==='succeeded'){const b=node('button','下载');b.type='button';b.className='button secondary';b.addEventListener('click',()=>action(()=>ports.issued(v.artifact_id,v.number,v.sha256)));actions.append(b);}row.append(actions);body.append(row);
  }
  $('doc-issues-wrap').hidden=!values.length;$('doc-issue-status').textContent=values.length?'当前身份共 '+values.length+' 条出具记录（最新在前，最多显示 50 条）。':'当前身份尚无出具记录。';
 }
 const hex64=v=>typeof v==='string'&&/^[a-f0-9]{64}$/.test(v),reference=v=>typeof v==='string'&&v.length>=1&&v.length<=128;
 // A UTC time to the second as the server writes it (YYYY-MM-DDTHH:MM:SSZ) that is a real instant; the fixed width makes string order time order.
 const utc=v=>typeof v==='string'&&/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/.test(v)&&Number.isFinite(Date.parse(v))&&new Date(v).toISOString().slice(0,19)+'Z'===v;
 const confirmedFields=['number','artifact_id','snapshot_sha256','sha256','pages','stamp','issued_at','reservation_ref','confirmation_ref','confirmed_at'];
 function checkIssues(values){
  if(!Array.isArray(values)||values.length>100)throw Error('无法验证出具记录列表。');
  for(const v of values){
   if(!v||typeof v!=='object'||!validID(v.issue_id)||!Object.hasOwn(issueStates,v.state)||!validID(v.template_id)||!Number.isSafeInteger(v.template_version)||v.template_version<1||!hex64(v.template_sha256)||!validID(v.snapshot_id)||!(v.group===undefined?Number.isSafeInteger(v.row_index)&&v.row_index>=0:v.row_index===undefined&&window.reportingIssueGroup.valid(v.group))||(v.error_code!==undefined&&(v.state!=='failed'||typeof v.error_code!=='string'||!/^[A-Z][A-Z0-9_]{0,63}$/.test(v.error_code))))throw Error('无法验证出具记录。');
   // Until the source product has confirmed there is no number, no file and no confirmation.
   if(v.state!=='succeeded'){if(confirmedFields.some(k=>v[k]!==undefined))throw Error('未完成的出具不应带有编号、产物或确认信息。');continue;}
   if(!validNumber(v.number)||!validID(v.artifact_id)||!hex64(v.sha256)||!hex64(v.snapshot_sha256)||!Number.isSafeInteger(v.pages)||v.pages<1||v.pages>100||!utc(v.issued_at)||!reference(v.reservation_ref)||!reference(v.confirmation_ref)||!utc(v.confirmed_at))throw Error('无法验证出具记录。');
   if(v.confirmed_at<v.issued_at)throw Error('出具记录的确认时间早于出具时间。');
   if(v.stamp!=='ISSUED No. '+v.number+' - '+v.issued_at+' - '+v.issue_id)throw Error('出具记录与其印章不一致。');
  }
 }
 $('doc-issues-refresh').addEventListener('click',()=>action(()=>ports.exchange('/issues?limit=50',undefined,showIssues,'出具记录')));
 function previews(){if(!ports.previews())throw Error('服务端未启用 PDF 渲染，无法生成预览。');}
 $('doc-saved-pdf').addEventListener('click',()=>action(()=>{previews();const id=cleanSaved(),row=Number($('doc-row').value);if(!ports.snapshot()||!Number.isSafeInteger(row)||row<1||row>1000)throw Error('请先打开冻结快照并填写有效行号。');ports.download({version:saved.version,snapshot_id:ports.snapshot(),row_index:row-1},'/document-templates/'+id+'/previews',refusalFor());}));
 function action(fn){try{if(!ports.active()||!result)throw Error('请先查询当前授权数据并等待操作结束。');fn();}catch(e){$('doc-status').textContent=e.message;}}
 // Every applied change passes here. An edit is one undo step (an unchanged result is none, a refused one never gets here); opening or resetting a template replaces it and clears the history.
 function install(d,label){validate(d);const next=canonical(d);if(label===undefined){past=[];future=[];canvas.reset();}else if(config&&JSON.stringify(next)!==JSON.stringify(config)){past.push({config,label});if(past.length>HISTORY)past.shift();future=[];}config=next;publishedOpen=false;showChecks(null);render();$('doc-status').textContent='模板已更新；配置不含业务数据。';}
 const apply=(d,label)=>install(d,label||'修改'),replaceTemplate=d=>install(d);
 // Undo and redo never restore "opened published, unedited": publishedOpen stays false, so issuing needs the published version opened again.
 function stepBack(from,to,verb){const step=from.pop();try{validate(step.config);}catch(e){past=[];future=[];render();throw Error('历史记录与当前查询不符，已清空。');}to.push({config,label:step.label});config=step.config;publishedOpen=false;showChecks(null);render();$('doc-status').textContent=verb+'：'+step.label+'。';}
 function undo(){if(!past.length)throw Error('没有可撤销的修改。');stepBack(past,future,'已撤销');}
 function redo(){if(!future.length)throw Error('没有可重做的修改。');stepBack(future,past,'已重做');}
 $('doc-undo').addEventListener('click',()=>action(undo));$('doc-redo').addEventListener('click',()=>action(redo));
 // What the canvas calls: the same validation and canonical form as the inputs, answered instead of thrown.
 function edit(d,label){try{if(!ports.active()||!result)throw Error('请先查询当前授权数据并等待操作结束。');apply(d,label);return {ok:true};}catch(e){return {ok:false,message:e.message};}}
 function test(d){try{validate(d);return '';}catch(e){return e.message;}}
 function syncPrint(){$('doc-background-status').textContent=config?.background?'当前底稿 '+config.background.id+' · SHA256 '+config.background.sha256:'当前模板无底稿。';const p=config?.print||{mode:'full',offset_x:0,offset_y:0};$('doc-print-mode').value=p.mode;$('doc-offset-x').value=String(p.offset_x);$('doc-offset-y').value=String(p.offset_y);$('doc-print-status').textContent=config?.schema_version==='2.0'?'模板 2.0 · 分带版面（无套打层与底稿，预览仍含PREVIEW标记）':'模板 '+(config?.schema_version||'—')+' · '+(p.mode==='variables'?'变量层对位预览（仍含PREVIEW标记）':'完整文本预览')+' · 偏移 '+p.offset_x+' / '+p.offset_y+' mm';}
 const canvas=window.createReportingCanvas({columns:()=>result?result.columns:[],edit,test,columnMinimum:(font,i)=>i===0?Math.max(8,labelMinimum[font]||0):8,canUndo:()=>past.length>0,canRedo:()=>future.length>0});
 // The place a refused preview names (the template cannot change while a preview is being made: edits wait for the operation to end).
 const refusalFor=()=>refused;
 function refused(detail){if(!window.reportingRefusal.valid(detail)||!config)return;$('doc-status').textContent='预览指出的位置：'+window.reportingRefusal.text(detail,labelsOf())+'。';if(config.schema_version!=='2.0')canvas.mark(detail);}
 function render(){syncPrint();canvas.mark(null);const target=$('doc-elements');target.replaceChildren();if(!config){canvas.show(null);return;}
  if(config.schema_version==='2.0'){for(const line of describe2(config)){const item=node('div');item.className='designer-row';item.append(node('span',line));target.append(item);}$('doc-table-status').textContent='2.0 分带模板：明细表 '+config.bands.detail.elements[0].columns.length+' 列，按分组生成每份文档，超过一页自动续页并承前过次。';canvas.unsupported('画布只显示 1.x 模板（固定文本框与明细表）。当前是 2.0 分带模板：用下方“模板元数据JSON”编辑，仍可预览、检查样本、保存与发布；带的画布编辑在后续版本提供。');return;}
  for(const e of config.elements){const item=node('div');item.className='designer-row';item.append(node('span',e.id+' · '+e.source+' · '+(e.source==='literal'?e.text:e.field)));for(const k of ['x','y','width','height','font_size']){const label=node('label',k),input=node('input');input.type='number';input.step=k==='font_size'?'1':'0.1';input.value=e[k];input.setAttribute('aria-label',e.id+' '+k);input.addEventListener('change',()=>action(()=>{const d=JSON.parse(JSON.stringify(config));d.elements.find(v=>v.id===e.id)[k]=Number(input.value);if(k!=='font_size'&&!Number.isInteger(Number(input.value))&&d.schema_version==='1.0'){d.schema_version='1.1';d.print={mode:'full',offset_x:0,offset_y:0};}apply(d,'修改 '+e.id+' '+k);}));label.append(input);item.append(label);}const remove=node('button','移除');remove.type='button';remove.addEventListener('click',()=>action(()=>apply({...config,elements:config.elements.filter(v=>v.id!==e.id)},'移除 '+e.id)));item.append(remove);target.append(item);}
  if(config.table){const t=config.table,item=node('div');item.className='designer-row';item.append(node('span','明细表 · '+t.columns.map(c=>c.field+' '+c.width+'mm').join('、')));for(const k of ['x','y','width','height','font_size']){const label=node('label',k),input=node('input');input.type='number';input.step=k==='font_size'?'1':'0.1';input.value=t[k];input.setAttribute('aria-label','table '+k);input.addEventListener('change',()=>action(()=>{const d=JSON.parse(JSON.stringify(config));d.table[k]=Number(input.value);apply(d,'修改明细表 '+k);}));label.append(input);item.append(label);}const remove=node('button','移除明细表');remove.type='button';remove.addEventListener('click',()=>action(()=>removeTable()));item.append(remove);target.append(item);}
  $('doc-table-status').textContent=config.table?'明细表：'+config.table.columns.length+' 列，列出冻结快照的全部行，超过一页自动续页并承前过次。':'无明细表。';canvas.show(config);}
 // A template with a table is 1.3 whatever else changes (print settings, background); without one the given version applies.
 const schemaFor=(c,otherwise)=>c.table?'1.3':otherwise;
 const only1x=()=>{if(config?.schema_version==='2.0')throw Error('2.0 分带模板请用“模板元数据JSON”编辑；文本框、明细表、套打与底稿设置只适用于 1.x 模板。');};
 function removeTable(){only1x();if(!config.table)return;const d={...config,schema_version:config.background?'1.2':'1.1'};delete d.table;apply(d,'移除明细表');}
 function addTable(){action(()=>{only1x();const chosen=[...$('doc-table-columns').selectedOptions].map(o=>o.value);if(chosen.length<1||chosen.length>12)throw Error('请选择1–12个明细表字段。');const width=Number($('doc-table-width').value),size=Number($('doc-table-font').value),first=chosen.length===1?width:Math.max(labelMinimum[size]||42,Math.floor(width*10/chosen.length)/10),rest=chosen.length-1,each=rest?Math.floor((width-first)*10/rest)/10:0;const columns=chosen.map((field,i)=>({field,width:i===0?first:i===chosen.length-1?Math.round((width-first-each*(rest-1))*10)/10:each}));const table={x:Number($('doc-table-x').value),y:Number($('doc-table-y').value),width,height:Number($('doc-table-height').value),font_size:Number($('doc-table-font').value),columns};const d={...config,schema_version:'1.3',print:config.print||{mode:'full',offset_x:0,offset_y:0},table};if(!config.background)delete d.background;apply(d,'添加明细表');});}
 $('doc-table-add').addEventListener('click',addTable);$('doc-table-remove').addEventListener('click',()=>action(removeTable));
 function add(source){action(()=>{only1x();const used=new Set(config.elements.map(e=>e.id));let n=1;while(used.has('n'+n))n++;const e={id:'n'+n,source,field:source==='literal'?'':$('doc-field').value,text:source==='literal'?$('doc-text').value:'',x:10,y:25+config.elements.length*15,width:190,height:10,font_size:12,align:'left'};apply({...config,elements:[...config.elements,e]},'添加 '+e.id);});}
 $('doc-add-text').addEventListener('click',()=>add('literal'));$('doc-add-row').addEventListener('click',()=>add('row'));$('doc-add-total').addEventListener('click',()=>add('total'));
 $('doc-reset').addEventListener('click',()=>action(()=>replaceTemplate({schema_version:'1.0',kind:'document',binding:binding(),elements:[{id:'n1',source:'literal',field:'',text:'Document preview',x:10,y:25,width:190,height:12,font_size:18,align:'left'}]})));
 $('doc-print-apply').addEventListener('click',()=>action(()=>{only1x();apply({...config,schema_version:schemaFor(config,config.schema_version==='1.2'?'1.2':'1.1'),print:{mode:$('doc-print-mode').value,offset_x:Number($('doc-offset-x').value),offset_y:Number($('doc-offset-y').value)}},'套打设置');}));
 function applyBackground(ref){only1x();if(!validBackground(ref))throw Error('无法验证底稿引用。');apply({...config,schema_version:schemaFor(config,'1.2'),print:config.print||{mode:'full',offset_x:0,offset_y:0},background:{id:ref.id,sha256:ref.sha256}},'应用底稿');}
 $('doc-background-upload').addEventListener('click',()=>action(()=>{const file=$('doc-background-file').files[0];if(!file||file.size<1||file.size>524288)throw Error('请选择不超过512KiB的A4 PNG。');$('doc-background-file').value='';ports.upload(file,binding(),ref=>{applyBackground(ref);$('doc-status').textContent='底稿已上传并应用；请保存模板版本。';});}));
 $('doc-background-list').addEventListener('click',()=>action(()=>ports.exchange('/document-backgrounds',undefined,values=>{if(!Array.isArray(values)||values.length>8||!values.every(v=>validBackground({id:v.id,sha256:v.sha256})&&keys(v,['id','sha256','binding','width','height'])&&Number.isSafeInteger(v.width)&&v.width>=210&&v.width<=1240&&Number.isSafeInteger(v.height)&&v.height>=297&&v.height<=1754))throw Error('无法验证底稿列表。');backgrounds=values.filter(v=>sameBinding(v));$('doc-background-listing').replaceChildren();for(const v of backgrounds){const o=node('option',v.id+' · '+v.width+' × '+v.height);o.value=v.id;$('doc-background-listing').append(o);}$('doc-status').textContent='当前数据集可用底稿 '+backgrounds.length+' 个。';},'底稿列表')));
 $('doc-background-apply').addEventListener('click',()=>action(()=>{const v=backgrounds.find(v=>v.id===$('doc-background-listing').value);if(!v)throw Error('请先读取并选择当前数据集底稿。');applyBackground({id:v.id,sha256:v.sha256});}));
 $('doc-background-remove').addEventListener('click',()=>action(()=>{only1x();if(!config.background)return;const d={...config,schema_version:schemaFor(config,'1.1')};delete d.background;apply(d,'移除底稿');}));
 $('doc-json-save').addEventListener('click',()=>action(()=>{$('doc-json').value=JSON.stringify(config,null,2);$('doc-status').textContent='元数据JSON已生成，可复制保存；不包含数据或授权。';}));
 $('doc-json-open').addEventListener('click',()=>action(()=>{const text=$('doc-json').value;if(new TextEncoder().encode(text).length>65536)throw Error('模板超过64KiB。');replaceTemplate(JSON.parse(text));}));
 $('doc-pdf').addEventListener('click',()=>action(()=>{previews();validate(config);const row=Number($('doc-row').value);if(!ports.snapshot()||!Number.isSafeInteger(row)||row<1||row>1000)throw Error('请先生成/打开冻结快照，并填写有效的快照全局行号。');ports.download({config:JSON.parse(JSON.stringify(config)),snapshot_id:ports.snapshot(),row_index:row-1},undefined,refusalFor());}));
 // Sample check (S03-I): the template against the first and last row and the longest and shortest value of each row-field box, by the layout the issue worker runs, without rendering.
 const checkReasons={first:'首行',last:'末行',most_rows:'行数最多的分组',fewest_rows:'行数最少的分组'},checkCodes={UNSUPPORTED_COMBINATION:'值放不下所在框或列，或含字体不支持的字符'};
 const labelsOf=()=>Object.fromEntries((result?result.columns:[]).map(c=>[c.id,c.label]));
 const reasonText=r=>{const m=/^(longest|shortest):((?:n|e)\d+)$/.exec(r),c=/^cell:(.+)$/.exec(r);return checkReasons[r]||(m?m[2]+(m[1]==='longest'?' 最长':' 最短'):c?'列 '+(labelsOf()[c[1]]||c[1])+' 最长的单元格':r);};
 function checkShape(v){
  if(!keys(v,['rows'])||!Array.isArray(v.rows)||v.rows.length>50)throw Error('无法验证样本检查结果。');let last=-1;
  for(const r of v.rows){
   if(!r||typeof r!=='object'||Array.isArray(r)||!Object.keys(r).every(k=>['row_index','ok','code','detail','reasons'].includes(k))||!Number.isSafeInteger(r.row_index)||r.row_index<=last||r.row_index>=1000000||typeof r.ok!=='boolean'||(r.ok?r.code!==undefined||r.detail!==undefined:(typeof r.code!=='string'||!/^[A-Z][A-Z0-9_]{0,63}$/.test(r.code)||(r.detail!==undefined&&!window.reportingRefusal.valid(r.detail))))||(r.reasons!==undefined&&(!Array.isArray(r.reasons)||r.reasons.length>50||!r.reasons.every(x=>typeof x==='string'&&/^(first|last|most_rows|fewest_rows|(longest|shortest):(?:n(?:[1-9]|1[0-9]|2[0-4])|e(?:[1-9]|[1-5][0-9]|6[0-4]))|cell:[^\x00-\x1f\x7f]{1,128})$/.test(x)))))throw Error('无法验证样本检查结果。');
   last=r.row_index;
  }
 }
 function showChecks(v,request){
  const body=$('doc-check-body'),wrap=$('doc-check-wrap'),line=$('doc-check-status');
  body.replaceChildren();wrap.hidden=true;
  if(v===null){if(checks)line.textContent='模板或快照已变化，上一次的检查结果已作废；请重新检查样本。';checks=null;return;}
  checks=null;
  if(!request||request.snapshot_id!==ports.snapshot()||JSON.stringify(request.config)!==JSON.stringify(config))return;
  try{checkShape(v);}catch(e){line.textContent=e.message;return;}
  for(const r of v.rows){
   const row=node('tr'),load=node('button','载入预览行号');load.type='button';load.className='button secondary small';
   load.addEventListener('click',()=>{$('doc-row').value=String(r.row_index+1);line.textContent='预览行号已设为 '+(r.row_index+1)+'。';});
   const act=node('td');act.append(load);
   // A refusal that names an element or a column of a 1.x template can be shown on the canvas (S03-J).
   if(r.detail&&(r.detail.element||r.detail.column)&&config?.schema_version!=='2.0'){const where=node('button','在画布定位');where.type='button';where.className='button secondary small';where.addEventListener('click',()=>{canvas.mark(r.detail);line.textContent='第 '+(r.row_index+1)+' 行：'+window.reportingRefusal.text(r.detail,labelsOf())+'。';});act.append(where);}
   row.append(node('td','第 '+(r.row_index+1)+' 行'),node('td',r.ok?'通过':'不通过 · '+r.code+(checkCodes[r.code]?'（'+checkCodes[r.code]+'）':'')+(r.detail?' · '+window.reportingRefusal.text(r.detail,labelsOf()):'')),node('td',(r.reasons||[]).map(reasonText).join('、')||'—'),act);body.append(row);
  }
  const bad=v.rows.filter(r=>!r.ok).length;checks=v;
  line.textContent=v.rows.length?'已检查 '+v.rows.length+' 行：通过 '+(v.rows.length-bad)+' 行，不通过 '+bad+' 行。只核对版面（字体、字符与尺寸），不渲染 PDF；Typst 渲染才能发现的问题仍以 PDF 预览为准。':'快照没有可检查的行。';
  wrap.hidden=!v.rows.length;
 }
 $('doc-check').addEventListener('click',()=>action(()=>{previews();validate(config);if(!ports.snapshot())throw Error('请先生成/打开冻结快照，再检查样本。');const body={config:JSON.parse(JSON.stringify(config)),snapshot_id:ports.snapshot(),samples:'boundary'};$('doc-check-status').textContent='正在检查样本…';ports.check(body,v=>showChecks(v,body));}));
 return {clear(logout=false){past=[];future=[];scope='';checks=null;$('doc-check-body').replaceChildren();$('doc-check-wrap').hidden=true;$('doc-check-status').textContent='检查样本：用当前模板检查冻结快照的首行、末行与各行字段最长/最短的行，不渲染 PDF。';$('doc-check').hidden=true;canvas.clear();backgrounds=[];$('doc-background-file').value='';$('doc-background-listing').replaceChildren();$('doc-background-status').textContent='尚无当前授权模板。';$('doc-pdf').hidden=$('doc-saved-pdf').hidden=true;$('doc-issue').hidden=true;publishedOpen=false;$('doc-issues-body').replaceChildren();$('doc-issues-wrap').hidden=true;$('doc-issue-status').textContent='正式出具需要已发布模板、冻结快照与行号。';resetRelease();result=null;query=null;if(logout){config=null;saved=null;}$('doc-print-mode').value='full';$('doc-offset-x').value='0';$('doc-offset-y').value='0';$('doc-print-status').textContent='尚无当前授权模板。';$('doc-server-listing').replaceChildren();$('doc-server-version').value='1';$('doc-server-status').textContent='查询当前授权结果后可使用服务器模板。';$('doc-elements').replaceChildren();$('doc-field').replaceChildren();$('doc-table-columns').replaceChildren();$('doc-table-status').textContent='无明细表。';$('doc-json').value='';$('doc-text').value='';$('doc-row').value='1';$('doc-status').textContent='先查询当前授权数据；生成PDF需冻结快照。';},refresh(value,q){resetRelease();result=value;query=q;if(saved&&!sameBinding(saved.config))saved=null;$('doc-server-status').textContent=saved?'正在编辑模板 '+saved.id+' · 版本 '+saved.version:'未关联服务器模板。';const key=JSON.stringify([q,ports.snapshot()]);if(key!==scope){past=[];future=[];scope=key;}
try{validate(config);}catch{past=[];future=[];config={schema_version:'1.0',kind:'document',binding:binding(),elements:[{id:'n1',source:'literal',field:'',text:'Document preview',x:10,y:25,width:190,height:12,font_size:18,align:'left'}]};}$('doc-field').replaceChildren();$('doc-table-columns').replaceChildren();for(const c of value.columns){const o=node('option',c.label);o.value=c.id;$('doc-field').append(o);const p=node('option',c.label);p.value=c.id;$('doc-table-columns').append(p);}render();$('doc-pdf').hidden=$('doc-saved-pdf').hidden=$('doc-check').hidden=!ports.previews();$('doc-issue').hidden=!ports.issuance();$('doc-status').textContent=ports.previews()?'单页文本模板；生成PDF需当前冻结快照，行号为快照全局行号。':'单页文本模板；服务端未启用 PDF 渲染，不提供预览。';}};
};
