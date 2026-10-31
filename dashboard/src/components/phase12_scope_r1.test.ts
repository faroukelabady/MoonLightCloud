import {render,fireEvent,waitFor,cleanup} from '@testing-library/svelte';
import {it,expect,vi,afterEach, beforeEach } from 'vitest';
import App from '../App.svelte';
import {dashboardApi,ApiError} from '../lib/api.js';
import {choose} from '../test/choose.js';
import { setLang } from '../lib/i18n.svelte.js';

// These tests read the English interface; each language is shown alone.
beforeEach(() => setLang('en'));
vi.mock('../lib/chartAction.js',()=>({chart:()=>({destroy(){}})}));
afterEach(()=>{cleanup();vi.restoreAllMocks();});
const A='aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa',B='bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb';
// An all-Stores OWNER with a FULL (MFA-complete) session (ADR-0053).
const ME={authenticated:true,stage:'full' as const,csrf_token:'t',user:{id:'u',login:'review@test',display_name:'review',role:'owner' as const,mfa_enabled:true},permissions:['reports.read','catalog.read'],all_stores:true,store_ids:[],session:{expires_at:'',idle_timeout_seconds:1800}};
const bucket={currency:'EGP',line_sales_minor:'100',line_refund_minor:'0',net_minor:'100'};
function tags(name:string){return {generated_at:'',timezone:'Africa/Cairo',store_id:null,overlap_note:'Tag totals overlap and must not be summed',rows:[{tag_id:A,tag_slug:'gold',name_ar:name,name_en:name,units:1,units_returned:0,currencies:[bucket]}]};}
it('R1 regression: Store B selected while loaded Store A Tags remain visible',async()=>{
 window.history.replaceState({},'',`/dashboard/categories?store_id=${A}`);
 for(const k of Object.keys(dashboardApi)) vi.spyOn(dashboardApi,k as any).mockImplementation(async()=>({}) as any);
 vi.mocked(dashboardApi.me).mockResolvedValue(ME);
 vi.mocked(dashboardApi.stores).mockResolvedValue({stores:[A,B].map(id=>({store_id:id,display_name:id===A?'Store A':'Store B',timezone:'Africa/Cairo',status:'active',device_count:1,created_at:'',updated_at:''}))});
 vi.mocked(dashboardApi.overview).mockResolvedValue({period:{start_local:'2026-10-03',end_local_exclusive:'2026-10-04'},summary:{transaction_count:0,return_transaction_count:0,currency_totals:[]},normalized:{},averages:{},fx:{}} as any);
 vi.mocked(dashboardApi.daily).mockResolvedValue({days:[],display_currency:'EGP',normalized:false} as any);
 for(const k of ['products','categories','branches'] as const) vi.mocked(dashboardApi[k]).mockResolvedValue({rows:[]} as any);
 vi.mocked(dashboardApi.syncHealth).mockResolvedValue({freshness:{},pending_count:0,retry_count:0,return_pending_count:0,return_retry_count:0} as any);
 vi.mocked(dashboardApi.activityPage).mockResolvedValue({items:[],offset:0,has_more:false});vi.mocked(dashboardApi.syncSummary).mockResolvedValue({categories:[],total_synced:0});vi.mocked(dashboardApi.latestSales).mockResolvedValue({sales:[]});
 vi.mocked(dashboardApi.orders).mockResolvedValue({orders:[],next_cursor:null,status_counts:[],webhook_inbox:{}} as any);
 vi.mocked(dashboardApi.orderSummary).mockResolvedValue({currency_totals:[],status_counts:[],provider_totals:[]} as any);
 vi.mocked(dashboardApi.catalogHealth).mockResolvedValue({counts:[],detail:[]} as any);
 vi.mocked(dashboardApi.tags).mockImplementation(async(_p,_c,s)=>s===A?tags('ONLY_STORE_A'):await new Promise(()=>{}));
 const r=render(App);
 await waitFor(()=>expect(r.container.textContent).toContain('ONLY_STORE_A'));
 await choose(r.getByTestId('store-selector'),B);
 await waitFor(()=>expect(vi.mocked(dashboardApi.tags).mock.calls.some(c=>c[2]===B)).toBe(true));
 expect(r.getByTestId('store-selector').dataset.value).toBe(B);
 expect(r.container.textContent).not.toContain('ONLY_STORE_A');
});
it('R1 regression: Store B selected while loaded Store A health remains visible',async()=>{
 window.history.replaceState({},'',`/dashboard/sync?store_id=${A}`);
 for(const k of Object.keys(dashboardApi)) vi.spyOn(dashboardApi,k as any).mockImplementation(async()=>({}) as any);
 vi.mocked(dashboardApi.me).mockResolvedValue(ME);
 vi.mocked(dashboardApi.stores).mockResolvedValue({stores:[A,B].map(id=>({store_id:id,display_name:id===A?'Store A':'Store B',timezone:'Africa/Cairo',status:'active',device_count:1,created_at:'',updated_at:''}))});
 vi.mocked(dashboardApi.overview).mockResolvedValue({period:{start_local:'2026-10-03',end_local_exclusive:'2026-10-04'},summary:{transaction_count:0,return_transaction_count:0,currency_totals:[]},normalized:{},averages:{},fx:{}} as any);
 vi.mocked(dashboardApi.daily).mockResolvedValue({days:[],display_currency:'EGP',normalized:false} as any);
 for(const k of ['products','categories','branches'] as const) vi.mocked(dashboardApi[k]).mockResolvedValue({rows:[]} as any);
 vi.mocked(dashboardApi.syncHealth).mockResolvedValue({freshness:{},pending_count:0,retry_count:0,return_pending_count:0,return_retry_count:0} as any);
 vi.mocked(dashboardApi.activityPage).mockResolvedValue({items:[],offset:0,has_more:false});vi.mocked(dashboardApi.syncSummary).mockResolvedValue({categories:[],total_synced:0});vi.mocked(dashboardApi.latestSales).mockResolvedValue({sales:[]});
 vi.mocked(dashboardApi.orders).mockResolvedValue({orders:[],next_cursor:null,status_counts:[],webhook_inbox:{}} as any);
 vi.mocked(dashboardApi.orderSummary).mockResolvedValue({currency_totals:[],status_counts:[],provider_totals:[]} as any);
 vi.mocked(dashboardApi.catalogHealth).mockImplementation(async(s)=>s===A?{generated_at:'',store_id:null,provider_key:'',reason_codes:[],counts:[{reason_code:'VARIANT_MISSING_SKU',products:1}],detail:[{reason_code:'VARIANT_MISSING_SKU',provider_key:'',sku:'ONLY_STORE_A'}],detail_limit:50,detail_truncated:false}:await new Promise(()=>{}));
 vi.mocked(dashboardApi.tags).mockResolvedValue({rows:[],overlap_note:''} as any);
 const r=render(App);
 await waitFor(()=>expect(r.container.textContent).toContain('ONLY_STORE_A'));
 await choose(r.getByTestId('store-selector'),B);
 await waitFor(()=>expect(vi.mocked(dashboardApi.catalogHealth).mock.calls.some(c=>c[0]===B)).toBe(true));
 expect(r.getByTestId('store-selector').dataset.value).toBe(B);
 expect(r.container.textContent).not.toContain('ONLY_STORE_A');
});
it('R1 regression: new Store B Retail totals appear beside old Store A online totals',async()=>{
 window.history.replaceState({},'',`/dashboard/sales?store_id=${A}`);
 for(const k of Object.keys(dashboardApi)) vi.spyOn(dashboardApi,k as any).mockImplementation(async()=>({}) as any);
 vi.mocked(dashboardApi.me).mockResolvedValue(ME);
 vi.mocked(dashboardApi.stores).mockResolvedValue({stores:[A,B].map(id=>({store_id:id,display_name:id===A?'Store A':'Store B',timezone:'Africa/Cairo',status:'active',device_count:1,created_at:'',updated_at:''}))});
 vi.mocked(dashboardApi.overview).mockImplementation(async(_p,s)=>({period:{start_local:'2026-10-03',end_local_exclusive:'2026-10-04'},summary:{transaction_count:0,return_transaction_count:0,currency_totals:[{currency:'EGP',net_sales_minor:s===A?'1100':'2200'}]},normalized:{},averages:{},fx:{}} as any));
 vi.mocked(dashboardApi.daily).mockResolvedValue({days:[],display_currency:'EGP',normalized:false} as any);
 for(const k of ['products','categories','branches'] as const) vi.mocked(dashboardApi[k]).mockResolvedValue({rows:[]} as any);
 vi.mocked(dashboardApi.syncHealth).mockResolvedValue({freshness:{},pending_count:0,retry_count:0,return_pending_count:0,return_retry_count:0} as any);
 vi.mocked(dashboardApi.activityPage).mockResolvedValue({items:[],offset:0,has_more:false});vi.mocked(dashboardApi.syncSummary).mockResolvedValue({categories:[],total_synced:0});vi.mocked(dashboardApi.latestSales).mockResolvedValue({sales:[]});
 vi.mocked(dashboardApi.orders).mockResolvedValue({orders:[],next_cursor:null,status_counts:[],webhook_inbox:{}} as any);
 vi.mocked(dashboardApi.orderSummary).mockImplementation(async(_p,s)=>s===A?{generated_at:'',store_id:null,provider_key:'',active_statuses:[],currency_totals:[{currency:'EGP',orders:1,value_minor:'3300',active_orders:1,active_value_minor:'3300'}],status_counts:[],provider_totals:[{provider_key:'ONLY_STORE_A',currency:'EGP',orders:1,value_minor:'3300',active_orders:1,active_value_minor:'3300'}]}:await new Promise(()=>{}));
 vi.mocked(dashboardApi.catalogHealth).mockResolvedValue({counts:[],detail:[]} as any);
 vi.mocked(dashboardApi.tags).mockResolvedValue({rows:[],overlap_note:''} as any);
 const r=render(App);
 await waitFor(()=>expect(r.container.textContent).toContain('ONLY_STORE_A'));
 await choose(r.getByTestId('store-selector'),B);
 await waitFor(()=>expect(vi.mocked(dashboardApi.orderSummary).mock.calls.some(c=>c[1]===B)).toBe(true));
 expect(r.getByTestId('store-selector').dataset.value).toBe(B);
 expect(r.container.textContent).not.toContain('ONLY_STORE_A');
 expect(r.queryByLabelText('Finalized Retail Sales')).toBeNull();
});

// Deliberately ignore AbortSignal in these deferred doubles: epoch fencing must
// still reject an old successful response after a newer scope has completed.
function deferred<T>() {let resolve!:(v:T)=>void;let reject!:(e:Error)=>void;const promise=new Promise<T>((r,j)=>{resolve=r;reject=j});return {promise,resolve,reject};}
function setupScopes(){
 window.history.replaceState({},'',`/dashboard/sales?store_id=${A}`);
 for(const k of Object.keys(dashboardApi)) vi.spyOn(dashboardApi,k as any).mockImplementation(async()=>({}) as any);
 vi.mocked(dashboardApi.me).mockResolvedValue(ME);
 vi.mocked(dashboardApi.stores).mockResolvedValue({stores:[A,B].map(id=>({store_id:id,display_name:id===A?'Store A':'Store B',timezone:'Africa/Cairo',status:'active',device_count:1,created_at:'',updated_at:''}))});
 vi.mocked(dashboardApi.daily).mockResolvedValue({days:[],display_currency:'EGP',normalized:false} as any);
 for(const k of ['products','categories','branches'] as const)vi.mocked(dashboardApi[k]).mockResolvedValue({rows:[]} as any);
 vi.mocked(dashboardApi.syncHealth).mockResolvedValue({freshness:{},pending_count:0,retry_count:0,return_pending_count:0,return_retry_count:0} as any);
 vi.mocked(dashboardApi.activityPage).mockResolvedValue({items:[],offset:0,has_more:false});vi.mocked(dashboardApi.syncSummary).mockResolvedValue({categories:[],total_synced:0});vi.mocked(dashboardApi.latestSales).mockResolvedValue({sales:[]});
 vi.mocked(dashboardApi.orders).mockResolvedValue({orders:[],next_cursor:null,status_counts:[],webhook_inbox:{}} as any);
 vi.mocked(dashboardApi.tags).mockResolvedValue({rows:[],overlap_note:''} as any);
 vi.mocked(dashboardApi.catalogHealth).mockResolvedValue({counts:[],detail:[]} as any);
 const retailCalls:Array<{scope:string,pending:ReturnType<typeof deferred<any>>}>=[];
 const onlineCalls:Array<{scope:string,currency:string,pending:ReturnType<typeof deferred<any>>}>=[];
 vi.mocked(dashboardApi.overview).mockImplementation((_p,scope)=>{const pending=deferred<any>();retailCalls.push({scope,pending});return pending.promise});
 vi.mocked(dashboardApi.orderSummary).mockImplementation((_p,scope,_provider,currency)=>{const pending=deferred<any>();onlineCalls.push({scope,currency,pending});return pending.promise});
 const retail=(n:string)=>({period:{start_local:'2026-10-03',end_local_exclusive:'2026-10-04'},summary:{transaction_count:1,units_sold:1,return_transaction_count:0,units_returned:0,currency_totals:[{currency:'EGP',subtotal_minor:n,discount_minor:'0',tax_minor:'0',sales_total_minor:n,line_cost_minor:'0',refund_total_minor:'0',net_sales_minor:n,returned_units:0,returned_cost_minor:'0',net_cost_minor:'0'}]},normalized:{normalized_total_minor:n,normalized_refund_minor:'0',normalized_net_minor:n,transactions:1,units:1,return_transactions:0,units_returned:0,usd_sale_count:0},averages:{all:{transactions:1,units:1,average_minor:n},egp:{transactions:1,units:1,average_minor:n},usd:{transactions:0,units:0,average_minor:'0'}},fx:{has_usd:false,latest_rate:null,multiple_rates_used:false}});
 const online=(name:string)=>({currency_totals:[{currency:'EGP',orders:1,value_minor:'4400',active_orders:1,active_value_minor:'4400'}],status_counts:[],provider_totals:[{provider_key:name,currency:'EGP',orders:1,value_minor:'4400',active_orders:1,active_value_minor:'4400'}]});
 return {retailCalls,onlineCalls,retail,online};
}
for(const first of ['retail','online'] as const)it(`coherent comparison waits for both current responses (${first} first), errors and retry`,async()=>{
 const h=setupScopes(),r=render(App);await waitFor(()=>expect(h.onlineCalls.length).toBe(1));h.retailCalls[0].pending.resolve(h.retail('1100'));h.onlineCalls[0].pending.resolve(h.online('A_READY'));await waitFor(()=>expect(r.container.textContent).toContain('A_READY'));
 await choose(r.getByTestId('store-selector'),B);await waitFor(()=>expect(h.onlineCalls.length).toBe(2));expect(r.container.textContent).not.toContain('A_READY');
 if(first==='retail')h.retailCalls[1].pending.resolve(h.retail('2200'));else h.onlineCalls[1].pending.resolve(h.online('B_READY'));
 await waitFor(()=>expect(r.queryByLabelText('Finalized Retail Sales')).toBeNull());
 if(first==='retail')h.onlineCalls[1].pending.resolve(h.online('B_READY'));else h.retailCalls[1].pending.resolve(h.retail('2200'));
 await waitFor(()=>expect(r.container.textContent).toContain('B_READY'));expect(r.getByLabelText('Finalized Retail Sales').textContent).toContain('22');
 await choose(r.getByTestId('store-selector'),A);await waitFor(()=>expect(h.onlineCalls.length).toBe(3));h.retailCalls[2].pending.resolve(h.retail('3300'));h.onlineCalls[2].pending.reject(new ApiError(503,'UNAVAILABLE','bounded fixture failure'));await waitFor(()=>expect(r.queryByLabelText('Finalized Retail Sales')).toBeNull());expect(r.container.textContent).not.toContain('B_READY');
 await waitFor(()=>expect(r.container.textContent).toContain('Reporting is temporarily unavailable'));const retries=r.getAllByRole('button').filter(b=>b.textContent?.includes('Retry'));expect(retries.length).toBeGreaterThan(0);await fireEvent.click(retries[0]);await waitFor(()=>expect(h.onlineCalls.length).toBe(4));h.retailCalls[3].pending.resolve(h.retail('3300'));h.onlineCalls[3].pending.resolve(h.online('A_RETRY'));await waitFor(()=>expect(r.container.textContent).toContain('A_RETRY'));
});
it('late A, rapid A→B→A, combined period/currency and history scopes remain fenced',async()=>{
 const h=setupScopes(),r=render(App);await waitFor(()=>expect(h.onlineCalls.length).toBe(1));
 await choose(r.getByTestId('store-selector'),B);await waitFor(()=>expect(h.onlineCalls.length).toBe(2));h.retailCalls[1].pending.resolve(h.retail('2200'));h.onlineCalls[1].pending.resolve(h.online('B_CURRENT'));await waitFor(()=>expect(r.container.textContent).toContain('B_CURRENT'));
 h.retailCalls[0].pending.resolve(h.retail('1100'));h.onlineCalls[0].pending.resolve(h.online('A_STALE'));await Promise.resolve();expect(r.container.textContent).not.toContain('A_STALE');expect(r.container.textContent).toContain('B_CURRENT');
 await choose(r.getByTestId('store-selector'),A);await waitFor(()=>expect(h.onlineCalls.length).toBe(3));
 await choose(r.getByTestId('store-selector'),B);await waitFor(()=>expect(h.onlineCalls.length).toBe(4));
 await choose(r.getByTestId('store-selector'),A);await waitFor(()=>expect(h.onlineCalls.length).toBe(5));h.retailCalls[4].pending.resolve(h.retail('5500'));h.onlineCalls[4].pending.resolve(h.online('A_NEW'));await waitFor(()=>expect(r.container.textContent).toContain('A_NEW'));
 for(const i of [2,3]){h.retailCalls[i].pending.resolve(h.retail('9900'));h.onlineCalls[i].pending.resolve(h.online('SUPERSEDED'))}await Promise.resolve();expect(r.container.textContent).not.toContain('SUPERSEDED');
 window.history.pushState({},'',`/dashboard/sales?store_id=${B}&period=yesterday&currency=USD`);window.dispatchEvent(new PopStateEvent('popstate'));await waitFor(()=>expect(h.onlineCalls.length).toBe(6));expect(r.container.textContent).not.toContain('A_NEW');expect(h.onlineCalls[5].scope).toBe(B);expect(h.onlineCalls[5].currency).toBe('USD');expect(vi.mocked(dashboardApi.orderSummary).mock.calls[5][0].period).toBe('yesterday');h.retailCalls[5].pending.resolve(h.retail('6600'));h.onlineCalls[5].pending.resolve(h.online('B_HISTORY'));await waitFor(()=>expect(r.container.textContent).toContain('B_HISTORY'));
});

// Deferred order reads ignore cancellation to test ownership, not fetch timing.
function orderRow(id:string){return {provider_key:'website',external_order_id:id,order_number:`ORDER_${id}`,provider_status:'processing',canonical_status:'processing',currency:'EGP',total_minor:'100',created_at:'2026-10-03T10:00:00Z',modified_at:'2026-10-03T10:00:00Z',customer_name:'Fixture',mapping_complete:true,unmapped_line_count:0,provider_deleted:false,revision:1};}
function orderPage(ids:string[],cursor:string|null){return {orders:ids.map(orderRow),next_cursor:cursor,status_counts:[],webhook_inbox:{pending:0,retry:0,blocked:0,oldest_pending_at:null}};}
function orderDetail(id:string){return {summary:orderRow(id),discount_minor:'0',shipping_minor:'0',cart_tax_minor:'0',total_tax_minor:'0',prices_include_tax:false,paid_at:null,completed_at:null,payment_method:'cash',payment_method_title:'Cash',customer_first_name:`DETAIL_${id}`,customer_last_name:'',customer_email:'',customer_phone:'',lines:[],addresses:[],status_history:[]};}
for(const first of ['more','detail'] as const)it(`independent order reads finish together (${first} starts first)`,async()=>{
 setupScopes();window.history.replaceState({},'',`/dashboard/orders?store_id=${A}`);
 const more=deferred<any>(),detail=deferred<any>();
 vi.mocked(dashboardApi.orders).mockImplementation(async(_s,_p,c)=>c?more.promise:orderPage(['a'],'cursor-a') as any);
 vi.mocked(dashboardApi.orderDetail).mockReturnValue(detail.promise);
 const r=render(App);await waitFor(()=>expect(r.queryByText('ORDER_a')).not.toBeNull());
 const view=async()=>fireEvent.click(r.getByRole('button',{name:/View/}));
 const load=async()=>fireEvent.click(r.getByRole('button',{name:/Load more/}));
 if(first==='more'){await load();await view()}else{await view();await load()}
 more.resolve(orderPage(['a','b'],'cursor-b'));detail.resolve(orderDetail('a'));
 await waitFor(()=>expect(r.queryByText('ORDER_b')).not.toBeNull());
 await waitFor(()=>expect(r.queryByText('DETAIL_a')).not.toBeNull());
 expect((r.getByRole('button',{name:/Load more/}) as HTMLButtonElement).disabled).toBe(false);
 expect(r.getAllByText('ORDER_b')).toHaveLength(1);
});
it('replacing an order list rejects old continuation but preserves a pending detail',async()=>{
 setupScopes();window.history.replaceState({},'',`/dashboard/orders?store_id=${A}`);
 const more=deferred<any>(),detail=deferred<any>();let lists=0;
 vi.mocked(dashboardApi.orders).mockImplementation(async(_s,_p,c)=>c?more.promise:orderPage([++lists===1?'a':'filtered'],lists===1?'cursor-a':null) as any);
 vi.mocked(dashboardApi.orderDetail).mockReturnValue(detail.promise);
 const r=render(App);await waitFor(()=>expect(r.queryByText('ORDER_a')).not.toBeNull());
 await fireEvent.click(r.getByRole('button',{name:/Load more/}));
 await fireEvent.click(r.getByRole('button',{name:/View/}));
 await choose(r.getByTestId('orders-status'),'completed');
 await waitFor(()=>expect(r.queryByText('ORDER_filtered')).not.toBeNull());
 more.resolve(orderPage(['old-continuation'],null));detail.resolve(orderDetail('a'));
 await waitFor(()=>expect(r.queryByText('DETAIL_a')).not.toBeNull());
 expect(r.queryByText('ORDER_old-continuation')).toBeNull();
});

it('order filters do not discard pending analytics siblings',async()=>{
 const h=setupScopes();window.history.replaceState({},'',`/dashboard/?store_id=${A}`);
 vi.mocked(dashboardApi.orders).mockResolvedValue(orderPage(['a'],null) as any);
 const r=render(App);await waitFor(()=>expect(r.queryByText('ORDER_a')).not.toBeNull());
 await choose(r.getByTestId('orders-status'),'completed');
 h.retailCalls[0].pending.resolve(h.retail('4242'));
 await waitFor(()=>expect(r.container.textContent).toContain('42.42'));
 expect(h.retailCalls).toHaveLength(1);
});
it('late detail A cannot replace newer detail B',async()=>{
 setupScopes();window.history.replaceState({},'',`/dashboard/orders?store_id=${A}`);
 const first=deferred<any>(),second=deferred<any>();
 vi.mocked(dashboardApi.orders).mockResolvedValue(orderPage(['a','b'],null) as any);
 vi.mocked(dashboardApi.orderDetail).mockImplementation(async(_provider,id)=>id==='a'?first.promise:second.promise);
 const r=render(App);await waitFor(()=>expect(r.queryByText('ORDER_b')).not.toBeNull());
 const views=r.getAllByRole('button',{name:/View/});await fireEvent.click(views[0]);await fireEvent.click(views[1]);
 second.resolve(orderDetail('b'));await waitFor(()=>expect(r.queryByText('DETAIL_b')).not.toBeNull());
 first.resolve(orderDetail('a'));await Promise.resolve();await Promise.resolve();
 expect(r.queryByText('DETAIL_a')).toBeNull();expect(r.queryByText('DETAIL_b')).not.toBeNull();
});
it('a Store transition fences every old order sibling including late authorization errors',async()=>{
 setupScopes();window.history.replaceState({},'',`/dashboard/orders?store_id=${A}`);
 const more=deferred<any>(),detail=deferred<any>();
 vi.mocked(dashboardApi.orders).mockImplementation(async(_status,_provider,cursor,store)=>cursor?more.promise:orderPage([store===A?'a':'b'],store===A?'cursor-a':null) as any);
 vi.mocked(dashboardApi.orderDetail).mockReturnValue(detail.promise);
 const r=render(App);await waitFor(()=>expect(r.queryByText('ORDER_a')).not.toBeNull());
 await fireEvent.click(r.getByRole('button',{name:/Load more/}));await fireEvent.click(r.getByRole('button',{name:/View/}));
 await choose(r.getByTestId('store-selector'),B);await waitFor(()=>expect(r.queryByText('ORDER_b')).not.toBeNull());
 more.reject(new ApiError(401,'UNAUTHORIZED','old session'));detail.resolve(orderDetail('a'));
 await Promise.resolve();await Promise.resolve();
 expect(r.queryByText('DETAIL_a')).toBeNull();expect(r.queryByText('ORDER_b')).not.toBeNull();
});
it('does not start new Store reads while logout is pending',async()=>{
 setupScopes();window.history.replaceState({},'',`/dashboard/orders?store_id=${A}`);
 const logout=deferred<any>(),late=deferred<any>();void late.promise.catch(()=>{});
 vi.mocked(dashboardApi.logout).mockReturnValue(logout.promise);
 vi.mocked(dashboardApi.orders).mockImplementation(async(_s,_p,_c,store)=>store===A?orderPage(['a'],null):late.promise as any);
 const r=render(App);await waitFor(()=>expect(r.queryByText('ORDER_a')).not.toBeNull());
 await fireEvent.click(r.getByRole('button',{name:/Account menu/}));await fireEvent.click(r.getByRole('menuitem',{name:/Log out/}));
 await choose(r.getByTestId('store-selector'),B);
 expect(vi.mocked(dashboardApi.orders).mock.calls.map(c=>c[3])).toEqual([A]);
 logout.resolve(undefined);
 await waitFor(()=>expect(r.queryByTestId('store-selector')).toBeNull());
 late.reject(new ApiError(401,'UNAUTHORIZED','obsolete request'));
});
