import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import ActivityCard from './ActivityCard.svelte';
import LatestSalesCard from './LatestSalesCard.svelte';
import MediaStatusCard from './MediaStatusCard.svelte';
import UserMenu from './UserMenu.svelte';
import { dashboardApi, authApi, ApiError } from '../lib/api.js';
import type { ActivityItem, MediaStatus, Me } from '../lib/api.js';
import { setLang } from '../lib/i18n.svelte.js';

beforeEach(() => setLang('en'));
afterEach(() => {
	cleanup();
	vi.restoreAllMocks();
});

const item = (n: number): ActivityItem => ({ kind: 'projected', event_id: `e-${n}`, event_type: `sale.finalized.v3 #${n}`, timestamp: new Date().toISOString() });

describe('ActivityCard', () => {
	it('scrolls inside the card and pages through older activity', async () => {
		const page = vi.spyOn(dashboardApi, 'activityPage').mockResolvedValue({ items: [item(11)], offset: 10, has_more: false });
		render(ActivityCard, { props: { items: Array.from({ length: 10 }, (_, i) => item(i + 1)), hasMore: true, store: 'S', status: 'loaded', errStatus: null, onretry: () => {} } });
		expect(screen.getByTestId('activity-feed').querySelectorAll('li')).toHaveLength(10);
		expect((screen.getByRole('button', { name: 'Previous' }) as HTMLButtonElement).disabled).toBe(true);
		await fireEvent.click(screen.getByRole('button', { name: 'Next' }));
		await waitFor(() => expect(screen.getByText('Page 2')).toBeTruthy());
		expect(page).toHaveBeenCalledWith('S', 10);
		expect(screen.getByTestId('activity-feed').querySelectorAll('li')).toHaveLength(1);
		expect((screen.getByRole('button', { name: 'Next' }) as HTMLButtonElement).disabled).toBe(true);
		await fireEvent.click(screen.getByRole('button', { name: 'Previous' }));
		expect(screen.getByTestId('activity-feed').querySelectorAll('li')).toHaveLength(10);
	});

	it('hides the pager when everything fits on one page', () => {
		render(ActivityCard, { props: { items: [item(1)], hasMore: false, status: 'loaded', errStatus: null, onretry: () => {} } });
		expect(screen.queryByRole('button', { name: 'Next' })).toBeNull();
	});
});

describe('LatestSalesCard', () => {
	const sale = { sale_id: 's-1', sale_number: 'S-0042', channel: 'STORE', occurred_at: '2026-10-30T10:00:00Z', currency: 'EGP', total_minor: '35000', cashier_name: 'Mona' };

	it('opens the sale with sold price, cost and profit per item', async () => {
		const detail = vi.spyOn(dashboardApi, 'saleDetail').mockResolvedValue({
			...sale,
			subtotal_minor: '35000',
			discount_minor: '0',
			tax_minor: '0',
			shop_name_ar: 'متجر',
			shop_name_en: 'Shop',
			lines: [
				{ sku: 'P-1', product_name: 'Papyrus', quantity: 2, unit_price_minor: '10000', line_total_minor: '20000', unit_cost_minor: '4000', line_cost_minor: '8000' },
				{ sku: 'F-1', product_name: 'Frame', quantity: 1, unit_price_minor: '15000', line_total_minor: '15000', unit_cost_minor: '6000', line_cost_minor: '6000' }
			],
			cost_minor: '14000',
			cost_complete: true
		});
		render(LatestSalesCard, { props: { sales: [sale], status: 'loaded', errStatus: null, onretry: () => {}, store: 'S' } });
		await fireEvent.click(screen.getByRole('button', { name: 'Show sale S-0042' }));
		const dialog = await screen.findByRole('dialog', { name: 'Sale S-0042' });
		expect(detail).toHaveBeenCalledWith('S', 's-1');
		await waitFor(() => expect(within(dialog).getAllByTestId('sale-line')).toHaveLength(2));
		const first = within(dialog).getAllByTestId('sale-line')[0].textContent ?? '';
		expect(first).toContain('Papyrus');
		expect(first).toMatch(/200 EGP[\s\S]*100 EGP each[\s\S]*80 EGP[\s\S]*40 EGP each[\s\S]*120 EGP/);
		expect(within(dialog).getByTestId('sale-profit').textContent).toMatch(/Profit \(before discount and tax\)\s*210 EGP/);
		await fireEvent.keyDown(dialog, { key: 'Escape' });
		expect(screen.queryByRole('dialog')).toBeNull();
	});

	it('does not show a profit when a cost is unknown', async () => {
		vi.spyOn(dashboardApi, 'saleDetail').mockResolvedValue({
			...sale,
			subtotal_minor: '35000',
			discount_minor: '0',
			tax_minor: '0',
			shop_name_ar: 'متجر',
			shop_name_en: 'Shop',
			lines: [{ sku: 'P-1', product_name: 'Papyrus', quantity: 1, unit_price_minor: '35000', line_total_minor: '35000', unit_cost_minor: null, line_cost_minor: null }],
			cost_minor: '0',
			cost_complete: false
		});
		render(LatestSalesCard, { props: { sales: [sale], status: 'loaded', errStatus: null, onretry: () => {}, store: '' } });
		await fireEvent.click(screen.getByRole('button', { name: 'Show sale S-0042' }));
		const dialog = await screen.findByRole('dialog');
		await waitFor(() => expect(within(dialog).getByTestId('sale-profit').textContent).toContain('The profit is not shown.'));
		expect(within(dialog).getByTestId('sale-line').textContent).toContain('Not known');
	});
});

describe('MediaStatusCard', () => {
	const base: MediaStatus = { storage: 'r2', check: 'ok', checked_at: new Date().toISOString(), check_ms: 84, stored_files: 12, stored_bytes: 3 * 1024 * 1024, uploaded_in_range: 2, last_upload_at: new Date().toISOString(), gallery_images: 14, waiting_images: 2 };

	it.each([
		['ok', 'Working'],
		['missing', 'A file is missing'],
		['error', 'Not answering'],
		['empty', 'No images yet']
	] as const)('reports the %s check', (check, text) => {
		render(MediaStatusCard, { props: { data: { ...base, check }, status: 'loaded', errStatus: null, onretry: () => {}, oncheck: () => {} } });
		const state = screen.getByTestId('media-check');
		expect(state.dataset.check).toBe(check);
		expect(state.textContent).toContain('Cloudflare R2');
		expect(state.textContent).toContain(text);
	});

	it('shows the counts and checks again on request', async () => {
		const oncheck = vi.fn();
		const { container } = render(MediaStatusCard, { props: { data: base, status: 'loaded', errStatus: null, onretry: () => {}, oncheck } });
		const text = container.textContent ?? '';
		expect(text).toMatch(/Stored images\s*12\s*3\.0 MB/);
		expect(text).toMatch(/Waiting for upload\s*2\s*\/ 14/);
		await fireEvent.click(screen.getByRole('button', { name: 'Check again' }));
		expect(oncheck).toHaveBeenCalled();
	});
});

describe('UserMenu', () => {
	const me = { authenticated: true, stage: 'full', csrf_token: 't', user: { id: 'u', login: 'alex@test.example', display_name: 'Alex Morgan', role: 'owner', mfa_enabled: true }, permissions: [], all_stores: true, store_ids: [], session: { expires_at: '', idle_timeout_seconds: 1800 } } as unknown as Me;

	it('shows the user, the role, Change password and Log out in one menu', async () => {
		const onlogout = vi.fn();
		render(UserMenu, { props: { me, onlogout } });
		const avatar = screen.getByRole('button', { name: 'Account menu: Alex Morgan' });
		expect(avatar.textContent).toContain('AM');
		await fireEvent.click(avatar);
		const menu = screen.getByRole('menu');
		expect(menu.textContent).toContain('alex@test.example');
		expect(menu.textContent).toContain('Owner');
		await fireEvent.click(within(menu).getByRole('menuitem', { name: 'Log out' }));
		expect(onlogout).toHaveBeenCalled();
	});

	it('changes the password only when the new one is confirmed', async () => {
		const change = vi.spyOn(authApi, 'changePassword').mockResolvedValue({ stage: 'full', csrf_token: 't2' });
		render(UserMenu, { props: { me, onlogout: () => {} } });
		await fireEvent.click(screen.getByRole('button', { name: /Account menu/ }));
		await fireEvent.click(screen.getByRole('menuitem', { name: 'Change password' }));
		const dialog = screen.getByRole('dialog', { name: 'Change password' });
		await fireEvent.input(within(dialog).getByLabelText('Current password'), { target: { value: 'old-password-1' } });
		await fireEvent.input(within(dialog).getByLabelText('New password'), { target: { value: 'new-password-123' } });
		await fireEvent.input(within(dialog).getByLabelText('Confirm new password'), { target: { value: 'new-password-124' } });
		await fireEvent.submit(dialog.querySelector('form')!);
		expect(within(dialog).getByRole('alert').textContent).toContain('not the same');
		expect(change).not.toHaveBeenCalled();
		await fireEvent.input(within(dialog).getByLabelText('Confirm new password'), { target: { value: 'new-password-123' } });
		await fireEvent.submit(dialog.querySelector('form')!);
		await waitFor(() => expect(within(dialog).getByRole('status').textContent).toContain('Password changed.'));
		expect(change).toHaveBeenCalledWith('old-password-1', 'new-password-123');
	});

	it('shows the server reason when the current password is wrong', async () => {
		vi.spyOn(authApi, 'changePassword').mockRejectedValue(new ApiError(401, 'UNAUTHORIZED', 'bad'));
		render(UserMenu, { props: { me, onlogout: () => {} } });
		await fireEvent.click(screen.getByRole('button', { name: /Account menu/ }));
		await fireEvent.click(screen.getByRole('menuitem', { name: 'Change password' }));
		const dialog = screen.getByRole('dialog');
		await fireEvent.input(within(dialog).getByLabelText('Current password'), { target: { value: 'wrong-password' } });
		await fireEvent.input(within(dialog).getByLabelText('New password'), { target: { value: 'new-password-123' } });
		await fireEvent.input(within(dialog).getByLabelText('Confirm new password'), { target: { value: 'new-password-123' } });
		await fireEvent.submit(dialog.querySelector('form')!);
		await waitFor(() => expect(within(dialog).getByRole('alert')).toBeTruthy());
	});
});
