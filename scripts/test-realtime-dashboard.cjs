const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const source = fs.readFileSync(require('node:path').join(__dirname, '../internal/server/static/app.js'), 'utf8');
const connections = [], intervals = new Map(), timeouts = new Map();
let nextTimer = 1;
class EventSource {
  constructor(url) { this.url=url;this.listeners={};connections.push(this); }
  addEventListener(name, fn) { this.listeners[name]=fn; }
  emit(name, data) { this.listeners[name]({data:JSON.stringify(data)}); }
  close() { this.closed=true; }
}
const context={EventSource,AbortController,localStorage:{getItem:()=>null},console,
  document:{getElementById:()=>null,querySelector:()=>null,querySelectorAll:()=>[]},
  setInterval:fn=>{const id=nextTimer++;intervals.set(id,fn);return id},clearInterval:id=>intervals.delete(id),
  setTimeout:fn=>{const id=nextTimer++;timeouts.set(id,fn);return id},clearTimeout:id=>timeouts.delete(id),
  renders:0,updates:0,historyLoads:0};
vm.createContext(context);
vm.runInContext(source.slice(0,source.indexOf('let themeMode =')),context);
const broken = context.historyGaps([[0,1],[30000,2],[600000,3]],1,'24h');
assert.equal(broken.length,4,'missing raw history must insert an explicit break');
assert.equal(broken[2][1],null);
assert.equal(context.historyGaps([[0,1],[900000,2]],1,'168h').length,2,'normal aggregated spacing must remain connected');

vm.runInContext(`renderAll=()=>renders++;renderInfoBar=()=>{};setDashboardState=()=>{};
updateLiveSummaries=()=>updates++;updateDetailCharts=()=>{if(!currentHistoryDay)historyLoads++;};`,context);
const node=(sample,history,revision,cpu=10)=>({agent:{id:1,hostname:'test',last_seen:sample,interval:30},
 latest_metric:{created_at:sample,cpu_usage:cpu},latest_tcpping:[{name:'CTv4',target:'x:80'}],
 tcpping_enabled:true,traffic:{sent:100,recv:200},revision,
 live:{sample_at:sample,last_full_sample_at:history,last_full_report_at:history,offline_after:120}});
context.applyEventNodes([node(100,100,1)],true);
vm.runInContext('expandedId=1;',context);
context.applyEventNodes([node(105,100,2,77)]);
assert.equal(context.renders,1,'realtime must not rebuild the page/charts');
assert.equal(context.historyLoads,0,'realtime must not refetch history');
assert.equal(context.updates,1);
context.applyEventNodes([node(110,110,3,20)]);
assert.equal(context.historyLoads,1,'committed historical sample refreshes current history');
context.applyEventNodes([node(120,110,4,88)]);
context.applyEventNodes([node(115,115,5,21)]);
assert.equal(vm.runInContext('agents[0].latest_metric.cpu_usage',context),88,'delayed full report must not roll back live CPU');
vm.runInContext('currentHistoryDay={start:1,end:2};',context);
const loaded=context.historyLoads;
context.applyEventNodes([node(130,130,6)]);
assert.equal(context.historyLoads,loaded,'past-day selection must not trigger live history requests');
const revisionCPU=vm.runInContext('agents[0].latest_metric.cpu_usage',context);
context.applyEventNodes([node(200,200,2,99)]);
assert.equal(vm.runInContext('agents[0].latest_metric.cpu_usage',context),revisionCPU,'queued old revisions must not overwrite snapshot');

context.startPolling();context.startPolling();assert.equal(intervals.size,1);
context.connectEvents();context.connectEvents();assert.equal(connections.length,1);
const stream=connections[0];stream.onopen();assert.equal(intervals.size,0,'SSE and polling must not run together');
stream.emit('snapshot',[node(140,130,10)]);
stream.emit('node',{kind:'realtime',node:node(145,130,11)});
assert.equal(vm.runInContext('agents[0].latest_metric.created_at',context),145);
stream.onerror();stream.onerror();assert(stream.closed);assert.equal(intervals.size,1);assert.equal(timeouts.size,1);
const reconnect=[...timeouts.values()][0];timeouts.clear();reconnect();
assert.equal(connections.length,2);connections[1].onopen();assert.equal(intervals.size,0);
connections[1].emit('snapshot',[node(130,130,0)]);
assert.equal(vm.runInContext('agents[0].latest_metric.created_at',context),145,'server restart keeps actual sample timestamps');
connections[1].emit('node',{kind:'realtime',node:node(150,130,1)});
assert.equal(vm.runInContext('agents[0].latest_metric.created_at',context),150,'revisions restart cleanly after reconnect');
stream.emit('node',{kind:'realtime',node:node(999,999,999)});
assert.equal(vm.runInContext('agents[0].latest_metric.created_at',context),150,'closed stream events must not cross reconnect generations');
connections[1].emit('node',{kind:'remove',id:1});
assert.equal(vm.runInContext('agents.length',context),0);
context.stopEvents();assert.equal(intervals.size,0);assert.equal(timeouts.size,0);
console.log('Realtime dashboard checks passed: localized updates, independent history, ordering, reconnect and bounded polling.');
