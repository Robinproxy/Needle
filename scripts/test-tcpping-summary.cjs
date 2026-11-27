const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const source = fs.readFileSync(path.join(__dirname,'../internal/server/static/app.js'),'utf8');
const context={console,localStorage:{getItem:()=>null},document:{getElementById:()=>null}};
vm.createContext(context);
vm.runInContext(source.slice(0,source.indexOf('let themeMode =')),context);
const probe={name:'CMv4',target:'example.com:80',latency_ms:999,success:true};
const summary={name:probe.name,target:probe.target,latency_ms:72,sample_count:120,success_count:119};
const node={agent:{id:1,hostname:'test'},tcpping_enabled:true,latest_tcpping:[probe],tcpping_summary:[summary]};
const card=context.cardProbesHTML(node);
assert(card.includes('72ms') && card.includes('0.8%'));
assert(!card.includes('999ms'),'latest latency must not replace the hourly mean');
assert(!card.includes(' failed'),'healthy latest probe is distinct from historical loss');
const list=context.renderListRow(node,0,false);
assert(list.includes('72.0ms') && list.includes('0.8%'),'list and card must use the same window');
const failing={...node,latest_tcpping:[{...probe,success:false}]};
const red=context.cardProbesHTML(failing);
assert(red.includes('probe-row failed') && red.includes('72ms') && red.includes('0.8%'));
assert.equal(context.probeSummary(failing,failing.latest_tcpping[0]).latencyColor,context.valCss('red'));
assert.equal(context.probeSummary(failing,failing.latest_tcpping[0]).lossColor,context.valCss('red'));
assert(context.renderListRow(failing,0,false).includes('list-ping failed'));
const failedHour={...failing,tcpping_summary:[{...summary,sample_count:3,success_count:0,latency_ms:0}]};
assert(context.cardProbesHTML(failedHour).includes('100%'));
assert(!context.cardProbesHTML(failedHour).includes('0ms'),'all failures have no measured latency');
const zero={...node,tcpping_summary:[{...summary,latency_ms:0,success_count:120}]};
assert(context.cardProbesHTML(zero).includes('0ms'));
assert.equal(context.probeSummary(zero,probe).latencyColor,context.valCss('gray'));
for(const data of [undefined,[],[{...summary,target:'previous.example:80'}],[{...summary,sample_count:0,success_count:0}]]){
  const empty={...node,tcpping_summary:data};
  const stats=context.probeSummary(empty,probe);
  assert.equal(stats.latency,null);assert.equal(stats.loss,null);
  assert(!context.cardProbesHTML(empty).includes('0%'),'no samples must not look healthy');
  assert(!context.cardProbesHTML(empty).includes('999ms'),'no fallback to a differently scoped instant value');
}
assert.equal(context.cardProbesHTML({...node,tcpping_enabled:false}),'');

// A delayed full report cannot replace a newer committed probe snapshot, but
// an equal-history snapshot can refresh the rolling window as old samples age.
const previous={...node,revision:1,live:{last_full_sample_at:100},agent:{...node.agent,last_seen:100}};
const incoming={...previous,revision:2,live:{last_full_sample_at:90},tcpping_summary:[]};
assert.equal(context.mergeLiveNode(previous,incoming).tcpping_summary,previous.tcpping_summary);
const aged={...incoming,live:{last_full_sample_at:100}};
assert.equal(context.mergeLiveNode(previous,aged).tcpping_summary,aged.tcpping_summary);
console.log('TCP Ping summary checks passed: hourly mean/loss, current failures, target isolation, no-data/zero handling, both layouts and snapshot ordering.');
