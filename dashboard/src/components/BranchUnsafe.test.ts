import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import BranchCard from './BranchCard.svelte';

// R02: unsafe magnitudes must render exact text (the card has no bars).
describe('BranchCard unsafe values', () => {
	const rows = [
		{ shop_name_ar: 'ضخم', shop_name_en: 'Huge', shop_phone: '0', channel: 'STORE', currency: 'EGP', transactions: 1, units: 1, return_transactions: 0, units_returned: 0, subtotal_minor: '9007199254740993', sales_total_minor: '9007199254740993', refund_total_minor: '0', returned_cost_minor: '0' },
		{ shop_name_ar: 'صغير', shop_name_en: 'Tiny', shop_phone: '0', channel: 'STORE', currency: 'EGP', transactions: 1, units: 1, return_transactions: 0, units_returned: 0, subtotal_minor: '100', sales_total_minor: '100', refund_total_minor: '0', returned_cost_minor: '0' }
	];

	it('keeps exact text for >2^53 amounts', () => {
		const { container } = render(BranchCard, {
			props: { rows, status: 'loaded', errStatus: null, onretry: () => {} }
		});
		expect(container.textContent).toContain('90,071,992,547,409.93');
		expect(container.querySelector('.fill, .track')).toBeNull();
		expect(screen.queryByText(/^0$/)).toBeNull();
	});
});
