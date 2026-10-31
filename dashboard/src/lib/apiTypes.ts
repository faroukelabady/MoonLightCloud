// Dashboard BFF response and request shapes. Money stays string.

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
	// Gross profit after refunds (net sales - net tax - net cost), per
	// recorded currency and in EGP with each event converted at its own rate.
	profit?: {
		by_currency: (ProfitFigures & { currency: string })[];
		egp: ProfitFigures;
	};
}

export interface ProfitFigures {
	net_sales_minor: string;
	net_tax_minor: string;
	net_cost_minor: string;
	profit_minor: string;
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

// One sale with its lines. Money is exact minor units in the sale currency;
// a null cost means Retail did not record one.
export interface SaleDetailLine {
	sku: string;
	product_name: string;
	width_cm?: number;
	height_cm?: number;
	quantity: number;
	unit_price_minor: string;
	line_total_minor: string;
	unit_cost_minor: string | null;
	line_cost_minor: string | null;
}

export interface SaleDetail {
	sale_id: string;
	sale_number: string;
	channel: string;
	occurred_at: string;
	currency: string;
	subtotal_minor: string;
	discount_minor: string;
	tax_minor: string;
	total_minor: string;
	cashier_name?: string;
	fx_rate_microrate?: string;
	shop_name_ar: string;
	shop_name_en: string;
	lines: SaleDetailLine[];
	cost_minor: string;
	cost_complete: boolean;
}

export type SyncCategoryKey = 'sales' | 'returns' | 'catalog' | 'stock';

export interface SyncSummary {
	categories: { category: SyncCategoryKey; synced: number; last_received_at?: string }[];
	total_synced: number;
	last_received_at?: string;
}

export interface MediaStatus {
	storage: string;
	check: 'ok' | 'missing' | 'error' | 'empty';
	checked_at: string;
	check_ms: number;
	stored_files: number;
	stored_bytes: number;
	uploaded_in_range: number;
	last_upload_at?: string;
	gallery_images: number;
	waiting_images: number;
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
	/** Control channel state: polling, poll_stale or never_polled. */
	control?: 'polling' | 'poll_stale' | 'never_polled';
	last_seen_at?: string | null;
	last_poll_at?: string | null;
	last_data_at?: string | null;
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



export interface AdminProductRow {
	product_id: string;
	// Phase 17-R2: structural type identity (never inferred).
	product_type_id?: string;
	name_ar: string;
	name_en: string;
	is_active: boolean;
	catalog_revision: number;
	sell_online: boolean;
	// Phase 17-R0 (ADR-0049): product rows carry NO sku/stock —
	// variant_count/derived_stock derive from the product's variants
	// (SKU/stock authority lives on the variant rows).
	variant_count: number;
	derived_stock: number;
	configuration_revision: number;
	has_pending: boolean;
	// ADR-0056: list thumbnail file ("" when no image has arrived).
	thumbnail_sha256?: string;
}

// One gallery entry; variant_id "" is the Product gallery. available is
// false until Retail has uploaded the file.
export interface AdminProductImage {
	image_id: string;
	variant_id: string;
	position: number;
	is_primary: boolean;
	sha256: string;
	width: number;
	height: number;
	available: boolean;
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
	// Phase 17: SKU/inventory-owning variant rows (variant rows carry the
	// SKU; product rows carry the derived rollup only).
	variants?: AdminProductVariant[];
	images?: AdminProductImage[];
}

export interface AdminProductVariantAttribute {
	definition_code: string;
	value_code: string;
	name_ar: string;
	name_en: string;
	definition_name_ar: string;
	definition_name_en: string;
	position: number;
}

// Phase 17-R2: structural ProductType projection (Retail authority).
export interface AdminProductType {
	type_id: string;
	code: string;
	name_ar: string;
	name_en: string;
	description_ar?: string | null;
	description_en?: string | null;
	is_active: boolean;
	position: number;
	type_revision: number;
	dimensions: string[];
	capabilities: string[];
	has_pending?: boolean;
}

export interface AdminProductVariant {
	variant_id: string;
	product_id: string;
	sku: string;
	is_active: boolean;
	deleted: boolean;
	price_egp_minor: string;
	price_usd_minor: string | null;
	stock_quantity: number;
	position: number;
	combination_key: string;
	variant_revision: number;
	catalog_revision: number;
	inventory_revision: number;
	has_pending: boolean;
	attributes: AdminProductVariantAttribute[];
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

// What the shop lets Cloud change (Retail Settings → Cloud sync), as
// advertised by the Store's devices. Retail checks every command again.
export interface AdminPermissions {
	online_selling: boolean;
	products: boolean;
	catalog: boolean;
	devices: number;
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
	target_kind?: string;
	requested_key?: string;
	result_entity_id?: string;
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

