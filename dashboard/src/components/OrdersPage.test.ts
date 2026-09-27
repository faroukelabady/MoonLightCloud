import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, render, screen, fireEvent } from '@testing-library/svelte';
import OrdersPage from './OrdersPage.svelte';
import type { OrderSummary } from '../lib/api.js';

const order: OrderSummary = {
	provider_key: 'website',
	external_order_id: '800',
	order_number: '800',
	provider_status: 'processing',
	canonical_status: 'PROCESSING',
	currency: 'EGP',
	total_minor: '9007199254740993',
	created_at: '2026-09-27T10:00:00Z',
	modified_at: '2026-09-27T11:00:00Z',
	customer_name: 'A B',
	mapping_complete: false,
	unmapped_line_count: 1,
	provider_deleted: false,
	revision: 2
};

const base: {
	orders: OrderSummary[];
	counts: { canonical_status: string; total: number }[];
	inbox: { pending: number; retry: number; blocked: number; oldest_pending_at: null };
	status: 'loading' | 'loaded' | 'empty' | 'error';
	errStatus: null;
	filterStatus: string;
	filterProvider: string;
	nextCursor: null;
	more: 'idle';
	moreErr: null;
	selected: null;
	detailStatus: 'idle' | 'loading' | 'loaded' | 'error';
	detailErr: null;
} = {
	orders: [order],
	counts: [{ canonical_status: 'PROCESSING', total: 1 }],
	inbox: { pending: 0, retry: 0, blocked: 0, oldest_pending_at: null },
	status: 'loaded',
	errStatus: null,
	filterStatus: '',
	filterProvider: '',
	nextCursor: null,
	more: 'idle' as const,
	moreErr: null,
	selected: null,
	detailStatus: 'idle',
	detailErr: null
};

describe('OrdersPage', () => {
	afterEach(() => cleanup());
	it('shows Arabic and English labels', () => {
		render(OrdersPage, { props: { ...base, onstatus: () => {}, onprovider: () => {}, onloadmore: () => {}, onselect: () => {}, onretry: () => {} } });
		expect(screen.getByText(/الطلبات عبر الإنترنت/)).toBeTruthy();
		expect(screen.getByText(/Online Orders/)).toBeTruthy();
	});

	it('renders exact >2^53 money without float loss', () => {
		const { container } = render(OrdersPage, { props: { ...base, onstatus: () => {}, onprovider: () => {}, onloadmore: () => {}, onselect: () => {}, onretry: () => {} } });
		expect(container.textContent).toContain('90,071,992,547,409.93');
	});

	it('shows the unmapped-lines warning, never a fake product', () => {
		render(OrdersPage, { props: { ...base, onstatus: () => {}, onprovider: () => {}, onloadmore: () => {}, onselect: () => {}, onretry: () => {} } });
		expect(screen.getAllByText(/غير مكتمل/).length).toBeGreaterThan(0);
		expect(document.body.textContent).not.toContain('MoonLight Product');
	});

	it('shows the deleted indicator for provider-deleted orders', () => {
		const deleted = { ...order, provider_deleted: true, canonical_status: 'DELETED' };
		render(OrdersPage, { props: { ...base, orders: [deleted], onstatus: () => {}, onprovider: () => {}, onloadmore: () => {}, onselect: () => {}, onretry: () => {} } });
		expect(screen.getByText(/محذوف/)).toBeTruthy();
	});

	it('shows empty state', () => {
		render(OrdersPage, { props: { ...base, orders: [], status: 'empty', onstatus: () => {}, onprovider: () => {}, onloadmore: () => {}, onselect: () => {}, onretry: () => {} } });
		expect(screen.getByText(/لا توجد طلبات/)).toBeTruthy();
	});

	it('shows error state with retry', async () => {
		const onretry = vi.fn();
		render(OrdersPage, { props: { ...base, orders: [], status: 'error', errStatus: 503, onstatus: () => {}, onprovider: () => {}, onloadmore: () => {}, onselect: () => {}, onretry } });
		await fireEvent.click(screen.getByRole('button', { name: /إعادة المحاولة/ }));
		expect(onretry).toHaveBeenCalled();
	});

	it('opens detail on view and closes it', async () => {
		const onselect = vi.fn();
		render(OrdersPage, { props: { ...base, onstatus: () => {}, onprovider: () => {}, onloadmore: () => {}, onselect, onretry: () => {} } });
		await fireEvent.click(screen.getByText(/عرض \/ View/));
		expect(onselect).toHaveBeenCalledWith(order);
	});

	it('forwards status filter changes', async () => {
		const onstatus = vi.fn();
		render(OrdersPage, { props: { ...base, onstatus, onprovider: () => {}, onloadmore: () => {}, onselect: () => {}, onretry: () => {} } });
		const select = screen.getByLabelText(/الحالة \/ Status/) as HTMLSelectElement;
		await fireEvent.change(select, { target: { value: 'COMPLETED' } });
		expect(onstatus).toHaveBeenCalledWith('COMPLETED');
	});

	it('renders detail drawer with lines, history, and mapping alert', () => {
		const detail = {
			summary: order,
			discount_minor: '0',
			shipping_minor: '3000',
			cart_tax_minor: '0',
			total_tax_minor: '0',
			prices_include_tax: false,
			paid_at: null,
			completed_at: null,
			payment_method: 'cod',
			payment_method_title: 'Cash',
			customer_first_name: 'A',
			customer_last_name: 'B',
			customer_email: 'a@example.com',
			customer_phone: '+201000000000',
			lines: [
				{ external_line_id: 1, external_product_id: '999', variation_id: 0, sku: 'FOREIGN', name: 'Foreign', quantity: 1, total_minor: '500', moonlight_product_id: null, mapped: false }
			],
			addresses: [],
			status_history: [{ order_revision: 1, provider_status: 'pending', canonical_status: 'PENDING', observed_at: '2026-09-27T10:00:00Z' }]
		} as never;
		const { container } = render(OrdersPage, { props: { ...base, selected: detail, detailStatus: 'loaded', onstatus: () => {}, onprovider: () => {}, onloadmore: () => {}, onselect: () => {}, onretry: () => {} } });
		expect(screen.getByText(/غير مربوطة بكتالوج/)).toBeTruthy();
		expect(container.textContent).toContain('FOREIGN');
		expect(container.textContent).toContain('rev 1');
		expect(container.textContent).toContain('a@example.com');
	});
});

describe('OrdersPage pagination', () => {
	afterEach(() => cleanup());
	const pageProps = (extra: Record<string, unknown>) => ({
		...base,
		onstatus: () => {},
		onprovider: () => {},
		onloadmore: () => {},
		onselect: () => {},
		onretry: () => {},
		...extra
	});

	it('shows Load more exactly when a cursor exists', () => {
		render(OrdersPage, { props: pageProps({ nextCursor: 'opaque-token' }) });
		expect(screen.getByRole('button', { name: /تحميل المزيد \/ Load more/ })).toBeTruthy();
	});

	it('hides Load more on the final page', () => {
		render(OrdersPage, { props: pageProps({ nextCursor: null }) });
		expect(screen.queryByRole('button', { name: /تحميل المزيد/ })).toBeNull();
	});

	it('disables Load more while loading the next page', () => {
		render(OrdersPage, { props: pageProps({ nextCursor: 'opaque-token', more: 'loading' as const }) });
		expect((screen.getByRole('button', { name: /جارٍ التحميل/ }) as HTMLButtonElement).disabled).toBe(true);
	});

	it('keeps loaded rows and offers retry when the next page fails', async () => {
		const onloadmore = vi.fn();
		render(OrdersPage, { props: pageProps({ nextCursor: 'opaque-token', more: 'error' as const, moreErr: 503, onloadmore }) });
		// Page-1 rows stay visible.
		expect(screen.getAllByText('800').length).toBeGreaterThan(0);
		await fireEvent.click(screen.getAllByRole('button', { name: /إعادة المحاولة/ })[0]);
		expect(onloadmore).toHaveBeenCalled();
	});

	it('fires continuation on Load more click', async () => {
		const onloadmore = vi.fn();
		render(OrdersPage, { props: pageProps({ nextCursor: 'opaque-token', onloadmore }) });
		await fireEvent.click(screen.getByRole('button', { name: /تحميل المزيد/ }));
		expect(onloadmore).toHaveBeenCalledTimes(1);
	});
});
