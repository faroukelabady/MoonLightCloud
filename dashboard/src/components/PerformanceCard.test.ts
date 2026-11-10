import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, render, screen, within } from '@testing-library/svelte';
import PerformanceCard from './PerformanceCard.svelte';
import { setLang } from '../lib/i18n.svelte.js';

vi.mock('../lib/chartAction.js', () => ({ chart: () => ({ destroy() {} }) }));

afterEach(() => {
	cleanup();
	setLang('ar');
});

const trend = {
	titleAr: 'الاتجاه اليومي (إجمالي / مرتجعات / صافي)',
	titleEn: 'Gross / refunds / net over time',
	labels: ['2026-11-01', '2026-11-02'],
	values: [10, 20],
	refundValues: [0, 5],
	netValues: [10, 15],
	exact: [],
	refundExact: [],
	rangeError: false,
	unit: 'EGP',
	status: 'loaded' as const,
	errStatus: null,
	onretry: () => {}
};
const row = (name: string, net: string) => ({ name, units: 1, units_returned: 0, amount_minor: net, refund_minor: '0', net_minor: net });

describe('Sales performance card', () => {
	it('shows the trend and the category breakdown in one card with exact net shares', () => {
		setLang('en');
		const { container } = render(PerformanceCard, {
			props: {
				trend,
				categories: [row('Jewelry', '75000'), row('Papyrus', '25000'), row('Refunded', '-1000')],
				kind: 'root_category',
				onkind: () => {},
				categoryUnit: 'EGP',
				money: 'EGP',
				categoriesStatus: 'loaded',
				categoriesErr: null,
				onretry: () => {}
			}
		});
		expect(container.querySelectorAll('.card')).toHaveLength(1);
		expect(screen.getByText('Sales performance')).toBeTruthy();
		expect(screen.getByTestId('sales-trend-chart')).toBeTruthy();
		const legend = within(screen.getByTestId('category-legend')).getAllByRole('listitem');
		expect(legend.map((li) => li.textContent?.replace(/\s+/g, ' ').trim())).toEqual([
			'Jewelry 750 EGP 75.0%',
			'Papyrus 250 EGP 25.0%',
			'Refunded -10 EGP 0.0%'
		]);
	});

	it('keeps each pane\'s own loading and error state', () => {
		setLang('en');
		render(PerformanceCard, {
			props: {
				trend: { ...trend, status: 'error', errStatus: 500 },
				categories: [],
				kind: 'root_category',
				onkind: () => {},
				categoryUnit: 'EGP',
				money: 'EGP',
				categoriesStatus: 'loaded',
				categoriesErr: null,
				onretry: () => {}
			}
		});
		expect(screen.queryByTestId('sales-trend-chart')).toBeNull();
		expect(screen.getByText('No categories in this period')).toBeTruthy();
	});
});
