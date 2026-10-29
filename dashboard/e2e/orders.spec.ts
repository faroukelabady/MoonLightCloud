import { test, expect, type Page } from '@playwright/test';
import { uiLogin } from './auth';

// Targeted Phase 6C + R2 browser suite on one login: 25 mocked orders
// traverse 20 + 5 via Load more with no duplicates; then the page loads a
// list, detail, exact money and the unmapped warning, with no console or
// page errors. API responses are mocked; the suite proves the shipped UI
// contract, not backend state.
// Explicit dev-stack credentials + TOTP (no default account): see auth.ts.

function row(i: number) {
	const id = `07${String(i).padStart(3, '0')}`;
	return {
		provider_key: 'website', external_order_id: id, order_number: id,
		provider_status: 'processing', canonical_status: 'processing',
		currency: 'EGP', total_minor: '68000',
		created_at: '2026-09-27T12:00:00Z', modified_at: '2026-09-27T12:00:00Z',
		customer_name: 'A B', mapping_complete: true, unmapped_line_count: 0,
		provider_deleted: false, revision: 1
	};
}

const PAGE1 = {
	orders: Array.from({ length: 20 }, (_, i) => row(i)),
	next_cursor: 'cursor-page-2',
	status_counts: [{ canonical_status: 'processing', total: 25 }],
	webhook_inbox: { pending: 0, retry: 0, blocked: 0, oldest_pending_at: null }
};

const PAGE2 = {
	orders: Array.from({ length: 5 }, (_, i) => row(20 + i)),
	next_cursor: null,
	status_counts: [{ canonical_status: 'processing', total: 25 }],
	webhook_inbox: { pending: 0, retry: 0, blocked: 0, oldest_pending_at: null }
};

async function mockPages(page: Page) {
	await page.route('**/api/v1/dashboard/orders*', (r) => {
		const url = new URL(r.request().url());
		const body = url.searchParams.get('cursor') ? PAGE2 : PAGE1;
		return r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
	});
}

const ORDERS = {
	orders: [
		{
			provider_key: 'website', external_order_id: '800', order_number: '800',
			provider_status: 'processing', canonical_status: 'processing',
			currency: 'EGP', total_minor: '9007199254740993',
			created_at: '2026-09-27T10:00:00Z', modified_at: '2026-09-27T11:00:00Z',
			customer_name: 'A B', mapping_complete: false, unmapped_line_count: 1,
			provider_deleted: false, revision: 2
		}
	],
	status_counts: [{ canonical_status: 'processing', total: 1 }],
	webhook_inbox: { pending: 0, retry: 0, blocked: 0, oldest_pending_at: null }
};

const DETAIL = {
	summary: ORDERS.orders[0],
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
	status_history: [{ order_revision: 1, provider_status: 'pending', canonical_status: 'pending', observed_at: '2026-09-27T10:00:00Z' }]
};

async function mockOrders(page: Page) {
	// Detail pattern first: Playwright serves the first matching route.
	// NOTE: single `*` never crosses `/`, so the detail glob needs `**`.
	await page.route('**/api/v1/dashboard/orders/**/*', (r) => r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(DETAIL) }));
	await page.route('**/api/v1/dashboard/orders*', (r) => r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(ORDERS) }));
}

async function login(page: Page) {
	await uiLogin(page);
}

test('orders paginate, then render list, detail, exact money and warning', async ({ page }) => {
	const errors: string[] = [];
	page.on('pageerror', (e) => errors.push(String(e).slice(0, 300)));
	await mockPages(page);
	await login(page);
	await page.goto('orders');
	await expect(page.getByRole('main').getByText('الطلبات عبر الإنترنت')).toBeVisible({ timeout: 15000 });
	// First page: 20 view buttons, continuation visible.
	await expect(page.getByRole('button', { name: /تحميل المزيد/ })).toBeVisible();
	expect(await page.getByRole('button', { name: /عرض الطلب/ }).count()).toBe(20);
	// Load more appends the final 5 and the control disappears.
	await page.getByRole('button', { name: /تحميل المزيد/ }).click();
	await expect(page.getByRole('button', { name: /عرض الطلب/ })).toHaveCount(25, { timeout: 10000 });
	await expect(page.getByRole('button', { name: /تحميل المزيد/ })).toHaveCount(0);
	// No duplicate order rows by stable identity.
	const ids = await page.evaluate(() =>
		Array.from(document.querySelectorAll('tbody tr td.num:first-child')).map((td) => td.textContent)
	);
	expect(new Set(ids).size).toBe(25);
	let overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
	expect(overflow, 'no horizontal page overflow').toBeLessThanOrEqual(1);

	// Same session: one unmapped order with exact money and its detail.
	await page.unroute('**/api/v1/dashboard/orders*');
	await mockOrders(page);
	await page.reload();
	await expect(page.getByRole('main').getByText('الطلبات عبر الإنترنت')).toBeVisible({ timeout: 15000 });
	await expect(page.getByText('90,071,992,547,409.93')).toBeVisible();
	await page.getByRole('button', { name: /عرض الطلب/ }).click();
	await expect(page.getByText(/غير مربوطة بكتالوج/)).toBeVisible();
	await expect(page.getByText('FOREIGN', { exact: true })).toBeVisible();
	overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
	expect(overflow, 'no horizontal page overflow').toBeLessThanOrEqual(1);
	expect(errors, 'page errors').toEqual([]);
});
