const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const html = fs.readFileSync(__dirname + '/panel.html', 'utf8');
const source = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)].at(-1)[1]
  .replace(/^(refreshLoginStatus|load|modelCtlLoad|loadAutoCheckin|startModelMetadata)\((?:false)?\);$/gm, '');
const decode = s => s.replace(/&(amp|lt|gt|quot|#39);/g, (_, k) => ({amp:'&',lt:'<',gt:'>',quot:'"','#39':"'"}[k]));
// Offline DOM mock parses the actual markup into a node tree, with attributes,
// selectors and event dispatch. Tests exercise rendered controls and handlers,
// rather than stubbing querySelectorAll to an empty array.
class Element {
 constructor(tag, document) { this.tagName=tag.toUpperCase();this.ownerDocument=document;this.attributes={};this.children=[];this.listeners={};this.style={};this.dataset={};this.value='';this.disabled=false;this.checked=false;this.open=false; }
 setAttribute(key,value) { this.attributes[key]=String(value);if(key.startsWith('data-'))this.dataset[key.slice(5).replace(/-([a-z])/g,(_,c)=>c.toUpperCase())]=String(value);if(['disabled','checked','open'].includes(key))this[key]=true;if(key==='value')this.value=String(value);if(key==='style')for(const entry of String(value).split(';')){const [k,v]=entry.split(':');if(k)this.style[k.trim()]=(v||'').trim();} }
 getAttribute(key) { return this.attributes[key]??null; }
 removeAttribute(key) { delete this.attributes[key]; }
 get href(){return this.getAttribute('href');} set href(v){this.setAttribute('href',v);}
 get className(){return this.attributes.class||'';} set className(v){this.setAttribute('class',v);}
 get classList(){return {add:c=>{this.className+=' '+c;},remove:c=>{this.className=this.className.split(' ').filter(v=>v!==c).join(' ');}};}
 appendChild(el){el.parentNode=this;this.children.push(el);return el;}
 get firstChild(){return this.children[0]||null;}
 remove(){if(this.parentNode)this.parentNode.children=this.parentNode.children.filter(x=>x!==this);}
 replaceWith(el){const p=this.parentNode;p.children[p.children.indexOf(this)]=el;el.parentNode=p;}
 set innerHTML(value){this._html=String(value);this.children=[];parse(this._html,this);}
 get innerHTML(){return this._html||'';}
 get textContent(){return this.tagName==='#TEXT'?this.text:this.children.map(x=>x.textContent).join('');}
 set textContent(value){this.children=[];const node=new Element('#text',this.ownerDocument);node.text=String(value);this.appendChild(node);this._html=String(value);}
 matches(selector){const attrs=[...selector.matchAll(/\[([^=\]]+)(?:="([^"]*)")?\]/g)];for(const [,k,v]of attrs){if(this.getAttribute(k)==null||(v!=null&&this.getAttribute(k)!==v))return false;}selector=selector.replace(/\[[^\]]*\]/g,'');const id=selector.match(/#([\w-]+)/);if(id&&this.getAttribute('id')!==id[1])return false;for(const [,c]of selector.matchAll(/\.([\w-]+)/g))if(!this.className.split(/\s+/).includes(c))return false;const tag=selector.match(/^[\w-]+/);return !tag||this.tagName===tag[0].toUpperCase();}
 querySelectorAll(selector){const parts=selector.split(/\s+(?![^\[]*\])/);let roots=[this];for(const part of parts){const found=[];const visit=node=>{for(const child of node.children){if(child.matches(part))found.push(child);visit(child);}};roots.forEach(visit);roots=found;}return [...new Set(roots)];}
 querySelector(selector){return this.querySelectorAll(selector)[0]||null;}
 addEventListener(type,fn){(this.listeners[type]??=[]).push(fn);}
 async dispatch(type,extra={}){if(this.disabled&&type==='click')return;const event={type,target:this,currentTarget:this,preventDefault(){this.defaultPrevented=true;},...extra};for(const fn of this.listeners[type]||[])await fn(event);if(this['on'+type])return this['on'+type](event);const inline=this.getAttribute('on'+type);if(inline){this.ownerDocument.context.__event=event;this.ownerDocument.context.__target=this;return vm.runInContext('(function(event){return '+inline+'}).call(__target,__event)',this.ownerDocument.context);}}
 focus(){this.ownerDocument.activeElement=this;} blur(){}
}
function parse(markup,root){const stack=[root];for(const match of markup.matchAll(/<!--[\s\S]*?-->|<\/?[a-zA-Z][^>]*>|[^<]+/g)){const token=match[0];if(token.startsWith('<!--'))continue;if(token.startsWith('</')){stack.pop();continue;}if(token[0]==='<'){const tag=token.match(/^<([\w-]+)/)[1];const el=new Element(tag,root.ownerDocument);for(const [,k,v1,v2,v3]of token.slice(tag.length+1,-1).matchAll(/([^\s=/>]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+)))?/g))el.setAttribute(k,decode(v1??v2??v3??''));stack.at(-1).appendChild(el);if(!/^(input|br|hr|meta|link|img)$/i.test(tag)&&!token.endsWith('/>'))stack.push(el);}else{const node=new Element('#text',root.ownerDocument);node.text=decode(token);stack.at(-1).appendChild(node);}}}
function harness(){
 const intervals=[],listeners={};
 const document={createElement:tag=>new Element(tag,document),addEventListener(type,fn){(listeners[type]??=[]).push(fn);}};
 const root=new Element('body',document);root.innerHTML=html.match(/<body>([\s\S]*?)<script>/)[1];
 document.getElementById=id=>root.querySelector('#'+id);document.querySelector=s=>root.querySelector(s);document.querySelectorAll=s=>root.querySelectorAll(s);
 const context=vm.createContext({document,window:{location:{pathname:'/v0/resource/plugins/trae/panel',search:'',host:'test'}},navigator:{},sessionStorage:{getItem(){return null},setItem(){},removeItem(){}},setInterval(fn,ms){intervals.push({fn,ms});},clearInterval(){},setTimeout(){},clearTimeout(){},requestAnimationFrame:fn=>fn(),TextEncoder,TextDecoder,URL,URLSearchParams,Intl,console,CSS:{escape:v=>v},fetch(){throw Error('unexpected network')},confirm(){return false},prompt(){return null}});
 document.context=context;vm.runInContext(source,context);vm.runInContext('var messages=[];toast=(...args)=>messages.push(args);',context);
 return {context,document,intervals,listeners,element:document.getElementById,run:code=>vm.runInContext(code,context)};
}
const catalog=`modelGroupsData={model_order:['a','b'],groups:[{realm:'cn',credentials:2,models:[{id:'a',name:'Alpha',context_length:64000,context_lengths:[64000],credentials:2},{id:'b'}]},{realm:'solo',credentials:1,models:[{id:'a-solo',base_id:'a',context_lengths:[64000,128000],credentials:1}]}]};modelRowOrder=['a','b'];modelExclMap={};modelStateValid=true;renderModelGroups();`;
const plain = v => JSON.parse(JSON.stringify(v));

test('permanent section order, direct login toolbar and no observation or manual model save UI',()=>{
 const h=harness();let last=-1;for(const id of ['toolbar','accountFilters','accountSearch','grid','promoBox','modelCtlCard']){const index=html.indexOf('id="'+id+'"');assert.ok(index>last,id);last=index;}
 assert.equal(h.element('modelCtlCard').style.display,undefined);assert.equal(h.element('loginHelp').open,false);
 assert.doesNotMatch(html,/模型管理|>保存隐藏设置<|>保存排序<|账号观察|summaryBox|modelCtlToggle|重启过则请重新/);
 assert.match(h.element('title').textContent,/Trae/);assert.equal(h.element('toolbar').querySelectorAll('button').length,5);
});
test('DOM filter chips, counts, search fields and local sort preserve source order',async()=>{
 const h=harness();h.run(`lastAccounts=[{auth_index:'one',variant:'cn',nickname:'Zed',name:'file-one',uid:'uid1'},{auth_index:'two',variant:'solo',label:'Alice',name:'file-two',uid:'uid2'},{auth_index:'three',variant:'intl',nickname:'Intl',name:'file-three',uid:'uid3'}];renderAccounts();`);
 assert.equal(h.element('accountFilters').querySelector('[data-filter="all"]').textContent,'全部3');
 await h.element('accountFilters').querySelector('[data-filter="solo"]').dispatch('click');assert.equal(h.element('grid').querySelectorAll('.card').length,1);assert.equal(h.element('grid').querySelector('.card').dataset.auth,'two');
 await h.element('accountFilters').querySelector('[data-filter="all"]').dispatch('click');
 for(const q of ['alice','FILE-two','uid2']){h.element('accountSearch').value=q;await h.element('accountSearch').dispatch('input');assert.equal(h.element('grid').querySelector('.card').dataset.auth,'two');assert.equal(h.element('accountCount').textContent,'1 / 3 个账号');}
 h.element('accountSearch').value='';h.element('accountSort').value='name';await h.element('accountSort').dispatch('change');assert.equal(h.element('grid').querySelector('.card').dataset.auth,'two');assert.equal(h.run('lastAccounts[0].auth_index'),'one');
});
test('progress requires known numeric evidence, never reward balance or invented totals',()=>{
 const h=harness();let result=h.run('progressHTML({usage_model:"unknown",remain_known:false,total_remain:0},{})');assert.match(result,/未上报/);assert.doesNotMatch(result,/width:/);
 result=h.run('progressHTML({usage_model:"basic",remain_known:true,total_remain:12,total:20,used:8},{})');assert.match(result,/可用 12/);assert.match(result,/总额 20/);assert.match(result,/width:40%/);assert.match(result,/已用 8/);
 result=h.run('progressHTML({usage_model:"fast",remain_known:true,total_remain:0,fast_limit:10,fast_used:10},{})');assert.match(result,/width:100%/);assert.match(result,/已用 10/);
 result=h.run('card({variant:"cn",checkin:{credits:999},credits:{credits_pool_known:true,credits_pool_remain:3}})');assert.match(result,/积分/);assert.match(result,/可用 3<\/span>/);assert.match(result,/999 积分（非当前余额）/);assert.doesNotMatch(result,/width:/);
 result=h.run('progressHTML({remain_known:true,total_remain:null,total:20},{})');assert.doesNotMatch(result,/width:/);
});
test('Intl no checkin, manually disabled protected, no pool or automatic-delete claims',()=>{
 const h=harness();assert.doesNotMatch(h.run('card({auth_index:"intl",variant:"intl"})'),/data-action="checkin"/);
 h.run(`lastAccounts=[{auth_index:'off',variant:'cn',disabled:true}];renderAccounts()`);assert.equal(h.element('grid').querySelector('[data-action="checkin"]').disabled,true);
 assert.doesNotMatch(html,/codebuddy\.ai|loadPoolStatus|releaseCooldown|poolByUID|签到历史|目录默认|插件可转发|恢复账号默认|耗尽自动/);
});
test('model DOM uses rounded row grid, three facts and one row action without checkboxes',()=>{
 const h=harness();h.run(catalog);const rows=h.element('modelGroupsBox').querySelectorAll('.model-row');assert.equal(rows.length,2);assert.equal(rows[0].querySelectorAll('.realm-facts').length,2);assert.equal(rows[0].querySelectorAll('[data-model-toggle]').length,1);assert.equal(h.element('modelGroupsBox').querySelectorAll('input').length,0);assert.equal(rows[0].querySelector('.drag-handle').getAttribute('draggable'),'true');
 assert.equal(h.run('catalogRows().length'),2);assert.match(rows[0].textContent,/倍率 未上报思考 未上报上下文 64K/);
});
test('effective context is blue and explicit thinking defaults match WorkBuddy, all readonly',async()=>{
 const h=harness();h.run(catalog);const rows=h.element('modelGroupsBox').querySelectorAll('.model-row');assert.ok(rows[0].querySelectorAll('.ctx-default').length>0);assert.equal(rows[0].querySelectorAll('.thinking-default').length,0);
 const target=h.element('modelGroupsBox');target.innerHTML=h.run(`modelChannelCell('solo',{id:'deepseek-solo',context_length:1000000,context_lengths:[1000000],thinking:true,thinking_state:'known',facts:[{contexts:{dev:200000},context_evidence:{kind:'upstream_usage_and_markers',observed_input_tokens:990767},capability_catalog:[{source_function:'reference',contexts:{dev:200000,max:1000000},thinking_options:['off','on'],thinking_default:'off'}]}]})`);
 assert.match(target.textContent,/上下文 200K \/ 1M/);assert.equal(target.querySelector('.ctx-default').textContent,'1M');assert.equal(target.querySelector('.thinking-default').textContent,'开启（默认）');assert.match(target.querySelector('.realm-cell').getAttribute('title'),/CPA声明 1M/);assert.match(target.querySelector('.realm-cell').getAttribute('title'),/已实测输入 990767 tokens/);
 h.run(`var writes=0;api=hostAPI=async()=>{writes++;throw Error('unexpected write')};`);
 for(const selector of ['button','[onclick]','[role="button"]','[tabindex]'])assert.equal(target.querySelectorAll(selector).length,0,selector);
 for(const el of target.querySelectorAll('span')){assert.equal(el.onclick,undefined);await el.dispatch('click');assert.notEqual(el.style.cursor,'pointer');}assert.equal(h.run('writes'),0);
 assert.doesNotMatch(html,/\.(?:realm-cell|realm-facts|ctx-default|thinking-default)[^{]*\{[^}]*cursor\s*:\s*pointer/);
 target.innerHTML=h.run(`modelChannelCell('cn',{id:'x',context_length:128000,context_lengths:[64000,128000],default_context_length:128000,default_thinking:'on',thinking:null,facts:[{capability_catalog:[{thinking_options:['on'],thinking_default:'on'}]}]})`);assert.equal(target.querySelectorAll('.ctx-default').length,0);assert.equal(target.querySelectorAll('.thinking-default').length,0);assert.match(target.textContent,/思考 on/);
 assert.equal(h.run(`modelMultiplierLabel({multiplier_state:'unknown',multiplier:0.1})`),'未上报');assert.equal(h.run(`modelMultiplierLabel({multiplier_state:'stale',multiplier:0.1})`),'待刷新');assert.match(h.run(`modelMultiplierLabel({multiplier_state:'different',multipliers:[1,2]})`),/账号差异/);
});
test('hide click serially saves all present channels and preserves unseen excludes and global locks',async()=>{
 const h=harness();h.run(catalog);h.run(`modelExclMap={'trae':['b'],'trae-cn':['unseen']};renderModelGroups();var calls=[];var map={'trae':['b'],'trae-cn':['unseen']};hostAPI=async(path,opts)=>{calls.push({path,opts});if(opts){const body=JSON.parse(opts.body);map[body.provider]=body.models;}return {'oauth-excluded-models':map};};`);
 assert.equal(h.element('modelGroupsBox').querySelector('[data-model-toggle="b"]').disabled,true);await h.element('modelGroupsBox').querySelector('[data-model-toggle="a"]').dispatch('click');
 const calls=plain(h.run('calls'));assert.equal(calls.length,4);assert.deepEqual(JSON.parse(calls[1].opts.body),{provider:'trae-cn',models:['unseen','a']});assert.deepEqual(JSON.parse(calls[2].opts.body),{provider:'trae-solo',models:['a-solo']});assert.match(h.run('messages[0][0]'),/已保存并核实/);assert.match(h.element('modelGroupsBox').querySelector('[data-model-toggle="a"]').textContent,/恢复/);
});
test('hide rejects false success and reloads on PATCH failure or mismatched readback',async()=>{
 for(const failure of ['throw','mismatch']){const h=harness();h.run(catalog);h.run(`var calls=[];api=async()=>({groups:[{realm:'cn',models:[{id:'a'},{id:'b'}]}],model_order:['a','b']});hostAPI=async(path,opts)=>{calls.push({path,opts});if(opts&&${failure==='throw'})throw Error('write failed');return {'oauth-excluded-models':{}};};`);
 await h.element('modelGroupsBox').querySelector('[data-model-toggle="a"]').dispatch('click');assert.match(h.run('messages[0][0]'),/未确认/);assert.equal(h.run('messages.some(x=>x[0].includes("已保存"))'),false);assert.equal(h.run('modelBusy'),false);assert.equal(h.run('modelEdits.size'),0);assert.match(h.element('modelGroupsBox').querySelector('[data-model-toggle="a"]').textContent,/隐藏/);assert.ok(h.run('calls.length')>=3);}
});
test('restore does not remove wildcard; global lock arriving during save rejects change',async()=>{
 for(const map of [{'trae-cn':['a*'],'trae-solo':['a-solo']},{trae:['a*'],'trae-solo':['a-solo']}]){const h=harness();h.run(catalog);h.run(`modelExclMap={'trae-cn':['a'],'trae-solo':['a-solo']};var patches=0;hostAPI=async(path,opts)=>{if(opts)patches++;return {'oauth-excluded-models':${JSON.stringify(map)}}};api=async()=>({groups:[],model_order:[]});renderModelGroups();`);await h.element('modelGroupsBox').querySelector('[data-model-toggle="a"]').dispatch('click');assert.equal(h.run('patches'),0);assert.match(h.run('messages[0][0]'),/未确认/);}
});
test('model save busy blocks duplicate handlers and does not announce success before readback',async()=>{
 const h=harness();h.run(catalog);h.run(`var release;var calls=0;var map={};hostAPI=async(path,opts)=>{calls++;if(opts){if(calls===2)await new Promise(r=>release=r);const body=JSON.parse(opts.body);map[body.provider]=body.models;}return {'oauth-excluded-models':map}};`);
 const pending=h.element('modelGroupsBox').querySelector('[data-model-toggle="a"]').dispatch('click');await new Promise(setImmediate);assert.equal(h.run('modelBusy'),true);assert.equal(h.element('modelGroupsBox').querySelector('[data-model-toggle="b"]').disabled,true);assert.equal(h.run('messages.length'),0);await h.run(`modelToggle('b')`);assert.equal(h.run('calls'),2);h.run('release()');await pending;assert.equal(h.run('modelBusy'),false);assert.match(h.run('messages[0][0]'),/已保存并核实/);
});
test('keyboard order change auto-PATCHes only model_order and verifies host and runtime',async()=>{
 const h=harness();h.run(catalog);h.run(`var calls=[];hostAPI=async(path,opts)=>{calls.push({path,opts});return {model_order:['b','a']}};api=async()=>({model_order:['b','a'],groups:[{realm:'cn',models:[{id:'a'},{id:'b'}]}]});`);
 await h.element('modelGroupsBox').querySelector('[data-drag-index="0"]').dispatch('keydown',{key:'ArrowDown'});const calls=plain(h.run('calls'));assert.deepEqual(JSON.parse(calls[0].opts.body),{model_order:['b','a']});assert.equal(calls[0].path,'/plugins/trae/config');assert.match(h.run('messages[0][0]'),/已保存并核实/);assert.equal(h.run('catalogRows()[0].id'),'b');
});
test('drag/drop persists order; failure reloads rather than retaining fake optimistic order',async()=>{
 const h=harness();h.run(catalog);h.run(`var patches=0;hostAPI=async(path,opts)=>{if(opts){patches++;throw Error('write failed')};return {'oauth-excluded-models':{}}};api=async()=>({model_order:['a','b'],groups:[{realm:'cn',models:[{id:'a'},{id:'b'}]}]});`);
 const box=h.element('modelGroupsBox');await box.querySelector('[data-drag-index="0"]').dispatch('dragstart',{dataTransfer:{setData(){}}});await box.querySelector('[data-model-id="b"]').dispatch('drop');assert.equal(h.run('patches'),1);assert.equal(h.run('catalogRows()[0].id'),'a');assert.match(h.run('messages[0][0]'),/未确认/);
});
test('order mismatch reloads; reload failure leaves controls disabled',async()=>{
 const h=harness();h.run(catalog);h.run(`hostAPI=async(path,opts)=>path==='/oauth-excluded-models'?Promise.reject(Error('offline')):{model_order:['b','a']};api=async()=>({model_order:['a','b'],groups:[]});`);await h.run('modelMove(0,1)');assert.equal(h.run('modelStateValid'),false);assert.match(h.run('messages[0][0]'),/未确认/);assert.equal(h.element('modelGroupsBox').querySelector('.drag-handle').disabled,true);
});
test('old model snapshot cannot overwrite newer models, exclusions or activity state',async()=>{
 const h=harness();h.run(`var resolves=[];api=()=>new Promise(r=>resolves.push(r));hostAPI=async()=>({'oauth-excluded-models':{}});`);const old=h.run('modelCtlLoad()'),latest=h.run('modelCtlLoad()');h.run(`resolves[1]({groups:[{realm:'cn',models:[{id:'new'}]}],promotions:{status:'known',items:[]}})`);await latest;h.run(`resolves[0]({groups:[{realm:'cn',models:[{id:'old'}]}]})`);await old;assert.equal(h.run('catalogRows()[0].id'),'new');assert.equal(h.run('promotionsData.status'),'known');
});
test('auto-checkin config defaults true only after read, PATCH plus host/runtime verification',async()=>{
 const h=harness();h.run(`var calls=[];var config={};hostAPI=async(path,opts)=>{calls.push({path,opts});if(opts)config=JSON.parse(opts.body);return config;};api=async()=>({checkin_auto:false});`);await h.run('loadAutoCheckin()');assert.equal(h.element('autoCheckin').checked,true);assert.equal(h.element('autoCheckin').disabled,false);h.element('autoCheckin').checked=false;await h.element('autoCheckin').dispatch('change');assert.deepEqual(JSON.parse(h.run('calls.find(x=>x.opts).opts.body')),{checkin_auto:false});assert.match(h.run('messages[0][0]'),/已保存并核实/);
});
test('auto-checkin failed read disables toggle; runtime mismatch never claims save success',async()=>{
 const h=harness();h.run(`hostAPI=async()=>{throw Error('offline')};`);await h.run('loadAutoCheckin()');assert.equal(h.element('autoCheckin').disabled,true);
 h.run(`hostAPI=async()=>({checkin_auto:true});api=async()=>({checkin_auto:true});`);await h.run('loadAutoCheckin()');h.element('autoCheckin').checked=false;await h.element('autoCheckin').dispatch('change');assert.match(h.run('messages[0][0]'),/未确认/);assert.equal(h.element('autoCheckin').checked,true);
});
test('unknown activities remain visible; known empty only describes the snapshot',()=>{
 const h=harness();h.run('renderPromotions(null)');assert.match(h.element('promoCount').textContent,/暂无活动数据/);assert.match(h.element('promoList').textContent,/尚未取得活动信息/);assert.notEqual(h.element('promoBox').style.display,'none');h.run(`renderPromotions({status:'known',items:[]})`);assert.match(h.element('promoList').textContent,/当前快照未列出/);
});
test('activity fixture shows server status, timezone, cross-midnight and epoch-second expiry',()=>{
 const h=harness();const data={status:'known',server_time:'2026-10-22T17:30:00Z',items:[{title:'Fixture night',model_id:'m',realm:'cn',status:'active',multiplier:0.5,time_zone:'Asia/Shanghai',end_at:Date.parse('2026-10-25T00:00:00Z')/1000,time_windows:[{weekdays:[1,2,3,4,5],start_minute:23*60,end_minute:2*60,time_zone:'Asia/Shanghai'}]},{title:'Fixture ended',model_id:'m2',status:'active',end_at:Date.parse('2026-10-22T16:00:00Z')/1000},{title:'Fixture stale',status:'active',snapshot_stale:true},{title:'Fixture outside',status:'inactive'},{title:'Fixture upcoming',status:'upcoming'},{title:'Fixture expired',status:'expired'}]};h.run(`renderPromotions(${JSON.stringify(data)})`);
 assert.match(h.element('promoCount').textContent,/1 进行中/);const text=h.element('promoList').textContent;for(const label of ['跨午夜','Asia/Shanghai','已结束','快照已过期','时段外','未开始','0.5×'])assert.ok(text.includes(label),label);assert.equal(h.element('promoList').querySelectorAll('.on.promo-row').length,1);assert.doesNotMatch(text,/1970/);
});
test('XSS account/model names, IDs, default metadata, activities and timezone are escaped',()=>{
 const h=harness(),attack='m"><img src=x onerror=alert(1)>&\'solo';h.run(`lastAccounts=[{variant:'cn',nickname:${JSON.stringify(attack)},uid:${JSON.stringify(attack)}}];renderAccounts();modelStateValid=true;modelExclMap={};modelGroupsData={groups:[{realm:'cn',models:[{id:${JSON.stringify(attack)},name:${JSON.stringify(attack)},thinking:${JSON.stringify(attack)},context_length:64000}]}]};renderModelGroups();renderPromotions({status:'known',items:[{title:${JSON.stringify(attack)},models:[${JSON.stringify(attack)}],status:'active',time_zone:${JSON.stringify(attack)},time_windows:[{weekdays:[${JSON.stringify(attack)}],start_minute:0,end_minute:60}]}]});`);
 for(const id of ['grid','modelGroupsBox','promoList']){assert.equal(h.element(id).querySelectorAll('img').length,0);assert.equal(h.element(id).querySelectorAll('script').length,0);assert.doesNotMatch(h.element(id).innerHTML,/<img|<script/);assert.ok(h.element(id).textContent.includes(attack));}assert.equal(h.element('modelGroupsBox').querySelector('[data-model-toggle]').dataset.modelToggle,attack);
});
test('confirmed-only checkin cannot borrow another account or Intl success',async()=>{
 const h=harness();for(const res of [{claim_code:0},{claim_message:'success'},{already_checked_in:true},{confirmed:true,error:'bad'},{}])assert.equal(h.run(`isCheckinDoneResult(${JSON.stringify(res)})`),false);assert.equal(h.run('isCheckinDoneResult({confirmed:true})'),true);
 h.run(`lastAccounts=[{auth_index:'one',variant:'cn'},{auth_index:'two',variant:'cn'},{auth_index:'intl',variant:'intl'}];api=async()=>({results:[{auth_index:'one',confirmed:true},{auth_index:'two',claim_code:0},{auth_index:'intl',confirmed:true}]});load=async()=>{};`);await h.run('checkinAll(null)');assert.equal(h.run('lastAccounts[0].checkin.checked_in'),true);assert.equal(h.run('lastAccounts[1].checkin'),undefined);assert.equal(h.run('lastAccounts[2].checkin'),undefined);
 h.run(`lastAccounts=[{auth_index:'one',variant:'cn'}];api=async()=>({results:[{auth_index:'two',confirmed:true}]});`);await h.run(`checkin('one','uid-one',null)`);assert.equal(h.run('lastAccounts[0].checkin'),undefined);
});
test('native host delete requires confirmation and encoded filename',async()=>{
 const h=harness();h.run(`lastAccounts=[{auth_index:'one',variant:'cn',name:'file one.json'}];var calls=[];hostAPI=async(...args)=>{calls.push(args);return {status:'ok'}};load=async()=>{};`);await h.run(`accountAction('delete','one',null)`);assert.equal(h.run('calls.length'),0);h.context.confirm=()=>true;await h.run(`accountAction('delete','one',null)`);assert.equal(h.run('calls[0][0]'),'/auth-files?name=file%20one.json');assert.equal(h.run('calls[0][1].method'),'DELETE');
});
test('login owner route, callback recovery, expansion and saved===true rule preserved',async()=>{
 const h=harness();h.run(`var calls=[],stored={};sessionStorage.setItem=(k,v)=>stored[k]=v;sessionStorage.removeItem=k=>delete stored[k];window.open=()=>{};refreshLoginStatus=async()=>{};load=async()=>{};api=async(path,opts)=>{calls.push({path,opts});return {state:'state-one',url:'https://example.test/authorize',persistent:true}};`);await h.run(`startPanelLogin('solo',null)`);assert.equal(h.element('loginHelp').open,true);assert.equal(h.run('calls[0].path'),'/login/start');assert.equal(h.run('calls[0].opts.method'),'POST');assert.equal(h.run('JSON.parse(calls[0].opts.body).variant'),'solo');assert.match(h.element('loginLaunch').textContent,/打开本次授权页面/);
 h.run(`api=async()=>({status:'ok',saved:false});messages=[];`);await h.run('pollPanelLogin()');assert.equal(h.run('panelLoginState'),'state-one');assert.equal(h.run('messages.length'),0);h.run(`api=async()=>({status:'ok',saved:true})`);await h.run('pollPanelLogin()');assert.equal(h.run('panelLoginState'),'');assert.match(h.run('messages[0][0]'),/凭证已保存/);assert.ok(h.element('cbPaste'));assert.match(source,/oauth_submit\?cb_url=/);assert.match(html,/服务重启后仍可恢复有效登录/);
});

test('pool-only credits response preserves real total and used independently from plan quota',()=>{
 const h=harness();h.run(`var account={};applyCreditsResult(account,{credits_pool_known:true,credits_pool_remain:30,credits_pool_total:100,credits_pool_used:70});`);
 const rendered=h.run('progressHTML(account.credits,{})');assert.match(rendered,/可用 30/);assert.match(rendered,/总额 100/);assert.match(rendered,/已用 70/);assert.match(rendered,/width:70%/);assert.doesNotMatch(rendered,/套餐剩余/);
});
test('missing host exclusion response fails closed; directory refresh stays busy until readback',async()=>{
 const h=harness();h.run(catalog);h.run(`var release;api=()=>new Promise(r=>release=r);hostAPI=async()=>({});`);const pending=h.run('modelCtlLoad()');assert.equal(h.element('modelRefreshBtn').disabled,true);assert.equal(h.element('modelGroupsBox').querySelector('[data-model-toggle="a"]').disabled,true);h.run(`release({groups:[]})`);await pending;assert.equal(h.run('modelStateValid'),false);assert.equal(h.element('modelRefreshBtn').disabled,false);assert.match(h.element('modelStatus').textContent,/读取失败/);
});
test('stale old read failure cannot erase newer successful catalog',async()=>{
 const h=harness();h.run(`var pending=[];api=()=>new Promise((resolve,reject)=>pending.push({resolve,reject}));hostAPI=async()=>({'oauth-excluded-models':{}});`);const first=h.run('modelCtlLoad()'),second=h.run('modelCtlLoad()');h.run(`pending[1].resolve({groups:[{realm:'cn',models:[{id:'new'}]}]})`);await second;h.run(`pending[0].reject(Error('old failure'))`);await first;assert.equal(h.run('modelStateValid'),true);assert.equal(h.run('catalogRows()[0].id'),'new');assert.doesNotMatch(h.element('modelStatus').textContent,/失败/);
});
test('partial activities retain known per-account status, expired remains ended even if snapshot is old',()=>{
 const h=harness();h.run(`renderPromotions({status:'unknown',server_time:'2026-10-23T00:00:00Z',items:[{status:'active',title:'Known',account_scope:'cn-account-1'},{status:'unknown',title:'Unknown'},{status:'expired',snapshot_stale:true,title:'Ended'}]})`);assert.match(h.element('promoCount').textContent,/1 进行中 · 1 条件信息不完整/);assert.match(h.element('promoList').querySelector('.promo-realm').getAttribute('title'),/cn-account-1/);assert.match(h.element('promoList').textContent,/已结束/);assert.match(h.run('promoTime(1792713600, "")'),/UTC/);
});
test('pending restored login opens callback recovery without starting a new login',async()=>{
 const h=harness();h.run(`panelLoginState='restored-state';api=async(path)=>path==='/login/poll'?{saved:false}:{pending:true,restored:true,persistent:true,listener_alive:false,variant:'solo',ttl_seconds_remaining:500};`);await h.run('refreshLoginStatus()');assert.equal(h.element('loginHelp').open,true);assert.match(h.element('loginStatus').textContent,/从磁盘恢复/);assert.match(h.element('loginStatus').textContent,/粘贴框仍可完成本次登录/);assert.equal(h.run('panelLoginState'),'restored-state');
});

test('WB toolbar emphasis, shared filter row and consumed quota progress semantics',()=>{
 const h=harness();assert.equal(h.element('refreshBtn').className,'');assert.ok(h.element('toolbar').querySelectorAll('button').slice(0,3).every(b=>b.className==='sec'));
 const tools=h.element('accountTools');assert.ok(tools.querySelector('#accountFilters'));assert.ok(tools.querySelector('#accountSearch'));assert.ok(tools.querySelector('#accountSort'));
 const result=h.run(`progressHTML({credits_pool_known:true,credits_pool_remain:2920,credits_pool_total:5400,credits_pool_used:2480,credits_pool_pack_count:2,fetched_at:'2026-10-23T00:00:00Z'},{})`);
 assert.match(result,/可用 2920/);assert.match(result,/已用 2480 · 46%/);assert.match(result,/总额 5400/);assert.match(result,/2 个包/);assert.match(result,/2026-10-23 00:00:00 UTC/);assert.doesNotMatch(result,/2026-10-23T/);
 const unknown=h.run(`progressHTML({remain_known:true,total_remain:12,total:20},{})`);assert.doesNotMatch(unknown,/width:|%/);
});
test('ISO weekday activity labels and single timezone annotation',()=>{
 const h=harness();assert.equal(h.run('promoWeekdays([1,2,3,4,5])'),'周一至周五');assert.equal(h.run('promoWeekdays([6,7])'),'周末');assert.equal(h.run('promoWeekdays([1,2,3,4,5,6,7])'),'每天');assert.equal(h.run('promoWeekdays([0,6])'),'weekday 0/6');
 h.run(`renderPromotions({status:'known',items:[{status:'active',time_zone:'Asia/Shanghai',time_windows:[{weekdays:[1,2,3,4,5],start_minute:1380,end_minute:120,time_zone:'Asia/Shanghai'}]}]})`);assert.equal((h.element('promoList').textContent.match(/Asia\/Shanghai/g)||[]).length,1);assert.match(h.element('promoList').textContent,/周一至周五/);assert.match(h.element('promoList').textContent,/跨午夜/);
});

test('activity duplicates group only identical facts, preserve account price/window differences, cap six visible rows',()=>{
 const h=harness();const base={title:'same',models:['m'],realm:'cn',status:'active',multiplier:0.5,time_zone:'Asia/Shanghai',time_windows:[{start_minute:0,end_minute:60}]};const data={status:'known',items:[{...base,account_scope:'a'},{...base,account_scope:'b'},{...base,account_scope:'c',multiplier:0.8},{...base,account_scope:'d',time_windows:[{start_minute:60,end_minute:120}]},...Array.from({length:6},(_,i)=>({title:'other'+i,models:['x'+i],status:i===0?'expired':'upcoming'}))]};
 h.run(`renderPromotions(${JSON.stringify(data)})`);assert.equal(h.run('groupedPromotions(promotionsData).length'),9);assert.match(h.element('promoList').textContent,/2 个账号/);assert.ok(h.element('promoMore'));assert.equal(h.element('promoMore').open,false);assert.match(h.element('promoMore').textContent,/展开其余 3 项/);assert.equal(h.element('promoList').children.filter(x=>x.className.includes('promo-row')).length,6);assert.equal(h.run('groupedPromotions(promotionsData).at(-1)._state'),'expired');assert.equal(h.run('groupedPromotions(promotionsData).filter(x=>x.multiplier===0.8).length'),1);
});
test('startup unknown, timers, visibility and boundaries never fetch upstream or retry',async()=>{
 const h=harness();h.run(`var calls=[];api=async(path,opts)=>{calls.push({path,opts});return {groups:[],promotions:{status:'unknown',items:[]}}};hostAPI=async()=>({'oauth-excluded-models':{}});`);await h.run('startModelMetadata()');assert.deepEqual(plain(h.run('calls')),[{path:'/models/groups'}]);
 h.run(`renderPromotions({status:'unknown',items:[{next_change:new Date(Date.now()-1000).toISOString()}]});`);
 const timer=h.intervals.find(x=>x.ms===60000);assert.ok(timer);for(let i=0;i<1500;i++)timer.fn();
 h.document.hidden=true;timer.fn();h.document.hidden=false;for(const listener of h.listeners.visibilitychange)listener();
 assert.deepEqual(plain(h.run('calls')),[{path:'/models/groups'}]);assert.doesNotMatch(source,/refreshModelMetadata|modelMetadataAttemptAt|modelInitialRefreshTried|15\*60\*1000/);
});
test('manual refresh alone requests upstream; failure clears payable multiplier',async()=>{
 const h=harness();h.run(catalog);h.run(`var calls=[];modelGroupsData.groups[0].models[0].multiplier=0.5;modelGroupsData.groups[0].models[0].multiplier_state='known';api=async path=>{calls.push(path);throw Error('offline')};hostAPI=async()=>({'oauth-excluded-models':{}});`);await h.element('modelRefreshBtn').dispatch('click');assert.deepEqual(plain(h.run('calls')),['/models/groups?refresh=1']);assert.equal(h.run('modelGroupsData.groups[0].models[0].multiplier_state'),'stale');assert.match(h.element('modelGroupsBox').textContent,/倍率 待刷新/);
});
test('elapsed snapshot time expires known schedule without inventing activation or fetching',()=>{
 const h=harness();h.run(catalog);h.run(`var now=Date.parse('2026-10-23T00:00:00Z');Date.now=()=>now;modelGroupsData.groups[0].models[0].multiplier=0.5;modelGroupsData.groups[0].models[0].multiplier_state='known';renderPromotions({status:'known',server_time:'2026-10-23T00:00:00Z',items:[{model_id:'a',realm:'cn',status:'active',multiplier:0.5,next_change:'2026-10-23T00:01:00Z',end_at:'2026-10-23T00:03:00Z'},{status:'upcoming',next_change:'2026-10-23T00:01:00Z'}]});`);
 const snapshot=h.element('promoMeta').textContent;assert.match(snapshot,/更新时间未上报/);assert.match(h.element('promoCount').textContent,/1 进行中/);h.run('now+=120000;minuteTick()');assert.match(h.element('promoCount').textContent,/2 条件信息不完整/);assert.match(h.element('modelGroupsBox').textContent,/倍率 待刷新/);assert.equal(h.element('promoMeta').textContent,snapshot);h.run('now+=120000;minuteTick()');assert.match(h.element('promoList').textContent,/已结束/);
});

test('host explicit null exclusion map is valid empty state; uppercase IDs verify after host normalization',async()=>{
 const h=harness();assert.deepEqual(plain(h.run(`exclusionMap({'oauth-excluded-models':null})`)),{});assert.throws(()=>h.run('exclusionMap({})'),/未返回/);
 h.run(`modelGroupsData={groups:[{realm:'cn',models:[{id:'GPT-5'}]}]};modelStateValid=true;modelExclMap={};var map=null;hostAPI=async(path,opts)=>{if(opts)map={'trae-cn':['gpt-5']};return {'oauth-excluded-models':map};};renderModelGroups();`);await h.element('modelGroupsBox').querySelector('[data-model-toggle="GPT-5"]').dispatch('click');assert.match(h.run('messages[0][0]'),/已保存并核实/);assert.match(h.element('modelGroupsBox').textContent,/恢复/);
});

test('credential refresh reads model cache exactly once, synchronizes all facts, reports partial failure',async()=>{
 for(const status of [{updated:true,status:'updated'},{updated:false,status:'error',error:'catalog unavailable'},null]){
  const h=harness();h.run(catalog);h.run(`lastAccounts=[{auth_index:'one',variant:'cn'}];var calls=[];load=async()=>{};api=async(path,opts)=>{calls.push({path,opts});if(path==='/accounts/refresh')return {model_refresh:${JSON.stringify(status)}};if(path==='/models/groups')return {groups:[{realm:'cn',models:[{id:'a',context_length:1000000,multiplier:0.12,thinking:true}]}],model_order:['a'],promotions:{status:'known',items:[{status:'active',title:'New snapshot'}]}};throw Error('unexpected path')};`);
  await h.run(`accountAction('token-refresh','one',null)`);const calls=plain(h.run('calls'));assert.deepEqual(calls.map(c=>c.path),['/accounts/refresh','/models/groups']);assert.equal(calls[0].opts.method,'POST');assert.equal(calls[1].opts,undefined);assert.match(h.element('modelGroupsBox').textContent,/上下文 1M/);assert.match(h.element('modelGroupsBox').textContent,/倍率 0.12×/);assert.match(h.element('modelGroupsBox').textContent,/思考 开启/);assert.match(h.element('promoList').textContent,/New snapshot/);
  if(status&&status.updated===false){assert.match(h.run('messages[0][0]'),/凭证已刷新，模型目录未更新/);assert.equal(h.run('messages[0][1]'),'warn');assert.match(h.run('messages[0][2]'),/catalog unavailable/);}else if(status)assert.match(h.run('messages[0][0]'),/凭证与模型目录已刷新/);else assert.match(h.run('messages[0][2]'),/状态未确认/);
 }
});
test('credential success with failed model readback remains partial and does not retry',async()=>{
 const h=harness();h.run(catalog);h.run(`lastAccounts=[{auth_index:'one'}];var calls=[];load=async()=>{};api=async path=>{calls.push(path);if(path==='/accounts/refresh')return {model_refresh:{updated:true,status:'updated'}};throw Error('cache offline')};`);await h.run(`accountAction('token-refresh','one',null)`);assert.deepEqual(plain(h.run('calls')),['/accounts/refresh','/models/groups']);assert.match(h.run('messages[0][0]'),/凭证已刷新，模型目录回读失败/);assert.equal(h.run('messages[0][1]'),'warn');assert.match(h.element('modelGroupsBox').textContent,/待刷新/);h.run('minuteTick()');assert.equal(h.run('calls.length'),2);
});
test('toolbar refresh performs one model cache GET after refresh response, not before',async()=>{
 const h=harness();h.run(catalog);h.run(`var calls=[],finish;loadAutoCheckin=async()=>{};lazyLoadCredits=()=>{};api=async(path,opts)=>{calls.push({path,opts});if(path==='/refresh'){await new Promise(r=>finish=r);return {accounts:[{auth_index:'one',variant:'cn'}],results:[{model_refresh:{updated:false,status:'error',error:'model failure'}}]}};if(path==='/intl/accounts')return {accounts:[]};if(path==='/models/groups')return {groups:[],promotions:{status:'unknown',items:[]}};throw Error('unexpected path')};`);
 const pending=h.run('load(true)');assert.deepEqual(plain(h.run('calls.map(c=>c.path)')),['/refresh']);h.run('finish()');await pending;assert.deepEqual(plain(h.run('calls.map(c=>c.path)')),['/refresh','/intl/accounts','/models/groups']);assert.match(h.run('messages[0][0]'),/凭证已刷新，模型目录未更新/);
});
test('known stale prices are labeled snapshot and unknown peers never share an asserted price',()=>{
 const h=harness();assert.equal(h.run(`modelMultiplierLabel({multiplier_state:'stale',multipliers:[0.12]})`),'0.12×（快照）');assert.equal(h.run(`modelMultiplierLabel({multiplier_state:'unknown',multipliers:[0.12],unknown_credentials:1})`),'0.12×（快照；部分账号）');
});
test('local tick preserves hide controls, drag handles, and expanded activity details',()=>{
 const h=harness();h.run(catalog);h.run(`renderPromotions({status:'known',items:Array.from({length:7},(_,i)=>({title:'item'+i,status:'active'}))})`);const box=h.element('modelGroupsBox'),handle=box.querySelector('.drag-handle'),toggle=box.querySelector('[data-model-toggle]');h.element('promoMore').open=true;handle.focus();h.run('minuteTick()');assert.equal(box.querySelector('.drag-handle'),handle);assert.equal(box.querySelector('[data-model-toggle]'),toggle);assert.equal(h.document.activeElement,handle);assert.equal(h.element('promoMore').open,true);
});

test('partial auxiliary model failure never claims full refresh success even with updated snapshot',()=>{
 const h=harness();h.run(`authRefreshToast({model_refresh:{updated:true,status:'partial',error:'aux catalog failed'}},true)`);assert.match(h.run('messages[0][0]'),/凭证已刷新，模型目录部分更新/);assert.equal(h.run('messages[0][1]'),'warn');assert.match(h.run('messages[0][2]'),/aux catalog failed/);
});
test('failed credential refresh never reads or refreshes model catalog',async()=>{
 const h=harness();h.run(`lastAccounts=[{auth_index:'one'}];var calls=[];api=async path=>{calls.push(path);return {error:'token rejected'}};`);await h.run(`accountAction('token-refresh','one',null)`);assert.deepEqual(plain(h.run('calls')),['/accounts/refresh']);assert.match(h.run('messages[0][0]'),/账号操作失败/);
});
test('activity source snapshot timestamps are distinct from cache response time',()=>{
 const h=harness();h.run(`renderPromotions({status:'known',server_time:'2026-10-23T10:00:00Z',items:[{status:'unknown',multiplier:0.12,fetched_at:'2026-10-22T12:00:00Z'}]})`);assert.match(h.element('promoMeta').textContent,/更新 2026-10-22 12:00:00 UTC/);assert.doesNotMatch(h.element('promoMeta').textContent,/2026-10-23|读取时间/);assert.match(h.element('promoList').textContent,/0.12×（参考）/);assert.match(h.element('promoList').querySelector('.promo-price').getAttribute('title'),/2026-10-22/);
});
test('auth catalog readback supersedes older cache load without resetting exclusion controls',async()=>{
 const h=harness();h.run(catalog);h.run(`var pending=[];api=()=>new Promise(resolve=>pending.push(resolve));hostAPI=async()=>({'oauth-excluded-models':{}});`);const old=h.run('modelCtlLoad()'),fresh=h.run('syncModelsAfterAuth()');h.run(`pending[1]({groups:[{realm:'cn',models:[{id:'new'}]}],promotions:{status:'known',items:[]}})`);await fresh;h.run(`pending[0]({groups:[{realm:'cn',models:[{id:'old'}]}]})`);await old;assert.equal(h.run('catalogRows()[0].id'),'new');assert.equal(h.run('modelStateValid'),true);assert.equal(h.run('promotionsData.status'),'known');
});
test('superseded auth read failure cannot stale a newer successful catalog',async()=>{
 const h=harness();h.run(catalog);h.run(`var pending=[];api=()=>new Promise((resolve,reject)=>pending.push({resolve,reject}));`);const old=h.run('syncModelsAfterAuth()'),fresh=h.run('syncModelsAfterAuth()');h.run(`pending[1].resolve({groups:[{realm:'cn',models:[{id:'new',multiplier:0.12,multiplier_state:'known'}]}]})`);await fresh;h.run(`pending[0].reject(Error('old failure'))`);await old;assert.equal(h.run('catalogRows()[0].id'),'new');assert.equal(h.run('modelGroupsData.groups[0].models[0].multiplier_state'),'known');
});

test('batch credential failure cannot become full success from another accounts model refresh',()=>{
 const h=harness();h.run(`authRefreshToast({results:[{error:'token failed'},{model_refresh:{updated:true,status:'updated'}}]},true)`);assert.match(h.run('messages[0][0]'),/账号刷新部分失败/);assert.equal(h.run('messages[0][1]'),'warn');
});


test('one account refresh button uses token+credits+models endpoint, disabled accounts stay protected',async()=>{
 const h=harness();h.run(`lastAccounts=[{auth_index:'off',variant:'cn',disabled:true},{auth_index:'on',variant:'cn'}];renderAccounts();var calls=[],finish;load=async()=>renderAccounts();api=async(path,opts)=>{calls.push({path,opts});if(path==='/accounts/refresh'){await new Promise(r=>finish=r);return {status:'partial',model_refresh:{updated:true,status:'partial',error:'aux unavailable'}};}return {groups:[],model_order:[]};};`);
 assert.equal(h.element('grid').querySelector('[data-auth="off"]').querySelector('[data-action="token-refresh"]').disabled,true);assert.equal(h.element('grid').querySelector('[data-auth="off"]').querySelector('[data-action="checkin"]').disabled,true);const card=h.element('grid').querySelector('[data-auth="on"]'),button=card.querySelector('[data-action="token-refresh"]');assert.equal(button.textContent,'刷新');assert.equal(card.querySelector('[data-action="refresh"]'),null);assert.doesNotMatch(card.textContent,/刷新额度|刷新凭证/);assert.equal(card.querySelector('[data-action="checkin"]').disabled,false);
 const pending=button.dispatch('click');assert.equal(button.disabled,true);h.run('renderAccounts()');assert.equal(h.element('grid').querySelector('[data-auth="on"]').querySelector('[data-action="token-refresh"]').disabled,true);await h.element('grid').querySelector('[data-auth="on"]').querySelector('[data-action="token-refresh"]').dispatch('click');assert.equal(h.run('calls.length'),1);h.run('finish()');await pending;
 assert.deepEqual(plain(h.run('calls.map(c=>c.path)')),['/accounts/refresh','/models/groups']);assert.equal(h.run('JSON.parse(calls[0].opts.body).auth_index'),'on');assert.equal(h.run('lastAccounts[0].disabled'),true);assert.equal(h.element('grid').querySelector('[data-auth="on"]').querySelector('[data-action="token-refresh"]').disabled,false);assert.equal(h.run('messages[0][1]'),'warn');
});
test('hidden display tail uses all present channels, leaves canonical order intact and restores original slot',async()=>{
 const h=harness();h.run(catalog);h.run(`modelRowOrder=['a','b'];modelExclMap={'trae-cn':['a']};renderModelGroups();`);
 let button=h.element('modelGroupsBox').querySelector('[data-model-toggle="a"]');assert.equal(button.textContent,'隐藏');assert.match(button.getAttribute('title'),/部分渠道已隐藏/);assert.equal(h.run('catalogRows()[0].id'),'a');
 h.run(`var calls=[],map={'trae-cn':['a','unseen'],'trae-solo':['unseen-solo']};hostAPI=async(path,opts)=>{calls.push({path,opts});if(opts){const b=JSON.parse(opts.body);map[b.provider]=b.models;}return {'oauth-excluded-models':map};};`);
 await button.dispatch('click');assert.deepEqual(plain(h.run('catalogRows().map(r=>r.id)')),['b','a']);assert.deepEqual(plain(h.run('modelRowOrder')),['a','b']);assert.deepEqual(plain(h.run('calls.filter(c=>c.opts).map(c=>JSON.parse(c.opts.body))')),[{provider:'trae-solo',models:['unseen-solo','a-solo']}]);
 await h.element('modelGroupsBox').querySelector('[data-model-toggle="a"]').dispatch('click');assert.deepEqual(plain(h.run('catalogRows().map(r=>r.id)')),['a','b']);assert.deepEqual(plain(h.run('map')),{'trae-cn':['unseen'],'trae-solo':['unseen-solo']});assert.equal(h.run('calls.some(c=>c.path.includes("config"))'),false);
});
test('global-only locks are disabled, channel locks restore only non-global exact exclusions',async()=>{
 const h=harness();h.run(catalog);h.run(`modelExclMap={trae:['a*']};renderModelGroups();`);let button=h.element('modelGroupsBox').querySelector('[data-model-toggle="a"]');assert.equal(button.textContent,'恢复');assert.equal(button.disabled,true);assert.match(button.getAttribute('title'),/全局规则锁定/);
 h.run(`modelExclMap={trae:['a'],'trae-cn':['a','unseen'],'trae-solo':['a-solo','unseen-solo']};var map=JSON.parse(JSON.stringify(modelExclMap)),calls=[];hostAPI=async(path,opts)=>{calls.push({path,opts});if(opts){const b=JSON.parse(opts.body);map[b.provider]=b.models;}return {'oauth-excluded-models':map};};renderModelGroups();`);
 button=h.element('modelGroupsBox').querySelector('[data-model-toggle="a"]');assert.equal(button.disabled,false);await button.dispatch('click');assert.deepEqual(plain(h.run('calls.filter(c=>c.opts).map(c=>JSON.parse(c.opts.body))')),[{provider:'trae-solo',models:['unseen-solo']}]);assert.deepEqual(plain(h.run('map.trae')),['a']);assert.deepEqual(plain(h.run('map["trae-cn"]')),['a','unseen']);assert.equal(h.element('modelGroupsBox').querySelector('[data-model-toggle="a"]').textContent,'隐藏');
});
test('restore wildcard on later channel preflights all channels before any PATCH',async()=>{
 const h=harness();h.run(catalog);h.run(`modelExclMap={'trae-cn':['a'],'trae-solo':['a-solo']};var patches=0;hostAPI=async(path,opts)=>{if(opts)patches++;return {'oauth-excluded-models':{'trae-cn':['a'],'trae-solo':['a-*','a-solo']}};};api=async()=>modelGroupsData;renderModelGroups();`);
 await h.element('modelGroupsBox').querySelector('[data-model-toggle="a"]').dispatch('click');assert.equal(h.run('patches'),0);assert.match(h.run('messages[0][2]'),/渠道通配规则/);assert.match(h.run('messages[0][0]'),/未确认/);
});
test('fresh GET precedes sequential three-channel PATCH preserving unseen providers, IDs and wildcard rules',async()=>{
 const h=harness();h.run(catalog);h.run(`modelGroupsData.groups.push({realm:'intl',models:[{id:'a-intl',base_id:'a'}]});var active=0,maxActive=0,calls=[],map={trae:['z*'],'trae-cn':['other','q*'],'trae-solo':['invisible'],'trae-intl':['legacy'],'unrelated':['all*']};hostAPI=async(path,opts)=>{calls.push({path,opts});if(opts){active++;maxActive=Math.max(maxActive,active);await Promise.resolve();const b=JSON.parse(opts.body);map[b.provider]=b.models;active--;}return {'oauth-excluded-models':map};};renderModelGroups();`);
 await h.element('modelGroupsBox').querySelector('[data-model-toggle="a"]').dispatch('click');assert.equal(h.run('maxActive'),1);assert.equal(h.run('calls[0].opts'),undefined);assert.equal(h.run('calls.at(-1).opts'),undefined);assert.deepEqual(plain(h.run('calls.filter(c=>c.opts).map(c=>JSON.parse(c.opts.body).provider)')),['trae-cn','trae-solo','trae-intl']);assert.deepEqual(plain(h.run('map')),{'trae':['z*'],'trae-cn':['other','q*','a'],'trae-solo':['invisible','a-solo'],'trae-intl':['legacy','a-intl'],'unrelated':['all*']});
});
test('keyboard and drag cannot cross hidden partition; saving keeps all IDs and hidden rank slots',async()=>{
 const h=harness();h.run(`modelGroupsData={groups:[{realm:'cn',models:['a','b','c','d','e'].map(id=>({id}))}]};modelRowOrder=['a','b','c','d','e'];modelExclMap={'trae-cn':['b','d']};modelStateValid=true;var calls=[],order;hostAPI=async(path,opts)=>{calls.push({path,opts});if(opts)order=JSON.parse(opts.body).model_order;return {model_order:order};};api=async()=>({...modelGroupsData,model_order:order});renderModelGroups();`);
 const box=h.element('modelGroupsBox');assert.deepEqual(plain(h.run('catalogRows().map(r=>r.id)')),['a','c','e','b','d']);await box.querySelector('[data-drag-index="2"]').dispatch('keydown',{key:'ArrowDown'});await box.querySelector('[data-drag-index="3"]').dispatch('keydown',{key:'ArrowUp'});await box.querySelector('[data-drag-index="3"]').dispatch('dragstart',{dataTransfer:{setData(){}}});await box.querySelector('[data-model-id="a"]').dispatch('drop');assert.equal(h.run('calls.length'),0);
 await box.querySelector('[data-drag-index="0"]').dispatch('keydown',{key:'ArrowDown'});assert.deepEqual(plain(h.run('order')),['c','b','a','d','e']);assert.deepEqual(plain(h.run('catalogRows().map(r=>r.id)')),['c','a','e','b','d']);
 await box.querySelector('[data-drag-index="3"]').dispatch('keydown',{key:'ArrowDown'});assert.deepEqual(plain(h.run('order')),['c','d','a','b','e']);assert.deepEqual(plain(h.run('catalogRows().map(r=>r.id)')),['c','a','e','d','b']);h.run('modelExclMap={};renderModelGroups()');assert.deepEqual(plain(h.run('catalogRows().map(r=>r.id)')),['c','d','a','b','e']);
});
test('single model card has no internally fixed height and full thinking/context facts wrap',()=>{
 const h=harness();assert.equal(h.document.querySelectorAll('#modelCtlCard').length,1);assert.equal((html.match(/id="modelCtlCard"/g)||[]).length,1);assert.doesNotMatch(html,/\.model-rows\{[^}]*(?:max-height|height:|overflow:auto)/);assert.match(html,/grid-template-columns:[^}]*86px/);assert.doesNotMatch(html,/\.realm-facts(?:>span)?\{[^}]*(?:ellipsis|nowrap|overflow:hidden)/);
 const box=h.element('modelGroupsBox');box.innerHTML=h.run(`modelChannelCell('cn',{id:'context',context_length:200000,thinking_options:['light','high','extra-high'],facts:[{contexts:{dev:200000,max:1000000}}]})`);assert.match(box.textContent,/light \/ high \/ extra-high/);assert.match(box.textContent,/200K \/ 1M/);assert.equal(box.querySelector('.ctx-default')?.textContent,'200K');assert.match(box.querySelector('.realm-cell').getAttribute('title'),/CPA声明 200K/);assert.equal(box.querySelectorAll('button').length,0);
});
test('login builds DOM link from raw ampersands once without changing encoded challenge or signature',async()=>{
 const h=harness(),url='https://example.test/authorize?client_id=a&amp;code_challenge=x%2By%252F&amp;signature=s%26x%3Dy';h.run(`var opened=[],navigated=[];window.open=(...args)=>{opened.push(args);return {opener:window,closed:false,location:{href:'about:blank',replace:url=>navigated.push(url)}}};refreshLoginStatus=async()=>{};api=async()=>({state:'login-state',url:${JSON.stringify(url)}});`);await h.run(`startPanelLogin('intl',null)`);
 const link=h.element('loginLaunch').querySelector('a'),expected=url.replace(/&amp;/g,'&');assert.equal(link.getAttribute('href'),expected);assert.equal(h.run('opened[0][0]'),'about:blank');assert.equal(h.run('navigated[0]'),expected);assert.equal(link.textContent,'打开本次授权页面');assert.equal(link.rel,'noopener noreferrer');assert.equal(h.run(`normalizeLoginLink('x&amp;amp;y%2526%26')`),'x&amp;y%2526%26');assert.equal(h.element('loginLaunch').querySelectorAll('script').length,0);
});
test('callback paste rejects authorization URL before network and preserves valid authcode bytes',async()=>{
 const h=harness();h.run(`var calls=[];fetch=async(url)=>{calls.push(url);return {ok:true,text:async()=>"回调已接收"}};load=async()=>{};refreshLoginStatus=async()=>{};`);
 for(const raw of ['https://example.test/authorize?client_id=x&code_challenge=abc','https://example.test/authorize?client_id=x&amp;code_challenge=abc','https://example.test/authorize?code_challenge=abc&authCodeInfo=']){h.element('cbPaste').value=raw;await h.run('submitCallbackURL(null)');assert.equal(h.run('calls.length'),0);assert.equal(h.element('cbPaste').value,raw);assert.match(h.run('messages.at(-1)[2]'),/完成授权.*复制完整回调链接/);}
 const raw='http://127.0.0.1:1234/authorize?authCodeInfo=a%2Bb%252F%26amp%3B&amp;signature=x%2526%3D';h.element('cbPaste').value=raw;await h.run('submitCallbackURL(null)');assert.equal(h.run('calls.length'),1);assert.equal(new URL(h.run('calls[0]'),'http://test').searchParams.get('cb_url'),raw);assert.equal(h.element('cbPaste').value,'');
});
test('activity reasons explain conditional entries without generic unknown or fake activation',()=>{
 const h=harness();h.run(`renderPromotions({status:'unknown',items:[{title:'off_peak_discount',discount_type:'off_peak_discount',status:'unknown',status_reason:'missing_timezone',models:['a']},{title:'subsidy_discount',discount_type:'subsidy_discount',status:'unknown',status_reason:'membership_not_confirmed',models:['b']}]})`);
 assert.match(h.element('promoList').textContent,/上游未提供时区/);assert.match(h.element('promoList').textContent,/会员资格未确认/);assert.doesNotMatch(h.element('promoList').textContent,/待核实|覆盖待核实/);assert.equal(h.element('promoList').querySelectorAll('.on.promo-row').length,0);assert.match(h.element('promoCount').textContent,/2 条件信息不完整/);
});

test('fresh host locks, not stale rendered locks, determine each channel targeted by hide',async()=>{
 const h=harness();h.run(catalog);h.run(`modelExclMap={trae:['a']};var map={'trae-cn':['unseen']},calls=[];hostAPI=async(path,opts)=>{calls.push({path,opts});if(opts){const b=JSON.parse(opts.body);map[b.provider]=b.models;}return {'oauth-excluded-models':map};};renderModelGroups();`);
 await h.element('modelGroupsBox').querySelector('[data-model-toggle="a"]').dispatch('click');assert.deepEqual(plain(h.run('map')),{'trae-cn':['unseen','a'],'trae-solo':['a-solo']});assert.equal(h.run('modelRowState(catalogRows().find(r=>r.id==="a")).hidden'),true);
});

test('login synchronously opens a detached blank popup before awaiting the start response',async()=>{
 const h=harness();h.run(`var events=[],finish,popup={opener:window,closed:false,document:{body:{textContent:''}},location:{href:'about:blank',replace(url){events.push(['navigate',url]);this.href=url}},close(){this.closed=true;events.push(['close']);}};window.open=(...args)=>{events.push(['open',...args]);return popup};api=async()=>{events.push(['request']);return new Promise(r=>finish=r)};refreshLoginStatus=async()=>{};`);
 const pending=h.run(`startPanelLogin('cn',null)`);assert.deepEqual(plain(h.run('events')),[['open','about:blank','_blank'],['request']]);assert.equal(h.run('popup.opener'),null);assert.match(h.run('popup.document.body.textContent'),/正在取得授权地址/);assert.equal(h.run('panelLoginState'),'');h.run(`finish({state:'pending-login',url:'https://example.test/authorize?client_id=x&code_challenge=y'})`);await pending;assert.equal(h.run('popup.location.href'),'https://example.test/authorize?client_id=x&code_challenge=y');assert.equal(h.run('panelLoginState'),'pending-login');assert.equal(h.run('messages.some(m=>m[0].includes("凭证已保存"))'),false);
});
test('blocked, closed or unnavigable popup leaves working manual link and clear recovery notice',async()=>{
 for(const mode of ['blocked','closed','throw','navigation-error']){
  const h=harness();h.run(`var closed=false;window.open=()=>${mode==='throw'?"{throw Error('blocked')}":mode==='blocked'?"null":`({opener:window,closed:${mode==='closed'},location:{href:'about:blank',replace(){throw Error('navigation blocked')}},close(){closed=true}})`};api=async()=>({state:'pending',url:'https://example.test/authorize?client_id=x&amp;code_challenge=y'});refreshLoginStatus=async()=>{};`);
  await h.run(`startPanelLogin('intl',null)`);assert.equal(h.element('loginLaunch').querySelector('a').getAttribute('href'),'https://example.test/authorize?client_id=x&code_challenge=y');assert.match(h.element('loginLaunch').textContent,/授权窗口未能自动打开.*点击上方链接/);assert.equal(h.run('panelLoginState'),'pending');assert.equal(h.run('messages[0][1]'),'warn');assert.match(h.run('messages[0][2]'),/尚未完成登录/);if(mode==='navigation-error')assert.equal(h.run('closed'),true);
 }
});
test('login start failure closes only still-blank popup and never persists false login state',async()=>{
 for(const moved of [false,true]){
  const h=harness();h.run(`var closed=false,popup={opener:window,closed:false,location:{href:'about:blank'},close(){closed=true}};window.open=()=>popup;api=async()=>{${moved?"popup.location.href='https://user-chosen.test/';":''}throw Error('start failed')};`);await h.run(`startPanelLogin('solo',null)`);assert.equal(h.run('closed'),!moved);assert.equal(h.run('panelLoginState'),'');assert.match(h.run('messages[0][0]'),/登录启动失败/);assert.equal(h.element('loginLaunch').querySelector('a'),null);
 }
});

test('catalog thinking default uses an explicit matching value without directory wording',()=>{
 const h=harness(),e=h.element('modelGroupsBox');e.innerHTML=h.run(`modelChannelCell('cn',{id:'m',context_length:1000000,context_lengths:[1000000],facts:[{contexts:{dev:200000},capability_catalog:[{source_function:'solo_agent',contexts:{max:1000000},thinking_options:['light','high','extra_high'],thinking_default:'high'}]}]})`);
 assert.match(e.textContent,/light \/ high（默认） \/ extra_high/);assert.doesNotMatch(e.textContent,/目录参考|普通|扩展/);assert.equal(e.querySelector('.ctx-default').textContent,'1M');assert.equal(e.querySelector('.thinking-default').textContent,'high（默认）');assert.equal(e.querySelectorAll('button,input,select,[onclick],[tabindex]').length,0);
 e.innerHTML=h.run(`modelChannelCell('cn',{id:'m',unknown_credentials:1,facts:[{capability_catalog:[{source_function:'solo_agent',thinking_options:['high'],thinking_default:'high'}]}]})`);assert.equal(e.querySelectorAll('.thinking-default').length,0);
 e.innerHTML=h.run(`modelChannelCell('cn',{id:'m',facts:[{capability_catalog:[{source_function:'solo_agent',thinking_options:['high','light'],thinking_default:'high'}]},{capability_catalog:[{source_function:'solo_agent',thinking_options:['high','light'],thinking_default:'light'}]}]})`);assert.equal(e.querySelectorAll('.thinking-default').length,0);
});

test('terminal exchange failure explains missing account and stops stale poll without claiming success',async()=>{
 const h=harness();h.run(`panelLoginState='test-state';var calls=[];api=async(path)=>{calls.push(path);return {pending:false,saved:false,stage:'exchange_started',restart_required:true,exchange_failure:{category:'http_error',http_status:404}}};`);
 await h.run('refreshLoginStatus()');assert.match(h.element('loginStatus').textContent,/HTTP 404/);assert.match(h.element('loginStatus').textContent,/账号尚未保存/);assert.doesNotMatch(h.element('loginStatus').textContent,/登录等待中|登录已完成/);assert.equal(h.run('calls.length'),1);
 await h.run('refreshLoginStatus()');assert.equal(h.run('calls.length'),1);assert.equal(h.run('panelLoginState'),'test-state');
 assert.match(h.run(`loginExchangeFailureText({exchange_failure:{category:'outcome_uncertain'}})`),/结果不确定/);
});

test('callback-received HTML is not a false failure toast or saved-account claim',async()=>{
 const h=harness();h.element('cbPaste').value='http://127.0.0.1:41890/authorize?authCodeInfo=mock-code';
 h.run(`fetch=async()=>({ok:true,text:async()=>'<h1>Authorization callback received</h1><p>授权回调已收到，凭证交换和保存尚待完成</p>'});var reads=0;load=async()=>{};refreshLoginStatus=async()=>{reads++};`);
 await h.run(`submitCallbackURL(document.createElement('button'))`);assert.equal(h.run('reads'),1);assert.equal(h.element('cbPaste').value,'');assert.match(h.run('messages[0][0]'),/授权回调已收到/);assert.equal(h.run(`messages.some(m=>/未完成登录|登录成功/.test(m[0]))`),false);
});
