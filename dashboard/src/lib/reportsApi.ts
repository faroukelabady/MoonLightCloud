// Printable reports: the sales report MoonLightRetail prints and the
// stocktaking valuation behind the zakat calculator. Money fields are
// exact minor-unit strings, like every other dashboard read.
import { get, query, type PeriodParams } from './http.js';

export type ReportDisplay = 'EGP' | 'USD' | 'all';
type Currency = 'EGP' | 'USD';

export interface ReportShop {
	name_ar: string;
	name_en: string;
	address_ar: string;
	address_en: string;
	phone: string;
}

export interface ReportSummaryRow {
	currency: Currency;
	sales_count: number;
	items_sold: number;
	gross_minor: string;
	discount_minor: string;
	tax_minor: string;
	sales_minor: string;
	cost_minor: string;
	missing_cost_items: number;
	return_count: number;
	returned_items: number;
	refund_minor: string;
	refunded_tax_minor: string;
	restocked_cost_minor: string;
	net_sales_minor: string;
	net_tax_minor: string;
	net_revenue_minor: string;
	net_cost_minor: string;
	profit_minor: string;
}

export interface ReportDailyRow {
	date: string;
	currency: Currency;
	sales_count: number;
	items_sold: number;
	sales_minor: string;
	cost_minor: string;
	missing_cost_items: number;
	returned_items: number;
	refund_minor: string;
	net_sales_minor: string;
}

export interface ReportGroupRow {
	id: string;
	sku?: string;
	name_ar: string;
	name_en: string;
	currency: Currency;
	items_sold: number;
	returned_items: number;
	net_items: number;
	sales_minor: string;
	cost_minor: string;
	missing_cost_items: number;
	refund_minor: string;
	net_sales_minor: string;
}

export interface ReportPaymentRow {
	method: 'cash' | 'card' | 'other';
	currency: Currency;
	payment_count: number;
	amount_minor: string;
	refund_minor: string;
	net_minor: string;
}

export interface ReportSaleRow {
	sale_id: string;
	sale_number: string;
	occurred_at: string;
	currency: Currency;
	products: string;
	skus: string;
	root_names_ar: string;
	root_names_en: string;
	subcategory_names_ar: string;
	subcategory_names_en: string;
	items_sold: number;
	payment_methods: string[];
	cost_minor: string;
	missing_cost_items: number;
	total_minor: string;
}

export interface SalesReport {
	generated_at: string;
	period: { kind: string; timezone: string; start_date: string; end_date: string };
	store_id: string | null;
	display_currency: ReportDisplay;
	shop: ReportShop | null;
	summary: ReportSummaryRow[];
	base: { sales_minor: string; net_sales_minor: string; profit_minor: string; missing_cost_items: number };
	daily: ReportDailyRow[];
	categories: ReportGroupRow[];
	subcategories: ReportGroupRow[];
	tags: ReportGroupRow[];
	products: ReportGroupRow[];
	payments: ReportPaymentRow[];
	sales: ReportSaleRow[];
	sales_total_count: number;
	unclassified_items: number;
	detail_offset: number;
	detail_limit: number;
}

export interface StockRow {
	variant_id: string;
	sku: string;
	product_name_ar: string;
	product_name_en: string;
	variant_name_ar: string;
	variant_name_en: string;
	category_id: string;
	active: boolean;
	quantity: number;
	unit_cost_minor: string | null;
	cost_minor: string | null;
	unit_price_minor: string | null;
	retail_minor: string | null;
}

export interface StockReport {
	generated_at: string;
	store_id: string | null;
	shop: ReportShop | null;
	totals: {
		products_in_stock: number;
		variants_in_stock: number;
		units: number;
		cost_minor: string;
		retail_minor: string;
		units_without_cost: number;
		units_without_price: number;
		inactive_units: number;
	};
	categories: { id: string; name_ar: string; name_en: string; products_in_stock: number; units: number; cost_minor: string; retail_minor: string }[];
	items: StockRow[];
}

function storeParam(store: string): string {
	return store ? `&store_id=${encodeURIComponent(store)}` : '';
}

export const reportsApi = {
	sales: (p: PeriodParams, currency: ReportDisplay, store: string, offset: number, limit: number, s?: AbortSignal) =>
		get<SalesReport>(`/api/v1/dashboard/reports/sales?${query(p)}&currency=${currency}&detail_offset=${offset}&detail_limit=${limit}${storeParam(store)}`, s),
	stock: (store: string, s?: AbortSignal) =>
		get<StockReport>(`/api/v1/dashboard/reports/stocktaking${store ? `?store_id=${encodeURIComponent(store)}` : ''}`, s)
};
