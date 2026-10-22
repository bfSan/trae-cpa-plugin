const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const html = fs.readFileSync(__dirname + '/panel.html', 'utf8');
const source = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)].at(-1)[1]
  .replace(/^refreshLoginStatus\(\);$/m, '').replace(/^load\(false\);$/m, '');
function harness() {
  const elements = new Map();
  function element(id) {
    if (!elements.has(id)) elements.set(id, {innerHTML:'',textContent:'',value:id==='accountFilter'?'all':'',style:{},dataset:{},addEventListener(){},querySelectorAll(){return []},querySelector(){return null},blur(){}});
    return elements.get(id);
  }
  const context = vm.createContext({document:{getElementById:element,querySelectorAll(){return []}},window:{location:{pathname:'/v0/resource/plugins/trae/panel',search:'',host:'test'}},navigator:{},sessionStorage:{getItem(){return null}},setInterval(){},clearInterval(){},setTimeout(){},clearTimeout(){},TextEncoder,TextDecoder,URL,URLSearchParams,console,fetch(){throw Error('unexpected network')}});
  vm.runInContext(source,context);
  return {context,element,run:code=>vm.runInContext(code,context)};
}

test('claim code/message and already flag are not confirmation',()=>{
 const h=harness();
 for(const res of [{claim_code:0},{claim_message:'success'},{already_checked_in:true},{confirmed:true,error:'bad'},{}])assert.equal(h.run(`isCheckinDoneResult(${JSON.stringify(res)})`),false);
 assert.equal(h.run('isCheckinDoneResult({confirmed:true})'),true);
});

test('Intl displays factual quota, no checkin action or pool management',()=>{
 const h=harness(), result=h.run('card({auth_index:"intl",variant:"intl",credits:{usage_model:"basic",remain_known:true,total_remain:12,total:20}})');
 assert.match(result,/12/);assert.match(result,/刷新额度/);assert.doesNotMatch(result,/data-action="checkin"|解除冷却|无积分/);
 assert.doesNotMatch(html,/codebuddy\.ai|loadPoolStatus|releaseCooldown|poolByUID|签到历史|目录默认|插件可转发|恢复账号默认/);
});

test('unknown quota never becomes zero or full bar',()=>{
 const h=harness(), result=h.run('progressHTML({usage_model:"unknown",remain_known:false,total_remain:0}, {})');
 assert.match(result,/未上报/);assert.doesNotMatch(result,/width:100%|width:0%|剩余 0/);
});

test('catalog merges suffixes into channels while retaining actual IDs and metadata',()=>{
 const h=harness();h.run(`modelGroupsData={groups:[{realm:'cn',credentials:2,models:[{id:'m',base_id:'m',name:'Model',context_lengths:[64000,128000],credentials:2}]},{realm:'solo',credentials:1,models:[{id:'m-solo',base_id:'m',context_length:64000,credentials:1}]}]};modelExclMap={'trae':['m-solo']};renderModelGroups();`);
 const result=h.element('modelGroupsBox').innerHTML;
 for(const label of ['排序','模型ID','名字','CN','SOLO','Intl','操作','64K / 128K（只读）','账号差异','m-solo','全局锁定','倍率 <','思考 <','默认未上报'])assert.ok(result.includes(label),label);
 assert.equal(h.run('catalogRows().length'),1);
 assert.doesNotMatch(result,/200K|400K|1M|context.*button|model-selected/);
});

test('hide changes preserve unseen exclusions and never copy global locks',()=>{
 const h=harness();h.run(`modelExclMap={'trae':['locked-solo']};modelEdits=new Map([['solo\\nhide-solo',false],['solo\\nlocked-solo',false],['solo\\nshow-solo',true]]);`);
 assert.deepEqual(JSON.parse(h.run(`JSON.stringify(exclusionPatch(['unseen-solo','show-solo'],'solo'))`)),['unseen-solo','hide-solo']);
 assert.equal(h.run(`exclusionMatches('GPT-5-solo',['gpt-*-solo'])`),true);
 assert.equal(h.run(`exclusionMatches('a.b',['a.b'])`),true);
 assert.equal(h.run(`exclusionMatches('axb',['a.b'])`),false);
});

test('wildcard cannot be silently removed to show one model',()=>{
 const h=harness();h.run(`modelExclMap={};modelEdits=new Map([['solo\\nm-solo',true]]);`);
 assert.throws(()=>h.run(`exclusionPatch(['*-solo'],'solo')`),/通配/);
});

test('order save patches only model_order and verifies host and runtime',async()=>{
 const h=harness();h.run(`modelGroupsData={groups:[{realm:'cn',models:[{id:'a'},{id:'b'}]}]};modelRowOrder=['b','a'];modelExclMap={};var calls=[];var messages=[];toast=(...a)=>messages.push(a);hostAPI=async(path,opts)=>{calls.push({path,opts});return {model_order:['b','a']}};api=async()=>({model_order:['b','a'],groups:[]});`);
 await h.run('modelOrderSave(null)');
 const calls=JSON.parse(h.run('JSON.stringify(calls)'));
 assert.equal(calls[0].path,'/plugins/trae/config');assert.deepEqual(JSON.parse(calls[0].opts.body),{model_order:['b','a']});assert.match(h.run('messages[0][0]'),/已保存并核实/);
 h.run(`modelGroupsData={groups:[{realm:'cn',models:[{id:'a'},{id:'b'}]}]};api=async()=>({model_order:['a','b'],groups:[]});messages=[];`);
 await h.run('modelOrderSave(null)');assert.match(h.run('messages[0][0]'),/未确认/);
});

test('partial batch only marks matching explicitly confirmed account',async()=>{
 const h=harness();h.run(`lastAccounts=[{auth_index:'one',variant:'cn'},{auth_index:'two',variant:'cn'},{auth_index:'intl',variant:'intl'}];api=async()=>({results:[{auth_index:'one',confirmed:true},{auth_index:'two',claim_code:0},{auth_index:'intl',confirmed:true}]});toast=()=>{};load=async()=>{};`);
 await h.run('checkinAll(null)');assert.equal(h.run('lastAccounts[0].checkin.checked_in'),true);assert.equal(h.run('lastAccounts[1].checkin'),undefined);assert.equal(h.run('lastAccounts[2].checkin'),undefined);
});

test('account filter applies to cards and native delete keeps confirmation',()=>{
 const h=harness();h.run(`lastAccounts=[{auth_index:'cn',variant:'cn'},{auth_index:'intl',variant:'intl'}]`);h.element('accountFilter').value='intl';h.run('renderAccounts()');
 assert.match(h.element('grid').innerHTML,/data-auth="intl"/);assert.doesNotMatch(h.element('grid').innerHTML,/data-auth="cn"/);
 assert.match(source,/\/auth-files\?name="\+encodeURIComponent\(a.name\)/);assert.match(source,/confirm\("确定删除/);
});

test('Trae callback paste contract remains intact',()=>{
 assert.match(html,/id="cbPaste"/);assert.match(source,/oauth_submit\?cb_url=/);assert.doesNotMatch(html,/id="oauthCode"|id="oauthState"/);
});


test('selected checkin never borrows a different account success',async()=>{
 const h=harness();h.run(`lastAccounts=[{auth_index:'one',variant:'cn'}];api=async()=>({results:[{auth_index:'two',confirmed:true}]});toast=()=>{};load=async()=>{};`);
 await h.run(`checkin('one','uid-one',null)`);assert.equal(h.run('lastAccounts[0].checkin'),undefined);
});

test('login launch uses authenticated POST and poll requires saved evidence',async()=>{
 const h=harness();h.run(`var calls=[],messages=[],stored={};toast=(...x)=>messages.push(x);sessionStorage.setItem=(k,v)=>stored[k]=v;sessionStorage.removeItem=k=>delete stored[k];window.open=()=>{};refreshLoginStatus=async()=>{};load=async()=>{};api=async(path,opts)=>{calls.push({path,opts});return {state:'state-one',url:'https://example.test/authorize',persistent:true}};`);
 await h.run(`startPanelLogin('solo',null)`);
 assert.equal(h.run('calls[0].path'),'/login/start');assert.equal(h.run('calls[0].opts.method'),'POST');assert.equal(h.run('JSON.parse(calls[0].opts.body).variant'),'solo');
 assert.match(h.element('loginLaunch').innerHTML,/打开本次授权页面/);
 h.run(`api=async()=>({status:'ok',saved:false});messages=[];`);await h.run('pollPanelLogin()');assert.equal(h.run('panelLoginState'),'state-one');assert.equal(h.run('messages.length'),0);
 h.run(`api=async()=>({status:'ok',saved:true});`);await h.run('pollPanelLogin()');assert.equal(h.run('panelLoginState'),'');assert.match(h.run('messages[0][0]'),/凭证已保存/);
});


test('catalog uses drag handles without visual arrow buttons and never auto-saves moves',()=>{
 const h=harness();h.run(`modelGroupsData={groups:[{realm:'cn',models:[{id:'a'},{id:'b'}]}]};modelExclMap={};renderModelGroups();`);
 const result=h.element('modelGroupsBox').innerHTML;
 assert.match(result,/class="drag-handle" draggable="true"/);assert.match(result,/>⠿<\/button>/);assert.match(result,/aria-keyshortcuts="ArrowUp ArrowDown"/);
 assert.doesNotMatch(result,/>[↑↓]<\/button>|data-move=|data-delta=/);
 h.run(`modelMove(0,1)`);assert.equal(h.run('catalogRows()[0].id'),'b');
 assert.equal(h.run('modelContextLabel(200000)'),'200K');assert.equal(h.run('modelContextLabel(256000)'),'256K');assert.equal(h.run('modelContextLabel(1048576)'),'1.048576M');
});

test('full model IDs are escaped in table cells, title, copy, and drag attributes',()=>{
 const h=harness(), id='m"><img src=x onerror=alert(1)>&\'solo';
 h.run(`modelGroupsData={groups:[{realm:'solo',credentials:2,models:[{id:${JSON.stringify(id)},base_id:${JSON.stringify(id)},name:${JSON.stringify(id)},context_length:200000,credentials:1}]}]};modelExclMap={};renderModelGroups();`);
 const result=h.element('modelGroupsBox').innerHTML;
 assert.doesNotMatch(result,/<img|<script|model-selected/);assert.ok(result.includes('m&quot;&gt;&lt;img src=x onerror=alert(1)&gt;&amp;&#39;solo'));
 assert.match(result,/title="[^"]*1\/2 凭证记录[^"]*200000 tokens/);assert.match(result,/200K（只读）/);
 const plain=result.replace(/<[^>]*>/g,'');assert.doesNotMatch(plain,/1\/2 凭证记录|200000 tokens/);
});
