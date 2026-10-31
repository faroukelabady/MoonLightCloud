import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import SettingsPage from './SettingsPage.svelte';
import { settingsApi } from '../lib/settingsApi.js';
import { ApiError } from '../lib/http.js';
import { setLang } from '../lib/i18n.svelte.js';

vi.mock('../lib/settingsApi.js', async (orig) => {
	const mod = (await orig()) as Record<string, unknown>;
	return {
		...mod,
		settingsApi: {
			get: vi.fn(), saveBusiness: vi.fn(), addRate: vi.fn(), deleteRate: vi.fn(), messaging: vi.fn(),
			addRecipient: vi.fn(), setRecipient: vi.fn(), addSchedule: vi.fn(), setSchedule: vi.fn(), runSchedule: vi.fn(), setTemplate: vi.fn()
		}
	};
});

const api = settingsApi as unknown as Record<keyof typeof settingsApi, ReturnType<typeof vi.fn>>;
const business = { tax_enabled: false, discount_enabled: false, returns_enabled: false, revision: 3, updated_at: '2026-10-27T10:00:00Z' };
const rate = { id: 'r1', rate: '50.25', rate_microrate: 50_250_000, effective_at: '2026-10-01T00:00:00Z', note: 'bank', created_at: '2026-10-01T00:00:00Z' };
const messaging = { scheduler_enabled: false, providers: [], recipients: [], schedules: [], runs: [] };

beforeEach(() => {
	setLang('en');
	api.get.mockResolvedValue({ business, exchange_rates: [rate] });
	api.messaging.mockResolvedValue(messaging);
});
afterEach(() => {
	cleanup();
	vi.resetAllMocks();
	setLang('ar');
});

describe('SettingsPage', () => {
	it('saves the toggles with the revision it read', async () => {
		api.saveBusiness.mockResolvedValue({ ...business, returns_enabled: true, revision: 4 });
		render(SettingsPage, { canManage: true });
		const returns = await screen.findByRole('switch', { name: 'Returns and voids' });
		expect(screen.getByTestId('save-toggles').hasAttribute('disabled')).toBe(true);
		await fireEvent.click(returns);
		await fireEvent.click(screen.getByTestId('save-toggles'));
		expect(api.saveBusiness).toHaveBeenCalledWith({ tax_enabled: false, discount_enabled: false, returns_enabled: true, revision: 3 });
		expect(await screen.findByText(/Settings saved/)).toBeTruthy();
	});

	it('reloads when someone else saved first', async () => {
		api.saveBusiness.mockRejectedValue(new ApiError(409, 'CONFLICT', 'request failed (409)', 'SETTINGS_CHANGED'));
		render(SettingsPage, { canManage: true });
		await fireEvent.click(await screen.findByRole('switch', { name: 'Tax' }));
		await fireEvent.click(screen.getByTestId('save-toggles'));
		expect((await screen.findByRole('alert')).textContent).toMatch(/Someone else just changed/);
		await waitFor(() => expect(api.get).toHaveBeenCalledTimes(2));
	});

	it('adds a rate and validates it first', async () => {
		api.addRate.mockResolvedValue({ ...rate, id: 'r2', rate: '51', effective_at: '2026-10-28T08:00:00Z', note: '' });
		render(SettingsPage, { canManage: true });
		expect((await screen.findByTestId('current-rate')).textContent).toMatch(/50\.25/);
		await fireEvent.click(screen.getByTestId('new-rate'));
		const input = screen.getByTestId('rate-value');
		await fireEvent.input(input, { target: { value: '-1' } });
		await fireEvent.click(screen.getByRole('button', { name: 'Add rate' }));
		expect(api.addRate).not.toHaveBeenCalled();
		await fireEvent.input(input, { target: { value: '51' } });
		await fireEvent.click(screen.getByRole('button', { name: 'Add rate' }));
		await waitFor(() => expect(screen.getAllByTestId('rate-row')).toHaveLength(2));
		expect(api.addRate).toHaveBeenCalledWith(expect.objectContaining({ rate: '51' }));
	});

	it('is read-only without settings.manage and hides report delivery', async () => {
		render(SettingsPage, { canManage: false });
		const tax = await screen.findByRole('switch', { name: 'Tax' });
		expect(tax.hasAttribute('disabled')).toBe(true);
		expect(screen.queryByTestId('save-toggles')).toBeNull();
		expect(screen.queryByRole('button', { name: 'Add rate' })).toBeNull();
		expect(api.messaging).not.toHaveBeenCalled();
	});

	it('explains that tokens live on the server and the scheduler is off', async () => {
		render(SettingsPage, { canManage: true });
		expect(await screen.findByTestId('no-providers')).toBeTruthy();
		expect(screen.getByTestId('scheduler-off')).toBeTruthy();
	});

	it('adds a WhatsApp recipient and sends a schedule now', async () => {
		api.messaging.mockResolvedValue({
			scheduler_enabled: true,
			providers: [{ key: 'whatsapp-main', kind: 'whatsapp', templates: [] }],
			recipients: [{ id: 'rc1', label: 'Owner', provider_key: 'whatsapp-main', recipient_masked: '…5678', locale: 'ar', enabled: true }],
			schedules: [{ id: 's1', name: 'Nightly', kind: 'daily', local_time: '21:00', anchor_local_date: '', enabled: true, next_run_at: '2026-10-28T19:00:00Z', recipient_ids: ['rc1'] }],
			runs: []
		});
		api.addRecipient.mockResolvedValue({});
		api.runSchedule.mockResolvedValue({ run_id: 'run1', created: true, scheduler_enabled: true });
		render(SettingsPage, { canManage: true });
		await screen.findByTestId('provider-whatsapp');
		await fireEvent.input(screen.getByLabelText('Name', { selector: '#recipient-label' }), { target: { value: 'Manager' } });
		await fireEvent.input(screen.getByLabelText('WhatsApp number (international)'), { target: { value: '201098765432' } });
		await fireEvent.click(screen.getByRole('button', { name: 'Add' }));
		await waitFor(() =>
			expect(api.addRecipient).toHaveBeenCalledWith({ label: 'Manager', provider_key: 'whatsapp-main', recipient: '201098765432', locale: 'ar' })
		);
		// Saving reloads the page data; actions wait until that finishes.
		await waitFor(() => expect(screen.getByRole('button', { name: 'Send now' }).hasAttribute('disabled')).toBe(false));
		await fireEvent.click(screen.getByRole('button', { name: 'Send now' }));
		await waitFor(() => expect(api.runSchedule).toHaveBeenCalledWith('s1', expect.any(String)));
		expect(await screen.findByText(/sent within about a minute/)).toBeTruthy();
	});

	it.each([
		{ language: 'en' as const, status: 401, code: 'UNAUTHORIZED', message: 'Session expired, please sign in again.' },
		{ language: 'ar' as const, status: 401, code: 'UNAUTHORIZED', message: 'انتهت الجلسة — سجّل الدخول مجددًا.' },
		{ language: 'en' as const, status: 500, code: 'INTERNAL', message: 'Could not save. Try again.' }
	])('explains a rejected schedule save ($status, $language) and keeps the draft', async ({ language, status, code, message }) => {
		api.messaging.mockResolvedValue({
			...messaging,
			scheduler_enabled: true,
			providers: [{ key: 'telegram-main', kind: 'telegram', templates: [] }],
			recipients: [{ id: 'rc1', label: 'Owner', provider_key: 'telegram-main', recipient_masked: '…5678', locale: 'en', enabled: true }]
		});
		api.addSchedule.mockRejectedValue(new ApiError(status, code, `request failed (${status})`));
		setLang(language);
		render(SettingsPage, { canManage: true });
		await screen.findByTestId('provider-telegram');
		const name = document.getElementById('schedule-name') as HTMLInputElement;
		await fireEvent.input(name, { target: { value: 'Telegram report' } });
		await fireEvent.click(document.getElementById('schedule-recipients-trigger')!);
		await fireEvent.click(screen.getByRole('checkbox'));
		await fireEvent.submit(name.closest('form')!);
		expect((await screen.findByRole('alert')).textContent).toBe(message);
		expect(api.addSchedule).toHaveBeenCalledTimes(1);
		expect(api.addSchedule).toHaveBeenCalledWith(expect.objectContaining({ name: 'Telegram report', recipient_ids: ['rc1'] }));
		expect(name.value).toBe('Telegram report');
		expect((screen.getByRole('checkbox') as HTMLInputElement).checked).toBe(true);
		expect(api.runSchedule).not.toHaveBeenCalled();
	});
});
