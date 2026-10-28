const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, '../internal/server/static/app.js'), 'utf8');
const fixtures = [
  {agent:{id:1,display_order:1,hostname:'permanent',region:'US',billing_period:'forever'},expiry_date:'2020-01-01'},
  {agent:{id:2,display_order:2,hostname:'today-offline',region:'US',billing_period:'1m',expires_at:9999999999},expiry_date:'2026-10-27'},
  {agent:{id:3,display_order:3,hostname:'router',region:'CN',billing_period:'1m'},expiry_date:'2026-11-01'},
  {agent:{id:4,display_order:4,hostname:'same-date',region:'SG',billing_period:'12m'},expiry_date:'2026-11-01'},
  {agent:{id:5,display_order:5,hostname:'unset',region:'US'}},
  {agent:{id:6,display_order:6,hostname:'annual',region:'US',billing_period:'12m',expires_at:1},expiry_date:'2027-10-24'},
  {agent:{id:7,display_order:7,hostname:'invalid-date',region:'CN',billing_period:'1m'},expiry_date:'2026-02-30'},
].map(a=>({...a,revision:1,latest_metric:{created_at:100,cpu_usage:10},live:{last_full_sample_at:100}})).reverse();

function harness(storage = new Map()) {
  const attributes = new Map(), classes = new Set();
  const button = {innerHTML:'<svg aria-hidden="true"></svg>',setAttribute:(key,value)=>attributes.set(key,value),
    set textContent(value) { this.innerHTML=value; },
    classList:{toggle:(key,on)=>on?classes.add(key):classes.delete(key)}};
  const elements = new Map([['server-info',{innerHTML:''}],['due-sort',button]]);
  const context = {AbortController,setTimeout,clearTimeout,console,
    localStorage:{getItem:key=>storage.get(key)??null,setItem:(key,value)=>storage.set(key,value)},
    document:{getElementById:id=>elements.get(id)||null,querySelector:()=>null,querySelectorAll:()=>[]},
    fixtures:structuredClone(fixtures),renders:[],updates:0};
  vm.createContext(context);
  vm.runInContext(source.slice(0,source.indexOf('let themeMode =')),context);
  vm.runInContext(`agents=fixtures;expandedId=3;currentHistoryDay={start:1,end:2};
    renderAll=()=>renders.push({ids:filterAndSort(agents).map(a=>a.agent.id),expandedId,day:currentHistoryDay,format:cardFormat});
    updateLiveSummaries=()=>updates++;
    setDashboardState=()=>{};`,context);
  return {context,storage,button,attributes,classes};
}
const ordered = ctx=>Array.from(vm.runInContext('filterAndSort(agents).map(a=>a.agent.id)',ctx));

(async()=>{
  const {context:ctx,storage,button,attributes,classes}=harness();
  const original = JSON.stringify(ctx.fixtures);
  assert.deepEqual(ordered(ctx),[1,2,3,4,5,6,7]);
  ctx.toggleDueSort();
  assert.deepEqual(ordered(ctx),[2,3,4,6,1,5,7],'projected dates, offline nodes, ties, and undated nodes must share one stable order');
  assert.equal(JSON.stringify(ctx.fixtures),original,'view sorting must not mutate Server records');
  assert.equal(storage.get('renewalSort'),'due');
  assert(button.innerHTML.includes('<svg'),'refreshing sort state must retain the calendar icon');
  assert.equal(attributes.get('aria-pressed'),'true');
  assert(classes.has('active'));
  assert.equal(ctx.renders[0].expandedId,3);
  assert.equal(ctx.renders[0].day.start,1,'sorting must preserve the historical date selection');

  vm.runInContext('filterRegion="CN";cardFormat="list";',ctx);
  assert.deepEqual(ordered(ctx),[3,7],'regional filtering and list mode must use the same ordering');
  ctx.toggleDueSort();
  assert.deepEqual(ordered(ctx),[3,7]);
  assert.equal(vm.runInContext('filterRegion',ctx),'CN');
  assert.equal(ctx.renders.at(-1).format,'list');
  assert(button.innerHTML.includes('<svg'));
  assert.equal(attributes.get('aria-pressed'),'false');
  assert(!classes.has('active'));
  vm.runInContext('filterRegion="";',ctx);
  assert.deepEqual(ordered(ctx),[1,2,3,4,5,6,7],'second click must restore the saved manual order');

  const reload=harness(new Map([['renewalSort','due']]));
  reload.context.renderInfoBar();
  assert.deepEqual(ordered(reload.context),[2,3,4,6,1,5,7]);
  assert(reload.button.innerHTML.includes('<svg'));
  assert.equal(harness(new Map([['renewalSort','invalid']])).context.renewalSortDate({agent:{},expiry_date:'invalid'}),'');
  for (const date of ['2026-02-29','2026-00-01','2026-13-01','2026-01-00','2026-1-01','2026-11-01<script>']) {
    assert.equal(ctx.renewalSortDate({agent:{},expiry_date:date}),'');
  }
  assert.equal(ctx.renewalSortDate({agent:{},expiry_date:'2028-02-29'}),'2028-02-29');

  // CPU and date-label changes that leave the order unchanged must not rebuild
  // charts. A changed position, including a projected monthly rollover, must.
  const live=reload.context;
  const annual=vm.runInContext('agents.find(a=>a.agent.id===6)',live);
  live.applyEventNodes([{...annual,expiry_date:'2028-10-24',revision:2}]);
  assert.equal(live.renders.length,0);
  live.applyEventNodes([{...annual,expiry_date:'2026-10-28',revision:3}]);
  assert.equal(live.renders.length,1);
  assert.deepEqual(ordered(live),[2,6,3,4,1,5,7]);
  live.applyEventNodes([{...annual,expiry_date:'2026-10-28',revision:4,latest_metric:{created_at:105,cpu_usage:80}}]);
  assert.equal(live.renders.length,1,'ordinary SSE samples must retain chart instances');
  assert.equal(live.renders[0].expandedId,3);
  assert.equal(live.renders[0].day.start,1);

  const snapshot=vm.runInContext('agents',live).map(a=>a.agent.id===2?{...a,expiry_date:'2026-11-27',revision:5}:a);
  live.fetch=async url=>({ok:true,json:async()=>url==='/api/agents'?snapshot:{version:'test'}});
  await live.softRefresh();
  assert.deepEqual(ordered(live),[6,3,4,2,1,5,7],'polling must respect a projected date change after renewal day');
  assert.equal(live.renders.length,2);
  await live.softRefresh();
  assert.equal(live.renders.length,2,'an unchanged polling result must not rebuild charts');

  const blocked=harness();
  blocked.context.localStorage.setItem=()=>{throw new Error('storage unavailable');};
  blocked.context.console={warn:()=>{}};
  blocked.context.toggleDueSort();
  assert.deepEqual(ordered(blocked.context),[2,3,4,6,1,5,7],'unavailable persistence must not prevent sorting');
  console.log('Renewal sorting checks passed: projected dates, manual ties, missing/permanent/offline nodes, filters, layouts, persistence, SSE/polling rollovers, and detail preservation.');
})().catch(error=>{console.error(error);process.exitCode=1;});
