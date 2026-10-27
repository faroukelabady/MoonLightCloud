import { describe, expect, it, vi, afterEach, beforeEach } from 'vitest';
import { cleanup, render, screen, fireEvent } from '@testing-library/svelte';
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
		sales_total_count: 0, unclassified_items: 0, detail_offset: 0, detail_limit: 50
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
