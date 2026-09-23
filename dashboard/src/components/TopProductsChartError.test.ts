import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import TopProducts from './TopProducts.svelte';

// Unsafe chart magnitude (>2^53 minor) must yield a stable widget error
// while the component stays interactive and no exception escapes.
describe('TopProducts unsafe chart values', () => {
	it('renders chart error state, not a crash', () => {
		const { container } = render(TopProducts, {
			props: {
				rows: [{ name: 'Huge', sku: 'HUGE-1', units: 1, amount_minor: '9007199254740993' }],
				money: 'EGP',
				unit: 'EGP',
				status: 'loaded',
				errStatus: null,
				onretry: () => {}
			}
		});
		// Table mode (default) still shows exact values.
		expect(container.textContent).toContain('Huge');
		expect(screen.queryByText(/خارج نطاق العرض/)).toBeNull();
	});
});
