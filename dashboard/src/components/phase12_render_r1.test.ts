import { render, cleanup } from '@testing-library/svelte';
import { afterEach, expect, it } from 'vitest';
import TagCard from './TagCard.svelte';
import CatalogHealthCard from './CatalogHealthCard.svelte';
import type { TagListResponse } from '../lib/api.js';
afterEach(cleanup);
const base: TagListResponse = {generated_at:'',timezone:'Africa/Cairo',store_id:null,overlap_note:'',rows:[{tag_id:'a',tag_slug:'gold',name_ar:'قديم',name_en:'Gold',units:1,units_returned:0,currencies:[{currency:'EGP',line_sales_minor:'100',line_refund_minor:'0',net_minor:'100'}]}]};
it('historical identities preserve corresponding nodes, labels and money across reorder and refresh',async()=>{
 const data={...base,rows:[...base.rows,{...base.rows[0],name_ar:'جديد',name_en:'Golden',currencies:[{currency:'EGP',line_sales_minor:'700',line_refund_minor:'200',net_minor:'500'}]},{...base.rows[0],tag_id:'b'}]};
 const props={data,status:'loaded' as const,errStatus:null,onretry(){},currency:'EGP' as const};
 const r=render(TagCard,{props});const before=Array.from(r.container.querySelectorAll('tbody tr'));expect(before).toHaveLength(3);expect(before[0].textContent).toContain('1 ج.م');expect(before[1].textContent).toContain('5 ج.م');
 await r.rerender({...props,data:{...data,rows:[...data.rows].reverse()}});const after=Array.from(r.container.querySelectorAll('tbody tr'));expect(after[0]).toBe(before[2]);expect(after[1]).toBe(before[1]);expect(after[2]).toBe(before[0]);
 await r.rerender({...props,data:{...data,rows:[]},status:'empty',currency:'USD'});expect(r.container.querySelectorAll('tbody tr')).toHaveLength(0);
 await r.rerender({...props,currency:'all'});expect(r.container.querySelectorAll('tbody tr')).toHaveLength(3);expect(r.container.textContent).toContain('Top Tags by Units');expect(r.container.textContent).toContain('عدد الوحدات');expect(r.container.textContent).toContain('قد يساهم بند البيع في عدة وسوم');expect(r.container.textContent).toContain('must not be summed');
 await r.rerender(props);expect(r.container.textContent).toContain('Top Tags by Net Sales');expect(r.container.textContent).toContain('صافي المبيعات');
});
it('current health and complete provider choices remain explicit when detail is truncated',()=>{
 const r=render(CatalogHealthCard,{props:{data:{generated_at:'',store_id:null,provider_key:'',providers:['alpha','zeta'],reason_codes:[],counts:[{reason_code:'VARIANT_MISSING_SKU',products:60}],detail:[{reason_code:'VARIANT_MISSING_SKU',provider_key:'alpha'}],detail_limit:50,detail_truncated:true},status:'loaded',errStatus:null,onretry(){},provider:'',onprovider(){}}});
 expect(Array.from(r.container.querySelectorAll('option')).map(o=>o.value)).toEqual(['','alpha','zeta']);expect(r.container.textContent).toContain('summary counts are complete');expect(r.container.textContent).toContain('selected reporting period does not filter it');expect(r.container.textContent).toContain('فترة التقرير المختارة');
});
