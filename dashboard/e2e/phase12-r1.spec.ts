import { test, expect, type Page } from '@playwright/test';
import { apiLogin } from './auth';
const A='aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', B='bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb';
const period={start_local:'2026-09-20',end_local_exclusive:'2026-10-01'};
function overview(n:string){return {period,summary:{transaction_count:1,units_sold:1,return_transaction_count:0,units_returned:0,currency_totals:[{currency:'EGP',subtotal_minor:n,discount_minor:'0',tax_minor:'0',sales_total_minor:n,line_cost_minor:'0',refund_total_minor:'0',net_sales_minor:n,returned_units:0,returned_cost_minor:'0',net_cost_minor:'0'}]},normalized:{normalized_total_minor:n,normalized_refund_minor:'0',normalized_net_minor:n,transactions:1,units:1,return_transactions:0,units_returned:0,usd_sale_count:0},averages:{all:{transactions:1,units:1,average_minor:n},egp:{transactions:1,units:1,average_minor:n},usd:{transactions:0,units:0,average_minor:'0'}},fx:{has_usd:false,latest_rate:null,multiple_rates_used:false}};}
function online(name:string){return {currency_totals:[{currency:'EGP',orders:1,value_minor:'4400'}],status_counts:[],provider_totals:[{provider_key:name,currency:'EGP',orders:1,value_minor:'4400'}]};}
function tags(name:string){return {rows:[{tag_id:A,tag_slug:'gold',name_ar:name,name_en:name,units:1,units_returned:0,currencies:[{currency:'EGP',line_sales_minor:'100',line_refund_minor:'0',net_minor:'100'}]}],overlap_note:''};}
function health(name:string){return {providers:['alpha','zeta'],counts:[{reason_code:'CATALOG_MISSING_SKU',products:1}],detail:[{reason_code:'CATALOG_MISSING_SKU',product_id:A,provider_key:'',name,sku:name}],detail_limit:50,detail_truncated:false};}
function gate(){let resolve!:()=>void;const promise=new Promise<void>(r=>resolve=r);return {promise,resolve};}
// Explicit dev-stack account + TOTP (no default account): see auth.ts.
async function auth(page:Page){await apiLogin(page);}
async function defaults(page:Page){await page.route('**/api/v1/dashboard/**',async route=>{
 const u=new URL(route.request().url()), path=u.pathname;
 if(path.includes('/auth/'))return route.continue();
 let data:unknown={};
 if(path.endsWith('/stores'))data={stores:[A,B].map(id=>({store_id:id,display_name:id===A?'Store A':'Store B',timezone:'Africa/Cairo',status:'active',device_count:1}))};
 else if(path.endsWith('/overview'))data=overview('1100');
 else if(path.endsWith('/daily'))data={days:[],display_currency:'EGP',normalized:false};
 else if(['/products','/categories','/branches'].some(s=>path.endsWith(s)))data={rows:[]};
 else if(path.endsWith('/sync-health'))data={freshness:{},pending_count:0,retry_count:0,return_pending_count:0,return_retry_count:0};
 else if(path.endsWith('/activity'))data={items:[]}; else if(path.endsWith('/sales/latest'))data={sales:[]};
 else if(path.endsWith('/orders'))data={orders:[],status_counts:[],webhook_inbox:{}};
 else if(path.endsWith('/orders/summary'))data=online('A_ONLINE');else if(path.endsWith('/tags'))data=tags('A_TAG');else if(path.endsWith('/catalog-health'))data=health('A_HEALTH');
 await route.fulfill({json:data});
});}

test('actual historical Gold/Golden API rows render in production assets without duplicate keys',async({page})=>{
 await auth(page);const errors:string[]=[];page.on('pageerror',e=>errors.push(e.message));
 const query=`?period=custom&from_date=2026-09-20&to_date=2026-09-30&store_id=${A}`;
 const response=await page.request.get(`/api/v1/dashboard/tags${query}`);expect(response.status()).toBe(200);const data=await response.json();expect(data.rows).toHaveLength(2);
 await page.goto(`/dashboard/categories${query}`);const rows=page.locator('table').filter({has:page.locator('th').filter({hasText:'Tag'})}).locator('tbody tr');await expect(rows).toHaveCount(2);await expect(rows.filter({hasText:'Gold'})).toHaveCount(2);
 await page.route('**/api/v1/dashboard/tags?**',r=>r.fulfill({json:{...data,rows:[...data.rows].reverse()}}));await page.getByRole('button',{name:'عرض / Show',exact:true}).click();await expect(rows).toHaveCount(2);expect(errors.filter(e=>e.includes('duplicate'))).toEqual([]);
});
for(const widget of ['tags','catalog-health'])test(`${widget}: held Store B response hides Store A data and failure never restores it`,async({page})=>{
 await auth(page);await defaults(page);const held=gate(),requested=gate();await page.route(`**/api/v1/dashboard/${widget}?**`,async r=>{const s=new URL(r.request().url()).searchParams.get('store_id');if(s===A)return r.fulfill({json:widget==='tags'?tags('A_TAG'):health('A_HEALTH')});requested.resolve();await held.promise;await r.fulfill({status:503,json:{error:{code:'UNAVAILABLE'}}});});
 await page.goto(`/dashboard/${widget==='tags'?'categories':'sync'}?store_id=${A}`);const old=widget==='tags'?'A_TAG':'A_HEALTH';await expect(page.getByText(old,{exact:true}).first()).toBeVisible();await page.getByTestId('store-selector').selectOption(B);await requested.promise;await expect(page.getByText(old,{exact:true})).toHaveCount(0);held.resolve();await expect(page.getByText(old,{exact:true})).toHaveCount(0);
});
for(const first of ['Retail','online'])test(`comparison resolves ${first} first without mixing Store scopes`,async({page})=>{
 await auth(page);await defaults(page);const retail=gate(),orders=gate(),seenR=gate(),seenO=gate();
 await page.route('**/api/v1/dashboard/overview?**',async r=>{if(new URL(r.request().url()).searchParams.get('store_id')===A)return r.fulfill({json:overview('1100')});seenR.resolve();await retail.promise;await r.fulfill({json:overview('2200')});});
 await page.route('**/api/v1/dashboard/orders/summary?**',async r=>{if(new URL(r.request().url()).searchParams.get('store_id')===A)return r.fulfill({json:online('A_ONLINE')});seenO.resolve();await orders.promise;await r.fulfill({json:online('B_ONLINE')});});
 await page.goto(`/dashboard/sales?store_id=${A}`);await expect(page.getByText('A_ONLINE',{exact:true})).toBeVisible();await page.getByTestId('store-selector').selectOption(B);await Promise.all([seenR.promise,seenO.promise]);if(first==='Retail')retail.resolve();else orders.resolve();await expect(page.getByText('A_ONLINE',{exact:true})).toHaveCount(0);await expect(page.getByText('B_ONLINE',{exact:true})).toHaveCount(0);if(first==='Retail')orders.resolve();else retail.resolve();await expect(page.getByText('B_ONLINE',{exact:true})).toBeVisible();await expect(page.getByLabel('Finalized Retail Sales / المبيعات النهائية')).toContainText('22');
});

test('late A response and rapid A→B→A cannot restore superseded Tags',async({page})=>{
 await auth(page);await defaults(page);const old=gate(),seen=gate();let aCalls=0;
 await page.route('**/api/v1/dashboard/tags?**',async r=>{const s=new URL(r.request().url()).searchParams.get('store_id');if(s===A&&++aCalls===1){seen.resolve();await old.promise;await r.fulfill({json:tags('A_STALE')});return;}await r.fulfill({json:tags(s===B?'B_CURRENT':'A_NEW')});});
 await page.goto(`/dashboard/categories?store_id=${A}`);await seen.promise;await page.getByTestId('store-selector').selectOption(B);await expect(page.getByText('B_CURRENT',{exact:true}).first()).toBeVisible();old.resolve();await expect(page.getByText('A_STALE',{exact:true})).toHaveCount(0);
 await page.getByTestId('store-selector').selectOption(A);await expect(page.getByText('A_NEW',{exact:true}).first()).toBeVisible();await page.getByTestId('store-selector').selectOption(B);await page.getByTestId('store-selector').selectOption(A);await expect(page.getByText('A_NEW',{exact:true}).first()).toBeVisible();await expect(page.getByText('B_CURRENT',{exact:true})).toHaveCount(0);
});
test('period/currency requests narrow comparison and history restores coherent scope',async({page})=>{
 await auth(page);await defaults(page);const calls:URL[]=[];
 await page.route('**/api/v1/dashboard/orders/summary?**',async r=>{const u=new URL(r.request().url());calls.push(u);const currency=u.searchParams.get('currency')||'EGP';await r.fulfill({json:{...online(`ONLINE_${currency}`),currency_totals:[{currency,orders:1,value_minor:'4400'}]}})});
 await page.goto(`/dashboard/sales?store_id=${A}&currency=EGP`);await expect(page.getByText('ONLINE_EGP',{exact:true})).toBeVisible();await page.getByRole('button',{name:'USD',exact:true}).first().click();await expect(page.getByText('ONLINE_USD',{exact:true})).toBeVisible();expect(calls.at(-1)?.searchParams.get('currency')).toBe('USD');await expect(page.getByLabel('Finalized Retail Sales / المبيعات النهائية')).not.toContainText('net EGP');await expect(page.getByLabel('Online Orders / الطلبات عبر الإنترنت')).toContainText('order value USD');
 await page.getByRole('button',{name:'أمس',exact:true}).click();await expect.poll(()=>calls.at(-1)?.searchParams.get('period')).toBe('yesterday');await page.goBack();await expect.poll(()=>calls.at(-1)?.searchParams.get('period')).toBe('last_10_completed_days');await page.goBack();await expect(page.getByText('ONLINE_EGP',{exact:true})).toBeVisible();
});
test('health provider change clears old data, has complete choices and retries coherently',async({page})=>{
 await auth(page);await defaults(page);const held=gate(),seen=gate();let failed=false;
 await page.route('**/api/v1/dashboard/catalog-health?**',async r=>{const p=new URL(r.request().url()).searchParams.get('provider_key');if(p==='zeta'&&!failed){seen.resolve();await held.promise;failed=true;return r.fulfill({status:503,json:{error:{code:'UNAVAILABLE'}}})}await r.fulfill({json:{...health(p==='zeta'?'ZETA_ONLY':'ALL_OLD'),detail_truncated:true}})});
 await page.goto(`/dashboard/sync?store_id=${A}`);await expect(page.getByText('ALL_OLD',{exact:true})).toBeVisible();await expect(page.locator('#health-provider option[value="zeta"]')).toHaveCount(1);await page.locator('#health-provider').selectOption('zeta');await seen.promise;await expect(page.getByText('ALL_OLD',{exact:true})).toHaveCount(0);held.resolve();await page.getByRole('button',{name:'إعادة المحاولة / Retry',exact:true}).click();await expect(page.getByText('ZETA_ONLY',{exact:true})).toBeVisible();await expect(page.getByText('ALL_OLD',{exact:true})).toHaveCount(0);
});
