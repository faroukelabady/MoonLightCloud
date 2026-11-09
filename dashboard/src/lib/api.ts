// Typed dashboard BFF client. One place for fetch, auth errors, and
// request cancellation (AbortController per navigation/filter change so
// slower old responses never overwrite newer state). Money stays string.

export { ApiError, mutationHeaders, setCSRFToken, type PeriodParams } from './http';
export * from './apiTypes';
export * from './updatesApi';
export * from './authApi';

import { ApiError, get, mutationHeaders, postJson, query, setCSRFToken, type PeriodParams } from './http';
import type { Me } from './authApi';
import type { CatalogCommandRequest } from './catalogCommands';
import type {
	OverviewResponse,
	DailyResponse,
	DailyMode,
	BreakdownMode,
	CategoryKind,
	ProductRow,
	CategoryRow,
	BranchRow,
	ActivityItem,
	OrderDetail,
	OrderListResponse,
	LatestSale,
	SaleDetail,
	SyncSummary,
	MediaStatus,
	DeviceRow,
	StoreRow,
	IncidentRow,
	IncidentDelivery,
	IncidentRecovery,
	DeviceIncidentSummary,
	SyncRequestResponse,
	SyncHealth,
	TagListResponse,
	OrderAnalyticsResponse,
	CatalogHealthResponse,
	AdminProductRow,
	AdminProductDetail,
	AdminProductType,
	AdminCategoryRow,
	AdminTagRow,
	AdminConfiguration,
	AdminCommand,
	AdminPermissions
} from './apiTypes';

// analytics and catalog health ("" = all durable providers).
export function providerQuery(provider: string): string {
	return provider ? `&provider_key=${encodeURIComponent(provider)}` : '';
}

// storeQuery appends the Store ownership scope. Empty selects the global
// scope (all Stores).
export function storeQuery(store: string): string {
	return store ? `&store_id=${encodeURIComponent(store)}` : '';
}

// isStoreID validates a Store scope value before it ever reaches the
// network: canonical UUID shape only.
export function isStoreID(value: string): boolean {
	return /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/.test(value);
}



export type DeviceProvision = { device_id: string; credential_id: string; name: string; store_id: string; credential: string };
export const dashboardApi = {
	me: async (s?: AbortSignal): Promise<Me> => {
		const me = await get<Me>('/api/v1/dashboard/auth/me', s);
		if (!s?.aborted) setCSRFToken(me.csrf_token);
		return me;
	},
	logout: async (): Promise<void> => {
		try {
			await fetch('/api/v1/dashboard/auth/logout', { method: 'POST', headers: mutationHeaders(), credentials: 'same-origin' });
		} finally {
			setCSRFToken('');
		}
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
	// One page of the feed; has_more is false after 200 items.
	activityPage: (store: string, offset: number, s?: AbortSignal) =>
		get<{ items: ActivityItem[]; offset: number; has_more: boolean }>(`/api/v1/dashboard/activity?limit=10&offset=${offset}${storeQuery(store)}`, s),
	saleDetail: (store: string, id: string, s?: AbortSignal) =>
		get<SaleDetail>(`/api/v1/dashboard/sales/${encodeURIComponent(id)}${store ? `?store_id=${encodeURIComponent(store)}` : ''}`, s),
	syncSummary: (p: PeriodParams, store: string, s?: AbortSignal) =>
		get<SyncSummary>(`/api/v1/dashboard/sync-summary?${query(p)}${storeQuery(store)}`, s),
	mediaStatus: (p: PeriodParams, store: string, s?: AbortSignal) =>
		get<MediaStatus>(`/api/v1/dashboard/media-status?${query(p)}${storeQuery(store)}`, s),
	latestSales: (store: string, s?: AbortSignal) => get<{ sales: LatestSale[] }>(`/api/v1/dashboard/sales/latest?limit=8${storeQuery(store)}`, s),
	orders: (status: string, provider: string, cursor: string | null | undefined, store: string, s?: AbortSignal) =>
		get<OrderListResponse>(
			`/api/v1/dashboard/orders?status=${encodeURIComponent(status)}&provider=${encodeURIComponent(provider)}&limit=20${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}${storeQuery(store)}`,
			s
		),
	orderDetail: (provider: string, id: string, store: string, s?: AbortSignal) =>
		get<OrderDetail>(`/api/v1/dashboard/orders/${encodeURIComponent(provider)}/${encodeURIComponent(id)}${store ? `?store_id=${encodeURIComponent(store)}` : ''}`, s),
	devices: (s?: AbortSignal) => get<{ devices: DeviceRow[]; control_enabled?: boolean }>('/api/v1/dashboard/devices', s),
	createStore: (req: {store_id:string;display_name:string;timezone:string;mfa_code:string}) => postJson<StoreRow>('/api/v1/dashboard/stores',req),
 createDevice: (req: {store_id:string;name:string;mfa_code:string}) => postJson<DeviceProvision>('/api/v1/dashboard/devices',req),
 rotateDevice: (id:string,mfa_code:string) => postJson<DeviceProvision>(`/api/v1/dashboard/devices/${encodeURIComponent(id)}/credentials/rotate`,{mfa_code}),
 revokeDevice: (id:string,mfa_code:string) => postJson<{revoked:boolean}>(`/api/v1/dashboard/devices/${encodeURIComponent(id)}/revoke`,{mfa_code}),
 stores: (s?: AbortSignal) => get<{ stores: StoreRow[] }>('/api/v1/dashboard/stores', s),
	adminProducts: (store: string, search: string, cursor: string | null, s?: AbortSignal) =>
		get<{ products: AdminProductRow[]; next_cursor: string }>(
			`/api/v1/dashboard/catalog-admin/products?store_id=${encodeURIComponent(store)}&search=${encodeURIComponent(search)}${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}&limit=20`,
			s
		),
	adminProduct: (store: string, id: string, s?: AbortSignal) =>
		get<AdminProductDetail>(`/api/v1/dashboard/catalog-admin/products/${encodeURIComponent(id)}?store_id=${encodeURIComponent(store)}`, s),
	// Image URL (not a fetch): the hash names the file, so the browser caches it.
	adminMediaUrl: (store: string, sha256: string) =>
		`/api/v1/dashboard/catalog-admin/media/${encodeURIComponent(sha256)}?store_id=${encodeURIComponent(store)}`,
	adminConfigurations: (store: string, id: string, s?: AbortSignal) =>
		get<{ configurations: AdminConfiguration[] }>(
			`/api/v1/dashboard/catalog-admin/products/${encodeURIComponent(id)}/configurations?store_id=${encodeURIComponent(store)}`,
			s
		),
	adminCategories: (store: string, s?: AbortSignal) =>
		get<{ categories: AdminCategoryRow[] }>(`/api/v1/dashboard/catalog-admin/categories?store_id=${encodeURIComponent(store)}`, s),
	adminTags: (store: string, s?: AbortSignal) =>
		get<{ tags: AdminTagRow[] }>(`/api/v1/dashboard/catalog-admin/tags?store_id=${encodeURIComponent(store)}`, s),
	adminProductTypes: (store: string, s?: AbortSignal) =>
		get<{ product_types: AdminProductType[] }>(
			`/api/v1/dashboard/catalog-admin/product-types?store_id=${encodeURIComponent(store)}`,
			s
		),
	adminProductType: (store: string, id: string, s?: AbortSignal) =>
		get<{ product_type: AdminProductType }>(
			`/api/v1/dashboard/catalog-admin/product-types/${encodeURIComponent(id)}?store_id=${encodeURIComponent(store)}`,
			s
		),
	adminCommands: (store: string, filters: { type?: string; entity_id?: string; status?: string }, s?: AbortSignal) => {
		const q = new URLSearchParams({ store_id: store, limit: '20' });
		if (filters.type) q.set('type', filters.type);
		if (filters.entity_id) q.set('entity_id', filters.entity_id);
		if (filters.status) q.set('status', filters.status);
		return get<{ commands: AdminCommand[]; next_cursor: string }>(`/api/v1/dashboard/catalog-admin/commands?${q.toString()}`, s);
	},
	adminCommand: (store: string, id: string, s?: AbortSignal) =>
		get<AdminCommand>(`/api/v1/dashboard/catalog-admin/commands/${encodeURIComponent(id)}?store_id=${encodeURIComponent(store)}&payload=1`, s),
	// mfaCode approves product, price and catalog changes (checked and
	// consumed by the server, never stored).
	adminCreateCommand: (req: CatalogCommandRequest, mfaCode?: string, s?: AbortSignal) =>
		postJson<AdminCommand>('/api/v1/dashboard/catalog-admin/commands', mfaCode ? { ...req, mfa_code: mfaCode } : req, s),
	adminPermissions: (store: string, s?: AbortSignal) =>
		get<AdminPermissions>(`/api/v1/dashboard/catalog-admin/permissions?store_id=${encodeURIComponent(store)}`, s),
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
			headers: mutationHeaders(),
			credentials: 'same-origin'
		});
		if (!res.ok) throw new ApiError(res.status, 'REQUEST_FAILED', `request failed (${res.status})`);
		const body = (await res.json()) as { incident: IncidentRow };
		return body.incident;
	},
	incidentResolve: async (id: string): Promise<IncidentRow> => {
		const res = await fetch(`/api/v1/dashboard/operations/incidents/${encodeURIComponent(id)}/resolve`, {
			method: 'POST',
			headers: mutationHeaders(),
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
			headers: mutationHeaders({ 'Idempotency-Key': idempotencyKey }),
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

