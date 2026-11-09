import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, within } from '@testing-library/svelte';
import SyncHealthCard from './SyncHealthCard.svelte';
import { setLang } from '../lib/i18n.svelte.js';
import type { SyncHealth, SyncSummary } from '../lib/api.js';

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
	pending_count: 1,
	processed_count: 0,
	blocked_count: 0,
	retry_count: 1,
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

const now = Date.now();
const ago = (minutes: number) => new Date(now - minutes * 60_000).toISOString();
const summary: SyncSummary = {
	categories: [
		{ category: 'sales', synced: 12, received_last_7_days: 12, total_received: 12, last_received_at: ago(3) },
		{ category: 'returns', synced: 1, received_last_7_days: 1, total_received: 1, last_received_at: ago(30) },
		{ category: 'catalog', synced: 40, received_last_7_days: 40, total_received: 40, last_received_at: ago(12) },
		{ category: 'stock', synced: 0, received_last_7_days: 0, total_received: 0 }
	],
	total_synced: 53,
	last_received_at: ago(3)
};

const base = { status: 'loaded', errStatus: null, onretry: () => {}, onsync: () => {} } as const;

afterEach(() => cleanup());

describe('SyncHealthCard', () => {
	it('shows each kind with its period count and newest arrival', () => {
		setLang('en');
		render(SyncHealthCard, { props: { ...base, health, summary } });
		const rows = within(screen.getByTestId('sync-categories')).getAllByRole('listitem');
		expect(rows.map((r) => r.querySelector('.name')?.textContent)).toEqual(['Sales', 'Returns', 'Products', 'Stock']);
		expect(rows[0].textContent).toContain('12 events received in the last 7 days');
		expect(rows[0].textContent).toContain('3 minutes ago');
		expect(rows[3].textContent).toContain('Not received');
	});

	it('shows useful weekly activity even when the selected completed period has no arrivals', () => {
		setLang('en');
		render(SyncHealthCard, { props: { ...base, health, summary: { ...summary, categories: [{ category: 'sales', synced: 0, received_last_7_days: 36, total_received: 50, last_received_at: ago(3) }] } } });
		const row = within(screen.getByTestId('sync-categories')).getByRole('listitem');
		expect(row.textContent).toContain('36 events received in the last 7 days');
		expect(row.textContent).not.toContain('selected period');
		const details = screen.getByTestId('sync-details') as HTMLDetailsElement;
		expect(details.open).toBe(false);
		expect(details.textContent).toContain('Selected period');
		expect(details.textContent).toContain('50');
	});

	it('shows the last sync and the queue in the footer', () => {
		setLang('en');
		const { container } = render(SyncHealthCard, { props: { ...base, health, summary } });
		const foot = container.querySelector('.foot')?.textContent ?? '';
		expect(foot).toContain('Last sync');
		expect(foot).toContain('3 minutes ago');
		expect(foot).toContain('2 items');
		expect(foot).not.toContain('Blocked');
	});

	it('never claims that every shop record arrived', () => {
		setLang('en');
		render(SyncHealthCard, { props: { ...base, health, summary } });
		const hero = screen.getByTestId('sync-hero');
		expect(hero.dataset.level).toBe('ok');
		expect(hero.textContent).toContain('Cloud has processed all data that it received.');
		expect(hero.textContent).not.toContain('all data is up to date');
	});

	it('flags blocked returns on their row, in the footer and with the error', () => {
		setLang('en');
		const blocked = {
			...health,
			return_last_error_code: 'RETURN_REFUND_ID_CONFLICT',
			return_last_error_label_ar: 'تعارض مرتجعات',
			return_last_error_label_en: 'Return conflict',
			freshness: { ...health.freshness, return_blocked_count: 2, return_projection_complete: false }
		} as unknown as SyncHealth;
		const { container } = render(SyncHealthCard, { props: { ...base, health: blocked, summary } });
		expect(screen.getByTestId('sync-hero').dataset.level).toBe('blocked');
		const rows = within(screen.getByTestId('sync-categories')).getAllByRole('listitem');
		expect(rows[0].querySelector('.dot.bad')).toBeNull();
		expect(rows[1].querySelector('.dot.bad')).not.toBeNull();
		expect(container.querySelector('.tile.bad')?.textContent).toContain('2');
		expect(container.textContent).toContain('Last error: Return conflict');
	});

	it('shows Arabic only when Arabic is chosen', () => {
		setLang('ar');
		const { container } = render(SyncHealthCard, { props: { ...base, health, summary, canSync: true } });
		const text = container.textContent ?? '';
		for (const label of ['المبيعات', 'المرتجعات', 'المنتجات', 'المخزون', 'آخر مزامنة', 'في الانتظار', 'مزامنة الآن']) expect(text).toContain(label);
		for (const english of ['Sales', 'Returns', 'Last sync', 'In queue', 'Sync now']) expect(text).not.toContain(english);
	});

	it('sends a sync when allowed, and refreshes otherwise', async () => {
		setLang('en');
		const onsync = vi.fn();
		const { unmount } = render(SyncHealthCard, { props: { ...base, health, summary, canSync: true, onsync } });
		await fireEvent.click(screen.getByRole('button', { name: /Sync now/ }));
		expect(onsync).toHaveBeenCalledTimes(1);
		unmount();
		render(SyncHealthCard, { props: { ...base, health, summary, canSync: false, onsync } });
		expect(screen.getByTestId('sync-now').textContent).toContain('Refresh');
	});

	it('keeps the health view when the period counts are not available', () => {
		setLang('en');
		render(SyncHealthCard, { props: { ...base, health, summary: null } });
		expect(screen.queryByTestId('sync-categories')).toBeNull();
		expect(screen.getByText('Period counts are not available.')).toBeTruthy();
	});
});

describe('SyncHealthCard errors', () => {
	it('keeps sale and return errors on separate lines', () => {
		setLang('en');
		const both = {
			...health,
			last_error_code: 'SALE_ID_CONFLICT',
			last_error_label_en: 'Sale conflict',
			return_last_error_code: 'RETURN_REFUND_ID_CONFLICT',
			return_last_error_label_en: 'Return conflict'
		} as unknown as SyncHealth;
		const { container } = render(SyncHealthCard, { props: { ...base, health: both, summary } });
		const lines = [...container.querySelectorAll('.errmsg')].map((e) => e.textContent);
		expect(lines).toEqual(['Last error: Sale conflict', 'Last error: Return conflict']);
	});
});
