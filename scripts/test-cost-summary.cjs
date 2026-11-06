const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname,'../internal/server/static/app.js'),'utf8');
const elements = new Map([['cost-summary',{innerHTML:'',hidden:true}]]);
const ctx = {console,AbortController,localStorage:{getItem:()=>null},
  document:{hidden:false,getElementById:id=>elements.get(id),querySelectorAll:()=>[]},
  setTimeout,clearTimeout,setInterval,clearInterval};
vm.createContext(ctx);
vm.runInContext(source.slice(0,source.indexOf('let themeMode =')),ctx);
const display = {currency:'CNY',source:'ECB',date:'2026-11-05',stale:false,
  rates:{EUR:'1',USD:'2',CNY:'8',GBP:'0.5',CAD:'1',JPY:'100',HKD:'9',AUD:'1.5'}};
const node = (currency,amount,period='1m',region='US') => ({agent:{id:1,region,billing_period:period,billing_cost:{currency,amount}},_online:false});
const total = (nodes,fx=display)=>JSON.parse(JSON.stringify(ctx.monthlyCostTotal(nodes,fx)));
const fixtures = [node('USD','10'),node('EUR','30','3m'),node('CNY','120','12m','CN'),node('GBP','36','36m'),
  node('CAD','24','24m'),node('CNY','60','6m','CN'),node('USD','0'),node('USD','1000','forever')];
assert.deepEqual(total(fixtures),{amount:'164.00',unpriced:0,unconverted:0});
assert.equal(total([node('CNY','0.06','12m'),node('CNY','0.06','12m')]).amount,'0.01','round once after summing');
assert.equal(total([node('CNY','999999999999.999999')]).amount,'1,000,000,000,000.00');
assert.equal(total([node('EUR','1')],{...display,currency:'JPY'}).amount,'100');
assert.equal(total([node('USD','12')],{...display,currency:'USD',rates:{}}).amount,'12.00','same-currency totals need no FX');
assert.deepEqual(total([node('USD','12')],{...display,rates:{}}),{amount:null,unpriced:0,unconverted:1});
assert.deepEqual(total([node('USD','0'),node('USD','1000','forever')]),{amount:'0.00',unpriced:0,unconverted:0});
const mixed = [...fixtures,node('USD',''),node('ZZZ','20')];
assert.deepEqual(total(mixed),{amount:'164.00',unpriced:1,unconverted:1});
assert.equal(total([node('USD','5','constructor')]).unpriced,1);
assert.equal(total([node('USD','NaN')]).unpriced,1);
assert.equal(total([node('USD','-5')]).unpriced,1);
assert.equal(total([node('USD','5')],{...display,rates:{...display.rates,USD:'0'}}).unconverted,1);

ctx.fixtures = fixtures;
vm.runInContext('agents = fixtures',ctx);
ctx.applyCostDisplay(display);
const html = ()=>elements.get('cost-summary').innerHTML;
assert(html().includes('CN¥164.00'));
assert(!html().includes('Rates:') && !html().includes('title='));
const visibleTotal = html();
ctx.applyCostDisplay({...display,hidden:true});
assert(elements.get('cost-summary').hidden && html()==='', 'hidden summaries must not leave stale amounts in the DOM');
ctx.renderCostSummary();
assert(elements.get('cost-summary').hidden, 'ordinary node refresh must respect hidden costs');
ctx.applyCostDisplay({...display,hidden:'true'});
assert(elements.get('cost-summary').hidden, 'invalid metadata must not change visibility');
ctx.applyCostDisplay({...display,hidden:false});
assert(!elements.get('cost-summary').hidden && html()===visibleTotal, 'showing costs restores the same calculation');
vm.runInContext("filterRegion = 'CN'",ctx);ctx.renderCostSummary();
assert(html().includes('CN¥20.00'),'region filtering changes only the displayed subtotal');
vm.runInContext("filterRegion = ''",ctx);
ctx.applyCostDisplay({...display,stale:true,date:'2026-10-01'});
assert(html().includes('CN¥164.00') && html().includes('Rates: 2026-10-01'),'old rates remain usable');
ctx.fixtures = [...fixtures,node('USD','5')];vm.runInContext('agents = fixtures',ctx);ctx.renderCostSummary();
assert(html().includes('CN¥184.00'),'new costs must use cached rates rather than freezing an old total');
for (const bad of [null,{}, {...display,currency:'<script>'},{...display,rates:[]},{...display,date:'<img>'}]) ctx.applyCostDisplay(bad);
assert(html().includes('CN¥184.00'),'invalid metadata must preserve the last working estimate');
ctx.applyCostDisplay({...display,currency:'USD'});
assert(html().includes('US$46.00'),'display currency changes without touching node prices');
ctx.fixtures=mixed;vm.runInContext('agents = fixtures',ctx);ctx.renderCostSummary();
assert(html().includes('1 unpriced') && html().includes('1 unconverted'));
ctx.fixtures=[node('USD','12')];vm.runInContext('agents = fixtures',ctx);
ctx.applyCostDisplay({...display,rates:{}});
assert(html().includes('Rates unavailable') && !html().includes('CN¥0.00'));
vm.runInContext('agents = []',ctx);ctx.renderCostSummary();
assert(elements.get('cost-summary').hidden);

// Live SSE metadata updates use the existing connection, with no external API
// requests or chart redraws. HTTP fallback is covered by refreshServerInfo.
const streams=[];
ctx.EventSource=class {constructor(url){this.url=url;this.events={};streams.push(this);}addEventListener(k,v){this.events[k]=v;}close(){}};
ctx.connectEvents();
ctx.fixtures=fixtures;vm.runInContext('agents = fixtures',ctx);
streams[0].events.costs({data:JSON.stringify(display)});
assert(html().includes('CN¥164.00'));
streams[0].events.costs({data:JSON.stringify({...display,hidden:true})});
assert(elements.get('cost-summary').hidden && html()==='', 'SSE must apply saved visibility without reload');
streams[0].events.costs({data:JSON.stringify({...display,hidden:false})});
assert(!elements.get('cost-summary').hidden && html().includes('CN¥164.00'));
ctx.stopEvents();
const prior=html();streams[0].events.costs({data:JSON.stringify({...display,currency:'USD'})});
assert.equal(html(),prior,'closed streams must not replace the latest display settings');

const deferred=()=>{let resolve;const promise=new Promise(done=>resolve=done);return {promise,resolve};};
const response=data=>({ok:true,json:async()=>data});
const savedDisplay=()=>JSON.parse(vm.runInContext('JSON.stringify(costDisplay)',ctx));

(async()=>{
  ctx.connectEvents();
  const stream=streams.at(-1);
  ctx.fixtures=fixtures;vm.runInContext('agents = fixtures',ctx);
  elements.set('version-label',{textContent:'',title:''});
  for(const scenario of [
    {delay:'headers',live:{...display,hidden:true}},
    {delay:'body',live:{...display,currency:'USD'}},
    {delay:'body',live:{...display,date:'2026-11-06',stale:true,rates:{...display.rates,USD:'3',CNY:'9'}}},
  ]){
    ctx.applyCostDisplay(display);
    const pending=deferred();
    ctx.fetch=()=>scenario.delay==='headers'?pending.promise:Promise.resolve({ok:true,json:()=>pending.promise});
    const request=ctx.refreshServerInfo();
    await Promise.resolve();await Promise.resolve();
    stream.events.costs({data:JSON.stringify(scenario.live)});
    const liveHTML=html(),liveHidden=elements.get('cost-summary').hidden;
    const oldInfo={cost_display:display,version:'v9.9.9-test'};
    pending.resolve(scenario.delay==='headers'?response(oldInfo):oldInfo);
    await request;
    assert.deepEqual(savedDisplay(),scenario.live,'a delayed HTTP response must not replace newer live visibility, currency or rates');
    assert.equal(html(),liveHTML,'old HTTP metadata must not redraw the live total');
    assert.equal(elements.get('cost-summary').hidden,liveHidden,'old HTTP metadata must not reveal a hidden total');
    assert.equal(elements.get('version-label').textContent,'v9.9.9','cost ordering must not suppress unrelated Server metadata');
  }

  // Invalid live metadata must not invalidate a pending good HTTP response.
  const pending=deferred();
  ctx.fetch=()=>pending.promise;
  const request=ctx.refreshServerInfo();
  stream.events.costs({data:JSON.stringify({...display,hidden:'invalid'})});
  const goodHTTP={...display,currency:'GBP',hidden:false};
  pending.resolve(response({cost_display:goodHTTP}));
  await request;
  assert.deepEqual(savedDisplay(),goodHTTP);

  // Polling remains authoritative once there is no newer valid live update.
  ctx.stopEvents();
  const fallback={...display,currency:'CAD',hidden:false};
  ctx.fetch=async()=>response({cost_display:fallback});
  await ctx.refreshServerInfo();
  assert.deepEqual(savedDisplay(),fallback,'prior SSE use must not permanently disable HTTP fallback');
  stream.events.costs({data:JSON.stringify({...display,hidden:true})});
  assert.deepEqual(savedDisplay(),fallback,'closed SSE streams must not affect fallback costs');
  console.log('Cost summary checks passed: exact totals, cached rates, visibility, SSE/HTTP ordering, invalid updates and polling fallback.');
})().catch(error=>{console.error(error);process.exitCode=1;}).finally(()=>ctx.stopEvents());
