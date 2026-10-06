import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, render, screen, fireEvent } from '@testing-library/svelte';
import CatalogAdminPage from './CatalogAdminPage.svelte';
import { dashboardApi } from '../lib/api.js';
vi.mock('../lib/api.js', async (orig) => {
 const m = await orig() as Record<string, unknown>;
 return {...m,dashboardApi:{adminProducts:vi.fn(),adminCategories:vi.fn(),adminTags:vi.fn(),adminCommands:vi.fn(),adminProduct:vi.fn(),adminConfigurations:vi.fn()}};
});
const api=dashboardApi as unknown as Record<string,ReturnType<typeof vi.fn>>;
afterEach(()=>{cleanup();vi.resetAllMocks()});
function fixtures(name:string){
 return {products:[{product_id:'p-'+name,sku:'SKU-'+name,name_ar:'Product '+name,catalog_revision:1}],categories:[{category_id:'c-'+name,name_ar:'Category '+name,parent_ids:[]}],tags:[{tag_id:'t-'+name,name_ar:'Tag '+name}],commands:[]};
}
function defaults(){api.adminCategories.mockResolvedValue({categories:fixtures('ready').categories});api.adminTags.mockResolvedValue({tags:fixtures('ready').tags});api.adminCommands.mockResolvedValue({commands:[],next_cursor:''})}
it('loads all sibling resources without cancelling successful responses', async()=>{
 const f=fixtures('ready');defaults();api.adminProducts.mockResolvedValue({products:f.products,next_cursor:''});
 render(CatalogAdminPage,{store:'store-A'});
 expect(await screen.findByText('SKU-ready')).toBeTruthy();
 await fireEvent.click(screen.getByRole('tab',{name:'الفئات / Categories'}));expect(await screen.findByText('Category ready')).toBeTruthy();
 await fireEvent.click(screen.getByRole('tab',{name:'الوسوم / Tags'}));expect(await screen.findByText('Tag ready')).toBeTruthy();
 expect(screen.queryByText('جارٍ التحميل… / Loading…')).toBeNull();
});
it('discards late old-Store results while retaining the new Store',async()=>{
 defaults();let oldResolve!:(value:unknown)=>void;
 api.adminProducts.mockImplementation((store:string)=>store==='A'?new Promise(resolve=>{oldResolve=resolve}):Promise.resolve({products:fixtures('B').products,next_cursor:''}));
 const page=render(CatalogAdminPage,{store:'A'});await vi.waitFor(()=>expect(api.adminProducts).toHaveBeenCalledWith('A','',null));
 await page.rerender({store:'B'});expect(await screen.findByText('SKU-B')).toBeTruthy();
 oldResolve({products:fixtures('A').products,next_cursor:''});await Promise.resolve();await Promise.resolve();
 expect(screen.queryByText('SKU-A')).toBeNull();expect(screen.getByText('SKU-B')).toBeTruthy();
});
it('does not let a late Product detail replace a newer selection',async()=>{
 defaults();const f=fixtures('ready');api.adminProducts.mockResolvedValue({products:[...f.products,{product_id:'other',sku:'OTHER',name_ar:'Other'}],next_cursor:''});
 let oldResolve!:(value:unknown)=>void;
 api.adminProduct.mockImplementation((_store:string,id:string)=>id==='other'?Promise.resolve({product_id:'other',sku:'OTHER',name_ar:'Other selected',catalog_revision:1,egp_price_minor:'0',cost_minor:'0',top_category_id:'category',subcategory_ids:[],tag_ids:[]}):new Promise(resolve=>{oldResolve=resolve}));api.adminConfigurations.mockResolvedValue({configurations:[]});
 render(CatalogAdminPage,{store:'A'});await fireEvent.click(await screen.findByRole('button',{name:'Product ready'}));
 await fireEvent.click(screen.getByRole('button',{name:'Other'}));expect(await screen.findByText('Other selected')).toBeTruthy();
 oldResolve({product_id:'p-ready',sku:'SKU-ready',name_ar:'Old selection',catalog_revision:1});await Promise.resolve();await Promise.resolve();expect(screen.queryByText('Old selection')).toBeNull();
});
