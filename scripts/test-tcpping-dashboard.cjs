const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');

const nodes = new Map();
const storage = new Map();
const ctx = {
  AbortController, setTimeout, clearTimeout,
  localStorage: { getItem: key => storage.get(key) ?? null, setItem: (key,value) => storage.set(key,value) },
  document: { documentElement: {classList:{contains:()=>false}}, getElementById: id => nodes.get(id) ?? null, querySelector: () => null, querySelectorAll: () => [] },
  console,
};
vm.createContext(ctx);
const source = fs.readFileSync(path.join(__dirname, '../internal/server/static/app.js'), 'utf8');
vm.runInContext(source.slice(0, source.indexOf('let themeMode =')), ctx);
const escapeAttribute = vm.runInContext('escapeAttr',ctx);
assert.equal(escapeAttribute('<img src="x">&\''),'&lt;img src=&quot;x&quot;&gt;&amp;&#39;');
for(const value of [null,undefined,0,'A&B',"'\"<>"])assert.equal(escapeAttribute(value),ctx.escapeHtml(value));
const node = { agent: {id:1, hostname:'node',region:'CN'}, tcpping_enabled:true, latest_tcpping:[{name:'CMv6',target:'v6.example:80',success:true,latency_ms:12}] };
ctx.fixture = [node];
vm.runInContext('agents = fixture',ctx);
assert(ctx.renderCard(node,false).includes(ctx.mapCarrier('CMv6')));
assert(ctx.renderListRow(node,0,false).includes('12.0ms'));
// Old card preferences no longer choose a hidden route in either view.
const twoRoutes = {...node, latest_tcpping:[...node.latest_tcpping,{name:'CTv6',target:'[2001:db8::1]:80',success:true,latency_ms:20}]};
storage.set('cardTcpping_1','CTv6');
assert(ctx.renderListRow(twoRoutes,0,false).includes('CMv6'));
assert(!ctx.renderListRow(twoRoutes,0,false).includes('CTv6'));
assert(ctx.renderCard(twoRoutes,false).includes('CMv6'));
assert(ctx.renderCard(twoRoutes,false).includes('CTv6'));
const history = [{name:'CMv4',latency_ms:10,success:true},{name:'CMv6',latency_ms:12,success:true}];
const stats = ctx.tcppingStatsHTML(1,history,['CMv4','CMv6']);
assert(!stats.includes('Inactive'));
assert(!stats.includes('data-name="CMv4"'));
assert(stats.includes('data-name="CMv6"'));
assert.equal(ctx.activeTcppingResults(1,history).length,1);
assert.equal(ctx.dashboardLayoutSignature([node]),ctx.dashboardLayoutSignature([{...node,latest_tcpping:[{...node.latest_tcpping[0],latency_ms:99}]}]));
const disabled = {...node,tcpping_enabled:false,latest_tcpping:[]};
assert.notEqual(ctx.dashboardLayoutSignature([node]),ctx.dashboardLayoutSignature([disabled]));
assert(!ctx.renderCard(disabled,false).includes('class="card-probes"'));
assert(ctx.renderCard(disabled,false).includes('class="card-probes-off"'));
assert(ctx.renderCard(disabled,false).includes('TCP Ping · Off'));
assert(!ctx.renderCard(node,false).includes('card-probes-off'));
assert(!ctx.renderCard(disabled,false).includes('class="probe-lat"'));
assert(!ctx.renderListRow(disabled,0,false).includes('class="list-ping'));
assert(!ctx.renderCard({...disabled,latest_tcpping:node.latest_tcpping},false).includes('CMv6'));
assert(ctx.renderCard({...node,tcpping_enabled:undefined,latest_tcpping:[]},false).includes('No TCP Ping data'));
ctx.fixture=[disabled];vm.runInContext('agents = fixture',ctx);
for(const id of ['tcpping-chart-1','tcpping-rows-1','tcpping-days-1'])nodes.set(id,{innerHTML:'old content',textContent:''});
let disposed=0;
ctx.fixtureChart={dispose:()=>disposed++};vm.runInContext('tcppingChart = fixtureChart',ctx);
ctx.renderTCPingChart(1,history);
assert.equal(disposed,1);
assert.equal(nodes.get('tcpping-days-1').innerHTML,'');
assert.equal(nodes.get('tcpping-rows-1').innerHTML,'');
assert(nodes.get('tcpping-chart-1').innerHTML.includes('No TCP Ping data'));
assert.equal(vm.runInContext('tcppingChart',ctx),null);
// Every node keeps the same toolbar below the system charts, without a target count.
const loadDetailData = ctx.loadDetailData;
ctx.loadDetailData = () => {};
nodes.set('detail-1',{innerHTML:''});
ctx.renderDetailContent(1);
const withoutProbes = nodes.get('detail-1').innerHTML;
assert(withoutProbes.includes('<h3>TCP Ping</h3>'));
assert(!withoutProbes.includes('tcpping-chart-'));
assert(!withoutProbes.includes('tcpping-controls'));
assert(!withoutProbes.includes('tcpping-stats'));
assert(!withoutProbes.includes('tcpping-status'));
assert(withoutProbes.includes('spark-netout'));
assert(withoutProbes.includes('metrics-days-1'));
assert.equal((withoutProbes.match(/data-range=/g)||[]).length,3);
assert(withoutProbes.indexOf('detail-range-group') > withoutProbes.indexOf('spark-netout'));
ctx.fixture=[node];vm.runInContext('agents = fixture',ctx);
ctx.renderDetailContent(1);
assert(nodes.get('detail-1').innerHTML.includes('tcpping-section-1'));
assert(nodes.get('detail-1').innerHTML.includes('tcpping-controls'));
assert(!nodes.get('detail-1').innerHTML.includes('tcpping-status'));
assert(!nodes.get('detail-1').innerHTML.includes('metrics-days-1'));
assert.equal((nodes.get('detail-1').innerHTML.match(/data-range=/g)||[]).length,3);
assert(nodes.get('detail-1').innerHTML.includes('id="detail-system-1" hidden'), 'legacy Agents must not leave an empty visible metadata row');
const systemNode = {...node, agent:{...node.agent, cpu_model:'Intel N150',os_name:'ImmortalWrt 24.10.4',arch:'x86_64',cpu_cores:4,virtualization_system:'kvm',virtualization_role:'host'}};
ctx.fixture=[systemNode];vm.runInContext('agents = fixture',ctx);
ctx.renderDetailContent(1);
const systemDetail = nodes.get('detail-1').innerHTML;
assert(systemDetail.includes('Intel N150 · ImmortalWrt 24.10.4 · x86_64 · KVM Host'));
assert(systemDetail.indexOf('detail-system-1') < systemDetail.indexOf('spark-grid'));
assert(systemDetail.includes('id="detail-system-1">'), 'system information is visible directly without a hover title');
assert.equal(ctx.systemInfoText({agent:{cpu_model:'AMD EPYC 7B13',arch:'aarch64'}}),'AMD EPYC 7B13 · ARM64');
assert.equal(ctx.systemInfoText({agent:{os_name:'Debian 12'}}),'Debian 12');
assert.equal(ctx.systemInfoText({agent:{}}),'');
for (const [system,label] of Object.entries({kvm:'KVM',qemu:'QEMU',vmware:'VMware',hyperv:'Hyper-V',vbox:'VirtualBox',xen:'Xen',docker:'Docker',lxc:'LXC',podman:'Podman'})) {
  assert.equal(ctx.systemInfoText({agent:{arch:'x86_64',virtualization_system:system,virtualization_role:'guest'}}),`x86_64 · ${label}`);
}
assert.equal(ctx.systemInfoText({agent:{os_name:'Debian 12',virtualization_system:'unknown'}}),'Debian 12');
assert.equal(ctx.systemInfoText({agent:{virtualization_system:'none'}}),'');
assert.equal(ctx.systemInfoText({agent:{virtualization_role:'host'}}),'');
assert.equal(ctx.systemInfoText({agent:{virtualization_system:'future-hypervisor'}}),'future-hypervisor');
assert.equal(ctx.systemInfoText({agent:{virtualization_system:'__proto__'}}),'__proto__');
const renamed={...node,agent:{...node.agent,hostname:'stable-agent',display_name:'Home <Router>'}};
assert.equal(ctx.nodeName(renamed),'Home <Router>');
assert(ctx.renderCard(renamed,false).includes('Home &lt;Router&gt;'));
assert(ctx.renderListRow(renamed,0,false).includes('Home &lt;Router&gt;'));
assert(!ctx.renderCard(renamed,false).includes('Home <Router>'));
assert.equal(renamed.agent.hostname,'stable-agent','a display label must not replace Agent identity');
assert.notEqual(ctx.dashboardLayoutSignature([node]),ctx.dashboardLayoutSignature([renamed]));
const noted = {...node, agent:{...node.agent,note:'家庭主路由 <b>test</b>'}};
const noteCard = ctx.renderCard(noted,false);
assert(noteCard.includes('<span class="card-note">家庭主路由 &lt;b&gt;test&lt;/b&gt;</span>'));
assert(noteCard.indexOf('class="card-uptime"') < noteCard.indexOf('class="card-note"'));
assert(!noteCard.includes('<b>test</b>'));
assert(!ctx.renderCard(node,false).includes('class="card-note"'),'empty notes must not leave a placeholder');
assert.notEqual(ctx.dashboardLayoutSignature([node]),ctx.dashboardLayoutSignature([noted]),'note edits must redraw the card');
const cpuModelMetrics=ctx.cardMetricsHTML(systemNode);
assert(!cpuModelMetrics.includes('Intel N150'));
assert.equal(cpuModelMetrics,ctx.cardMetricsHTML({...systemNode,agent:{...systemNode.agent,cpu_model:''}}),'CPU model must not change card content or hover titles');
assert.equal(ctx.renderCard(systemNode,false),ctx.renderCard({...systemNode,agent:{...systemNode.agent,virtualization_system:'',virtualization_role:''}},false),'virtualization must not change the card layout or add hover titles');
const hostile = {...node,agent:{...node.agent,cpu_model:'<img src=x onerror="alert(1)">',os_name:'<script>bad</script>',arch:'" onmouseover="bad',virtualization_system:'<iframe>bad</iframe>'}};
ctx.fixture=[hostile];vm.runInContext('agents = fixture',ctx);
ctx.renderDetailContent(1);
const safeDetail=nodes.get('detail-1').innerHTML;
assert(!safeDetail.includes('<img')&&!safeDetail.includes('<script>')&&!safeDetail.includes('<iframe>')&&!safeDetail.includes(' onmouseover="bad'));
assert(safeDetail.includes('&lt;img')&&safeDetail.includes('&lt;script&gt;')&&safeDetail.includes('&lt;iframe&gt;'));
const liveSystem = {textContent:'',title:'',hidden:true};
nodes.set('detail-system-1',liveSystem);
ctx.updateLiveSummaries([systemNode]);
assert.equal(liveSystem.textContent,'Intel N150 · ImmortalWrt 24.10.4 · x86_64 · KVM Host');
assert.equal(liveSystem.title,'');
assert.equal(liveSystem.hidden,false);
ctx.updateLiveSummaries([node]);
assert.equal(liveSystem.textContent,'');
assert.equal(liveSystem.hidden,true);
ctx.fixture=[node];vm.runInContext('agents = fixture',ctx);
ctx.loadDetailData = loadDetailData;

// Show/Hide must keep chart, rows, keyboard accessibility and saved preferences
// in sync. Individual route toggling must still work after either bulk action.
const selectionNames=['CMv4','CUv6'];
const selectable=()=>{
  const classes=new Set(),attributes=new Map();
  return {classes,attributes,classList:{toggle:(name,on)=>on?classes.add(name):classes.delete(name)},setAttribute:(name,value)=>attributes.set(name,value)};
};
const selectionRows=selectionNames.map(name=>({...selectable(),dataset:{name}}));
const selectionButtons=[selectable(),selectable()];
const queryAll=ctx.document.querySelectorAll;
ctx.document.querySelectorAll=selector=>selector.includes('tcpping-rows-1')?selectionRows:selector.includes('tcpping-controls')?selectionButtons:[];
const selectionOption={series:selectionNames.map(name=>({name})),legend:[{selected:{}}]};
ctx.fixtureSelectionChart={getOption:()=>selectionOption,setOption:options=>{selectionOption.legend[0].selected=options.legend.selected;}};
vm.runInContext('tcppingChart = fixtureSelectionChart',ctx);
ctx.setTcppingSelection(1,false);
assert.deepEqual(JSON.parse(storage.get('tcppingSel_1')),{CMv4:false,CUv6:false});
assert(selectionRows.every(row=>row.classes.has('hidden')&&row.attributes.get('aria-pressed')==='false'));
assert(!selectionButtons[0].classes.has('active')&&selectionButtons[1].classes.has('active'));
ctx.setTcppingSelection(1,true);
assert.deepEqual(JSON.parse(storage.get('tcppingSel_1')),{CMv4:true,CUv6:true});
assert(selectionRows.every(row=>!row.classes.has('hidden')&&row.attributes.get('aria-pressed')==='true'));
assert(selectionButtons[0].classes.has('active')&&!selectionButtons[1].classes.has('active'));
ctx.tcppingToggleLine(1,'CMv4');
assert.deepEqual(JSON.parse(storage.get('tcppingSel_1')),{CMv4:false,CUv6:true});
assert(selectionRows[0].classes.has('hidden')&&!selectionRows[1].classes.has('hidden'));
ctx.document.querySelectorAll=queryAll;
vm.runInContext('tcppingChart = null',ctx);

// Monthly quotas use the same decimal units for both usage and allowance.
const traffic = {available:true,has_data:true,sent:100_000_000_000,recv:296_000_000_000};
assert(ctx.trafficHTML(traffic,1_000_000_000_000).includes('396 GB / 1 TB'));
assert(ctx.trafficHTML(traffic,1_000_000_000_000).includes('39.6%'));
const over = ctx.trafficHTML({...traffic,sent:800_000_000_000},1_000_000_000_000);
assert(over.includes('109.6%')); assert(over.includes('width:100.0%'));
assert(ctx.trafficHTML(traffic,0).includes('396 GB used'));
assert(ctx.trafficHTML(traffic,0).includes('♾️'));
assert(!ctx.trafficHTML(traffic,0).includes('>—</span>'));
assert(!ctx.trafficHTML(traffic,0).includes('NaN'));
assert(ctx.trafficHTML({available:false},1e12).includes('Not configured / 1 TB'));
assert(ctx.trafficHTML({available:true,has_data:false},1e12).includes('No data / 1 TB'));
assert.equal(ctx.formatCardSpeed(2e6),'2 MB/s');
assert(ctx.cardNetworkHTML(node).includes('Monthly network usage'));
assert(!ctx.cardNetworkHTML(node).includes('0 B'));
assert(!ctx.cardNetworkHTML({...node,traffic:{...traffic,has_data:false}}).includes('296 GB'));
const dual = {...node,latest_tcpping:[
 {name:'CMv4',target:'192.0.2.1:80',success:true,latency_ms:20},
 {name:'CTv4',target:'192.0.2.2:80',success:false,latency_ms:0},
 {name:'CUv6',target:'[2001:db8::1]:80',success:true,latency_ms:30},
 {name:'custom<script>',target:'example.com:443',success:true,latency_ms:40},
]};
const probes = ctx.cardProbesHTML(dual);
assert(!probes.includes('probe-family-label'));
assert(probes.includes('probe-family-ipv4'));assert(probes.includes('probe-family-ipv6'));assert(probes.includes('probe-family-custom'));
assert(probes.indexOf('CMv4') < probes.indexOf('CTv4'));
assert(probes.includes('100%'));assert(probes.includes('custom&lt;script&gt;'));
assert(!ctx.cardProbesHTML(node).includes('IPv4'));
assert(ctx.cardProbesHTML(node).includes('probe-family-ipv6'));
assert(!ctx.cardProbesHTML(node).includes('probe-family-ipv4'));
const css = fs.readFileSync(path.join(__dirname, '../internal/server/static/style.css'), 'utf8');
assert.match(css, /\.probe-family-ipv4\s*\{[^}]*grid-column:\s*1;[^}]*grid-row:\s*1;/);
assert.match(css, /\.probe-family-ipv6\s*\{[^}]*grid-column:\s*2;[^}]*grid-row:\s*1;/);
// Every supported flag class must resolve to a shipped rectangular asset.
const staticDirectory=path.join(__dirname,'../internal/server/static');
const flagCSS=fs.readFileSync(path.join(staticDirectory,'vendor/flag-icons.min.css'),'utf8');
const flagCodes=fs.readdirSync(path.join(staticDirectory,'flags/4x3')).filter(name=>name.endsWith('.svg')).map(name=>name.slice(0,-4)).sort();
const cssFlagCodes=[...flagCSS.matchAll(/url\(([^)]+)\)/g)].map(([,url])=>{
  assert(fs.existsSync(path.resolve(staticDirectory,'vendor',url)),url);
  return path.basename(url,'.svg');
}).sort();
assert.deepEqual(cssFlagCodes,flagCodes);
for(const code of flagCodes){
  assert.match(code,/^[a-z]{2}$/);
  assert(ctx.flagEmoji(code.toUpperCase()).includes('fi-'+code));
}
// Retained history includes all six routes; detail rows follow the current three.
ctx.fixture=[{...node,latest_tcpping:['CM','CU','CT'].map(name=>({name:name+'v4'}))}];
vm.runInContext('agents = fixture',ctx);
const sixNames=['CMv4','CUv4','CTv4','CMv6','CUv6','CTv6'];
const sixHistory=sixNames.map(name=>({name,success:true,latency_ms:10}));
const threeStats=ctx.tcppingStatsHTML(1,sixHistory,sixNames);
assert.equal((threeStats.match(/class="tcpping-stat-row"/g)||[]).length,3);
assert(!threeStats.includes('v6'));
assert.equal(sixHistory.length,6,'display filtering must preserve historical input');
ctx.fixture=[{...node,latest_tcpping:sixNames.map(name=>({name}))}];
vm.runInContext('agents = fixture',ctx);
const sixStats=ctx.tcppingStatsHTML(1,sixHistory,sixNames);
assert.equal((sixStats.match(/class="tcpping-route-family"/g)||[]).length,2);
assert(sixStats.indexOf('data-family="v4"')<sixStats.indexOf('data-family="v6"'));
assert(sixStats.indexOf('data-name="CTv4"')<sixStats.indexOf('data-name="CMv6"'));
assert(sixStats.includes('title="Average latency"'));
for (const card of [node, dual, disabled, {...dual,agent:{...dual.agent,billing_cost:{currency:'USD',amount:'5'},billing_period:'1m'},expiry_date:'2026-11-08',expiry_days:20,traffic}]) {
  assert(!/\btitle\s*=/.test(ctx.renderCard(card,false)), 'no area of a node card should create a hover popup');
}
assert(systemDetail.includes('class="detail-header" title='),'sample freshness belongs in Node Detail');
ctx.fixture=[{...node,latest_tcpping:['CM','CU','CT'].map(name=>({name:name+'v4'}))}];
vm.runInContext('agents = fixture',ctx);
let chartOptions={};
ctx.echarts={init:()=>({setOption:options=>{Object.assign(chartOptions,options);},dispose:()=>{}})};
ctx.renderTCPingChart(1,sixHistory.map(r=>({...r,created_at:Math.floor(Date.now()/1000)})));
assert.equal(chartOptions.series.length,3,'detail chart must also omit disabled routes');
assert(chartOptions.series.every(s=>s.name.endsWith('v4')));
assert.equal((nodes.get('tcpping-rows-1').innerHTML.match(/class="tcpping-stat-row"/g)||[]).length,3);
ctx.fixture=[disabled];vm.runInContext('agents = fixture',ctx);

(async()=>{
  let renders=0;
  ctx.onRender=()=>renders++;
  vm.runInContext('renderAll = () => onRender(); renderInfoBar = () => {}; setDashboardState = () => {}; agents = fixture;',ctx);
  let next=[node];
  ctx.fetch=async url=>({ok:true,json:async()=>url==='/api/info'?{}:next});
  await ctx.softRefresh();
  assert.equal(renders,1,'enabling probes must redraw live summaries');
  next=[{...node,latest_tcpping:[{...node.latest_tcpping[0],latency_ms:15}]}];
  await ctx.softRefresh();assert.equal(renders,1,'ordinary samples must not force a full redraw');
  next=[disabled];await ctx.softRefresh();assert.equal(renders,2,'disabling probes must remove live latency fields');
  // Ordinary refresh updates all four metrics, both families and the speeds.
  const elements = new Map(['.card-metrics','.card-probes','.card-billing','.card-sub span:first-child'].map(k=>[k,{innerHTML:'',textContent:''}]));
  const speeds={innerHTML:''};
  Object.defineProperty(speeds,'title',{set:()=>assert.fail('live refresh must not restore the network hover popup')});
  elements.set('.card-speeds',speeds);
  const fakeCard={classList:{contains:name=>name==='card',toggle:()=>{}},querySelector:key=>elements.get(key)||null,
    setAttribute:name=>assert.notEqual(name,'title','live refresh must not restore the card hover popup')};
  ctx.document.querySelector=()=>fakeCard;
  const refreshed={...dual,agent:{...dual.agent,monthly_traffic_limit:1e12,cpu_cores:4},traffic,
    latest_metric:{cpu_usage:25,memory_total:4e9,memory_used:1e9,disk_total:10e9,disk_used:2e9,network_down:2e6,network_up:3.5e6,load1:.12,uptime:86400}};
  next=[refreshed];await ctx.softRefresh();
  // The first metric changes the layout; a subsequent sample uses live updates.
  await ctx.softRefresh();
  assert(elements.get('.card-metrics').innerHTML.includes('396 GB / 1 TB'));
  assert(elements.get('.card-metrics').innerHTML.includes('4C · Load 0.12'));
  assert(!ctx.cardMetricsHTML({...refreshed,agent:{...refreshed.agent,cpu_cores:0}}).includes('0C'));
  assert(ctx.cardMetricsHTML({...refreshed,agent:{...refreshed.agent,cpu_cores:0}}).includes('Load 0.12'));
  assert.equal((elements.get('.card-metrics').innerHTML.match(/class="metric"/g)||[]).length,4);
  assert(elements.get('.card-speeds').innerHTML.includes('↓ 2 MB/s'));
  assert(elements.get('.card-speeds').innerHTML.includes('↓ 296 GB'));
  assert(elements.get('.card-speeds').innerHTML.includes('↑ 100 GB'));
  assert(elements.get('.card-probes').innerHTML.includes('CUv6'));
  for (const element of elements.values()) assert(!/\btitle\s*=/.test(element.innerHTML),'live card content must remain free of hover popups');
  const detailHeader={title:''};
  nodes.get('detail-1').querySelector=selector=>selector==='.detail-header'?detailHeader:null;
  ctx.updateLiveSummaries([{...refreshed,live:{sample_at:100,last_full_sample_at:90,last_full_report_at:95,history_stale:true}}]);
  assert(detailHeader.title.includes('History saved'));
  assert(detailHeader.title.includes('History storage delayed'),'freshness diagnostics must still update in Node Detail');
  next=[{...refreshed,traffic:{...traffic,recv:400_000_000_000}}];await ctx.softRefresh();
  assert(elements.get('.card-metrics').innerHTML.includes('500 GB / 1 TB'));
  assert(elements.get('.card-speeds').innerHTML.includes('↓ 400 GB'));
  const changedBilling={...refreshed,agent:{...refreshed.agent,billing_period:'24m',billing_cost:{currency:'CAD',amount:'90.1'}},expiry_date:'2028-10-15',expiry_days:727};
  next=[changedBilling];await ctx.softRefresh();
  assert(elements.get('.card-billing').innerHTML.includes('CA$90.10/2yr'));
  assert(elements.get('.card-billing').innerHTML.includes('2028-10-15'));
  next=[{...changedBilling,agent:{...changedBilling.agent,billing_period:'forever',billing_cost:{currency:'CAD',amount:'0'}}}];
  await ctx.softRefresh();
  assert(elements.get('.card-billing').innerHTML.includes('Free'));
  assert(elements.get('.card-billing').innerHTML.includes('♾️'));
  const partial={...refreshed,latest_metric:{...refreshed.latest_metric,missing_metrics:1|2|4|16,cpu_usage:0,memory_total:0,memory_used:0,disk_total:0,disk_used:0,network_up:0,network_down:0}};
  const partialCPU=ctx.cardMetricsHTML(partial);
  assert(partialCPU.includes('4C · Load —'));
  assert(!partialCPU.includes('NaN'));
  assert(ctx.cardNetworkHTML(partial).includes('↓ 0 B/s'),'measured zero speed must remain visible');
  assert(ctx.cardNetworkHTML({...partial,latest_metric:{...partial.latest_metric,missing_metrics:8}}).includes('↓ —'));
  assert(ctx.renderListRow(partial,0,false).includes('list-bar-val">—'));
  // A disabled card has no probe element. Ordinary live refresh must still
  // update its traffic and system metrics without causing a dashboard error.
  const hiddenProbes={...refreshed,tcpping_enabled:false,latest_tcpping:[]};
  elements.delete('.card-probes');
  assert.doesNotThrow(()=>ctx.updateLiveSummaries([hiddenProbes]));
  next=[hiddenProbes];await ctx.softRefresh();
  await ctx.softRefresh();
  assert(elements.get('.card-metrics').innerHTML.includes('4C · Load 0.12'));
  assert(elements.get('.card-speeds').innerHTML.includes('↓ 2 MB/s'));
  const snapshots=[];ctx.snapshots=snapshots;
  vm.runInContext('renderSparkline=(id,data)=>snapshots.push({id,data});',ctx);
  for(const key of ['spark-cpu','spark-mem','spark-netin','spark-netout'])nodes.set(key+'-val',{innerHTML:''});
  ctx.renderSparklines(1,[{...refreshed.latest_metric,created_at:100}, {...partial.latest_metric,created_at:130}]);
  assert.equal(snapshots.find(s=>s.id==='spark-cpu').data.at(-1)[1],null,'missing CPU must produce a chart gap');
  assert.equal(nodes.get('spark-cpu-val').innerHTML,'—');
  assert.equal(snapshots.find(s=>s.id==='spark-netin').data.at(-1)[1],0,'valid zero network remains a data point');
  console.log('Dashboard checks passed: quota totals, partial collectors, zero readings, chart gaps, probe families, safe labels, and live refresh.');
})().catch(error=>{console.error(error);process.exitCode=1;});
