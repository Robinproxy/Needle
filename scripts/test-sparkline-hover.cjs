const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const echarts = require('../internal/server/static/vendor/echarts.min.js');

// Exercise the bundled renderer's real emphasis state, rather than only
// checking an option object: an invalid hover color removes the SVG stroke.
const source = fs.readFileSync(path.join(__dirname, '../internal/server/static/app.js'), 'utf8');
const elements = new Map();
const charts = new Map();
const context = {
  localStorage: { getItem: () => null },
  document: { documentElement: {}, getElementById: id => elements.get(id) },
  getComputedStyle: () => ({ getPropertyValue: () => context.channels }),
  echarts: { init: el => {
    const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width: 500, height: 100 });
    chart.setOption({ animation: false, stateAnimation: { duration: 0 } });
    charts.set(el.id, chart);
    return chart;
  } },
};
vm.createContext(context);
vm.runInContext(source.slice(0, source.indexOf('let themeMode =')), context);

function assertVisibleLine(chart, label) {
  const svg = chart.renderToSVGString();
  const strokes = [...svg.matchAll(/stroke="([^"]+)"/g)].map(match => match[1]);
  assert(strokes.some(color => color !== 'none' && color !== 'transparent'), label + ': line stroke disappeared');
  assert(!svg.includes('NaN'), label + ': invalid rendered geometry');
}

(async () => {
  for (const [theme, channels] of [['light', '215 35% 48%'], ['dark', '215 40% 68%']]) {
    context.channels = channels;
    for (const [id, values, percent] of [
      ['spark-cpu', [15, 28, null, 20], true],
      ['spark-mem', [25, 25, 25, 25], true],
      ['spark-netin', [1000, 2500, null, 1700], false],
      ['spark-netout', [320000, 320000, 320000, 320000], false],
      ['spark-netout-zero', [0, 0, 0, 0], false],
    ]) {
      elements.set(id, { id, innerHTML: '' });
      const data = values.map((value, i) => [1700000000000 + i * 30000, value]);
      context.renderSparkline(id, data, percent);
      const chart = charts.get(id);
      assertVisibleLine(chart, theme + ' ' + id + ' normal');
      assert.equal(chart.getOption().tooltip[0].trigger, 'axis', 'hover tooltip must remain enabled');
      chart.dispatchAction({ type: 'highlight', seriesIndex: 0 });
    }
    // ECharts applies emphasis during its next rendering frame, including SSR.
    await new Promise(resolve => setTimeout(resolve, 40));
    for (const [id, chart] of charts) {
      chart.getZr().flush();
      assertVisibleLine(chart, theme + ' ' + id + ' hover');
      chart.dispatchAction({ type: 'downplay', seriesIndex: 0 });
    }
    await new Promise(resolve => setTimeout(resolve, 40));
    for (const [id, chart] of charts) {
      assertVisibleLine(chart, theme + ' ' + id + ' pointer leave');
      chart.dispose();
    }
    charts.clear();
    vm.runInContext('sparkCharts = {};', context);
  }
  console.log('Sparkline hover checks passed: four metrics, light/dark themes, flat and zero values, gaps, emphasis, and pointer leave.');
})().catch(error => {
  for (const chart of charts.values()) chart.dispose();
  console.error(error);
  process.exitCode = 1;
});
