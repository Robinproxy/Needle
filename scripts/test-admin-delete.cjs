const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const root = path.join(__dirname, '../internal/server/static');
const source = fs.readFileSync(path.join(root, 'admin.js'), 'utf8');
const html = fs.readFileSync(path.join(root, 'admin.html'), 'utf8');
assert.match(html, /id="cancel-delete"[^>]*type="button"[^>]*autofocus/);
assert.match(html, /id="confirm-delete"[^>]*type="button"/);
assert(html.includes('This permanently deletes the node, its Token, history and traffic records.'));

function element() {
  const children = new Map();
  return {textContent:'',hidden:false,value:'',dataset:{},attributes:{},events:{},open:false,
    classList:{toggle(){}},reset(){},focus(){this.focused=true;},
    setAttribute(name,value){this.attributes[name]=value;},
    addEventListener(name,callback){this.events[name]=callback;},
    querySelector(name){if(!children.has(name))children.set(name,element());return children.get(name);},
    append(row){(this.rows??=[]).push(row);},replaceChildren(fragment){this.rows=fragment?.rows??[];},
    showModal(){this.open=true;},close(){this.open=false;this.events.close?.();}};
}
const fixture = id => ({id,hostname:'node-'+id,display_name:id===1?'<img src=x onerror=bad>':'',
  position:id,version:0,order_version:4,billing_currency:'USD',billing_amount:'5',billing_period:'1m',monthly_traffic_limit_gb:'500'});
const response = (status,data) => ({ok:status>=200&&status<300,status,json:async()=>data});
function harness() {
  const elements = new Map();
  const get = id => {if(!elements.has(id))elements.set(id,element());return elements.get(id);};
  get('profile-form').elements={namedItem:name=>get('field-'+name)};
  const requests=[];
  const context={console,URL,AbortController,setTimeout,clearTimeout,location:{reload(){}},
    window:{addEventListener(){}},localStorage:{getItem:()=>null},
    document:{getElementById:get,documentElement:element(),querySelectorAll:()=>[],createElement:element,createDocumentFragment:element},
    fixtures:[fixture(1),fixture(2)],
    fetch:async(url,options)=>{
      requests.push({url,options});
      if(options.method==='DELETE') {
        const result=await context.deletion();
        if(result.ok)context.fixtures=context.fixtures.filter(node=>url!=='/api/admin/nodes/'+node.id);
        return result;
      }
      if(context.reloadFails)return response(500,{error:'Reload failed'});
      return response(200,url.endsWith('/nodes')?{nodes:context.fixtures}:{currency:'USD'});
    },deletion:async()=>response(200,{status:'ok'}),
  };
  vm.createContext(context);
  vm.runInContext(source.replace(/\ncheckSession\(\);\s*$/,''),context);
  vm.runInContext("adminCSRF='test-csrf';",context);
  return {context,get,requests,click:()=>get('confirm-delete').events.click()};
}

(async()=>{
  const h=harness(),{context:ctx,get,requests,click}=h;
  await ctx.loadNodes();
  const row=get('node-list').rows[0];
  assert.equal(row.querySelector('.node-delete').attributes['aria-label'],'Delete '+ctx.fixtures[0].display_name);
  row.querySelector('.node-delete').events.click();
  assert.equal(get('delete-dialog').open,true);
  assert.equal(get('delete-title').textContent,'Delete '+ctx.fixtures[0].display_name+'?');
  assert.equal(get('delete-identity').textContent,'Agent hostname: node-1 · ID 1');
  assert(get('cancel-delete').focused,'Cancel gets the default focus');
  assert(!requests.some(request=>request.options.method==='DELETE'),'opening must not delete');
  get('cancel-delete').events.click();
  await click();
  assert(!requests.some(request=>request.options.method==='DELETE'),'canceling must clear the target');

  // Delayed replies cannot create duplicate deletes or allow switching targets.
  let resolve;
  ctx.deletion=()=>new Promise(done=>{resolve=done;});
  ctx.openDeleteDialog(ctx.fixtures[0]);
  const pending=click();
  assert(get('confirm-delete').disabled && get('cancel-delete').disabled);
  await click();
  ctx.openDeleteDialog(ctx.fixtures[1]);
  assert(get('delete-identity').textContent.includes('node-1'));
  let prevented=false;
  get('delete-dialog').events.cancel({preventDefault(){prevented=true;}});
  assert(prevented,'Escape cannot close an in-flight deletion');
  assert.equal(requests.filter(request=>request.options.method==='DELETE').length,1);
  const sent=requests.find(request=>request.options.method==='DELETE');
  assert.equal(sent.url,'/api/admin/nodes/1');
  assert.equal(sent.options.credentials,'same-origin');
  assert.equal(sent.options.headers['X-CSRF-Token'],'test-csrf');
  assert.deepEqual(JSON.parse(sent.options.body),{hostname:'node-1',version:0,order_version:4});
  resolve(response(200,{status:'ok'}));await pending;
  assert.equal(get('delete-dialog').open,false);
  assert.equal(get('save-notice').textContent,'Node deleted.');
  assert.equal(get('node-list').rows.length,1);
  assert.equal(get('node-list').rows[0].querySelector('h3').textContent,'node-2');

  for(const status of [500,409,404,401]){
    const test=harness();await test.context.loadNodes();
    test.context.deletion=async()=>response(status,{error:'Delete failed '+status});
    test.context.openDeleteDialog(test.context.fixtures[0]);await test.click();
    if(status===401){
      assert.equal(test.get('delete-dialog').open,false);
      assert.equal(test.get('management').hidden,true);
      assert.equal(test.get('login').hidden,false);
    }else{
      assert.equal(test.get('delete-dialog').open,true);
      assert.equal(test.get('node-list').rows.length,2,'failed deletes retain both nodes');
      assert.equal(test.get('delete-error').textContent,'Delete failed '+status);
      assert.equal(test.get('confirm-delete').disabled,status===409||status===404);
      assert.equal(test.get('cancel-delete').disabled,false);
    }
  }
  const reload=harness();await reload.context.loadNodes();
  reload.context.openDeleteDialog(reload.context.fixtures[0]);reload.context.reloadFails=true;
  await reload.click();
  assert.equal(reload.get('delete-dialog').open,false);
  assert.equal(reload.get('node-list').rows.length,1);
  assert.equal(reload.get('save-notice').textContent,'Node deleted.');
  assert(reload.get('page-status').textContent.includes('Node was deleted'));
  vm.runInContext("adminCSRF='';",reload.context);
  reload.context.openDeleteDialog(reload.context.fixtures[0]);
  assert.equal(reload.get('delete-dialog').open,false,'signed-out users cannot open deletion');
  console.log('Admin deletion checks passed: explicit confirmation, safe labels, cancel focus, single submission, versioned identity, failures, conflicts, session expiry and reload recovery.');
})().catch(error=>{console.error(error);process.exitCode=1;});
