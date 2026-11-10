import { cleanup, render, screen, within } from '@testing-library/svelte';
import { afterEach, beforeEach, it, expect } from 'vitest';
import { setLang } from '../lib/i18n.svelte';
import type { RecordedSaleLine, SalesReport } from '../lib/reportsApi';
import ReportSaleLines from './ReportSaleLines.svelte';

beforeEach(() => setLang('en'));
afterEach(() => { cleanup(); setLang('ar'); });
const original: RecordedSaleLine = {
  id: 'base', product_name: 'Papyrus', sku: 'ML-123456789ABC', currency: 'EGP', quantity: 1,
  unit_cost_minor: '500', catalog_price_minor: '1000', unit_price_minor: '900', line_total_minor: '900', attributes: [],
};
const option: RecordedSaleLine = {
  ...original, id: 'gold', sku: 'ML-23456789ABCD', unit_cost_minor: '700', catalog_price_minor: '1500',
  unit_price_minor: '1400', line_total_minor: '1400', attributes: [
    {definition_code:'color',value_code:'gold',name_ar:'ذهبي',name_en:'Gold',definition_name_ar:'اللون',definition_name_en:'Color'},
    {definition_code:'style',value_code:'traditional',name_ar:'تقليدي',name_en:'Traditional',definition_name_ar:'الأسلوب',definition_name_en:'Style'},
  ],
};
const sale = (id: string, lines?: RecordedSaleLine[]): SalesReport['sales'][number] => ({
  sale_id: id, sale_number: `MLR-20261110-${id}`, occurred_at: '2026-11-10T09:00:00Z', currency: 'EGP', products: 'Papyrus', skus: 'HISTORICAL-SKU',
  root_names_ar: 'بردي', root_names_en: 'Papyrus', subcategory_names_ar: 'فن', subcategory_names_en: 'Art', items_sold: 2,
  payment_methods: ['cash'], cost_minor: '1200', missing_cost_items: 0, total_minor: '2300', original_currency: 'EGP', original_total_minor: '2300', rate_microrate: null, lines,
});
it('shows flat Original and option rows with separate SKU/value columns and one sale total', () => {
  render(ReportSaleLines, {sales:[sale('11111111',[original,option]),sale('22222222',[option])]});
  const table=screen.getByRole('table',{name:'Sales details'});
  const rows=within(table).getAllByRole('row');
  expect(rows).toHaveLength(4);
  expect(screen.queryByRole('button')).toBeNull();
  expect(screen.queryByRole('link')).toBeNull();
  expect(within(rows[1]).getByText('Original')).toBeTruthy();
  expect(within(rows[2]).getByText('Gold / Traditional')).toBeTruthy();
  expect(screen.queryByText('Color: Gold')).toBeNull();
  expect(screen.getByRole('columnheader',{name:'Variant value'})).toBeTruthy();
  expect(screen.getByRole('columnheader',{name:'SKU'})).toBeTruthy();
  expect(within(rows[2]).getByText(option.sku).getAttribute('dir')).toBe('ltr');
  expect(rows[1].textContent).toContain('Cash');
  expect(rows[1].textContent).toContain('Category: Papyrus');
  expect(rows[1].textContent).toContain('Subcategory: Art');
  expect(rows[1].querySelector('.sale-total')?.textContent).toContain('23 EGP');
  expect(rows[2].querySelector('.sale-total')?.textContent).toBe('—');
  expect(within(rows[2]).getAllByRole('cell')[4].textContent).toBe('7 EGP');
  expect(within(rows[2]).getAllByRole('cell')[5].textContent).toBe('15 EGP');
  expect(within(rows[2]).getAllByRole('cell')[6].textContent).toBe('14 EGP');
});
it('groups whole reference cells through four tones, repeats per sale and wraps with the page offset',()=>{
 render(ReportSaleLines,{sales:Array.from({length:5},(_,i)=>sale(String(i),[original,option])),offset:3});
 const cells=screen.getAllByRole('rowheader');
 expect(cells.map(cell=>cell.dataset.saleTone)).toEqual(['3','3','0','0','1','1','2','2','3','3']);
 expect(new Set(cells.map(cell=>cell.dataset.saleTone)).size).toBe(4);
 expect(cells[0].querySelector('bdi')?.getAttribute('dir')).toBe('ltr');
});
it('preserves zero, unknown amounts and money above the JavaScript safe integer limit',()=>{
 render(ReportSaleLines,{sales:[sale('11111111',[
  {...original,unit_cost_minor:'0',catalog_price_minor:'0',unit_price_minor:'0',line_total_minor:'0'},
  {...option,unit_cost_minor:null,catalog_price_minor:null,unit_price_minor:'9007199254740993',line_total_minor:'9007199254740993'},
 ])]});
 const rows=within(screen.getByRole('table')).getAllByRole('row');
 expect(within(rows[1]).getAllByText('0 EGP')).toHaveLength(4);
 expect(within(rows[2]).getAllByText('Not recorded')).toHaveLength(2);
 expect(within(rows[2]).getAllByText('90,071,992,547,409.93 EGP')).toHaveLength(2);
});
it('retains native line prices beside a converted sale total and original recorded total',()=>{
 const usd={...original,currency:'USD' as const,unit_cost_minor:'100',catalog_price_minor:'200',unit_price_minor:'150',line_total_minor:'150'};
 render(ReportSaleLines,{sales:[{...sale('11111111',[usd]),total_minor:'7800',original_currency:'USD',original_total_minor:'150',rate_microrate:'52000000'},sale('22222222',[original])]});
 const rows=within(screen.getByRole('table')).getAllByRole('row');
 expect(screen.getByRole('columnheader',{name:'Recorded as'})).toBeTruthy();
 expect(rows[1].querySelector('.sale-total')?.textContent).toBe('78 EGP');
 expect(within(rows[1]).getAllByText('$1.50')).toHaveLength(3);
 expect(rows[1].textContent).toContain('at 52.00');
 expect(rows[2].lastElementChild?.textContent?.trim()).toBe('—');
});
it('does not invent unit prices, unit cost or option identity for older aggregate-only sales',()=>{
 render(ReportSaleLines,{sales:[sale('11111111')]});
 expect(screen.getAllByText('Not recorded')).toHaveLength(5);
 expect(screen.queryByText('12 EGP')).toBeNull();
 expect(screen.queryByText('Original')).toBeNull();
 expect(screen.getByText('HISTORICAL-SKU')).toBeTruthy();
 expect(screen.getByText('23 EGP')).toBeTruthy();
});
it('keeps Arabic option values and Original labels with LTR references and SKUs',()=>{
 setLang('ar');
 render(ReportSaleLines,{sales:[sale('11111111',[original,option])]});
 expect(screen.getByRole('columnheader',{name:'قيمة الخيار'})).toBeTruthy();
 expect(screen.getByText('الأصلي')).toBeTruthy();
 expect(screen.getByText('ذهبي / تقليدي')).toBeTruthy();
 expect(screen.getByText(option.sku).getAttribute('dir')).toBe('ltr');
 for(const ref of screen.getAllByText(/MLR-20261110-/))expect(ref.getAttribute('dir')).toBe('ltr');
});
