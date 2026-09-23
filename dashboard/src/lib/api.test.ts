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
