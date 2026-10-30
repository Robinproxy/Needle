const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const root = path.join(__dirname, '../internal/server/static');
const html = fs.readFileSync(path.join(root, 'admin.html'), 'utf8');
const source = fs.readFileSync(path.join(root, 'admin.js'), 'utf8');
assert.match(html, /<button[^>]*type="button"[^>]*id="refresh-admin"/);
assert(!html.includes('reload-nodes'));
assert(!source.includes('reload-nodes'));
assert.match(html, /name="provider_url" type="url"/);
assert(source.includes('target="_blank" rel="noopener noreferrer"'));

function element() {
  const children = new Map();
  return {textContent:'',hidden:false,value:'',dataset:{},attributes:{},events:{},
    classList:{toggle(){}},setCustomValidity(){},reset(){},showModal(){this.open=true;},close(){this.open=false;},
    setAttribute(key,value){this.attributes[key]=value;},
    addEventListener(key,listener){this.events[key]=listener;},
    querySelector(key){if(!children.has(key))children.set(key,element());return children.get(key);},
    append(row){(this.rows??=[]).push(row);},replaceChildren(fragment){this.rows=fragment?.rows??[];}};
}
const elements = new Map();
const get = id => {if(!elements.has(id))elements.set(id,element());return elements.get(id);};
get('profile-form').elements = {namedItem:name=>get('field-'+name)};
const buttons = ['billing','renewal','traffic'].map(key=>Object.assign(element(),{dataset:{sort:key}}));
const requests = [];
const ctx = {console,URL,AbortController,setTimeout,clearTimeout,location:{href:'https://needle.test/admin?check=1',reload(){ctx.reloads++;}},reloads:0,
  localStorage:{getItem:()=>null},window:{addEventListener(){}},
  document:{getElementById:get,documentElement:element(),querySelectorAll:()=>buttons,
    createElement:element,createDocumentFragment:element},
  fetch:async(url,options)=>{requests.push({url,options});return {ok:true,json:async()=>({nodes:ctx.fixtures})};}};
vm.createContext(ctx);
vm.runInContext(source.replace(/\ncheckSession\(\);\s*$/, ''),ctx);
const base = {billing_currency:'USD',billing_period:'1m',billing_amount:'12',next_renewal:'2026-11-10',monthly_traffic_limit_gb:'500'};
const fixture = (id,values={})=>({...base,id,position:id,hostname:'node-'+id,...values});
const nodes = [
  fixture(1,{billing_period:'forever',billing_amount:'10',next_renewal:'2000-01-01',monthly_traffic_limit_gb:'0'}),
  fixture(2),
  fixture(3,{billing_period:'12m',billing_amount:'60',next_renewal:'2026-12-01',monthly_traffic_limit_gb:'1000'}),
  fixture(4,{billing_currency:'CNY',billing_amount:'0',next_renewal:'2026-11-03',monthly_traffic_limit_gb:'2000',provider_url:'https://example.com/billing?a=1&b=2'}),
  fixture(5,{billing_amount:'',billing_period:'',next_renewal:'',monthly_traffic_limit_gb:'0'}),
  fixture(6,{billing_amount:'5',next_renewal:'2026-11-03',monthly_traffic_limit_gb:'1000'}),
  fixture(7,{billing_currency:'CNY',billing_amount:'38',next_renewal:'2026-11-08'}),
  fixture(8,{billing_amount:'',next_renewal:'',monthly_traffic_limit_gb:''}),
];
const order = ()=>Array.from(ctx.sortedAdminNodes(),node=>node.id);
const click = key=>buttons.find(button=>button.dataset.sort===key).events.click();

(async()=>{
  get('refresh-admin').events.click();
  assert.equal(ctx.reloads,1);
  assert.equal(ctx.location.href,'https://needle.test/admin?check=1','brand must reload the current page without navigation');
  ctx.fixtures=structuredClone(nodes).reverse();
  await ctx.loadNodes();
  const original=JSON.stringify(ctx.fixtures);
  assert.deepEqual(order(),[1,2,3,4,5,6,7,8]);
  for (const [key,ascending,descending] of [
    ['billing',[4,7,3,6,2,1,5,8],[7,4,2,3,6,1,5,8]],
    ['renewal',[4,6,7,2,3,1,5,8],[3,2,7,4,6,1,5,8]],
    ['traffic',[2,7,3,6,4,1,5,8],[1,5,4,3,6,2,7,8]],
  ]) {
    click(key); assert.deepEqual(order(),ascending,key+' ascending');
    const active=buttons.find(button=>button.dataset.sort===key);
    assert.equal(active.attributes['aria-pressed'],'true');
    assert.equal(active.querySelector('.sort-indicator').textContent,'↑');
    assert(buttons.filter(button=>button!==active).every(button=>button.attributes['aria-pressed']==='false'));
    click(key); assert.deepEqual(order(),descending,key+' descending');
    assert.equal(active.querySelector('.sort-indicator').textContent,'↓');
    click(key); assert.deepEqual(order(),[1,2,3,4,5,6,7,8],key+' restored positions');
    assert.equal(active.attributes['aria-pressed'],'false');
  }
  assert.equal(JSON.stringify(ctx.fixtures),original,'sorting must not mutate saved positions or profile versions');
  assert(requests.every(request=>!request.options.method),'view sorting must not send profile mutations');
  click('traffic');click('renewal');
  assert.deepEqual(order(),[4,6,7,2,3,1,5,8],'switching column starts ascending');
  await ctx.loadNodes();
  assert.deepEqual(order(),[4,6,7,2,3,1,5,8],'reload after a save must retain the active sort');
  get('node-list').rows[0].querySelector('button').events.click();
  assert.equal(get('editor-title').textContent,'node-4','editing a sorted row must select its original node');
  assert.equal(get('field-position').value,4,'sorting must not rewrite the position in the editor');
  assert.equal(get('field-provider_url').value,nodes[3].provider_url,'the editor must load the saved provider URL');
  const linked=get('node-list').rows[0].querySelector('.provider-link');
  assert.equal(linked.href,nodes[3].provider_url);
  assert.equal(linked.hidden,false);
  assert.equal(get('node-list').rows[1].querySelector('.provider-link').hidden,true,'missing URL must not show a placeholder button');
  assert(!get('node-list').rows[0].innerHTML.includes(nodes[3].provider_url),'URLs must be assigned through DOM properties, not interpolated into HTML');
  for (const bad of ['javascript:alert(1)','data:text/html,<script>alert(1)</script>','file:///etc/passwd','ftp://example.com','//example.com','/admin','https://user:password@example.com']) {
    assert.equal(ctx.providerWebsite(bad),'','unsafe navigation must be rejected: '+bad);
  }
  assert.equal(ctx.providerWebsite('https://example.com'),'https://example.com/');
  assert.equal(ctx.providerWebsite('http://example.com:8080/billing'),'http://example.com:8080/billing');
  const edited={...nodes[3],provider_url:''};
  ctx.fixtures=nodes.map(node=>node.id===edited.id?edited:node);
  await ctx.loadNodes();
  assert.equal(get('node-list').rows[0].querySelector('.provider-link').hidden,true,'clearing the URL must hide the link on reload');

  // Adjacent supported prices can lose their fractional digits as JS Numbers.
  ctx.fixtures=[fixture(9,{billing_amount:'999999999999.000002'}),fixture(10,{billing_amount:'999999999999.000001'})];
  await ctx.loadNodes();click('billing');
  assert.deepEqual(order(),[10,9],'billing comparisons must preserve six decimal places');
  assert.equal(ctx.billingSortValue(fixture(11,{billing_period:'forever',billing_amount:'0'})).lifetime,false,'free lifetime plans have zero monthly cost');
  ctx.fixtures=[fixture(9,{monthly_traffic_limit_gb:'8999999.999999999'}),fixture(10,{monthly_traffic_limit_gb:'8999999.999999998'})];
  await ctx.loadNodes();click('traffic');
  assert.deepEqual(order(),[10,9],'large allowances one byte apart must keep their order');
  ctx.fixtures=[];await ctx.loadNodes();
  assert.equal(get('no-nodes').hidden,false);
  console.log('Admin checks passed: same-page refresh, three sorting modes, exact values, unchanged positions, correct editor selection, and safe optional provider links.');
})().catch(error=>{console.error(error);process.exitCode=1;});
