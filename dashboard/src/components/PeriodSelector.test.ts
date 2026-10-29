import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, render, screen, fireEvent } from '@testing-library/svelte';
import PeriodSelector from './PeriodSelector.svelte';

afterEach(() => cleanup());

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

	it('always shows the date fields, editable only for a custom range', async () => {
		const onchange = vi.fn();
		const range = { from: '2026-10-18', to: '2026-10-27' };
		const { rerender } = render(PeriodSelector, {
			props: { params: { period: 'last_10_completed_days' }, timezone: 'Africa/Cairo', onchange, range }
		});
		// A preset shows its own dates, disabled, with Show disabled too.
		const from = screen.getByLabelText(/من/) as HTMLInputElement;
		expect(from.disabled).toBe(true);
		expect(from.value).toBe('2026-10-18');
		expect((screen.getByLabelText(/إلى/) as HTMLInputElement).value).toBe('2026-10-27');
		expect((screen.getByText(/عرض/).closest('button') as HTMLButtonElement).disabled).toBe(true);
		await rerender({ params: { period: 'custom' }, timezone: 'Africa/Cairo', onchange, range });
		expect((screen.getByLabelText(/من/) as HTMLInputElement).disabled).toBe(false);
		expect((screen.getByText(/عرض/).closest('button') as HTMLButtonElement).disabled).toBe(false);
	});

	it('starts a custom range from the dates on screen', async () => {
		const onchange = vi.fn();
		render(PeriodSelector, {
			props: { params: { period: 'today' }, timezone: 'Africa/Cairo', onchange, range: { from: '2026-10-29', to: '2026-10-29' } }
		});
		await fireEvent.click(screen.getByText('نطاق مخصص'));
		expect((screen.getByLabelText(/من/) as HTMLInputElement).value).toBe('2026-10-29');
		expect(onchange).not.toHaveBeenCalled();
	});
});

describe('PeriodSelector URL sync (L01)', () => {
	it('synchronizes inputs when params change (back/forward) without clobbering typing', async () => {
		const onchange = vi.fn();
		const { rerender } = render(PeriodSelector, {
			props: { params: { period: 'custom', from_date: '2026-09-20', to_date: '2026-09-21' }, timezone: 'Africa/Cairo', onchange }
		});
		expect((screen.getByLabelText(/من/) as HTMLInputElement).value).toBe('2026-09-20');
		// Simulate browser Back: params change -> inputs follow.
		await rerender({ params: { period: 'custom', from_date: '2026-09-22', to_date: '2026-09-23' }, timezone: 'Africa/Cairo', onchange });
		expect((screen.getByLabelText(/من/) as HTMLInputElement).value).toBe('2026-09-22');
		expect((screen.getByLabelText(/إلى/) as HTMLInputElement).value).toBe('2026-09-23');
	});
});

describe('PeriodSelector draft/apply (R07)', () => {
	it('clicking Custom reveals inputs without requesting', async () => {
		const onchange = vi.fn();
		render(PeriodSelector, {
			props: { params: { period: 'today' }, timezone: 'Africa/Cairo', onchange }
		});
		await fireEvent.click(screen.getByText('نطاق مخصص'));
		expect(screen.getByLabelText(/من/)).toBeTruthy();
		expect(onchange).not.toHaveBeenCalled();
	});

	it('Apply without dates commits nothing and shows an error', async () => {
		const onchange = vi.fn();
		render(PeriodSelector, {
			props: { params: { period: 'today' }, timezone: 'Africa/Cairo', onchange }
		});
		await fireEvent.click(screen.getByText('نطاق مخصص'));
		await fireEvent.click(screen.getByText(/عرض/));
		expect(onchange).not.toHaveBeenCalled();
		expect(screen.getByRole('alert')).toBeTruthy();
	});
});
