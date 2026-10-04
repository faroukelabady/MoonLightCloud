import {render,fireEvent,waitFor,cleanup} from '@testing-library/svelte';
import {it,expect,vi,afterEach} from 'vitest';
import TagCard from './TagCard.svelte';
import App from '../App.svelte';
import {dashboardApi,ApiError} from '../lib/api.js';
vi.mock('../lib/chartAction.js',()=>({chart:()=>({destroy(){}})}));
afterEach(()=>{cleanup();vi.restoreAllMocks();});
const A='aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa',B='bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb';
const bucket={currency:'EGP',line_sales_minor:'100',line_refund_minor:'0',net_minor:'100'};
function tags(name:string){return {generated_at:'',timezone:'Africa/Cairo',store_id:null,overlap_note:'Tag totals overlap and must not be summed',rows:[{tag_id:A,tag_slug:'gold',name_ar:name,name_en:name,units:1,units_returned:0,currencies:[bucket]}]};}
it('R1 regression: valid renamed historical Tag rows collide in the actual component',()=>{
 const d=tags('Gold');d.rows.push({...d.rows[0],name_ar:'Golden',name_en:'Golden'});
 expect(()=>render(TagCard,{props:{data:d,status:'loaded',errStatus:null,onretry(){},currency:'EGP'}})).not.toThrow();
});
it('R1 regression: Store B selected while loaded Store A Tags remain visible',async()=>{
 window.history.replaceState({},'',`/dashboard/categories?store_id=${A}`);
 for(const k of Object.keys(dashboardApi)) vi.spyOn(dashboardApi,k as any).mockImplementation(async()=>({}) as any);
 vi.mocked(dashboardApi.me).mockResolvedValue({authenticated:true,username:'review'});
 vi.mocked(dashboardApi.stores).mockResolvedValue({stores:[A,B].map(id=>({store_id:id,display_name:id===A?'Store A':'Store B',timezone:'Africa/Cairo',status:'active',device_count:1,created_at:'',updated_at:''}))});
 vi.mocked(dashboardApi.overview).mockResolvedValue({period:{start_local:'2026-10-03',end_local_exclusive:'2026-10-04'},summary:{transaction_count:0,return_transaction_count:0,currency_totals:[]},normalized:{},averages:{},fx:{}} as any);
 vi.mocked(dashboardApi.daily).mockResolvedValue({days:[],display_currency:'EGP',normalized:false} as any);
 for(const k of ['products','categories','branches'] as const) vi.mocked(dashboardApi[k]).mockResolvedValue({rows:[]} as any);
 vi.mocked(dashboardApi.syncHealth).mockResolvedValue({freshness:{},pending_count:0,retry_count:0,return_pending_count:0,return_retry_count:0} as any);
 vi.mocked(dashboardApi.activity).mockResolvedValue({items:[]});vi.mocked(dashboardApi.latestSales).mockResolvedValue({sales:[]});
 vi.mocked(dashboardApi.orders).mockResolvedValue({orders:[],next_cursor:null,status_counts:[],webhook_inbox:{}} as any);
 vi.mocked(dashboardApi.orderSummary).mockResolvedValue({currency_totals:[],status_counts:[],provider_totals:[]} as any);
 vi.mocked(dashboardApi.catalogHealth).mockResolvedValue({counts:[],detail:[]} as any);
 vi.mocked(dashboardApi.tags).mockImplementation(async(_p,_c,s)=>s===A?tags('ONLY_STORE_A'):await new Promise(()=>{}));
 const r=render(App);
 await waitFor(()=>expect(r.container.textContent).toContain('ONLY_STORE_A'));
 await fireEvent.change(r.getByTestId('store-selector'),{target:{value:B}});
 await waitFor(()=>expect(vi.mocked(dashboardApi.tags).mock.calls.some(c=>c[2]===B)).toBe(true));
 expect((r.getByTestId('store-selector') as HTMLSelectElement).value).toBe(B);
 expect(r.container.textContent).not.toContain('ONLY_STORE_A');
});
it('R1 regression: Store B selected while loaded Store A health remains visible',async()=>{
 window.history.replaceState({},'',`/dashboard/sync?store_id=${A}`);
 for(const k of Object.keys(dashboardApi)) vi.spyOn(dashboardApi,k as any).mockImplementation(async()=>({}) as any);
 vi.mocked(dashboardApi.me).mockResolvedValue({authenticated:true,username:'review'});
 vi.mocked(dashboardApi.stores).mockResolvedValue({stores:[A,B].map(id=>({store_id:id,display_name:id===A?'Store A':'Store B',timezone:'Africa/Cairo',status:'active',device_count:1,created_at:'',updated_at:''}))});
 vi.mocked(dashboardApi.overview).mockResolvedValue({period:{start_local:'2026-10-03',end_local_exclusive:'2026-10-04'},summary:{transaction_count:0,return_transaction_count:0,currency_totals:[]},normalized:{},averages:{},fx:{}} as any);
 vi.mocked(dashboardApi.daily).mockResolvedValue({days:[],display_currency:'EGP',normalized:false} as any);
 for(const k of ['products','categories','branches'] as const) vi.mocked(dashboardApi[k]).mockResolvedValue({rows:[]} as any);
 vi.mocked(dashboardApi.syncHealth).mockResolvedValue({freshness:{},pending_count:0,retry_count:0,return_pending_count:0,return_retry_count:0} as any);
 vi.mocked(dashboardApi.activity).mockResolvedValue({items:[]});vi.mocked(dashboardApi.latestSales).mockResolvedValue({sales:[]});
 vi.mocked(dashboardApi.orders).mockResolvedValue({orders:[],next_cursor:null,status_counts:[],webhook_inbox:{}} as any);
 vi.mocked(dashboardApi.orderSummary).mockResolvedValue({currency_totals:[],status_counts:[],provider_totals:[]} as any);
 vi.mocked(dashboardApi.catalogHealth).mockImplementation(async(s)=>s===A?{generated_at:'',store_id:null,provider_key:'',reason_codes:[],counts:[{reason_code:'CATALOG_MISSING_SKU',products:1}],detail:[{reason_code:'CATALOG_MISSING_SKU',provider_key:'',sku:'ONLY_STORE_A'}],detail_limit:50,detail_truncated:false}:await new Promise(()=>{}));
 vi.mocked(dashboardApi.tags).mockResolvedValue({rows:[],overlap_note:''} as any);
 const r=render(App);
 await waitFor(()=>expect(r.container.textContent).toContain('ONLY_STORE_A'));
 await fireEvent.change(r.getByTestId('store-selector'),{target:{value:B}});
 await waitFor(()=>expect(vi.mocked(dashboardApi.catalogHealth).mock.calls.some(c=>c[0]===B)).toBe(true));
 expect((r.getByTestId('store-selector') as HTMLSelectElement).value).toBe(B);
 expect(r.container.textContent).not.toContain('ONLY_STORE_A');
});
it('R1 regression: new Store B Retail totals appear beside old Store A online totals',async()=>{
 window.history.replaceState({},'',`/dashboard/sales?store_id=${A}`);
 for(const k of Object.keys(dashboardApi)) vi.spyOn(dashboardApi,k as any).mockImplementation(async()=>({}) as any);
 vi.mocked(dashboardApi.me).mockResolvedValue({authenticated:true,username:'review'});
 vi.mocked(dashboardApi.stores).mockResolvedValue({stores:[A,B].map(id=>({store_id:id,display_name:id===A?'Store A':'Store B',timezone:'Africa/Cairo',status:'active',device_count:1,created_at:'',updated_at:''}))});
 vi.mocked(dashboardApi.overview).mockImplementation(async(_p,s)=>({period:{start_local:'2026-10-03',end_local_exclusive:'2026-10-04'},summary:{transaction_count:0,return_transaction_count:0,currency_totals:[{currency:'EGP',net_sales_minor:s===A?'1100':'2200'}]},normalized:{},averages:{},fx:{}} as any));
 vi.mocked(dashboardApi.daily).mockResolvedValue({days:[],display_currency:'EGP',normalized:false} as any);
 for(const k of ['products','categories','branches'] as const) vi.mocked(dashboardApi[k]).mockResolvedValue({rows:[]} as any);
 vi.mocked(dashboardApi.syncHealth).mockResolvedValue({freshness:{},pending_count:0,retry_count:0,return_pending_count:0,return_retry_count:0} as any);
 vi.mocked(dashboardApi.activity).mockResolvedValue({items:[]});vi.mocked(dashboardApi.latestSales).mockResolvedValue({sales:[]});
 vi.mocked(dashboardApi.orders).mockResolvedValue({orders:[],next_cursor:null,status_counts:[],webhook_inbox:{}} as any);
 vi.mocked(dashboardApi.orderSummary).mockImplementation(async(_p,s)=>s===A?{generated_at:'',store_id:null,provider_key:'',active_statuses:[],currency_totals:[{currency:'EGP',orders:1,value_minor:'3300',active_orders:1,active_value_minor:'3300'}],status_counts:[],provider_totals:[{provider_key:'ONLY_STORE_A',currency:'EGP',orders:1,value_minor:'3300',active_orders:1,active_value_minor:'3300'}]}:await new Promise(()=>{}));
 vi.mocked(dashboardApi.catalogHealth).mockResolvedValue({counts:[],detail:[]} as any);
 vi.mocked(dashboardApi.tags).mockResolvedValue({rows:[],overlap_note:''} as any);
 const r=render(App);
 await waitFor(()=>expect(r.container.textContent).toContain('ONLY_STORE_A'));
 await fireEvent.change(r.getByTestId('store-selector'),{target:{value:B}});
 await waitFor(()=>expect(vi.mocked(dashboardApi.orderSummary).mock.calls.some(c=>c[1]===B)).toBe(true));
 expect((r.getByTestId('store-selector') as HTMLSelectElement).value).toBe(B);
 expect(r.container.textContent).not.toContain('ONLY_STORE_A');
 expect(r.queryByLabelText('Finalized Retail Sales / المبيعات النهائية')).toBeNull();
});

// Deliberately ignore AbortSignal in these deferred doubles: epoch fencing must
// still reject an old successful response after a newer scope has completed.
function deferred<T>() {let resolve!:(v:T)=>void;let reject!:(e:Error)=>void;const promise=new Promise<T>((r,j)=>{resolve=r;reject=j});return {promise,resolve,reject};}
function setupScopes(){
 window.history.replaceState({},'',`/dashboard/sales?store_id=${A}`);
 for(const k of Object.keys(dashboardApi)) vi.spyOn(dashboardApi,k as any).mockImplementation(async()=>({}) as any);
 vi.mocked(dashboardApi.me).mockResolvedValue({authenticated:true,username:'review'});
 vi.mocked(dashboardApi.stores).mockResolvedValue({stores:[A,B].map(id=>({store_id:id,display_name:id===A?'Store A':'Store B',timezone:'Africa/Cairo',status:'active',device_count:1,created_at:'',updated_at:''}))});
 vi.mocked(dashboardApi.daily).mockResolvedValue({days:[],display_currency:'EGP',normalized:false} as any);
 for(const k of ['products','categories','branches'] as const)vi.mocked(dashboardApi[k]).mockResolvedValue({rows:[]} as any);
 vi.mocked(dashboardApi.syncHealth).mockResolvedValue({freshness:{},pending_count:0,retry_count:0,return_pending_count:0,return_retry_count:0} as any);
 vi.mocked(dashboardApi.activity).mockResolvedValue({items:[]});vi.mocked(dashboardApi.latestSales).mockResolvedValue({sales:[]});
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
 await fireEvent.change(r.getByTestId('store-selector'),{target:{value:B}});await waitFor(()=>expect(h.onlineCalls.length).toBe(2));expect(r.container.textContent).not.toContain('A_READY');
 if(first==='retail')h.retailCalls[1].pending.resolve(h.retail('2200'));else h.onlineCalls[1].pending.resolve(h.online('B_READY'));
 await waitFor(()=>expect(r.queryByLabelText('Finalized Retail Sales / المبيعات النهائية')).toBeNull());
 if(first==='retail')h.onlineCalls[1].pending.resolve(h.online('B_READY'));else h.retailCalls[1].pending.resolve(h.retail('2200'));
 await waitFor(()=>expect(r.container.textContent).toContain('B_READY'));expect(r.getByLabelText('Finalized Retail Sales / المبيعات النهائية').textContent).toContain('22');
 await fireEvent.change(r.getByTestId('store-selector'),{target:{value:A}});await waitFor(()=>expect(h.onlineCalls.length).toBe(3));h.retailCalls[2].pending.resolve(h.retail('3300'));h.onlineCalls[2].pending.reject(new ApiError(503,'UNAVAILABLE','bounded fixture failure'));await waitFor(()=>expect(r.queryByLabelText('Finalized Retail Sales / المبيعات النهائية')).toBeNull());expect(r.container.textContent).not.toContain('B_READY');
 await waitFor(()=>expect(r.container.textContent).toContain('Reporting is temporarily unavailable'));const retries=r.getAllByRole('button').filter(b=>b.textContent?.includes('Retry'));expect(retries.length).toBeGreaterThan(0);await fireEvent.click(retries[0]);await waitFor(()=>expect(h.onlineCalls.length).toBe(4));h.retailCalls[3].pending.resolve(h.retail('3300'));h.onlineCalls[3].pending.resolve(h.online('A_RETRY'));await waitFor(()=>expect(r.container.textContent).toContain('A_RETRY'));
});
it('late A, rapid A→B→A, combined period/currency and history scopes remain fenced',async()=>{
 const h=setupScopes(),r=render(App);await waitFor(()=>expect(h.onlineCalls.length).toBe(1));
 await fireEvent.change(r.getByTestId('store-selector'),{target:{value:B}});await waitFor(()=>expect(h.onlineCalls.length).toBe(2));h.retailCalls[1].pending.resolve(h.retail('2200'));h.onlineCalls[1].pending.resolve(h.online('B_CURRENT'));await waitFor(()=>expect(r.container.textContent).toContain('B_CURRENT'));
 h.retailCalls[0].pending.resolve(h.retail('1100'));h.onlineCalls[0].pending.resolve(h.online('A_STALE'));await Promise.resolve();expect(r.container.textContent).not.toContain('A_STALE');expect(r.container.textContent).toContain('B_CURRENT');
 await fireEvent.change(r.getByTestId('store-selector'),{target:{value:A}});await waitFor(()=>expect(h.onlineCalls.length).toBe(3));
 await fireEvent.change(r.getByTestId('store-selector'),{target:{value:B}});await waitFor(()=>expect(h.onlineCalls.length).toBe(4));
 await fireEvent.change(r.getByTestId('store-selector'),{target:{value:A}});await waitFor(()=>expect(h.onlineCalls.length).toBe(5));h.retailCalls[4].pending.resolve(h.retail('5500'));h.onlineCalls[4].pending.resolve(h.online('A_NEW'));await waitFor(()=>expect(r.container.textContent).toContain('A_NEW'));
 for(const i of [2,3]){h.retailCalls[i].pending.resolve(h.retail('9900'));h.onlineCalls[i].pending.resolve(h.online('SUPERSEDED'))}await Promise.resolve();expect(r.container.textContent).not.toContain('SUPERSEDED');
 window.history.pushState({},'',`/dashboard/sales?store_id=${B}&period=yesterday&currency=USD`);window.dispatchEvent(new PopStateEvent('popstate'));await waitFor(()=>expect(h.onlineCalls.length).toBe(6));expect(r.container.textContent).not.toContain('A_NEW');expect(h.onlineCalls[5].scope).toBe(B);expect(h.onlineCalls[5].currency).toBe('USD');expect(vi.mocked(dashboardApi.orderSummary).mock.calls[5][0].period).toBe('yesterday');h.retailCalls[5].pending.resolve(h.retail('6600'));h.onlineCalls[5].pending.resolve(h.online('B_HISTORY'));await waitFor(()=>expect(r.container.textContent).toContain('B_HISTORY'));
});
