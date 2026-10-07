import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, render, screen, fireEvent } from '@testing-library/svelte';
import CatalogAdminPage from './CatalogAdminPage.svelte';
import { dashboardApi } from '../lib/api.js';
vi.mock('../lib/api.js', async (orig) => {
 const m = await orig() as Record<string, unknown>;
 return {...m,dashboardApi:{adminProducts:vi.fn(),adminCategories:vi.fn(),adminTags:vi.fn(),adminCommands:vi.fn(),adminProduct:vi.fn(),adminConfigurations:vi.fn(),adminProductTypes:vi.fn(),adminProductType:vi.fn(),adminCreateCommand:vi.fn(),adminCommand:vi.fn()}};
});
const api=dashboardApi as unknown as Record<string,ReturnType<typeof vi.fn>>;
afterEach(()=>{cleanup();vi.resetAllMocks()});
// Phase 17-R0 (ADR-0049): product rows carry variant_count/derived_stock
// (never sku/stock); the single variant's SKU is shown from the variant
// rows as "1 variant / SKU: X".
function productRow(name:string){
 return {product_id:'p-'+name,name_ar:'Product '+name,catalog_revision:1,variant_count:1,derived_stock:3};
}
function productDetail(name:string){
 return {...productRow(name),name_en:'',description_ar:'',description_en:'',width_cm:null,height_cm:null,top_category_id:'c-'+name,subcategory_ids:[],tag_ids:[],egp_price_minor:'0',usd_price_minor:null,cost_minor:'0',is_active:true,sell_online:false,sell_offline:true,sales_policy_revision:1,has_pending:false,
 variants:[{variant_id:'v-'+name,product_id:'p-'+name,sku:'SKU-'+name,is_active:true,deleted:false,price_egp_minor:'0',price_usd_minor:null,stock_quantity:3,position:0,combination_key:'x',variant_revision:1,catalog_revision:1,inventory_revision:1,has_pending:false,attributes:[]}]};
}
function fixtures(name:string){
 return {products:[productRow(name)],categories:[{category_id:'c-'+name,name_ar:'Category '+name,parent_ids:[]}],tags:[{tag_id:'t-'+name,name_ar:'Tag '+name}],commands:[]};
}
function defaults(){api.adminCategories.mockResolvedValue({categories:fixtures('ready').categories});api.adminTags.mockResolvedValue({tags:fixtures('ready').tags});api.adminCommands.mockResolvedValue({commands:[],next_cursor:''});api.adminProductTypes.mockResolvedValue({product_types:[{type_id:'t-papyrus',code:'papyrus',name_ar:'برديات',name_en:'Papyrus',is_active:true,position:0,type_revision:1,dimensions:['color'],capabilities:['frame_configuration']}]})}
it('loads all sibling resources without cancelling successful responses', async()=>{
 const f=fixtures('ready');defaults();api.adminProducts.mockResolvedValue({products:f.products,next_cursor:''});
 render(CatalogAdminPage,{store:'store-A'});
 expect(await screen.findByText('Product ready')).toBeTruthy();
 expect(screen.getByText('1 variant')).toBeTruthy();
 await fireEvent.click(screen.getByRole('tab',{name:'الفئات / Categories'}));expect(await screen.findByText('Category ready')).toBeTruthy();
 await fireEvent.click(screen.getByRole('tab',{name:'الوسوم / Tags'}));expect(await screen.findByText('Tag ready')).toBeTruthy();
 expect(screen.queryByText('جارٍ التحميل… / Loading…')).toBeNull();
});
it('discards late old-Store results while retaining the new Store', async()=>{
 defaults();let oldResolve!:(value:unknown)=>void;
 api.adminProducts.mockImplementation((store:string)=>store==='A'?new Promise(resolve=>{oldResolve=resolve}):Promise.resolve({products:fixtures('B').products,next_cursor:''}));
 const page=render(CatalogAdminPage,{store:'A'});await vi.waitFor(()=>expect(api.adminProducts).toHaveBeenCalledWith('A','',null));
 await page.rerender({store:'B'});expect(await screen.findByText('Product B')).toBeTruthy();
 oldResolve({products:fixtures('A').products,next_cursor:''});await Promise.resolve();await Promise.resolve();
 expect(screen.queryByText('Product A')).toBeNull();expect(screen.getByText('Product B')).toBeTruthy();
});
it('does not let a late Product detail replace a newer selection',async()=>{
 defaults();const f=fixtures('ready');api.adminProducts.mockResolvedValue({products:[...f.products,{product_id:'other',name_ar:'Other',catalog_revision:1,variant_count:1,derived_stock:0}],next_cursor:''});
 let oldResolve!:(value:unknown)=>void;
 api.adminProduct.mockImplementation((_store:string,id:string)=>id==='other'?Promise.resolve({...productDetail('other'),name_ar:'Other selected'}):new Promise(resolve=>{oldResolve=resolve}));api.adminConfigurations.mockResolvedValue({configurations:[]});
 render(CatalogAdminPage,{store:'A'});await fireEvent.click(await screen.findByRole('button',{name:'Product ready'}));
 await fireEvent.click(screen.getByRole('button',{name:'Other'}));expect(await screen.findByText('Other selected')).toBeTruthy();
 // Phase 17-R0: the detail shows the spec variant summary with the
 // single variant row's SKU.
 expect(await screen.findByText('1 variant / SKU: SKU-other')).toBeTruthy();
 oldResolve({...productDetail('ready'),name_ar:'Old selection'});await Promise.resolve();await Promise.resolve();expect(screen.queryByText('Old selection')).toBeNull();
});
it('shows product types with capabilities and queues type changes', async()=>{
 defaults();const f=fixtures('ready');api.adminProducts.mockResolvedValue({products:f.products,next_cursor:''});
 render(CatalogAdminPage,{store:'A'});
 await fireEvent.click(screen.getByRole('tab',{name:'الأنواع / Types'}));
 expect(await screen.findByText('برديات')).toBeTruthy();
 expect(screen.getByText('papyrus')).toBeTruthy();
 await fireEvent.click(screen.getByRole('button',{name:'برديات'}));
 await vi.waitFor(() => {
   const hits = screen.queryAllByText((_, el) => el?.textContent?.includes('frame_configuration') ?? false);
   expect(hits.length).toBeGreaterThan(0);
 });
});
it('distinguishes create intent from result identity', async()=>{
 defaults();const f=fixtures('ready');api.adminProducts.mockResolvedValue({products:f.products,next_cursor:''});
 const pending={id:'cmd-1',store_id:'A',type:'catalog.product-type.create.v1',version:1,entity_id:'',target_kind:'create',requested_key:'book',payload_hash:'0'.repeat(64),expected_revision:0,actor:'op',status:'PENDING',aggregate:'PENDING',converged:false,targets:[],created_at:'',updated_at:''};
 const done={...pending,result_entity_id:'type-9',aggregate:'CONVERGED',converged:true};
 api.adminCommands.mockResolvedValue({commands:[pending],next_cursor:''});
 api.adminCommand.mockResolvedValue(pending);
 render(CatalogAdminPage,{store:'A'});
 await fireEvent.click(screen.getByRole('tab',{name:'الأوامر / Commands'}));
 await fireEvent.click(await screen.findByRole('button',{name:'catalog.product-type.create.v1'}));
 // Waiting state names the requested code, never a placeholder UUID.
 await vi.waitFor(()=>expect(screen.queryAllByText((_,el)=>(el?.textContent?.includes('waiting for Retail') ?? false)).length).toBeGreaterThan(0));
 expect(screen.queryByText('cmd-1')).toBeNull();
 // After application the actual result identity appears.
 api.adminCommand.mockResolvedValue(done);
 await fireEvent.click(await screen.findByRole('button',{name:'catalog.product-type.create.v1'}));
 await vi.waitFor(()=>expect(screen.queryAllByText((_,el)=>(el?.textContent?.includes('type-9') ?? false)).length).toBeGreaterThan(0));
});
