import { render } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import TagCard from './TagCard.svelte';
import OnlineComparison from './OnlineComparison.svelte';
import CatalogHealthCard from './CatalogHealthCard.svelte';
import type { TagListResponse, OrderAnalyticsResponse, CatalogHealthResponse } from '../lib/api.js';

vi.mock('../lib/chartAction.js', () => ({ chart: () => ({ destroy() {} }) }));

const tags: TagListResponse = {
	generated_at: '2026-10-03T00:00:00Z',
	timezone: 'Africa/Cairo',
	store_id: null,
	overlap_note: 'Tag totals overlap and must not be summed to derive total business revenue.',
	rows: [
		{
			tag_id: '11111111-1111-4111-8111-111111111111',
			tag_slug: 'gold',
			name_ar: 'ذهبي',
			name_en: 'Gold',
			units: 2,
			units_returned: 0,
			currencies: [{ currency: 'EGP', line_sales_minor: '9007199254740993', line_refund_minor: '0', net_minor: '9007199254740993' }]
		}
	]
};

const online: OrderAnalyticsResponse = {
	generated_at: '2026-10-03T00:00:00Z',
	store_id: null,
	provider_key: '',
	active_statuses: ['PENDING', 'PROCESSING', 'ON_HOLD', 'COMPLETED'],
	currency_totals: [{ currency: 'EGP', orders: 1, value_minor: '100', active_orders: 1, active_value_minor: '100' }],
	status_counts: [{ canonical_status: 'CANCELLED', orders: 1 }],
	provider_totals: [{ provider_key: 'shopify-main', currency: 'EGP', orders: 1, value_minor: '100' }]
};

const health: CatalogHealthResponse = {
	generated_at: '2026-10-03T00:00:00Z',
	store_id: null,
	provider_key: '',
	reason_codes: ['VARIANT_MISSING_SKU'],
	counts: [{ reason_code: 'VARIANT_MISSING_SKU', products: 0 }],
	detail: [],
	detail_limit: 50,
	detail_truncated: false
};

// §25: Tag overlap disclosure is mandatory and rendered.
// §138: >2^53 money renders exactly from the string representation.
describe('Phase12 widgets', () => {
	it('TagCard discloses non-additive overlap and renders >2^53 money exactly', () => {
		const { container } = render(TagCard, {
			props: { data: tags, status: 'loaded', errStatus: null, onretry: () => {}, currency: 'EGP' }
		});
		const text = container.textContent ?? '';
		expect(text).toMatch(/must not be summed to derive total business revenue/i);
		// 9007199254740993 minor units must appear exactly (BigInt formatting).
		expect(text).toMatch(/90[,.]?071[,.]?992[,.]?547[,.]?409\.93/);
	});

	it('OnlineComparison separates sources and carries the double-count warning', () => {
		const { container } = render(OnlineComparison, {
			props: { online, retail: null, status: 'loaded', errStatus: null, onretry: () => {} }
		});
		const text = container.textContent ?? '';
		expect(text).toMatch(/Finalized Retail Sales/);
		expect(text).toMatch(/Online Orders/);
		expect(text).toMatch(/may not represent additional recognized revenue beyond finalized Retail Sales/i);
		// Cancelled orders stay visible in the status breakdown.
		expect(text).toMatch(/CANCELLED/);
	});

	it('CatalogHealthCard is diagnostic-only and shows stable reason codes', () => {
		const { container } = render(CatalogHealthCard, {
			props: {
				data: health,
				status: 'loaded',
				errStatus: null,
				onretry: () => {},
				provider: '',
				onprovider: () => {}
			}
		});
		const text = container.textContent ?? '';
		expect(text).toMatch(/VARIANT_MISSING_SKU/);
		expect(text).toMatch(/Diagnostic only/);
	});
});
