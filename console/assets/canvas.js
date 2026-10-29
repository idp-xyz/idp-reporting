'use strict';
// The document canvas (S03-I): a picture of the template and a pointer and keyboard way to edit it.
// It never decides what a template means. Every edit builds a new configuration and hands it to
// ports.edit, which is the designer's apply (validate + canonical + one undo step); a refusal leaves
// the configuration as it was, the items snap back and the status line names the reason.
//
// Coordinates are millimetres in the template and integer 0.1 mm units here (Math.round(mm*10)), so
// that no float drift reaches the template: u/10 is the double nearest to the decimal.
//
// Geometry, for tests and for anyone who needs a pointer position: the canvas is one SVG whose user
// units are pixels. A4 (210 x 297 mm) is drawn at 3 px per mm at 100 % zoom (px per mm = 3 * zoom /
// 100, zoom 50..200); the page's top-left corner is at (24, 24) from the SVG's top-left corner (the
// rulers take the 24 px before it), so millimetre m on the page is at 24 + 3 * zoom / 100 * m px.
// Drags use pointer deltas only; the one absolute position is where a drag began, to know which band is under the pointer.
//
// A schema 2.0 template (S03-L) is drawn as its page: the banner strip, then the bands stacked from the top of the safe
// region (y 25 mm) -- page header, document header, detail (stretched to the space the other bands leave it, never less
// than the table's minimum), document footer, with the page footer at the bottom of the region (y 270 mm). Every element
// belongs to a band and its position is relative to the band's top-left corner (x 10 mm); the model below turns that into
// page units for drawing, snapping, alignment and distribution, and back again when an element is written.
window.createReportingCanvas=function(ports){
 const NS='http://www.w3.org/2000/svg',SCALE=3,RULER=24,PAD=16,GUTTER=104,HANDLE=8,TOLERANCE=10,DRAG_PIXELS=3;
 const $=id=>document.getElementById(id);
 const U=mm=>Math.round(mm*10),M=u=>u/10,clone=v=>JSON.parse(JSON.stringify(v)),same=(a,b)=>JSON.stringify(a)===JSON.stringify(b);
 const svgEl=(tag,attrs,text)=>{const e=document.createElementNS(NS,tag);for(const k of Object.keys(attrs||{}))e.setAttribute(k,String(attrs[k]));if(text!==undefined)e.textContent=text;return e;};
 const node=(tag,text,className)=>{const e=document.createElement(tag);if(text!==undefined)e.textContent=text;if(className)e.className=className;return e;};
 const sources={literal:'固定文字',row:'行字段',total:'合计'},sources2={literal:'固定文字',field:'字段',group_sum:'分组合计',page:'页码文字',issue_number:'出具编号'};
 const bandNames=['page_header','document_header','detail','document_footer','page_footer'],FIXED=['page_header','document_header','document_footer','page_footer'],bandLabels=window.reportingRefusal.bandLabels;
 const SAFE_T=250,SAFE_B=2700,BAND_X=100,BAND_W=1900;

 // ---- the model: pure operations on a configuration, in 0.1 mm units ------------------------------
 const banded=d=>!!d&&(d.schema_version==='2.0'||d.schema_version==='2.1');
 const tableOf=d=>banded(d)?d.bands.detail.elements[0]:d.table,tableKey=d=>banded(d)?tableOf(d).id:'table';
 const items=d=>banded(d)?items2(d):[...d.elements.map(e=>({key:e.id,table:false,ref:e})),...(d.table?[{key:'table',table:true,ref:d.table}]:[])];
 const rectOf=i=>i.band!==undefined?rect2(i):({x:U(i.ref.x),y:U(i.ref.y),w:U(i.ref.width),h:U(i.ref.height)});
 const overlap=(a,b)=>a.x<b.x+b.w&&b.x<a.x+a.w&&a.y<b.y+b.h&&b.y<a.y+a.h;
 // The table's columns scale in proportion to its width, each rounded to 0.1 mm, the remainder on the last.
 function scaleColumns(t,w){const old=U(t.width),n=t.columns.length;let used=0;t.columns.forEach((c,k)=>{const cw=k===n-1?w-used:Math.round(U(c.width)*w/old);c.width=M(cw);used+=cw;});}
 function setRect(i,r){
  if(i.band!==undefined)return write2(i,r,i.top);
  const t=i.ref;
  if(i.table&&r.w!==U(t.width))scaleColumns(t,r.w);
  t.x=M(r.x);t.y=M(r.y);t.width=M(r.w);t.height=M(r.h);
 }
 // ---- schema 2.0: the bands. Page units (0.1 mm from the page's top-left corner) are the common frame. ----
 // Where each band is: its top and height. The detail band takes what the others leave of the safe region, but never less than
 // the table needs (ports.minDetail, 0.1 mm); the document footer follows it, so a footer that does not fit shows where it would hang.
 function geo(d){
  const b=d.bands,h=n=>b[n]?U(b[n].height):0,ph=h('page_header'),dh=h('document_header'),df=h('document_footer'),pf=h('page_footer'),detail=Math.max(ports.minDetail(tableOf(d).font_size),SAFE_B-SAFE_T-ph-dh-pf-df);
  const g={page_header:{top:SAFE_T,h:ph},document_header:{top:SAFE_T+ph,h:dh},detail:{top:SAFE_T+ph+dh,h:detail},document_footer:{top:SAFE_T+ph+dh+detail,h:df},page_footer:{top:SAFE_B-pf,h:pf}};
  g.document_footer.over=!!b.document_footer&&g.document_footer.top+df>SAFE_B-pf;return g;
 }
 function items2(d){const g=geo(d),out=[];for(const name of bandNames){const band=d.bands[name];if(!band)continue;for(const e of band.elements)out.push({key:e.id,table:e.type==='table',ref:e,band:name,top:g[name].top,bh:g[name].h});}return out;}
 function rect2(i){
  const e=i.ref;
  if(e.type==='line'){const x=Math.min(U(e.x1),U(e.x2)),y=Math.min(U(e.y1),U(e.y2));return {x:BAND_X+x,y:i.top+y,w:Math.abs(U(e.x2)-U(e.x1)),h:Math.abs(U(e.y2)-U(e.y1))};}
  if(e.type==='table')return {x:BAND_X+U(e.x),y:i.top,w:U(e.width),h:i.bh};
  if(e.type==='qrcode')return {x:BAND_X+U(e.x),y:i.top+U(e.y),w:U(e.size),h:U(e.size)};
  return {x:BAND_X+U(e.x),y:i.top+U(e.y),w:U(e.width),h:U(e.height)};
 }
 // Writes a page rectangle back as band positions, relative to the band whose top is T. A line keeps its shape and moves; the table
 // keeps its place in the detail band (only its x and width are its own).
 function write2(i,r,T){
  const e=i.ref;
  if(e.type==='line'){const o=rect2(i),dx=r.x-o.x,dy=r.y-o.y+i.top-T;e.x1=M(U(e.x1)+dx);e.x2=M(U(e.x2)+dx);e.y1=M(U(e.y1)+dy);e.y2=M(U(e.y2)+dy);return;}
  if(e.type==='table'){if(r.w!==U(e.width))scaleColumns(e,r.w);e.x=M(r.x-BAND_X);e.width=M(r.w);return;}
  // A QR code is square: the side takes the dimension that changed (the larger when both did).
  if(e.type==='qrcode'){const o=U(e.size);e.x=M(r.x-BAND_X);e.y=M(r.y-T);e.size=M(r.w!==o&&r.h!==o?Math.max(r.w,r.h):r.w!==o?r.w:r.h);return;}
  e.x=M(r.x-BAND_X);e.y=M(r.y-T);e.width=M(r.w);e.height=M(r.h);
 }
 // Puts an element of d (an item of items(d)) at a page rectangle inside band `to`, taking it out of its own band when that differs.
 function put2(d,i,r,to){
  const g=geo(d);
  if(to!==i.band){const from=d.bands[i.band].elements;from.splice(from.indexOf(i.ref),1);d.bands[to].elements.push(i.ref);}
  write2(i,r,g[to].top);
 }
 const bandAt=(d,g,v)=>FIXED.find(n=>d.bands[n]&&v>=g[n].top&&v<g[n].top+g[n].h);
 // A schema 1.0 template edited off whole millimetres becomes 1.1, as the inputs do.
 const settle=d=>{if(d.schema_version==='1.0'&&d.elements.some(e=>[e.x,e.y,e.width,e.height].some(v=>!Number.isInteger(v)))){d.schema_version='1.1';d.print={mode:'full',offset_x:0,offset_y:0};}return d;};
 function moveBy(d,keys,dx,dy){for(const i of items(d))if(keys.includes(i.key)){const r=rectOf(i);r.x+=dx;r.y+=dy;setRect(i,r);}return d;}
 function resizeTo(d,key,r){const i=items(d).find(v=>v.key===key);if(i)setRect(i,r);return d;}
 function align(d,keys,mode){
  const sel=items(d).filter(i=>keys.includes(i.key));if(sel.length<2)throw Error('对齐需要选择两项或更多。');
  const rs=sel.map(rectOf),l=Math.min(...rs.map(r=>r.x)),t=Math.min(...rs.map(r=>r.y)),rr=Math.max(...rs.map(r=>r.x+r.w)),b=Math.max(...rs.map(r=>r.y+r.h));
  sel.forEach((i,n)=>{const r=rs[n];if(mode==='left')r.x=l;else if(mode==='right')r.x=rr-r.w;else if(mode==='center')r.x=Math.round((l+rr-r.w)/2);else if(mode==='top')r.y=t;else if(mode==='bottom')r.y=b-r.h;else r.y=Math.round((t+b-r.h)/2);setRect(i,r);});
  return d;
 }
 // Equal gaps between the items in order along the axis; the first and the last stay. The gaps differ by at most 0.1 mm when the free space does not divide.
 function distribute(d,keys,axis){
  const sel=items(d).filter(i=>keys.includes(i.key)).map(i=>({i,r:rectOf(i)}));if(sel.length<3)throw Error('等距分布需要选择三项或更多。');
  const a=axis==='h'?'x':'y',s=axis==='h'?'w':'h';sel.sort((p,q)=>p.r[a]-q.r[a]||p.r[s]-q.r[s]);
  const gaps=sel.length-1,last=sel[gaps].r,free=last[a]+last[s]-sel[0].r[a]-sel.reduce((t,v)=>t+v.r[s],0);let run=sel[0].r[a]+sel[0].r[s];
  sel.forEach((v,n)=>{if(!n)return;v.r[a]=run+Math.round(n*free/gaps)-Math.round((n-1)*free/gaps);run=v.r[a]+v.r[s];setRect(v.i,v.r);});
  return d;
 }
 // Removing the table falls back to 1.2 with a background and 1.1 without, as the remove button does.
 function remove(d,keys){
  if(banded(d)){
   const gone=keys.filter(k=>k!==tableKey(d));if(!gone.length)throw Error('明细表不能删除：2.0 的明细带必须有一个明细表。');
   for(const n of FIXED)if(d.bands[n])d.bands[n].elements=d.bands[n].elements.filter(e=>!gone.includes(e.id));return d;
  }
  d.elements=d.elements.filter(e=>!keys.includes(e.id));if(keys.includes('table')&&d.table){d.schema_version=d.background?'1.2':'1.1';delete d.table;}return d;}
 // A copy of each selected box under the lowest free id, at the nearest free place below it: the
 // lowest y at or under the box's bottom where it overlaps nothing and fits (fits(d) says '' when valid).
 function duplicate(d,keys,fits){
  const made=[];
  for(const src of d.elements.filter(e=>keys.includes(e.id))){
   const used=new Set(d.elements.map(e=>e.id));let n=1;while(used.has('n'+n))n++;
   if(n>24)throw Error('最多24个文本框。');
   const copy={...src,id:'n'+n},h=U(src.height);let failure='',placed=false,tries=0;
   const others=items(d).map(rectOf);
   for(let y=U(src.y)+h;y+h<=2700&&tries<20;y++){
    if(others.some(o=>overlap({x:U(src.x),y,w:U(src.width),h},o)))continue;
    copy.y=M(y);const probe={...d,elements:[...d.elements,copy]};failure=fits?fits(settle(clone(probe))):'';
    if(!failure){placed=true;break;}tries++;
   }
   if(!placed)throw Error(failure||'下方没有可放置副本的空位。');
   d.elements.push(copy);made.push(copy.id);
  }
  return {d,made};
 }
 // A copy of each selected element (a 2.0 template: the table is not copied) under the lowest free id, in its own band at the nearest free place
 // below it: the lowest offset at or under its bottom where no other text box overlaps it and the template is valid (fits(d) says '' when valid).
 function duplicate2(d,keys,fits){
  const made=[];
  for(const src of items(d).filter(i=>keys.includes(i.key)&&!i.table)){
   const used=new Set(items(d).map(i=>i.key));let n=1;while(used.has('e'+n))n++;
   if(n>64)throw Error('最多64个元素。');
   const band=d.bands[src.band],H=U(band.height),e=src.ref,r=rect2(src),top=r.y-src.top,step=Math.max(r.h,10),texts=band.elements.filter(o=>boxed(o.type)&&o!==e).map(boxOf);
   let failure='',placed=false,tries=0,copy;
   for(let dy=step;top+dy+r.h<=H&&tries<20;dy++){
    copy=clone(e);copy.id='e'+n;if(e.type==='line'){copy.y1=M(U(e.y1)+dy);copy.y2=M(U(e.y2)+dy);}else copy.y=M(U(e.y)+dy);
    if(boxed(e.type)&&texts.some(o=>overlap(boxOf(copy),o)))continue;
    const probe=clone(d);probe.bands[src.band].elements.push(copy);failure=fits?fits(probe):'';
    if(!failure){placed=true;break;}tries++;
   }
   if(!placed)throw Error(failure||'下方没有可放置副本的空位。');
   band.elements.push(copy);made.push(copy.id);
  }
  return {d,made};
 }
 // A text, a code and an image own a box of their band that nothing else of that kind may overlap (a QR code's is a square).
 const boxed=type=>type==='text'||type==='barcode'||type==='qrcode'||type==='image',boxOf=o=>({x:U(o.x),y:U(o.y),w:U(o.type==='qrcode'?o.size:o.width),h:U(o.type==='qrcode'?o.size:o.height)});
 const columnMinimum=(d,i)=>U(ports.columnMinimum(tableOf(d).font_size,i,banded(d)));
 // Moves the boundary after column index so that the column to its left is `left` wide, giving
 // to or taking from the column to the right; each column keeps its minimum. Returns the width used.
 function moveBoundary(d,index,left){
  const c=tableOf(d).columns,pair=U(c[index].width)+U(c[index+1].width),w=Math.max(columnMinimum(d,index),Math.min(pair-columnMinimum(d,index+1),left));
  c[index].width=M(w);c[index+1].width=M(pair-w);return w;
 }
 // Snapping: the candidates are the moving feature's positions (the first is the one the grid takes);
 // an edge or centre of another item within 1 mm wins over the grid and shows a guide.
 function snapAxis(cands,targets,grid){
  let best=null;for(const c of cands)for(const t of targets){const dist=Math.abs(t-c);if(dist<=TOLERANCE&&(!best||dist<best.dist))best={dist,adj:t-c};}
  const adj=best?best.adj:Math.round(cands[0]/grid)*grid-cands[0];
  return {adj,guides:best?[...new Set(targets.filter(t=>cands.some(c=>Math.abs(c+adj-t)<=0.5)))]:[]};
 }
 function targets(d,exclude,axis){const out=[];for(const i of items(d)){if(exclude.includes(i.key))continue;const r=rectOf(i),a=axis==='x'?r.x:r.y,s=axis==='x'?r.w:r.h;out.push(a,a+s/2,a+s);}return out;}
 const bounds=(d,keys)=>{const rs=items(d).filter(i=>keys.includes(i.key)).map(rectOf);return {l:Math.min(...rs.map(r=>r.x)),t:Math.min(...rs.map(r=>r.y)),r:Math.max(...rs.map(r=>r.x+r.w)),b:Math.max(...rs.map(r=>r.y+r.h))};};
 function planMove(base,keys,dx,dy,grid){
  const b=bounds(base,keys),sx=snapAxis([b.l+dx,(b.l+b.r)/2+dx,b.r+dx],targets(base,keys,'x'),grid),sy=snapAxis([b.t+dy,(b.t+b.b)/2+dy,b.b+dy],targets(base,keys,'y'),grid);
  const mx=Math.round(dx+sx.adj),my=Math.round(dy+sy.adj),d=settle(moveBy(clone(base),keys,mx,my));
  // Guides only for the axes the pointer moved along: alignments that were there before the drag are not news.
  return {d,guides:{x:dx?sx.guides:[],y:dy?sy.guides:[]},readout:{u:b.l+mx,v:b.t+my,text:'x '+M(b.l+mx)+'  y '+M(b.t+my)+' 毫米'}};
 }
 function planResize(base,key,dir,dx,dy,grid){
  const i=items(base).find(v=>v.key===key),r0=rectOf(i),ex=targets(base,[key],'x'),ey=targets(base,[key],'y'),guides={x:[],y:[]};
  let l=r0.x,t=r0.y,r=r0.x+r0.w,b=r0.y+r0.h;
  const edge=(v,delta,tg,axis)=>{const s=snapAxis([v+delta],tg,grid);guides[axis].push(...s.guides);return Math.round(v+delta+s.adj);};
  if(dir.includes('w'))l=edge(l,dx,ex,'x');if(dir.includes('e'))r=edge(r,dx,ex,'x');if(dir.includes('n'))t=edge(t,dy,ey,'y');if(dir.includes('s'))b=edge(b,dy,ey,'y');
  // An edge dragged past the opposite one leaves a zero size, which the designer then refuses.
  if(r<l){if(dir.includes('w'))l=r;else r=l;}if(b<t){if(dir.includes('n'))t=b;else b=t;}
  const d=settle(resizeTo(clone(base),key,{x:l,y:t,w:r-l,h:b-t}));
  return {d,guides,readout:{u:l,v:t,text:M(r-l)+' × '+M(b-t)+' 毫米'}};
 }
 function planBoundary(base,index,dx,grid){
  const t=tableOf(base),ox=banded(base)?BAND_X:0,before=t.columns.slice(0,index).reduce((s,c)=>s+U(c.width),0),at=ox+U(t.x)+before+U(t.columns[index].width),s=snapAxis([at+dx],banded(base)?targets2(base,[t.id],'x','detail'):targets(base,['table'],'x'),grid),want=Math.round(at+dx+s.adj),d=clone(base),w=moveBoundary(d,index,want-ox-U(t.x)-before);
  return {d,guides:{x:ox+U(t.x)+before+w===want?s.guides:[],y:[]},readout:{u:ox+U(t.x)+before+w,v:banded(base)?geo(base).detail.top:U(t.y),text:'第 '+(index+1)+' 列 '+M(w)+' 毫米'}};
 }
 // Snapping targets in a 2.0 template, in page units: the edges and centres of the other elements of the band, and the band's own edges and centre.
 function targets2(d,exclude,axis,name){
  const g=geo(d)[name],out=axis==='x'?[BAND_X,BAND_X+BAND_W/2,BAND_X+BAND_W]:[g.top,g.top+g.h/2,g.top+g.h];
  for(const i of items(d)){if(i.band!==name||exclude.includes(i.key))continue;const r=rectOf(i),a=axis==='x'?r.x:r.y,s=axis==='x'?r.w:r.h;out.push(a,a+s/2,a+s);}
  return out;
 }
 // Moving 2.0 elements: all by the same amount. When every one of them is in one band, the band under the pointer (py is where the drag began,
 // in page units) takes them, their positions becoming relative to it; the grid and the snapping work in the frame of the band they land in.
 function planMove2(base,keys,dx,dy,grid,anchor,py){
  const g=geo(base),its=items(base).filter(i=>keys.includes(i.key)),home=[...new Set(its.map(i=>i.band))];
  if(its.length&&its.every(i=>i.table))dy=0;
  const hit=home.length===1&&!its.some(i=>i.table)&&py!==undefined?bandAt(base,g,py+dy):undefined,to=hit||(its.find(i=>i.key===anchor)||its[0]).band,T=g[to].top;
  const rs=its.map(rectOf),l=Math.min(...rs.map(r=>r.x)),t=Math.min(...rs.map(r=>r.y)),rt=Math.max(...rs.map(r=>r.x+r.w)),b=Math.max(...rs.map(r=>r.y+r.h));
  const sx=snapAxis([l+dx,(l+rt)/2+dx,rt+dx],targets2(base,keys,'x',to),grid),sy=snapAxis([t+dy-T,(t+b)/2+dy-T,b+dy-T],targets2(base,keys,'y',to).map(v=>v-T),grid);
  // An axis the pointer did not move along stays where it is: a level drag does not slide into a snap on the other axis.
  const mx=dx?Math.round(dx+sx.adj):0,my=dy?Math.round(dy+sy.adj):0,d=clone(base);
  for(const i of items(d))if(keys.includes(i.key)){const r=rectOf(i);r.x+=mx;r.y+=my;put2(d,i,r,i.table||home.length>1?i.band:to);}
  return {d,guides:{x:dx?sx.guides:[],y:dy?sy.guides.map(v=>v+T):[]},readout:{u:l+mx,v:t+my,text:'x '+M(l+mx-BAND_X)+'  y '+M(t+my-T)+' 毫米 · '+bandLabels[to]}};
 }
 // Resizing one 2.0 element (the table: only its left and right edges): edges snap to the other elements of the band and to its edges.
 function planResize2(base,key,dir,dx,dy,grid){
  const i=items(base).find(v=>v.key===key),r0=rectOf(i),T=i.top,ex=targets2(base,[key],'x',i.band),ey=targets2(base,[key],'y',i.band).map(v=>v-T),guides={x:[],y:[]};
  let l=r0.x,t=r0.y-T,r=r0.x+r0.w,b=r0.y-T+r0.h;
  const edge=(v,delta,tg,axis)=>{const s=snapAxis([v+delta],tg,grid);guides[axis].push(...s.guides.map(u=>axis==='y'?u+T:u));return Math.round(v+delta+s.adj);};
  if(dir.includes('w'))l=edge(l,dx,ex,'x');if(dir.includes('e'))r=edge(r,dx,ex,'x');if(dir.includes('n'))t=edge(t,dy,ey,'y');if(dir.includes('s'))b=edge(b,dy,ey,'y');
  if(r<l){if(dir.includes('w'))l=r;else r=l;}if(b<t){if(dir.includes('n'))t=b;else b=t;}
  const d=clone(base);setRect(items(d).find(v=>v.key===key),{x:l,y:t+T,w:r-l,h:b-t});
  return {d,guides,readout:{u:l,v:t+T,text:M(r-l)+' × '+M(b-t)+' 毫米'}};
 }
 // One end of a line: it snaps to the grid, to the elements of the band, to the band's edges and to the line's other end (so that lines come out level or upright).
 function planLineEnd(base,key,end,dx,dy,grid){
  const i=items(base).find(v=>v.key===key),e=i.ref,T=i.top,o=end===1?2:1,xs=[...targets2(base,[key],'x',i.band).map(v=>v-BAND_X),U(e['x'+o])],ys=[...targets2(base,[key],'y',i.band).map(v=>v-T),U(e['y'+o])];
  const sx=snapAxis([U(e['x'+end])+dx],xs,grid),sy=snapAxis([U(e['y'+end])+dy],ys,grid),nx=Math.round(U(e['x'+end])+dx+sx.adj),ny=Math.round(U(e['y'+end])+dy+sy.adj),d=clone(base),j=items(d).find(v=>v.key===key);
  j.ref['x'+end]=M(nx);j.ref['y'+end]=M(ny);
  return {d,guides:{x:dx?sx.guides.map(v=>v+BAND_X):[],y:dy?sy.guides.map(v=>v+T):[]},readout:{u:BAND_X+nx,v:T+ny,text:'端点 '+end+'：x '+M(nx)+'  y '+M(ny)+' 毫米'}};
 }
 // A band's height by its bottom edge: whole millimetres (0.1 with Alt), or the bottom of the lowest element within 1 mm.
 function planBand(base,name,dy,grid){
  const band=base.bands[name],h0=U(band.height),T=geo(base)[name].top,all=items(base),tg=band.elements.map(e=>{const r=rectOf(all.find(i=>i.key===e.id));return r.y+r.h-T;});
  const s=snapAxis([h0+dy],tg,grid),h=Math.round(h0+dy+s.adj),d=clone(base);d.bands[name].height=M(h);
  return {d,guides:{x:[],y:dy?s.guides.map(v=>v+T):[]},readout:{u:BAND_X,v:T+h,text:bandLabels[name]+'高度 '+M(h)+' 毫米'}};
 }

 // ---- state and elements ----------------------------------------------------------------------------
 let config=null,selection=[],zoom=100,drag=null,palette=null,focusAfter=null,marked=null;
 const host=$('doc-canvas-host'),statusLine=$('doc-canvas-status'),live=$('doc-canvas-live'),modeLine=$('doc-canvas-mode');
 const svg=svgEl('svg',{class:'cv-svg',role:'group','aria-label':'A4 文档画布，单位毫米','aria-describedby':'doc-canvas-help',tabindex:'-1'});
 const layers={};for(const name of ['rulers','page','bands','shapes','items','overlay','guides','tags','readout']){layers[name]=svgEl('g',{class:'cv-'+name});svg.append(layers[name]);}
 layers.tags.setAttribute('aria-hidden','true');layers.guides.setAttribute('aria-hidden','true');layers.shapes.setAttribute('aria-hidden','true');layers.readout.setAttribute('aria-hidden','true');layers.rulers.setAttribute('aria-hidden','true');
 if(host)host.append(svg);
 const ppm=()=>SCALE*zoom/100;
 const fmt=n=>String(n);
 const labels=()=>{const m={};for(const c of ports.columns())m[c.id]=c.label;return m;};
 function nameOf(i,lab){
  if(i.band!==undefined)return nameOf2(i,lab);
  const r=i.ref,pos='，x '+fmt(r.x)+' 毫米，y '+fmt(r.y)+' 毫米，宽 '+fmt(r.width)+' 毫米，高 '+fmt(r.height)+' 毫米';
  if(i.table)return '明细表，列 '+r.columns.map(c=>lab[c.field]||c.field).join('、')+'，字号 '+r.font_size+' 点'+pos;
  return r.id+'，'+sources[r.source]+(r.source==='literal'?' “'+r.text+'”':' '+(lab[r.field]||r.field))+'，字号 '+r.font_size+' 点'+pos;
 }
 function nameOf2(i,lab){
  const e=i.ref,band='，'+bandLabels[i.band];
  if(e.type==='table')return '明细表 '+e.id+'，列 '+e.columns.map(c=>c.label||lab[c.field]||c.field).join('、')+'，字号 '+e.font_size+' 点，x '+fmt(e.x)+' 毫米，宽 '+fmt(e.width)+' 毫米'+band;
  if(e.type==='line')return e.id+'，线，从 x '+fmt(e.x1)+' y '+fmt(e.y1)+' 到 x '+fmt(e.x2)+' y '+fmt(e.y2)+' 毫米，线宽 '+fmt(e.stroke)+' 点'+band;
  if(e.type==='qrcode')return e.id+'，二维码，纠错 '+e.ecc+'，值 '+codeValue(e,lab)+'，x '+fmt(e.x)+' 毫米，y '+fmt(e.y)+' 毫米，边长 '+fmt(e.size)+' 毫米'+band;
  const pos='，x '+fmt(e.x)+' 毫米，y '+fmt(e.y)+' 毫米，宽 '+fmt(e.width)+' 毫米，高 '+fmt(e.height)+' 毫米'+band;
  if(e.type==='barcode')return e.id+'，条码 Code 128，值 '+codeValue(e,lab)+(e.human_readable?'，印出值，字号 '+e.font_size+' 点':'，不印出值')+pos;
  if(e.type==='image')return e.id+'，图片 '+e.image.id+pos;
  if(e.type==='rect')return e.id+'，矩形，线宽 '+fmt(e.stroke)+' 点，填充 '+(e.fill==='none'?'无':e.fill)+pos;
  return e.id+'，'+sources2[e.source]+(e.source==='literal'||e.source==='page'?' “'+e.text+'”':e.source==='issue_number'?'':' '+(lab[e.field]||e.field))+'，字号 '+e.font_size+' 点'+(e.bold?'，粗体':'')+(e.wrap?'，自动换行':'')+pos;
 }
 // Where a code gets its value, in words.
 const codeValue=(e,lab)=>e.source==='literal'?'固定 “'+e.text+'”':e.source==='issue_number'?'出具编号':'字段 '+(lab[e.field]||e.field);
 const idle='先查询当前授权数据，画布随模板显示。',blank='画布显示版面，不显示底稿与数据，PDF 预览为准。';
 const status=text=>{if(statusLine)statusLine.textContent=text;};
 const announce=text=>{if(live)live.textContent=text;};
 function selectionText(){
  if(!config||!selection.length)return '未选择任何项目。';
  const lab=labels(),its=items(config).filter(i=>selection.includes(i.key));
  return its.length===1?'已选择 '+nameOf(its[0],lab)+'。':'已选择 '+its.length+' 项：'+its.map(i=>i.table?'明细表':i.key).join('、')+'。';
 }

 // ---- drawing -------------------------------------------------------------------------------------------
 // Top to bottom, then left to right; at the same place the 1.x table comes last, the 2.0 table first (a line along the edge of the band above it is drawn over it, so that it stays selectable).
 function readingOrder(d){const tie=banded(d)?-1:1;return items(d).sort((a,b)=>{const p=rectOf(a),q=rectOf(b);return p.y-q.y||p.x-q.x||(a.table?tie:0)-(b.table?tie:0);});}
 function rulers(p,W,H){
  const out=[svgEl('rect',{class:'cv-ruler',x:0,y:0,width:W,height:RULER}),svgEl('rect',{class:'cv-ruler',x:0,y:0,width:RULER,height:H})],label=p>=2.4?10:20;
  let top='',left='';
  for(let mm=0;mm<=300;mm+=5){
   const major=mm%10===0,len=major?9:4;
   if(mm<=210){const x=RULER+mm*p;top+='M'+x+' '+RULER+'V'+(RULER-len);if(mm%label===0)out.push(svgEl('text',{class:'cv-ruler-text',x:x+2,y:11},String(mm)));}
   if(mm<=297){const y=RULER+mm*p;left+='M'+RULER+' '+y+'H'+(RULER-len);if(mm%label===0&&mm>0)out.push(svgEl('text',{class:'cv-ruler-text',x:1,y:y-2},String(mm)));}
  }
  out.push(svgEl('path',{class:'cv-ruler-ticks',d:top+left}),svgEl('text',{class:'cv-ruler-text',x:1,y:11},'mm'));
  return out;
 }
 // The 1.x boxes and table, in reading order; the print offset's ghosts under them.
 function drawBoxes(d,extra,p,X,L,order,bad,lab){
  const print=d.print||{mode:'full',offset_x:0,offset_y:0},ox=U(print.offset_x),oy=U(print.offset_y);
  // The printed place of each item when the whole print is offset; the literals the variables mode does not print are dimmed.
  const prints=i=>i.table||print.mode==='full'||i.ref.source!=='literal';
  if(ox||oy)for(const i of order){if(!prints(i))continue;const r=rectOf(i);layers.page.append(svgEl('rect',{class:'cv-ghost',x:X(r.x+ox),y:X(r.y+oy),width:L(r.w),height:L(r.h)}));}
  for(const i of order){
   const r=rectOf(i),x=X(r.x),y=X(r.y),w=L(r.w),h=L(r.h),sel=selection.includes(i.key),dim=!prints(i);
   const refused=!!marked&&!!marked.element&&marked.element===i.key;
   const g=svgEl('g',{class:'cv-item '+(i.table?'cv-table':'cv-'+i.ref.source)+(sel?' cv-selected':'')+(dim?' cv-dim':'')+(bad.has(i.key)?' cv-bad':'')+(refused?' cv-refused':''),'data-key':i.key,tabindex:'0',role:'button','aria-pressed':sel?'true':'false','aria-label':nameOf(i,lab)+(dim?'，变量层不打印':'')+(refused?'，预览或检查指出此处放不下或无法打印':'')});
   g.append(svgEl('rect',{class:'cv-box',x,y,width:w,height:h}));
   if(i.table){
    const fs=i.ref.font_size*25.4/72*p,head=Math.min(h,fs*2.4),cols=i.ref.columns;let run=0;
    g.append(svgEl('rect',{class:'cv-thead',x,y,width:w,height:head}));
    for(let y2=y+head+fs*2;y2<y+h-1&&y2<y+head+fs*2*30;y2+=fs*2)g.append(svgEl('line',{class:'cv-rowline',x1:x,x2:x+w,y1:y2,y2}));
    cols.forEach((c,k)=>{
     const cw=L(U(c.width)),clip=svgEl('svg',{x:x+L(run),y,width:cw,height:head});
     clip.append(svgEl('text',{class:'cv-text',x:3,y:head/2+fs*0.35,'font-size':fs},lab[c.field]||c.field));g.append(clip);
     if(k>0)g.append(svgEl('line',{class:'cv-colline',x1:x+L(run),x2:x+L(run),y1:y,y2:y+h}));run+=U(c.width);
    });
   }else{
    const e=i.ref,fs=e.font_size*25.4/72*p,clip=svgEl('svg',{x,y,width:w,height:h}),text=e.source==='literal'?e.text:(e.source==='total'?'〔合计〕':'〔')+(lab[e.field]||e.field)+(e.source==='total'?'':'〕');
    clip.append(svgEl('text',{class:'cv-text',x:e.align==='left'?0:e.align==='center'?w/2:w,y:1.16*fs,'font-size':fs,'text-anchor':e.align==='left'?'start':e.align==='center'?'middle':'end'},text));g.append(clip);
   }
   layers.items.append(g);
   layers.tags.append(svgEl('text',{class:'cv-tag',x:x+1,y:Math.max(y-3,RULER+9)},i.table?'明细表':i.ref.id+' · '+sources[i.ref.source]));
  }
 }
 // The 2.0 bands (a labelled region each, the fixed ones with a grabbable bottom edge; the edges the keyboard reaches are returned, to go last in the tab order) and their elements, in reading order. The filled rectangles are drawn
 // in a layer of their own under every element, so that a text on a grey box shows whatever the order of the two.
 const greys={grey1:245,grey2:232,grey3:215,grey4:190};
 // The modules a code takes, estimated for the placeholder's quiet zone: a literal's own length, 16 characters for a field and the longest
 // issue number (32) for the number; the platform's layout (not this estimate) decides what fits. Code 128: a start symbol, the characters, the
 // check symbol and the stop, 11 modules each but the stop's 13, and ten modules of quiet zone on each side; QR: byte mode, the least version
 // that holds the bytes at the level (the data codewords of versions 1 to 17 at L, M, Q, H), four modules of quiet zone on each side.
 const QR_WORDS=[[19,16,13,9],[34,28,22,16],[55,44,34,26],[80,64,48,36],[108,86,62,46],[136,108,76,60],[156,124,88,66],[194,154,110,86],[232,182,132,100],[274,216,154,122],[324,254,180,140],[370,290,206,158],[428,334,244,180],[461,365,261,197],[523,415,295,223],[589,453,325,253],[647,507,367,283]];
 const valueSize=(e,bytes)=>e.source==='literal'?(bytes?new TextEncoder().encode(e.text).length:[...e.text].length):e.source==='issue_number'?32:16;
 const code128Modules=e=>11*(Math.min(64,Math.max(1,valueSize(e)))+2)+13+20;
 const qrModules=e=>{const n=valueSize(e,true),level='LMQH'.indexOf(e.ecc);for(let v=1;v<=QR_WORDS.length;v++)if(QR_WORDS[v-1][level]-(v<10?2:3)>=n)return 17+4*v+8;return 85+8;};
 // A code as a labelled placeholder: its box, the symbol inside the quiet zone (bars; three finder patterns), the value it takes.
 function drawCode(g,e,x,y,w,h,p,lab){
  const qr=e.type==='qrcode',total=qr?qrModules(e):code128Modules(e),quiet=(qr?4:10)/total,line=(qr?'二维码 QR '+e.ecc:'条码 Code 128')+' · '+codeValue(e,lab);
  // The line of the value is under the bars of a barcode that prints it: 1 pt and 1.46 type sizes.
  const zone=!qr&&e.human_readable?(1+1.46*e.font_size)*25.4/72*p:0,sx=x+w*quiet,sy=qr?y+h*quiet:y,sw=w*(1-2*quiet),sh=qr?h*(1-2*quiet):Math.max(0,h-zone);
  g.append(svgEl('rect',{class:'cv-quiet',x:sx,y:sy,width:sw,height:sh}));
  if(qr){
   const f=sw*7/(total-8);
   for(const [fx,fy] of [[sx,sy],[sx+sw-f,sy],[sx,sy+sh-f]])g.append(svgEl('rect',{class:'cv-finder',x:fx+0.5,y:fy+0.5,width:Math.max(1,f-1),height:Math.max(1,f-1)}));
  }else{
   let bars='';for(let k=0;k*4<sw-2;k++)bars+='M'+(sx+1+k*4)+' '+sy+'v'+sh;
   g.append(svgEl('path',{class:'cv-bars','stroke-width':2,d:bars}));
  }
  const clip=svgEl('svg',{x,y,width:w,height:h});
  clip.append(svgEl('text',{class:'cv-codelabel',x:3,y:11},line),svgEl('text',{class:'cv-codelabel cv-quiet-note',x:3,y:h-3},'静区 '+(qr?'4':'10')+' 模块（约 '+Math.round(total)+' 模块估算）'));
  g.append(clip);
 }
 // An image from the content route when the console has it, else a labelled box.
 function drawImage(g,e,x,y,w,h){
  const url=ports.image?ports.image(e.image):'',clip=svgEl('svg',{x,y,width:w,height:h});
  if(url)g.append(svgEl('image',{class:'cv-picture',href:url,x,y,width:w,height:h,preserveAspectRatio:'xMidYMid meet'}));
  clip.append(svgEl('text',{class:'cv-codelabel',x:3,y:11},'图片 '+e.image.id.slice(0,8)+(url?'':' · 未读取')));g.append(clip);
 }
 function drawBands(d,extra,p,X,L,order,bad,lab){
  const g=geo(d),metrics=ports.metrics?ports.metrics():new Set(),edges=[];
  for(const name of bandNames){
   const band=d.bands[name];if(!band)continue;
   const b=g[name],fixed=name!=='detail',head=bandLabels[name]+' · '+(fixed?'':'约 ')+fmt(M(b.h))+' 毫米',note=fixed?(b.over?'放不下时移到新页':''):'随页面伸展';
   layers.page.append(svgEl('rect',{class:'cv-band cv-b-'+name+(b.over?' cv-over':'')+(marked&&marked.band===name?' cv-refused-band':'')+(extra.badBand===name?' cv-bad-band':''),'data-band-box':name,x:X(BAND_X),y:X(b.top),width:L(BAND_W),height:L(b.h)}));
   layers.page.append(svgEl('line',{class:'cv-band-lead',x1:X(BAND_X+BAND_W),x2:X(2100)+GUTTER-6,y1:X(b.top),y2:X(b.top)}));
   layers.tags.append(svgEl('text',{class:'cv-band-tag',x:X(2100)+6,y:X(b.top)+11},head));
   if(note)layers.tags.append(svgEl('text',{class:'cv-band-note',x:X(2100)+6,y:X(b.top)+22},note));
   // The edge is two rectangles: the one the pointer grabs lies under the elements (an element or a line on the edge stays selectable), the one the keyboard reaches
   // comes after them in the tab order and takes no pointer events.
   if(fixed){
    const edge={x:X(BAND_X),y:X(b.top+b.h)-5,width:L(BAND_W),height:10,'data-band':name};
    layers.bands.append(svgEl('rect',{class:'cv-bh'+(extra.badBand===name?' cv-bad':''),'aria-hidden':'true',...edge}));
    edges.push(svgEl('rect',{class:'cv-bk'+(extra.badBand===name?' cv-bad':''),tabindex:'0',role:'slider','aria-orientation':'vertical','aria-label':bandLabels[name]+'高度：拖动下边缘或用上下方向键调整','aria-valuemin':'5','aria-valuemax':'245','aria-valuenow':fmt(M(b.h)),'aria-valuetext':bandLabels[name]+'高度 '+fmt(M(b.h))+' 毫米',...edge}));
   }
  }
  for(const i of order){
   const e=i.ref,r=rectOf(i),x=X(r.x),y=X(r.y),w=L(r.w),h=L(r.h),sel=selection.includes(i.key),refused=!!marked&&!!marked.element&&marked.element===i.key;
   const kind=e.type==='text'?{literal:'literal',field:'row',group_sum:'total',page:'page',issue_number:'number'}[e.source]:e.type;
   const g2=svgEl('g',{class:'cv-item cv-'+kind+(sel?' cv-selected':'')+(bad.has(i.key)?' cv-bad':'')+(refused?' cv-refused':''),'data-key':i.key,tabindex:'0',role:'button','aria-pressed':sel?'true':'false','aria-label':nameOf2(i,lab)+(refused?'，预览或检查指出此处放不下或无法打印':'')});
   let tag=e.id+' · ';
   if(e.type==='line'){
    const a=[X(BAND_X+U(e.x1)),X(i.top+U(e.y1)),X(BAND_X+U(e.x2)),X(i.top+U(e.y2))];
    g2.append(svgEl('line',{class:'cv-hit',x1:a[0],y1:a[1],x2:a[2],y2:a[3]}),svgEl('line',{class:'cv-stroke','stroke-width':Math.max(1,e.stroke*25.4/72*p),x1:a[0],y1:a[1],x2:a[2],y2:a[3]}));
    tag+='线';
   }else if(e.type==='rect'){
    layers.shapes.append(svgEl('rect',{class:'cv-shape',x,y,width:w,height:h,fill:e.fill==='none'?'none':'rgb('+greys[e.fill]+','+greys[e.fill]+','+greys[e.fill]+')',stroke:e.stroke>0?'currentColor':'none','stroke-width':Math.max(0.5,e.stroke*25.4/72*p)}));g2.append(svgEl('rect',{class:'cv-box',x,y,width:w,height:h}));
    tag+='矩形';
   }else if(e.type==='table'){
    const fs=e.font_size*25.4/72*p,head=Math.min(h,fs*2.4),row=fs*2,anchor=c=>(c.align||(metrics.has(c.field)?'right':'left')),edge=(c,cw)=>anchor(c)==='left'?3:anchor(c)==='center'?cw/2:cw-3,end=c=>anchor(c)==='left'?'start':anchor(c)==='center'?'middle':'end';let run=0;
    g2.append(svgEl('rect',{class:'cv-box',x,y,width:w,height:h}),svgEl('rect',{class:'cv-thead',x,y,width:w,height:head}));
    if(head+row<=h)g2.append(svgEl('line',{class:'cv-rowline',x1:x,x2:x+w,y1:y+head+row,y2:y+head+row}));
    e.columns.forEach((c,k)=>{
     const cw=L(U(c.width)),clip=svgEl('svg',{x:x+L(run),y,width:cw,height:Math.min(h,head+row)});
     clip.append(svgEl('text',{class:'cv-text cv-bold','font-weight':'700','x':edge(c,cw),y:head/2+fs*0.35,'font-size':fs,'text-anchor':end(c)},c.label||lab[c.field]||c.field),svgEl('text',{class:'cv-text cv-sample',x:edge(c,cw),y:head+row/2+fs*0.35,'font-size':fs,'text-anchor':end(c)},'〔'+(lab[c.field]||c.field)+'〕'));g2.append(clip);
     if(k>0)g2.append(svgEl('line',{class:'cv-colline',x1:x+L(run),x2:x+L(run),y1:y,y2:y+h}));run+=U(c.width);
    });
    if(head+row*2.2<=h){const more=svgEl('svg',{x,y:y+head+row,width:w,height:h-head-row});more.append(svgEl('text',{class:'cv-text cv-sample',x:6,y:row*0.7,'font-size':Math.max(8,fs*0.85)},'…… 明细行向下延续，超过一页续页并承前过次，末页合计'));g2.append(more);}
    tag+='明细表';
   }else if(e.type==='barcode'||e.type==='qrcode'){
    g2.append(svgEl('rect',{class:'cv-box',x,y,width:w,height:h}));drawCode(g2,e,x,y,w,h,p,lab);
    tag+=e.type==='qrcode'?'二维码':'条码';
   }else if(e.type==='image'){
    g2.append(svgEl('rect',{class:'cv-box',x,y,width:w,height:h}));drawImage(g2,e,x,y,w,h);
    tag+='图片';
   }else{
    const fs=e.font_size*25.4/72*p,clip=svgEl('svg',{x,y,width:w,height:h}),text=e.source==='literal'||e.source==='page'?e.text:e.source==='issue_number'?'〔出具编号〕':(e.source==='group_sum'?'〔合计〕':'〔')+(lab[e.field]||e.field)+(e.source==='group_sum'?'':'〕');
    g2.append(svgEl('rect',{class:'cv-box',x,y,width:w,height:h}));
    clip.append(svgEl('text',{class:'cv-text','font-weight':e.bold?'700':'400',x:e.align==='left'?0:e.align==='center'?w/2:w,y:1.16*fs,'font-size':fs,'text-anchor':e.align==='left'?'start':e.align==='center'?'middle':'end'},text));
    if(e.wrap)clip.append(svgEl('text',{class:'cv-wrap',x:w-2,y:Math.max(10,h-3),'text-anchor':'end'},'↵'));
    g2.append(clip);tag+=sources2[e.source]+(e.wrap?' · 换行':'');
   }
   layers.items.append(g2);
   layers.tags.append(svgEl('text',{class:'cv-tag',x:x+1,y:Math.max(y-3,RULER+9)},tag));
  }
  return edges;
 }
 function draw(view,extra){
  const d=view||config;extra=extra||{};
  const active=document.activeElement,focusKey=focusAfter||(active&&host&&host.contains(active)&&active.getAttribute?(active.getAttribute('data-key')||(active.hasAttribute('data-col')?'col:'+active.getAttribute('data-col'):active.hasAttribute('data-band')?'band:'+active.getAttribute('data-band'):active.hasAttribute('data-end')?'end:'+active.getAttribute('data-end'):null)):null);focusAfter=null;
  // A 2.0 template has a gutter right of the page where each band is named: the labels never lie over an element.
  const p=ppm(),W=Math.round(RULER+210*p+PAD+(d&&banded(d)?GUTTER:0)),H=Math.round(RULER+297*p+PAD),X=u=>RULER+u/10*p,L=u=>u/10*p;
  svg.setAttribute('width',W);svg.setAttribute('height',H);svg.setAttribute('viewBox','0 0 '+W+' '+H);
  for(const name of Object.keys(layers))layers[name].replaceChildren();
  layers.rulers.append(...rulers(p,W,H));
  layers.page.append(svgEl('rect',{class:'cv-paper',x:RULER,y:RULER,width:210*p,height:297*p}));
  if(!d){svg.setAttribute('aria-label','A4 文档画布：尚无模板');if(modeLine)modeLine.textContent=blank;return;}
  svg.setAttribute('aria-label','A4 文档画布，单位毫米');
  const two=banded(d);
  layers.page.append(svgEl('rect',{class:'cv-strip',x:X(100),y:X(100),width:L(1900),height:L(80)}),svgEl('text',{class:'cv-strip-text',x:X(100)+6,y:X(100)+Math.min(L(80)-3,13)},'横幅 / 出具印章保留区（10–18 毫米，不可选）'),svgEl('rect',{class:'cv-safe',x:X(100),y:X(250),width:L(1900),height:L(2450)}),svgEl('text',{class:'cv-safe-text',x:X(2000)-4,y:two?X(250)-4:X(2700)-4,'text-anchor':'end'},'安全区 x 10–200 · y 25–270'));
  const print=d.print||{mode:'full',offset_x:0,offset_y:0},ox=U(print.offset_x),oy=U(print.offset_y),lab=labels(),order=readingOrder(d),bad=new Set(extra.bad||[]);
  let edges=[];if(two)edges=drawBands(d,extra,p,X,L,order,bad,lab);else drawBoxes(d,extra,p,X,L,order,bad,lab);
  // The column a refusal names (S03-J): outlined over the table, whatever the pointer does; the band a refusal names (2.0) likewise.
  const t=tableOf(d),tk=tableKey(d),tr=t?rectOf(items(d).find(i=>i.key===tk)):null,tx=t?(two?BAND_X:0)+U(t.x):0;
  if(t&&marked&&marked.column){
   const at=t.columns.findIndex(c=>c.field===marked.column);
   if(at>=0){const before=t.columns.slice(0,at).reduce((s,c)=>s+U(c.width),0);layers.overlay.append(svgEl('rect',{class:'cv-refused-col','data-refused':marked.column,x:X(tx+before),y:X(tr.y),width:L(U(t.columns[at].width)),height:L(tr.h),'aria-hidden':'true'}));}
  }
  // Column boundaries of the table: always grabbable with the pointer, focusable once the table is selected.
  if(t){
   const y=X(tr.y),h=L(tr.h);let run=0;
   t.columns.slice(0,-1).forEach((c,k)=>{
    run+=U(c.width);const bx=X(tx)+L(run),mm=M(U(t.x)+run);
    layers.overlay.append(svgEl('rect',{class:'cv-col'+(bad.has(tk)?' cv-bad':''),'data-col':k,x:bx-4,y,width:8,height:h,tabindex:selection.includes(tk)?'0':'-1',role:'slider','aria-orientation':'horizontal','aria-label':'明细表第 '+(k+1)+' 列与第 '+(k+2)+' 列的边界','aria-valuemin':fmt(M(U(t.x))),'aria-valuemax':fmt(M(U(t.x)+U(t.width))),'aria-valuenow':fmt(mm),'aria-valuetext':'x '+fmt(mm)+' 毫米，左列宽 '+fmt(M(U(c.width)))+' 毫米'}));
   });
  }
  const chosen=order.filter(i=>selection.includes(i.key));
  if(chosen.length===1){
   const i=chosen[0],r=rectOf(i),l=X(r.x),tp=X(r.y),rt=l+L(r.w),b=tp+L(r.h),cx=(l+rt)/2,cy=(tp+b)/2;
   if(two&&i.ref.type==='line'){
    const e=i.ref;
    [[1,X(BAND_X+U(e.x1)),X(i.top+U(e.y1))],[2,X(BAND_X+U(e.x2)),X(i.top+U(e.y2))]].forEach(([n,hx,hy])=>layers.overlay.append(svgEl('rect',{class:'cv-handle cv-end','data-end':n,x:hx-HANDLE/2,y:hy-HANDLE/2,width:HANDLE,height:HANDLE,tabindex:'0',role:'button','aria-label':'线 '+e.id+' 的'+(n===1?'起点':'终点')+'：拖动或用方向键移动'})));
   }else{
    const dirs=two&&i.table?[['e',rt,cy],['w',l,cy]]:[['nw',l,tp],['n',cx,tp],['ne',rt,tp],['e',rt,cy],['se',rt,b],['s',cx,b],['sw',l,b],['w',l,cy]];
    for(const [dir,hx,hy] of dirs)layers.overlay.append(svgEl('rect',{class:'cv-handle cv-h-'+dir,'data-dir':dir,x:hx-HANDLE/2,y:hy-HANDLE/2,width:HANDLE,height:HANDLE}));
   }
  }else if(chosen.length>1){
   const b=bounds(d,selection);layers.overlay.append(svgEl('rect',{class:'cv-bbox',x:X(b.l),y:X(b.t),width:L(b.r-b.l),height:L(b.b-b.t)}));
  }
  layers.overlay.append(...edges);
  for(const u of (extra.guides&&extra.guides.x)||[])layers.guides.append(svgEl('line',{class:'cv-guide',x1:X(u),x2:X(u),y1:RULER,y2:RULER+297*p}));
  for(const u of (extra.guides&&extra.guides.y)||[])layers.guides.append(svgEl('line',{class:'cv-guide',x1:RULER,x2:RULER+210*p,y1:X(u),y2:X(u)}));
  if(extra.readout)layers.readout.append(svgEl('text',{class:'cv-readout'+(extra.message?' cv-bad':''),x:X(extra.readout.u),y:Math.max(X(extra.readout.v)-16,RULER+10)},extra.readout.text+(extra.message?'（将被拒绝）':'')));
  if(modeLine)modeLine.textContent=two?d.schema_version+' 分带模板：各带自上而下叠放在安全区内，明细带随页面伸展，元素位置相对于所在带 · '+(d.group?'分组 '+d.group.fields.join('、'):'无分组（整个快照一份文档）')+' · 画布显示版面，不显示数据，PDF 预览为准':(print.mode==='variables'?'变量层：只打印行字段、合计与明细表，固定文字灰显且不打印':'完整文本：打印全部文本框与明细表')+' · 整体偏移 '+fmt(print.offset_x)+' / '+fmt(print.offset_y)+' 毫米'+(ox||oy?'（虚线为偏移后的打印位置）':'')+' · 画布显示版面，不显示底稿与数据，PDF 预览为准';
  if(focusKey&&host){const target=focusKey.startsWith('col:')?svg.querySelector('[data-col="'+focusKey.slice(4)+'"]'):focusKey.startsWith('band:')?svg.querySelector('.cv-bk[data-band="'+focusKey.slice(5)+'"]'):focusKey.startsWith('end:')?svg.querySelector('[data-end="'+focusKey.slice(4)+'"]'):svg.querySelector('[data-key="'+focusKey+'"]');if(target&&target!==document.activeElement&&target.focus)target.focus({preventScroll:true});}
 }

 // ---- selection, toolbar, edits -------------------------------------------------------------------------
 const buttons=['doc-undo','doc-redo','doc-dup','doc-del','doc-align-left','doc-align-center','doc-align-right','doc-align-top','doc-align-middle','doc-align-bottom','doc-distribute-h','doc-distribute-v'];
 function syncToolbar(){
  const n=selection.length,tk=config?tableKey(config):'table',boxes=config?selection.filter(k=>k!==tk).length:0,set=(id,off)=>{const b=$(id);if(b)b.disabled=!!off;};
  set('doc-undo',!config||!ports.canUndo());set('doc-redo',!config||!ports.canRedo());set('doc-dup',!config||!boxes);set('doc-del',!config||!n);
  for(const id of buttons.slice(4,10))set(id,!config||n<2);for(const id of buttons.slice(10))set(id,!config||n<3);
  const z=$('doc-zoom-value');if(z)z.textContent=zoom+' %';
 }
 // The designer shows the properties of the selected element: it is told whenever the selection is another.
 let notified='';
 function notify(){const s=selection.join(',');if(s===notified)return;notified=s;if(ports.selected)ports.selected([...selection]);}
 function select(keys,message){
  const known=config?items(config).map(i=>i.key):[];selection=keys.filter((k,n)=>known.includes(k)&&keys.indexOf(k)===n);
  draw();syncToolbar();announce(message||selectionText());notify();
 }
 function commit(d,label,afterSelect){
  settle(d);
  const result=ports.edit(d,label);
  if(!result.ok){status('未应用：'+result.message+' 项目已恢复原位。');draw();return false;}
  if(afterSelect)selection=afterSelect;
  draw();syncToolbar();status('已'+label+'。可用 Ctrl/Cmd+Z 撤销。');if(afterSelect){announce(selectionText());notify();}
  return true;
 }
 function attempt(fn,label){
  if(!config)return;
  try{const out=fn(clone(config));if(out)commit(out.d||out,label,out.made);}catch(e){status('未应用：'+e.message);}
 }
 const needSelection=keys=>{if(!keys.length)throw Error('请先选择画布上的项目。');return keys;};
 const actions={
  align:mode=>attempt(d=>align(d,selection,mode),'对齐'),
  distribute:axis=>attempt(d=>distribute(d,selection,axis),'等距分布'),
  remove:()=>attempt(d=>remove(d,needSelection(selection)),'删除'),
  duplicate:()=>attempt(d=>banded(d)?duplicate2(d,needSelection(selection.filter(k=>k!==tableKey(d))),m=>ports.test(m)):duplicate(d,needSelection(selection.filter(k=>k!=='table')),m=>ports.test(m)),'复制'),
 };
 function nudge(dx,dy){
  if(!config||!selection.length)return;
  if(banded(config)&&!dx&&selection.every(k=>k===tableKey(config))){status('明细表紧接在上方各带之后，纵向位置由各带的高度决定，不能微调。');return;}
  attempt(d=>moveBy(d,selection,dx,dy),'微调');
 }
 function nudgeBoundary(index,delta){
  attempt(d=>{const t=tableOf(d);if(!t)return null;const before=t.columns[index].width,w=moveBoundary(d,index,U(before)+delta);if(M(w)===before){status('该列已到最小宽度或相邻列已到最小宽度。');return null;}return d;},'调整列宽');
 }
 // A band's height by the keyboard (2.0): the bottom edge moves by delta (0.1 mm units).
 function nudgeBand(name,delta){
  attempt(d=>{const b=d.bands[name];if(!b)return null;b.height=M(U(b.height)+delta);return d;},'调整带高');
 }
 // One end of the selected line by the keyboard (2.0).
 function nudgeEnd(end,dx,dy){
  attempt(d=>{const e=items(d).find(i=>i.key===selection[0]);if(!e||e.ref.type!=='line')return null;e.ref['x'+end]=M(U(e.ref['x'+end])+dx);e.ref['y'+end]=M(U(e.ref['y'+end])+dy);return d;},'移动端点');
 }

 // ---- pointer ------------------------------------------------------------------------------------------------
 // The page position (0.1 mm units from the page's corner) of a pointer event, to know which band is under it when a drag begins.
 function pagePoint(ev){const r=svg.getBoundingClientRect?svg.getBoundingClientRect():{left:0,top:0};return {u:(ev.clientX-r.left-RULER)/ppm()*10,v:(ev.clientY-r.top-RULER)/ppm()*10};}
 function start(ev,kind,extra){
  drag={kind,id:ev.pointerId,x0:ev.clientX,y0:ev.clientY,py:pagePoint(ev).v,base:clone(config),moved:false,cand:null,message:'',...extra};
  try{if(svg.setPointerCapture&&ev.pointerId!==undefined)svg.setPointerCapture(ev.pointerId);}catch(_){}
 }
 function release(id){try{if(svg.releasePointerCapture&&id!==undefined&&svg.hasPointerCapture&&svg.hasPointerCapture(id))svg.releasePointerCapture(id);}catch(_){}}
 svg.addEventListener('pointerdown',ev=>{
  if(!config||drag||(ev.button!==undefined&&ev.button!==0))return;
  const t=ev.target,found=t&&t.closest?t.closest('[data-dir],[data-col],[data-key],[data-band],[data-end]'):null;
  if(!found){if(!ev.shiftKey&&selection.length)select([],'已清除选择。');if(svg.focus)svg.focus({preventScroll:true});return;}
  ev.preventDefault();
  if(found.hasAttribute('data-dir')){if(selection.length===1)start(ev,'resize',{key:selection[0],dir:found.getAttribute('data-dir')});return;}
  if(found.hasAttribute('data-end')){focusAfter='end:'+found.getAttribute('data-end');if(selection.length===1)start(ev,'lineend',{key:selection[0],end:Number(found.getAttribute('data-end'))});return;}
  if(found.hasAttribute('data-band')){focusAfter='band:'+found.getAttribute('data-band');start(ev,'band',{name:found.getAttribute('data-band')});return;}
  if(found.hasAttribute('data-col')){
   focusAfter='col:'+found.getAttribute('data-col');
   if(!(selection.length===1&&selection[0]===tableKey(config)))select([tableKey(config)]);
   start(ev,'boundary',{index:Number(found.getAttribute('data-col'))});return;
  }
  const key=found.getAttribute('data-key');
  focusAfter=key;
  if(ev.shiftKey){select(selection.includes(key)?selection.filter(k=>k!==key):[...selection,key]);return;}
  const wasIn=selection.includes(key);
  if(wasIn)draw();else select([key]);
  start(ev,'move',{keys:[...selection],key,collapse:wasIn&&selection.length>1});
 });
 svg.addEventListener('pointermove',ev=>{
  if(!drag||(ev.pointerId!==undefined&&drag.id!==undefined&&ev.pointerId!==drag.id))return;
  const px=ev.clientX-drag.x0,py=ev.clientY-drag.y0;
  if(!drag.moved&&Math.hypot(px,py)<DRAG_PIXELS)return;
  drag.moved=true;
  const p=ppm(),dx=Math.round(px/p*10),dy=Math.round(py/p*10),grid=ev.altKey?1:10;
  const two=banded(drag.base),plan=drag.kind==='move'?(two?planMove2(drag.base,drag.keys,dx,dy,grid,drag.key,drag.py):planMove(drag.base,drag.keys,dx,dy,grid)):drag.kind==='resize'?(two?planResize2:planResize)(drag.base,drag.key,drag.dir,dx,dy,grid):drag.kind==='lineend'?planLineEnd(drag.base,drag.key,drag.end,dx,dy,grid):drag.kind==='band'?planBand(drag.base,drag.name,dy,grid):planBoundary(drag.base,drag.index,dx,grid);
  drag.cand=plan.d;drag.message=same(plan.d,drag.base)?'':ports.test(plan.d);
  draw(plan.d,{guides:plan.guides,readout:plan.readout,bad:drag.message?(drag.kind==='boundary'?[tableKey(drag.base)]:drag.kind==='move'?drag.keys:drag.kind==='band'?[]:[drag.key]):null,badBand:drag.message&&drag.kind==='band'?drag.name:null,message:drag.message});
 });
 function finish(ev,cancelled){
  if(!drag)return;
  const g=drag;drag=null;release(g.id);
  if(cancelled||!g.moved){
   if(!cancelled&&g.kind==='move'&&g.collapse)select([g.key]);else draw();
   if(cancelled)status('已取消。');
   return;
  }
  if(!g.cand||same(g.cand,g.base)){draw();return;}
  commit(g.cand,{move:'移动',resize:'调整大小',lineend:'移动端点',band:'调整带高'}[g.kind]||'调整列宽');
 }
 svg.addEventListener('pointerup',ev=>finish(ev,false));
 svg.addEventListener('pointercancel',ev=>finish(ev,true));

 // ---- keyboard ---------------------------------------------------------------------------------------------------
 const visible=()=>!!host&&!host.closest('[hidden]');
 const editing=t=>!!t&&!!t.closest&&!!t.closest('input,textarea,select,[contenteditable="true"]');
 function selectAll(){if(!config)return;const all=readingOrder(config).map(i=>i.key);select(all,'全选：'+all.length+' 项。');}
 if(host)host.addEventListener('keydown',ev=>{
  if(!config||(palette&&palette.open))return;
  const mod=ev.ctrlKey||ev.metaKey,key=ev.key,t=ev.target,k=t&&t.getAttribute?t.getAttribute('data-key'):null,col=t&&t.hasAttribute&&t.hasAttribute('data-col')?Number(t.getAttribute('data-col')):null,band=t&&t.hasAttribute&&t.hasAttribute('data-band')?t.getAttribute('data-band'):null,end=t&&t.hasAttribute&&t.hasAttribute('data-end')?Number(t.getAttribute('data-end')):null;
  if(key==='Escape'){ev.preventDefault();if(drag){finish(ev,true);}else if(selection.length)select([],'已清除选择。');return;}
  if(mod&&key.toLowerCase()==='a'){ev.preventDefault();selectAll();return;}
  if(mod&&key.toLowerCase()==='d'){ev.preventDefault();actions.duplicate();return;}
  if((key==='Enter'||key===' ')&&k){ev.preventDefault();select(ev.shiftKey||mod?(selection.includes(k)?selection.filter(v=>v!==k):[...selection,k]):[k]);return;}
  if((key==='Delete'||key==='Backspace')&&!mod){ev.preventDefault();if(band===null)actions.remove();return;}
  if(key.startsWith('Arrow')&&!mod&&!ev.altKey){
   ev.preventDefault();const step=ev.shiftKey?10:1,dx=key==='ArrowRight'?step:key==='ArrowLeft'?-step:0,dy=key==='ArrowDown'?step:key==='ArrowUp'?-step:0;
   if(col!==null){if(dx)nudgeBoundary(col,dx);return;}
   if(band!==null){if(dy)nudgeBand(band,dy);return;}
   if(end!==null){nudgeEnd(end,dx,dy);return;}
   // An arrow on an item that is not selected selects it first, so that it is the one that moves.
   if(k&&!selection.includes(k)){selection=[k];draw();syncToolbar();announce(selectionText());notify();}
   nudge(dx,dy);
  }
 });
 // Undo, redo and the palette work anywhere on the page while the canvas is showing; a text field keeps its own undo.
 document.addEventListener('keydown',ev=>{
  if(!visible()||!config)return;
  const mod=ev.ctrlKey||ev.metaKey,key=(ev.key||'').toLowerCase();
  if(!mod||(palette&&palette.open))return;
  if(key==='k'&&!ev.shiftKey&&!ev.altKey){ev.preventDefault();openPalette();return;}
  if(editing(ev.target)||ev.altKey)return;
  const redo=(key==='z'&&ev.shiftKey)||key==='y',undo=key==='z'&&!ev.shiftKey;
  if(undo||redo){ev.preventDefault();const b=$(undo?'doc-undo':'doc-redo');if(b&&!b.disabled)b.click();}
 });
 // Toolbar buttons.
 const bind=(id,fn)=>{const b=$(id);if(b)b.addEventListener('click',fn);};
 for(const mode of ['left','center','right','top','middle','bottom'])bind('doc-align-'+mode,()=>actions.align(mode));
 bind('doc-distribute-h',()=>actions.distribute('h'));bind('doc-distribute-v',()=>actions.distribute('v'));
 bind('doc-dup',actions.duplicate);bind('doc-del',actions.remove);bind('doc-palette-open',()=>openPalette());
 const zoomInput=$('doc-zoom');
 if(zoomInput)zoomInput.addEventListener('input',()=>{const v=Math.round(Number(zoomInput.value));if(v>=50&&v<=200){zoom=v;draw();syncToolbar();}});

 // ---- the command palette (Ctrl/Cmd+K) ---------------------------------------------------------------------------
 // Each command is a button of the page; running a command clicks that button, and a command is
 // listed only while its button exists, is not hidden (itself or an ancestor) and is enabled.
 const commands=[
  ['添加固定文字','doc-add-text','add literal text 固定文字 文本'],['添加行字段','doc-add-row','add row field 行字段 字段'],['添加原范围合计','doc-add-total','add total 合计'],
  ['添加或替换明细表','doc-table-add','add table detail 明细表'],['移除明细表','doc-table-remove','remove table 明细表'],
  ['转换为 2.0 分带模板','doc-convert','convert schema 2.0 转换 分带'],
  ['在所选带加固定文字','doc2-add-literal','band add literal text 固定文字 文本 带'],['添加条码到所选带','doc2-add-barcode','band add barcode code 128 条码 带'],['添加二维码到所选带','doc2-add-qrcode','band add qr code 二维码 带'],['添加图片到所选带','doc2-add-image','band add image picture logo 图片 带'],['添加出具编号文字到所选带','doc2-add-number','band add issue number 出具编号 带'],['在所选带加字段文本','doc2-add-field','band add field text 字段 带'],['在所选带加分组合计','doc2-add-sum','band add group sum 分组合计 合计 带'],['在所选带加页码文字','doc2-add-page','band add page text 页码 带'],['在所选带加线','doc2-add-line','band add line 线 带'],['在所选带加矩形','doc2-add-rect','band add rectangle rect 矩形 带'],['添加明细表列','doc2-col-add','add table column 明细表 列'],
  ['左对齐','doc-align-left','align left'],['水平居中对齐','doc-align-center','align center'],['右对齐','doc-align-right','align right'],['顶对齐','doc-align-top','align top'],['垂直居中对齐','doc-align-middle','align middle'],['底对齐','doc-align-bottom','align bottom'],
  ['水平等距分布','doc-distribute-h','distribute horizontally 等距'],['垂直等距分布','doc-distribute-v','distribute vertically 等距'],
  ['复制所选文本框','doc-dup','duplicate copy'],['删除所选项目','doc-del','delete remove'],
  ['撤销','doc-undo','undo'],['重做','doc-redo','redo'],
  ['生成PDF预览','doc-pdf','pdf preview 预览'],['检查样本','doc-check','check samples boundary 边界 样本'],
  ['保存模板版本','doc-server-save','save version 保存'],['发布当前保存版本','doc-release-publish','publish 发布'],['打开已发布模板','doc-published-open','open published 已发布'],['正式出具','doc-issue','issue 出具'],
 ];
 const listed=()=>commands.filter(c=>{const b=$(c[1]);return !!b&&!b.hidden&&!b.disabled&&!b.closest('[hidden]');});
 function buildPalette(){
  const backdrop=node('div',undefined,'cv-backdrop'),box=node('div',undefined,'cv-palette'),title=node('h2','命令面板'),input=node('input'),list=node('ul',undefined,'cv-commands'),empty=node('p','没有匹配的命令。','cv-empty'),close=node('button','关闭');
  backdrop.hidden=true;box.setAttribute('role','dialog');box.setAttribute('aria-modal','true');box.id='cv-palette';title.id='cv-palette-title';box.setAttribute('aria-labelledby','cv-palette-title');
  input.type='text';input.id='cv-palette-input';input.setAttribute('role','combobox');input.setAttribute('aria-label','搜索命令');input.setAttribute('aria-autocomplete','list');input.setAttribute('aria-controls','cv-palette-list');input.setAttribute('aria-expanded','true');input.setAttribute('autocomplete','off');input.setAttribute('spellcheck','false');input.placeholder='输入命令，例如：对齐、撤销、检查样本';
  list.id='cv-palette-list';list.setAttribute('role','listbox');list.setAttribute('aria-label','命令');empty.setAttribute('role','status');empty.hidden=true;
  close.type='button';close.className='button secondary small';close.id='cv-palette-close';
  box.append(title,input,list,empty,close);backdrop.append(box);document.body.append(backdrop);
  const p={open:false,backdrop,box,input,list,empty,close,shown:[],index:0,previous:null,inert:false};
  function show(){
   const words=input.value.trim().toLowerCase().split(/\s+/).filter(Boolean);
   p.shown=listed().filter(c=>words.every(w=>(c[0]+' '+c[2]).toLowerCase().includes(w)));p.index=Math.min(p.index,Math.max(0,p.shown.length-1));
   list.replaceChildren();
   p.shown.forEach((c,n)=>{const li=node('li',c[0]);li.id='cv-command-'+n;li.setAttribute('role','option');li.setAttribute('aria-selected',n===p.index?'true':'false');li.addEventListener('click',()=>run(n));li.addEventListener('pointermove',()=>{if(p.index!==n){p.index=n;mark();}});list.append(li);});
   empty.hidden=p.shown.length>0;input.setAttribute('aria-activedescendant',p.shown.length?'cv-command-'+p.index:'');
  }
  function mark(){[...list.children].forEach((li,n)=>li.setAttribute('aria-selected',n===p.index?'true':'false'));input.setAttribute('aria-activedescendant',p.shown.length?'cv-command-'+p.index:'');const li=list.children[p.index];if(li&&li.scrollIntoView)li.scrollIntoView({block:'nearest'});}
  function run(n){const c=p.shown[n];if(!c)return;const b=$(c[1]);closePalette();if(b&&!b.disabled&&!b.hidden)b.click();}
  input.addEventListener('input',()=>{p.index=0;show();});
  box.addEventListener('keydown',ev=>{
   if(ev.key==='Escape'){ev.preventDefault();ev.stopPropagation();closePalette();return;}
   if(ev.key==='ArrowDown'||ev.key==='ArrowUp'){ev.preventDefault();if(p.shown.length){p.index=(p.index+(ev.key==='ArrowDown'?1:p.shown.length-1))%p.shown.length;mark();}return;}
   if(ev.key==='Home'&&ev.target===input&&ev.ctrlKey){p.index=0;mark();return;}
   if(ev.key==='Enter'&&ev.target===input){ev.preventDefault();run(p.index);return;}
   if(ev.key==='Tab'){const order=[input,close];const at=order.indexOf(document.activeElement);ev.preventDefault();order[(at+(ev.shiftKey?order.length-1:1))%order.length].focus();}
  });
  backdrop.addEventListener('pointerdown',ev=>{if(ev.target===backdrop)closePalette();});
  close.addEventListener('click',()=>closePalette());
  p.show=show;return p;
 }
 function openPalette(){
  if(!config||!visible())return;
  if(!palette)palette=buildPalette();
  if(palette.open)return;
  const shell=document.querySelector('.shell');palette.previous=document.activeElement;palette.inert=!!shell&&shell.inert===true;if(shell)shell.inert=true;
  palette.open=true;palette.input.value='';palette.index=0;palette.show();palette.backdrop.hidden=false;palette.input.focus();
 }
 function closePalette(){
  if(!palette||!palette.open)return;
  palette.open=false;palette.backdrop.hidden=true;const shell=document.querySelector('.shell');if(shell)shell.inert=palette.inert;
  const back=palette.previous;palette.previous=null;if(back&&back.isConnected&&back.focus)back.focus({preventScroll:true});
 }

 // ---- what the designer calls ------------------------------------------------------------------------------------------
 draw(null);syncToolbar();
 return {
  // Called with the designer's configuration (or null) after every change, undo, redo and open.
  show(c){
   config=c;drag=null;const before=selection.join(',');
   const known=c?items(c).map(i=>i.key):[];selection=selection.filter(k=>known.includes(k));
   if(c&&statusLine&&statusLine.textContent===idle)status('画布显示当前模板；点击项目开始编辑，Ctrl/Cmd+K 打开命令面板。');
   draw();syncToolbar();if(before!==selection.join(','))announce(selectionText());notify();
  },
  clear(){config=null;selection=[];drag=null;marked=null;closePalette();draw(null);syncToolbar();status(idle);announce('未选择任何项目。');notify();},
  // The picture of an image has arrived: drawn again, with the selection and the focus where they were.
  refresh(){if(config&&!drag)draw();},
  // The band, element or column a refusal names, outlined until the template changes; null clears it.
  mark(detail){
   const next=detail&&(detail.element||detail.column||detail.band)?{element:detail.element||'',column:detail.column||'',band:detail.band||''}:null;
   if(JSON.stringify(next)===JSON.stringify(marked))return;
   marked=next;draw();
   if(marked&&config){const t=tableOf(config),known=items(config).some(i=>i.key===marked.element)||(!!t&&t.columns.some(c=>c.field===marked.column))||(banded(config)&&!!config.bands[marked.band]);if(known)status('预览或检查指出的位置已描出轮廓'+(marked.band?'：'+bandLabels[marked.band]:'')+(marked.element?(marked.band?'，':'：')+marked.element:'')+(marked.column?'，列 '+marked.column:'')+'。');}
  },
  marked:()=>marked?{...marked}:null,
  // A template that is opened or reset is another page: nothing stays selected.
  reset(){selection=[];drag=null;announce('未选择任何项目。');notify();},
  selection:()=>[...selection],
  select,
  zoom:()=>zoom,
  scale:SCALE,
  origin:{x:RULER,y:RULER},
  palette:{open:openPalette,close:closePalette,isOpen:()=>!!palette&&palette.open},
  ops:{items,moveBy,resizeTo,align,distribute,remove,duplicate,duplicate2,moveBoundary,snapAxis,settle,planMove,planResize,planBoundary,planMove2,planResize2,planLineEnd,planBand,geo,scaleColumns},
 };
};
