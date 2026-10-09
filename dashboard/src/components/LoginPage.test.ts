import { describe, expect, it, vi, afterEach, beforeEach } from 'vitest';
import { cleanup, render, screen, fireEvent, waitFor } from '@testing-library/svelte';
import LoginPage from './LoginPage.svelte';
import UsersPage from './UsersPage.svelte';
import Sidebar from './Sidebar.svelte';
import { authApi, dashboardApi, usersApi, ApiError, mutationHeaders, setCSRFToken } from '../lib/api.js';
import type { Me } from '../lib/api.js';

afterEach(() => {
	cleanup();
	vi.restoreAllMocks();
	setCSRFToken('');
});

function me(over: Partial<Me> = {}): Me {
	return {
		authenticated: true,
		stage: 'FULL',
		csrf_token: 'csrf-1',
		user: { id: 'u1', login: 'owner@shop.test', display_name: 'Owner', role: 'OWNER', mfa_enabled: true },
		permissions: ['users.read', 'users.manage', 'security.manage', 'releases.read', 'catalog.read', 'devices.read', 'operations.read'],
		all_stores: true,
		store_ids: [],
		session: { expires_at: '', idle_timeout_seconds: 1800 },
		...over
	};
}

describe('LoginPage (ADR-0053)', () => {
	beforeEach(() => window.history.replaceState({}, '', '/dashboard/'));

	it('signs in through the MFA challenge to a full session', async () => {
		const login = vi.spyOn(authApi, 'login').mockResolvedValue({ stage: 'MFA_PENDING', csrf_token: 'c' });
		const verify = vi.spyOn(authApi, 'verifyTOTP').mockResolvedValue({ stage: 'FULL', csrf_token: 'c2' });
		const onauthenticated = vi.fn();
		render(LoginPage, { onauthenticated });
		await fireEvent.input(screen.getByLabelText(/Email or login/), { target: { value: 'owner@shop.test' } });
		await fireEvent.input(screen.getByLabelText(/Password/), { target: { value: 'test-only-password-1' } });
		await fireEvent.submit(screen.getByTestId('login-form'));
		expect(login).toHaveBeenCalledWith('owner@shop.test', 'test-only-password-1');
		const code = await screen.findByLabelText(/Authenticator code/);
		await fireEvent.input(code, { target: { value: '123456' } });
		await fireEvent.submit(screen.getByTestId('mfa-form'));
		expect(verify).toHaveBeenCalledWith('123456');
		await waitFor(() => expect(onauthenticated).toHaveBeenCalledOnce());
	});

	it('shows a generic error that never reveals whether the account exists', async () => {
		vi.spyOn(authApi, 'login').mockRejectedValue(new ApiError(401, 'UNAUTHORIZED', 'x', 'INVALID_CREDENTIALS'));
		render(LoginPage, { onauthenticated: vi.fn() });
		await fireEvent.input(screen.getByLabelText(/Email or login/), { target: { value: 'nobody@shop.test' } });
		await fireEvent.input(screen.getByLabelText(/Password/), { target: { value: 'whatever-password' } });
		await fireEvent.submit(screen.getByTestId('login-form'));
		expect((await screen.findByRole('alert')).textContent).toContain('Invalid email or password.');
		expect((screen.getByLabelText(/Password/) as HTMLInputElement).value).toBe('');
	});

	it('enrolls MFA and shows recovery codes once before continuing', async () => {
		vi.spyOn(authApi, 'login').mockResolvedValue({ stage: 'MFA_SETUP', csrf_token: 'c' });
		vi.spyOn(authApi, 'enrollStart').mockResolvedValue({ secret: 'JBSWY3DPEHPK3PXP', otpauth_uri: 'otpauth://totp/x?secret=JBSWY3DPEHPK3PXP' });
		vi.spyOn(authApi, 'enrollConfirm').mockResolvedValue({ stage: 'FULL', csrf_token: 'c3', recovery_codes: ['AAAA-BBBB-CCCC-DDDD', 'EEEE-FFFF-GGGG-HHHH'] });
		const onauthenticated = vi.fn();
		render(LoginPage, { onauthenticated });
		await fireEvent.input(screen.getByLabelText(/Email or login/), { target: { value: 'owner@shop.test' } });
		await fireEvent.input(screen.getByLabelText(/Password/), { target: { value: 'test-only-password-1' } });
		await fireEvent.submit(screen.getByTestId('login-form'));
		expect((await screen.findByTestId('totp-secret')).textContent).toBe('JBSW Y3DP EHPK 3PXP');
		await fireEvent.input(screen.getByLabelText(/Code/), { target: { value: '654321' } });
		await fireEvent.submit(screen.getByTestId('setup-form'));
		const codes = await screen.findByTestId('recovery-codes');
		expect(codes.textContent).toContain('AAAA-BBBB-CCCC-DDDD');
		const cont = screen.getByText(/Continue/) as HTMLButtonElement;
		expect(cont.disabled).toBe(true);
		await fireEvent.click(screen.getByRole('checkbox'));
		await fireEvent.click(cont);
		expect(onauthenticated).toHaveBeenCalledOnce();
	});

	it('activates from a URL fragment and removes the token from the address bar', async () => {
		window.history.replaceState({}, '', '/dashboard/#activate=one-time-token');
		const activate = vi.spyOn(authApi, 'activate').mockResolvedValue({ stage: 'MFA_SETUP', csrf_token: 'c' });
		vi.spyOn(authApi, 'enrollStart').mockResolvedValue({ secret: 'ABCD', otpauth_uri: 'otpauth://x' });
		render(LoginPage, { onauthenticated: vi.fn() });
		expect(await screen.findByTestId('activate-form')).toBeTruthy();
		expect(window.location.hash).toBe('');
		const [pw, pw2] = screen.getAllByLabelText(/password/i) as HTMLInputElement[];
		await fireEvent.input(pw, { target: { value: 'test-only-password-1' } });
		await fireEvent.input(pw2, { target: { value: 'test-only-password-1' } });
		await fireEvent.submit(screen.getByTestId('activate-form'));
		expect(activate).toHaveBeenCalledWith('one-time-token', 'test-only-password-1');
		expect(await screen.findByTestId('setup-form')).toBeTruthy();
	});

	it('resumes a pending MFA stage from an existing session', async () => {
		render(LoginPage, { stage: 'MFA_PENDING', onauthenticated: vi.fn() });
		expect(await screen.findByTestId('mfa-form')).toBeTruthy();
	});
});

describe('CSRF and storage (ADR-0053)', () => {
	it('sends the in-memory CSRF token on mutations and keeps nothing in storage', async () => {
		const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify(me()), { status: 200 }));
		await dashboardApi.me();
		expect(mutationHeaders()['X-CSRF-Token']).toBe('csrf-1');
		fetchSpy.mockResolvedValue(new Response(JSON.stringify({ user: {}, activation_token: 't' }), { status: 201 }));
		await usersApi.create({ login: 'a@b.cd', display_name: 'A', role: 'ADMIN', all_stores: true, store_ids: [] });
		const init = fetchSpy.mock.calls.at(-1)![1] as RequestInit;
		expect((init.headers as Record<string, string>)['X-CSRF-Token']).toBe('csrf-1');
		expect(init.credentials).toBe('same-origin');
		expect(window.localStorage.length).toBe(0);
		expect(window.sessionStorage.length).toBe(0);
		fetchSpy.mockResolvedValue(new Response('{}', { status: 200 }));
		await dashboardApi.logout();
		expect(mutationHeaders()['X-CSRF-Token']).toBe('');
	});
});

describe('permission-gated UI (UX only; the server enforces)', () => {
	it('hides navigation the human lacks permission for', () => {
		render(Sidebar, { route: 'overview', navigate: vi.fn(), permissions: ['reports.read'], allStores: false });
		expect(screen.queryByText('Users & Security')).toBeNull();
		expect(screen.queryByText('Updates')).toBeNull();
		expect(screen.queryByText('Operations')).toBeNull();
		cleanup();
		render(Sidebar, { route: 'overview', navigate: vi.fn(), permissions: me().permissions, allStores: true });
		expect(screen.getByText('Users & Security')).toBeTruthy();
		expect(screen.getByText('Operations')).toBeTruthy();
	});

	it('offers account administration only to OWNERs and confirms sensitive actions', async () => {
		vi.spyOn(usersApi, 'list').mockResolvedValue({
			users: [
				{ id: 'u2', login: 'admin@shop.test', display_name: 'Admin', role: 'ADMIN', status: 'ACTIVE', all_stores: true, store_ids: [], mfa_enabled: true, created_at: '', updated_at: '' }
			],
			next_cursor: ''
		});
		vi.spyOn(usersApi, 'audit').mockResolvedValue({ events: [], next_before: 0 });
		const action = vi.spyOn(usersApi, 'action').mockResolvedValue({} as never);
		const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(false);
		render(UsersPage, { me: me(), stores: [], onsessionchange: vi.fn() });
		expect(await screen.findByTestId('create-user')).toBeTruthy();
		await fireEvent.click(await screen.findByText(/Disable/));
		expect(confirmSpy).toHaveBeenCalledOnce();
		expect(action).not.toHaveBeenCalled();
		cleanup();
		const adminMe = me({ user: { id: 'u3', login: 'a@shop.test', display_name: 'A', role: 'ADMIN', mfa_enabled: true }, permissions: ['catalog.read'] });
		render(UsersPage, { me: adminMe, stores: [], onsessionchange: vi.fn() });
		expect(screen.queryByTestId('create-user')).toBeNull();
		expect(screen.queryByTestId('user-row')).toBeNull();
	});
});
