import { describe, expect, it, vi, afterEach } from 'vitest';
import { cleanup, render, screen, fireEvent, within } from '@testing-library/svelte';
import OperationsPage from './OperationsPage.svelte';
import { dashboardApi, ApiError } from '../lib/api.js';

vi.mock('../lib/api.js', async (orig) => {
	const mod = (await orig()) as Record<string, unknown>;
	return {
		...mod,
		dashboardApi: { incidents: vi.fn(), incidentAck: vi.fn(), incidentResolve: vi.fn() }
	};
});

const api = dashboardApi as unknown as {
	incidents: ReturnType<typeof vi.fn>;
	incidentAck: ReturnType<typeof vi.fn>;
	incidentResolve: ReturnType<typeof vi.fn>;
};

afterEach(() => {
	cleanup();
	vi.resetAllMocks();
});

function row(id: string, rule: string, state: string, severity = 'warning') {
	return {
		id,
		rule,
		subject_type: 'device',
		subject_id: 'dev-1',
		severity,
		state,
		episode: 1,
		opened_at: '2026-09-29T10:00:00Z',
		last_observed_at: '2026-09-29T10:00:00Z',
		open_intent_materialized: true,
		resolved_intent_materialized: true
	};
}

describe('OperationsPage', () => {
	it('lists open and acknowledged incidents with filters', async () => {
		api.incidents.mockResolvedValue({
			incidents: [row('i1', 'DEVICE_OFFLINE', 'open'), row('i2', 'NOTIFICATION_AMBIGUOUS', 'acknowledged', 'urgent')],
			next_cursor: ''
		});
		render(OperationsPage);
		expect(await screen.findByText('DEVICE_OFFLINE')).toBeTruthy();
		expect(await screen.findByText('NOTIFICATION_AMBIGUOUS')).toBeTruthy();
		expect(await screen.findByText('urgent')).toBeTruthy();
	});

	it('acknowledges without claiming resolution', async () => {
		api.incidents.mockResolvedValue({ incidents: [row('i1', 'DEVICE_OFFLINE', 'open')], next_cursor: '' });
		api.incidentAck.mockResolvedValue(row('i1', 'DEVICE_OFFLINE', 'acknowledged'));
		render(OperationsPage);
		const btn = (await screen.findByTestId('ack')) as HTMLButtonElement;
		await fireEvent.click(btn);
		expect(api.incidentAck).toHaveBeenCalledOnce();
		expect(await screen.findByText('acknowledged')).toBeTruthy();
	});

	it('surfaces resolve conflicts when the condition is still active', async () => {
		api.incidents.mockResolvedValue({ incidents: [row('i1', 'DEVICE_OFFLINE', 'open')], next_cursor: '' });
		api.incidentResolve.mockRejectedValue(new ApiError(409, 'CONFLICT', 'request failed (409)'));
		render(OperationsPage);
		const btn = (await screen.findByTestId('resolve')) as HTMLButtonElement;
		await fireEvent.click(btn);
		expect(await screen.findByText('Condition still active — resolve is blocked until it clears.')).toBeTruthy();
	});

	it('paginates with the next cursor', async () => {
		api.incidents
			.mockResolvedValueOnce({ incidents: [row('i1', 'DEVICE_OFFLINE', 'open')], next_cursor: 'cursor-1' })
			.mockResolvedValueOnce({ incidents: [row('i2', 'DEVICE_SYNC_FAILED', 'open', 'urgent')], next_cursor: '' });
		render(OperationsPage);
		const more = (await screen.findByTestId('more')) as HTMLButtonElement;
		await fireEvent.click(more);
		expect(api.incidents).toHaveBeenCalledTimes(2);
		expect(await screen.findByText('DEVICE_SYNC_FAILED')).toBeTruthy();
	});
});

describe('OperationsPage intent review badge', () => {
	it('flags legacy rows with unmaterialized intents and hides it otherwise', async () => {
		api.incidents.mockResolvedValue({
			incidents: [
				{ ...row('legacy', 'DEVICE_OFFLINE', 'open'), open_intent_materialized: false },
				row('fresh', 'DEVICE_OFFLINE', 'open'),
				{ ...row('legacy-resolved', 'DEVICE_OFFLINE', 'resolved'), resolved_intent_materialized: false },
				row('manual-resolved', 'DEVICE_OFFLINE', 'resolved')
			],
			next_cursor: ''
		});
		render(OperationsPage);
		const badges = await screen.findAllByTestId('intent-review');
		expect(badges).toHaveLength(2);
		expect(badges[0].textContent).toContain('needs review');
		const rows = screen.getAllByTestId('incident-row');
		expect(rows).toHaveLength(4);
		expect(within(rows[1] as HTMLElement).queryByTestId('intent-review')).toBeNull();
		expect(within(rows[3] as HTMLElement).queryByTestId('intent-review')).toBeNull();
	});
});
