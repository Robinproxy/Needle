const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const elements = new Map(), storage = new Map();
const context = {
  console, setTimeout, clearTimeout,
  localStorage: {getItem: key => storage.get(key) ?? null, setItem: (key, value) => storage.set(key, value)},
  document: {
    documentElement: {classList: {contains: () => false}},
    getElementById: id => elements.get(id), querySelectorAll: () => [],
  },
};
vm.createContext(context);
vm.runInContext(fs.readFileSync(path.join(__dirname, '../internal/server/static/vendor/echarts.min.js'), 'utf8'), context);
const echarts = context.echarts;
echarts.setPlatformAPI({measureText: text => ({width:String(text).length * 7})});
const source = fs.readFileSync(path.join(__dirname, '../internal/server/static/app.js'), 'utf8');
vm.runInContext(source.slice(0, source.indexOf('let themeMode =')), context);
const plain = value => JSON.parse(JSON.stringify(value));
const points = (values, step = 30000) => values.map((value, i) => [1700000000000 + i * step, value]);
const filter = (values, step) => plain(context.despikeLatency(points(values, step)));
const spike = [180,181,180,180,2000,180,181,180,180];
assert.deepEqual(filter(spike), [180,181,180,180,180,180,181,180,180]);
assert.deepEqual(filter([20,20,20,900,900,20,20,20]), [20,20,20,20,20,20,20,20]);
for (const values of [
  [180,181,180,181,180,181,180], // normal jitter
  [180,190,185,195,180,191,180], // small bumps are not worth suppressing
  [0,0,0,0,0,0,0],
  [20,20,20,900,900,900,20,20,20], // three-sample plateau
  [20,20,20,900,900,900,900,900,900], // sustained degradation
  [20,20,null,900,20,20,20], // timeout next to a peak
  [900,20,20,20,20,20,900], // no evidence on both sides
  [20,900,20], // too few samples
  [180,180,180,10,180,180,180], // low readings are never raised
]) assert.deepEqual(filter(values), values);
assert.deepEqual(filter(spike, 120000), spike, 'sparse probes must not be treated as brief spikes');
assert.deepEqual(filter([20,20,20,900,900,20,20,20], 60000), [20,20,20,900,900,20,20,20], 'two-minute events stay visible');
const gapped = points(spike);
gapped.slice(4).forEach(p => {p[0] += 600000;});
assert.deepEqual(plain(context.despikeLatency(gapped)), spike, 'do not compare across an offline gap');
const originals = points(spike);
context.despikeLatency(originals);
assert.deepEqual(originals, points(spike), 'never mutate original samples');

// Exercise the real bundled renderer: the original value is a separate tooltip
// dimension and must not keep the y-axis at 2000 after the line is despiked.
const chart = echarts.init(null, null, {renderer:'svg', ssr:true, width:900, height:240});
context.echarts = {init: () => chart};
context.fixture = [{agent:{id:1,hostname:'test',report_interval:30}, tcpping_enabled:true,
  latest_tcpping:[{name:'CTv4'},{name:'CUv6'}]}];
vm.runInContext('agents = fixture;', context);
for (const id of ['tcpping-chart-1','tcpping-days-1','tcpping-rows-1']) elements.set(id,{innerHTML:''});
const classes = new Set(), attributes = new Map();
const button = {classList:{toggle:(name,on)=>on?classes.add(name):classes.delete(name)},
  setAttribute:(name,value)=>attributes.set(name,value)};
elements.set('tcpping-despike-1',button);
const history = points(spike).map(([ts,latency_ms]) => ({created_at:ts/1000,name:'CTv4',latency_ms,success:true}));
history.push(...points(spike.map(() => 25)).map(([ts,latency_ms]) => ({created_at:ts/1000,name:'CUv6',latency_ms,success:true})));
const before = JSON.stringify(history);
try {
  context.renderTCPingChart(1,history);
  const axisTop = () => chart.getModel().getComponent('yAxis').axis.scale.getExtent()[1];
  assert(axisTop() >= 2000);
  const ctSeries = () => chart.getOption().series.find(s => s.name === 'CTv4');
  const stats = elements.get('tcpping-rows-1').innerHTML;
  context.setTcppingSelection(1,false);
  context.tcppingToggleLine(1,'CTv4');
  const selection = JSON.stringify(chart.getOption().legend[0].selected);
  context.toggleTcppingDespike(1);
  assert.equal(storage.get('tcppingDespike'),'true');
  assert(classes.has('active')); assert.equal(attributes.get('aria-pressed'),'true');
  assert(axisTop() < 300, 'raw tooltip dimension must not expand the plotted axis');
  assert.equal(JSON.stringify(chart.getOption().legend[0].selected),selection);
  assert.equal(elements.get('tcpping-rows-1').innerHTML,stats);
  const data = ctSeries().data;
  assert.deepEqual(plain(data[4]), [originals[4][0],180,2000]);
  const tooltip = context.tcppingTooltip([{data:data[4],seriesName:'CTv4'}]);
  assert(tooltip.includes('2000.0 ms') && tooltip.includes('Displayed: 180.0 ms'));
  assert(context.tcppingTooltip([{data:[0,0,0],seriesName:'CTv4'}]).includes('0.0 ms'));
  assert(context.tcppingTooltip([{data:[0,null,null],seriesName:'CTv4'}]).includes('timeout'));
  assert(!context.tcppingTooltip([{data:data[4],seriesName:'<img onerror=bad>'}]).includes('<img'));
  context.toggleTcppingDespike(1);
  assert(axisTop() >= 2000);
  assert.equal(ctSeries().data[4][1],2000);
  context.toggleTcppingDespike(1);
  for (const range of ['168h','720h']) {
    vm.runInContext(`currentHistoryRange = '${range}'`,context);
    context.updateTcppingDespikeButton(1);
    assert(button.disabled); assert(!classes.has('active'));
    context.toggleTcppingDespike(1);
    assert.equal(storage.get('tcppingDespike'),'true','disabled button must not reset the preference');
    context.renderTCPingChart(1,history);
    assert.equal(ctSeries().data[4][1],2000);
  }
  vm.runInContext("currentHistoryRange = 'day'",context);
  context.updateTcppingDespikeButton(1);
  context.renderTCPingChart(1,history);
  assert(!button.disabled && classes.has('active'));
  assert.equal(ctSeries().data[4][1],180);
  assert.equal(JSON.stringify(history),before);
  assert(!chart.renderToSVGString().includes('NaN'));
  context.document.getElementById = () => null;
  context.localStorage.setItem = () => {throw new Error('storage unavailable');};
  assert.doesNotThrow(() => context.toggleTcppingDespike(1));
} finally {chart.dispose();}

console.log('TCP Ping despike checks passed: isolated peaks, sustained latency, gaps, raw tooltips/statistics, axis rescale, selections, and ranges.');
