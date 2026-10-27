// Shared fetch helpers for the dashboard BFF: one error mapping, the
// in-memory CSRF token and query-string builders.

export class ApiError extends Error {
	status: number;
	code: string;
	// detail is the server's error message (a stable reason such as
	// RELEASE_SEQUENCE_CONFLICT), when the server sent one.
	detail: string;
	constructor(status: number, code: string, message: string, detail = '') {
		super(message);
		this.status = status;
		this.code = code;
		this.detail = detail;
	}
}

export interface PeriodParams {
	period: string;
	from_date?: string;
	to_date?: string;
}

export function query(p: PeriodParams): string {
	const q = new URLSearchParams({ period: p.period });
	if (p.from_date) q.set('from_date', p.from_date);
	if (p.to_date) q.set('to_date', p.to_date);
	return q.toString();
}

// providerQuery appends the optional provider filter for operational

export function q(params: Record<string, string | number | undefined>): string {
	const out = new URLSearchParams();
	for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== '') out.set(k, String(v));
	const s = out.toString();
	return s ? `?${s}` : '';
}

// failure maps a non-OK response to ApiError with the server's code and
// message when it sent one.
async function failure(res: Response): Promise<ApiError> {
	let code = 'INTERNAL';
	let detail = '';
	try {
		const body = (await res.json()) as { error?: { code?: string; message?: string } };
		if (body.error?.code) code = body.error.code;
		if (body.error?.message) detail = body.error.message;
	} catch {
		/* keep generic */
	}
	return new ApiError(res.status, code, `request failed (${res.status})`, detail);
}

export async function get<T>(path: string, signal?: AbortSignal): Promise<T> {
	const res = await fetch(path, { signal, credentials: 'same-origin' });
	if (res.status === 401) throw new ApiError(401, 'UNAUTHORIZED', 'session required');
	if (res.status === 503) throw new ApiError(503, 'UNAVAILABLE', 'reporting temporarily unavailable');
	if (!res.ok) throw await failure(res);
	return (await res.json()) as T;
}

// CSRF (ADR-0053): the per-session token lives in memory only (never
// localStorage/sessionStorage/IndexedDB). The session itself is an
// HttpOnly cookie the page can never read. Every mutation sends the token.
let csrfToken = '';
export function setCSRFToken(token: string): void {
	csrfToken = token;
}
export function mutationHeaders(extra: Record<string, string> = {}): Record<string, string> {
	return { ...extra, 'X-CSRF-Token': csrfToken };
}

export async function postJson<T>(path: string, body: unknown, signal?: AbortSignal): Promise<T> {
	return sendJson<T>('POST', path, body, signal);
}

// sendJson sends one JSON mutation (POST, PUT, DELETE) with the CSRF token.
export async function sendJson<T>(method: 'POST' | 'PUT' | 'DELETE', path: string, body: unknown, signal?: AbortSignal): Promise<T> {
	const res = await fetch(path, {
		method,
		headers: mutationHeaders({ 'content-type': 'application/json' }),
		credentials: 'same-origin',
		body: JSON.stringify(body),
		signal
	});
	if (res.status === 401) throw new ApiError(401, 'UNAUTHORIZED', 'session required');
	if (!res.ok) throw await failure(res);
	return (await res.json()) as T;
}
