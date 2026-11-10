import { describe, expect, it, vi, afterEach, beforeEach } from 'vitest';
import { act, cleanup, render, screen, fireEvent, waitFor, within } from '@testing-library/svelte';
import ReportsPage from './ReportsPage.svelte';
import { reportsApi, type SalesReport, type StockReport } from '../lib/reportsApi.js';
import { ApiError } from '../lib/http.js';
import { setLang } from '../lib/i18n.svelte.js';
import { choose } from '../test/choose.js';

vi.mock('../lib/reportsApi.js', async (orig) => {
	const mod = (await orig()) as Record<string, unknown>;
	return { ...mod, reportsApi: { sales: vi.fn(), stock: vi.fn() } };
});

const api = reportsApi as unknown as { sales: ReturnType<typeof vi.fn>; stock: ReturnType<typeof vi.fn> };
const store = { store_id: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', display_name: 'Luxor Shop', timezone: 'Africa/Cairo', status: 'active', device_count: 1, created_at: '2026-09-01T00:00:00Z', updated_at: '2026-09-01T00:00:00Z' };
const params = { period: 'last_10_completed_days' };
const noFilter = { root_category_ids: [], subcategory_ids: [], tag_ids: [], payment_method: '' };

function summary(currency: 'EGP' | 'USD', sales: string, extra: Partial<SalesReport['summary'][number]> = {}) {
	return {
		currency, sales_count: 2, items_sold: 3, gross_minor: sales, discount_minor: '0', tax_minor: '0', sales_minor: sales,
		cost_minor: '52000', missing_cost_items: 0, return_count: 0, returned_items: 0, refund_minor: '0', refunded_tax_minor: '0',
		restocked_cost_minor: '0', net_sales_minor: sales, net_tax_minor: '0', net_revenue_minor: sales, net_cost_minor: '52000',
		profit_minor: (BigInt(sales) - 52000n).toString(), ...extra
	};
}

function salesReport(rows: SalesReport['summary']): SalesReport {
	return {
		generated_at: '2026-10-27T10:00:00Z',
		period: { kind: 'last_10_completed_days', timezone: 'Africa/Cairo', start_date: '2026-10-17', end_date: '2026-10-26' },
		store_id: store.store_id, display_currency: 'EGP',
		settings: { tax_enabled: false, discount_enabled: false, returns_enabled: false, revision: 1, updated_at: '2026-10-27T10:00:00Z' },
		filter: { root_category_ids: [], subcategory_ids: [], tag_ids: [], payment_method: '' },
		filter_options: { categories: [{ id: 'c1', name_ar: 'فرعوني', name_en: 'Pharaonic' }], subcategories: [], tags: [{ id: 't1', name_ar: 'ذهبي', name_en: 'Gold' }] },
		shop: { name_ar: 'متجر الأقصر', name_en: 'Luxor Papyrus', address_ar: 'الأقصر', address_en: 'Luxor', phone: '+20100' },
		summary: rows, base: { sales_minor: '114000', net_sales_minor: '114000', profit_minor: '62000', missing_cost_items: 0 },
		daily: [], categories: [], subcategories: [], tags: [], products: [], payments: [], sales: [],
		sales_total_count: 0, unclassified_items: 0, detail_offset: 0, detail_limit: 50,
		currency_mix: [{ currency: 'EGP', sales_count: 2, total_minor: '114000', report_currency: 'EGP', report_total_minor: '114000' }],
		exchange_rate: { latest: null, period_min_microrate: null, period_max_microrate: null }
	};
}

const stock: StockReport = {
	generated_at: '2026-10-27T10:00:00Z', store_id: store.store_id, shop: null,
	totals: { products_in_stock: 1, variants_in_stock: 1, units: 10, cost_minor: '40000000', retail_minor: '60000000', units_without_cost: 0, units_without_price: 0, inactive_units: 0 },
	categories: [], items: []
};

beforeEach(() => setLang('en'));
afterEach(() => {
	cleanup();
	vi.resetAllMocks();
	setLang('ar');
});

describe('ReportsPage', () => {
	it('loads the sales report in EGP for the selected Store and folds counts into business totals', async () => {
		api.sales.mockResolvedValue(salesReport([summary('EGP', '114000')]));
		render(ReportsPage, { params, store: store.store_id, stores: [store] });
		expect(await screen.findByText('Business totals · EGP')).toBeTruthy();
		expect(api.sales).toHaveBeenCalledWith(params, 'EGP', noFilter, store.store_id, 0, 50, expect.anything());
		expect(screen.getByRole('rowheader', { name: 'Number of sales' })).toBeTruthy();
		expect(screen.queryByText('Sales and quantities')).toBeNull();
		expect(screen.getByText('Luxor Papyrus')).toBeTruthy();
	});

	it('switches the display currency and shows refunds only when the period has returns', async () => {
		api.sales.mockResolvedValueOnce(salesReport([summary('EGP', '114000')]));
		api.sales.mockResolvedValueOnce(salesReport([summary('USD', '219', { return_count: 1, returned_items: 1, refund_minor: '10' })]));
		render(ReportsPage, { params, store: '', stores: [store] });
		await screen.findByText('Business totals · EGP');
		await choose(screen.getByTestId('report-currency'), 'USD');
		expect(await screen.findByText('Sales and quantities')).toBeTruthy();
		expect(api.sales).toHaveBeenLastCalledWith(params, 'USD', noFilter, store.store_id, 0, 50, expect.anything());
		expect(screen.getByText('How the figures add up')).toBeTruthy();
	});

	it('filters by tag and payment method like MoonLightRetail', async () => {
		api.sales.mockResolvedValue(salesReport([summary('EGP', '114000')]));
		render(ReportsPage, { params, store: store.store_id, stores: [store] });
		await screen.findByText('Business totals · EGP');
		await fireEvent.click(screen.getByTestId('report-tags'));
		await fireEvent.click(document.querySelector<HTMLInputElement>('#report-tags-popup input[data-value="t1"]')!);
		await choose(screen.getByTestId('report-payment'), 'card');
		expect(api.sales).toHaveBeenLastCalledWith(params, 'EGP', { ...noFilter, tag_ids: ['t1'], payment_method: 'card' }, store.store_id, 0, 50, expect.anything());
		expect(screen.getByRole('button', { name: 'Tags: Gold ×' })).toBeTruthy();
	});

	it('shows refund rows when the business uses returns, even without any', async () => {
		const report = salesReport([summary('EGP', '114000')]);
		report.settings.returns_enabled = true;
		api.sales.mockResolvedValue(report);
		render(ReportsPage, { params, store: store.store_id, stores: [store] });
		expect(await screen.findByText('Sales and quantities')).toBeTruthy();
	});

	it('explains a missing USD rate', async () => {
		api.sales.mockRejectedValue(new ApiError(409, 'CONFLICT', 'request failed (409)', 'EXCHANGE_RATE_REQUIRED'));
		render(ReportsPage, { params, store: store.store_id, stores: [store] });
		expect((await screen.findByRole('alert')).textContent).toMatch(/no USD exchange rate/);
	});

	it('explains the currencies and USD rates behind the report', async () => {
		const report = salesReport([summary('EGP', '114000')]);
		report.currency_mix = [
			{ currency: 'EGP', sales_count: 1, total_minor: '10000', report_currency: 'EGP', report_total_minor: '10000' },
			{ currency: 'USD', sales_count: 2, total_minor: '200', report_currency: 'EGP', report_total_minor: '10400' }
		];
		report.exchange_rate = {
			latest: { rate_microrate: '52500000', effective_at: '2026-10-25T08:00:00Z', source: 'cloud' },
			period_min_microrate: '50000000',
			period_max_microrate: '52000000'
		};
		report.sales = [
			{ sale_id: 's1', sale_number: 'R-0001', occurred_at: '2026-10-20T10:00:00Z', currency: 'EGP', products: 'Scarab', skus: 'PAP-1', root_names_ar: '', root_names_en: '',
				subcategory_names_ar: '', subcategory_names_en: '', items_sold: 1, payment_methods: ['card'], cost_minor: '0', missing_cost_items: 0, total_minor: '10400',
				original_currency: 'USD', original_total_minor: '200', rate_microrate: '52000000' },
			{ sale_id: 's2', sale_number: 'R-0002', occurred_at: '2026-10-20T09:00:00Z', currency: 'EGP', products: 'Papyrus', skus: 'PAP-2', root_names_ar: '', root_names_en: '',
				subcategory_names_ar: '', subcategory_names_en: '', items_sold: 1, payment_methods: ['cash'], cost_minor: '0', missing_cost_items: 0, total_minor: '10000',
				original_currency: 'EGP', original_total_minor: '10000', rate_microrate: null }
		];
		report.sales_total_count = 2;
		api.sales.mockResolvedValue(report);
		render(ReportsPage, { params, store: store.store_id, stores: [store] });
		const card = await screen.findByTestId('report-currency-mix');
		expect(card.textContent).toMatch(/Sales recorded in EGP\s*100 EGP\s*Sales: 1/);
		expect(card.textContent).toMatch(/Sales recorded in USD\s*\$2\.00\s*Sales: 2\s*In this report: 104 EGP/);
		expect(card.textContent).toMatch(/Latest USD rate\s*1 USD = 52\.50 EGP/);
		expect(card.textContent).toMatch(/From Cloud settings/);
        expect(card.textContent).toMatch(/All sales after conversion \(EGP\)\s*1,140 EGP\s*Sales: 2/);
		expect(card.textContent).toMatch(/50\.00 – 52\.00 EGP per USD/);
		expect(card.textContent).toMatch(/past sales can use a different rate/);

		await fireEvent.click(screen.getByRole('tab', { name: 'Sales details' }));
		expect(screen.getByRole('columnheader', { name: 'Recorded as' })).toBeTruthy();
		const usdRow = screen.getByText('R-0001').closest('tr')!;
		expect(usdRow.textContent).toMatch(/\$2\.00\s*at 52\.00/);
		expect(screen.getByText('R-0002').closest('tr')!.textContent).toMatch(/—$/);
		// The reference stays in the cell's direction: only the code is LTR.
		expect(screen.getByText('R-0001').tagName).toBe('BDI');
		expect(screen.getByText('R-0001').closest('th')!.hasAttribute('dir')).toBe(false);
	});

	it('says when no USD rate is known and lists USD even without USD sales', async () => {
		api.sales.mockResolvedValue(salesReport([summary('EGP', '114000')]));
		render(ReportsPage, { params, store: store.store_id, stores: [store] });
		const card = await screen.findByTestId('report-currency-mix');
		expect(card.textContent).toMatch(/Sales recorded in USD\s*\$0\.00\s*Sales: 0/);
		expect(card.textContent).toMatch(/No USD rate recorded yet/);
		expect(card.textContent).not.toMatch(/USD rates used in this period/);
	});

	it('calculates zakat from the stock value and the typed gold price', async () => {
		api.sales.mockResolvedValue(salesReport([summary('EGP', '114000')]));
		api.stock.mockResolvedValue(stock);
		render(ReportsPage, { params, store: store.store_id, stores: [store] });
		await fireEvent.click(screen.getByRole('tab', { name: 'Stocktaking & zakat' }));
		await screen.findByText('Zakat on trade goods');
		// 600,000.00 at selling price; nisab 85 g × 4,000.00 = 340,000.00 → 2.5% due.
		await fireEvent.input(screen.getByTestId('zakat-gold'), { target: { value: '4000' } });
		expect(screen.getByTestId('zakat-due').textContent).toMatch(/15,000/);
		expect(screen.getByRole('status').textContent).toMatch(/reaches nisab/);
	});
});


// Header scope changes must not reuse another Store's hidden choices or page.
function pagedReport(label = 'Current item'): SalesReport {
 const report = salesReport([summary('EGP', '114000')]);
 report.filter_options.subcategories = [{ id: 'sub1', name_ar: 'فن', name_en: 'Art' }];
 report.sales_total_count = 60;
 report.sales = [{sale_id:'sale-1',sale_number:'MLR-20261110-12345678',occurred_at:'2026-11-10T09:00:00Z',currency:'EGP',products:label,skus:'ML-123456789ABC',root_names_ar:'',root_names_en:'',subcategory_names_ar:'',subcategory_names_en:'',items_sold:1,payment_methods:['card'],cost_minor:'52000',missing_cost_items:0,total_minor:'114000',original_currency:'EGP',original_total_minor:'114000',rate_microrate:null}];
 return report;
}
async function chooseClassifications() {
 for (const [id, value] of [['report-categories','c1'],['report-subcategories','sub1'],['report-tags','t1']]) {
  await fireEvent.click(screen.getByTestId(id));
  await fireEvent.click(document.querySelector<HTMLInputElement>(`#${id}-popup input[data-value="${value}"]`)!);
  await fireEvent.click(screen.getByTestId(id));
 }
}
async function addScopeCost() {
 await fireEvent.click(screen.getByRole('button',{name:/Add cost/}));
 await fireEvent.input(screen.getByLabelText('Short description 1'),{target:{value:'Store A delivery'}});
 await fireEvent.input(screen.getByLabelText(/^Amount 1/),{target:{value:'10'}});
 await fireEvent.click(screen.getByRole('button',{name:'Update report costs'}));
 const profit=screen.getByRole('rowheader',{name:'Net profit after extra costs'}).closest('tr')!;
 expect(within(profit).getByText('610 EGP')).toBeTruthy();
}
async function laterPage() {
 await fireEvent.click(screen.getByRole('tab',{name:'Sales details'}));
 await fireEvent.click(screen.getByRole('button',{name:'Next'}));
 await waitFor(()=>expect(api.sales.mock.lastCall?.[4]).toBe(50));
}
it('clears Store-specific filters, page and temporary costs on Store change while retaining currency and payment',async()=>{
 api.sales.mockResolvedValue(pagedReport());
 const view=render(ReportsPage,{params,store:store.store_id,stores:[store]});
 await screen.findByText('Business totals · EGP');
 await chooseClassifications();
 await choose(screen.getByTestId('report-payment'),'card');
 await choose(screen.getByTestId('report-currency'),'USD');
 await addScopeCost();
 await laterPage();
 const second={...store,store_id:'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb',display_name:'Cairo Shop'};
 await view.rerender({params,store:second.store_id,stores:[store,second]});
 await waitFor(()=>expect(api.sales).toHaveBeenLastCalledWith(params,'USD',{...noFilter,payment_method:'card'},second.store_id,0,50,expect.anything()));
 expect(screen.queryByLabelText('Short description 1')).toBeNull();
 expect(screen.getByText('No extra costs added.')).toBeTruthy();
 for(const label of ['Root categories: Pharaonic ×','Subcategories: Art ×','Tags: Gold ×'])expect(screen.queryByRole('button',{name:label})).toBeNull();
 await fireEvent.click(screen.getByRole('tab',{name:'Overview'}));
 const profit=screen.getByRole('rowheader',{name:'Net profit after extra costs'}).closest('tr')!;
 expect(within(profit).getByText('620 EGP')).toBeTruthy();
});
it.each([
 [{period:'last_10_completed_days'},{period:'today'}],
 [{period:'custom',from_date:'2026-10-17',to_date:'2026-10-26'},{period:'custom',from_date:'2026-10-20',to_date:'2026-10-26'}],
])('resets page and costs when the header period changes, retaining same-Store filters',async(before,after)=>{
 api.sales.mockResolvedValue(pagedReport());
 const view=render(ReportsPage,{params:before,store:store.store_id,stores:[store]});
 await screen.findByText('Business totals · EGP');
 await chooseClassifications();
 await choose(screen.getByTestId('report-payment'),'card');
 await addScopeCost();
 await laterPage();
 await view.rerender({params:after,store:store.store_id,stores:[store]});
 await waitFor(()=>expect(api.sales).toHaveBeenLastCalledWith(after,'EGP',{root_category_ids:['c1'],subcategory_ids:['sub1'],tag_ids:['t1'],payment_method:'card'},store.store_id,0,50,expect.anything()));
 expect(screen.queryByLabelText('Short description 1')).toBeNull();
 expect(screen.getByText('No extra costs added.')).toBeTruthy();
 expect(screen.getByRole('button',{name:'Tags: Gold ×'})).toBeTruthy();
 await fireEvent.click(screen.getByRole('tab',{name:'Overview'}));
 const profit=screen.getByRole('rowheader',{name:'Net profit after extra costs'}).closest('tr')!;
 expect(within(profit).getByText('620 EGP')).toBeTruthy();
});
it('aborts the old scope request and ignores its late result after a Store change',async()=>{
 let finishOld!: (report:SalesReport)=>void;
 api.sales.mockResolvedValueOnce(pagedReport('First Store item')).mockImplementationOnce(()=>new Promise(resolve=>finishOld=resolve)).mockResolvedValue(pagedReport('Second Store item'));
 const view=render(ReportsPage,{params,store:store.store_id,stores:[store]});
 await screen.findByText('Business totals · EGP');
 await laterPage();
 const signal=api.sales.mock.lastCall?.[6] as AbortSignal;
 const second={...store,store_id:'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb',display_name:'Cairo Shop'};
 await view.rerender({params,store:second.store_id,stores:[store,second]});
 await screen.findByText('Second Store item');
 expect(signal.aborted).toBe(true);
 await act(async()=>{finishOld(pagedReport('Late first Store item'));});
 expect(screen.getByText('Second Store item')).toBeTruthy();
 expect(screen.queryByText('Late first Store item')).toBeNull();
});
