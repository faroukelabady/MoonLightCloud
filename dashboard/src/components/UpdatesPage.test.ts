import { describe, expect, it, vi, afterEach } from 'vitest';
import { cleanup, render, screen, fireEvent } from '@testing-library/svelte';
import UpdatesPage from './UpdatesPage.svelte';
import { updatesApi, ApiError, updateStateLabel, previewEnvelope } from '../lib/api.js';

vi.mock('../lib/api.js', async (orig) => {
	const mod = (await orig()) as Record<string, unknown>;
	return {
		...mod,
		updatesApi: {
			releases: vi.fn(),
			importRelease: vi.fn(),
			setReleaseStatus: vi.fn(),
			rollouts: vi.fn(),
			createRollout: vi.fn(),
			rolloutAction: vi.fn(),
			widenRollout: vi.fn(),
			targets: vi.fn(),
			fleet: vi.fn()
		}
	};
});

const api = updatesApi as unknown as Record<string, ReturnType<typeof vi.fn>>;
const STORE = '11111111-1111-4111-8111-111111111111';

afterEach(() => {
	cleanup();
	vi.resetAllMocks();
});

function envelopeText(files: string[]): string {
	const manifest = { version: '1.4.0', release_sequence: 14, artifacts: files.map((f) => ({ file_name: f })) };
	return JSON.stringify({ format: 'moonlight-release-envelope-v1', key_id: 'k', manifest: btoa(JSON.stringify(manifest)), signature: 's' });
}

function release(id: string, status: 'ACTIVE' | 'REVOKED') {
	return {
		id, manifest_digest: 'd', release_sequence: 14, version: '1.4.0', build_commit: 'a'.repeat(40), min_installed_sequence: 0,
		key_id: 'kid', status, imported_by: 'op', imported_at: '2026-10-01T00:00:00Z', status_changed_by: 'op',
		status_changed_at: '2026-10-01T00:00:00Z', artifacts: []
	};
}

describe('update state labels', () => {
	it('never reports success for states before Retail confirmed health', () => {
		for (const s of ['PENDING', 'DELIVERED', 'DOWNLOADING', 'VERIFIED', 'WAITING_SAFE_BOUNDARY', 'INSTALLING', 'AWAITING_HEALTH']) {
			expect(updateStateLabel(s)).not.toMatch(/Succeeded|Updated/);
		}
		expect(updateStateLabel('SUCCEEDED')).toMatch(/Succeeded/);
		expect(updateStateLabel('ROLLED_BACK')).toMatch(/Rolled back/);
	});

	it('preview tolerates garbage and only lists signed file names', () => {
		expect(previewEnvelope('not json')).toBeNull();
		expect(previewEnvelope('{"manifest":"!!"}')).toBeNull();
		expect(previewEnvelope(envelopeText(['a.tar.gz']))).toEqual({ version: '1.4.0', sequence: 14, files: ['a.tar.gz'] });
	});
});

describe('UpdatesPage', () => {
	it('renders fleet with truthful capability and target state', async () => {
		api.fleet.mockResolvedValue({
			devices: [
				{ device_id: 'd1', device_name: 'till-1', device_status: 'active', store_id: STORE, version: '1.3.0', release_sequence: 13,
					updater_protocol: 1, updater_capable: true, updater_reported: true, target_state: 'WAITING_SAFE_BOUNDARY',
					target_version: '1.4.0', target_sequence: 14 },
				{ device_id: 'd2', device_name: 'old', device_status: 'active', store_id: STORE, release_sequence: 0, updater_protocol: 0,
					updater_capable: false, updater_reported: false }
			],
			next_cursor: ''
		});
		render(UpdatesPage, { store: STORE });
		const rows = await screen.findAllByTestId('fleet-row');
		expect(rows).toHaveLength(2);
		expect(api.fleet).toHaveBeenCalledWith(STORE, undefined);
		expect(rows[0].textContent).toContain('Waiting for safe boundary');
		expect(rows[0].textContent).not.toContain('Succeeded');
		expect(rows[1].textContent).toContain('Not reported');
	});

	it('drops the previous Store fleet as soon as the Store changes', async () => {
		const OTHER = '22222222-2222-4222-8222-222222222222';
		const row = (store: string, name: string) => ({ device_id: name, device_name: name, device_status: 'active', store_id: store,
			release_sequence: 1, updater_protocol: 1, updater_capable: true, updater_reported: true });
		let releaseB!: (v: unknown) => void;
		api.fleet.mockImplementation((s: string) =>
			s === STORE ? Promise.resolve({ devices: [row(STORE, 'A-TILL')], next_cursor: '' }) : new Promise((r) => (releaseB = r))
		);
		const { rerender } = render(UpdatesPage, { store: STORE });
		expect((await screen.findByTestId('fleet-row')).textContent).toContain('A-TILL');
		await rerender({ store: OTHER });
		expect(screen.queryByText('A-TILL')).toBeNull(); // B still pending
		releaseB({ devices: [row(OTHER, 'B-TILL')], next_cursor: '' });
		expect((await screen.findByTestId('fleet-row')).textContent).toContain('B-TILL');
		expect(screen.queryByText('A-TILL')).toBeNull();
	});

	it('imports a signed envelope with a URL per signed artifact and surfaces server rejection', async () => {
		api.fleet.mockResolvedValue({ devices: [], next_cursor: '' });
		api.releases.mockResolvedValue({ releases: [] });
		api.importRelease.mockRejectedValueOnce(new ApiError(400, 'INVALID_INPUT', 'request failed (400)', 'UPDATE_SIGNATURE_INVALID'));
		render(UpdatesPage, { store: STORE });
		await fireEvent.click(await screen.findByText('الإصدارات / Releases'));
		const text = (await screen.findByTestId('envelope-text')) as HTMLTextAreaElement;
		const env = envelopeText(['moonlight-linux-amd64.tar.gz']);
		await fireEvent.input(text, { target: { value: env } });
		const url = (await screen.findByTestId('artifact-url')) as HTMLInputElement;
		await fireEvent.input(url, { target: { value: 'https://artifacts.example.test/m.tar.gz' } });
		await fireEvent.click(await screen.findByTestId('import-release'));
		expect(api.importRelease).toHaveBeenCalledWith(env, { 'moonlight-linux-amd64.tar.gz': 'https://artifacts.example.test/m.tar.gz' });
		expect((await screen.findByTestId('updates-notice')).textContent).toContain('signature or manifest rejected');
	});

	it('creates a store-scoped draft rollout and starts it', async () => {
		api.fleet.mockResolvedValue({ devices: [], next_cursor: '' });
		api.releases.mockResolvedValue({ releases: [release('r1', 'ACTIVE'), release('r2', 'REVOKED')] });
		const draft = { id: 'ro1', release_id: 'r1', release_version: '1.4.0', release_sequence: 14, scope: 'STORE', store_id: STORE,
			mode: 'MANDATORY', percentage: 10, status: 'DRAFT', target_count: 3, created_by: 'op', created_at: '', updated_at: '',
			counts: { PENDING: 1, NOT_SELECTED: 2 } };
		api.rollouts.mockResolvedValue({ rollouts: [draft], next_cursor: '' });
		api.createRollout.mockResolvedValue(draft);
		api.rolloutAction.mockResolvedValue({ ...draft, status: 'ACTIVE' });
		render(UpdatesPage, { store: STORE });
		await fireEvent.click(await screen.findByText('الطرح / Rollouts'));
		const select = (await screen.findByTestId('rollout-release')) as HTMLSelectElement;
		// Revoked releases are not offered for new rollouts.
		expect(Array.from(select.options).map((o) => o.value)).toEqual(['', 'r1']);
		await fireEvent.change(select, { target: { value: 'r1' } });
		await fireEvent.click(await screen.findByTestId('create-rollout'));
		expect(api.createRollout).toHaveBeenCalledWith(expect.objectContaining({ release_id: 'r1', scope: 'STORE', store_id: STORE, device_id: undefined }));
		const row = await screen.findByTestId('rollout-row');
		expect(row.textContent).toContain('Pending: 1');
		// Starting a MANDATORY rollout requires an explicit confirmation.
		const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(false);
		await fireEvent.click(await screen.findByText('بدء / Start'));
		expect(confirmSpy).toHaveBeenCalledOnce();
		expect(api.rolloutAction).not.toHaveBeenCalled();
		confirmSpy.mockReturnValue(true);
		await fireEvent.click(await screen.findByText('بدء / Start'));
		expect(api.rolloutAction).toHaveBeenCalledWith('ro1', 'start');
		confirmSpy.mockRestore();
	});

	it('requires a Store before a Store-scoped rollout can be created', async () => {
		api.fleet.mockResolvedValue({ devices: [], next_cursor: '' });
		api.releases.mockResolvedValue({ releases: [release('r1', 'ACTIVE')] });
		api.rollouts.mockResolvedValue({ rollouts: [], next_cursor: '' });
		render(UpdatesPage, { store: '' });
		await fireEvent.click(await screen.findByText('الطرح / Rollouts'));
		const select = (await screen.findByTestId('rollout-release')) as HTMLSelectElement;
		await fireEvent.change(select, { target: { value: 'r1' } });
		expect(((await screen.findByTestId('create-rollout')) as HTMLButtonElement).disabled).toBe(true);
	});
});
