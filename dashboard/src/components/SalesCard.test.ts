import { describe, expect, it, vi } from 'vitest';
import { render, screen, fireEvent, within } from '@testing-library/svelte';
import SalesCard from './SalesCard.svelte';

const loaded = {
	summary: { transaction_count: 2, units_sold: 3, currency_totals: [] },
	normalized: { normalized_total_minor: '386250', transactions: 2, units: 3, usd_sale_count: 1 },
	fx: { has_usd: true, latest_rate: '52.000000', multiple_rates_used: false }
} as never;

describe('SalesCard', () => {
	it('shows skeleton while loading, never stale numbers', () => {
		render(SalesCard, { props: { data: null, mode: 'all', onmode: () => {}, status: 'loading' } });
		expect(document.querySelector('.skeleton')).toBeTruthy();
		expect(document.body.textContent).not.toContain('386,250');
	});

	it('shows empty state without data', () => {
		render(SalesCard, { props: { data: null, mode: 'all', onmode: () => {}, status: 'empty' } });
		expect(screen.getByText(/لا توجد مبيعات/)).toBeTruthy();
	});

	it('shows error state', () => {
		render(SalesCard, { props: { data: null, mode: 'all', onmode: () => {}, status: 'error' } });
		expect(screen.getByText(/تعذر تحميل البيانات/)).toBeTruthy();
	});

	it('renders normalized All total with helper text and switches modes', async () => {
		const onmode = vi.fn();
		const { container } = render(SalesCard, {
			props: { data: loaded, mode: 'all', onmode, status: 'loaded' }
		});
		const q = within(container as HTMLElement);
		expect(container.textContent).toContain('3,862.50');
		expect(q.getByText(/سعر الصرف التاريخي/)).toBeTruthy();
		await fireEvent.click(q.getByText('EGP'));
		expect(onmode).toHaveBeenCalledWith('EGP');
	});
});
