import { describe, expect, it, vi, afterEach } from 'vitest';
import { cleanup, render, screen, fireEvent } from '@testing-library/svelte';
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

describe('DevicesPage', () => {
	it('renders ONLINE/OFFLINE/NEVER_SEEN with Sync Now states', async () => {
		api.devices.mockResolvedValue({
			devices: [
				{ device_id: 'd-online', name: 'shop', lifecycle: 'active', connectivity: 'ONLINE', last_seen_at: '2026-09-29T10:00:00Z', active_command: { id: 'c1', type: 'sync_now', version: 1, lease_generation: 1, requested_at: '2026-09-29T09:59:00Z', status: 'running' } },
				{ device_id: 'd-off', lifecycle: 'active', connectivity: 'OFFLINE', last_seen_at: '2026-09-28T10:00:00Z', active_command: null },
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
			devices: [{ device_id: 'd1', lifecycle: 'active', connectivity: 'ONLINE', active_command: { id: 'c1', type: 'sync_now', version: 1, lease_generation: 1, requested_at: 'x', status: 'pending' } }]
		});
		render(DevicesPage);
		const btn = (await screen.findByText('Sync Now')) as HTMLButtonElement;
		expect(btn.disabled).toBe(true);
		expect(await screen.findByText('Queued')).toBeTruthy();
	});

	it('queues Sync Now for offline device and shows status', async () => {
		api.devices.mockResolvedValue({ devices: [{ device_id: 'd2', lifecycle: 'active', connectivity: 'OFFLINE', active_command: null }] });
		api.syncRequest.mockResolvedValue({ command: { id: 'c9', status: 'pending' }, created: true });
		render(DevicesPage);
		const btn = (await screen.findByTestId('sync-now')) as HTMLButtonElement;
		await fireEvent.click(btn);
		expect(api.syncRequest).toHaveBeenCalledOnce();
		expect(await screen.findByText('Sync requested — queued for device.')).toBeTruthy();
	});

	it('shows failed command state', async () => {
		api.devices.mockResolvedValue({
			devices: [{ device_id: 'd3', lifecycle: 'active', connectivity: 'ONLINE', active_command: { id: 'c2', type: 'sync_now', version: 1, lease_generation: 1, requested_at: 'x', status: 'failed' } }]
		});
		render(DevicesPage);
		expect(await screen.findByText('Failed')).toBeTruthy();
	});
});
