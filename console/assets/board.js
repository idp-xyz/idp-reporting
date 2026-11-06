'use strict';
// The dashboard studio (S06-C, ADR 0008 §4; docs/STUDIO.md "看板工作室"): the document studio's regions (topbar, tool rail, side panel, work area,
// inspector, status bar, dialogs) laid around the dashboard's existing controls. dashboard.js keeps every function and every id; this layer only
// switches panels and tabs, derives the state badge from the dashboard's own state, mirrors its texts into the regions, proxies the cards' buttons
// in the structure panel, runs the publish confirmation and the help dialog, and opens the drawers at narrow widths. It writes no markup and no
// style attribute, loads nothing and stores nothing. In the embed page it runs the same way; the controls that open another console page are hidden there.
window.createReportingBoardStudio = function (ports) {
  const $=id=>document.getElementById(id);
  const root=$('page-dashboard');if(!root||!$('bd-studio'))return null;
  const studio=$('bd-studio');
  const qa=sel=>Array.from(root.querySelectorAll(sel));
  const SVG='http://www.w3.org/2000/svg';
  function h(tag,attrs,...children){const n=document.createElement(tag);if(attrs)for(const [k,v] of Object.entries(attrs)){if(v===undefined||v===null||v===false)continue;if(k==='text')n.textContent=v;else if(k==='onclick')n.addEventListener('click',v);else n.setAttribute(k,v===true?'':String(v));}for(const c of children.flat())if(c!==undefined&&c!==null)n.append(c);return n;}
  function icon(name){const s=document.createElementNS(SVG,'svg');s.setAttribute('class','ic');s.setAttribute('aria-hidden','true');s.setAttribute('focusable','false');const u=document.createElementNS(SVG,'use');u.setAttribute('href','#s-'+name);s.append(u);return s;}
  const narrowSidebar=()=>window.matchMedia&&window.matchMedia('(max-width:660px)').matches;
  const narrowInspector=()=>window.matchMedia&&window.matchMedia('(max-width:950px)').matches;

  // ---- the tool rail and the side views ----
  const rail=qa('.rail-tabs [role=tab]'),views=qa('.side-view');
  function showSide(name){
    for(const b of rail){const on=b.dataset.side===name;b.classList.toggle('active',on);b.setAttribute('aria-selected',String(on));b.tabIndex=on?0:-1;}
    for(const v of views){const on=v.dataset.view===name;v.hidden=!on;v.classList.toggle('active',on);}
    $('bd-sidebar').setAttribute('aria-labelledby','bd-rail-'+name);
  }
  for(const b of rail){
    b.addEventListener('click',()=>{showSide(b.dataset.side);if(narrowSidebar())toggleSidebar(true);});
    b.addEventListener('keydown',e=>{const i=rail.indexOf(b);let j=null;if(e.key==='ArrowDown'||e.key==='ArrowRight')j=(i+1)%rail.length;else if(e.key==='ArrowUp'||e.key==='ArrowLeft')j=(i-1+rail.length)%rail.length;else if(e.key==='Home')j=0;else if(e.key==='End')j=rail.length-1;if(j===null)return;e.preventDefault();showSide(rail[j].dataset.side);rail[j].focus();});
  }
  // ---- the inspector's tabs ----
  const tabs=qa('.inspector-tabs [role=tab]');
  function showTab(name){for(const t of tabs){const on=t.dataset.propTab===name;t.classList.toggle('active',on);t.setAttribute('aria-selected',String(on));t.tabIndex=on?0:-1;const panel=$(t.getAttribute('aria-controls'));if(panel)panel.hidden=!on;}}
  for(const t of tabs){
    t.addEventListener('click',()=>showTab(t.dataset.propTab));
    t.addEventListener('keydown',e=>{const i=tabs.indexOf(t);let j=null;if(e.key==='ArrowRight'||e.key==='ArrowDown')j=(i+1)%tabs.length;else if(e.key==='ArrowLeft'||e.key==='ArrowUp')j=(i-1+tabs.length)%tabs.length;else if(e.key==='Home')j=0;else if(e.key==='End')j=tabs.length-1;if(j===null)return;e.preventDefault();showTab(tabs[j].dataset.propTab);tabs[j].focus();});
  }
  // ---- the drawers (narrow widths; the classes do nothing at full width) ----
  function toggleSidebar(force){const el=$('bd-sidebar');const open=force===undefined?!el.classList.contains('open'):!!force;el.classList.toggle('open',open);if(open)$('bd-inspector').classList.remove('open');}
  function toggleInspector(force){const el=$('bd-inspector');const open=force===undefined?!el.classList.contains('open'):!!force;el.classList.toggle('open',open);if(open)$('bd-sidebar').classList.remove('open');if(open&&narrowInspector()){const t=tabs.find(x=>x.classList.contains('active'));if(t)t.focus();}}
  // ---- dialogs: modal, focus-trapped, Esc closes, focus returns ----
  let openDialog=null;
  const focusable=el=>Array.from(el.querySelectorAll('button:not([disabled]),a[href],input:not([disabled]),select:not([disabled]),textarea:not([disabled]),[tabindex]:not([tabindex="-1"])')).filter(e=>!e.hidden&&!e.closest('[hidden]'));
  function open(id,opener){
    close();const overlay=$(id);overlay.hidden=false;studio.inert=true;
    openDialog={overlay,opener:opener||document.activeElement};
    const first=focusable(overlay.querySelector('[role=dialog]'));(first.find(e=>e.dataset.act!=='close')||first[0]||overlay).focus();
    document.addEventListener('keydown',trap,true);
  }
  function close(){if(!openDialog)return;const {overlay,opener}=openDialog;openDialog=null;overlay.hidden=true;studio.inert=false;document.removeEventListener('keydown',trap,true);if(opener&&opener.isConnected&&typeof opener.focus==='function')opener.focus();}
  function trap(e){
    if(!openDialog)return;
    if(e.key==='Escape'){e.preventDefault();e.stopPropagation();close();return;}
    if(e.key!=='Tab')return;
    const list=focusable(openDialog.overlay.querySelector('[role=dialog]'));if(!list.length)return;
    const first=list[0],last=list[list.length-1];
    if(e.shiftKey&&(document.activeElement===first||!openDialog.overlay.contains(document.activeElement))){e.preventDefault();last.focus();}
    else if(!e.shiftKey&&document.activeElement===last){e.preventDefault();first.focus();}
  }
  for(const b of qa('.overlay [data-act=close]'))b.addEventListener('click',close);
  for(const o of qa('.overlay'))o.addEventListener('mousedown',e=>{if(e.target===o)close();});
  // Esc at the studio level closes an open drawer.
  studio.addEventListener('keydown',e=>{if(e.key!=='Escape'||openDialog)return;if($('bd-sidebar').classList.contains('open')){toggleSidebar(false);const t=rail.find(x=>x.classList.contains('active'));if(t)t.focus();}else if($('bd-inspector').classList.contains('open')){toggleInspector(false);$('bd-ctx-inspector').focus();}});

  // ---- what the dashboard says, where the studio shows it ----
  const say=text=>{$('board-status').textContent=text;};
  function badge(s){
    if(!s.result)return ['未查询','neutral'];
    if(!s.saved)return ['草稿','neutral'];
    const published=s.release&&s.release.published_version===s.saved.version;
    const base=published?'已发布 v'+s.saved.version:'已保存 v'+s.saved.version;
    return [s.dirty?base+' · 有未保存修改':base,published?(s.dirty?'amber':'green'):(s.dirty?'amber':'')];
  }
  function sync(){
    const s=ports.dashboard.state();
    const [text,tone]=badge(s);
    const el=$('bd-state');el.textContent=text;el.className='state-badge'+(tone?' '+tone:'');
    const st=$('bd-status-state');st.replaceChildren(h('span',{class:'status-dot','aria-hidden':'true'}),document.createTextNode(text));
    $('bd-start').hidden=!!s.result;$('bd-desk').hidden=!s.result;
    const source=$('board-source').textContent;
    for(const id of ['bd-ctx-source','bd-status-source'])$(id).textContent=source;
    const name=s.binding?s.binding.dataset.id+'@'+s.binding.dataset.version:'尚无当前授权结果';
    $('bd-side-dataset').textContent=name;$('bd-data-name').textContent=name;$('bd-insp-dataset').textContent=s.binding?s.binding.dataset.id+'@'+s.binding.dataset.version:'数据集待绑定';
    $('bd-side-rows').textContent=s.result?(s.snapshot?'冻结快照当前页':'当前查询预览')+' · '+s.rows+' 行 · '+s.columns+' 个字段':'选择数据后在此组合组件。';
    const count=$('board-count').textContent;$('bd-status-count').textContent=count;$('bd-insp-meta').textContent=count+(s.saved?' · 看板 '+s.saved.id.slice(0,8)+' · 版本 '+s.saved.version:' · 尚未保存到服务器');
    structure();
    if(openDialog&&openDialog.overlay===$('bd-d-versions'))renderVersions();
  }
  function structure(){
    const cards=Array.from($('board-widgets').children),list=$('bd-layers');list.replaceChildren();$('bd-layers-empty').hidden=cards.length>0;
    cards.forEach((card,index)=>{
      const title=card.querySelector('h3'),controls=Array.from(card.querySelectorAll('.board-controls button'));
      const kind=card.querySelector('.board-chart')?'bar':card.querySelector('table')?'table':'metric';
      const row=h('div',{class:'layer-row',role:'listitem'},icon(kind==='bar'?'aggregate':kind==='table'?'table':'summary'),h('span',{text:(index+1)+' · '+(title?title.textContent:'组件')}));
      [['上移','up'],['下移','down'],['移除','trash']].forEach(([label,ic],i)=>{const b=controls[i];if(!b)return;row.append(h('button',{type:'button',class:'quiet'+(i===2?' danger':''),'aria-label':label+'：'+(title?title.textContent:'组件'),title:label,disabled:b.disabled||undefined,onclick:()=>{b.click();const again=$('bd-layers').children[Math.min(Math.max(i===0?index-1:i===1?index+1:index,0),Math.max($('bd-layers').children.length-1,0))];if(again)again.querySelectorAll('button')[i]?.focus();}},icon(ic)));});
      list.append(row);
    });
  }
  // ---- the topbar: 版本 opens the version dialog (S07-G, ADR 0011: the document studio's 版本历史与发布指针); 发布 reads the release state and asks ----
  let pick=0,pickFor='';
  function renderVersions(){
    const s=ports.dashboard.state(),pub=s.release?s.release.published_version:0,list=$('bd-version-list'),focused=list.contains(document.activeElement);list.replaceChildren();
    // When the editor's version changes (saved, opened, another dashboard), the pick follows it; otherwise it stays where the person put it.
    const editor=s.saved?s.saved.id+':'+s.saved.version:'';if(editor!==pickFor||!pick||pick>s.latest){pick=s.saved?s.saved.version:0;pickFor=editor;}
    for(let v=s.latest;v>=1;v--){
      const current=!!s.saved&&v===s.saved.version;
      const b=h('button',{type:'button',class:'version-card','aria-current':String(v===pick),onclick:()=>{pick=v;renderVersions();}},h('div',{class:'between'},h('strong',{text:'v'+v}),h('span',{class:'pill '+(v===pub?'green':'blue'),text:v===pub?'已发布':'已保存'})),h('p',{text:current?'编辑器打开的版本'+(s.dirty?'（有未保存修改）':''):'保存版本'}));
      list.append(b);
    }
    if(focused){const c=list.querySelector('[aria-current="true"]');if(c)c.focus();}
    const has=!!s.saved&&pick>=1;$('bd-version-empty').hidden=has;$('bd-version-card').hidden=!has;
    if(has){
      $('bd-vd-title').textContent='看板 · v'+pick;
      $('bd-vd-note').textContent=pick===s.saved.version?(s.dirty?'编辑器打开的版本，有未保存的修改；发布它会先保存为下一个版本。':'编辑器打开的版本。'):'恢复该版本只改变当前草稿；发布指针仍指向 '+(pub?'v'+pub:'未发布')+'。';
      $('bd-ver-restore').disabled=pick===s.saved.version&&!s.dirty;$('board-release-withdraw').disabled=!pub;
    }
    $('bd-version-note').textContent=s.saved?'保存版本不可变；当前发布指针：'+(s.release?(pub?'v'+pub:'无'):'（发布状态未读取）'):'尚未保存任何版本';
  }
  async function openVersions(){
    const s=ports.dashboard.state();
    if(!s.result){say('请先选择数据：预览或冻结快照。');return;}
    pick=0;renderVersions();open('bd-d-versions',$('bd-versions'));
    // Opening reads the list (the latest version) and the release state; 刷新发布状态与历史 stays as the retry after a conflict.
    if(s.saved){await ports.dashboard.listVersions();await ports.dashboard.readRelease();if(openDialog&&openDialog.overlay===$('bd-d-versions'))renderVersions();}
  }
  $('bd-versions').addEventListener('click',()=>{openVersions();});
  $('bd-ver-restore').addEventListener('click',async()=>{if(!pick)return;const v=pick;close();await ports.dashboard.restoreVersion(v);sync();});
  $('bd-ver-publish').addEventListener('click',()=>{if(!pick)return;const s=ports.dashboard.state();publish(s.saved&&pick===s.saved.version?0:pick);});
  async function publish(version){
    const before=ports.dashboard.state();
    if(!before.result){say('请先选择数据：预览或冻结快照。');return;}
    // 保存并发布 (S07-B): unsaved changes, or no saved version yet, are saved as the next version first; the confirmation says so and does both.
    // A historical version from the version dialog is published as it is (a rollback): nothing is saved and the editor does not change.
    historical=version||0;saveFirst=!historical&&(!before.saved||before.dirty);
    if(!saveFirst)await ports.dashboard.readRelease();
    const s=ports.dashboard.state();if(!saveFirst&&(!s.release||!s.saved))return;
    const next=s.saved?s.saved.version+1:1,target=historical||(s.saved?s.saved.version:0);
    const dl=$('bd-publish-summary');dl.replaceChildren();
    const rows=[['看板',s.saved?s.saved.id:'新看板'],['将发布的版本',saveFirst?'先保存为版本 '+next+'，再发布它':'保存版本 '+target+(historical?'（历史版本）':'')],['数据集',s.binding?s.binding.dataset.id+'@'+s.binding.dataset.version:'—']];
    if(!historical)rows.push(['组件',s.widgets+' 个']);
    rows.push(['当前发布',saveFirst?(s.release?(s.release.published_version?'版本 '+s.release.published_version:'未发布'):'保存后读取'):(s.release.published_version?'版本 '+s.release.published_version:'未发布')],['发布修订',saveFirst?(s.release?String(s.release.revision):'保存后读取'):String(s.release.revision)]);
    for(const [k,v] of rows)dl.append(h('dt',{text:k}),h('dd',{text:v}));
    $('bd-publish-note').textContent=saveFirst?(s.saved?'画布有未保存的修改：先保存为新的不可变版本，再把发布指针移到它。':'这个看板还没有保存版本：先保存为版本 1，再发布它。'):historical?'发布历史版本是一次明确的回退：打开已发布看板得到 v'+historical+'；编辑器里的内容不变。':(s.release.published_version===s.saved.version?'这个保存版本已经是当前发布版本；再次发布会记一次新的发布修订。':'发布后，打开已发布看板得到的就是这个版本；之后的保存不改变发布指针。');
    $('bd-publish-confirm').textContent=saveFirst?'保存并发布':'确认发布';
    open('bd-d-publish',$('bd-publish'));
  }
  let saveFirst=false,historical=0;
  $('bd-publish').addEventListener('click',()=>{publish();});
  $('bd-publish-confirm').addEventListener('click',async()=>{
    close();
    if(saveFirst){const was=ports.dashboard.state().saved;await ports.dashboard.save();const now=ports.dashboard.state();if(!now.saved||(was&&now.saved.version===was.version)){sync();return;}await ports.dashboard.readRelease();if(!ports.dashboard.state().release){sync();return;}}
    await ports.dashboard.publish(historical||undefined);sync();
  });
  for(const b of qa('[data-act=help]'))b.addEventListener('click',()=>open('bd-d-help',b));
  // The data buttons (the start card, the context bar, the data panel) open the console's data drawer (S07-A); without one (the embed page) they go to the query form on the page.
  for(const b of qa('[data-act=data]'))b.addEventListener('click',()=>{if(ports.data)ports.data(b);else{const f=document.getElementById('query-companies');if(f)f.focus();}});
  for(const b of qa('[data-act=toggle-sidebar]'))b.addEventListener('click',()=>toggleSidebar());
  for(const b of qa('[data-act=toggle-inspector]'))b.addEventListener('click',()=>toggleInspector());
  sync();
  return {
    sync,
    show(){sync();},
    clear(){close();pick=0;pickFor='';toggleSidebar(false);toggleInspector(false);showSide('components');showTab('properties');sync();},
    showTab,showSide
  };
};
