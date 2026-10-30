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
context.profileRefreshes=0;
context.softRefresh=()=>context.profileRefreshes++;
stream.emit('node',{kind:'resync'});
assert.equal(context.profileRefreshes,1,'profile/order changes must reload the entire committed node list');
stream.emit('snapshot',[node(140,130,10)]);
stream.emit('node',{kind:'realtime',node:node(145,130,11)});
assert.equal(vm.runInContext('agents[0].latest_metric.created_at',context),145);
stream.onerror();stream.onerror();assert(stream.closed);assert.equal(intervals.size,1);assert.equal(timeouts.size,1);
const reconnect=[...timeouts.values()][0];timeouts.clear();reconnect();
assert.equal(connections.length,2);connections[1].onopen();assert.equal(intervals.size,0);
connections[1].emit('snapshot',[node(130,130,1)]);
assert.equal(vm.runInContext('agents[0].latest_metric.created_at',context),145,'server restart keeps actual sample timestamps');
connections[1].emit('node',{kind:'realtime',node:node(150,130,2)});
assert.equal(vm.runInContext('agents[0].latest_metric.created_at',context),150,'revisions restart cleanly after reconnect');
stream.emit('node',{kind:'realtime',node:node(999,999,999)});
assert.equal(vm.runInContext('agents[0].latest_metric.created_at',context),150,'closed stream events must not cross reconnect generations');
connections[1].emit('node',{kind:'remove',id:1});
assert.equal(vm.runInContext('agents.length',context),0);
context.stopEvents();assert.equal(intervals.size,0);assert.equal(timeouts.size,0);
console.log('Realtime dashboard checks passed: localized updates, independent history, ordering, reconnect and bounded polling.');

// Browser lifecycle and outages use a fake clock so grace periods and wake-up
// races can be exercised without waiting or relying on a live network.
function recoveryHarness() {
  let now = 1_800_000_000_000, nextID = 1;
  const timers = new Map(), streams = [], events = {}, elements = new Map();
  const element = () => ({hidden:true,textContent:'',classList:{add(){},remove(){}}});
  for (const id of ['dashboard-status','empty-retry','empty-state','empty-title','empty-desc']) elements.set(id,element());
  class Clock extends Date { constructor(...args){super(...(args.length?args:[now]));} static now(){return now;} }
  class Stream {
    constructor(){this.listeners={};streams.push(this);}
    addEventListener(name,callback){this.listeners[name]=callback;}
    close(){this.closed=true;}
    emit(name,data){this.listeners[name]({data:JSON.stringify(data)});}
  }
  const schedule = (fn,delay,repeat=0) => {const id=nextID++;timers.set(id,{fn,at:now+delay,repeat});return id;};
  const ctx = {Date:Clock,EventSource:Stream,AbortController,console:{error(){},warn(){}},
    localStorage:{getItem:()=>null},window:{addEventListener:(name,fn)=>events[name]=fn,location:{reload:()=>assert.fail('recovery must preserve the current page')}},
    document:{hidden:false,getElementById:id=>elements.get(id)||null,querySelector:()=>null,querySelectorAll:()=>[],addEventListener:(name,fn)=>events[name]=fn},
    setTimeout:(fn,delay)=>schedule(fn,delay),clearTimeout:id=>timers.delete(id),
    setInterval:(fn,delay)=>schedule(fn,delay,delay),clearInterval:id=>timers.delete(id)};
  vm.createContext(ctx);
  vm.runInContext(source.slice(0,source.indexOf('let themeMode =')),ctx);
  vm.runInContext('renderAll=()=>{};renderInfoBar=()=>{};updateLiveSummaries=()=>{};updateDetailCharts=()=>{};',ctx);
  vm.runInContext(source.slice(source.indexOf("window.addEventListener('pagehide',")),ctx);
  const flush = async()=>{for(let i=0;i<16;i++)await Promise.resolve();};
  const tick = async ms => {
    const end=now+ms;
    for (;;) {
      const entry=[...timers].filter(([,t])=>t.at<=end).sort((a,b)=>a[1].at-b[1].at)[0];
      if(!entry)break;
      const [id,t]=entry;now=t.at;
      if(t.repeat)t.at+=t.repeat;else timers.delete(id);
      t.fn();await flush();
    }
    now=end;await flush();
  };
  const row = (name='live')=>({...node(Math.floor(now/1000),Math.floor(now/1000),1),agent:{id:1,hostname:name,last_seen:Math.floor(now/1000),region:'US'}});
  let online=true,requests=0;
  const response=data=>({ok:true,json:async()=>data});
  ctx.fetch=async url=>{
    if(url==='/api/info')return response({});
    requests++;if(!online)throw new TypeError('offline');return response([row()]);
  };
  return {ctx,events,elements,timers,streams,flush,tick,row,response,
    status:elements.get('dashboard-status'),retry:elements.get('empty-retry'),
    setOnline:value=>online=value,requests:()=>requests,
    value:code=>vm.runInContext(code,ctx)};
}

(async()=>{
  const h=recoveryHarness(),c=h.ctx;
  h.setOnline(false);await c.fullRefresh();
  assert.equal(h.retry.hidden,false,'a first-load failure needs a centered Retry control');
  assert.equal(h.status.hidden,true,'first-load errors must not create a banner');
  h.setOnline(true);await c.softRefresh();assert(h.retry.hidden);
  h.value('expandedId=1;filterRegion="US";currentHistoryDay={start:1,end:2};');
  c.connectEvents();h.streams.at(-1).onopen();
  h.setOnline(false);h.streams.at(-1).onerror();await h.flush();
  await h.tick(29999);assert(h.status.hidden,'brief disconnects must remain quiet');
  await h.tick(1);assert(!h.status.hidden,'a sustained data outage must show the small Retry notice');
  h.streams.at(-1).onopen();assert(!h.status.hidden,'opening a socket alone does not prove fresh data');
  h.setOnline(true);await c.softRefresh();assert(h.status.hidden,'successful polling clears the notice even without SSE');
  h.streams.at(-1).onerror();await h.flush();await h.tick(35000);
  assert(h.status.hidden,'failed SSE reconnects must stay quiet while HTTP data still arrives');

  const old=h.streams.at(-1),beforeHide=h.requests();
  c.document.hidden=true;h.events.visibilitychange();
  await h.tick(300000);
  assert.equal(h.requests(),beforeHide,'hidden pages must not keep polling or reconnecting');
  assert.equal(h.timers.size,0);
  old.onopen();assert.equal(h.timers.size,0,'late open events from a closed stream must be ignored');
  c.document.hidden=false;const beforeWake=h.requests(),beforeStreams=h.streams.length;
  h.events.visibilitychange();h.events.online();h.events.focus();await h.flush();
  assert.equal(h.requests(),beforeWake+1,'overlapping wake/online/focus events must make one recovery request');
  assert.equal(h.streams.length,beforeStreams+1,'wake-up must rebuild the old connection once');
  assert.equal(h.value('expandedId'),1);assert.equal(h.value('filterRegion'),'US');
  assert.equal(h.value('currentHistoryDay.start'),1,'recovery must keep the selected historical day');
  assert(h.status.hidden);
  h.events.pagehide();const beforeRestore=h.requests();
  h.events.pageshow({persisted:true});await h.flush();
  assert.equal(h.requests(),beforeRestore+1,'back/forward-cache restore must restart updates');
  h.events.pageshow({persisted:false});assert.equal(h.requests(),beforeRestore+1);

  // Even if a transport ignores AbortSignal, a pre-sleep response cannot paint
  // over resumed data or release the resumed request's in-flight guard.
  const race=recoveryHarness(),r=race.ctx,pending=[];
  await r.fullRefresh();
  r.fetch=async url=>url==='/api/info'?race.response({}):new Promise(resolve=>pending.push(resolve));
  const oldRequest=r.softRefresh();await race.flush();
  race.events.pagehide();race.events.pageshow({persisted:true});await race.flush();
  assert.equal(pending.length,2,'wake-up must not wait for a frozen request timeout');
  pending[0](race.response([race.row('old')]));await oldRequest;
  assert.equal(race.value('refreshInFlight'),true);
  assert.equal(race.value('agents[0].agent.hostname'),'live');
  pending[1](race.response([race.row('resumed')]));await race.flush();
  assert.equal(race.value('agents[0].agent.hostname'),'resumed');
  assert.equal(race.value('refreshInFlight'),false);
  const fallback=recoveryHarness();
  fallback.ctx.applyEventNodes([{...fallback.row('before-restart'),revision:40}],true);
  await fallback.ctx.softRefresh();
  assert.equal(fallback.value('agents[0].agent.hostname'),'live','fallback GET must accept fresh state after Server revisions restart');
  const empty=recoveryHarness();
  empty.ctx.fetch=async url=>empty.response(url==='/api/info'?{}:[]);
  await empty.ctx.fullRefresh();
  empty.ctx.fetch=async()=>{throw new TypeError('offline');};
  await empty.ctx.softRefresh();await empty.tick(30000);
  assert(!empty.status.hidden,'a previously loaded empty dashboard also needs an outage notice');
  r.stopEvents();c.stopEvents();
  console.log('Recovery checks passed: quiet grace, sustained outages, HTTP fallback, sleep/wake, deduplication, cached-page restore and canceled-response isolation.');
})().catch(error=>{console.error(error);process.exitCode=1;});
