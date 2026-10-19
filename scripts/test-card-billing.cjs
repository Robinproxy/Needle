const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const path=require('node:path');
const ctx={localStorage:{getItem:()=>null}};
vm.createContext(ctx);
const source=fs.readFileSync(path.join(__dirname,'../internal/server/static/app.js'),'utf8');
vm.runInContext(source.slice(0,source.indexOf('let themeMode =')),ctx);
const node=(currency,amount,period)=>({agent:{id:1,hostname:'node',billing_cost:{currency,amount},billing_period:period}});
for(const [currency,amount,period,want] of [
  ['USD','2.5','1m','US$2.50/mo'],['CNY','80','3m','CN¥80.00/3mo'],
  ['EUR','15','6m','€15.00/6mo'],['GBP','24','12m','£24.00/yr'],
  ['CAD','90.1','24m','CA$90.10/2yr'],['HKD','120','36m','HK$120.00/3yr'],
  ['JPY','750','1m','JP¥750/mo'],['AUD','8','1m','AU$8.00/mo'],
  ['CHF','0.123456','12m','CHF 0.123456/yr'],['USD','99','forever','US$99.00 · Lifetime'],
  ['USD','0','forever','Free'],['CNY','','1m','Not set'],
]) assert.equal(ctx.billingCostText(node(currency,amount,period)),want);
assert.equal(ctx.billingCostText({agent:{}}),'Not set');
for(const [currency,amount] of [['USD','<script>'],['<img>','5'],['USD',null]]){
  const html=ctx.cardBillingHTML(node(currency,amount,'1m'));
  assert(html.includes('Not set'));
  assert(!html.includes('<script>')&&!html.includes('<img>'));
}
for(const [days,text] of [[20,'20d'],[0,'Due today'],[-3,'Due today']]){
  const a={...node('USD','5','1m'),expiry_days:days,expiry_date:'2026-11-08'};
  const badge=ctx.expiryBadge(a);
  assert(badge.includes('>'+text+'</span>'));
  assert(!badge.includes('title='),'countdown must have no due-date hover tooltip');
  const card=ctx.renderCard({...a,tcpping_enabled:false},false);
  assert(card.indexOf('TCP Ping · Off')<card.indexOf('class="card-billing"'));
  assert(card.includes('datetime="2026-11-08"'));
  assert(!card.includes('title="Due'));
}
const permanent=node('USD','0','forever');
assert(ctx.expiryBadge(permanent).includes('♾️'));
assert(!ctx.expiryBadge(permanent).includes('title='));
assert(ctx.cardBillingHTML(permanent).includes('Free'));
assert(ctx.cardBillingHTML(permanent).includes('♾️'));
assert.equal(ctx.expiryBadge({agent:{},expiry_days:0,expiry_date:''}),'');
assert(!ctx.cardBillingHTML({agent:{},expiry_date:'2026-11-08" onmouseover="alert(1)'}).includes('onmouseover'));
console.log('Card billing checks passed: currencies, exact amounts, periods, free/unset, due/overdue, permanent, tooltip removal, and safe output.');
