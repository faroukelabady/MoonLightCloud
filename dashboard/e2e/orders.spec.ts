import { test, expect, type Page } from '@playwright/test';

// Targeted Phase 6C browser suite: authenticated orders page loads,
// list renders, detail renders, exact money, unmapped warning, and no
// console/page errors. API responses are mocked; the suite proves the
// shipped UI contract, not backend state.
const USER = process.env.E2E_DASHBOARD_USER ?? 'operator';
const PASS = process.env.E2E_DASHBOARD_PASSWORD ?? 'moonlight-dev-operator';

const ORDERS = {
	orders: [
		{
			provider_key: 'website', external_order_id: '800', order_number: '800',
			provider_status: 'processing', canonical_status: 'PROCESSING',
			currency: 'EGP', total_minor: '9007199254740993',
			created_at: '2026-09-27T10:00:00Z', modified_at: '2026-09-27T11:00:00Z',
			customer_name: 'A B', mapping_complete: false, unmapped_line_count: 1,
			provider_deleted: false, revision: 2
		}
	],
	status_counts: [{ canonical_status: 'PROCESSING', total: 1 }],
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
	status_history: [{ order_revision: 1, provider_status: 'pending', canonical_status: 'PENDING', observed_at: '2026-09-27T10:00:00Z' }]
};

async function mockOrders(page: Page) {
	// Detail pattern first: Playwright serves the first matching route.
	// NOTE: single `*` never crosses `/`, so the detail glob needs `**`.
	await page.route('**/api/v1/dashboard/orders/**/*', (r) => r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(DETAIL) }));
	await page.route('**/api/v1/dashboard/orders*', (r) => r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(ORDERS) }));
}

async function login(page: Page) {
	await page.goto('login');
	await page.getByLabel(/اسم المستخدم/).fill(USER);
	await page.getByLabel(/كلمة المرور/).fill(PASS);
	await page.getByRole('button', { name: /دخول/ }).click();
	await expect(page.getByText('لوحة متابعة المبيعات')).toBeVisible({ timeout: 15000 });
}

test('online orders page loads, renders list, detail, exact money, and warning', async ({ page }) => {
	const errors: string[] = [];
	page.on('pageerror', (e) => errors.push(String(e).slice(0, 300)));
	await mockOrders(page);
	await login(page);
	await page.goto('orders');
	await expect(page.getByRole('main').getByText('الطلبات عبر الإنترنت')).toBeVisible({ timeout: 15000 });
	await expect(page.getByText('90,071,992,547,409.93')).toBeVisible();
	await page.getByRole('button', { name: /عرض \/ View/ }).click();
	await expect(page.getByText(/غير مربوطة بكتالوج/)).toBeVisible();
	await expect(page.getByText('FOREIGN', { exact: true })).toBeVisible();
	const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
	expect(overflow, 'no horizontal page overflow').toBeLessThanOrEqual(1);
	expect(errors, 'page errors').toEqual([]);
});
