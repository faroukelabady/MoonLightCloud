// Typed dashboard BFF client. One place for fetch, auth errors, and
// request cancellation (AbortController per navigation/filter change so
// slower old responses never overwrite newer state). Money stays string.

export class ApiError extends Error {
	status: number;
	code: string;
	constructor(status: number, code: string, message: string) {
		super(message);
		this.status = status;
		this.code = code;
	}
}

export interface PeriodMeta {
	kind: string;
	timezone: string;
	start_local: string;
	end_local_exclusive: string;
	start_utc: string;
	end_utc: string;
}

export interface CurrencyBucket {
	currency: string;
	subtotal_minor: number;
	discount_minor: number;
	tax_minor: number;
	sales_total_minor: number;
	line_cost_minor?: number;
}

export interface Freshness {
	latest_sale_event_received_at: string | null;
	latest_projected_sale_occurred_at: string | null;
	projection_backlog_count: number;
	blocked_sale_event_count: number;
	cloud_projection_complete: boolean;
}

export interface OverviewResponse {
	generated_at: string;
	timezone: string;
	period: PeriodMeta;
	summary: {
		transaction_count: number;
		units_sold: number;
		currency_totals: CurrencyBucket[];
	};
	normalized: { normalized_total_minor: string; transactions: number; units: number; usd_sale_count: number };
	fx: {
		has_usd: boolean;
		latest_rate?: string;
		latest_rate_microrate?: string;
		latest_occurred_at?: string;
		min_rate_microrate?: string;
		max_rate_microrate?: string;
		multiple_rates_used: boolean;
	};
}

export interface DailyRow {
	date: string;
	transactions: number;
	normalized_minor: string;
}

export interface ProductRow {
	product_id?: string | null;
	sku: string;
	product_name: string;
	units: number;
	amount_minor: string;
}

export interface CategoryRow {
	kind: string;
	classification_id: string;
	name_ar: string;
	name_en: string;
	units: number;
	amount_minor: string;
}

export interface BranchRow {
	shop_name_ar: string;
	shop_name_en: string;
	shop_phone: string;
	channel: string;
	currency: string;
	transactions: number;
	units: number;
	subtotal_minor: string;
	sales_total_minor: string;
}

export interface ActivityItem {
	kind: string;
	event_id: string;
	event_type: string;
	timestamp: string;
	device_name?: string | null;
	detail?: string | null;
}

export interface LatestSale {
	sale_id: string;
	sale_number: string;
	channel: string;
	occurred_at: string;
	currency: string;
	total_minor: string;
	cashier_name?: string | null;
}

export interface SyncHealth {
	freshness: Freshness;
	pending_count: number;
	processed_count: number;
	blocked_count: number;
	retry_count: number;
	oldest_pending_at?: string | null;
	last_error_event?: string;
	last_error_code?: string;
	last_error_message?: string;
}

export interface PeriodParams {
	period: string;
	from_date?: string;
	to_date?: string;
}

function query(p: PeriodParams): string {
	const q = new URLSearchParams({ period: p.period });
	if (p.from_date) q.set('from_date', p.from_date);
	if (p.to_date) q.set('to_date', p.to_date);
	return q.toString();
}

async function get<T>(path: string, signal?: AbortSignal): Promise<T> {
	const res = await fetch(path, { signal, credentials: 'same-origin' });
	if (res.status === 401) throw new ApiError(401, 'UNAUTHORIZED', 'session required');
	if (res.status === 503) throw new ApiError(503, 'UNAVAILABLE', 'reporting temporarily unavailable');
	if (!res.ok) {
		let code = 'INTERNAL';
		try {
			const body = (await res.json()) as { error?: { code?: string; message?: string } };
			if (body.error?.code) code = body.error.code;
		} catch {
			/* keep generic */
		}
		throw new ApiError(res.status, code, `request failed (${res.status})`);
	}
	return (await res.json()) as T;
}

export const dashboardApi = {
	me: (s?: AbortSignal) => get<{ authenticated: boolean; username: string }>('/api/v1/dashboard/auth/me', s),
	login: async (username: string, password: string): Promise<void> => {
		const res = await fetch('/api/v1/dashboard/auth/login', {
			method: 'POST',
			headers: { 'content-type': 'application/json' },
			body: JSON.stringify({ username, password }),
			credentials: 'same-origin'
		});
		if (!res.ok) throw new ApiError(res.status, 'UNAUTHORIZED', 'invalid operator credentials');
	},
	logout: async (): Promise<void> => {
		await fetch('/api/v1/dashboard/auth/logout', { method: 'POST', credentials: 'same-origin' });
	},
	overview: (p: PeriodParams, s?: AbortSignal) => get<OverviewResponse>(`/api/v1/dashboard/overview?${query(p)}`, s),
	daily: (p: PeriodParams, s?: AbortSignal) =>
		get<{ days: DailyRow[] }>(`/api/v1/dashboard/daily?${query(p)}`, s),
	products: (p: PeriodParams, mode: 'all' | 'native', currency: string, s?: AbortSignal) =>
		get<{ rows: ProductRow[] }>(`/api/v1/dashboard/products?${query(p)}&mode=${mode}&currency=${currency}`, s),
	categories: (p: PeriodParams, kind: string, mode: 'all' | 'native', currency: string, s?: AbortSignal) =>
		get<{ rows: CategoryRow[] }>(`/api/v1/dashboard/categories?${query(p)}&kind=${kind}&mode=${mode}&currency=${currency}`, s),
	branches: (p: PeriodParams, s?: AbortSignal) =>
		get<{ rows: BranchRow[] }>(`/api/v1/dashboard/branches?${query(p)}`, s),
	syncHealth: (s?: AbortSignal) => get<SyncHealth>('/api/v1/dashboard/sync-health', s),
	activity: (s?: AbortSignal) => get<{ items: ActivityItem[] }>('/api/v1/dashboard/activity?limit=20', s),
	latestSales: (s?: AbortSignal) => get<{ sales: LatestSale[] }>('/api/v1/dashboard/sales/latest?limit=8', s)
};
