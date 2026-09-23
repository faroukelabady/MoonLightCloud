import { describe, expect, it, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import PeriodSelector from './PeriodSelector.svelte';

describe('PeriodSelector', () => {
	it('renders all four periods Arabic-first and selects', async () => {
		const onchange = vi.fn();
		render(PeriodSelector, {
			props: {
				params: { period: 'last_10_completed_days' },
				timezone: 'Africa/Cairo',
				onchange
			}
		});
		expect(screen.getByText('آخر 10 أيام')).toBeTruthy();
		expect(screen.getByText('أمس')).toBeTruthy();
		expect(screen.getByText('اليوم')).toBeTruthy();
		expect(screen.getByText('نطاق مخصص')).toBeTruthy();
		expect(screen.getByText(/Africa\/Cairo/)).toBeTruthy();
		await fireEvent.click(screen.getByText('أمس'));
		expect(onchange).toHaveBeenCalledWith(expect.objectContaining({ period: 'yesterday' }));
	});

	it('shows custom date inputs only in custom mode', async () => {
		const onchange = vi.fn();
		const { rerender } = render(PeriodSelector, {
			props: { params: { period: 'today' }, timezone: 'Africa/Cairo', onchange }
		});
		expect(screen.queryByLabelText(/من/)).toBeNull();
		await rerender({ params: { period: 'custom' }, timezone: 'Africa/Cairo', onchange });
		expect(screen.getByLabelText(/من/)).toBeTruthy();
		expect(screen.getByLabelText(/إلى/)).toBeTruthy();
	});
});
