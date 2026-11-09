import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import MessagingSettings from './MessagingSettings.svelte';
import { settingsApi } from '../lib/settingsApi.js';
import { setLang } from '../lib/i18n.svelte.js';

vi.mock('../lib/settingsApi.js', () => ({ settingsApi: {
	messaging: vi.fn(), setTemplate: vi.fn(), addRecipient: vi.fn(), getRecipient: vi.fn(), updateRecipient: vi.fn(), deleteRecipient: vi.fn(), setRecipient: vi.fn(), addSchedule: vi.fn(), updateSchedule: vi.fn(), deleteSchedule: vi.fn(), setSchedule: vi.fn(), runSchedule: vi.fn(), runPage: vi.fn()
} }));
const api = settingsApi as unknown as Record<keyof typeof settingsApi, ReturnType<typeof vi.fn>>;
const recipient = { id: 'r1', label: 'Owner WhatsApp', provider_key: 'whatsapp-main', recipient_masked: '…5678', locale: 'en', enabled: true };
const schedule = { id: 's1', name: 'Ten-day WhatsApp', kind: 'ten_day', local_time: '21:00', anchor_local_date: '2026-11-08', enabled: true, next_run_at: '2026-11-08T19:00:00Z', recipient_ids: ['r1'], revision: 7 };
const fixture = () => ({ scheduler_enabled: true, providers: [
	{ key: 'telegram-main', kind: 'telegram', templates: [] },
	{ key: 'whatsapp-main', kind: 'whatsapp', templates: [{ template_key: 'ten_day_business_report_v1', locale: 'en', external_name: 'moonlight_report', language: 'en_US', params: ['report_body'], enabled: true }] }
], recipients: [recipient], schedules: [schedule], runs: [] as Record<string, unknown>[], runs_next_cursor: '', runs_remaining: 0 });

beforeEach(() => { setLang('en'); api.messaging.mockResolvedValue(fixture()); });
afterEach(() => { cleanup(); vi.useRealTimers(); vi.restoreAllMocks(); vi.resetAllMocks(); setLang('ar'); });

describe('report configuration panels', () => {
	it('opens distinct provider dialogs with examples and address guidance', async () => {
		render(MessagingSettings);
		const recipients = await screen.findByTestId('recipients-panel');
		expect(within(recipients).getByRole('button', { name: 'Configure WhatsApp template' }).querySelector('svg')).toBeTruthy();
		expect(within(recipients).getByRole('button', { name: 'Configure Telegram templates' }).querySelector('svg')).toBeTruthy();
		await fireEvent.click(screen.getByTestId('provider-telegram'));
		let dialog = await screen.findByRole('dialog');
		expect(within(dialog).getByText(/For a private chat, use its numeric chat ID/)).toBeTruthy();
		expect(within(dialog).getByRole('region', { name: 'Example report content' })).toBeTruthy();
		await fireEvent.click(within(dialog).getByRole('button', { name: 'Close' }));
		await fireEvent.click(screen.getByTestId('provider-whatsapp'));
		dialog = await screen.findByRole('dialog');
		expect((within(dialog).getByLabelText('Approved template name') as HTMLInputElement).value).toBe('moonlight_report');
		expect((within(dialog).getByLabelText('Language code') as HTMLInputElement).value).toBe('en_US');
		expect(within(dialog).getByText(/Egypt example: 01012345678 becomes 201012345678/)).toBeTruthy();
		expect(within(dialog).getByRole('region', { name: 'Example report content' }).querySelector('pre')?.textContent).toContain('\n\n');
		expect(within(dialog).getByRole('table').textContent).toContain('moonlight_report');
		api.setTemplate.mockResolvedValue({});
		await fireEvent.click(within(dialog).getByRole('button', { name: 'Save template' }));
		await waitFor(() => expect(api.setTemplate).toHaveBeenCalledWith(expect.objectContaining({ provider_key: 'whatsapp-main', template_key: 'ten_day_business_report_v1', locale: 'en', language: 'en_US', params: ['report_body'] })));
	});

	it.each([
		{ kind: 'telegram', dispatch: 'accepted', delivery: 'accepted', label: 'Sent', good: true },
		{ kind: 'whatsapp', dispatch: 'accepted', delivery: 'accepted', label: 'Accepted by provider', good: true },
		{ kind: 'whatsapp', dispatch: 'accepted', delivery: 'delivered', label: 'Delivered', good: true },
		{ kind: 'whatsapp', dispatch: 'accepted', delivery: 'read', label: 'Read', good: true },
		{ kind: 'telegram', dispatch: 'blocked', delivery: 'unknown', label: 'Failed', good: false },
		{ kind: 'whatsapp', dispatch: 'ambiguous', delivery: 'unknown', label: 'Outcome uncertain', good: false }
	])('shows the actual $kind outcome: $label', async ({ kind, dispatch, delivery, label, good }) => {
		api.messaging.mockResolvedValue({ ...fixture(), runs: [{ id: 'run-result', schedule_id: 's1', schedule_name: 'Daily', kind: 'manual', status: 'completed', scheduled_for: '2026-11-08T05:00:00Z', attempt_count: 1, last_error_code: '', deliveries: [{ id: 'd1', provider_key: `${kind}-main`, provider_kind: kind, recipient_label: 'Owner', recipient_masked: '…5678', report_status: 'enqueued', dispatch_status: dispatch, delivery_status: delivery, last_error_code: dispatch === 'blocked' ? 'AUTHENTICATION' : '', attempt_count: 1, send_started: true, updated_at: '' }] }] });
		render(MessagingSettings);
		const row = await screen.findByTestId('report-run-row');
		expect(within(row).getAllByText(label)).toHaveLength(2);
		expect(row.querySelector('td:nth-child(4) .badge')?.classList.contains('ok')).toBe(good);
		expect(row.textContent).not.toContain('Queued for delivery');
		if (dispatch === 'blocked') expect(row.textContent).toContain('AUTHENTICATION');
	});

	it('updates the current history page with actual send results and stops polling on disposal', async () => {
		vi.useFakeTimers();
		vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible');
		const row = { id: 'run-10', schedule_id: 's1', schedule_name: 'Paged history', kind: 'manual', status: 'completed', scheduled_for: '2026-11-08T05:00:00Z', attempt_count: 1, last_error_code: '', deliveries: [{ id: 'd1', provider_key: 'telegram-main', provider_kind: 'telegram', recipient_label: 'Owner', recipient_masked: '…6789', report_status: 'enqueued', dispatch_status: 'pending', delivery_status: 'unknown', last_error_code: '', send_started: false, attempt_count: 0, updated_at: '' }] };
		api.messaging.mockResolvedValue({ ...fixture(), runs: [], runs_next_cursor: 'cursor-1', runs_remaining: 1 });
		api.runPage.mockResolvedValueOnce({ runs: [row], next_cursor: '', remaining_runs: 0 });
		const view = render(MessagingSettings);
		await vi.waitFor(() => expect(screen.getByTestId('recent-sends-panel')).toBeTruthy());
		await fireEvent.click(screen.getByRole('button', { name: 'Next' }));
		await vi.waitFor(() => expect(screen.getByRole('cell', { name: 'Queued for sending' })).toBeTruthy());
		api.runPage.mockResolvedValue({ runs: [{ ...row, deliveries: [{ ...row.deliveries[0], dispatch_status: 'accepted', delivery_status: 'accepted' }] }], next_cursor: '', remaining_runs: 0 });
		await vi.advanceTimersByTimeAsync(5000);
		expect(screen.getByRole('cell', { name: 'Sent' })).toBeTruthy();
		expect(api.runPage).toHaveBeenLastCalledWith('cursor-1', expect.any(AbortSignal));
		expect(screen.getByRole('navigation').textContent).toContain('Page 2');
		expect(api.runSchedule).not.toHaveBeenCalled();
		api.runPage.mockRejectedValueOnce(new Error('offline'));
		await vi.advanceTimersByTimeAsync(5000);
		expect(screen.getByRole('status').textContent).toContain('last known results');
		expect(screen.getByRole('cell', { name: 'Sent' })).toBeTruthy();
		const calls = api.runPage.mock.calls.length;
		const signal = api.runPage.mock.calls.at(-1)?.[1] as AbortSignal;
		view.unmount();
		expect(signal.aborted).toBe(true);
		await vi.advanceTimersByTimeAsync(5000);
		expect(api.runPage).toHaveBeenCalledTimes(calls);
	});

	it('blocks leading-zero WhatsApp input and preserves the draft', async () => {
		api.messaging.mockResolvedValue({ ...fixture(), providers: [fixture().providers[1]] });
		render(MessagingSettings);
		await fireEvent.click(await screen.findByRole('button', { name: 'Add recipient' }));
		const dialog = await screen.findByRole('dialog');
		await fireEvent.input(within(dialog).getByLabelText('Name'), { target: { value: 'My phone' } });
		await fireEvent.input(within(dialog).getByLabelText('WhatsApp number (international)'), { target: { value: '0201012345678' } });
		expect(within(dialog).getByRole('alert').textContent).toMatch(/without a leading 0/);
		expect(within(dialog).getByRole('button', { name: 'Add' }).hasAttribute('disabled')).toBe(true);
		expect(api.addRecipient).not.toHaveBeenCalled();
		await fireEvent.input(within(dialog).getByLabelText('WhatsApp number (international)'), { target: { value: '201012345678' } });
		expect(within(dialog).getByRole('button', { name: 'Add' }).hasAttribute('disabled')).toBe(false);
		expect((within(dialog).getByLabelText('Name') as HTMLInputElement).value).toBe('My phone');
	});

	it('loads the full address only when editing, then saves the existing recipient', async () => {
		api.getRecipient.mockResolvedValue({ id: 'r1', label: recipient.label, provider_key: recipient.provider_key, recipient: '201012345678', locale: 'en', enabled: true });
		api.updateRecipient.mockResolvedValue({});
		render(MessagingSettings);
		const panel = await screen.findByTestId('recipients-panel');
		expect(panel.textContent).not.toContain('201012345678');
		expect(api.getRecipient).not.toHaveBeenCalled();
		await fireEvent.click(within(panel).getByRole('button', { name: 'Edit' }));
		const dialog = await screen.findByRole('dialog');
		await fireEvent.input(within(dialog).getByLabelText('WhatsApp number (international)'), { target: { value: '201098765432' } });
		await fireEvent.click(within(dialog).getByRole('button', { name: 'Save changes' }));
		await waitFor(() => expect(api.updateRecipient).toHaveBeenCalledWith('r1', { label: recipient.label, recipient: '201098765432', locale: 'en' }));
		expect(api.addRecipient).not.toHaveBeenCalled();
	});

	it('edits a schedule with its revision and leaves send-now untouched', async () => {
		api.updateSchedule.mockResolvedValue({});
		render(MessagingSettings);
		const panel = await screen.findByTestId('schedules-panel');
		await fireEvent.click(within(panel).getByRole('button', { name: 'Edit' }));
		const form = screen.getByTestId('schedule-form');
		await fireEvent.input(within(form).getByLabelText('Name'), { target: { value: 'Renamed schedule' } });
		await fireEvent.click(within(form).getByRole('button', { name: 'Save changes' }));
		await waitFor(() => expect(api.updateSchedule).toHaveBeenCalledWith('s1', expect.objectContaining({ name: 'Renamed schedule', kind: 'ten_day', recipient_ids: ['r1'], revision: 7 })));
		expect(api.runSchedule).not.toHaveBeenCalled();
	});

	it.each([{ kind: 'recipient', panel: 'recipients-panel', method: 'deleteRecipient', id: 'r1' }, { kind: 'schedule', panel: 'schedules-panel', method: 'deleteSchedule', id: 's1' }])('confirms deletion of a $kind and supports cancelling', async ({ panel, method, id }) => {
		api[method as keyof typeof api].mockResolvedValue({ deleted: true });
		render(MessagingSettings);
		const target = await screen.findByTestId(panel);
		await fireEvent.click(within(target).getByRole('button', { name: 'Delete' }));
		let dialog = await screen.findByRole('dialog');
		expect(dialog.textContent).toMatch(/Past history is kept/);
		await fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }));
		expect(api[method as keyof typeof api]).not.toHaveBeenCalled();
		await fireEvent.click(within(target).getByRole('button', { name: 'Delete' }));
		dialog = await screen.findByRole('dialog');
		await fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }));
		await waitFor(() => expect(api[method as keyof typeof api]).toHaveBeenCalledWith(id));
	});

	it('paginates history beyond the first twenty records and retains saved schedule names', async () => {
		const runs = Array.from({ length: 23 }, (_, i) => ({ id: `run-${i}`, schedule_id: 'deleted-schedule', schedule_name: `Historic report ${i}`, kind: 'manual', status: 'completed', scheduled_for: '2026-11-08T05:00:00Z', attempt_count: 1, last_error_code: '' }));
		api.messaging.mockResolvedValue({ ...fixture(), runs: runs.slice(0, 10), runs_next_cursor: 'run-9', runs_remaining: 13 });
		api.runPage.mockImplementation((cursor: string) => Promise.resolve(cursor === 'run-9' ? { runs: runs.slice(10, 20), next_cursor: 'run-19', remaining_runs: 3 } : cursor === 'run-19' ? { runs: runs.slice(20), next_cursor: '', remaining_runs: 0 } : { runs: runs.slice(0, 10), next_cursor: 'run-9', remaining_runs: 13 }));
		render(MessagingSettings);
		const panel = await screen.findByTestId('recent-sends-panel');
		expect(within(panel).getAllByTestId('report-run-row')).toHaveLength(10);
		expect(within(panel).getByRole('navigation').textContent).toContain('2 pages remaining');
		await fireEvent.click(within(panel).getByRole('button', { name: 'Next' }));
		await within(panel).findByText('Historic report 10');
		expect(within(panel).getByRole('navigation').textContent).toContain('1 page remaining');
		await fireEvent.click(within(panel).getByRole('button', { name: 'Next' }));
		await within(panel).findByText('Historic report 22');
		expect(within(panel).getAllByTestId('report-run-row')).toHaveLength(3);
		expect(within(panel).getByRole('navigation').textContent).toContain('Last page');
		expect(within(panel).getByRole('button', { name: 'Next' }).hasAttribute('disabled')).toBe(true);
		await fireEvent.click(within(panel).getByRole('button', { name: 'Previous' }));
		await within(panel).findByText('Historic report 10');
		expect(within(panel).getAllByTestId('report-run-row')).toHaveLength(10);
		expect(api.runPage).toHaveBeenCalledWith('run-9', expect.any(AbortSignal));
	});
});
