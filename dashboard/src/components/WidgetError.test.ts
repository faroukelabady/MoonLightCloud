import { describe, expect, it, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import WidgetError from './WidgetError.svelte';

describe('WidgetError', () => {
	it('shows retry affordance on 503', async () => {
		const onretry = vi.fn();
		render(WidgetError, { props: { status: 503, onretry } });
		expect(screen.getByText(/غير متاحة مؤقتًا/)).toBeTruthy();
		await fireEvent.click(screen.getByText(/إعادة المحاولة/));
		expect(onretry).toHaveBeenCalledTimes(1);
	});

	it('shows generic error otherwise', () => {
		const onretry = vi.fn();
		render(WidgetError, { props: { status: 500, onretry } });
		expect(screen.getByText(/تعذر تحميل البيانات/)).toBeTruthy();
	});
});
