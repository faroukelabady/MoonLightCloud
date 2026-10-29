import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, render, screen, waitFor, within } from '@testing-library/svelte';
import App from '../App.svelte';
import Sidebar from './Sidebar.svelte';
import CountsTable from './CountsTable.svelte';
import BranchCard from './BranchCard.svelte';
import SyncHealthCard from './SyncHealthCard.svelte';
import { dashboardApi } from '../lib/api.js';
import type { BranchRow, OverviewResponse, SyncHealth } from '../lib/api.js';
import { setLang } from '../lib/i18n.svelte.js';

vi.mock('../lib/chartAction.js', () => ({ chart: () => ({ destroy() {} }) }));

afterEach(() => {
	cleanup();
	vi.restoreAllMocks();
	setLang('ar');
});

const overview = {
	summary: { transaction_count: 482, units_sold: 1274, return_transaction_count: 7, units_returned: 9, currency_totals: [] },
	averages: {
		all: { transactions: 482, units: 1274, average_minor: '0' },
		egp: { transactions: 400, units: 1000, average_minor: '0' },
		usd: { transactions: 82, units: 274, average_minor: '0' }
	}
} as unknown as OverviewResponse;

describe('Transactions and units table', () => {
	it('puts sale and return transactions and units in one table', () => {
		setLang('en');
		render(CountsTable, { props: { data: overview, mode: 'all' } });
		const table = screen.getByTestId('activity-totals');
		expect(within(table).getAllByRole('columnheader').map((h) => h.textContent)).toEqual(['Sales', 'Returns']);
		expect(within(table).getByRole('row', { name: /Transactions/ }).textContent).toMatch(/Transactions\s*482\s*7/);
		expect(within(table).getByRole('row', { name: /Units/ }).textContent).toMatch(/Units\s*1,274\s*9/);
		expect(screen.getByText('Scope: all')).toBeTruthy();
	});

	it('scopes sales to the currency mode and keeps returns whole-period', () => {
		setLang('en');
		render(CountsTable, { props: { data: overview, mode: 'USD' } });
		const table = screen.getByTestId('activity-totals');
		expect(within(table).getByRole('row', { name: /Transactions/ }).textContent).toMatch(/82\s*7/);
		expect(screen.getByText('Scope: USD')).toBeTruthy();
	});
});

describe('Branch card', () => {
	const row = (currency: string, sales: string, transactions: number): BranchRow => ({
		shop_name_ar: 'الأقصر', shop_name_en: 'Luxor', shop_phone: '0', channel: 'STORE', currency, transactions, units: transactions * 2,
		return_transactions: 1, units_returned: 1, subtotal_minor: sales, sales_total_minor: sales, refund_total_minor: '0', returned_cost_minor: '0'
	});

	it('shows one row per branch: sales per currency, then transactions and units, with no bars', () => {
		setLang('en');
		const rows = [row('EGP', '1245800', 12), row('USD', '25000', 3), { ...row('EGP', '9900', 1), shop_name_ar: 'أسوان', shop_name_en: 'Aswan' }];
		const { container } = render(BranchCard, { props: { rows, status: 'loaded', errStatus: null, onretry: () => {} } });
		const table = screen.getByTestId('branch-table');
		expect(within(table).getAllByRole('columnheader').map((h) => h.textContent?.trim())).toEqual(['Branch / Channel', 'Sales EGP', 'Sales USD', 'Transactions', 'Units']);
		const bodyRows = within(table).getAllByRole('row').slice(1);
		expect(bodyRows).toHaveLength(2);
		// Counts add up across the currencies: 12 + 3 transactions, 24 + 6 units.
		expect(bodyRows[0].textContent).toMatch(/Luxor.*12,458 EGP.*\$250\.00.*15.*30/);
		// A branch without USD sales says so instead of showing zero.
		expect(within(bodyRows[1]).getAllByRole('cell')[1].textContent?.trim()).toBe('—');
		expect(container.querySelector('.fill, .track')).toBeNull();
	});

	it('follows the currency mode from the top bar', () => {
		setLang('en');
		render(BranchCard, { props: { rows: [row('EGP', '1245800', 12), row('USD', '25000', 3)], status: 'loaded', errStatus: null, onretry: () => {}, mode: 'USD' } });
		const table = screen.getByTestId('branch-table');
		expect(within(table).getAllByRole('columnheader').map((h) => h.textContent?.trim())).toEqual(['Branch / Channel', 'Sales USD', 'Transactions', 'Units']);
		expect(within(table).getAllByRole('row')[1].textContent).toMatch(/\$250\.00\s*3\s*6/);
	});

	it('holds the online comparison in the same card', () => {
		setLang('en');
		const online = { generated_at: '', store_id: null, provider_key: '', active_statuses: [], currency_totals: [{ currency: 'EGP', orders: 2, value_minor: '4400', active_orders: 1, active_value_minor: '2200' }], status_counts: [], provider_totals: [] };
		render(BranchCard, { props: { rows: [row('EGP', '100', 1)], status: 'loaded', errStatus: null, onretry: () => {}, online: online as never, onlineStatus: 'loaded' } });
		expect(screen.getByRole('heading', { name: /Finalized sales vs online orders/ })).toBeTruthy();
		expect(screen.getByLabelText('Online Orders').textContent).toContain('order value EGP');
	});
});

describe('Sync health headline', () => {
	const health = (freshness: Partial<SyncHealth['freshness']>) =>
		({
			freshness: { projection_backlog_count: 0, blocked_sale_event_count: 0, cloud_projection_complete: true, return_backlog_count: 0, return_blocked_count: 0, return_projection_complete: true, ...freshness },
			pending_count: 0, retry_count: 0, return_pending_count: 0, return_retry_count: 0
		}) as unknown as SyncHealth;
	const base = { status: 'loaded', errStatus: null, onretry: () => {}, onrefresh: () => {} } as const;

	it.each([
		[{}, 'ok', 'Connected · sales & returns up to date'],
		[{ cloud_projection_complete: false, projection_backlog_count: 4 }, 'catching', 'Connected · catching up'],
		[{ return_blocked_count: 1, return_projection_complete: false }, 'blocked', 'Needs attention · blocked events']
	])('reports the real state (%o)', (freshness, level, text) => {
		setLang('en');
		render(SyncHealthCard, { props: { ...base, health: health(freshness) } });
		const hero = screen.getByTestId('sync-hero');
		expect(hero.dataset.level).toBe(level);
		expect(hero.textContent).toContain(text);
	});
});

describe('Sync health card', () => {
	it('shows no shop configured yet on a yellow headline when no Store exists', () => {
		setLang('en');
		render(SyncHealthCard, { props: { health: null, status: 'loaded', errStatus: null, onretry: () => {}, onrefresh: () => {}, noShop: true } });
		const hero = screen.getByTestId('sync-hero');
		expect(hero.dataset.level).toBe('noshop');
		expect(hero.textContent).toContain('No shop configured yet');
		expect(screen.queryByTestId('sync-table')).toBeNull();
	});

	it('lays the sync figures out as a table of sales and returns', () => {
		setLang('en');
		const health = { freshness: { projection_backlog_count: 2, blocked_sale_event_count: 0, cloud_projection_complete: false, return_backlog_count: 0, return_blocked_count: 0, return_projection_complete: true }, pending_count: 1, retry_count: 1, return_pending_count: 0, return_retry_count: 0 } as unknown as SyncHealth;
		render(SyncHealthCard, { props: { health, status: 'loaded', errStatus: null, onretry: () => {}, onrefresh: () => {} } });
		const table = screen.getByTestId('sync-table');
		expect(within(table).getAllByRole('columnheader').map((h) => h.textContent?.trim())).toEqual(['Sales', 'Returns']);
		expect(within(table).getAllByRole('rowheader').map((h) => h.textContent?.trim())).toEqual(['Latest event', 'Backlog', 'Blocked', 'Status']);
		expect(within(table).getByRole('row', { name: /Backlog/ }).textContent).toMatch(/2\s*1 pending · 1 retry/);
	});
});

describe('Overview', () => {
	it('drops the Sales Summary and Categories & Tags pages from the sidebar', () => {
		setLang('en');
		render(Sidebar, { props: { route: 'overview', navigate: () => {} } });
		expect(screen.queryByRole('button', { name: 'Sales Summary' })).toBeNull();
		expect(screen.queryByRole('button', { name: 'Categories & Tags' })).toBeNull();
		expect(screen.getByRole('button', { name: 'Reports' })).toBeTruthy();
	});

	it('opens a former page as the overview, sync health first, with the currency switch in the top bar', async () => {
		setLang('en');
		window.history.replaceState({}, '', '/dashboard/categories?currency=all');
		for (const k of Object.keys(dashboardApi)) vi.spyOn(dashboardApi, k as any).mockImplementation((async () => ({})) as any);
		vi.mocked(dashboardApi.me).mockResolvedValue({ authenticated: true, stage: 'full', csrf_token: 't', user: { id: 'u', login: 'o@test', display_name: 'Owner', role: 'owner', mfa_enabled: true }, permissions: ['reports.read'], all_stores: true, store_ids: [], session: { expires_at: '', idle_timeout_seconds: 1800 } } as never);
		vi.mocked(dashboardApi.stores).mockResolvedValue({ stores: [] });
		vi.mocked(dashboardApi.overview).mockResolvedValue({ period: { start_local: '2026-10-03', end_local_exclusive: '2026-10-04' }, summary: { transaction_count: 0, return_transaction_count: 0, currency_totals: [] }, normalized: {}, averages: {}, fx: {} } as never);
		vi.mocked(dashboardApi.daily).mockResolvedValue({ days: [], display_currency: 'EGP', normalized: false } as never);
		for (const k of ['products', 'categories', 'branches'] as const) vi.mocked(dashboardApi[k]).mockResolvedValue({ rows: [] } as never);
		vi.mocked(dashboardApi.syncHealth).mockResolvedValue({ freshness: {}, pending_count: 0, retry_count: 0, return_pending_count: 0, return_retry_count: 0 } as never);
		vi.mocked(dashboardApi.tags).mockResolvedValue({ rows: [] } as never);
		vi.mocked(dashboardApi.orderSummary).mockResolvedValue({ currency_totals: [], status_counts: [], provider_totals: [] } as never);
		vi.mocked(dashboardApi.activity).mockResolvedValue({ items: [] });
		vi.mocked(dashboardApi.latestSales).mockResolvedValue({ sales: [] });
		vi.mocked(dashboardApi.orders).mockResolvedValue({ orders: [], next_cursor: null, status_counts: [], webhook_inbox: {} } as never);
		vi.mocked(dashboardApi.catalogHealth).mockResolvedValue({ counts: [], detail: [] } as never);

		const { container } = render(App);
		await waitFor(() => expect(container.querySelector('.dash.ov')).toBeTruthy());
		expect(window.location.pathname).toBe('/dashboard/');
		expect(window.location.search).toBe('?currency=all');
		const cells = [...container.querySelectorAll('.dash.ov > .cell')].map((c) => [...c.classList].find((k) => k.startsWith('a-')));
		expect(cells[0]).toBe('a-sync');
		for (const area of ['a-sales', 'a-trend', 'a-products', 'a-cat', 'a-tags', 'a-branch', 'a-latest', 'a-act', 'a-orders']) expect(cells).toContain(area);
		// One currency switch, in the top bar.
		expect(container.querySelector('.topbar [data-testid="currency-mode"]')).toBeTruthy();
		expect(container.querySelectorAll('.dash [aria-pressed]').length).toBe(container.querySelectorAll('.dash .cell:not(.a-sales) [aria-pressed]').length);
		await waitFor(() => expect(dashboardApi.tags).toHaveBeenCalled());
		expect(dashboardApi.orderSummary).toHaveBeenCalled();
	});

	it('drops a Store from the address that the registry does not list', async () => {
		setLang('en');
		const stale = 'a126a7cb-60fe-4f63-b04c-83264600e4fa';
		const live = 'cc035b12-8456-4d18-ad8c-3010b4460450';
		window.history.replaceState({}, '', `/dashboard/?store_id=${stale}&currency=all`);
		for (const k of Object.keys(dashboardApi)) vi.spyOn(dashboardApi, k as any).mockImplementation((async () => ({})) as any);
		vi.mocked(dashboardApi.me).mockResolvedValue({ authenticated: true, stage: 'full', csrf_token: 't', user: { id: 'u', login: 'o@test', display_name: 'Owner', role: 'owner', mfa_enabled: true }, permissions: ['reports.read'], all_stores: true, store_ids: [], session: { expires_at: '', idle_timeout_seconds: 1800 } } as never);
		vi.mocked(dashboardApi.stores).mockResolvedValue({ stores: [{ store_id: live, display_name: 'MoonLight Store', timezone: 'Africa/Cairo', status: 'active', device_count: 1, created_at: '', updated_at: '' }] });
		vi.mocked(dashboardApi.overview).mockResolvedValue({ period: { start_local: '2026-10-03', end_local_exclusive: '2026-10-04' }, summary: { transaction_count: 0, return_transaction_count: 0, currency_totals: [] }, normalized: {}, averages: {}, fx: {} } as never);
		vi.mocked(dashboardApi.daily).mockResolvedValue({ days: [], display_currency: 'EGP', normalized: false } as never);
		for (const k of ['products', 'categories', 'branches'] as const) vi.mocked(dashboardApi[k]).mockResolvedValue({ rows: [] } as never);
		vi.mocked(dashboardApi.syncHealth).mockResolvedValue({ freshness: {}, pending_count: 0, retry_count: 0, return_pending_count: 0, return_retry_count: 0 } as never);
		vi.mocked(dashboardApi.tags).mockResolvedValue({ rows: [] } as never);
		vi.mocked(dashboardApi.orderSummary).mockResolvedValue({ currency_totals: [], status_counts: [], provider_totals: [] } as never);
		vi.mocked(dashboardApi.activity).mockResolvedValue({ items: [] });
		vi.mocked(dashboardApi.latestSales).mockResolvedValue({ sales: [] });
		vi.mocked(dashboardApi.orders).mockResolvedValue({ orders: [], next_cursor: null, status_counts: [], webhook_inbox: {} } as never);
		vi.mocked(dashboardApi.catalogHealth).mockResolvedValue({ counts: [], detail: [] } as never);

		render(App);
		await waitFor(() => expect(dashboardApi.overview).toHaveBeenCalled());
		// Every read asks for all Stores, and the address no longer names the stale one.
		expect(vi.mocked(dashboardApi.overview).mock.calls.every((c) => c[1] === '')).toBe(true);
		expect(window.location.search).not.toContain(stale);
	});
});
