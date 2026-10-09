import { request } from '@playwright/test';

// Test-owned report data goes through the real device sync API (the same
// path Retail uses), never a global seed: a spec ingests exactly the sales it
// asserts on, with deterministic event IDs so re-runs are idempotent replays.
// The runner (scripts/e2e-dashboard.sh) provisions the device credential and
// registers its Store through the device API.

export interface SaleEvent {
	event_id: string;
	occurred_at: string;
	payload: Record<string, unknown>;
}

function credential(name: string): string {
	const v = process.env[name];
	if (!v) throw new Error(`${name} must be set (device credential provisioned by scripts/e2e-dashboard.sh)`);
	return v;
}

export async function ingestSales(credentialEnv: string, events: SaleEvent[]): Promise<void> {
	const origin = new URL(process.env.E2E_BASE_URL ?? 'http://127.0.0.1:8080/dashboard/').origin;
	const ctx = await request.newContext({ baseURL: origin, extraHTTPHeaders: { Authorization: `Bearer ${credential(credentialEnv)}` } });
	try {
		const res = await ctx.post('/api/v1/sync/batches', {
			data: { events: events.map((e) => ({ event_id: e.event_id, event_type: 'sale.finalized.v2', occurred_at: e.occurred_at, payload: e.payload })) }
		});
		if (res.status() !== 200) throw new Error(`sync batch: ${res.status()} ${await res.text()}`);
	} finally {
		await ctx.dispose();
	}
}
