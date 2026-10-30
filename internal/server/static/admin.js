'use strict';

let adminCSRF = '';
let adminNodes = [];
let editingNode = null;
let sessionTimer = null;
let adminSort = {key: '', direction: 1};
const sortButtons = document.querySelectorAll('.admin-sort');
const el = id => document.getElementById(id);
const profileForm = el('profile-form');
const field = name => profileForm.elements.namedItem(name);
el('refresh-admin').addEventListener('click', () => location.reload());

try {
  document.documentElement.classList.toggle('dark', localStorage.getItem('themeMode') === 'dark');
} catch (_) {}

function message(id, text) {
  el(id).textContent = text;
  el(id).hidden = !text;
}

function signedOut(text = '') {
  adminCSRF = '';
  adminNodes = [];
  editingNode = null;
  clearTimeout(sessionTimer);
  el('node-list').replaceChildren();
  profileForm.reset();
  if (el('editor').open) el('editor').close();
  el('management').hidden = true;
  el('logout').hidden = true;
  el('login').hidden = false;
  message('login-error', text);
}

async function adminFetch(path, options = {}) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), 15000);
  try {
    const response = await fetch('/api/admin/' + path, {
      ...options, credentials: 'same-origin', cache: 'no-store', signal: controller.signal,
      headers: {'Content-Type': 'application/json', 'X-Needle-Admin': '1', ...(adminCSRF ? {'X-CSRF-Token': adminCSRF} : {}), ...options.headers},
    });
    const data = await response.json();
    if (!response.ok) {
      if (response.status === 401 && path !== 'login') signedOut('Your session ended. Sign in again to edit.');
      const error = new Error(data.error || 'Request failed');
      error.status = response.status;
      throw error;
    }
    return data;
  } catch (error) {
    if (error.name === 'AbortError') throw new Error('The request timed out. Refresh to check the saved settings before trying again.');
    throw error;
  } finally { clearTimeout(timer); }
}

function setSession(data) {
  adminCSRF = data.csrf_token;
  el('login').hidden = true;
  el('setup').hidden = true;
  el('management').hidden = false;
  el('logout').hidden = false;
  clearTimeout(sessionTimer);
  sessionTimer = setTimeout(() => signedOut('Your session expired. Sign in again to edit.'), Math.max(0, data.expires_at * 1000 - Date.now()));
}

async function checkSession() {
  try {
    const status = await adminFetch('status');
    message('page-status', '');
    if (!status.configured) {
      signedOut(); el('login').hidden = true; el('setup').hidden = false;
    } else if (!status.authenticated) signedOut();
    else { setSession(status); await loadNodes(); }
  } catch (_) { message('page-status', 'Could not connect. Check the Server connection and reload this page.'); }
}

function costText(node) {
  if (node.billing_amount === '') return 'Not set';
  if (Number(node.billing_amount) === 0) return 'Free';
  const period = {'1m':'/mo','3m':'/3mo','6m':'/6mo','12m':'/yr','24m':'/2yr','36m':'/3yr',forever:' · Lifetime'}[node.billing_period] || '';
  return node.billing_currency + ' ' + node.billing_amount + period;
}

function allowanceText(value) {
  const gb = Number(value);
  if (gb === 0) return '♾️';
  return gb >= 1000 ? (gb / 1000).toLocaleString('en', {maximumFractionDigits: 6}) + ' TB' : gb.toLocaleString('en', {maximumFractionDigits: 3}) + ' GB';
}

async function loadNodes() {
  const data = await adminFetch('nodes');
  if (!Array.isArray(data.nodes) || data.nodes.some(node => !Number.isSafeInteger(node.id))) throw new Error('Invalid node list');
  adminNodes = data.nodes;
  renderNodes();
}

function decimalSortValue(value, precision) {
  if (typeof value !== 'string' || !new RegExp('^\\d+(\\.\\d{1,' + precision + '})?$').test(value)) return null;
  const [whole, fraction = ''] = value.split('.');
  return BigInt(whole + fraction.padEnd(precision, '0'));
}

function billingSortValue(node) {
  const amount = decimalSortValue(node.billing_amount, 6);
  if (amount === null) return null;
  const months = {'1m':1,'3m':3,'6m':6,'12m':12,'24m':24,'36m':36}[node.billing_period];
  if (!months && node.billing_period !== 'forever') return null;
  return {currency: node.billing_currency, amount, months: BigInt(months || 1), lifetime: node.billing_period === 'forever' && amount > 0n};
}

function sortedAdminNodes() {
  const {key, direction} = adminSort;
  return adminNodes.slice().sort((a, b) => {
    const manual = a.position - b.position || a.id - b.id;
    if (!key) return manual;
    let av, bv, comparison;
    if (key === 'billing') {
      av = billingSortValue(a); bv = billingSortValue(b);
    } else if (key === 'renewal') {
      av = a.billing_period !== 'forever' && a.next_renewal || null;
      bv = b.billing_period !== 'forever' && b.next_renewal || null;
    } else {
      av = decimalSortValue(a.monthly_traffic_limit_gb, 9);
      bv = decimalSortValue(b.monthly_traffic_limit_gb, 9);
      if (av === 0n) av = Infinity;
      if (bv === 0n) bv = Infinity;
    }
    // Missing values stay last in either direction; equal values keep the
    // saved positions, without mutating the records used by the editor.
    if (av === null || bv === null) return (av === null) - (bv === null) || manual;
    if (key === 'billing') {
      // Currencies are never converted or interleaved. One-off lifetime
      // payments follow recurring prices; compare decimals without rounding.
      const group = av.currency.localeCompare(bv.currency) || Number(av.lifetime) - Number(bv.lifetime);
      if (group) return group;
      const left = av.amount * bv.months, right = bv.amount * av.months;
      comparison = left < right ? -1 : left > right ? 1 : 0;
    } else comparison = av < bv ? -1 : av > bv ? 1 : 0;
    return comparison * direction || manual;
  });
}

function toggleAdminSort(key) {
  adminSort = adminSort.key !== key ? {key, direction: 1}
    : adminSort.direction === 1 ? {key, direction: -1} : {key: '', direction: 1};
  renderNodes();
}

function providerWebsite(value) {
  if (!value) return '';
  try {
    const website = new URL(value);
    return ['http:', 'https:'].includes(website.protocol) && !website.username && !website.password ? website.href : '';
  } catch (_) { return ''; }
}

function renderNodes() {
  for (const button of sortButtons) {
    const active = button.dataset.sort === adminSort.key;
    button.setAttribute('aria-pressed', String(active));
    button.querySelector('.sort-indicator').textContent = active ? adminSort.direction === 1 ? '↑' : '↓' : '↕';
    const name = {billing:'Billing by currency and monthly equivalent',renewal:'Next renewal',traffic:'Monthly traffic'}[button.dataset.sort];
    button.setAttribute('aria-label', name + (active ? adminSort.direction === 1 ? ': ascending; show descending' : ': descending; restore saved order' : ': show ascending'));
  }
  const fragment = document.createDocumentFragment();
  for (const node of sortedAdminNodes()) {
    const row = document.createElement('article');
    row.className = 'admin-node'; row.setAttribute('role', 'listitem');
    row.innerHTML = '<div class="node-identity"><h3></h3><div class="node-summary"><span class="node-meta"></span><span class="node-note" hidden></span></div></div><div><span class="cell-label">Billing</span><span class="node-value billing"></span></div><div><span class="cell-label">Next renewal</span><span class="node-value renewal"></span></div><div><span class="cell-label">Monthly traffic</span><span class="node-value allowance"></span></div><div class="node-actions"><a class="node-action provider-link" target="_blank" rel="noopener noreferrer" hidden><svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"><path d="M15 3h6v6M10 14 21 3"/><path d="M21 14v5a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5"/></svg></a><button class="node-action node-edit" type="button"><svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"><path d="m16 3 5 5-12 12-6 1 1-6Z"/><path d="m14 5 5 5"/></svg></button></div>';
    row.querySelector('h3').textContent = node.display_name || node.hostname;
    row.querySelector('.node-meta').textContent = '#' + node.position + ' · ' + (node.region || 'Region not set') + (node.display_name ? ' · ' + node.hostname : '');
    row.querySelector('.billing').textContent = costText(node);
    const note = row.querySelector('.node-note');
    note.textContent = node.note || '';
    note.hidden = !node.note;
    row.querySelector('.node-summary').classList.toggle('has-note', !!node.note);
    row.querySelector('.renewal').textContent = node.billing_period === 'forever' ? '♾️' : node.next_renewal || 'Not set';
    row.querySelector('.allowance').textContent = allowanceText(node.monthly_traffic_limit_gb);
    const website = providerWebsite(node.provider_url);
    const link = row.querySelector('.provider-link');
    link.hidden = !website;
    if (website) {
      link.href = website;
      link.setAttribute('aria-label', 'Provider website for ' + (node.display_name || node.hostname) + ' (opens in new tab)');
    }
    row.querySelector('button').setAttribute('aria-label', 'Edit ' + (node.display_name || node.hostname));
    row.querySelector('button').addEventListener('click', () => openEditor(node));
    fragment.append(row);
  }
  el('node-list').replaceChildren(fragment);
  el('no-nodes').hidden = adminNodes.length !== 0;
}

function syncPeriod() {
  const dated = field('billing_period').value && field('billing_period').value !== 'forever';
  field('expires_at').disabled = !dated;
  field('expires_at').required = !!dated;
  if (!dated) field('expires_at').value = '';
}

function syncAllowance() {
  field('monthly_traffic_limit_gb').disabled = field('unlimited').checked;
  field('monthly_traffic_limit_gb').required = !field('unlimited').checked;
  if (field('unlimited').checked) field('monthly_traffic_limit_gb').value = '';
  else if (!field('monthly_traffic_limit_gb').value) field('monthly_traffic_limit_gb').value = '1000';
}

function openEditor(node) {
  editingNode = node;
  profileForm.reset();
  for (const name of ['display_name','note','provider_url','region','position','billing_currency','billing_amount','billing_period','expires_at','monthly_traffic_limit_gb']) field(name).value = node[name] ?? '';
  validateNote();
  field('position').max = String(adminNodes.length);
  field('unlimited').checked = Number(node.monthly_traffic_limit_gb) === 0;
  syncPeriod(); syncAllowance();
  el('editor-title').textContent = node.display_name || node.hostname;
  el('node-identity').textContent = 'Agent hostname: ' + node.hostname;
  el('projected-renewal').textContent = node.next_renewal ? 'Next scheduled renewal: ' + node.next_renewal + '. The date above is the saved renewal anchor.' : '';
  el('traffic-reset').textContent = 'Monthly reset: day ' + (node.traffic_reset_day || 1) + '. Editing billing dates keeps this reset day and accumulated usage.';
  message('profile-error', ''); el('reload-profile').hidden = true;
  if (!el('editor').open) el('editor').showModal();
}

el('login-form').addEventListener('submit', async event => {
  event.preventDefault();
  const form = event.currentTarget, button = form.querySelector('button');
  button.disabled = true; message('login-error', '');
  try {
    const data = await adminFetch('login', {method:'POST', body:JSON.stringify({username:form.elements.username.value,password:form.elements.password.value})});
    form.reset(); setSession(data); await loadNodes();
  } catch (error) { message(adminCSRF ? 'page-status' : 'login-error', error.message); }
  finally { button.disabled = false; }
});

profileForm.addEventListener('submit', async event => {
  event.preventDefault();
  if (!editingNode || !adminCSRF) return;
  validateNote();
  if (!profileForm.reportValidity()) return;
  const node = editingNode;
  const payload = {version:node.version, order_version:node.order_version, position:Number(field('position').value)};
  for (const name of ['display_name','note','provider_url','region','billing_currency','billing_amount','billing_period','expires_at']) payload[name] = field(name).value;
  payload.monthly_traffic_limit_gb = field('unlimited').checked ? '0' : field('monthly_traffic_limit_gb').value;
  el('save-profile').disabled = true; message('profile-error', '');
  try {
    await adminFetch('nodes/' + node.id, {method:'PATCH',body:JSON.stringify(payload)});
    el('editor').close(); editingNode = null;
    message('save-notice', 'Settings saved. The public dashboard will update automatically.');
    try { await loadNodes(); }
    catch (_) { message('page-status', 'Settings were saved. Refresh to reload the node list.'); }
  } catch (error) {
    if (error.status !== 401) message('profile-error', error.message);
    el('reload-profile').hidden = error.status !== 409 && error.status != null;
  } finally { el('save-profile').disabled = false; }
});

el('logout').addEventListener('click', async () => {
  try { await adminFetch('logout', {method:'POST',body:'{}'}); signedOut(); }
  catch (error) { message('page-status', error.message); }
});
for (const button of sortButtons) button.addEventListener('click', () => toggleAdminSort(button.dataset.sort));
el('reload-profile').addEventListener('click', async () => {
  const id = editingNode?.id;
  try { await loadNodes(); const current = adminNodes.find(node => node.id === id); if (current) openEditor(current); else el('editor').close(); }
  catch (error) { message('profile-error', error.message); }
});
for (const id of ['close-editor','cancel-editor']) el(id).addEventListener('click', () => el('editor').close());
field('billing_period').addEventListener('change', syncPeriod);
field('unlimited').addEventListener('change', syncAllowance);
function validateNote() {
  const note = field('note');
  note.setCustomValidity(Array.from(note.value.trim()).length > 40 ? 'Use up to 40 characters.' : '');
}
field('note').addEventListener('input', validateNote);
el('setup-reload').addEventListener('click', checkSession);
window.addEventListener('pageshow', () => { if (adminCSRF) checkSession(); });
checkSession();
