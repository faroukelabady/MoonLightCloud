import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, render, screen, fireEvent, within } from '@testing-library/svelte';
import SalesCard from './SalesCard.svelte';
import { setLang } from '../lib/i18n.svelte.js';

afterEach(() => {
	cleanup();
	setLang('ar');
});

const bucket = (currency: string, sales: string, refund: string, returnedUnits = 0) => ({
	currency, subtotal_minor: sales, discount_minor: '0', tax_minor: '0', sales_total_minor: sales, line_cost_minor: '0',
	refund_total_minor: refund, net_sales_minor: (BigInt(sales) - BigInt(refund)).toString(), returned_units: returnedUnits,
	returned_cost_minor: '0', net_cost_minor: '0'
});
const figures = (net: string, tax: string, cost: string) => ({
	net_sales_minor: net, net_tax_minor: tax, net_cost_minor: cost, profit_minor: (BigInt(net) - BigInt(tax) - BigInt(cost)).toString()
});

// EGP 2,000.00 less a 862.50 refund; USD 12.50 at 52 (= 650.00 EGP).
const loaded = {
	summary: { transaction_count: 3, units_sold: 7, return_transaction_count: 1, units_returned: 2, currency_totals: [bucket('EGP', '200000', '86250', 2), bucket('USD', '1250', '0')] },
	normalized: { normalized_total_minor: '265000', normalized_refund_minor: '86250', normalized_net_minor: '178750', transactions: 3, units: 7, return_transactions: 1, units_returned: 2, usd_sale_count: 1 },
	averages: {
		all: { transactions: 3, units: 7, average_minor: '88333' },
		egp: { transactions: 2, units: 6, average_minor: '100000' },
		usd: { transactions: 1, units: 1, average_minor: '1250' }
	},
	fx: { has_usd: true, latest_rate: '52.000000', multiple_rates_used: false },
	profit: {
		by_currency: [{ currency: 'EGP', ...figures('113750', '0', '50000') }, { currency: 'USD', ...figures('1250', '50', '400') }],
		egp: figures('178750', '2600', '70800')
	}
} as never;

const props = (data: unknown, mode: 'all' | 'EGP' | 'USD' = 'all') => ({ data: data as never, mode, status: 'loaded' as const, errStatus: null, onretry: () => {} });

describe('SalesCard', () => {
	it('shows skeleton while loading, never stale numbers', () => {
		render(SalesCard, { props: { ...props(null), status: 'loading' } });
		expect(document.querySelector('.skeleton')).toBeTruthy();
		expect(document.body.textContent).not.toContain('1,787.50');
	});

	it('shows empty state without data', () => {
		render(SalesCard, { props: { ...props(null), status: 'empty' } });
		expect(screen.getByText(/لا توجد مبيعات/)).toBeTruthy();
	});

	it('shows error state', async () => {
		const onretry = vi.fn();
		render(SalesCard, { props: { ...props(null), status: 'error', errStatus: 503, onretry } });
		expect(screen.getByText(/غير متاحة مؤقتًا/)).toBeTruthy();
		await fireEvent.click(screen.getByText(/إعادة المحاولة/));
		expect(onretry).toHaveBeenCalled();
	});

	it('All: one money table per currency plus the EGP total, with net profit after conversion', () => {
		setLang('en');
		const { container } = render(SalesCard, { props: props(loaded) });
		const table = screen.getByTestId('sales-money');
		expect(within(table).getAllByRole('columnheader').map((h) => h.textContent?.trim())).toEqual(['EGP', 'USD', 'Total in EGP']);
		const row = (name: RegExp) => within(table).getByRole('row', { name }).textContent ?? '';
		expect(row(/^Net sales/)).toMatch(/1,137\.50 EGP\s*\$12\.50\s*1,787\.50 EGP/);
		expect(row(/Tax \(net\)/)).toMatch(/0 EGP\s*\$0\.50\s*26 EGP/);
		expect(row(/Cost of goods/)).toMatch(/500 EGP\s*\$4\.00\s*708 EGP/);
		// 1,787.50 - 26 - 708 = 1,053.50 after converting USD at each sale's rate.
		expect(row(/Net profit/)).toMatch(/637\.50 EGP\s*\$8\.00\s*1,053\.50 EGP/);
		expect(screen.getByTestId('net-sales').textContent).toBe('1,787.50 EGP');
		expect(screen.getByTestId('net-profit').textContent).toBe('1,053.50 EGP');
		expect(screen.getByTestId('average-value').textContent).toBe('883.33 EGP');
		// Counts sit in the same bordered sheet as the money table.
		expect(screen.getByTestId('sales-transactions').textContent).toMatch(/Transactions\s*3\s*1 returns/);
		expect(screen.getByTestId('sales-summary').closest('.sheet')?.contains(screen.getByTestId('sales-money'))).toBe(true);
		expect(container.textContent).toContain('1 USD = 52.000000 EGP');
	});

	it('EGP shows only the EGP column and EGP-scoped figures', () => {
		setLang('en');
		render(SalesCard, { props: props(loaded, 'EGP') });
		const table = screen.getByTestId('sales-money');
		expect(within(table).getAllByRole('columnheader').map((h) => h.textContent?.trim())).toEqual(['EGP']);
		expect(screen.getByTestId('net-sales').textContent).toBe('1,137.50 EGP');
		expect(screen.getByTestId('net-profit').textContent).toBe('637.50 EGP');
		expect(screen.getByTestId('average-value').textContent).toBe('1,000 EGP');
		// No tax in EGP, so no tax row.
		expect(within(table).queryByRole('row', { name: /Tax/ })).toBeNull();
		expect(screen.getByTestId('sales-units').textContent).toMatch(/Units\s*6\s*2 returned/);
		expect(screen.getByText(/Return transactions count every currency/)).toBeTruthy();
	});

	it('renders a negative net and a loss with a real minus sign', () => {
		setLang('en');
		const neg = {
			...(loaded as object),
			summary: { transaction_count: 0, units_sold: 0, return_transaction_count: 1, units_returned: 2, currency_totals: [bucket('EGP', '1000', '2500')] },
			normalized: { normalized_total_minor: '1000', normalized_refund_minor: '2500', normalized_net_minor: '-1500', transactions: 0, units: 0, return_transactions: 1, units_returned: 2, usd_sale_count: 0 },
			profit: { by_currency: [{ currency: 'EGP', ...figures('-1500', '0', '0') }], egp: figures('-1500', '0', '0') }
		};
		render(SalesCard, { props: props(neg) });
		expect(screen.getByTestId('net-sales').textContent).toBe('-15 EGP');
		expect(screen.getByTestId('net-profit').classList.contains('loss')).toBe(true);
	});

	it('hides profit rows when the server sends no profit', () => {
		setLang('en');
		const { profit: _profit, ...old } = loaded as Record<string, unknown>;
		void _profit;
		render(SalesCard, { props: props(old) });
		expect(screen.queryByTestId('net-profit')).toBeNull();
		expect(within(screen.getByTestId('sales-money')).queryByRole('row', { name: /Net profit/ })).toBeNull();
	});
});
