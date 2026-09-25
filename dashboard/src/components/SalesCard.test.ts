import { describe, expect, it, vi } from 'vitest';
import { render, screen, fireEvent, within } from '@testing-library/svelte';
import SalesCard from './SalesCard.svelte';

const loaded = {
	summary: { transaction_count: 2, units_sold: 3, return_transaction_count: 1, units_returned: 2, currency_totals: [] },
	normalized: { normalized_total_minor: '386250', normalized_refund_minor: '86250', normalized_net_minor: '300000', transactions: 2, units: 3, return_transactions: 1, units_returned: 2, usd_sale_count: 1 },
	averages: {
		all: { transactions: 3, units: 7, average_minor: '128750' },
		egp: { transactions: 1, units: 2, average_minor: '200000' },
		usd: { transactions: 2, units: 5, average_minor: '1875' }
	},
	fx: { has_usd: true, latest_rate: '52.000000', multiple_rates_used: false }
} as never;

describe('SalesCard', () => {
	it('shows skeleton while loading, never stale numbers', () => {
		render(SalesCard, { props: { data: null, mode: 'all', onmode: () => {}, status: 'loading', errStatus: null, onretry: () => {} } });
		expect(document.querySelector('.skeleton')).toBeTruthy();
		expect(document.body.textContent).not.toContain('386,250');
	});

	it('shows empty state without data', () => {
		render(SalesCard, { props: { data: null, mode: 'all', onmode: () => {}, status: 'empty', errStatus: null, onretry: () => {} } });
		expect(screen.getByText(/لا توجد مبيعات/)).toBeTruthy();
	});

	it('shows error state', async () => {
		const onretry = vi.fn();
		render(SalesCard, { props: { data: null, mode: 'all', onmode: () => {}, status: 'error', errStatus: 503, onretry } });
		expect(screen.getByText(/غير متاحة مؤقتًا/)).toBeTruthy();
		await fireEvent.click(screen.getByText(/إعادة المحاولة/));
		expect(onretry).toHaveBeenCalled();
	});

	it('renders net All total with gross/refund context and switches modes', async () => {
		const onmode = vi.fn();
		const { container } = render(SalesCard, {
			props: { data: loaded, mode: 'all', onmode, status: 'loaded', errStatus: null, onretry: () => {} }
		});
		const q = within(container as HTMLElement);
		// Primary KPI is net (386250 − 86250 = 300000), gross/refunds visible.
		expect(container.textContent).toContain('3,000');
		expect(container.textContent).toContain('3,862.50');
		expect(container.textContent).toContain('862.50');
		expect(q.getByText(/سعر الصرف التاريخي/)).toBeTruthy();
		await fireEvent.click(q.getByText('EGP'));
		expect(onmode).toHaveBeenCalledWith('EGP');
	});
});

describe('SalesCard KPI scope (R08)', () => {
	async function kpis(mode: 'all' | 'EGP' | 'USD') {
		const { container, unmount } = render(SalesCard, {
			props: { data: loaded, mode, onmode: () => {}, status: 'loaded', errStatus: null, onretry: () => {} }
		});
		const text = container.textContent ?? '';
		unmount();
		return text;
	}

	it('All shows all-currency transactions and units', async () => {
		const text = await kpis('all');
		expect(text).toContain('3');
		expect(text).toContain('7');
	});

	it('EGP shows EGP-scoped transactions and units', async () => {
		const { container } = render(SalesCard, {
			props: { data: loaded, mode: 'EGP', onmode: () => {}, status: 'loaded', errStatus: null, onretry: () => {} }
		});
		const values = Array.from(container.querySelectorAll('.kpi-value')).map((e) => e.textContent);
		// transactions KPI = 1, units KPI = 2 (never the all-currency 3/7).
		expect(values[0]).toBe('1');
		expect(values[2]).toBe('2');
	});

	it('renders negative net with a real minus sign, never clamped', () => {
		const neg = {
			summary: { transaction_count: 0, units_sold: 0, return_transaction_count: 1, units_returned: 2, currency_totals: [] },
			normalized: { normalized_total_minor: '1000', normalized_refund_minor: '2500', normalized_net_minor: '-1500', transactions: 0, units: 0, return_transactions: 1, units_returned: 2, usd_sale_count: 0 },
			averages: (loaded as { averages: unknown }).averages,
			fx: { has_usd: false, multiple_rates_used: false }
		} as never;
		const { container } = render(SalesCard, {
			props: { data: neg, mode: 'all', onmode: () => {}, status: 'loaded', errStatus: null, onretry: () => {} }
		});
		expect(container.textContent).toContain('-15');
		expect(container.textContent).not.toContain('0 ج.م\n');
	});

	it('shows empty Refunds as zero with net equal to gross', () => {
		const none = {
			summary: { transaction_count: 1, units_sold: 2, return_transaction_count: 0, units_returned: 0, currency_totals: [] },
			normalized: { normalized_total_minor: '200000', normalized_refund_minor: '0', normalized_net_minor: '200000', transactions: 1, units: 2, return_transactions: 0, units_returned: 0, usd_sale_count: 0 },
			averages: (loaded as { averages: unknown }).averages,
			fx: { has_usd: false, multiple_rates_used: false }
		} as never;
		const { container } = render(SalesCard, {
			props: { data: none, mode: 'all', onmode: () => {}, status: 'loaded', errStatus: null, onretry: () => {} }
		});
		expect(container.textContent).toContain('2,000');
	});
});
