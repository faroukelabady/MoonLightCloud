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
	store_id?: string | null;
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
	store_id: string | null;
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
	store_id?: string;
	store_name?: string;
}

export interface StoreRow {
	store_id: string;
	display_name: string;
	timezone: string;
	status: string;
	device_count: number;
	created_at: string;
	updated_at: string;
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

// providerQuery appends the optional provider filter for operational
// analytics and catalog health ("" = all durable providers).
export function providerQuery(provider: string): string {
	return provider ? `&provider_key=${encodeURIComponent(provider)}` : '';
}

// storeQuery appends the Store ownership scope. Empty selects the global
// scope (all Stores plus legacy rows, backward compatible).
export function storeQuery(store: string): string {
	return store ? `&store_id=${encodeURIComponent(store)}` : '';
}

// isStoreID validates a Store scope value before it ever reaches the
// network: canonical UUID shape only.
export function isStoreID(value: string): boolean {
	return /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/.test(value);
}


// ---- Phase 12: Top Tags / Online analytics / Catalog health ----

export type TagCurrencyBucket = {
	currency: string;
	line_sales_minor: string;
	line_refund_minor: string;
	net_minor: string;
};
export type TagRow = {
	tag_id?: string;
	tag_slug?: string;
	name_ar?: string;
	name_en?: string;
	units: number;
	units_returned: number;
	currencies: TagCurrencyBucket[];
};
export type TagListResponse = {
	generated_at: string;
	timezone: string;
	store_id: string | null;
	rows: TagRow[];
	overlap_note: string;
};
export type OrderCurrencyTotal = {
	currency: string;
	orders: number;
	value_minor: string;
	active_orders: number;
	active_value_minor: string;
};
export type OrderAnalyticsStatusCount = { canonical_status: string; orders: number };
export type OrderProviderCount = {
	provider_key: string;
	currency: string;
	orders: number;
	value_minor: string;
};
export type OrderAnalyticsResponse = {
	generated_at: string;
	store_id: string | null;
	provider_key: string;
	active_statuses: string[];
	currency_totals: OrderCurrencyTotal[];
	status_counts: OrderAnalyticsStatusCount[];
	provider_totals: OrderProviderCount[];
};
export type CatalogHealthCount = { reason_code: string; products: number };
export type CatalogHealthItem = {
	reason_code: string;
	provider_key: string;
	product_id?: string;
	sku?: string;
	name?: string;
	store_id?: string;
};
export type CatalogHealthResponse = {
	providers?: string[];
	generated_at: string;
	store_id: string | null;
	provider_key: string;
	reason_codes: string[];
	counts: CatalogHealthCount[];
	detail: CatalogHealthItem[];
	detail_limit: number;
	detail_truncated: boolean;
};

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

export interface AdminProductRow {
	product_id: string;
	sku: string;
	name_ar: string;
	name_en: string;
	is_active: boolean;
	catalog_revision: number;
	sell_online: boolean;
	stock_quantity: number;
	configuration_revision: number;
	has_pending: boolean;
}

export interface AdminProductDetail extends AdminProductRow {
	description_ar: string;
	description_en: string;
	width_cm: number | null;
	height_cm: number | null;
	top_category_id: string;
	subcategory_ids: string[];
	tag_ids: string[];
	egp_price_minor: string;
	usd_price_minor: string | null;
	cost_minor: string;
	sell_offline: boolean;
	sales_policy_revision: number;
}

export interface AdminCategoryRow {
	category_id: string;
	name_ar: string;
	name_en: string;
	status: string;
	online_enabled: boolean;
	parent_ids: string[];
	catalog_revision: number;
	has_pending: boolean;
}

export interface AdminTagRow {
	tag_id: string;
	slug: string;
	name_ar: string;
	name_en: string;
	is_active: boolean;
	catalog_revision: number;
	has_pending: boolean;
}

export interface AdminConfiguration {
	id: string;
	style_code: string;
	style_name_ar: string;
	style_name_en: string | null;
	color_code: string;
	color_name_ar: string;
	color_name_en: string | null;
	egp_delta_minor: string;
	usd_delta_minor: string | null;
	enabled: boolean;
	position: number;
}

export interface AdminCommandTarget {
	id: string;
	command_id: string;
	device_id: string;
	device_name?: string;
	status: string;
	result_code?: string;
	entity_id?: string;
	pre_revision: number;
	post_revision: number;
	capable?: boolean | null;
}

export interface AdminCommand {
	id: string;
	store_id: string;
	type: string;
	version: number;
	entity_id: string;
	payload?: Record<string, unknown>;
	payload_hash: string;
	expected_revision: number;
	actor: string;
	status: string;
	aggregate: string;
	converged: boolean;
	targets?: AdminCommandTarget[];
	created_at: string;
	updated_at: string;
}

async function postJson<T>(path: string, body: unknown, signal?: AbortSignal): Promise<T> {
	const res = await fetch(path, {
		method: 'POST',
		headers: { 'content-type': 'application/json' },
		credentials: 'same-origin',
		body: JSON.stringify(body),
		signal
	});
	if (res.status === 401) throw new ApiError(401, 'UNAUTHORIZED', 'session required');
	if (!res.ok) {
		let code = 'INTERNAL';
		try {
			const parsed = (await res.json()) as { error?: { code?: string } };
			if (parsed.error?.code) code = parsed.error.code;
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
	overview: (p: PeriodParams, store: string, s?: AbortSignal) => get<OverviewResponse>(`/api/v1/dashboard/overview?${query(p)}${storeQuery(store)}`, s),
	daily: (p: PeriodParams, mode: DailyMode, store: string, s?: AbortSignal) =>
		get<DailyResponse>(`/api/v1/dashboard/daily?${query(p)}&mode=${mode}${storeQuery(store)}`, s),
	products: (p: PeriodParams, mode: BreakdownMode, currency: string, store: string, s?: AbortSignal) =>
		get<{ rows: ProductRow[] }>(`/api/v1/dashboard/products?${query(p)}&mode=${mode}&currency=${currency}&limit=10${storeQuery(store)}`, s),
	categories: (p: PeriodParams, kind: CategoryKind, mode: BreakdownMode, currency: string, store: string, s?: AbortSignal) =>
		get<{ rows: CategoryRow[] }>(`/api/v1/dashboard/categories?${query(p)}&kind=${kind}&mode=${mode}&currency=${currency}&limit=10${storeQuery(store)}`, s),
	branches: (p: PeriodParams, store: string, s?: AbortSignal) =>
		get<{ rows: BranchRow[] }>(`/api/v1/dashboard/branches?${query(p)}${storeQuery(store)}`, s),
	syncHealth: (s?: AbortSignal) => get<SyncHealth>('/api/v1/dashboard/sync-health', s),
	tags: (p: PeriodParams, currency: string, store: string, limit: number, s?: AbortSignal) =>
		get<TagListResponse>(`/api/v1/dashboard/tags?${query(p)}&currency=${currency}${storeQuery(store)}&limit=${limit}`, s),
	orderSummary: (p: PeriodParams, store: string, provider: string, currency: string, s?: AbortSignal) =>
		get<OrderAnalyticsResponse>(`/api/v1/dashboard/orders/summary?${query(p)}${storeQuery(store)}${providerQuery(provider)}&currency=${encodeURIComponent(currency)}`, s),
	catalogHealth: (store: string, provider: string, s?: AbortSignal) =>
		get<CatalogHealthResponse>(`/api/v1/dashboard/catalog-health?limit=50${storeQuery(store)}${providerQuery(provider)}`, s),
	activity: (store: string, s?: AbortSignal) => get<{ items: ActivityItem[] }>(`/api/v1/dashboard/activity?limit=20${storeQuery(store)}`, s),
	latestSales: (store: string, s?: AbortSignal) => get<{ sales: LatestSale[] }>(`/api/v1/dashboard/sales/latest?limit=8${storeQuery(store)}`, s),
	orders: (status: string, provider: string, cursor: string | null | undefined, store: string, s?: AbortSignal) =>
		get<OrderListResponse>(
			`/api/v1/dashboard/orders?status=${encodeURIComponent(status)}&provider=${encodeURIComponent(provider)}&limit=20${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}${storeQuery(store)}`,
			s
		),
	orderDetail: (provider: string, id: string, store: string, s?: AbortSignal) =>
		get<OrderDetail>(`/api/v1/dashboard/orders/${encodeURIComponent(provider)}/${encodeURIComponent(id)}${store ? `?store_id=${encodeURIComponent(store)}` : ''}`, s),
	devices: (s?: AbortSignal) => get<{ devices: DeviceRow[] }>('/api/v1/dashboard/devices', s),
	stores: (s?: AbortSignal) => get<{ stores: StoreRow[] }>('/api/v1/dashboard/stores', s),
	adminProducts: (store: string, search: string, cursor: string | null, s?: AbortSignal) =>
		get<{ products: AdminProductRow[]; next_cursor: string }>(
			`/api/v1/dashboard/catalog-admin/products?store_id=${encodeURIComponent(store)}&search=${encodeURIComponent(search)}${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}&limit=20`,
			s
		),
	adminProduct: (store: string, id: string, s?: AbortSignal) =>
		get<AdminProductDetail>(`/api/v1/dashboard/catalog-admin/products/${encodeURIComponent(id)}?store_id=${encodeURIComponent(store)}`, s),
	adminConfigurations: (store: string, id: string, s?: AbortSignal) =>
		get<{ configurations: AdminConfiguration[] }>(
			`/api/v1/dashboard/catalog-admin/products/${encodeURIComponent(id)}/configurations?store_id=${encodeURIComponent(store)}`,
			s
		),
	adminCategories: (store: string, s?: AbortSignal) =>
		get<{ categories: AdminCategoryRow[] }>(`/api/v1/dashboard/catalog-admin/categories?store_id=${encodeURIComponent(store)}`, s),
	adminTags: (store: string, s?: AbortSignal) =>
		get<{ tags: AdminTagRow[] }>(`/api/v1/dashboard/catalog-admin/tags?store_id=${encodeURIComponent(store)}`, s),
	adminCommands: (store: string, filters: { type?: string; entity_id?: string; status?: string }, s?: AbortSignal) => {
		const q = new URLSearchParams({ store_id: store, limit: '20' });
		if (filters.type) q.set('type', filters.type);
		if (filters.entity_id) q.set('entity_id', filters.entity_id);
		if (filters.status) q.set('status', filters.status);
		return get<{ commands: AdminCommand[] }>(`/api/v1/dashboard/catalog-admin/commands?${q.toString()}`, s);
	},
	adminCommand: (store: string, id: string, s?: AbortSignal) =>
		get<AdminCommand>(`/api/v1/dashboard/catalog-admin/commands/${encodeURIComponent(id)}?store_id=${encodeURIComponent(store)}&payload=1`, s),
	adminCreateCommand: (req: { store_id: string; type: string; entity_id: string; expected_revision: number; payload: Record<string, unknown> }, s?: AbortSignal) =>
		postJson<AdminCommand>('/api/v1/dashboard/catalog-admin/commands', req, s),
	adminCancelCommand: (store: string, id: string, s?: AbortSignal) =>
		postJson<AdminCommand>(`/api/v1/dashboard/catalog-admin/commands/${encodeURIComponent(id)}/cancel?store_id=${encodeURIComponent(store)}`, {}, s),
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
