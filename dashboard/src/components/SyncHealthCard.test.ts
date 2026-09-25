import { describe, expect, it, vi } from 'vitest';
import { render } from '@testing-library/svelte';
import SyncHealthCard from './SyncHealthCard.svelte';
import type { SyncHealth } from '../lib/api.js';

const health = {
	freshness: {
		latest_sale_event_received_at: '2026-09-25T10:00:00Z',
		blocked_sale_event_count: 0,
		cloud_projection_complete: true
	},
	queue_count: 0,
	pending_count: 0,
	processed_count: 0,
	blocked_count: 0,
	retry_count: 0,
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

	it('marks the complete badge sale-scoped and discloses the return boundary', () => {
		const { container } = render(SyncHealthCard, { props: { ...base, health, onretry: vi.fn(), onrefresh: vi.fn() } });
		const text = container.textContent ?? '';
		expect(text).toContain('إسقاط المبيعات السحابي');
		expect(text).toContain('Cloud sale projection complete');
		expect(text).toContain('المرحلة 4B');
		expect(text).toContain('Phase 4B');
	});

	it('keeps sale-scoped metrics untouched', () => {
		const { container } = render(SyncHealthCard, { props: { ...base, health, onretry: vi.fn(), onrefresh: vi.fn() } });
		const text = container.textContent ?? '';
		expect(text).toContain('Latest Sale event received');
		expect(text).toContain('In queue');
	});
});
