const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, '../internal/server/static/app.js'), 'utf8');
const node = {agent:{id:1,hostname:'node',last_seen:Math.floor(Date.now()/1000)},latest_tcpping:[]};
const response = data => ({ok:true,json:async()=>data});
const deferred = () => {let resolve;const promise=new Promise(r=>{resolve=r;});return {promise,resolve};};

function harness() {
  const elements = new Map();
  const states = [], logs = [], renders = [];
  const context = {
    AbortController, setTimeout, clearTimeout,
    localStorage:{getItem:()=>null,setItem:()=>{}},
    console:{error:(...args)=>logs.push(args),warn:(...args)=>logs.push(args)},
    document:{getElementById:id=>elements.get(id)||null,querySelector:()=>null,querySelectorAll:()=>[]},
    states, renders,
  };
  vm.createContext(context);
  vm.runInContext(source.slice(0,source.indexOf('let themeMode =')),context);
  vm.runInContext(`renderAll=()=>renders.push('all'); renderInfoBar=()=>{};
    setDashboardState=(state,message)=>states.push({state,message});`,context);
  return {context,elements,states,logs,renders};
}

(async()=>{
  const {context:ctx,states,logs,renders}=harness();
  ctx.fetch=async url=>url==='/api/info'?{ok:false,status:502}:response([node]);
  await ctx.fullRefresh();
  assert.equal(renders.length,1,'a failed auxiliary info request must not block node rendering');
  assert.equal(states.at(-1).state,'ready');
  assert(logs.length>0,'auxiliary failure must still be logged');

  ctx.fetch=async url=>url==='/api/info'?response({version:'test'}):{ok:false,status:500};
  await ctx.softRefresh();
  assert.equal(states.at(-1).state,'stale');
  assert(states.at(-1).message.includes('HTTP 500'));
  assert.equal(vm.runInContext('agents[0].agent.hostname',ctx),'node');
  assert(logs.some(args=>args.some(arg=>String(arg).includes('/api/agents: HTTP 500'))));
  ctx.fetch=async url=>response(url==='/api/info'?{}:[node]);
  await ctx.softRefresh();assert.equal(states.at(-1).state,'ready');

  ctx.fetch=async url=>url==='/api/info'?response({}):{ok:true,json:async()=>{throw new SyntaxError('unexpected <');}};
  await ctx.softRefresh();
  assert(states.at(-1).message.includes('invalid data'));
  ctx.fetch=async url=>response(url==='/api/info'?{}:{error:'not a node list'});
  await ctx.softRefresh();assert(states.at(-1).message.includes('invalid data'));
  assert.equal(vm.runInContext('agents[0].agent.hostname',ctx),'node');
  ctx.fetch=async()=>{throw new TypeError('Failed to fetch');};
  await ctx.softRefresh();assert(states.at(-1).message.includes('connection failed'));
  ctx.fetch=async()=>({ok:true,json:async()=>{throw new TypeError('connection closed during body');}});
  await ctx.softRefresh();assert(states.at(-1).message.includes('connection failed'));

  ctx.fetch=async url=>response(url==='/api/info'?{}:[node]);
  vm.runInContext('renderAll=()=>{throw new Error("broken render");}',ctx);
  await ctx.fullRefresh();assert(states.at(-1).message.includes('display could not be updated'));
  assert(!states.at(-1).message.includes('connection failed'));
  vm.runInContext('renderAll=()=>renders.push("all");',ctx);
  await ctx.softRefresh();assert.equal(states.at(-1).state,'ready');

  // A hanging info request cannot delay a successful node response.
  const info=deferred();
  ctx.fetch=async url=>url==='/api/info'?info.promise:response([]);
  await ctx.fullRefresh();
  assert.equal(states.at(-1).state,'ready');
  assert.equal(vm.runInContext('agents.length',ctx),0);
  info.resolve(response({}));await new Promise(resolve=>setTimeout(resolve,0));

  // Timeouts cover the body too, and release the controller for the next request.
  ctx.fetch=(_url,{signal})=>new Promise((_resolve,reject)=>{
    signal.addEventListener('abort',()=>reject(new Error('aborted')),{once:true});
  });
  await assert.rejects(ctx.fetchJSON('/timeout',{timeoutMs:5}),error=>error.kind==='timeout'&&error.message.includes('/timeout'));
  ctx.fetch=async(_url,{signal})=>({ok:true,json:()=>new Promise((_resolve,reject)=>{
    signal.addEventListener('abort',()=>reject(new Error('aborted body')),{once:true});
  })});
  await assert.rejects(ctx.fetchJSON('/body-timeout',{timeoutMs:5}),error=>error.kind==='timeout');
  ctx.fetch=async()=>response([]);await ctx.fullRefresh();assert.equal(states.at(-1).state,'ready');

  const detail=harness(),d=detail.context;
  detail.elements.set('detail-1',{});
  for(const kind of ['metrics','tcpping'])detail.elements.set(kind+'-error-1',{hidden:true,textContent:''});
  d.fixture=[node];d.drawn=[];
  vm.runInContext(`agents=fixture;expandedId=1;
    renderSparklines=(id,rows)=>drawn.push({id,kind:'metrics',rows});
    renderTCPingChart=(id,rows)=>drawn.push({id,kind:'tcpping',rows});`,d);
  d.fetch=async url=>url.includes('/metrics')?response([{cpu_usage:20}]):{ok:false,status:502};
  await d.loadDetailData(1,'24h');
  assert.equal(d.drawn.length,1,'a failed probe request must not suppress successful metrics');
  assert.equal(detail.elements.get('metrics-error-1').hidden,true);
  assert.equal(detail.elements.get('tcpping-error-1').hidden,false);
  assert(detail.elements.get('tcpping-error-1').innerHTML.includes('HTTP 502'));
  assert(detail.elements.get('tcpping-error-1').innerHTML.includes('>Retry</button>'));
  d.fetch=async()=>response([]);await d.loadDetailData(1,'24h');
  assert.equal(detail.elements.get('tcpping-error-1').hidden,true);

  // Disabling probes must stop history requests while preserving system ranges
  // and the weekly date drill-down, even with old probe samples still present.
  const requested=[];
  const now=Math.floor(Date.now()/1000);
  d.fixture=[{...node,tcpping_enabled:false,latest_tcpping:[{name:'old',created_at:now}]}];
  vm.runInContext('agents=fixture;currentMetricsRange="168h";currentTcppingRange="168h";',d);
  detail.elements.set('metrics-days-1',{innerHTML:'',hidden:true});
  detail.elements.set('detail-day-btn',{textContent:'1d',classList:{add:()=>{}}});
  d.fetch=async url=>{requested.push(url);return response([{created_at:now,cpu_usage:20}]);};
  const beforeDisabled=d.drawn.length;
  await d.loadDetailData(1,'168h');
  assert.equal(requested.length,1);
  assert(requested[0].includes('/metrics?range=168h'));
  assert.equal(d.drawn.length,beforeDisabled+1);
  assert.equal(d.drawn.at(-1).kind,'metrics');
  assert(detail.elements.get('metrics-days-1').innerHTML.includes('openHistoricalDay(1,'));
  assert(!detail.elements.get('metrics-days-1').hidden);
  d.openHistoricalDay(1,now*1000);
  // Fetch starts synchronously; await completion before changing the fixture.
  await new Promise(resolve=>setTimeout(resolve,0));
  assert(requested.at(-1).includes('/metrics?since='));
  const ranges=[];
  d.document.querySelectorAll=selector=>{
    assert.equal(selector,'#detail-1 .detail-range-group .theme-btn');
    return ['24h','168h','720h'].map(range=>({dataset:{range},classList:{toggle:(_name,on)=>{if(on)ranges.push(range);}}}));
  };
  d.switchDetailRange(1,'720h');
  await new Promise(resolve=>setTimeout(resolve,0));
  assert.equal(ranges.at(-1),'720h');
  assert(requested.at(-1).includes('/metrics?range=720h'));
  assert(requested.every(url=>!url.includes('/tcpping')));
  d.fixture=[node];vm.runInContext('agents=fixture;',d);
  const beforeEnabled=requested.length;
  await d.loadDetailData(1,'24h');
  assert.equal(requested.length,beforeEnabled+2,'re-enabling must restore probe requests');
  assert(requested.at(-1).includes('/tcpping'));

  // Even a transport that delivers after cancellation cannot draw over a newer view.
  const pending=[];
  d.fetch=()=>{const call=deferred();pending.push(call);return call.promise;};
  const old=d.loadDetailData(1,'24h');
  const before=d.drawn.length;
  detail.elements.set('detail-2',{});
  vm.runInContext('expandedId=2;',d);
  d.fetch=async()=>response([]);
  await d.loadDetailData(2,'168h');
  for(const call of pending)call.resolve(response([{cpu_usage:99}]));
  await old;
  assert.equal(d.drawn.length,before+2);
  assert(d.drawn.slice(before).every(draw=>draw.id===2),'old-node response must never render in new detail');

  const rangePending=[];
  d.fetch=()=>{const call=deferred();rangePending.push(call);return call.promise;};
  const oldRange=d.loadDetailData(2,'24h');
  const beforeRange=d.drawn.length;
  d.fetch=async()=>response([]);await d.loadDetailData(2,'720h');
  for(const call of rangePending)call.resolve(response([{cpu_usage:88}]));
  await oldRange;
  assert.equal(d.drawn.length,beforeRange+2,'old-range response must be discarded');

  const closing=[];
  d.fetch=()=>{const call=deferred();closing.push(call);return call.promise;};
  const closeRequest=d.loadDetailData(2,'24h');
  const beforeClose=d.drawn.length;
  vm.runInContext('expandedId=null;destroyDetailCharts();',d);
  for(const call of closing)call.resolve(response([]));await closeRequest;
  assert.equal(d.drawn.length,beforeClose,'closing a detail must invalidate pending results');

  const empty=harness();
  let disposed=0;
  empty.context.chart={dispose:()=>disposed++};
  for(const key of ['spark-cpu','spark-mem','spark-netin','spark-netout']){
    empty.elements.set(key,{innerHTML:'old chart'});empty.elements.set(key+'-val',{textContent:'old value'});
  }
  vm.runInContext('sparkCharts={"spark-cpu":chart};',empty.context);
  empty.context.renderSparklines(1,[]);
  assert.equal(disposed,1);
  assert.equal(empty.elements.get('spark-cpu-val').textContent,'—');
  assert(empty.elements.get('spark-cpu').innerHTML.includes('No data'));
  console.log('Dashboard refresh checks passed: independent metadata, request failures/recovery, invalid data, timeouts, render errors, detail isolation, cancellation, and empty history.');
})().catch(error=>{console.error(error);process.exitCode=1;});
