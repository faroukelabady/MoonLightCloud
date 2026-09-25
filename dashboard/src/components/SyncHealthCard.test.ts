import { describe, expect, it, vi } from 'vitest';
import { render } from '@testing-library/svelte';
import SyncHealthCard from './SyncHealthCard.svelte';
import type { SyncHealth } from '../lib/api.js';

const health = {
	freshness: {
		latest_sale_event_received_at: '2026-09-25T10:00:00Z',
		latest_return_event_received_at: '2026-09-25T11:00:00Z',
		blocked_sale_event_count: 0,
		cloud_projection_complete: true,
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
	last_error_label_en: null
} as unknown as SyncHealth;

const base = { status: 'loaded', errStatus: null, onretry: () => {}, onrefresh: () => {} } as const;

describe('SyncHealthCard sale scope (Phase 4A)', () => {
	it('headlines sale projection, never all-data', () => {
		const { container } = render(SyncHealthCard, { props: { ...base, health, onretry: vi.fn(), onrefresh: vi.fn() } });
		const text = container.textContent ?? '';
		expect(text).toContain('إسقاط المبيعات');
		expect(text).toContain('sale projection up to date');
		expect(text).not.toContain('جميع البيانات');
		expect(text).not.toContain('all data is up to date');
	});

	it('shows separate return completeness and no Phase 4A exclusion note', () => {
		const { container } = render(SyncHealthCard, { props: { ...base, health, onretry: vi.fn(), onrefresh: vi.fn() } });
		const text = container.textContent ?? '';
		expect(text).toContain('إسقاط المبيعات السحابي');
		expect(text).toContain('Cloud sale projection complete');
		expect(text).toContain('إسقاط المرتجعات السحابي');
		expect(text).toContain('Cloud return projection complete');
		expect(text).toContain('Latest Return event received');
		expect(text).not.toContain('المرحلة 4B');
		expect(text).not.toContain('excluded from reporting until Phase 4B');
	});

	it('keeps sale-scoped metrics untouched', () => {
		const { container } = render(SyncHealthCard, { props: { ...base, health, onretry: vi.fn(), onrefresh: vi.fn() } });
		const text = container.textContent ?? '';
		expect(text).toContain('Latest Sale event received');
		expect(text).toContain('In queue');
	});

	it('flags blocked returns without hiding sale completeness', () => {
		const blocked = {
			...health,
			return_blocked_count: 2,
			freshness: { ...health.freshness, return_blocked_count: 2, return_projection_complete: false }
		} as unknown as SyncHealth;
		const { container } = render(SyncHealthCard, { props: { ...base, health: blocked, onretry: vi.fn(), onrefresh: vi.fn() } });
		const text = container.textContent ?? '';
		expect(text).toContain('مرتجعات محظورة');
		expect(text).toContain('Blocked returns need review');
		expect(text).toContain('Cloud sale projection complete');
	});
});
