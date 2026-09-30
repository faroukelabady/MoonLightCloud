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
	subtotal_minor: string;
	discount_minor: string;
	tax_minor: string;
	sales_total_minor: string;
	line_cost_minor: string;
	refund_total_minor: string;
	net_sales_minor: string;
	returned_units: number;
	returned_cost_minor: string;
	net_cost_minor: string;
}

export interface Freshness {
	latest_sale_event_received_at: string | null;
	latest_projected_sale_occurred_at: string | null;
	projection_backlog_count: number;
	blocked_sale_event_count: number;
	cloud_projection_complete: boolean;
	latest_return_event_received_at: string | null;
	latest_projected_return_occurred_at: string | null;
	return_backlog_count: number;
	return_blocked_count: number;
	return_projection_complete: boolean;
}

export interface OverviewResponse {
	generated_at: string;
	timezone: string;
	period: PeriodMeta;
	summary: {
		transaction_count: number;
		units_sold: number;
		return_transaction_count: number;
		units_returned: number;
		currency_totals: CurrencyBucket[];
	};
	normalized: {
		normalized_total_minor: string;
		normalized_refund_minor: string;
		normalized_net_minor: string;
		transactions: number;
		units: number;
		return_transactions: number;
		units_returned: number;
		usd_sale_count: number;
	};
	averages: { all: ModeAverage; egp: ModeAverage; usd: ModeAverage };
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
	units: number;
	return_transactions: number;
	units_returned: number;
	amount_minor: string;
	refund_minor: string;
}

export interface DailyResponse {
	timezone: string;
	period: PeriodMeta;
	mode: 'all' | 'EGP' | 'USD';
	display_currency: string;
	normalized: boolean;
	days: DailyRow[];
}

export interface ModeAverage {
	transactions: number;
	units: number;
	average_minor: string;
}

// Endpoint-specific request contracts. Daily and breakdown endpoints use
// different mode vocabularies; distinct types make cross-use a compile
// error instead of a runtime 400.
export type CurrencySelection = 'all' | 'EGP' | 'USD';
export type DailyMode = CurrencySelection;
export type BreakdownMode = 'all' | 'native';
export type CategoryKind = 'root_category' | 'subcategory';

export interface ProductRow {
	product_id?: string | null;
	sku: string;
	product_name: string;
	units: number;
	units_returned: number;
	amount_minor: string;
	refund_minor: string;
	net_minor: string;
}

export interface CategoryRow {
	kind: string;
	classification_id: string;
	name_ar: string;
	name_en: string;
	units: number;
	units_returned: number;
	amount_minor: string;
	refund_minor: string;
	net_minor: string;
}

export interface BranchRow {
	shop_name_ar: string;
	shop_name_en: string;
	shop_phone: string;
	channel: string;
	currency: string;
	transactions: number;
	units: number;
	return_transactions: number;
	units_returned: number;
	subtotal_minor: string;
	sales_total_minor: string;
	refund_total_minor: string;
	returned_cost_minor: string;
}

export interface ActivityItem {
	kind: string;
	event_id: string;
	event_type: string;
	timestamp: string;
	device_name?: string | null;
	detail?: string | null;
}

export interface OrderSummary {
	provider_key: string;
	external_order_id: string;
	order_number: string;
	provider_status: string;
	canonical_status: string;
	currency: string;
	total_minor: string;
	created_at: string;
	modified_at: string;
	customer_name: string;
	mapping_complete: boolean;
	unmapped_line_count: number;
	provider_deleted: boolean;
	revision: number;
}

export interface OrderLineView {
	external_line_id: number;
	external_product_id: string;
	variation_id: number;
	sku: string;
	name: string;
	quantity: number;
	total_minor: string;
	moonlight_product_id: string | null;
	mapped: boolean;
}

export interface OrderAddressView {
	kind: string;
	first_name: string;
	last_name: string;
	company: string;
	address_1: string;
	address_2: string;
	city: string;
	state: string;
	postcode: string;
	country: string;
	email: string;
	phone: string;
}

export interface OrderStatusEvent {
	order_revision: number;
	provider_status: string;
	canonical_status: string;
	observed_at: string;
}

export interface OrderDetail {
	summary: OrderSummary;
	discount_minor: string;
	shipping_minor: string;
	cart_tax_minor: string;
	total_tax_minor: string;
	prices_include_tax: boolean;
	paid_at: string | null;
	completed_at: string | null;
	payment_method: string;
	payment_method_title: string;
	customer_first_name: string;
	customer_last_name: string;
	customer_email: string;
	customer_phone: string;
	lines: OrderLineView[];
	addresses: OrderAddressView[];
	status_history: OrderStatusEvent[];
}

export interface OrderStatusCount {
	canonical_status: string;
	total: number;
}

export interface WebhookInboxStats {
	pending: number;
	retry: number;
	blocked: number;
	oldest_pending_at: string | null;
}

export interface OrderListResponse {
	orders: OrderSummary[];
	next_cursor: string | null;
	status_counts: OrderStatusCount[];
	webhook_inbox: WebhookInboxStats;
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

export interface DeviceCommandWire {
	id: string;
	type: string;
	version: number;
	lease_generation: number;
	requested_at: string;
	status: string;
	accepted_at?: string | null;
	running_at?: string | null;
	finished_at?: string | null;
	result_code?: string | null;
}

export interface DeviceRow {
	device_id: string;
	name?: string;
	lifecycle: string;
	connectivity: string;
	last_seen_at?: string | null;
	active_command?: DeviceCommandWire | null;
	recent_commands?: DeviceCommandWire[];
}

export interface IncidentRow {
	id: string;
	rule: string;
	subject_type: string;
	subject_id: string;
	severity: string;
	state: string;
	episode: number;
	opened_at: string;
	last_observed_at: string;
	acknowledged_at?: string | null;
	resolved_at?: string | null;
	resolution_code?: string | null;
	open_intent_materialized: boolean;
	resolved_intent_materialized: boolean;
}

export interface IncidentDelivery {
	id: string;
	event: string;
	provider_key: string;
	recipient_masked: string;
	locale: string;
	template_key: string;
	status: string;
	has_notification: boolean;
}

export interface IncidentRecovery {
	id: string;
	action_type: string;
	state: string;
	target_entity_id?: string | null;
	result_code?: string | null;
	attempt_count: number;
}

export interface DeviceIncidentSummary {
	device_id: string;
	open_count: number;
	max_severity: string;
}

export interface SyncRequestResponse {
	command: DeviceCommandWire;
	created: boolean;
}

export interface SyncHealth {
	freshness: Freshness;
	queue_count: number;
	pending_count: number;
	processed_count: number;
	blocked_count: number;
	retry_count: number;
	return_pending_count: number;
	return_processed_count: number;
	return_blocked_count: number;
	return_retry_count: number;
	return_last_error_event?: string;
	return_last_error_code?: string;
	return_last_error_label_ar?: string;
	return_last_error_label_en?: string;
	oldest_pending_at?: string | null;
	last_error_event?: string;
	last_error_code?: string;
	last_error_label_ar?: string;
	last_error_label_en?: string;
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
	daily: (p: PeriodParams, mode: DailyMode, s?: AbortSignal) =>
		get<DailyResponse>(`/api/v1/dashboard/daily?${query(p)}&mode=${mode}`, s),
	products: (p: PeriodParams, mode: BreakdownMode, currency: string, s?: AbortSignal) =>
		get<{ rows: ProductRow[] }>(`/api/v1/dashboard/products?${query(p)}&mode=${mode}&currency=${currency}`, s),
	categories: (p: PeriodParams, kind: CategoryKind, mode: BreakdownMode, currency: string, s?: AbortSignal) =>
		get<{ rows: CategoryRow[] }>(`/api/v1/dashboard/categories?${query(p)}&kind=${kind}&mode=${mode}&currency=${currency}`, s),
	branches: (p: PeriodParams, s?: AbortSignal) =>
		get<{ rows: BranchRow[] }>(`/api/v1/dashboard/branches?${query(p)}`, s),
	syncHealth: (s?: AbortSignal) => get<SyncHealth>('/api/v1/dashboard/sync-health', s),
	activity: (s?: AbortSignal) => get<{ items: ActivityItem[] }>('/api/v1/dashboard/activity?limit=20', s),
	latestSales: (s?: AbortSignal) => get<{ sales: LatestSale[] }>('/api/v1/dashboard/sales/latest?limit=8', s),
	orders: (status: string, provider: string, cursor?: string | null, s?: AbortSignal) =>
		get<OrderListResponse>(
			`/api/v1/dashboard/orders?status=${encodeURIComponent(status)}&provider=${encodeURIComponent(provider)}&limit=20${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`,
			s
		),
	orderDetail: (provider: string, id: string, s?: AbortSignal) =>
		get<OrderDetail>(`/api/v1/dashboard/orders/${encodeURIComponent(provider)}/${encodeURIComponent(id)}`, s),
	devices: (s?: AbortSignal) => get<{ devices: DeviceRow[] }>('/api/v1/dashboard/devices', s),
	incidents: (params: { state?: string; severity?: string; rule?: string; limit?: number; cursor?: string | null }, s?: AbortSignal) => {
		const q = new URLSearchParams();
		if (params.state) q.set('state', params.state);
		if (params.severity) q.set('severity', params.severity);
		if (params.rule) q.set('rule', params.rule);
		if (params.limit) q.set('limit', String(params.limit));
		if (params.cursor) q.set('cursor', params.cursor);
		return get<{ incidents: IncidentRow[]; next_cursor: string }>(`/api/v1/dashboard/operations/incidents?${q.toString()}`, s);
	},
	incidentDetail: (id: string, s?: AbortSignal) =>
		get<{ incident: IncidentRow; deliveries: IncidentDelivery[]; recoveries: IncidentRecovery[] }>(
			`/api/v1/dashboard/operations/incidents/${encodeURIComponent(id)}`, s
		),
	incidentAck: async (id: string): Promise<IncidentRow> => {
		const res = await fetch(`/api/v1/dashboard/operations/incidents/${encodeURIComponent(id)}/acknowledge`, {
			method: 'POST',
			credentials: 'same-origin'
		});
		if (!res.ok) throw new ApiError(res.status, 'REQUEST_FAILED', `request failed (${res.status})`);
		const body = (await res.json()) as { incident: IncidentRow };
		return body.incident;
	},
	incidentResolve: async (id: string): Promise<IncidentRow> => {
		const res = await fetch(`/api/v1/dashboard/operations/incidents/${encodeURIComponent(id)}/resolve`, {
			method: 'POST',
			credentials: 'same-origin'
		});
		if (!res.ok) throw new ApiError(res.status, 'REQUEST_FAILED', `request failed (${res.status})`);
		const body = (await res.json()) as { incident: IncidentRow };
		return body.incident;
	},
	operationsSummary: (s?: AbortSignal) =>
		get<{ devices: DeviceIncidentSummary[] }>('/api/v1/dashboard/operations/summary', s),
	syncRequest: async (deviceId: string, idempotencyKey: string): Promise<SyncRequestResponse> => {
		const res = await fetch(`/api/v1/dashboard/devices/${encodeURIComponent(deviceId)}/sync-requests`, {
			method: 'POST',
			headers: { 'Idempotency-Key': idempotencyKey },
			credentials: 'same-origin'
		});
		if (res.status === 401) throw new ApiError(401, 'UNAUTHORIZED', 'session required');
		if (!res.ok) {
			let code = 'INTERNAL';
			try {
				const body = (await res.json()) as { error?: { code?: string; message?: string } };
				if (body.error?.message) code = body.error.message;
				else if (body.error?.code) code = body.error.code;
			} catch {
				/* keep generic */
			}
			throw new ApiError(res.status, code, `request failed (${res.status})`);
		}
		return (await res.json()) as SyncRequestResponse;
	}
};
