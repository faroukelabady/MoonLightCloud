import { describe, expect, it, vi } from 'vitest';
import { render, screen, fireEvent, within } from '@testing-library/svelte';
import SalesCard from './SalesCard.svelte';

const loaded = {
	summary: { transaction_count: 2, units_sold: 3, currency_totals: [] },
	normalized: { normalized_total_minor: '386250', transactions: 2, units: 3, usd_sale_count: 1 },
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

	it('renders normalized All total with helper text and switches modes', async () => {
		const onmode = vi.fn();
		const { container } = render(SalesCard, {
			props: { data: loaded, mode: 'all', onmode, status: 'loaded', errStatus: null, onretry: () => {} }
		});
		const q = within(container as HTMLElement);
		expect(container.textContent).toContain('3,862.50');
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
});
