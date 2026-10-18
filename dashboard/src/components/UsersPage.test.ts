import { describe, expect, it, vi, afterEach } from 'vitest';
import { cleanup, render, screen, fireEvent } from '@testing-library/svelte';
import UsersPage from './UsersPage.svelte';
import { usersApi } from '../lib/api.js';
import type { Me, UserView } from '../lib/api.js';

vi.mock('../lib/api.js', async (orig) => {
	const mod = (await orig()) as Record<string, unknown>;
	return { ...mod, usersApi: { list: vi.fn(), create: vi.fn(), action: vi.fn(), audit: vi.fn() } };
});

const api = usersApi as unknown as Record<string, ReturnType<typeof vi.fn>>;
const STORE = '11111111-1111-4111-8111-111111111111';

const owner: Me = {
	authenticated: true, stage: 'full', csrf_token: 'c',
	user: { id: 'u-owner', login: 'owner@x.test', display_name: 'Owner', role: 'owner', mfa_enabled: true },
	permissions: ['users.read', 'users.manage', 'security.manage'], all_stores: true, store_ids: [],
	session: { expires_at: '2026-10-09T12:00:00Z', idle_timeout_seconds: 1800 }
};

function user(id: string, login: string, role: 'owner' | 'admin', status: UserView['status']): UserView {
	return { id, login, display_name: login, role, status, all_stores: role === 'owner', store_ids: role === 'owner' ? [] : [STORE],
		mfa_enabled: status === 'active', created_at: '2026-10-01T00:00:00Z', updated_at: '2026-10-01T00:00:00Z' };
}

afterEach(() => {
	cleanup();
	vi.resetAllMocks();
});

describe('UsersPage', () => {
	it('shows a newly created account in the list right after creation', async () => {
		const existing = user('u-owner', 'owner@x.test', 'owner', 'active');
		const created = user('u-new', 'new@x.test', 'admin', 'pending_setup');
		api.list.mockResolvedValueOnce({ users: [existing], next_cursor: '' }).mockResolvedValueOnce({ users: [existing, created], next_cursor: '' });
		api.audit.mockResolvedValue({ events: [], next_before: 0 });
		api.create.mockResolvedValue({ user: created, activation_token: 'tok' });
		render(UsersPage, { me: owner, stores: [{ store_id: STORE, display_name: 'Store A', timezone: 'Africa/Cairo', status: 'active', device_count: 0, created_at: '', updated_at: '' }], onsessionchange: () => {} });
		expect(await screen.findAllByTestId('user-row')).toHaveLength(1);

		await fireEvent.input(screen.getByLabelText(/Login/), { target: { value: 'new@x.test' } });
		await fireEvent.input(screen.getByLabelText(/Display name/), { target: { value: 'New' } });
		const stores = screen.getByLabelText(/Stores/, { selector: 'select' }) as HTMLSelectElement;
		stores.options[0].selected = true;
		await fireEvent.change(stores);
		await fireEvent.submit(screen.getByTestId('create-user'));

		expect(await screen.findByTestId('activation-link')).toBeTruthy();
		const rows = await screen.findAllByTestId('user-row');
		expect(rows).toHaveLength(2);
		expect(rows[1].textContent).toContain('pending_setup');
		expect(api.list).toHaveBeenCalledTimes(2);
	});

	it('refreshes the list after a status change', async () => {
		const a = user('u-a', 'a@x.test', 'admin', 'active');
		api.list.mockResolvedValueOnce({ users: [a], next_cursor: '' }).mockResolvedValueOnce({ users: [{ ...a, status: 'disabled' }], next_cursor: '' });
		api.audit.mockResolvedValue({ events: [], next_before: 0 });
		api.action.mockResolvedValue({ ...a, status: 'disabled' });
		const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(true);
		render(UsersPage, { me: owner, stores: [], onsessionchange: () => {} });
		await fireEvent.click(await screen.findByText(/Disable/));
		expect(await screen.findByText('disabled')).toBeTruthy();
		expect(api.action).toHaveBeenCalledWith('u-a', 'status', { status: 'disabled' });
		confirmSpy.mockRestore();
	});
});
