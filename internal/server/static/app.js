let agents = [];
let expandedId = null;
let sparkCharts = {};
let tcppingChart = null;
let filterRegion = '';
let cardFormat = localStorage.getItem('cardFormat') || 'card';
let infoData = null;
let currentTcppingRange = '24h';
let currentTcppingId = null;
let currentMetricsRange = '24h';
let gridCols = 4;
let trafficCache = {};
const TCPPING_COLORS = ['#3b82f6', '#22c55e', '#f59e0b', '#ef4444', '#8b5cf6', '#06b6d4', '#ec4899'];
const TCPPING_ORDER = ['CMv4', 'CMv6', 'CUv4', 'CUv6', 'CTv4', 'CTv6'];

function sortTcppingNames(names) {
  const set = new Set(names);
  const ordered = TCPPING_ORDER.filter(n => set.has(n));
  const extras = [...set].filter(n => !TCPPING_ORDER.includes(n)).sort();
  return ordered.concat(extras);
}

function tcppingColor(name) {
  const idx = TCPPING_ORDER.indexOf(name);
  if (idx >= 0) return TCPPING_COLORS[idx];
  let h = 0;
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) >>> 0;
  return TCPPING_COLORS[h % TCPPING_COLORS.length];
}

function escapeHtml(s) {
  if (s == null) return '';
  return String(s)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

function escapeAttr(s) {
  if (s == null) return '';
  return String(s)
    .replace(/&/g, '&amp;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;');
}

function mapCarrier(name) {
  return name.replace(/^移动/, 'CM').replace(/^联通/, 'CU').replace(/^电信/, 'CT');
}

function pingLatColor(ms) {
  if (!ms || ms === Infinity) return 'red';
  if (ms < 80) return 'green';
  if (ms < 161) return 'amber';
  return 'red';
}
function pingLossColor(pct) {
  if (pct === 0) return 'gray';
  if (pct < 3) return 'amber';
  return 'red';
}
function valCss(c) {
  const m = { green: 'var(--success)', amber: 'var(--warning)', red: 'var(--destructive)', gray: 'var(--muted-foreground)' };
  return 'hsl(' + m[c] + ')';
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
  if (!code || !/^[a-zA-Z]{2}$/.test(code)) return '\u{1F310}';
  return '<span class="fi fi-' + code.toLowerCase() + '"></span>';
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
      const label = /^[a-zA-Z]{2}$/.test(code) ? flagEmoji(code) : escapeHtml(code);
      const active = filterRegion === code;
      return '<span class="item region-filter' + (active ? ' active' : '') + '" data-region="' + escapeAttr(code) + '" style="cursor:pointer"><span class="value pri">' + label + '</span> <span class="value">' + count + '</span></span>';
    })
    .join('');

  document.getElementById('server-info').innerHTML =
    '<span class="item' + (!filterRegion ? ' active' : '') + '" data-region="" style="cursor:pointer"><span class="label">NODES</span> <span class="value">' + online + '/' + total + '</span></span>'
    + regionHtml;
}

function loadServerInfo() {
  fetch('/api/info').then(r => r.json()).then(info => {
    infoData = info;
    renderInfoBar();
    const el = document.getElementById('version-label');
    if (el) el.textContent = 'NEEDLE ' + info.version;
  }).catch(() => {});
}

function filterAndSort(list) {
  let arr = [...list];
  if (filterRegion) {
    arr = arr.filter(a => a.agent.region === filterRegion);
  }
  arr.sort((a, b) => {
    if (a._online !== b._online) return a._online ? -1 : 1;
    return a.agent.id - b.agent.id;
  });
  return arr;
}

function switchCardFormat(mode) {
  cardFormat = mode;
  localStorage.setItem('cardFormat', mode);
  renderAll();
  if (expandedId) {
    const agent = agents.find(a => a.agent.id === expandedId);
    if (!agent) expandedId = null;
  }
  updateHeaderIcons();
}

const VIEW_ICONS = {
  card: '<svg id="view-icon" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="3" width="7" height="7"/><rect x="14" y="3" width="7" height="7"/><rect x="3" y="14" width="7" height="7"/><rect x="14" y="14" width="7" height="7"/></svg>',
  list: '<svg id="view-icon" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="3" y1="6" x2="21" y2="6"/><line x1="3" y1="12" x2="21" y2="12"/><line x1="3" y1="18" x2="21" y2="18"/><line x1="3" y1="6" x2="21" y2="6"/></svg>',
};
const THEME_ICONS = {
  light: '<svg id="theme-icon" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><circle cx="12" cy="12" r="4.2"/><path d="M12 2v2.5M12 19.5V22M2 12h2.5M19.5 12H22M5.64 5.64l1.77 1.77M16.6 16.6l1.77 1.77M5.64 18.36l1.77-1.77M16.6 7.4l1.77-1.77"/></svg>',
  dark: '<svg id="theme-icon" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M12 3a9 9 0 1 0 9 9c-4 0-7-3-7-7 0-1 .2-2 .7-3A9 9 0 0 1 12 3z"/></svg>',
  system: '<svg id="theme-icon" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><rect x="3" y="4" width="18" height="12" rx="2"/><path d="M9 20h6M12 16v4"/><path d="M7 4V2M17 4V2" stroke-width="1.2"/></svg>',
};

function toggleView() {
  switchCardFormat(cardFormat === 'card' ? 'list' : 'card');
}

function cycleTheme() {
  const modes = ['light', 'dark', 'system'];
  setThemeMode(modes[(modes.indexOf(themeMode) + 1) % modes.length]);
}

function updateHeaderIcons() {
  const viewIcon = document.getElementById('view-icon');
  if (viewIcon) viewIcon.outerHTML = VIEW_ICONS[cardFormat];
  const themeIcon = document.getElementById('theme-icon');
  if (themeIcon) themeIcon.outerHTML = THEME_ICONS[themeMode];
}

function renderAll(scrollToDetail) {
  const container = document.getElementById('card-grid');
  const empty = document.getElementById('empty-state');

  container.className = cardFormat === 'list' ? 'list-grid' : 'card-grid';

  if (!agents.length) {
    container.innerHTML = '';
    empty.style.display = 'flex';
    return;
  }
  empty.style.display = 'none';

  const filtered = filterAndSort(agents);

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
      if (scrollToDetail) {
        setTimeout(() => {
          const el = document.getElementById('detail-' + expandedId);
          if (el) el.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
        }, 100);
      }
    }
  }
  refreshTraffic();
}

function renderCard(a, idx, isActive) {
  const m = a.latest_metric;
  const isOnline = a._online || false;
  const cpu = m ? m.cpu_usage : 0;
  const memPct = m ? (m.memory_used / m.memory_total * 100) : 0;
  const memStr = m ? formatBytes(m.memory_used) + ' / ' + formatBytes(m.memory_total) : '';
  const diskPct = m ? (m.disk_used / m.disk_total * 100) : 0;
  const diskStr = m ? formatBytes(m.disk_used) + ' / ' + formatBytes(m.disk_total) : '';
  const upSpeed = m ? formatSpeed(m.network_up) : '0';
  const downSpeed = m ? formatSpeed(m.network_down) : '0';
  const uptime = m ? formatUptime(m.uptime) : '-';
  const sess = String(idx + 1).padStart(2, '0');
  const expiryDays = a.expiry_days || 0;
  const expiryDate = a.expiry_date || '';

  // TCPing card line — selected target or first
  let pingHtml = '';
  if (a.latest_tcpping && a.latest_tcpping.length > 0) {
    const saved = getCardTcpping(a.agent.id);
    const p = (saved && a.latest_tcpping.find(t => t.name === saved)) || a.latest_tcpping[0];
    const latStr = p.success ? p.latency_ms.toFixed(1) + 'ms' : '-';
    const lossStr = p.success ? '0%' : '-';
    const dotBg = tcppingColor(p.name);
    const latClr = p.success ? valCss(pingLatColor(p.latency_ms)) : valCss('gray');
    const lossClr = valCss(pingLossColor(p.success ? 0 : 100));
    pingHtml = '<div class="card-ping"><span class="ping-dot" style="background:' + dotBg + '"></span><span class="ping-label" onclick="event.stopPropagation();cycleCardTcpping(' + a.agent.id + ')" style="cursor:pointer">' + escapeHtml(mapCarrier(p.name)) + '</span><span class="ping-lat"><span class="ping-tag">Lat</span> <span class="ping-val" style="color:' + latClr + '">' + latStr + '</span></span><span class="ping-loss"><span class="ping-tag">Loss</span> <span class="ping-val" style="color:' + lossClr + '">' + lossStr + '</span></span></div>';
  }

  let expiryHtml = '';
  if (expiryDays > 0) {
    const ec = expiryDays < 7 ? ' expiry-urgent' : '';
    expiryHtml = '<span class="expiry-days' + ec + '" title="Due ' + expiryDate + '">' + expiryDays + '</span>';
  }

  return '<div class="card' + (isActive ? ' active' : '') + (!isOnline ? ' offline' : '') + '" onclick="toggleExpand(' + a.agent.id + ')" data-id="' + a.agent.id + '">'
    + '<div class="card-top">'
      + '<span class="status-dot ' + (isOnline ? 'online' : 'offline') + '"></span>'
      + '<span class="card-hostname">' + escapeHtml(a.agent.hostname) + '</span>'
      + '<span class="card-session">' + flagEmoji(a.agent.region) + '</span>'
    + '</div>'
    + '<div class="card-sub"><span>' + uptime + '</span>' + expiryHtml + '</div>'
    + '<div class="card-metrics">'
      + '<div class="metric"><div class="metric-header"><span class="label">CPU</span><span class="value ' + metricColor(cpu) + '">' + pct(cpu) + '</span></div><div class="metric-bar"><div class="metric-fill ' + metricColor(cpu) + '" style="width:' + cpu.toFixed(0) + '%"></div></div></div>'
      + '<div class="metric"><div class="metric-header"><span class="label">MEM</span><span class="value ' + metricColor(memPct) + '">' + pct(memPct) + '</span></div><div class="metric-bar"><div class="metric-fill ' + metricColor(memPct) + '" style="width:' + memPct.toFixed(0) + '%"></div></div><div class="metric-sub">' + memStr + '</div></div>'
      + '<div class="metric"><div class="metric-header"><span class="label">DSK</span><span class="value ' + metricColor(diskPct) + '">' + pct(diskPct) + '</span></div><div class="metric-bar"><div class="metric-fill ' + metricColor(diskPct) + '" style="width:' + diskPct.toFixed(0) + '%"></div></div><div class="metric-sub">' + diskStr + '</div></div>'
    + '</div>'
    + '<div class="card-traffic" data-traffic-id="' + a.agent.id + '"><span class="traffic-label">TRF</span><span class="traffic-down">\u2193 —</span><span class="traffic-divider">/</span><span class="traffic-up">\u2191 —</span></div>'
    + pingHtml
    + '<div class="card-footer-line"><div class="net"><span class="net-down">\u2193 ' + downSpeed + '</span><span class="net-up">\u2191 ' + upSpeed + '</span></div><span>' + (m ? relativeTime(m.created_at * 1000) : '') + '</span></div>'
  + '</div>';
}

function renderListRow(a, idx, isActive) {
  const m = a.latest_metric;
  const isOnline = a._online || false;
  const cpu = m ? m.cpu_usage : 0;
  const memPct = m ? (m.memory_used / m.memory_total * 100) : 0;
  const diskPct = m ? (m.disk_used / m.disk_total * 100) : 0;
  const upSpeed = m ? formatSpeed(m.network_up) : '0';
  const downSpeed = m ? formatSpeed(m.network_down) : '0';
  const uptime = m ? formatUptime(m.uptime) : '-';
  const region = a.agent.region;
  const regionLabel = region && region.length === 2 ? flagEmoji(region) : escapeHtml(region || '');
  const sess = String(idx + 1).padStart(2, '0');

  let pingHtml = '';
  if (a.latest_tcpping && a.latest_tcpping.length > 0) {
    const saved = getCardTcpping(a.agent.id);
    const p = (saved && a.latest_tcpping.find(t => t.name === saved)) || a.latest_tcpping[0];
    const latStr = p.success ? p.latency_ms.toFixed(1) + 'ms' : '-';
    const lossStr = p.success ? '0%' : '-';
    const dotBg = tcppingColor(p.name);
    const latClr = p.success ? valCss(pingLatColor(p.latency_ms)) : valCss('gray');
    const lossClr = valCss(pingLossColor(p.success ? 0 : 100));
    pingHtml = '<span class="list-ping">'
      + '<span class="ping-dot" style="background:' + dotBg + '"></span>'
      + '<span class="list-ping-name">' + escapeHtml(mapCarrier(p.name)) + '</span>'
      + '<span class="list-ping-lat" style="color:' + latClr + '">' + latStr + '</span>'
      + '<span class="list-ping-loss" style="color:' + lossClr + '">' + lossStr + '</span>'
      + '</span>';
  }

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
    + pingHtml
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
    renderAll(true);
  }
}

function cycleCardTcpping(id) {
  const agent = agents.find(a => a.agent.id === id);
  if (!agent || !agent.latest_tcpping || !agent.latest_tcpping.length) return;
  const names = sortTcppingNames(agent.latest_tcpping.map(t => t.name));
  const current = getCardTcpping(id);
  const idx = current ? names.indexOf(current) : -1;
  const next = (idx + 1) % names.length;
  setCardTcpping(id, names[next]);
  renderAll();
}

function getCardTcpping(id) {
  return localStorage.getItem('cardTcpping_' + id) || '';
}

function setCardTcpping(id, name) {
  localStorage.setItem('cardTcpping_' + id, name);
}



function fetchTrafficForCard(id) {
  fetch('/api/agents/' + id + '/traffic')
    .then(r => r.json())
    .then(data => {
      trafficCache[id] = data;
      updateTrafficDisplay(id, data);
    })
    .catch(() => {});
}

function updateTrafficDisplay(id, data) {
  const sentStr = formatBytes(data.sent);
  const recvStr = formatBytes(data.recv);
  const el = document.querySelector('[data-traffic-id="' + id + '"]');
  if (el) {
    el.innerHTML = '<span class="traffic-label">TRF</span><span class="traffic-down">\u2193 ' + recvStr + '</span><span class="traffic-divider">/</span><span class="traffic-up">\u2191 ' + sentStr + '</span>';
  }
}

function refreshTraffic() {
  const filtered = filterAndSort(agents);
  filtered.forEach(a => {
    const id = a.agent.id;
    if (!trafficCache[id]) {
      fetchTrafficForCard(id);
    } else {
      updateTrafficDisplay(id, trafficCache[id]);
    }
  });
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

  const range = currentMetricsRange || '24h';
  detailEl.innerHTML = '<div class="detail-header">'
    + '<div class="detail-title">' + escapeHtml(agent.agent.hostname) + ' <span class="sub">Node Detail</span></div>'
    + '<button class="btn btn-ghost btn-sm" onclick="toggleExpand(' + id + ')">\u2715</button>'
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
          + '<div class="theme-btn-group detail-range-group">'
            + '<button type="button" class="theme-btn' + (range === '24h' ? ' active' : '') + '" data-range="24h" onclick="switchDetailRange(' + id + ',\'24h\')">1d</button>'
            + '<button type="button" class="theme-btn' + (range === '168h' ? ' active' : '') + '" data-range="168h" onclick="switchDetailRange(' + id + ',\'168h\')">7d</button>'
          + '</div>'
        + '</div>'
        + '<div class="tcpping-controls">'
          + '<div class="theme-btn-group">'
            + '<button class="theme-btn active" onclick="tcppingSelectAll(\'' + id + '\')">Show</button>'
            + '<button class="theme-btn" onclick="tcppingSelectNone(\'' + id + '\')">Hide</button>'
          + '</div>'
        + '</div>'
      + '</div>'
      + '<div class="tcpping-chart-wrap"><div id="tcpping-chart-' + id + '" class="tcpping-chart"></div></div>'
      + '<div class="tcpping-stats" id="tcpping-stats-' + id + '">'
        + '<div class="tcpping-stats-header"><span class="col-dot"></span><span class="col-name">Source</span><span class="col-stat">Avg</span><span class="col-stat">Jitter</span><span class="col-stat">Loss</span></div>'
        + '<div id="tcpping-rows-' + id + '"></div>'
      + '</div>'
    + '</div>'
    + '</div>';

  currentTcppingId = id;
  currentTcppingRange = range;
  currentMetricsRange = range;
  fetch('/api/agents/' + id + '/metrics?range=' + range).then(r => r.json()).then(data => {
    renderSparklines(id, data);
  }).catch(() => {});
  fetchTCPingData(id, range);
}

function renderSparklines(id, metrics) {
  if (!metrics || !metrics.length) return;

  const cpuData = metrics.map(m => [m.created_at * 1000, m.cpu_usage]);
  const memData = metrics.map(m => {
    const pctVal = m.memory_total > 0 ? (m.memory_used / m.memory_total * 100) : 0;
    return [m.created_at * 1000, pctVal];
  });
  const netInData = metrics.map(m => [m.created_at * 1000, m.network_down]);
  const netOutData = metrics.map(m => [m.created_at * 1000, m.network_up]);

  const peak = arr => arr.length ? Math.max(...arr.map(d => d[1])) : 0;
  const cpuPeak = peak(cpuData);
  const memPeak = peak(memData);
  const netInPeak = peak(netInData);
  const netOutPeak = peak(netOutData);

  document.getElementById('spark-cpu-val').textContent = cpuData.length ? cpuData[cpuData.length - 1][1].toFixed(1) + '%  peak ' + cpuPeak.toFixed(1) + '%' : '—';
  document.getElementById('spark-mem-val').textContent = memData.length ? memData[memData.length - 1][1].toFixed(1) + '%  peak ' + memPeak.toFixed(1) + '%' : '—';
  document.getElementById('spark-netin-val').innerHTML = netInData.length ? formatSpeed(netInData[netInData.length - 1][1]) + '/s  <span style="color:#8b5cf6">peak ' + formatSpeed(netInPeak) + '</span>' : '—';
  document.getElementById('spark-netout-val').innerHTML = netOutData.length ? formatSpeed(netOutData[netOutData.length - 1][1]) + '/s  <span style="color:#f59e0b">peak ' + formatSpeed(netOutPeak) + '</span>' : '—';

  renderSparkline('spark-cpu', cpuData, '#3b82f6', true);
  renderSparkline('spark-mem', memData, '#22c55e', true);
  renderSparkline('spark-netin', netInData, '#8b5cf6', false);
  renderSparkline('spark-netout', netOutData, '#f59e0b', false);
}

function formatSparkXAxis(ts) {
  const d = new Date(ts);
  return (d.getMonth() + 1) + '/' + d.getDate();
}

function renderSparkline(elemId, data, color, isPercent) {
  const el = document.getElementById(elemId);
  if (!el) return;
  if (sparkCharts[elemId]) { sparkCharts[elemId].dispose(); }
  const chart = echarts.init(el);
  chart.setOption({
    grid: { left: 4, right: 4, top: 2, bottom: 4 },
    xAxis: { type: 'time', show: false },
    yAxis: {
      type: 'value', show: false,
      min: isPercent ? 0 : 'dataMin',
      max: isPercent ? 100 : 'dataMax',
    },
    tooltip: {
      trigger: 'axis',
      formatter: params => {
        const p = params[0];
        if (!p) return '';
        const d = new Date(p.data[0]);
        const dateStr = (d.getMonth() + 1) + '/' + String(d.getDate()).padStart(2, '0');
        const timeStr = String(d.getHours()).padStart(2, '0') + ':' + String(d.getMinutes()).padStart(2, '0');
        const valStr = isPercent ? p.data[1].toFixed(1) + '%'
          : (p.data[1] >= 1000000 ? (p.data[1] / 1000000).toFixed(2) + 'M/s'
            : p.data[1] >= 1000 ? (p.data[1] / 1000).toFixed(1) + 'K/s'
            : p.data[1].toFixed(0) + '/s');
        return dateStr + '<br/>' + timeStr + '<br/>' + valStr;
      },
      textStyle: { fontSize: 11 },
    },
    series: [{
      type: 'line', data, smooth: true, symbol: 'none', sampling: 'lttb',
      lineStyle: { color, width: 1.5 },
      areaStyle: { color: color + '30' },
    }]
  });
  sparkCharts[elemId] = chart;
}

function switchDetailRange(id, range) {
  id = +id;
  if (currentMetricsRange === range && currentTcppingRange === range) return;
  currentMetricsRange = range;
  currentTcppingRange = range;
  const rangeBtns = document.querySelectorAll('#tcpping-section-' + id + ' .detail-range-group .theme-btn');
  rangeBtns.forEach(b => {
    b.classList.toggle('active', b.dataset.range === range);
  });
  fetch('/api/agents/' + id + '/metrics?range=' + range).then(r => r.json()).then(data => {
    renderSparklines(id, data);
  }).catch(() => {});
  fetchTCPingData(id, range);
}

function switchMetricsRange(id, range) {
  switchDetailRange(id, range);
}

function fetchTCPingData(id, range) {
  currentTcppingRange = range;
  fetch('/api/agents/' + id + '/tcpping?range=' + range).then(r => r.json()).then(data => {
    renderTCPingChart(id, data);
  }).catch(() => {});
}

function switchTcppingRange(id, range) {
  switchDetailRange(id, range);
}

function formatRangeAxisLabel(ts) {
  const d = new Date(ts);
  if (currentTcppingRange === '24h') {
    return String(d.getHours()).padStart(2, '0') + ':' + String(d.getMinutes()).padStart(2, '0');
  }
  return (d.getMonth() + 1) + '/' + d.getDate();
}

function renderTCPingChart(id, results) {
  if (!results || !results.length) {
    const el = document.getElementById('tcpping-chart-' + id);
    if (el) el.innerHTML = '<div style="color:hsl(215,20%,65%);font-size:13px;text-align:center;padding:80px 0">No TCPing data</div>';
    const rows = document.getElementById('tcpping-rows-' + id);
    if (rows) rows.innerHTML = '';
    return;
  }

  const names = sortTcppingNames([...new Set(results.map(r => r.name))]);
  const selected = {};
  names.forEach((n) => { selected[n] = true; });

  const series = names.map((name) => {
    const data = results.filter(r => r.name === name).map(r => [r.created_at * 1000, r.success ? r.latency_ms : null]);
    return { name, type: 'line', data, smooth: true, symbol: 'none', connectNulls: false, lineStyle: { width: 1.5, color: tcppingColor(name) } };
  });

  const chartEl = document.getElementById('tcpping-chart-' + id);
  if (!chartEl) return;
  if (tcppingChart) { try { tcppingChart.dispose(); } catch(e) {} }

  const is1d = currentTcppingRange === '24h';
  const chart = echarts.init(chartEl);
  chart.setOption({
    tooltip: {
      trigger: 'axis', valueFormatter: v => v ? v.toFixed(1) + ' ms' : 'timeout',
      textStyle: { fontSize: 11 },
    },
    legend: { show: false, selected },
    grid: { left: 8, right: 32, top: 8, bottom: 20 },
    xAxis: {
      type: 'time',
      axisLine: { lineStyle: { color: 'hsl(var(--border) / 0.5)' } },
      minInterval: is1d ? 4 * 3600 * 1000 : 24 * 3600 * 1000,
      splitNumber: is1d ? 5 : 7,
      axisLabel: {
        color: 'hsl(var(--muted-foreground))', fontSize: 10,
        hideOverlap: true,
        formatter: v => formatRangeAxisLabel(v),
      },
      splitLine: { show: false },
    },
    yAxis: {
      type: 'value', position: 'right',
      axisLine: { show: false },
      axisTick: { show: false },
      splitLine: { lineStyle: { color: 'hsl(var(--border) / 0.2)', width: 1 } },
      axisLabel: { color: 'hsl(var(--muted-foreground))', fontSize: 10, margin: 0, formatter: v => v + 'ms' },
    },
    series,
  });
  tcppingChart = chart;

  // Render stats rows
  const statsHtml = names.map((name) => {
    const targetResults = results.filter(r => r.name === name);
    const avg = targetResults.reduce((s, r, _, a) => s + (r.success ? r.latency_ms : 0), 0) / targetResults.filter(r => r.success).length || 0;
    const vals = targetResults.filter(r => r.success).map(r => r.latency_ms);
    const jitter = vals.length > 1 ? vals.reduce((s, v, idx, a) => idx > 0 ? s + Math.abs(v - a[idx - 1]) : s, 0) / (vals.length - 1) : 0;
    const losses = targetResults.filter(r => !r.success).length;
    const lossPct = (losses / targetResults.length * 100);
    const color = tcppingColor(name);

    const displayName = mapCarrier(name);
    return '<div class="tcpping-stat-row" data-agent-id="' + Number(id) + '" data-name="' + escapeAttr(name) + '">'
      + '<span class="col-dot"><span style="background:' + color + '"></span></span>'
      + '<span class="col-name">' + escapeHtml(displayName) + '</span>'
      + '<span class="col-stat">' + (avg ? avg.toFixed(1) + 'ms' : '—') + '</span>'
      + '<span class="col-stat">' + (jitter ? jitter.toFixed(1) + 'ms' : '—') + '</span>'
       + '<span class="col-stat ' + (lossPct >= 5 && vals.length > 0 ? 'high-loss' : '') + '">' + (vals.length > 0 ? lossPct.toFixed(1) + '%' : '-') + '</span>'
     + '</div>';
   }).join('');
   document.getElementById('tcpping-rows-' + id).innerHTML = statsHtml;
   applyTcppingSelections(id, names);
}

function saveTcppingSelections(id) {
  if (!tcppingChart) return;
  const sel = tcppingChart.getOption().legend?.[0]?.selected || {};
  localStorage.setItem('tcppingSel_' + id, JSON.stringify(sel));
}

function loadTcppingSelections(id) {
  try { return JSON.parse(localStorage.getItem('tcppingSel_' + id)); } catch(e) { return null; }
}

function applyTcppingSelections(id, names) {
  const saved = loadTcppingSelections(id);
  if (!saved || !tcppingChart) return;
  const selected = {};
  names.forEach(n => { selected[n] = saved[n] !== undefined ? saved[n] : true; });
  tcppingChart.setOption({ legend: { selected } });
  document.querySelectorAll('#tcpping-rows-' + id + ' .tcpping-stat-row').forEach(r => {
    r.classList.toggle('hidden', selected[r.dataset.name] === false);
  });
  const allHidden = names.every(n => selected[n] === false);
  document.querySelectorAll('#tcpping-section-' + id + ' .tcpping-controls .theme-btn').forEach((b, i) => {
    b.classList.toggle('active', i === (allHidden ? 1 : 0));
  });
}

function tcppingSelectAll(id) {
  if (!tcppingChart) return;
  const selected = {};
  tcppingChart.getOption().series.forEach(s => { selected[s.name] = true; });
  tcppingChart.setOption({ legend: { selected } });
  document.querySelectorAll('#tcpping-rows-' + id + ' .tcpping-stat-row').forEach(r => r.classList.remove('hidden'));
  saveTcppingSelections(id);
  document.querySelectorAll('#tcpping-section-' + id + ' .tcpping-controls .theme-btn').forEach((b, i) => {
    b.classList.toggle('active', i === 0);
  });
}

function tcppingSelectNone(id) {
  if (!tcppingChart) return;
  const selected = {};
  tcppingChart.getOption().series.forEach(s => { selected[s.name] = false; });
  tcppingChart.setOption({ legend: { selected } });
  document.querySelectorAll('#tcpping-rows-' + id + ' .tcpping-stat-row').forEach(r => r.classList.add('hidden'));
  saveTcppingSelections(id);
  document.querySelectorAll('#tcpping-section-' + id + ' .tcpping-controls .theme-btn').forEach((b, i) => {
    b.classList.toggle('active', i === 1);
  });
}

function tcppingToggleLine(id, name) {
  if (!tcppingChart) return;
  const option = tcppingChart.getOption();
  const sel = option.legend?.[0]?.selected || {};
  sel[name] = !sel[name];
  tcppingChart.setOption({ legend: { selected: { ...sel } } });
  document.querySelectorAll('#tcpping-rows-' + id + ' .tcpping-stat-row').forEach(r => {
    if (r.dataset.name === name) r.classList.toggle('hidden');
  });
  saveTcppingSelections(id);
}

function fullRefresh() {
  const wasExpanded = expandedId;
  Promise.all([
    fetch('/api/agents').then(r => r.json()),
    fetch('/api/info').then(r => r.json()),
  ]).then(([data, info]) => {
    agents = data;
    agents.forEach(a => {
      const m = a.latest_metric;
      a._online = m && (Date.now() - m.created_at * 1000 < 120000);
    });
    infoData = info;
    renderInfoBar();
    if (wasExpanded && !agents.find(a => a.agent.id === wasExpanded)) {
      expandedId = null;
    }
    renderAll();
    if (expandedId) updateDetailCharts(expandedId);
  }).catch(err => console.error('fullRefresh:', err));
}

function softRefresh() {
  Promise.all([
    fetch('/api/agents').then(r => r.json()),
    fetch('/api/info').then(r => r.json()),
  ]).then(([data, info]) => {
    agents = data;
    agents.forEach(a => {
      const m = a.latest_metric;
      a._online = m && (Date.now() - m.created_at * 1000 < 120000);
    });
    infoData = info;
    renderInfoBar();

    data.forEach(a => {
      const card = document.querySelector('[data-id="' + a.agent.id + '"]');
      if (!card) return;
      const m = a.latest_metric;
      const isOnline = a._online || false;

      card.classList.toggle('offline', !isOnline);
      const dot = card.querySelector('.status-dot');
      if (dot) {
        dot.className = 'status-dot ' + (isOnline ? 'online' : 'offline');
        dot.onclick = null;
      }

      if (!m) return;
      const cpu = m.cpu_usage;
      const memPct = m.memory_total > 0 ? (m.memory_used / m.memory_total * 100) : 0;
      const diskPct = m.disk_total > 0 ? (m.disk_used / m.disk_total * 100) : 0;

      const isCard = card.classList.contains('card');
      if (isCard) {
        const metrics = card.querySelectorAll('.metric');
        if (metrics.length >= 3) {
          const cpuFill = metrics[0].querySelector('.metric-fill');
          if (cpuFill) { cpuFill.style.width = cpu.toFixed(0) + '%'; cpuFill.className = 'metric-fill ' + metricColor(cpu); }
          const cpuVal = metrics[0].querySelector('.value');
          if (cpuVal) cpuVal.textContent = pct(cpu);

          const memFill = metrics[1].querySelector('.metric-fill');
          if (memFill) { memFill.style.width = memPct.toFixed(0) + '%'; memFill.className = 'metric-fill ' + metricColor(memPct); }
          const memVal = metrics[1].querySelector('.value');
          if (memVal) memVal.textContent = pct(memPct);
          const memSub = metrics[1].querySelector('.metric-sub');
          if (memSub) memSub.textContent = formatBytes(m.memory_used) + ' / ' + formatBytes(m.memory_total);

          const diskFill = metrics[2].querySelector('.metric-fill');
          if (diskFill) { diskFill.style.width = diskPct.toFixed(0) + '%'; diskFill.className = 'metric-fill ' + metricColor(diskPct); }
          const diskVal = metrics[2].querySelector('.value');
          if (diskVal) diskVal.textContent = pct(diskPct);
          const diskSub = metrics[2].querySelector('.metric-sub');
          if (diskSub) diskSub.textContent = formatBytes(m.disk_used) + ' / ' + formatBytes(m.disk_total);
        }

        const sub = card.querySelector('.card-sub');
        if (sub) { const u = sub.querySelector('span:first-child'); if (u) u.textContent = formatUptime(m.uptime); }
        const expiryEl = sub ? sub.querySelector('.expiry-days') : null;
        if (a.expiry_days > 0 && expiryEl) {
          expiryEl.textContent = a.expiry_days;
          expiryEl.className = 'expiry-days' + (a.expiry_days < 7 ? ' expiry-urgent' : '');
          expiryEl.title = 'Due ' + (a.expiry_date || '');
        }

        const netDown = card.querySelector('.card-footer-line .net-down');
        const netUp = card.querySelector('.card-footer-line .net-up');
        if (netDown) netDown.textContent = '\u2193 ' + formatSpeed(m.network_down);
        if (netUp) netUp.textContent = '\u2191 ' + formatSpeed(m.network_up);
        const timeEl = card.querySelector('.card-footer-line > span:last-child');
        if (timeEl) timeEl.textContent = relativeTime(m.created_at * 1000);

        const pingLabel = card.querySelector('.ping-label');
        const pingLat = card.querySelector('.ping-lat');
        const pingLoss = card.querySelector('.ping-loss');
        if (a.latest_tcpping && a.latest_tcpping.length > 0 && pingLabel) {
          const saved = getCardTcpping(a.agent.id);
          const p = (saved && a.latest_tcpping.find(t => t.name === saved)) || a.latest_tcpping[0];
          pingLabel.textContent = mapCarrier(p.name);
          const pingDot = card.querySelector('.ping-dot');
          if (pingDot) {
            pingDot.style.background = tcppingColor(p.name);
          }
          if (pingLat) {
            const lv = p.success ? p.latency_ms.toFixed(1) + 'ms' : '-';
            const lc = p.success ? valCss(pingLatColor(p.latency_ms)) : valCss('gray');
            pingLat.innerHTML = '<span class="ping-tag">Lat</span> <span class="ping-val" style="color:' + lc + '">' + lv + '</span>';
          }
          if (pingLoss) {
            const lv = p.success ? '0%' : '-';
            const lc = valCss(pingLossColor(p.success ? 0 : 100));
            pingLoss.innerHTML = '<span class="ping-tag">Loss</span> <span class="ping-val" style="color:' + lc + '">' + lv + '</span>';
          }
        }
      } else {
        const bars = card.querySelectorAll('.list-bar');
        if (bars.length >= 3) {
          const upd = (i, v) => { const f = bars[i].querySelector('.metric-fill'); if (f) { f.style.width = v.toFixed(0) + '%'; f.className = 'metric-fill ' + metricColor(v); } const l = bars[i].querySelector('.list-bar-val'); if (l) l.textContent = pct(v); };
          upd(0, cpu); upd(1, memPct); upd(2, diskPct);
        }
        const netEl = card.querySelector('.list-net');
        if (netEl) netEl.textContent = '\u2193' + formatSpeed(m.network_down) + ' \u2191' + formatSpeed(m.network_up);
        const uptimeEl = card.querySelector('.list-uptime');
        if (uptimeEl) uptimeEl.textContent = formatUptime(m.uptime);
        const timeEl = card.querySelector('.list-time');
        if (timeEl) timeEl.textContent = relativeTime(m.created_at * 1000);
      }
    });

    refreshTraffic();
    if (expandedId) updateDetailCharts(expandedId);
  }).catch(err => console.error('softRefresh:', err));
}

function updateDetailCharts(id) {
  const metricsRange = currentMetricsRange || '24h';
  const tcppingRange = currentTcppingRange || '24h';
  fetch('/api/agents/' + id + '/metrics?range=' + metricsRange).then(r => r.json()).then(metrics => {
    if (!metrics || !metrics.length) return;
    const cpuData = metrics.map(m => [m.created_at * 1000, m.cpu_usage]);
    const memData = metrics.map(m => {
      const pctVal = m.memory_total > 0 ? (m.memory_used / m.memory_total * 100) : 0;
      return [m.created_at * 1000, pctVal];
    });
    const netInData = metrics.map(m => [m.created_at * 1000, m.network_down]);
    const netOutData = metrics.map(m => [m.created_at * 1000, m.network_up]);

    const peak2 = arr => arr.length ? Math.max(...arr.map(d => d[1])) : 0;
    document.getElementById('spark-cpu-val').textContent = cpuData.length ? cpuData[cpuData.length - 1][1].toFixed(1) + '%  peak ' + peak2(cpuData).toFixed(1) + '%' : '—';
    document.getElementById('spark-mem-val').textContent = memData.length ? memData[memData.length - 1][1].toFixed(1) + '%  peak ' + peak2(memData).toFixed(1) + '%' : '—';
    document.getElementById('spark-netin-val').innerHTML = netInData.length ? formatSpeed(netInData[netInData.length - 1][1]) + '/s  <span style="color:#8b5cf6">peak ' + formatSpeed(peak2(netInData)) + '</span>' : '—';
    document.getElementById('spark-netout-val').innerHTML = netOutData.length ? formatSpeed(netOutData[netOutData.length - 1][1]) + '/s  <span style="color:#f59e0b">peak ' + formatSpeed(peak2(netOutData)) + '</span>' : '—';

    const configs = [
      ['spark-cpu', cpuData], ['spark-mem', memData],
      ['spark-netin', netInData], ['spark-netout', netOutData],
    ];
    configs.forEach(([elemId, data]) => {
      const chart = sparkCharts[elemId];
      if (chart) chart.setOption({ series: [{ data }] });
    });
  }).catch(() => {});

  fetch('/api/agents/' + id + '/tcpping?range=' + tcppingRange).then(r => r.json()).then(results => {
    if (!results || !results.length) return;
    const names = sortTcppingNames([...new Set(results.map(r => r.name))]);
    const series = names.map((name) => {
    const data = results.filter(r => r.name === name).map(r => [r.created_at * 1000, r.success ? r.latency_ms : null]);
    return { name, type: 'line', data, smooth: true, symbol: 'none', connectNulls: false, lineStyle: { width: 1.5, color: tcppingColor(name) } };
    });
    if (tcppingChart) tcppingChart.setOption({ series });

    const statsHtml = names.map((name) => {
      const targetResults = results.filter(r => r.name === name);
      const avg = targetResults.reduce((s, r, _, a) => s + (r.success ? r.latency_ms : 0), 0) / targetResults.filter(r => r.success).length || 0;
      const vals = targetResults.filter(r => r.success).map(r => r.latency_ms);
      const jitter = vals.length > 1 ? vals.reduce((s, v, idx, a) => idx > 0 ? s + Math.abs(v - a[idx - 1]) : s, 0) / (vals.length - 1) : 0;
      const losses = targetResults.filter(r => !r.success).length;
      const lossPct = (losses / targetResults.length * 100);
      const color = tcppingColor(name);
      const displayName = mapCarrier(name);
      return '<div class="tcpping-stat-row" data-agent-id="' + Number(id) + '" data-name="' + escapeAttr(name) + '">'
        + '<span class="col-dot"><span style="background:' + color + '"></span></span>'
        + '<span class="col-name">' + escapeHtml(displayName) + '</span>'
        + '<span class="col-stat">' + (avg ? avg.toFixed(1) + 'ms' : '—') + '</span>'
        + '<span class="col-stat">' + (jitter ? jitter.toFixed(1) + 'ms' : '—') + '</span>'
        + '<span class="col-stat ' + (lossPct >= 5 && vals.length > 0 ? 'high-loss' : '') + '">' + (vals.length > 0 ? lossPct.toFixed(1) + '%' : '-') + '</span>'
      + '</div>';
    }).join('');
    const rowsEl = document.getElementById('tcpping-rows-' + id);
    if (rowsEl) rowsEl.innerHTML = statsHtml;
    applyTcppingSelections(id, names);
  }).catch(() => {});
}

let themeMode = localStorage.getItem('themeMode') || 'light';

function setThemeMode(mode) {
  themeMode = mode;
  localStorage.setItem('themeMode', mode);
  applyTheme();
  updateHeaderIcons();
}

function applyTheme() {
  const html = document.documentElement;
  if (themeMode === 'dark') { html.classList.add('dark'); return; }
  if (themeMode === 'light') { html.classList.remove('dark'); return; }
  const prefersDark = window.matchMedia('(prefers-color-scheme: dark)').matches;
  html.classList.toggle('dark', prefersDark);
}

(function initTheme() {
  applyTheme();
  updateHeaderIcons();
  window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
    if (themeMode === 'system') applyTheme();
  });
})();

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

document.getElementById('server-info')?.addEventListener('click', e => {
  const el = e.target.closest('[data-region]');
  if (!el || !e.currentTarget.contains(el)) return;
  setRegionFilter(el.getAttribute('data-region') || '');
});

document.addEventListener('click', e => {
  const row = e.target.closest('.tcpping-stat-row[data-name]');
  if (!row) return;
  const id = parseInt(row.getAttribute('data-agent-id'), 10);
  const name = row.getAttribute('data-name');
  if (!Number.isFinite(id) || name == null) return;
  tcppingToggleLine(id, name);
});

loadServerInfo();
fullRefresh();
setInterval(softRefresh, 30000);

updateViewIcon();
