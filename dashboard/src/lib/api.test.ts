import { describe, expect, it, vi, beforeEach } from 'vitest';
import { dashboardApi, ApiError } from './api.js';

function jsonResponse(status: number, body: unknown): Response {
	return new Response(JSON.stringify(body), { status });
}

describe('dashboardApi', () => {
	beforeEach(() => {
		vi.stubGlobal(
			'fetch',
			vi.fn(async (url: string) => {
				if (String(url).includes('/auth/me')) return jsonResponse(200, { authenticated: true, username: 'op' });
				return jsonResponse(200, {});
			})
		);
	});

	it('maps 401 to session error', async () => {
		vi.stubGlobal('fetch', vi.fn(async () => jsonResponse(401, { error: { code: 'UNAUTHORIZED' } })));
		await expect(dashboardApi.me()).rejects.toMatchObject({ status: 401 });
	});

	it('maps 503 to unavailable without leaking body', async () => {
		vi.stubGlobal('fetch', vi.fn(async () => jsonResponse(503, { error: { code: 'UNAVAILABLE' } })));
		try {
			await dashboardApi.me();
			expect.unreachable();
		} catch (err) {
			expect(err).toBeInstanceOf(ApiError);
			expect((err as ApiError).status).toBe(503);
		}
	});

	it('passes AbortSignal through for stale-request cancellation', async () => {
		const fetchMock = vi.fn(async () => jsonResponse(200, { authenticated: true }));
		vi.stubGlobal('fetch', fetchMock);
		const c = new AbortController();
		await dashboardApi.me(c.signal);
		expect(fetchMock).toHaveBeenCalledWith(expect.anything(), expect.objectContaining({ signal: c.signal }));
	});
});

describe('endpoint request contracts', () => {
	async function lastUrl(fn: () => Promise<unknown>) {
		const fetchMock = vi.fn(async () => jsonResponse(200, { rows: [], days: [] }));
		vi.stubGlobal('fetch', fetchMock);
		await fn().catch(() => {});
		const calls = fetchMock.mock.calls as unknown[][];
		if (calls.length === 0) throw new Error('fetch not called');
		return String(calls[0][0]);
	}

	it('daily uses all|EGP|USD vocabulary, never native', async () => {
		for (const m of ['all', 'EGP', 'USD'] as const) {
			const url = await lastUrl(() => dashboardApi.daily({ period: 'today' }, m, ''));
			expect(url).toContain(`mode=${m}`);
		}
		const url = await lastUrl(() => dashboardApi.daily({ period: 'today' }, 'EGP', ''));
		expect(url).not.toContain('mode=native');
	});

	it('categories sends the canonical root_category kind', async () => {
		const url = await lastUrl(() =>
			dashboardApi.categories({ period: 'today' }, 'root_category', 'all', '', '')
		);
		expect(url).toContain('kind=root_category');
		expect(url).not.toContain('kind=root&');
	});

	it('products keeps the all|native vocabulary', async () => {
		const url = await lastUrl(() => dashboardApi.products({ period: 'today' }, 'native', 'EGP', ''));
		expect(url).toContain('mode=native');
	});
});

describe('store scope contracts', () => {
	async function lastUrl(fn: () => Promise<unknown>) {
		const fetchMock = vi.fn(async () => jsonResponse(200, { rows: [], days: [] }));
		vi.stubGlobal('fetch', fetchMock);
		await fn().catch(() => {});
		const calls = fetchMock.mock.calls as unknown[][];
		if (calls.length === 0) throw new Error('fetch not called');
		return String(calls[0][0]);
	}

	it('omits store_id for the global scope', async () => {
		const url = await lastUrl(() => dashboardApi.overview({ period: 'today' }, ''));
		expect(url).not.toContain('store_id');
	});

	it('appends a validated store_id for specific scope', async () => {
		const id = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa';
		const url = await lastUrl(() => dashboardApi.overview({ period: 'today' }, id));
		expect(url).toContain(`store_id=${id}`);
	});

	it('threads store through orders list and detail', async () => {
		const id = 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb';
		const list = await lastUrl(() => dashboardApi.orders('', '', null, id));
		expect(list).toContain(`store_id=${id}`);
		const detail = await lastUrl(() => dashboardApi.orderDetail('website', '100', id));
		expect(detail).toContain(`store_id=${id}`);
		const global = await lastUrl(() => dashboardApi.orderDetail('website', '100', ''));
		expect(global).not.toContain('store_id');
	});

	it('validates store UUID shape client-side', async () => {
		const { isStoreID } = await import('./api.js');
		expect(isStoreID('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa')).toBe(true);
		expect(isStoreID('')).toBe(false);
		expect(isStoreID('not-a-uuid')).toBe(false);
		expect(isStoreID('00000000-0000-0000-0000-000000000000')).toBe(true);
	});
});
