import { test, expect, type Page, type Route } from '@playwright/test';
import { uiLogin } from './auth';

// Phase 18 Updates page (ADR-0051/0052). Sign-in is real (production cookie
// over HTTPS in scripts/e2e-dashboard.sh); the release/rollout/fleet data
// routes are fixtures, so no real signed release is needed to exercise the
// page contract: tabs, permission denial and Store-scope isolation.

const STORE_A = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa';
const STORE_B = 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb';

function device(store: string, name: string) {
	return {
		device_id: store === STORE_A ? 'd0000000-0000-4000-8000-00000000000a' : 'd0000000-0000-4000-8000-00000000000b',
		device_name: name, device_status: 'active', store_id: store, last_seen_at: '2026-10-01T10:00:00Z',
		version: '1.4.0', release_sequence: 14, updater_protocol: 1, updater_capable: true, updater_reported: true,
		update_state: 'IDLE'
	};
}

const RELEASE = {
	id: 'r0000000-0000-4000-8000-000000000001', manifest_digest: 'sha256:' + 'ab'.repeat(32), release_sequence: 15,
	version: '1.5.0', build_commit: 'c'.repeat(40), min_installed_sequence: 1, key_id: 'e2e-key', status: 'ACTIVE',
	imported_by: 'owner@e2e.test', imported_at: '2026-10-01T09:00:00Z', status_changed_by: '', status_changed_at: '2026-10-01T09:00:00Z',
	artifacts: []
};

const ROLLOUT = {
	id: 'o0000000-0000-4000-8000-000000000001', release_id: RELEASE.id, release_version: '1.5.0', release_sequence: 15,
	scope: 'STORE', store_id: STORE_A, mode: 'OPTIONAL', percentage: 50, status: 'ACTIVE', target_count: 2,
	created_by: 'owner@e2e.test', created_at: '2026-10-01T09:30:00Z', updated_at: '2026-10-01T09:30:00Z', counts: { DELIVERED: 1 }
};

const json = (r: Route, status: number, body: unknown) => r.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
const denied = (r: Route) => json(r, 403, { error: { code: 'FORBIDDEN', message: 'forbidden' } });

interface Opts {
	fleetB?: 'slow' | 'fail';
	releases?: 'ok' | 'forbidden';
	rollouts?: 'ok' | 'forbidden';
}

async function mockUpdates(page: Page, opts: Opts = {}) {
	await page.route('**/api/v1/dashboard/fleet*', async (r) => {
		const store = new URL(r.request().url()).searchParams.get('store_id') ?? '';
		if (store === STORE_B && opts.fleetB === 'slow') await new Promise((res) => setTimeout(res, 1500));
		if (store === STORE_B && opts.fleetB === 'fail') return json(r, 500, { error: { code: 'INTERNAL', message: 'boom' } });
		const devices = store === STORE_A ? [device(STORE_A, 'A-TILL')] : store === STORE_B ? [device(STORE_B, 'B-TILL')] : [];
		return json(r, 200, { devices, next_cursor: '' });
	});
	await page.route('**/api/v1/dashboard/releases', (r) => (opts.releases === 'forbidden' ? denied(r) : json(r, 200, { releases: [RELEASE] })));
	await page.route('**/api/v1/dashboard/rollouts*', (r) =>
		opts.rollouts === 'forbidden' ? denied(r) : json(r, 200, { rollouts: [ROLLOUT], next_cursor: '' })
	);
}

async function openUpdates(page: Page, store = STORE_A) {
	await uiLogin(page, `updates?store_id=${store}`);
	await expect(page.getByTestId('updates-page')).toBeVisible();
}

test('Updates page loads and the Fleet / Releases / Rollouts tabs render', async ({ page }) => {
	await mockUpdates(page);
	await openUpdates(page);
	await expect(page.getByRole('tablist')).toBeVisible();
	await expect(page.getByTestId('fleet-row')).toHaveCount(1);
	await expect(page.getByTestId('fleet-row')).toContainText('A-TILL');
	await page.getByRole('button', { name: /Releases/ }).click();
	await expect(page.getByTestId('release-row')).toHaveCount(1);
	await expect(page.getByTestId('release-row')).toContainText('1.5.0');
	await expect(page.getByTestId('import-release')).toHaveCount(0); // no envelope pasted yet
	await page.getByRole('button', { name: /Rollouts/ }).click();
	await expect(page.getByTestId('rollout-row')).toHaveCount(1);
	await expect(page.getByTestId('rollout-row')).toHaveAttribute('data-status', 'ACTIVE');
});

test('permission denial is shown truthfully, never as an empty registry', async ({ page }) => {
	await mockUpdates(page, { releases: 'forbidden', rollouts: 'forbidden' });
	await openUpdates(page);
	await page.getByRole('button', { name: /Releases/ }).click();
	await expect(page.getByTestId('updates-page').getByRole('alert')).toContainText(/Could not load releases/);
	await expect(page.getByTestId('release-row')).toHaveCount(0);
	await page.getByRole('button', { name: /Rollouts/ }).click();
	await expect(page.getByTestId('updates-page').getByRole('alert')).toContainText(/Could not load rollouts/);
	await expect(page.getByTestId('rollout-row')).toHaveCount(0);
});

test('Store switch never shows the previous Store fleet while the next one loads', async ({ page }) => {
	await mockUpdates(page, { fleetB: 'slow' });
	await openUpdates(page);
	await expect(page.getByTestId('fleet-row')).toContainText('A-TILL');
	await page.getByTestId('store-selector').selectOption(STORE_B);
	// Store B's response is held for 1.5 s: Store A's device must already be gone.
	await expect(page.getByText('A-TILL')).toHaveCount(0, { timeout: 1000 });
	await expect(page.getByTestId('fleet-row')).toContainText('B-TILL');
	await expect(page.getByText('A-TILL')).toHaveCount(0);
});

test('failed Store switch does not restore the previous Store fleet', async ({ page }) => {
	await mockUpdates(page, { fleetB: 'fail' });
	await openUpdates(page);
	await expect(page.getByTestId('fleet-row')).toContainText('A-TILL');
	await page.getByTestId('store-selector').selectOption(STORE_B);
	await expect(page.getByTestId('updates-page').getByRole('alert')).toContainText(/Could not load fleet/);
	await expect(page.getByText('A-TILL')).toHaveCount(0);
});
