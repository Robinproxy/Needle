const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');

const nodes = new Map();
const storage = new Map();
const ctx = {
  localStorage: { getItem: key => storage.get(key) ?? null, setItem: (key,value) => storage.set(key,value) },
  document: { getElementById: id => nodes.get(id) ?? null, querySelector: () => null, querySelectorAll: () => [] },
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
assert.equal((stats.match(/>Inactive</g)||[]).length,1);
assert(ctx.tcpingStatusText(node).includes('1 target enabled'));
assert.equal(ctx.tcpingLayoutSignature([node]),ctx.tcpingLayoutSignature([{...node,latest_tcpping:[{...node.latest_tcpping[0],latency_ms:99}]}]));
const disabled = {...node,tcpping_enabled:false,latest_tcpping:[]};
assert.notEqual(ctx.tcpingLayoutSignature([node]),ctx.tcpingLayoutSignature([disabled]));
assert(ctx.renderCard(disabled,false).includes('TCP Ping disabled'));
assert(!ctx.renderCard(disabled,false).includes('class="probe-lat"'));
assert(ctx.renderListRow(disabled,0,false).includes('Disabled'));
ctx.fixture=[disabled];vm.runInContext('agents = fixture',ctx);
for(const id of ['tcpping-chart-1','tcpping-rows-1','tcpping-days-1','tcpping-status-1'])nodes.set(id,{innerHTML:'old content',textContent:''});
let disposed=0;
ctx.fixtureChart={dispose:()=>disposed++};vm.runInContext('tcppingChart = fixtureChart',ctx);
ctx.renderTCPingChart(1,[]);
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
assert(probes.includes('IPv4'));assert(probes.includes('IPv6'));assert(probes.includes('Custom'));
assert(probes.indexOf('CTv4') < probes.indexOf('CMv4'));
assert(probes.includes('100%'));assert(probes.includes('custom&lt;script&gt;'));
assert(!ctx.cardProbesHTML(node).includes('IPv4'));

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
  console.log('Dashboard checks passed: quota totals, overage, unset/no data, probe families, safe labels, and live refresh.');
})().catch(error=>{console.error(error);process.exitCode=1;});
