import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('../lib/chartAction.js', () => ({
	chart: () => ({ destroy() {} })
}));

beforeEach(() => {
	vi.stubGlobal(
		'ResizeObserver',
		class {
			observe() {}
			unobserve() {}
			disconnect() {}
		}
	);
});
import { render, screen } from '@testing-library/svelte';
import TrendChart from './TrendChart.svelte';

const base = {
	titleAr: 'الاتجاه اليومي للمبيعات',
	titleEn: 'Sales over time',
	labels: [] as string[],
	values: [] as number[],
	unit: 'EGP normalized',
	errStatus: null as number | null,
	onretry: () => {}
};

// R03: unsafe chart values produce the range state (not a load error)
// while exact daily values remain listed.
describe('TrendChart range state', () => {
	it('shows range error plus exact fallback values', () => {
		const { container } = render(TrendChart, {
			props: {
				...base,
				status: 'loaded',
				rangeError: true,
				exact: [{ date: '2026-09-20', amount_minor: '9007199254740993' }]
			}
		});
		expect(screen.getByTestId('sales-trend-range-error')).toBeTruthy();
		expect(container.textContent).toContain('9007199254740993');
		expect(container.querySelector('[data-testid="sales-trend-chart"]')).toBeNull();
	});

	it('renders the chart when values are safe', () => {
		render(TrendChart, {
			props: { ...base, status: 'loaded', rangeError: false, exact: [], labels: ['2026-09-20'], values: [100] }
		});
		expect(screen.getByTestId('sales-trend-chart')).toBeTruthy();
	});
});
