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
// Drags use pointer deltas only, never absolute positions.
window.createReportingCanvas=function(ports){
 const NS='http://www.w3.org/2000/svg',SCALE=3,RULER=24,PAD=16,HANDLE=8,TOLERANCE=10,DRAG_PIXELS=3;
 const $=id=>document.getElementById(id);
 const U=mm=>Math.round(mm*10),M=u=>u/10,clone=v=>JSON.parse(JSON.stringify(v)),same=(a,b)=>JSON.stringify(a)===JSON.stringify(b);
 const svgEl=(tag,attrs,text)=>{const e=document.createElementNS(NS,tag);for(const k of Object.keys(attrs||{}))e.setAttribute(k,String(attrs[k]));if(text!==undefined)e.textContent=text;return e;};
 const node=(tag,text,className)=>{const e=document.createElement(tag);if(text!==undefined)e.textContent=text;if(className)e.className=className;return e;};
 const sources={literal:'固定文字',row:'行字段',total:'合计'};

 // ---- the model: pure operations on a configuration, in 0.1 mm units ------------------------------
 const items=d=>[...d.elements.map(e=>({key:e.id,table:false,ref:e})),...(d.table?[{key:'table',table:true,ref:d.table}]:[])];
 const rectOf=i=>({x:U(i.ref.x),y:U(i.ref.y),w:U(i.ref.width),h:U(i.ref.height)});
 const overlap=(a,b)=>a.x<b.x+b.w&&b.x<a.x+a.w&&a.y<b.y+b.h&&b.y<a.y+a.h;
 // The table's columns scale in proportion to its width, each rounded to 0.1 mm, the remainder on the last.
 function setRect(i,r){
  const t=i.ref;
  if(i.table&&r.w!==U(t.width)){const old=U(t.width),n=t.columns.length;let used=0;t.columns.forEach((c,k)=>{const w=k===n-1?r.w-used:Math.round(U(c.width)*r.w/old);c.width=M(w);used+=w;});}
  t.x=M(r.x);t.y=M(r.y);t.width=M(r.w);t.height=M(r.h);
 }
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
 function remove(d,keys){d.elements=d.elements.filter(e=>!keys.includes(e.id));if(keys.includes('table')&&d.table){d.schema_version=d.background?'1.2':'1.1';delete d.table;}return d;}
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
 const columnMinimum=(d,i)=>U(ports.columnMinimum(d.table.font_size,i));
 // Moves the boundary after column index so that the column to its left is `left` wide, giving
 // to or taking from the column to the right; each column keeps its minimum. Returns the width used.
 function moveBoundary(d,index,left){
  const c=d.table.columns,pair=U(c[index].width)+U(c[index+1].width),w=Math.max(columnMinimum(d,index),Math.min(pair-columnMinimum(d,index+1),left));
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
  const t=base.table,before=t.columns.slice(0,index).reduce((s,c)=>s+U(c.width),0),at=U(t.x)+before+U(t.columns[index].width),s=snapAxis([at+dx],targets(base,['table'],'x'),grid),want=Math.round(at+dx+s.adj),d=clone(base),w=moveBoundary(d,index,want-U(t.x)-before);
  return {d,guides:{x:U(t.x)+before+w===want?s.guides:[],y:[]},readout:{u:U(t.x)+before+w,v:U(t.y),text:'第 '+(index+1)+' 列 '+M(w)+' 毫米'}};
 }

 // ---- state and elements ----------------------------------------------------------------------------
 let config=null,selection=[],zoom=100,drag=null,palette=null,focusAfter=null,marked=null;
 const host=$('doc-canvas-host'),statusLine=$('doc-canvas-status'),live=$('doc-canvas-live'),modeLine=$('doc-canvas-mode'),scroll=$('doc-canvas-scroll'),note=$('doc-canvas-note');
 // A template the canvas cannot show (2.0, S03-J) gets a short note in place of the page.
 function setNote(text){if(scroll)scroll.hidden=!!text;if(note){note.hidden=!text;note.textContent=text||'';}}
 const svg=svgEl('svg',{class:'cv-svg',role:'group','aria-label':'A4 文档画布，单位毫米','aria-describedby':'doc-canvas-help',tabindex:'-1'});
 const layers={};for(const name of ['rulers','page','items','overlay','guides','tags','readout']){layers[name]=svgEl('g',{class:'cv-'+name});svg.append(layers[name]);}
 layers.tags.setAttribute('aria-hidden','true');layers.guides.setAttribute('aria-hidden','true');layers.readout.setAttribute('aria-hidden','true');layers.rulers.setAttribute('aria-hidden','true');
 if(host)host.append(svg);
 const ppm=()=>SCALE*zoom/100;
 const fmt=n=>String(n);
 const labels=()=>{const m={};for(const c of ports.columns())m[c.id]=c.label;return m;};
 function nameOf(i,lab){
  const r=i.ref,pos='，x '+fmt(r.x)+' 毫米，y '+fmt(r.y)+' 毫米，宽 '+fmt(r.width)+' 毫米，高 '+fmt(r.height)+' 毫米';
  if(i.table)return '明细表，列 '+r.columns.map(c=>lab[c.field]||c.field).join('、')+'，字号 '+r.font_size+' 点'+pos;
  return r.id+'，'+sources[r.source]+(r.source==='literal'?' “'+r.text+'”':' '+(lab[r.field]||r.field))+'，字号 '+r.font_size+' 点'+pos;
 }
 const idle='先查询当前授权数据，画布随模板显示。',blank='画布显示版面，不显示底稿与数据，PDF 预览为准。';
 const status=text=>{if(statusLine)statusLine.textContent=text;};
 const announce=text=>{if(live)live.textContent=text;};
 function selectionText(){
  if(!config||!selection.length)return '未选择任何项目。';
  const lab=labels(),its=items(config).filter(i=>selection.includes(i.key));
  return its.length===1?'已选择 '+nameOf(its[0],lab)+'。':'已选择 '+its.length+' 项：'+its.map(i=>i.key==='table'?'明细表':i.key).join('、')+'。';
 }

 // ---- drawing -------------------------------------------------------------------------------------------
 function readingOrder(d){return items(d).sort((a,b)=>U(a.ref.y)-U(b.ref.y)||U(a.ref.x)-U(b.ref.x)||(a.table?1:0)-(b.table?1:0));}
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
 function draw(view,extra){
  const d=view||config;extra=extra||{};
  const active=document.activeElement,focusKey=focusAfter||(active&&host&&host.contains(active)&&active.getAttribute?(active.getAttribute('data-key')||(active.hasAttribute('data-col')?'col:'+active.getAttribute('data-col'):null)):null);focusAfter=null;
  const p=ppm(),W=Math.round(RULER+210*p+PAD),H=Math.round(RULER+297*p+PAD),X=u=>RULER+u/10*p,L=u=>u/10*p;
  svg.setAttribute('width',W);svg.setAttribute('height',H);svg.setAttribute('viewBox','0 0 '+W+' '+H);
  for(const name of Object.keys(layers))layers[name].replaceChildren();
  layers.rulers.append(...rulers(p,W,H));
  layers.page.append(svgEl('rect',{class:'cv-paper',x:RULER,y:RULER,width:210*p,height:297*p}));
  if(!d){svg.setAttribute('aria-label','A4 文档画布：尚无模板');if(modeLine)modeLine.textContent=blank;return;}
  svg.setAttribute('aria-label','A4 文档画布，单位毫米');
  layers.page.append(svgEl('rect',{class:'cv-strip',x:X(100),y:X(100),width:L(1900),height:L(80)}),svgEl('text',{class:'cv-strip-text',x:X(100)+6,y:X(100)+Math.min(L(80)-3,13)},'横幅 / 出具印章保留区（10–18 毫米，不可选）'),svgEl('rect',{class:'cv-safe',x:X(100),y:X(250),width:L(1900),height:L(2450)}),svgEl('text',{class:'cv-safe-text',x:X(2000)-4,y:X(2700)-4,'text-anchor':'end'},'安全区 x 10–200 · y 25–270'));
  const print=d.print||{mode:'full',offset_x:0,offset_y:0},ox=U(print.offset_x),oy=U(print.offset_y),lab=labels(),order=readingOrder(d),bad=new Set(extra.bad||[]);
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
  // The column a refusal names (S03-J): outlined over the table, whatever the pointer does.
  if(d.table&&marked&&marked.column){
   const t=d.table,at=t.columns.findIndex(c=>c.field===marked.column);
   if(at>=0){const before=t.columns.slice(0,at).reduce((s,c)=>s+U(c.width),0);layers.overlay.append(svgEl('rect',{class:'cv-refused-col','data-refused':marked.column,x:X(U(t.x)+before),y:X(U(t.y)),width:L(U(t.columns[at].width)),height:L(U(t.height)),'aria-hidden':'true'}));}
  }
  // Column boundaries of the table: always grabbable with the pointer, focusable once the table is selected.
  if(d.table){
   const t=d.table,x=X(U(t.x)),y=X(U(t.y)),h=L(U(t.height));let run=0;
   t.columns.slice(0,-1).forEach((c,k)=>{
    run+=U(c.width);const bx=x+L(run),mm=M(U(t.x)+run);
    layers.overlay.append(svgEl('rect',{class:'cv-col'+(bad.has('table')?' cv-bad':''),'data-col':k,x:bx-4,y,width:8,height:h,tabindex:selection.includes('table')?'0':'-1',role:'slider','aria-orientation':'horizontal','aria-label':'明细表第 '+(k+1)+' 列与第 '+(k+2)+' 列的边界','aria-valuemin':fmt(M(U(t.x))),'aria-valuemax':fmt(M(U(t.x)+U(t.width))),'aria-valuenow':fmt(mm),'aria-valuetext':'x '+fmt(mm)+' 毫米，左列宽 '+fmt(M(U(c.width)))+' 毫米'}));
   });
  }
  const chosen=order.filter(i=>selection.includes(i.key));
  if(chosen.length===1){
   const r=rectOf(chosen[0]),l=X(r.x),t=X(r.y),rt=l+L(r.w),b=t+L(r.h),cx=(l+rt)/2,cy=(t+b)/2;
   for(const [dir,hx,hy] of [['nw',l,t],['n',cx,t],['ne',rt,t],['e',rt,cy],['se',rt,b],['s',cx,b],['sw',l,b],['w',l,cy]])layers.overlay.append(svgEl('rect',{class:'cv-handle cv-h-'+dir,'data-dir':dir,x:hx-HANDLE/2,y:hy-HANDLE/2,width:HANDLE,height:HANDLE}));
  }else if(chosen.length>1){
   const b=bounds(d,selection);layers.overlay.append(svgEl('rect',{class:'cv-bbox',x:X(b.l),y:X(b.t),width:L(b.r-b.l),height:L(b.b-b.t)}));
  }
  for(const u of (extra.guides&&extra.guides.x)||[])layers.guides.append(svgEl('line',{class:'cv-guide',x1:X(u),x2:X(u),y1:RULER,y2:RULER+297*p}));
  for(const u of (extra.guides&&extra.guides.y)||[])layers.guides.append(svgEl('line',{class:'cv-guide',x1:RULER,x2:RULER+210*p,y1:X(u),y2:X(u)}));
  if(extra.readout)layers.readout.append(svgEl('text',{class:'cv-readout'+(extra.message?' cv-bad':''),x:X(extra.readout.u),y:Math.max(X(extra.readout.v)-16,RULER+10)},extra.readout.text+(extra.message?'（将被拒绝）':'')));
  if(modeLine)modeLine.textContent=(print.mode==='variables'?'变量层：只打印行字段、合计与明细表，固定文字灰显且不打印':'完整文本：打印全部文本框与明细表')+' · 整体偏移 '+fmt(print.offset_x)+' / '+fmt(print.offset_y)+' 毫米'+(ox||oy?'（虚线为偏移后的打印位置）':'')+' · 画布显示版面，不显示底稿与数据，PDF 预览为准';
  if(focusKey&&host){const target=focusKey.startsWith('col:')?svg.querySelector('[data-col="'+focusKey.slice(4)+'"]'):svg.querySelector('[data-key="'+focusKey+'"]');if(target&&target!==document.activeElement&&target.focus)target.focus({preventScroll:true});}
 }

 // ---- selection, toolbar, edits -------------------------------------------------------------------------
 const buttons=['doc-undo','doc-redo','doc-dup','doc-del','doc-align-left','doc-align-center','doc-align-right','doc-align-top','doc-align-middle','doc-align-bottom','doc-distribute-h','doc-distribute-v'];
 function syncToolbar(){
  const n=selection.length,boxes=config?selection.filter(k=>k!=='table').length:0,set=(id,off)=>{const b=$(id);if(b)b.disabled=!!off;};
  set('doc-undo',!config||!ports.canUndo());set('doc-redo',!config||!ports.canRedo());set('doc-dup',!config||!boxes);set('doc-del',!config||!n);
  for(const id of buttons.slice(4,10))set(id,!config||n<2);for(const id of buttons.slice(10))set(id,!config||n<3);
  const z=$('doc-zoom-value');if(z)z.textContent=zoom+' %';
 }
 function select(keys,message){
  const known=config?items(config).map(i=>i.key):[];selection=keys.filter((k,n)=>known.includes(k)&&keys.indexOf(k)===n);
  draw();syncToolbar();announce(message||selectionText());
 }
 function commit(d,label,afterSelect){
  settle(d);
  const result=ports.edit(d,label);
  if(!result.ok){status('未应用：'+result.message+' 项目已恢复原位。');draw();return false;}
  if(afterSelect)selection=afterSelect;
  draw();syncToolbar();status('已'+label+'。可用 Ctrl/Cmd+Z 撤销。');if(afterSelect)announce(selectionText());
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
  duplicate:()=>attempt(d=>duplicate(d,needSelection(selection.filter(k=>k!=='table')),m=>ports.test(m)),'复制'),
 };
 function nudge(dx,dy){
  if(!config||!selection.length)return;
  attempt(d=>moveBy(d,selection,dx,dy),'微调');
 }
 function nudgeBoundary(index,delta){
  attempt(d=>{const t=d.table;if(!t)return null;const before=t.columns[index].width,w=moveBoundary(d,index,U(before)+delta);if(M(w)===before){status('该列已到最小宽度或相邻列已到最小宽度。');return null;}return d;},'调整列宽');
 }

 // ---- pointer ------------------------------------------------------------------------------------------------
 function start(ev,kind,extra){
  drag={kind,id:ev.pointerId,x0:ev.clientX,y0:ev.clientY,base:clone(config),moved:false,cand:null,message:'',...extra};
  try{if(svg.setPointerCapture&&ev.pointerId!==undefined)svg.setPointerCapture(ev.pointerId);}catch(_){}
 }
 function release(id){try{if(svg.releasePointerCapture&&id!==undefined&&svg.hasPointerCapture&&svg.hasPointerCapture(id))svg.releasePointerCapture(id);}catch(_){}}
 svg.addEventListener('pointerdown',ev=>{
  if(!config||drag||(ev.button!==undefined&&ev.button!==0))return;
  const t=ev.target,found=t&&t.closest?t.closest('[data-dir],[data-col],[data-key]'):null;
  if(!found){if(!ev.shiftKey&&selection.length)select([],'已清除选择。');if(svg.focus)svg.focus({preventScroll:true});return;}
  ev.preventDefault();
  if(found.hasAttribute('data-dir')){if(selection.length===1)start(ev,'resize',{key:selection[0],dir:found.getAttribute('data-dir')});return;}
  if(found.hasAttribute('data-col')){
   focusAfter='col:'+found.getAttribute('data-col');
   if(!(selection.length===1&&selection[0]==='table'))select(['table']);
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
  const plan=drag.kind==='move'?planMove(drag.base,drag.keys,dx,dy,grid):drag.kind==='resize'?planResize(drag.base,drag.key,drag.dir,dx,dy,grid):planBoundary(drag.base,drag.index,dx,grid);
  drag.cand=plan.d;drag.message=same(plan.d,drag.base)?'':ports.test(plan.d);
  draw(plan.d,{guides:plan.guides,readout:plan.readout,bad:drag.message?(drag.kind==='boundary'?['table']:drag.kind==='move'?drag.keys:[drag.key]):null,message:drag.message});
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
  commit(g.cand,g.kind==='move'?'移动':g.kind==='resize'?'调整大小':'调整列宽');
 }
 svg.addEventListener('pointerup',ev=>finish(ev,false));
 svg.addEventListener('pointercancel',ev=>finish(ev,true));

 // ---- keyboard ---------------------------------------------------------------------------------------------------
 const visible=()=>!!host&&!host.closest('[hidden]');
 const editing=t=>!!t&&!!t.closest&&!!t.closest('input,textarea,select,[contenteditable="true"]');
 function selectAll(){if(!config)return;const all=readingOrder(config).map(i=>i.key);select(all,'全选：'+all.length+' 项。');}
 if(host)host.addEventListener('keydown',ev=>{
  if(!config||(palette&&palette.open))return;
  const mod=ev.ctrlKey||ev.metaKey,key=ev.key,t=ev.target,k=t&&t.getAttribute?t.getAttribute('data-key'):null,col=t&&t.hasAttribute&&t.hasAttribute('data-col')?Number(t.getAttribute('data-col')):null;
  if(key==='Escape'){ev.preventDefault();if(drag){finish(ev,true);}else if(selection.length)select([],'已清除选择。');return;}
  if(mod&&key.toLowerCase()==='a'){ev.preventDefault();selectAll();return;}
  if(mod&&key.toLowerCase()==='d'){ev.preventDefault();actions.duplicate();return;}
  if((key==='Enter'||key===' ')&&k){ev.preventDefault();select(ev.shiftKey||mod?(selection.includes(k)?selection.filter(v=>v!==k):[...selection,k]):[k]);return;}
  if((key==='Delete'||key==='Backspace')&&!mod){ev.preventDefault();actions.remove();return;}
  if(key.startsWith('Arrow')&&!mod&&!ev.altKey){
   ev.preventDefault();const step=ev.shiftKey?10:1,dx=key==='ArrowRight'?step:key==='ArrowLeft'?-step:0,dy=key==='ArrowDown'?step:key==='ArrowUp'?-step:0;
   if(col!==null){if(dx)nudgeBoundary(col,dx);return;}
   // An arrow on an item that is not selected selects it first, so that it is the one that moves.
   if(k&&!selection.includes(k)){selection=[k];draw();syncToolbar();announce(selectionText());}
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
   setNote('');config=c;drag=null;const before=selection.join(',');
   const known=c?items(c).map(i=>i.key):[];selection=selection.filter(k=>known.includes(k));
   if(c&&statusLine&&statusLine.textContent===idle)status('画布显示当前模板；点击项目开始编辑，Ctrl/Cmd+K 打开命令面板。');
   draw();syncToolbar();if(before!==selection.join(','))announce(selectionText());
  },
  clear(){setNote('');config=null;selection=[];drag=null;marked=null;closePalette();draw(null);syncToolbar();status(idle);announce('未选择任何项目。');},
  // A template of another form than the canvas draws (2.0): the canvas shows a note and nothing can be selected or edited.
  unsupported(text){config=null;selection=[];drag=null;marked=null;closePalette();draw(null);syncToolbar();setNote(text);status(text);announce(text);},
  // The element or column a refusal names, outlined until the template changes; null clears it.
  mark(detail){
   const next=detail&&(detail.element||detail.column)?{element:detail.element||'',column:detail.column||''}:null;
   if(JSON.stringify(next)===JSON.stringify(marked))return;
   marked=next;draw();
   if(marked&&config){const known=items(config).some(i=>i.key===marked.element)||(config.table&&config.table.columns.some(c=>c.field===marked.column));if(known)status('预览或检查指出的位置已描出轮廓'+(marked.element?'：'+marked.element:'')+(marked.column?'，列 '+marked.column:'')+'。');}
  },
  marked:()=>marked?{...marked}:null,
  // A template that is opened or reset is another page: nothing stays selected.
  reset(){selection=[];drag=null;announce('未选择任何项目。');},
  selection:()=>[...selection],
  select,
  zoom:()=>zoom,
  scale:SCALE,
  origin:{x:RULER,y:RULER},
  palette:{open:openPalette,close:closePalette,isOpen:()=>!!palette&&palette.open},
  ops:{items,moveBy,resizeTo,align,distribute,remove,duplicate,moveBoundary,snapAxis,settle,planMove,planResize,planBoundary},
 };
};
