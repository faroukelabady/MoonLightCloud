import { describe, expect, it, vi, afterEach } from 'vitest';
import { cleanup, render, screen, fireEvent, within } from '@testing-library/svelte';
import DevicesPage from './DevicesPage.svelte';
import { dashboardApi } from '../lib/api.js';

vi.mock('../lib/api.js', async (orig) => {
	const mod = (await orig()) as Record<string, unknown>;
	return { ...mod, dashboardApi: { devices: vi.fn(), syncRequest: vi.fn() } };
});

const api = dashboardApi as unknown as {
	devices: ReturnType<typeof vi.fn>;
	syncRequest: ReturnType<typeof vi.fn>;
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
				{ device_id: 'd-online', name: 'shop', lifecycle: 'active', connectivity: 'ONLINE', last_seen_at: '2026-09-29T10:00:00Z', active_command: cmd('c1', 'running'), recent_commands: [cmd('c1', 'running')] },
				{ device_id: 'd-off', lifecycle: 'active', connectivity: 'OFFLINE', last_seen_at: '2026-09-28T10:00:00Z', active_command: null, recent_commands: [] },
				{ device_id: 'd-new', lifecycle: 'active', connectivity: 'NEVER_SEEN', last_seen_at: null, active_command: null }
			]
		});
		render(DevicesPage);
		expect(await screen.findByText('ONLINE')).toBeTruthy();
		expect(await screen.findByText('OFFLINE')).toBeTruthy();
		expect(await screen.findByText('NEVER_SEEN')).toBeTruthy();
		expect(await screen.findByText('Syncing')).toBeTruthy();
	});

	it('disables Sync Now while a command is active', async () => {
		api.devices.mockResolvedValue({
			devices: [{ device_id: 'd1', lifecycle: 'active', connectivity: 'ONLINE', active_command: cmd('c1', 'pending'), recent_commands: [cmd('c1', 'pending')] }]
		});
		render(DevicesPage);
		const btn = (await screen.findByText('Sync Now')) as HTMLButtonElement;
		expect(btn.disabled).toBe(true);
		expect(await screen.findByText('Queued')).toBeTruthy();
	});

	it('queues Sync Now for offline device and shows status', async () => {
		api.devices.mockResolvedValue({ devices: [{ device_id: 'd2', lifecycle: 'active', connectivity: 'OFFLINE', active_command: null, recent_commands: [] }] });
		api.syncRequest.mockResolvedValue({ command: { id: 'c9', status: 'pending' }, created: true });
		render(DevicesPage);
		const btn = (await screen.findByTestId('sync-now')) as HTMLButtonElement;
		await fireEvent.click(btn);
		expect(api.syncRequest).toHaveBeenCalledOnce();
		expect(await screen.findByText('Sync requested — queued for device.')).toBeTruthy();
	});

	it('shows completed terminal history from recent_commands when idle', async () => {
		api.devices.mockResolvedValue({
			devices: [{ device_id: 'd3', lifecycle: 'active', connectivity: 'ONLINE', last_seen_at: '2026-09-29T10:00:00Z', active_command: null, recent_commands: [cmd('c2', 'completed', '2026-09-29T10:00:00Z')] }]
		});
		render(DevicesPage);
		expect(await screen.findByText('Last sync: Completed')).toBeTruthy();
		const btn = (await screen.findByTestId('sync-now')) as HTMLButtonElement;
		expect(btn.disabled).toBe(false);
	});

	it('shows failed terminal history from recent_commands when idle', async () => {
		api.devices.mockResolvedValue({
			devices: [{ device_id: 'd4', lifecycle: 'active', connectivity: 'OFFLINE', active_command: null, recent_commands: [cmd('c3', 'failed', '2026-09-29T08:00:00Z')] }]
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
			devices: [{ device_id: 'd5', lifecycle: 'active', connectivity: 'ONLINE', active_command: cmd('c-active', 'running', '2026-09-29T10:02:00Z'), recent_commands: recent }]
		});
		render(DevicesPage);
		// Active visible, terminal history hidden while active.
		expect(await screen.findByText('Syncing')).toBeTruthy();
		expect(screen.queryByTestId('sync-history')).toBeNull();
	});

	it('lists past history when no command is active', async () => {
		const recent = [cmd('c2', 'completed', '2026-09-29T10:01:00Z'), cmd('c1', 'failed', '2026-09-29T10:00:00Z')];
		api.devices.mockResolvedValue({
			devices: [{ device_id: 'd6', lifecycle: 'active', connectivity: 'ONLINE', active_command: null, recent_commands: recent }]
		});
		render(DevicesPage);
		const hist = await screen.findByTestId('sync-history');
		const items = within(hist as HTMLElement).getAllByRole('listitem');
		expect(items).toHaveLength(2);
		expect(items[0].textContent).toContain('Completed');
		expect(items[1].textContent).toContain('Failed');
	});
});
