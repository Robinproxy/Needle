let agents = [];
let expandedId = null;
let sparkCharts = {};
let tcppingChart = null;
let filterRegion = '';
let cardFormat = localStorage.getItem('cardFormat') || 'card';
let infoData = null;
let currentTcppingRange = '24h';
let currentTcppingId = null;
let currentMetricsRange = '1h';
let gridCols = 4;

function escapeHtml(s) {
  if (typeof s !== 'string') return s;
  return s.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;').replace(/'/g,'&#39;');
}

function mapCarrier(name) {
  return name.replace(/^移动/, 'CM').replace(/^联通/, 'CU').replace(/^电信/, 'CT');
}


function getGridCols() {
  const w = window.innerWidth;
  if (w >= 1280) return 4;
  if (w >= 1024) return 3;
  if (w >= 640) return 2;
  return 1;
}

function formatBytes(b) {
  if (!b || b === 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  const i = Math.floor(Math.log(b) / Math.log(1024));
  return (b / Math.pow(1024, i)).toFixed(b > 1073741824 ? 1 : 0) + ' ' + units[i];
}

function formatSpeed(bps) {
  if (!bps || bps <= 0) return '0';
  if (bps < 1000) return bps.toFixed(0);
  if (bps < 1000000) return (bps / 1000).toFixed(bps > 10000 ? 0 : 1) + 'K';
  return (bps / 1000000).toFixed(bps > 10000000 ? 1 : 2) + 'M';
}

function formatUptime(s) {
  if (!s || s <= 0) return '-';
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  if (d > 0) return d + 'd ' + h + 'h';
  const m = Math.floor((s % 3600) / 60);
  return h + 'h ' + m + 'm';
}

function flagEmoji(code) {
  if (!code || code.length !== 2) return '\u{1F310}';
  const cp = 0x1F1E6 + code.toUpperCase().charCodeAt(0) - 65;
  const cp2 = 0x1F1E6 + code.toUpperCase().charCodeAt(1) - 65;
  return String.fromCodePoint(cp) + String.fromCodePoint(cp2);
}

function pct(v) {
  return v != null ? v.toFixed(1) + '%' : '—';
}

function metricColor(v) {
  if (v == null) return 'blue';
  if (v >= 90) return 'red';
  if (v >= 70) return 'amber';
  if (v >= 40) return 'green';
  return 'blue';
}

function setRegionFilter(code) {
  filterRegion = filterRegion === code ? '' : code;
  renderAll();
  renderInfoBar();
}

function renderInfoBar() {
  const d = infoData?.db_stats || {};
  const now = Date.now();
  const online = agents.filter(a => {
    const m = a.latest_metric;
    return m && (now - m.created_at * 1000 < 120000);
  }).length;
  const total = d.agent_count || 0;

  const regions = {};
  agents.forEach(a => {
    const r = a.agent.region || '\u{1F310}';
    regions[r] = (regions[r] || 0) + 1;
  });
  const regionHtml = Object.entries(regions)
    .sort((a, b) => b[1] - a[1])
    .map(([code, count]) => {
      const label = code.length === 2 ? flagEmoji(code) : code;
      const active = filterRegion === code;
      return '<span class="item region-filter' + (active ? ' active' : '') + '" onclick="setRegionFilter(\'' + escapeHtml(code) + '\')"><span class="value pri">' + label + '</span> <span class="value">' + count + '</span></span>';
    })
    .join('');

  document.getElementById('server-info').innerHTML =
    '<span class="item' + (!filterRegion ? ' active' : '') + '" onclick="setRegionFilter(\'\')" style="cursor:pointer"><span class="label">NODES</span> <span class="value">' + total + '</span> <span class="value pri">(' + online + ' online, ' + (total - online) + ' offline)</span></span>'
    + regionHtml;
}

function loadServerInfo() {
  fetch('/api/info').then(r => r.json()).then(info => {
    infoData = info;
    renderInfoBar();
  }).catch(() => {});
}

function filterAndSort(list) {
  let arr = [...list];
  if (filterRegion) {
    arr = arr.filter(a => a.agent.region === filterRegion);
  }
  arr.sort((a, b) => a.agent.id - b.agent.id);
  return arr;
}

function toggleCardFormat() {
  cardFormat = cardFormat === 'card' ? 'list' : 'card';
  localStorage.setItem('cardFormat', cardFormat);
  renderAll();
  if (expandedId) {
    const agent = agents.find(a => a.agent.id === expandedId);
    if (!agent) expandedId = null;
  }
}

function renderAll() {
  const container = document.getElementById('card-grid');
  const empty = document.getElementById('empty-state');

  container.className = cardFormat === 'list' ? 'list-grid' : 'card-grid';

  if (!agents.length) {
    container.innerHTML = '';
    empty.style.display = 'flex';
    document.getElementById('node-count-sub').textContent = '';
    return;
  }
  empty.style.display = 'none';

  const filtered = filterAndSort(agents);
  document.getElementById('node-count-sub').textContent = filtered.length + ' of ' + agents.length;

  if (!filtered.length) {
    container.innerHTML = '<div class="empty-state" style="display:flex;grid-column:1/-1">No matching nodes</div>';
    return;
  }

  const isList = cardFormat === 'list';
  gridCols = getGridCols();

  // Calculate detail slot: insert after the last card in the expanded card's row
  let slotIdx = -1;
  if (expandedId) {
    const ei = filtered.findIndex(a => a.agent.id === expandedId);
    if (ei >= 0) {
      const row = Math.floor(ei / gridCols);
      slotIdx = Math.min((row + 1) * gridCols - 1, filtered.length - 1);
    }
  }

  let html = '';
  for (let idx = 0; idx < filtered.length; idx++) {
    const a = filtered[idx];
    const isActive = expandedId === a.agent.id;

    if (isList) {
      html += renderListRow(a, idx, isActive);
      if (isActive) {
        html += '<div class="inline-detail" id="detail-' + a.agent.id + '"></div>';
      }
    } else {
      html += renderCard(a, idx, isActive);
      if (idx === slotIdx && expandedId) {
        html += '<div class="inline-detail" id="detail-' + expandedId + '"></div>';
      }
    }
  }
  container.innerHTML = html;

  if (expandedId) {
    const agent = filtered.find(a => a.agent.id === expandedId);
    if (agent) {
      renderDetailContent(expandedId);
      // scroll to detail
      setTimeout(() => {
        const el = document.getElementById('detail-' + expandedId);
        if (el) el.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
      }, 100);
    }
  }
}

function renderCard(a, idx, isActive) {
  const m = a.latest_metric;
  const now = Date.now();
  const isOnline = m && (now - m.created_at * 1000 < 120000);
  const cpu = m ? m.cpu_usage : 0;
  const memPct = m ? (m.memory_used / m.memory_total * 100) : 0;
  const memStr = m ? formatBytes(m.memory_used) + ' / ' + formatBytes(m.memory_total) : '';
  const diskPct = m ? (m.disk_used / m.disk_total * 100) : 0;
  const diskStr = m ? formatBytes(m.disk_used) + ' / ' + formatBytes(m.disk_total) : '';
  const upSpeed = m ? formatSpeed(m.network_up) : '0';
  const downSpeed = m ? formatSpeed(m.network_down) : '0';
  const uptime = m ? formatUptime(m.uptime) : '-';
  const sess = String(idx + 1).padStart(2, '0');

  // TCPing card line — always use first target
  let pingHtml = '';
  if (a.latest_tcpping && a.latest_tcpping.length > 0) {
    const p = a.latest_tcpping[0];
    const latStr = p.success ? p.latency_ms.toFixed(1) + 'ms' : 'timeout';
    const lossStr = p.success ? '0%' : '100%';
    pingHtml = '<div class="card-ping"><span class="ping-dot"></span><span class="ping-label">' + escapeHtml(mapCarrier(p.name)) + '</span><span class="ping-lat">Latency ' + latStr + '</span><span class="ping-loss">Loss ' + lossStr + '</span></div>';
  }

  return '<div class="card' + (isActive ? ' active' : '') + (!isOnline ? ' offline' : '') + '" onclick="toggleExpand(' + a.agent.id + ')" data-id="' + a.agent.id + '">'
    + '<div class="card-top">'
      + '<span class="status-dot ' + (isOnline ? 'online' : 'offline') + '"></span>'
      + '<span class="card-hostname">' + escapeHtml(a.agent.hostname) + '</span>'
      + '<span class="card-session">' + flagEmoji(a.agent.region) + '</span>'
    + '</div>'
    + '<div class="card-sub">' + uptime + '</div>'
    + '<div class="card-metrics">'
      + '<div class="metric"><div class="metric-header"><span class="label">CPU</span><span class="value" style="color:hsl(var(--primary))">' + pct(cpu) + '</span></div><div class="metric-bar"><div class="metric-fill ' + metricColor(cpu) + '" style="width:' + cpu.toFixed(0) + '%"></div></div></div>'
      + '<div class="metric"><div class="metric-header"><span class="label">MEM</span><span class="value" style="color:hsl(var(--success))">' + pct(memPct) + '</span></div><div class="metric-bar"><div class="metric-fill ' + metricColor(memPct) + '" style="width:' + memPct.toFixed(0) + '%"></div></div><div class="metric-sub">' + memStr + '</div></div>'
      + '<div class="metric"><div class="metric-header"><span class="label">DSK</span><span class="value" style="color:hsl(var(--warning))">' + pct(diskPct) + '</span></div><div class="metric-bar"><div class="metric-fill ' + metricColor(diskPct) + '" style="width:' + diskPct.toFixed(0) + '%"></div></div><div class="metric-sub">' + diskStr + '</div></div>'
    + '</div>'
    + pingHtml
    + '<div class="card-footer-line"><div class="net"><span>\u2193 ' + downSpeed + '</span><span>\u2191 ' + upSpeed + '</span></div><span>' + (m ? relativeTime(m.created_at * 1000) : '') + '</span></div>'
  + '</div>';
}

function renderListRow(a, idx, isActive) {
  const m = a.latest_metric;
  const now = Date.now();
  const isOnline = m && (now - m.created_at * 1000 < 120000);
  const cpu = m ? m.cpu_usage : 0;
  const memPct = m ? (m.memory_used / m.memory_total * 100) : 0;
  const diskPct = m ? (m.disk_used / m.disk_total * 100) : 0;
  const upSpeed = m ? formatSpeed(m.network_up) : '0';
  const downSpeed = m ? formatSpeed(m.network_down) : '0';
  const uptime = m ? formatUptime(m.uptime) : '-';
  const region = a.agent.region;
  const regionLabel = region && region.length === 2 ? flagEmoji(region) : escapeHtml(region || '');
  const sess = String(idx + 1).padStart(2, '0');

  return '<div class="list-row' + (isActive ? ' active' : '') + ' ' + (!isOnline ? 'offline' : '') + '" onclick="toggleExpand(' + a.agent.id + ')" data-id="' + a.agent.id + '">'
    + '<span class="status-dot ' + (isOnline ? 'online' : 'offline') + '"></span>'
    + '<span class="list-hostname">' + escapeHtml(a.agent.hostname) + '</span>'
    + '<span class="list-region">' + regionLabel + '</span>'
    + '<span class="list-session">#' + sess + '</span>'
    + '<div class="list-bars">'
      + '<div class="list-bar"><span class="list-bar-label">CPU</span><div class="metric-bar"><div class="metric-fill ' + metricColor(cpu) + '" style="width:' + cpu.toFixed(0) + '%"></div></div><span class="list-bar-val">' + pct(cpu) + '</span></div>'
      + '<div class="list-bar"><span class="list-bar-label">MEM</span><div class="metric-bar"><div class="metric-fill ' + metricColor(memPct) + '" style="width:' + memPct.toFixed(0) + '%"></div></div><span class="list-bar-val">' + pct(memPct) + '</span></div>'
      + '<div class="list-bar"><span class="list-bar-label">DSK</span><div class="metric-bar"><div class="metric-fill ' + metricColor(diskPct) + '" style="width:' + diskPct.toFixed(0) + '%"></div></div><span class="list-bar-val">' + pct(diskPct) + '</span></div>'
    + '</div>'
    + '<span class="list-net">\u2193' + downSpeed + ' \u2191' + upSpeed + '</span>'
    + '<span class="list-uptime">' + uptime + '</span>'
    + '<span class="list-time">' + (m ? relativeTime(m.created_at * 1000) : '') + '</span>'
  + '</div>';
}

function relativeTime(ts) {
  const diff = Math.floor((Date.now() - ts) / 1000);
  if (diff < 60) return 'just now';
  if (diff < 3600) return Math.floor(diff / 60) + 'm ago';
  return Math.floor(diff / 3600) + 'h ago';
}

function toggleExpand(id) {
  if (expandedId === id) {
    expandedId = null;
    destroyDetailCharts();
    renderAll();
  } else {
    destroyDetailCharts();
    expandedId = id;
    renderAll();
  }
}

function destroyDetailCharts() {
  Object.values(sparkCharts).forEach(c => { try { c.dispose(); } catch(e) {} });
  sparkCharts = {};
  if (tcppingChart) { try { tcppingChart.dispose(); } catch(e) {}; tcppingChart = null; }
}

function renderDetailContent(id) {
  const detailEl = document.getElementById('detail-' + id);
  if (!detailEl) return;

  const agent = agents.find(a => a.agent.id === id);
  if (!agent) return;

  detailEl.innerHTML = '<div class="detail-header">'
    + '<div class="detail-title">' + escapeHtml(agent.agent.hostname) + ' <span class="sub">Node Detail</span></div>'
    + '<button class="btn btn-ghost btn-sm" onclick="toggleExpand(' + id + ')">\u2715</button>'
    + '</div>'
    + '<div class="detail-range-bar">'
      + '<div class="range-group">'
        + '<button class="tcpping-range-btn' + (currentMetricsRange === '1h' ? ' active' : '') + '" data-range="1h" onclick="switchMetricsRange(\'' + id + '\',\'1h\')">1h</button>'
        + '<button class="tcpping-range-btn' + (currentMetricsRange === '24h' ? ' active' : '') + '" data-range="24h" onclick="switchMetricsRange(\'' + id + '\',\'24h\')">1d</button>'
        + '<button class="tcpping-range-btn' + (currentMetricsRange === '168h' ? ' active' : '') + '" data-range="168h" onclick="switchMetricsRange(\'' + id + '\',\'168h\')">7d</button>'
        + '<button class="tcpping-range-btn' + (currentMetricsRange === '720h' ? ' active' : '') + '" data-range="720h" onclick="switchMetricsRange(\'' + id + '\',\'720h\')">30d</button>'
      + '</div>'
    + '</div>'
    + '<div class="spark-grid">'
      + '<div class="spark-item"><div class="spark-header"><span class="label">CPU</span><span class="value" id="spark-cpu-val">—</span></div><div id="spark-cpu" class="spark-chart"></div></div>'
      + '<div class="spark-item"><div class="spark-header"><span class="label">Memory</span><span class="value" id="spark-mem-val">—</span></div><div id="spark-mem" class="spark-chart"></div></div>'
      + '<div class="spark-item"><div class="spark-header"><span class="label">Net In</span><span class="value" id="spark-netin-val">—</span></div><div id="spark-netin" class="spark-chart"></div></div>'
      + '<div class="spark-item"><div class="spark-header"><span class="label">Net Out</span><span class="value" id="spark-netout-val">—</span></div><div id="spark-netout" class="spark-chart"></div></div>'
    + '</div>'
    + '<div class="tcpping-section" id="tcpping-section-' + id + '">'
      + '<div class="tcpping-header">'
        + '<div class="tcpping-header-left">'
          + '<h3>TCP Ping</h3>'
          + '<div class="tcpping-range-group">'
            + '<button class="tcpping-range-btn active" data-range="1h" onclick="switchTcppingRange(\'' + id + '\',\'1h\')">1h</button>'
            + '<button class="tcpping-range-btn" data-range="24h" onclick="switchTcppingRange(\'' + id + '\',\'24h\')">1d</button>'
            + '<button class="tcpping-range-btn" data-range="168h" onclick="switchTcppingRange(\'' + id + '\',\'168h\')">7d</button>'
          + '</div>'
        + '</div>'
        + '<div class="tcpping-controls">'
          + '<button class="btn btn-xs btn-soft" onclick="tcppingSelectAll(\'' + id + '\')">\u2713 All</button>'
          + '<button class="btn btn-xs btn-ghost" onclick="tcppingSelectNone(\'' + id + '\')">Hide All</button>'
        + '</div>'
      + '</div>'
      + '<div class="tcpping-chart-wrap"><div id="tcpping-chart-' + id + '" class="tcpping-chart"></div></div>'
      + '<div class="tcpping-stats" id="tcpping-stats-' + id + '">'
        + '<div class="tcpping-stats-header"><span class="col-dot"></span><span class="col-name">Source</span><span class="col-stat">Avg</span><span class="col-stat">Jitter</span><span class="col-stat">Loss</span></div>'
        + '<div id="tcpping-rows-' + id + '"></div>'
      + '</div>'
    + '</div>'
    + '<div class="detail-sections">'
      + '<div class="detail-section"><div class="detail-section-title">System</div><table class="info-table"><tbody id="info-sys-' + id + '"></tbody></table></div>'
      + '<div class="detail-section"><div class="detail-section-title">Network &amp; Load</div><table class="info-table"><tbody id="info-net-' + id + '"></tbody></table></div>'
    + '</div>';

  const m = agent.latest_metric;
  if (m) {
    document.getElementById('info-sys-' + id).innerHTML =
      '<tr><td>Hostname</td><td>' + escapeHtml(agent.agent.hostname) + '</td></tr>'
      + '<tr><td>Uptime</td><td>' + formatUptime(m.uptime) + '</td></tr>'
      + '<tr><td>Memory</td><td>' + formatBytes(m.memory_total) + '</td></tr>'
      + '<tr><td>Disk</td><td>' + formatBytes(m.disk_total) + '</td></tr>';
    document.getElementById('info-net-' + id).innerHTML =
      '<tr><td>Net Up</td><td>' + formatSpeed(m.network_up) + '/s</td></tr>'
      + '<tr><td>Net Down</td><td>' + formatSpeed(m.network_down) + '/s</td></tr>'
      + '<tr><td>Load 1m</td><td>' + (m.load1 != null ? m.load1.toFixed(2) : '—') + '</td></tr>'
      + '<tr><td>Load 5m</td><td>' + (m.load5 != null ? m.load5.toFixed(2) : '—') + '</td></tr>'
      + '<tr><td>CPU Usage</td><td>' + pct(m.cpu_usage) + '</td></tr>';
  }

  // Fetch sparkline data
  currentTcppingId = id;
  currentTcppingRange = '1h';
  currentMetricsRange = '1h';
  fetch('/api/agents/' + id + '/metrics?range=1h').then(r => r.json()).then(data => {
    renderSparklines(id, data);
  }).catch(() => {});
  fetchTCPingData(id, '1h');
}

function renderSparklines(id, metrics) {
  if (!metrics || !metrics.length) return;

  const cpuData = metrics.map(m => [m.created_at * 1000, m.cpu_usage]);
  const memData = metrics.map(m => {
    const pctVal = m.memory_total > 0 ? (m.memory_used / m.memory_total * 100) : 0;
    return [m.created_at * 1000, pctVal];
  });
  const netInData = metrics.map(m => [m.created_at * 1000, m.network_up]);
  const netOutData = metrics.map(m => [m.created_at * 1000, m.network_down]);

  document.getElementById('spark-cpu-val').textContent = cpuData.length ? cpuData[cpuData.length - 1][1].toFixed(1) + '%' : '—';
  document.getElementById('spark-mem-val').textContent = memData.length ? memData[memData.length - 1][1].toFixed(1) + '%' : '—';
  document.getElementById('spark-netin-val').textContent = netInData.length ? formatSpeed(netInData[netInData.length - 1][1]) + '/s' : '—';
  document.getElementById('spark-netout-val').textContent = netOutData.length ? formatSpeed(netOutData[netOutData.length - 1][1]) + '/s' : '—';

  renderSparkline('spark-cpu', cpuData, '#3b82f6');
  renderSparkline('spark-mem', memData, '#22c55e');
  renderSparkline('spark-netin', netInData, '#8b5cf6');
  renderSparkline('spark-netout', netOutData, '#f59e0b');
}

function renderSparkline(elemId, data, color) {
  const el = document.getElementById(elemId);
  if (!el) return;
  if (sparkCharts[elemId]) { sparkCharts[elemId].dispose(); }
  const chart = echarts.init(el);
  chart.setOption({
    grid: { left: 0, right: 0, top: 0, bottom: 0 },
    xAxis: { type: 'time', show: false },
    yAxis: { type: 'value', show: false, min: 'dataMin', max: 'dataMax' },
    tooltip: { show: false },
    series: [{
      type: 'line', data, smooth: true, symbol: 'none',
      lineStyle: { color, width: 1.5 },
      areaStyle: { color: color + '30' },
    }]
  });
  sparkCharts[elemId] = chart;
}

function switchMetricsRange(id, range) {
  if (currentMetricsRange === range) return;
  currentMetricsRange = range;
  const rangeBtns = document.querySelectorAll('#detail-' + id + ' .detail-range-bar .tcpping-range-btn');
  rangeBtns.forEach(b => {
    b.classList.toggle('active', b.dataset.range === range);
  });
  fetch('/api/agents/' + id + '/metrics?range=' + range).then(r => r.json()).then(data => {
    renderSparklines(id, data);
  }).catch(() => {});
}

function fetchTCPingData(id, range) {
  currentTcppingRange = range;
  const rangeBtns = document.querySelectorAll('#tcpping-section-' + id + ' .tcpping-range-btn');
  rangeBtns.forEach(b => {
    b.classList.toggle('active', b.dataset.range === range);
  });
  fetch('/api/agents/' + id + '/tcpping?range=' + range).then(r => r.json()).then(data => {
    renderTCPingChart(id, data);
  }).catch(() => {});
}

function switchTcppingRange(id, range) {
  id = +id;
  if (currentTcppingId !== id || currentTcppingRange === range) return;
  fetchTCPingData(id, range);
}

function renderTCPingChart(id, results) {
  if (!results || !results.length) {
    const el = document.getElementById('tcpping-chart-' + id);
    if (el) el.innerHTML = '<div style="color:hsl(215,20%,65%);font-size:13px;text-align:center;padding:80px 0">No TCPing data</div>';
    const rows = document.getElementById('tcpping-rows-' + id);
    if (rows) rows.innerHTML = '';
    return;
  }

  const names = [...new Set(results.map(r => r.name))];
  const colors = ['#3b82f6', '#22c55e', '#f59e0b', '#ef4444', '#8b5cf6', '#06b6d4', '#ec4899'];
  const selected = {};
  names.forEach((n, i) => { selected[n] = true; });

  const series = names.map((name, i) => {
    const data = results.filter(r => r.name === name).map(r => [r.created_at * 1000, r.success ? r.latency_ms : null]);
    return { name, type: 'line', data, smooth: true, symbol: 'none', connectNulls: false, lineStyle: { width: 1.5, color: colors[i % colors.length] } };
  });

  const chartEl = document.getElementById('tcpping-chart-' + id);
  if (!chartEl) return;
  if (tcppingChart) { try { tcppingChart.dispose(); } catch(e) {} }

  const chart = echarts.init(chartEl);
  chart.setOption({
    tooltip: {
      trigger: 'axis', valueFormatter: v => v ? v.toFixed(1) + ' ms' : 'timeout',
      textStyle: { fontSize: 11 },
    },
    legend: { show: false, selected },
    grid: { left: 48, right: 10, top: 8, bottom: 8 },
    xAxis: { type: 'time', axisLine: { lineStyle: { color: 'hsl(217,33%,22%)' } }, axisLabel: { color: 'hsl(215,20%,65%)', fontSize: 10 } },
    yAxis: { type: 'value', name: 'ms', nameTextStyle: { color: 'hsl(215,20%,65%)', fontSize: 10 }, splitLine: { lineStyle: { color: 'hsla(217,33%,17%,0.6)' } }, axisLabel: { color: 'hsl(215,20%,65%)', fontSize: 10 } },
    series,
  });
  tcppingChart = chart;

  // Render stats rows
  const statsHtml = names.map((name, i) => {
    const targetResults = results.filter(r => r.name === name);
    const avg = targetResults.reduce((s, r, _, a) => s + (r.success ? r.latency_ms : 0), 0) / targetResults.filter(r => r.success).length || 0;
    const vals = targetResults.filter(r => r.success).map(r => r.latency_ms);
    const jitter = vals.length > 1 ? vals.reduce((s, v, idx, a) => idx > 0 ? s + Math.abs(v - a[idx - 1]) : s, 0) / (vals.length - 1) : 0;
    const losses = targetResults.filter(r => !r.success).length;
    const lossPct = (losses / targetResults.length * 100);
    const color = colors[i % colors.length];

    const displayName = mapCarrier(name);
    return '<div class="tcpping-stat-row" onclick="tcppingToggleLine(' + id + ',\'' + escapeHtml(name) + '\')" data-name="' + escapeHtml(name) + '">'
      + '<span class="col-dot"><span style="background:' + color + '"></span></span>'
      + '<span class="col-name">' + escapeHtml(displayName) + '</span>'
      + '<span class="col-stat">' + (avg ? avg.toFixed(1) + 'ms' : '—') + '</span>'
      + '<span class="col-stat">' + (jitter ? jitter.toFixed(1) + 'ms' : '—') + '</span>'
      + '<span class="col-stat ' + (lossPct >= 5 ? 'high-loss' : '') + '">' + lossPct.toFixed(1) + '%</span>'
    + '</div>';
  }).join('');
  document.getElementById('tcpping-rows-' + id).innerHTML = statsHtml;
}

function tcppingSelectAll(id) {
  if (!tcppingChart) return;
  const selected = {};
  tcppingChart.getOption().series.forEach(s => { selected[s.name] = true; });
  tcppingChart.setOption({ legend: { selected } });
  document.querySelectorAll('#tcpping-rows-' + id + ' .tcpping-stat-row').forEach(r => r.classList.remove('hidden'));
}

function tcppingSelectNone(id) {
  if (!tcppingChart) return;
  const selected = {};
  tcppingChart.getOption().series.forEach(s => { selected[s.name] = false; });
  tcppingChart.setOption({ legend: { selected } });
  document.querySelectorAll('#tcpping-rows-' + id + ' .tcpping-stat-row').forEach(r => r.classList.add('hidden'));
}

function tcppingToggleLine(id, name) {
  if (!tcppingChart) return;
  const option = tcppingChart.getOption();
  const sel = option.legend?.[0]?.selected || {};
  sel[name] = !sel[name];
  tcppingChart.setOption({ legend: { selected: { ...sel } } });
  // Toggle row highlight
  document.querySelectorAll('#tcpping-rows-' + id + ' .tcpping-stat-row').forEach(r => {
    if (r.dataset.name === name) r.classList.toggle('hidden');
  });
}

function refresh() {
  const wasExpanded = expandedId;
  Promise.all([
    fetch('/api/agents').then(r => r.json()),
    fetch('/api/info').then(r => r.json()),
  ]).then(([data, info]) => {
    agents = data;
    infoData = info;
    renderInfoBar();
    if (wasExpanded && !agents.find(a => a.agent.id === wasExpanded)) {
      expandedId = null;
    }
    renderAll();
    updateViewIcon();
  }).catch(err => console.error('refresh:', err));
}

function toggleTheme() {
  const html = document.documentElement;
  const isDark = html.classList.toggle('dark');
  const btn = document.getElementById('theme-toggle');
  if (btn) btn.textContent = isDark ? '\u2600\uFE0F' : '\uD83C\uDF19';
  localStorage.setItem('theme', isDark ? 'dark' : 'light');
}

(function initTheme() {
  const saved = localStorage.getItem('theme');
  const html = document.documentElement;
  if (saved === 'light') html.classList.remove('dark');
  else html.classList.add('dark');
  const btn = document.getElementById('theme-toggle');
  if (btn) btn.textContent = html.classList.contains('dark') ? '\u2600\uFE0F' : '\uD83C\uDF19';
})();

function updateViewIcon() {
  const gridIcon = document.getElementById('view-icon-grid');
  const listIcon = document.getElementById('view-icon-list');
  if (gridIcon && listIcon) {
    gridIcon.style.display = cardFormat === 'card' ? 'none' : '';
    listIcon.style.display = cardFormat === 'card' ? '' : 'none';
  }
}

window.addEventListener('resize', () => {
  const prev = gridCols;
  gridCols = getGridCols();
  if (gridCols !== prev && expandedId) renderAll();
  Object.values(sparkCharts).forEach(c => { try { c.resize(); } catch(e) {} });
  if (tcppingChart) try { tcppingChart.resize(); } catch(e) {}
});

document.addEventListener('keydown', e => {
  if (e.key === 'Escape' && expandedId) { toggleExpand(expandedId); }
});

loadServerInfo();
refresh();
setInterval(refresh, 15000);
updateViewIcon();
