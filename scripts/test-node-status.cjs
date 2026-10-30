const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, '../internal/server/static/app.js'), 'utf8');
function harness(layout = 'card', timeout = 120) {
  let now = 1_800_000_000_000;
  const start = now / 1000;
  const fields = new Map();
  const field = key => {
    if (!fields.has(key)) fields.set(key,{textContent:'',innerHTML:'',className:''});
    return fields.get(key);
  };
  const view = {attributes:{},classList:{contains:key=>key===layout},
    setAttribute(key,value){this.attributes[key]=value;},querySelector:key=>field(key),querySelectorAll:()=>[]};
  // No expiry badge container is needed in this isolated status check.
  view.querySelector = key=>['.card-sub','.list-expiry'].includes(key)?null:field(key);
  const info={innerHTML:''};
  class Clock extends Date {constructor(...args){super(...(args.length?args:[now]));} static now(){return now;}}
  const ctx={Date:Clock,AbortController,console:{error(){},warn(){}},renders:0,
    localStorage:{getItem:()=>null},setTimeout:()=>1,clearTimeout(){},
    document:{getElementById:id=>id==='server-info'?info:null,querySelector:()=>view,querySelectorAll:()=>[]}};
  vm.createContext(ctx);
  vm.runInContext(source.slice(0,source.indexOf('let themeMode =')),ctx);
  vm.runInContext('renderAll=()=>renders++;setDashboardState=()=>{};',ctx);
  const node=(seen=start,revision=1)=>({agent:{id:1,hostname:'node',region:'US',last_seen:seen,cpu_cores:4,
      billing_period:'1m',billing_cost:{currency:'USD',amount:'5'},monthly_traffic_limit:1e12},
    latest_metric:{created_at:seen,uptime:86400,cpu_usage:25,memory_total:8e9,memory_used:2e9,
      disk_total:20e9,disk_used:4e9,network_up:100,network_down:200,load1:.1},
    tcpping_enabled:false,latest_tcpping:[],traffic:{available:true,has_data:true,sent:10e9,recv:20e9},
    expiry_date:'2027-01-01',expiry_days:10,revision,live:{offline_after:timeout,last_full_sample_at:start,last_full_report_at:start}});
  let responseNode=node();
  ctx.fetch=async url=>({ok:true,json:async()=>url==='/api/info'?{}:[responseNode]});
  return {ctx,field,info,view,start,node,
    advance:seconds=>{now+=seconds*1000;},setNode:node=>{responseNode=node;},
    current:()=>vm.runInContext('agents[0]',ctx),now:()=>now/1000};
}

(async()=>{
  for (const layout of ['card','list-row']) {
    const h=harness(layout),c=h.ctx;
    await c.fullRefresh();await c.softRefresh();
    assert.equal(h.current()._online,true);
    assert.equal(h.field('.status-dot').className,'status-dot online','polling must use the computed status, not the raw response');
    const time=h.field('.card-uptime, .list-uptime');
    assert.equal(time.textContent,c.formatUptime(86400));
    const metrics=c.cardMetricsHTML(h.current()),billing=c.cardBillingHTML(h.current());

    h.advance(119);
    c.applyEventNodes([h.node()],true);
    assert.equal(h.current()._online,true,'brief missing reports must remain inside the existing grace window');
    assert(!time.textContent.includes('Seen'));
    h.advance(1);
    c.applyEventNodes([h.node()],true);
    assert.equal(h.current()._online,false,'the existing deadline must still detect overdue reports');
    assert.equal(h.field('.status-dot').className,'status-dot offline');
    assert.equal(time.textContent,'Seen 2m ago');
    assert.equal(h.field('.list-time').textContent,'','list mode must not repeat the same offline timestamp');
    assert.equal(c.cardMetricsHTML(h.current()),metrics,'overdue reports must not zero the last readings or traffic');
    assert.equal(c.cardBillingHTML(h.current()),billing,'overdue reports must not fade or change billing');
    assert(c.renderCard(h.current(),false).includes('class="card-uptime">Seen 2m ago'));
    assert(c.renderListRow(h.current(),0,false).includes('class="list-uptime">Seen 2m ago'));
    assert.equal((c.renderListRow(h.current(),0,false).match(/Seen 2m ago/g)||[]).length,1);
    h.advance(60);
    await c.softRefresh();
    assert.equal(time.textContent,'Seen 3m ago','the age must advance during HTTP fallback too');

    const recovered=h.node(h.now(),2);
    c.applyEventNodes([recovered]);
    assert.equal(time.textContent,c.formatUptime(86400));
    assert.equal(h.field('.status-dot').className,'status-dot online');
    assert.equal(c.renders,1,'status changes must update in place without rebuilding open charts');
  }
  const long=harness('card',900);
  await long.ctx.fullRefresh();long.advance(899);await long.ctx.softRefresh();
  assert.equal(long.current()._online,true,'longer Server-provided timeouts must not be replaced by 120 seconds');
  long.advance(1);await long.ctx.softRefresh();
  assert.equal(long.field('.card-uptime, .list-uptime').textContent,'Seen 15m ago');

  const outage=harness();
  await outage.ctx.fullRefresh();await outage.ctx.softRefresh();
  const previous=outage.current(),uptime=outage.field('.card-uptime, .list-uptime').textContent;
  outage.advance(300);
  outage.ctx.fetch=async()=>{throw new TypeError('network unavailable');};
  await outage.ctx.softRefresh();
  assert.equal(outage.current(),previous);
  assert.equal(outage.field('.card-uptime, .list-uptime').textContent,uptime,'browser disconnection must not relabel cached nodes');
  outage.ctx.renderInfoBar();
  assert(outage.info.innerHTML.includes('1/1'),'the node count must use the same last confirmed status as the card');

  assert.equal(outage.ctx.nodeUptimeText({agent:{},_online:false}),'No reports');
  assert.equal(outage.ctx.nodeUptimeText({agent:{},_online:true}),'No data');
  assert.equal(outage.ctx.nodeUptimeText({agent:{},_online:false,latest_metric:{created_at:outage.now()-180}}),'Seen 3m ago','older rows fall back to their last sample');
  console.log('Node status checks passed: polling/SSE grace, overdue age, recovery, saved values, stable charts, long intervals, and browser-outage isolation.');
})().catch(error=>{console.error(error);process.exitCode=1;});
