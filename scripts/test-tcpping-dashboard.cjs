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
assert(ctx.tcpingStatusText(node).includes('1 target enabled'));
assert.equal(ctx.dashboardLayoutSignature([node]),ctx.dashboardLayoutSignature([{...node,latest_tcpping:[{...node.latest_tcpping[0],latency_ms:99}]}]));
const disabled = {...node,tcpping_enabled:false,latest_tcpping:[]};
assert.notEqual(ctx.dashboardLayoutSignature([node]),ctx.dashboardLayoutSignature([disabled]));
assert(ctx.renderCard(disabled,false).includes('TCP Ping disabled'));
assert(!ctx.renderCard(disabled,false).includes('class="probe-lat"'));
assert(ctx.renderListRow(disabled,0,false).includes('Disabled'));
ctx.fixture=[disabled];vm.runInContext('agents = fixture',ctx);
for(const id of ['tcpping-chart-1','tcpping-rows-1','tcpping-days-1','tcpping-status-1'])nodes.set(id,{innerHTML:'old content',textContent:''});
let disposed=0;
ctx.fixtureChart={dispose:()=>disposed++};vm.runInContext('tcppingChart = fixtureChart',ctx);
ctx.renderTCPingChart(1,history);
assert.equal(disposed,1);
assert.equal(nodes.get('tcpping-days-1').innerHTML,'');
assert.equal(nodes.get('tcpping-rows-1').innerHTML,'');
assert(nodes.get('tcpping-chart-1').innerHTML.includes('TCP Ping disabled'));
assert.equal(vm.runInContext('tcppingChart',ctx),null);

// Monthly quotas use the same decimal units for both usage and allowance.
const traffic = {available:true,has_data:true,sent:100_000_000_000,recv:296_000_000_000};
assert(ctx.trafficHTML(traffic,1_000_000_000_000).includes('396 GB / 1 TB'));
assert(ctx.trafficHTML(traffic,1_000_000_000_000).includes('39.6%'));
const over = ctx.trafficHTML({...traffic,sent:800_000_000_000},1_000_000_000_000);
assert(over.includes('109.6%')); assert(over.includes('width:100.0%'));
assert(ctx.trafficHTML(traffic,0).includes('396 GB / ♾️'));
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
  const elements = new Map(['.card-metrics','.card-probes','.card-sub span:first-child'].map(k=>[k,{innerHTML:'',textContent:''}]));
  elements.set('.card-speeds',{title:'',innerHTML:''});
  const fakeCard={classList:{contains:name=>name==='card',toggle:()=>{}},querySelector:key=>elements.get(key)||null};
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
  next=[{...refreshed,traffic:{...traffic,recv:400_000_000_000}}];await ctx.softRefresh();
  assert(elements.get('.card-metrics').innerHTML.includes('500 GB / 1 TB'));
  assert(elements.get('.card-speeds').innerHTML.includes('↓ 400 GB'));
  const partial={...refreshed,latest_metric:{...refreshed.latest_metric,missing_metrics:1|2|4|16,cpu_usage:0,memory_total:0,memory_used:0,disk_total:0,disk_used:0,network_up:0,network_down:0}};
  const partialCPU=ctx.cardMetricsHTML(partial);
  assert(partialCPU.includes('4C · Load —'));
  assert(!partialCPU.includes('NaN'));
  assert(ctx.cardNetworkHTML(partial).includes('↓ 0 B/s'),'measured zero speed must remain visible');
  assert(ctx.cardNetworkHTML({...partial,latest_metric:{...partial.latest_metric,missing_metrics:8}}).includes('↓ —'));
  assert(ctx.renderListRow(partial,0,false).includes('list-bar-val">—'));
  const snapshots=[];ctx.snapshots=snapshots;
  vm.runInContext('renderSparkline=(id,data)=>snapshots.push({id,data});',ctx);
  for(const key of ['spark-cpu','spark-mem','spark-netin','spark-netout'])nodes.set(key+'-val',{innerHTML:''});
  ctx.renderSparklines(1,[{...refreshed.latest_metric,created_at:100}, {...partial.latest_metric,created_at:130}]);
  assert.equal(snapshots.find(s=>s.id==='spark-cpu').data.at(-1)[1],null,'missing CPU must produce a chart gap');
  assert.equal(nodes.get('spark-cpu-val').innerHTML,'—');
  assert.equal(snapshots.find(s=>s.id==='spark-netin').data.at(-1)[1],0,'valid zero network remains a data point');
  console.log('Dashboard checks passed: quota totals, partial collectors, zero readings, chart gaps, probe families, safe labels, and live refresh.');
})().catch(error=>{console.error(error);process.exitCode=1;});
