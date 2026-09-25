import { describe, expect, it, vi } from 'vitest';
import { render } from '@testing-library/svelte';
import SyncHealthCard from './SyncHealthCard.svelte';
import type { SyncHealth } from '../lib/api.js';

const health = {
	freshness: {
		latest_sale_event_received_at: '2026-09-25T10:00:00Z',
		latest_return_event_received_at: '2026-09-25T11:00:00Z',
		projection_backlog_count: 0,
		blocked_sale_event_count: 0,
		cloud_projection_complete: true,
		return_backlog_count: 0,
		return_blocked_count: 0,
		return_projection_complete: true
	},
	queue_count: 0,
	pending_count: 0,
	processed_count: 0,
	blocked_count: 0,
	retry_count: 0,
	return_pending_count: 0,
	return_processed_count: 1,
	return_blocked_count: 0,
	return_retry_count: 0,
	last_error_code: null,
	last_error_label_ar: null,
	last_error_label_en: null,
	return_last_error_code: null,
	return_last_error_label_ar: null,
	return_last_error_label_en: null
} as unknown as SyncHealth;

const base = { status: 'loaded', errStatus: null, onretry: () => {}, onrefresh: () => {} } as const;

describe('SyncHealthCard compact dual scope (Phase 4B)', () => {
	it('headlines connection with paired sale/return columns, never all-data', () => {
		const { container } = render(SyncHealthCard, { props: { ...base, health, onretry: vi.fn(), onrefresh: vi.fn() } });
		const text = container.textContent ?? '';
		expect(text).toContain('متصل');
		expect(text).toContain('Sales');
		expect(text).toContain('Returns');
		expect(text).toContain('Latest event');
		expect(text).toContain('Backlog');
		expect(text).toContain('Blocked');
		expect(text).not.toContain('جميع البيانات');
		expect(text).not.toContain('all data is up to date');
	});

	it('shows separate sale/return completeness and no Phase 4A exclusion note', () => {
		const { container } = render(SyncHealthCard, { props: { ...base, health, onretry: vi.fn(), onrefresh: vi.fn() } });
		const text = container.textContent ?? '';
		expect(text).toContain('مكتمل / Complete');
		expect(text).not.toContain('المرحلة 4B');
		expect(text).not.toContain('excluded from reporting until Phase 4B');
	});

	it('flags blocked returns without hiding sale completeness', () => {
		const blocked = {
			...health,
			return_blocked_count: 2,
			freshness: { ...health.freshness, return_blocked_count: 2, return_projection_complete: false }
		} as unknown as SyncHealth;
		const { container } = render(SyncHealthCard, { props: { ...base, health: blocked, onretry: vi.fn(), onrefresh: vi.fn() } });
		const text = container.textContent ?? '';
		expect(text).toContain('محظورة / Blocked');
		expect(text).toContain('مكتمل / Complete');
	});

	it('keeps sale and return diagnostics on independent channels', () => {
		const both = {
			...health,
			last_error_code: 'SALE_ID_CONFLICT',
			last_error_label_ar: 'تعارض',
			last_error_label_en: 'conflict',
			return_last_error_code: 'RETURN_REFUND_ID_CONFLICT',
			return_last_error_label_ar: 'تعارض مرتجعات',
			return_last_error_label_en: 'return conflict'
		} as unknown as SyncHealth;
		const { container } = render(SyncHealthCard, { props: { ...base, health: both, onretry: vi.fn(), onrefresh: vi.fn() } });
		const text = container.textContent ?? '';
		expect(text).toContain('تعارض');
		expect(text).toContain('تعارض مرتجعات');
		expect(text).toContain('return conflict');
	});
});
