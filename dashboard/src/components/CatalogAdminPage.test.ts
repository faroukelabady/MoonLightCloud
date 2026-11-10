import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { act, cleanup, render, screen, fireEvent, within } from '@testing-library/svelte';
import CatalogAdminPage from './CatalogAdminPage.svelte';
import { dashboardApi, ApiError } from '../lib/api.js';
import { setLang } from '../lib/i18n.svelte.js';
vi.mock('../lib/api.js', async (orig) => {
 const m = await orig() as Record<string, unknown>;
 return {...m,dashboardApi:{adminProducts:vi.fn(),adminCategories:vi.fn(),adminTags:vi.fn(),adminCommands:vi.fn(),adminProduct:vi.fn(),adminConfigurations:vi.fn(),adminProductTypes:vi.fn(),adminProductType:vi.fn(),adminCreateCommand:vi.fn(),adminCommand:vi.fn(),adminPermissions:vi.fn(),adminMediaUrl:vi.fn()}};
});
const api=dashboardApi as unknown as Record<string,ReturnType<typeof vi.fn>>;
// The editor shows one language; most tests read the English labels.
beforeEach(()=>setLang('en'));
afterEach(()=>{cleanup();vi.resetAllMocks()});
const allowAll={online_selling:true,products:true,catalog:true,devices:1};
// confirmCode answers the authenticator prompt that product, price and
// catalog changes open before anything is sent.
async function confirmCode(code='123456'){
 const input=await screen.findByLabelText('Authenticator code');
 await fireEvent.input(input,{target:{value:code}});
 await fireEvent.click(screen.getByRole('button',{name:'Confirm and send'}));
}
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
function defaults(){api.adminCategories.mockResolvedValue({categories:fixtures('ready').categories});api.adminTags.mockResolvedValue({tags:fixtures('ready').tags});api.adminCommands.mockResolvedValue({commands:[],next_cursor:''});api.adminProductTypes.mockResolvedValue({product_types:[{type_id:'t-papyrus',code:'papyrus',name_ar:'برديات',name_en:'Papyrus',is_active:true,position:0,type_revision:1,dimensions:['color'],capabilities:['frame_configuration']}]});api.adminPermissions.mockResolvedValue(allowAll)}
it('loads all sibling resources without cancelling successful responses', async()=>{
 const f=fixtures('ready');defaults();api.adminProducts.mockResolvedValue({products:f.products,next_cursor:''});
 render(CatalogAdminPage,{store:'store-A'});
 expect(await screen.findByText('Product ready')).toBeTruthy();
 expect(screen.getByText('1 variant')).toBeTruthy();
 await fireEvent.click(screen.getByRole('tab',{name:'Categories'}));expect(await screen.findByText('Category ready')).toBeTruthy();
 await fireEvent.click(screen.getByRole('tab',{name:'Tags'}));expect(await screen.findByText('Tag ready')).toBeTruthy();
 expect(screen.queryByText('Loading…')).toBeNull();
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
 await fireEvent.click(screen.getByRole('tab',{name:'Types'}));
 expect(await screen.findByText('Papyrus')).toBeTruthy();
 expect(screen.getByText('papyrus')).toBeTruthy();
 await fireEvent.click(screen.getByRole('button',{name:'Papyrus'}));
 await vi.waitFor(() => {
   const hits = screen.queryAllByText((_, el) => el?.textContent?.includes('frame_configuration') ?? false);
   expect(hits.length).toBeGreaterThan(0);
 });
});
it('distinguishes create intent from result identity', async()=>{
 defaults();const f=fixtures('ready');api.adminProducts.mockResolvedValue({products:f.products,next_cursor:''});
 const pending={id:'cmd-1',store_id:'A',type:'catalog.product-type.create.v1',version:1,entity_id:'',target_kind:'create',requested_key:'book',payload_hash:'0'.repeat(64),expected_revision:0,actor:'op',status:'pending',aggregate:'pending',converged:false,targets:[],created_at:'',updated_at:''};
 const done={...pending,result_entity_id:'type-9',aggregate:'converged',converged:true};
 api.adminCommands.mockResolvedValue({commands:[pending],next_cursor:''});
 api.adminCommand.mockResolvedValue(pending);
 render(CatalogAdminPage,{store:'A'});
 await fireEvent.click(screen.getByRole('tab',{name:'Commands'}));
 await fireEvent.click(await screen.findByRole('button',{name:'catalog.product-type.create.v1'}));
 // Waiting state names the requested code, never a placeholder UUID.
 await vi.waitFor(()=>expect(screen.queryAllByText((_,el)=>(el?.textContent?.includes('waiting for Retail') ?? false)).length).toBeGreaterThan(0));
 expect(screen.queryByText('cmd-1')).toBeNull();
 // After application the actual result identity appears.
 api.adminCommand.mockResolvedValue(done);
 await fireEvent.click(await screen.findByRole('button',{name:'catalog.product-type.create.v1'}));
 await vi.waitFor(()=>expect(screen.queryAllByText((_,el)=>(el?.textContent?.includes('type-9') ?? false)).length).toBeGreaterThan(0));
});
it('shows shared frame choices read-only (edited in Retail)', async()=>{
 defaults();api.adminProducts.mockResolvedValue({products:fixtures('ready').products,next_cursor:''});api.adminProduct.mockResolvedValue(productDetail('ready'));
 api.adminConfigurations.mockResolvedValue({configurations:[{id:'cfg-1',style_code:'classic',style_name_ar:'كلاسيكي',style_name_en:'Classic',color_code:'black',color_name_ar:'أسود',color_name_en:'Black',egp_delta_minor:'30000',usd_delta_minor:'600',enabled:true,position:0}]});
 render(CatalogAdminPage,{store:'A'});await fireEvent.click(await screen.findByRole('button',{name:'Product ready'}));
 const list=await screen.findByTestId('frame-configurations');
 expect(list.textContent).toContain('Classic / Black');expect(list.textContent).toContain('+300 EGP · +$6.00');
 expect(screen.queryByText(/Queue options change/)).toBeNull();expect(api.adminCreateCommand).not.toHaveBeenCalled();
});

it('submits classification replacement arrays and the catalog revision unchanged', async()=>{
 defaults();api.adminProducts.mockResolvedValue({products:fixtures('ready').products,next_cursor:''});
 api.adminProduct.mockResolvedValue({...productDetail('ready'),catalog_revision:17,sales_policy_revision:29});
 api.adminConfigurations.mockResolvedValue({configurations:[]});api.adminCreateCommand.mockResolvedValue({id:'command-123'});
 api.adminCategories.mockResolvedValue({categories:[...fixtures('ready').categories,{category_id:'sub-z',name_ar:'Sub Z',parent_ids:['c-ready']},{category_id:'sub-a',name_ar:'Sub A',parent_ids:['c-ready']}]});
 render(CatalogAdminPage,{store:'store-A'});await fireEvent.click(await screen.findByRole('button',{name:'Product ready'}));
 await screen.findByRole('button',{name:/Queue classification change/});
 await fireEvent.click(screen.getByLabelText('Sub Z'));await fireEvent.click(screen.getByLabelText('Sub A'));await fireEvent.click(screen.getByLabelText('Tag ready'));
 await fireEvent.click(screen.getByRole('button',{name:/Queue classification change/}));
 expect(api.adminCreateCommand).not.toHaveBeenCalled();
 await confirmCode();
 await vi.waitFor(()=>expect(api.adminCreateCommand).toHaveBeenCalledExactlyOnceWith({store_id:'store-A',type:'catalog.product.classification.update.v1',entity_id:'p-ready',expected_revision:17,payload:{product_id:'p-ready',top_category_id:'c-ready',subcategory_ids:['sub-z','sub-a'],tag_ids:['t-ready'],expected_catalog_revision:17}},'123456'));
});

it('submits product online intent with the separate sales policy revision', async()=>{
 defaults();api.adminProducts.mockResolvedValue({products:fixtures('ready').products,next_cursor:''});
 api.adminProduct.mockResolvedValue({...productDetail('ready'),catalog_revision:17,sales_policy_revision:29,sell_online:true});
 api.adminConfigurations.mockResolvedValue({configurations:[]});api.adminCreateCommand.mockResolvedValue({id:'command-123'});
 render(CatalogAdminPage,{store:'store-A'});await fireEvent.click(await screen.findByRole('button',{name:'Product ready'}));
 await fireEvent.click(await screen.findByRole('button',{name:/Disable online/}));
 await vi.waitFor(()=>expect(api.adminCreateCommand).toHaveBeenCalledExactlyOnceWith({store_id:'store-A',type:'catalog.product.online-policy.update.v1',entity_id:'p-ready',expected_revision:29,payload:{product_id:'p-ready',sell_online:false,expected_sales_policy_revision:29}}));
});

it('keeps category disable confirmation before catalog-revision command submission', async()=>{
 defaults();api.adminProducts.mockResolvedValue({products:[],next_cursor:''});
 api.adminCategories.mockResolvedValue({categories:[{category_id:'category-1',name_ar:'Category One',name_en:'',parent_ids:[],online_enabled:true,catalog_revision:31}]});
 api.adminCreateCommand.mockResolvedValue({id:'command-123'});
 const confirm=vi.spyOn(window,'confirm').mockReturnValue(false);
 try {
  render(CatalogAdminPage,{store:'store-A'});await fireEvent.click(screen.getByRole('tab',{name:'Categories'}));
  await fireEvent.click(await screen.findByRole('button',{name:'Category One'}));
  await fireEvent.click(screen.getByRole('button',{name:/Disable online/}));expect(api.adminCreateCommand).not.toHaveBeenCalled();
  confirm.mockReturnValue(true);await fireEvent.click(screen.getByRole('button',{name:/Disable online/}));
  await vi.waitFor(()=>expect(api.adminCreateCommand).toHaveBeenCalledExactlyOnceWith({store_id:'store-A',type:'catalog.category.online-policy.update.v1',entity_id:'category-1',expected_revision:31,payload:{category_id:'category-1',online_enabled:false,expected_catalog_revision:31}}));
 } finally { confirm.mockRestore(); }
});


it('shows prices in pounds and submits them as exact minor units', async()=>{
 defaults();api.adminProducts.mockResolvedValue({products:fixtures('ready').products,next_cursor:''});
 const detail={...productDetail('ready'),catalog_revision:17,sales_policy_revision:29,description_ar:null,description_en:'Description',width_cm:null,height_cm:7};
 api.adminProduct.mockResolvedValue(detail);api.adminConfigurations.mockResolvedValue({configurations:[]});api.adminCreateCommand.mockResolvedValue({id:'command-123'});
 render(CatalogAdminPage,{store:'store-A'});await fireEvent.click(await screen.findByRole('button',{name:'Product ready'}));
 await screen.findByRole('button',{name:/Queue details change/});
 await fireEvent.input(document.querySelector('#pd-ar')!,{target:{value:'  كتاب  '}});
 await fireEvent.input(document.querySelector('#pd-en')!,{target:{value:' Book '}});
 expect((document.querySelector('#pd-egp') as HTMLInputElement).value).toBe('0.00');
 await fireEvent.input(document.querySelector('#pd-egp')!,{target:{value:'90071992547409.93'}});
 await fireEvent.input(document.querySelector('#pd-usd')!,{target:{value:'12.5'}});
 await fireEvent.click(screen.getByRole('button',{name:/Queue details change/}));
 await confirmCode();
 await vi.waitFor(()=>expect(api.adminCreateCommand).toHaveBeenCalledExactlyOnceWith({store_id:'store-A',type:'catalog.product.details.update.v1',entity_id:'p-ready',expected_revision:17,payload:{product_id:'p-ready',arabic_name:'كتاب',arabic_description:null,english_name:'Book',english_description:'Description',width_cm:1,height_cm:7,egp_price_cents:'9007199254740993',usd_price_cents:'1250',cost_cents:'0',top_category_id:'c-ready',subcategory_ids:[],tag_ids:[],expected_catalog_revision:17}},'123456'));
});

it('submits ProductType creation as key intent with no fabricated entity identity', async()=>{
 defaults();api.adminProducts.mockResolvedValue({products:[],next_cursor:''});api.adminCreateCommand.mockResolvedValue({id:'command-123'});
 render(CatalogAdminPage,{store:'store-A'});await fireEvent.click(screen.getByRole('tab',{name:'Types'}));await screen.findByText('Papyrus');
 await fireEvent.input(document.querySelector('#nt-code')!,{target:{value:' BOOK '}});
 await fireEvent.input(document.querySelector('#nt-ar')!,{target:{value:' كتاب '}});
 await fireEvent.input(document.querySelector('#nt-en')!,{target:{value:' Book '}});
 await fireEvent.click(screen.getByRole('button',{name:/Queue type creation/}));
 await confirmCode();
 await vi.waitFor(()=>expect(api.adminCreateCommand).toHaveBeenCalledExactlyOnceWith({store_id:'store-A',type:'catalog.product-type.create.v1',entity_id:'',expected_revision:0,payload:{code:'book',name_ar:'كتاب',name_en:'Book',position:1,dimensions:[],capabilities:[],expected_type_revision:0}},'123456'));
});

it('shows the product editor in Arabic only when Arabic is chosen', async()=>{
 setLang('ar');defaults();api.adminProducts.mockResolvedValue({products:fixtures('ready').products,next_cursor:''});
 api.adminProduct.mockResolvedValue({...productDetail('ready'),name_en:'Ready product'});api.adminConfigurations.mockResolvedValue({configurations:[]});
 render(CatalogAdminPage,{store:'A'});await fireEvent.click(await screen.findByRole('button',{name:'Product ready'}));
 const editor=await screen.findByRole('region',{name:'محرر المنتج'});
 expect(editor.textContent).toContain('إدراج تعديل البيانات');expect(editor.textContent).toContain('سعر البيع (ج.م)');
 for (const english of ['Queue','Arabic name','Price (EGP)','Online availability','Classification','variant']) expect(editor.textContent).not.toContain(english);
 expect(screen.getByRole('tab',{name:'المنتجات'})).toBeTruthy();
 setLang('en');
 expect(await screen.findByRole('heading',{name:/Ready product/})).toBeTruthy();
});

it('locks what the shop has not allowed and says where to change it', async()=>{
 defaults();api.adminPermissions.mockResolvedValue({online_selling:true,products:false,catalog:false,devices:1});
 api.adminProducts.mockResolvedValue({products:fixtures('ready').products,next_cursor:''});api.adminProduct.mockResolvedValue({...productDetail('ready'),sell_online:true});
 api.adminConfigurations.mockResolvedValue({configurations:[]});api.adminCreateCommand.mockResolvedValue({id:'command-123'});
 render(CatalogAdminPage,{store:'store-A'});
 const summary=await screen.findByTestId('catalog-permissions');
 await vi.waitFor(()=>expect(summary.textContent).toMatch(/Product details and prices\s*Off/));
 await fireEvent.click(await screen.findByRole('button',{name:'Product ready'}));
 expect((await screen.findByRole('button',{name:'Queue details change'})).matches(':disabled')).toBe(true);
 expect((document.querySelector('#pd-egp') as HTMLInputElement).closest('fieldset')!.disabled).toBe(true);
 expect(screen.getByText(/does not allow Cloud to change product details or prices/)).toBeTruthy();
 expect((screen.getByRole('button',{name:'Queue classification change'}) as HTMLButtonElement).closest('fieldset')!.disabled).toBe(true);
 // Online selling is allowed and needs no authenticator code.
 await fireEvent.click(screen.getByRole('button',{name:'Disable online selling'}));
 await vi.waitFor(()=>expect(api.adminCreateCommand).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({type:'catalog.product.online-policy.update.v1'})));
 expect(screen.queryByLabelText('Authenticator code')).toBeNull();
});

it('keeps the prompt open on a wrong code and sends nothing more', async()=>{
 defaults();api.adminProducts.mockResolvedValue({products:fixtures('ready').products,next_cursor:''});api.adminProduct.mockResolvedValue(productDetail('ready'));
 api.adminConfigurations.mockResolvedValue({configurations:[]});
 api.adminCreateCommand.mockRejectedValueOnce(new ApiError(403,'FORBIDDEN','request failed (403)','MFA_INVALID')).mockResolvedValueOnce({id:'command-456'});
 render(CatalogAdminPage,{store:'store-A'});await fireEvent.click(await screen.findByRole('button',{name:'Product ready'}));
 await fireEvent.click(await screen.findByRole('button',{name:'Queue details change'}));
 await confirmCode('12345');
 expect(await screen.findByText('Enter the 6-digit code.')).toBeTruthy();
 expect(api.adminCreateCommand).not.toHaveBeenCalled();
 await confirmCode('111111');
 expect(await screen.findByText(/That code is not valid or was already used/)).toBeTruthy();
 expect(screen.getByLabelText('Authenticator code')).toBeTruthy();
 await confirmCode('222222');
 await vi.waitFor(()=>expect(api.adminCreateCommand).toHaveBeenLastCalledWith(expect.objectContaining({type:'catalog.product.details.update.v1'}),'222222'));
 expect(await screen.findByText(/Change queued/)).toBeTruthy();
 expect(screen.queryByLabelText('Authenticator code')).toBeNull();
 await fireEvent.click(screen.getByRole('button',{name:'Queue details change'}));
 await fireEvent.click(await screen.findByRole('button',{name:'Cancel'}));
 expect(screen.queryByLabelText('Authenticator code')).toBeNull();
 expect(api.adminCreateCommand).toHaveBeenCalledTimes(2);
});

it('explains a change the shop refused in Cloud', async()=>{
 defaults();api.adminProducts.mockResolvedValue({products:fixtures('ready').products,next_cursor:''});api.adminProduct.mockResolvedValue({...productDetail('ready'),sell_online:true});
 api.adminConfigurations.mockResolvedValue({configurations:[]});
 api.adminCreateCommand.mockRejectedValue(new ApiError(409,'CONFLICT','request failed (409)','CLOUD_PERMISSION_DISABLED'));
 render(CatalogAdminPage,{store:'store-A'});await fireEvent.click(await screen.findByRole('button',{name:'Product ready'}));
 await fireEvent.click(await screen.findByRole('button',{name:'Disable online selling'}));
 expect(await screen.findByText(/The shop does not allow Cloud to make this change/)).toBeTruthy();
});
it('shows product and variant images with list thumbnails (ADR-0056)', async()=>{
 defaults();api.adminMediaUrl.mockImplementation((store:string,sha:string)=>`/media/${store}/${sha}`);
 api.adminProducts.mockResolvedValue({products:[{...productRow('pic'),thumbnail_sha256:'main'},productRow('bare')],next_cursor:''});
 const img=(id:string,variant:string,position:number,primary:boolean,sha:string,available=true)=>({image_id:id,variant_id:variant,position,is_primary:primary,sha256:sha,width:800,height:600,available});
 api.adminProduct.mockResolvedValue({...productDetail('pic'),images:[img('i2','',1,false,'second'),img('i1','',0,true,'main'),img('i3','v-pic',0,true,'red'),img('i4','v-pic',1,false,'late',false)]});
 api.adminConfigurations.mockResolvedValue({configurations:[]});
 const {container}=render(CatalogAdminPage,{store:'A'});
 await screen.findByText('Product pic');
 const listThumbs=[...container.querySelectorAll('table.data img.thumb')].map(e=>e.getAttribute('src'));
 expect(listThumbs).toEqual(['/media/A/main']);
 expect(container.querySelectorAll('table.data span.thumb.empty').length).toBe(1);
 await fireEvent.click(screen.getByRole('button',{name:'Product pic'}));
 const gallery=await screen.findByTestId('product-images');
 // Shared images show first; the base gallery has the friendly Original label.
 expect(gallery.querySelector('figure img')?.getAttribute('src')).toBe('/media/A/main');
 expect(screen.getByText('Waiting for upload')).toBeTruthy();
 await fireEvent.click(screen.getByRole('button',{name:'Original — Image 1'}));
 expect(gallery.querySelector('figure img')?.getAttribute('src')).toBe('/media/A/red');
 // The variant row carries its own main image.
 expect([...container.querySelectorAll('td.thumb-cell img')].map(e=>e.getAttribute('src'))).toEqual(['/media/A/red']);
});


const frameChoice = {id:'cfg-1',style_code:'classic',style_name_ar:'كلاسيكي',style_name_en:'Classic',color_code:'black',color_name_ar:'أسود',color_name_en:'Black',egp_delta_minor:'30000',usd_delta_minor:'600',enabled:true,position:0};
it('shows a frame-read error instead of a false empty result and retries without losing product edits',async()=>{
 defaults();api.adminProducts.mockResolvedValue({products:fixtures('ready').products,next_cursor:''});
 api.adminProduct.mockResolvedValue(productDetail('ready'));
 api.adminConfigurations.mockRejectedValueOnce(new Error('private SQL path')).mockResolvedValue({configurations:[frameChoice]});
 render(CatalogAdminPage,{store:'A'});
 await fireEvent.click(await screen.findByRole('button',{name:'Product ready'}));
 expect(await screen.findByText('Loading failed.')).toBeTruthy();
 expect(screen.queryByText('No frame options.')).toBeNull();
 expect(screen.queryByText('private SQL path')).toBeNull();
 const name=screen.getByLabelText('Arabic name');
 await fireEvent.input(name,{target:{value:'Unsent edit'}});
 const region=screen.getByRole('region',{name:'Frame options'});
 await fireEvent.click(within(region).getByRole('button',{name:'Retry'}));
 expect((await screen.findByTestId('frame-configurations')).textContent).toContain('Classic / Black');
 expect((name as HTMLInputElement).value).toBe('Unsent edit');
 expect(api.adminProduct).toHaveBeenCalledTimes(1);
 expect(api.adminConfigurations).toHaveBeenNthCalledWith(2,'A','p-ready');
 expect(api.adminCreateCommand).not.toHaveBeenCalled();
});
it('shows frame loading separately and reports empty only after a successful frame read',async()=>{
 defaults();api.adminProducts.mockResolvedValue({products:fixtures('ready').products,next_cursor:''});
 api.adminProduct.mockResolvedValue(productDetail('ready'));
 let finish!: (value:unknown)=>void;
 api.adminConfigurations.mockImplementation(()=>new Promise(resolve=>finish=resolve));
 render(CatalogAdminPage,{store:'A'});
 await fireEvent.click(await screen.findByRole('button',{name:'Product ready'}));
 await screen.findByLabelText('Arabic name');
 const region=await screen.findByRole('region',{name:'Frame options'});
 expect(region.getAttribute('aria-busy')).toBe('true');
 expect(within(region).getByRole('status').textContent).toBe('Loading…');
 expect(screen.queryByText('No frame options.')).toBeNull();
 expect(screen.queryByText('Loading product…')).toBeNull();
 await act(async()=>{finish({configurations:[]});});
 expect(within(region).getByText('No frame options.')).toBeTruthy();
 expect(region.getAttribute('aria-busy')).toBe('false');
});
it('ignores a late frame result after selecting a different product',async()=>{
 defaults();api.adminProducts.mockResolvedValue({products:[productRow('ready'),productRow('other')],next_cursor:''});
 api.adminProduct.mockImplementation((_store:string,id:string)=>Promise.resolve(productDetail(id==='p-ready'?'ready':'other')));
 let finish!: (value:unknown)=>void;
 api.adminConfigurations.mockImplementation((_store:string,id:string)=>id==='p-ready'?new Promise(resolve=>finish=resolve):Promise.resolve({configurations:[{...frameChoice,style_name_en:'Modern'}]}));
 render(CatalogAdminPage,{store:'A'});
 await fireEvent.click(await screen.findByRole('button',{name:'Product ready'}));
 await vi.waitFor(()=>expect(api.adminConfigurations).toHaveBeenCalledWith('A','p-ready'));
 await fireEvent.click(screen.getByRole('button',{name:'Product other'}));
 expect((await screen.findByTestId('frame-configurations')).textContent).toContain('Modern / Black');
 await act(async()=>{finish({configurations:[frameChoice]});});
 expect(screen.getByTestId('frame-configurations').textContent).toContain('Modern / Black');
 expect(screen.queryByText(/Classic \/ Black/)).toBeNull();
});
it('ignores a failed retry from the previous Store and retains the new Store frame choices',async()=>{
 defaults();
 api.adminProducts.mockImplementation((store:string)=>Promise.resolve({products:fixtures(store).products,next_cursor:''}));
 api.adminProduct.mockImplementation((store:string)=>Promise.resolve(productDetail(store)));
 let failOld!: (error:Error)=>void;
 api.adminConfigurations.mockRejectedValueOnce(new Error('first read')).mockImplementationOnce(()=>new Promise((_resolve,reject)=>failOld=reject)).mockResolvedValue({configurations:[{...frameChoice,style_name_en:'Modern'}]});
 const page=render(CatalogAdminPage,{store:'A'});
 await fireEvent.click(await screen.findByRole('button',{name:'Product A'}));
 await screen.findByText('Loading failed.');
 await fireEvent.click(within(screen.getByRole('region',{name:'Frame options'})).getByRole('button',{name:'Retry'}));
 await page.rerender({store:'B'});
 await fireEvent.click(await screen.findByRole('button',{name:'Product B'}));
 expect((await screen.findByTestId('frame-configurations')).textContent).toContain('Modern / Black');
 await act(async()=>{failOld(new Error('private late failure'));});
 expect(screen.queryByText('Loading failed.')).toBeNull();
 expect(screen.queryByText('private late failure')).toBeNull();
 expect(screen.getByTestId('frame-configurations').textContent).toContain('Modern / Black');
});

it.each([['',null],['   ',null],['0','0']])('queues a name edit with purchase cost %j as %j only after OTP',async(input,expected)=>{
 defaults();
 api.adminProducts.mockResolvedValue({products:fixtures('ready').products,next_cursor:''});
 const detail={...productDetail('ready'),cost_minor:null};
 api.adminProduct.mockResolvedValue(detail);
 api.adminConfigurations.mockResolvedValue({configurations:[]});
 api.adminCreateCommand.mockResolvedValue({id:'command-unknown-cost'});
 render(CatalogAdminPage,{store:'store-A'});
 await fireEvent.click(await screen.findByRole('button',{name:'Product ready'}));
 const cost=await screen.findByLabelText('Cost (EGP)');
 expect((cost as HTMLInputElement).value).toBe('');
 expect(cost.getAttribute('aria-describedby')).toBe('pd-cost-hint');
 await fireEvent.input(document.querySelector('#pd-ar')!,{target:{value:'Renamed product'}});
 await fireEvent.input(cost,{target:{value:input}});
 await fireEvent.click(screen.getByRole('button',{name:'Queue details change'}));
 expect(api.adminCreateCommand).not.toHaveBeenCalled();
 await confirmCode();
 await vi.waitFor(()=>expect(api.adminCreateCommand).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({store_id:'store-A',type:'catalog.product.details.update.v1',payload:expect.objectContaining({arabic_name:'Renamed product',cost_cents:expected})}),'123456'));
 expect(detail.cost_minor).toBeNull();
 expect(detail.name_ar).toBe('Product ready');
 expect(api.adminProduct).toHaveBeenCalledTimes(1);
});

it.each(['-1','1.001','not-money'])('rejects malformed nonblank purchase cost %j before opening OTP',async(value)=>{
 defaults();api.adminProducts.mockResolvedValue({products:fixtures('ready').products,next_cursor:''});
 api.adminProduct.mockResolvedValue({...productDetail('ready'),cost_minor:null});api.adminConfigurations.mockResolvedValue({configurations:[]});
 render(CatalogAdminPage,{store:'A'});await fireEvent.click(await screen.findByRole('button',{name:'Product ready'}));
 await fireEvent.input(document.querySelector('#pd-cost')!,{target:{value}});
 await fireEvent.click(screen.getByRole('button',{name:'Queue details change'}));
 expect(api.adminCreateCommand).not.toHaveBeenCalled();
 expect(screen.queryByLabelText('Authenticator code')).toBeNull();
 expect(screen.getByText(/Enter prices in pounds or dollars/)).toBeTruthy();
});

function costVariantDetails(){
 const base=productDetail('ready').variants[0];
 const attr={definition_code:'color',value_code:'gold',definition_name_ar:'اللون',definition_name_en:'Color',name_ar:'ذهبي',name_en:'Gold',position:0};
 return {...productDetail('ready'),variant_count:3,variants:[
  {...base,variant_id:'base',sku:'ML-ORIGINAL',combination_key:'',attributes:[],price_egp_minor:null,price_usd_minor:null,cost_egp_minor:null,effective_price_egp_minor:'50000',effective_price_usd_minor:'1000',effective_cost_egp_minor:'30000'},
  {...base,variant_id:'gold',sku:'ML-GOLD',attributes:[attr],price_egp_minor:'0',price_usd_minor:'9007199254740993',cost_egp_minor:'0',effective_price_egp_minor:'0',effective_price_usd_minor:'9007199254740993',effective_cost_egp_minor:'0'},
  {...base,variant_id:'unknown',sku:'ML-UNKNOWN',attributes:[{...attr,value_code:'blue',name_ar:'أزرق',name_en:'Blue'}],price_egp_minor:null,price_usd_minor:null,cost_egp_minor:null,effective_price_egp_minor:null,effective_price_usd_minor:null,effective_cost_egp_minor:null}
 ]};
}
it('shows effective variant selling prices and purchase cost with raw inheritance, explicit zero and exact cents',async()=>{
 defaults();api.adminProducts.mockResolvedValue({products:fixtures('ready').products,next_cursor:''});
 api.adminProduct.mockResolvedValue(costVariantDetails());api.adminConfigurations.mockResolvedValue({configurations:[]});
 render(CatalogAdminPage,{store:'A'});await fireEvent.click(await screen.findByRole('button',{name:'Product ready'}));
 const table=await screen.findByRole('table',{name:'Variants'});
 expect(within(table).getByRole('columnheader',{name:'Purchase cost (EGP)'})).toBeTruthy();
 expect(within(table).getByRole('columnheader',{name:'Selling price (EGP)'})).toBeTruthy();
 expect(within(table).getByRole('columnheader',{name:'Selling price (USD)'})).toBeTruthy();
 const original=within(table).getByText('ML-ORIGINAL').closest('tr')!;
 expect(within(original).getByText('Original')).toBeTruthy();
 expect(original.textContent).toContain('300 EGP');expect(original.textContent).toContain('500 EGP');expect(original.textContent).toContain('$10.00');
 expect(within(original).getAllByText('Uses product value')).toHaveLength(3);
 const gold=within(table).getByText('ML-GOLD').closest('tr')!;
 expect(within(gold).getAllByText('0 EGP')).toHaveLength(2);
 expect(within(gold).getAllByText('Custom value')).toHaveLength(3);
 expect(within(gold).getByText('$90,071,992,547,409.93')).toBeTruthy();
 const unknown=within(table).getByText('ML-UNKNOWN').closest('tr')!;
 expect(within(unknown).getAllByText('Not recorded')).toHaveLength(3);
 expect(within(unknown).queryByText('0 EGP')).toBeNull();
 expect(api.adminCreateCommand).not.toHaveBeenCalled();
});
it('localizes Original and price inheritance labels in Arabic while keeping SKUs and money LTR',async()=>{
 setLang('ar');defaults();api.adminProducts.mockResolvedValue({products:fixtures('ready').products,next_cursor:''});
 api.adminProduct.mockResolvedValue(costVariantDetails());api.adminConfigurations.mockResolvedValue({configurations:[]});
 render(CatalogAdminPage,{store:'A'});await fireEvent.click(await screen.findByRole('button',{name:'Product ready'}));
 const table=await screen.findByRole('table',{name:'الخيارات'});
 expect(within(table).getByText('الأصلي')).toBeTruthy();
 expect(within(table).getByRole('columnheader',{name:'تكلفة الشراء (جنيه)'})).toBeTruthy();
 expect(within(table).getByRole('columnheader',{name:'سعر البيع (دولار)'})).toBeTruthy();
 expect(within(table).getAllByText('قيمة خاصة')).toHaveLength(3);
 expect(within(table).getByText('ML-GOLD').getAttribute('dir')).toBe('ltr');
 expect(within(table).getByText('$90,071,992,547,409.93').getAttribute('dir')).toBe('ltr');
 expect(table.textContent).not.toContain('Uses product value');
});
