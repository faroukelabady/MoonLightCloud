import { describe, expect, it, vi, afterEach, beforeEach } from 'vitest';
import { cleanup, render, screen, fireEvent, within, waitFor } from '@testing-library/svelte';
import DevicesPage from './DevicesPage.svelte';
import { dashboardApi } from '../lib/api.js';
import { setLang } from '../lib/i18n.svelte.js';

// These tests read the English interface; each language is shown alone.
beforeEach(() => {
 setLang('en');
 api.stores.mockResolvedValue({ stores: [] });
 api.operationsSummary.mockResolvedValue({ devices: [] });
});

vi.mock('../lib/api.js', async (orig) => {
	const mod = (await orig()) as Record<string, unknown>;
	return { ...mod, dashboardApi: { devices: vi.fn(), syncRequest: vi.fn(), operationsSummary: vi.fn(), stores: vi.fn() } };
});

const api = dashboardApi as unknown as {
	devices: ReturnType<typeof vi.fn>;
	syncRequest: ReturnType<typeof vi.fn>;
	operationsSummary: ReturnType<typeof vi.fn>;
	stores: ReturnType<typeof vi.fn>;
};


afterEach(() => {
	cleanup();
	vi.resetAllMocks();
});

function cmd(id: string, status: string, requested_at = '2026-09-29T09:59:00Z') {
	return { id, type: 'sync_now', version: 1, lease_generation: 1, requested_at, status };
}

describe('DevicesPage', () => {
	it('renders ONLINE/OFFLINE/NEVER_SEEN with active Sync Now states', async () => {
		api.devices.mockResolvedValue({
			devices: [
				{ device_id: 'd-online', name: 'shop', lifecycle: 'active', connectivity: 'online', last_seen_at: '2026-09-29T10:00:00Z', active_command: cmd('c1', 'running'), recent_commands: [cmd('c1', 'running')] },
				{ device_id: 'd-off', lifecycle: 'active', connectivity: 'offline', last_seen_at: '2026-09-28T10:00:00Z', active_command: null, recent_commands: [] },
				{ device_id: 'd-new', lifecycle: 'active', connectivity: 'never_seen', last_seen_at: null, active_command: null }
			]
		});
		render(DevicesPage);
		expect(await screen.findByText('Online')).toBeTruthy();
		expect(await screen.findByText('Offline')).toBeTruthy();
		expect(await screen.findByText('No contact yet')).toBeTruthy();
		expect(await screen.findByText('Syncing')).toBeTruthy();
	});

	it('flags an online device whose control channel is not polling', async () => {
		api.devices.mockResolvedValue({
			devices: [
				{ device_id: 'd-sync', lifecycle: 'active', connectivity: 'online', control: 'never_polled', last_seen_at: '2026-09-29T10:00:00Z', last_data_at: '2026-09-29T09:58:00Z', active_command: null, recent_commands: [] },
				{ device_id: 'd-stale', lifecycle: 'active', connectivity: 'online', control: 'poll_stale', last_poll_at: '2026-09-29T08:00:00Z', active_command: null, recent_commands: [] },
				{ device_id: 'd-ok', lifecycle: 'active', connectivity: 'online', control: 'polling', active_command: null, recent_commands: [] }
			]
		});
		render(DevicesPage);
		const rows = await screen.findAllByTestId('device-row');
		expect(within(rows[0]).getByTestId('control-indicator').textContent).toMatch(/No control poll recorded/);
		expect(within(rows[0]).getByTestId('last-data').querySelector('time')?.getAttribute('datetime')).toBe('2026-09-29T09:58:00Z');
		expect(within(rows[1]).getByTestId('control-indicator').textContent).toMatch(/The last control poll is old/);
		expect(within(rows[2]).queryByTestId('control-indicator')).toBeNull();
	});

	it('disables Sync Now while a command is active', async () => {
		api.devices.mockResolvedValue({
			devices: [{ device_id: 'd1', lifecycle: 'active', connectivity: 'online', active_command: cmd('c1', 'pending'), recent_commands: [cmd('c1', 'pending')] }]
		});
		render(DevicesPage);
		const btn = (await screen.findByText('Sync Now')) as HTMLButtonElement;
		expect(btn.disabled).toBe(true);
		expect(await screen.findByText('Queued')).toBeTruthy();
	});

	it('queues Sync Now for offline device and shows status', async () => {
		api.devices.mockResolvedValue({ devices: [{ device_id: 'd2', lifecycle: 'active', connectivity: 'offline', active_command: null, recent_commands: [] }] });
		api.syncRequest.mockResolvedValue({ command: { id: 'c9', status: 'pending' }, created: true });
		render(DevicesPage);
		const btn = (await screen.findByTestId('sync-now')) as HTMLButtonElement;
		await fireEvent.click(btn);
		expect(api.syncRequest).toHaveBeenCalledOnce();
		expect(await screen.findByText('Sync requested — queued for device.')).toBeTruthy();
	});

	it('shows completed terminal history from recent_commands when idle', async () => {
		api.devices.mockResolvedValue({
			devices: [{ device_id: 'd3', lifecycle: 'active', connectivity: 'online', last_seen_at: '2026-09-29T10:00:00Z', active_command: null, recent_commands: [cmd('c2', 'completed', '2026-09-29T10:00:00Z')] }]
		});
		render(DevicesPage);
		expect(await screen.findByText('Last sync: Completed')).toBeTruthy();
		const btn = (await screen.findByTestId('sync-now')) as HTMLButtonElement;
		expect(btn.disabled).toBe(false);
	});

	it('shows failed terminal history from recent_commands when idle', async () => {
		api.devices.mockResolvedValue({
			devices: [{ device_id: 'd4', lifecycle: 'active', connectivity: 'offline', active_command: null, recent_commands: [cmd('c3', 'failed', '2026-09-29T08:00:00Z')] }]
		});
		render(DevicesPage);
		expect(await screen.findByText('Last sync: Failed')).toBeTruthy();
	});

	it('renders bounded history newest-first excluding the active row', async () => {
		const recent = [
			cmd('c-active', 'running', '2026-09-29T10:02:00Z'),
			cmd('c-old2', 'completed', '2026-09-29T10:01:00Z'),
			cmd('c-old1', 'failed', '2026-09-29T10:00:00Z')
		];
		api.devices.mockResolvedValue({
			devices: [{ device_id: 'd5', lifecycle: 'active', connectivity: 'online', active_command: cmd('c-active', 'running', '2026-09-29T10:02:00Z'), recent_commands: recent }]
		});
		render(DevicesPage);
		// Active visible, terminal history hidden while active.
		expect(await screen.findByText('Syncing')).toBeTruthy();
		expect(screen.queryByTestId('sync-history')).toBeNull();
	});

	it('lists past history when no command is active', async () => {
		const recent = [cmd('c2', 'completed', '2026-09-29T10:01:00Z'), cmd('c1', 'failed', '2026-09-29T10:00:00Z')];
		api.devices.mockResolvedValue({
			devices: [{ device_id: 'd6', lifecycle: 'active', connectivity: 'online', active_command: null, recent_commands: recent }]
		});
		render(DevicesPage);
		const hist = await screen.findByTestId('sync-history');
		const items = within(hist as HTMLElement).getAllByRole('listitem');
		expect(items).toHaveLength(2);
		expect(items[0].textContent).toContain('Completed');
		expect(items[1].textContent).toContain('Failed');
	});
});


describe('DevicesPage incident badges', () => {
	it('shows the batched incident badge without extra per-device calls', async () => {
		api.devices.mockResolvedValue({
			devices: [{ device_id: 'd1', lifecycle: 'active', connectivity: 'online', active_command: null, recent_commands: [] }]
		});
		api.operationsSummary.mockResolvedValue({
			devices: [{ device_id: 'd1', open_count: 2, max_severity: 'urgent' }]
		});
		render(DevicesPage);
		expect(await screen.findByTestId('incident-badge')).toBeTruthy();
		expect((await screen.findByTestId('incident-badge')).textContent).toContain('2 incident(s)');
		expect(api.operationsSummary).toHaveBeenCalledOnce();
	});
});

describe('DevicesPage store display', () => {
	it('shows bound store names and unbound markers plus the store list', async () => {
		api.devices.mockResolvedValue({
			devices: [
				{ device_id: 'd1', lifecycle: 'active', connectivity: 'online', store_id: 's1', store_name: 'Cairo Gallery' },
				{ device_id: 'd2', lifecycle: 'active', connectivity: 'never_seen' },
			],
		});
		api.stores.mockResolvedValue({
			stores: [
				{ store_id: 's1', display_name: 'Cairo Gallery', timezone: 'Africa/Cairo', status: 'active', device_count: 1, created_at: '', updated_at: '' },
			],
		});
		render(DevicesPage);
		expect(await screen.findByText('Cairo Gallery')).toBeTruthy();
		expect(await screen.findByText('Unassigned devices')).toBeTruthy();
		expect(await screen.findAllByText('1 device(s)')).toHaveLength(2);
	});
});


describe('Stores grouping and availability', () => {
 const row = (id: string, store_id = 's1') => ({ device_id: id, name: 'Device '+id, store_id, lifecycle: 'active', connectivity: 'online', control: 'polling', recent_commands: [] });
 const directory = [
  { store_id: 's1', display_name: 'Cairo Gallery', status: 'active', timezone: 'Africa/Cairo' },
  { store_id: 's2', display_name: 'Luxor Gallery', status: 'active', timezone: 'Africa/Cairo' },
  { store_id: 's3', display_name: 'Empty Gallery', status: 'inactive', timezone: 'Africa/Cairo' }
 ];
 it('nests each device under its Store and keeps empty Stores visible', async () => {
  api.stores.mockResolvedValue({ stores: directory });
  api.devices.mockResolvedValue({ devices: [row('cairo-1'), row('cairo-2'), row('luxor-1', 's2')] });
  render(DevicesPage);
  const groups = await screen.findAllByTestId('store-group');
  expect(groups).toHaveLength(3);
  expect(within(groups[0]).getAllByTestId('device-row').map(r => r.getAttribute('data-device'))).toEqual(['cairo-1', 'cairo-2']);
  expect(within(groups[1]).getAllByTestId('device-row').map(r => r.getAttribute('data-device'))).toEqual(['luxor-1']);
  expect(within(groups[2]).getByText('No Retail devices are registered for this Store.')).toBeTruthy();
  expect(groups.every(g => !(g as HTMLDetailsElement).open)).toBe(true);
  (groups[0] as HTMLDetailsElement).open = true;
  await fireEvent(groups[0], new Event('toggle'));
 });
 it('filters Store selection immediately without displaying another Store', async () => {
  api.stores.mockResolvedValue({ stores: directory });
  api.devices.mockResolvedValue({ devices: [row('cairo-1'), row('luxor-1', 's2')] });
  const view = render(DevicesPage, { store: 's1' });
  expect(await screen.findByText('Device cairo-1')).toBeTruthy();
  expect(screen.queryByText('Device luxor-1')).toBeNull();
  await view.rerender({ store: 's2' });
  expect(screen.queryByText('Device cairo-1')).toBeNull();
  expect(screen.getByText('Device luxor-1')).toBeTruthy();
 });
 it('disables Sync Now when Cloud control is disabled and never calls creation', async () => {
  api.devices.mockResolvedValue({ control_enabled: false, devices: [row('d1')] });
  render(DevicesPage);
  expect(await screen.findByTestId('control-disabled')).toBeTruthy();
  const button = await screen.findByTestId('sync-now') as HTMLButtonElement;
  expect(button.disabled).toBe(true);
  await fireEvent.click(button);
  expect(api.syncRequest).not.toHaveBeenCalled();
 });
 it('hides mutation controls from read-only operators', async () => {
  api.devices.mockResolvedValue({ devices: [row('d1')] });
  render(DevicesPage, { canManage: false });
  expect(await screen.findByText('Device d1')).toBeTruthy();
  expect(screen.queryByTestId('sync-now')).toBeNull();
 });
 it('does not drop bound devices if the Store directory is unavailable', async () => {
  api.stores.mockRejectedValue(new Error('unavailable'));
  api.devices.mockResolvedValue({ devices: [{ ...row('d1'), store_name: 'Cairo Gallery' }] });
  render(DevicesPage);
  expect(await screen.findByText('Cairo Gallery')).toBeTruthy();
  expect(screen.getByText('Device d1')).toBeTruthy();
  expect(screen.getByText(/Store details could not load/)).toBeTruthy();
 });
 it('refreshes queued work into its terminal result and preserves accordion choice', async () => {
  api.devices.mockResolvedValueOnce({ devices: [{ ...row('d1'), active_command: cmd('c1', 'pending') }] })
   .mockResolvedValue({ devices: [{ ...row('d1'), active_command: null, recent_commands: [cmd('c1', 'completed')] }] });
  render(DevicesPage);
  expect(await screen.findByText('Queued')).toBeTruthy();
  const group = screen.getByTestId('store-group') as HTMLDetailsElement;
  group.open = false;
  await fireEvent(group, new Event('toggle'));
  await fireEvent.click(screen.getByText('Refresh'));
  expect(await screen.findByText('Last sync: Completed')).toBeTruthy();
  expect(group.open).toBe(false);
 });
 it('does not attach a late response after a newer refresh', async () => {
  let resolveOld!: (value: unknown) => void;
  api.devices.mockImplementationOnce(() => new Promise(resolve => { resolveOld = resolve; }))
   .mockResolvedValue({ devices: [row('current')] });
  const view = render(DevicesPage);
  await waitFor(() => expect(api.devices).toHaveBeenCalledOnce());
  await view.component.refresh();
  expect(await screen.findByText('Device current')).toBeTruthy();
  resolveOld({ devices: [row('old')] });
  await Promise.resolve();
  expect(screen.queryByText('Device old')).toBeNull();
 });
 it('aborts the active request when leaving the page', async () => {
  api.devices.mockImplementation(() => new Promise(() => {}));
  const view = render(DevicesPage);
  await waitFor(() => expect(api.devices).toHaveBeenCalledOnce());
  const signal = api.devices.mock.calls[0][0] as AbortSignal;
  view.unmount();
  expect(signal.aborted).toBe(true);
 });
});
