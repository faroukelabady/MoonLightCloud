import { test, expect, type Page } from '@playwright/test';

// Phase 9-R1 independent browser regression suite for Store scope
// transitions (F03) and authenticated Store-registry initialization (F05).
// Auth is real against the running Cloud dev server; dashboard data routes
// are mocked so the shipped UI contract is exercised deterministically.

const USER = process.env.E2E_DASHBOARD_USER ?? 'operator';
const PASS = process.env.E2E_DASHBOARD_PASSWORD ?? 'moonlight-dev-operator';

const STORE_A = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa';
const STORE_B = 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb';

const PERIOD = { kind: 'custom', timezone: 'Africa/Cairo', start_local: '2026-09-20T00:00:00+03:00', end_local_exclusive: '2026-09-21T00:00:00+03:00', start_utc: '2026-09-19T21:00:00Z', end_utc: '2026-09-20T21:00:00Z' };

function overviewWith(transactions: number) {
	return {
		generated_at: '2026-09-20T21:00:00Z', timezone: 'Africa/Cairo', period: PERIOD,
		summary: {
			transaction_count: transactions, units_sold: 8, return_transaction_count: 5, units_returned: 6,
			currency_totals: [
				{ currency: 'EGP', subtotal_minor: '390000', discount_minor: '0', tax_minor: '0', sales_total_minor: '390000', line_cost_minor: '240000', refund_total_minor: '260000', net_sales_minor: '130000', returned_units: 4, returned_cost_minor: '160000', net_cost_minor: '80000' }
			]
		},
		normalized: { normalized_total_minor: '525200', normalized_refund_minor: '395200', normalized_net_minor: '130000', transactions, units: 8, return_transactions: 5, units_returned: 6, usd_sale_count: 0 },
		averages: {
			all: { transactions, units: 8, average_minor: '105040' },
			egp: { transactions, units: 6, average_minor: '97500' },
			usd: { transactions: 0, units: 0, average_minor: '0' }
		},
		fx: { has_usd: false, latest_rate: null, multiple_rates_used: false }
	};
}

function storeRow(id: string, name: string) {
	return {
		store_id: id,
		display_name: name,
		timezone: 'Africa/Cairo',
		status: 'active',
		device_count: 1,
		created_at: '2026-09-01T00:00:00Z',
		updated_at: '2026-09-01T00:00:00Z'
	};
}

function orderSummary(store: string) {
	const a = store === STORE_A;
	return {
		provider_key: 'website',
		external_order_id: a ? '800' : '900',
		order_number: a ? 'A-800' : 'B-900',
		provider_status: 'processing',
		canonical_status: 'PROCESSING',
		currency: 'EGP',
		total_minor: a ? '65000' : '130000',
		created_at: '2026-09-27T10:00:00Z',
		modified_at: '2026-09-27T10:05:00Z',
		customer_name: a ? 'Alice Store A' : 'Bob Store B',
		mapping_complete: true,
		unmapped_line_count: 0,
		provider_deleted: false,
		revision: 1
	};
}

function orderDetail(store: string) {
	const a = store === STORE_A;
	return {
		summary: orderSummary(store),
		discount_minor: '0',
		shipping_minor: '0',
		cart_tax_minor: '0',
		total_tax_minor: '0',
		prices_include_tax: false,
		paid_at: null,
		completed_at: null,
		payment_method: 'cod',
		payment_method_title: 'Cash',
		customer_first_name: a ? 'Alice' : 'Bob',
		customer_last_name: a ? 'StoreA' : 'StoreB',
		customer_email: a ? 'alice-store-a@example.invalid' : 'bob-store-b@example.invalid',
		customer_phone: '+201000000000',
		lines: [
			{ external_line_id: 1, external_product_id: '1', variation_id: 0, sku: a ? 'SKU-A' : 'SKU-B', name: 'Line', quantity: 1, total_minor: '65000', moonlight_product_id: null, mapped: true }
		],
		addresses: [],
		status_history: []
	};
}

interface MockOptions {
	stores?: { id: string; name: string }[];
	failFirstStoreList?: boolean;
	slowDetailStore?: string;
	slowListStore?: string;
	slowContinuationStore?: string;
}

async function mockStoreScope(page: Page, opts: MockOptions = {}) {
	const stores = opts.stores ?? [{ id: STORE_A, name: 'Store A' }, { id: STORE_B, name: 'Store B' }];
	let storeCalls = 0;
	await page.route('**/api/v1/dashboard/stores', async (r) => {
		storeCalls += 1;
		if (opts.failFirstStoreList && storeCalls === 1) {
			await r.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: { code: 'INTERNAL', message: 'boom' } }) });
			return;
		}
		await r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ stores: stores.map((s) => storeRow(s.id, s.name)) }) });
	});
	// Detail first (more specific glob), then the list.
	await page.route('**/api/v1/dashboard/orders/**/*', async (r) => {
		const store = new URL(r.request().url()).searchParams.get('store_id') ?? STORE_B;
		if (opts.slowDetailStore && store === opts.slowDetailStore) {
			await new Promise((res) => setTimeout(res, 1200));
		}
		await r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(orderDetail(store)) });
	});
	await page.route('**/api/v1/dashboard/orders*', async (r) => {
		const u = new URL(r.request().url());
		const store = u.searchParams.get('store_id') ?? '';
		const cursor = u.searchParams.get('cursor');
		if (opts.slowContinuationStore && cursor && store === opts.slowContinuationStore) {
			await new Promise((res) => setTimeout(res, 1200));
			const page2 = store === STORE_A
				? { ...orderSummary(store), external_order_id: '801', order_number: 'A-801' }
				: { ...orderSummary(store), external_order_id: '901', order_number: 'B-901' };
			await r.fulfill({
				status: 200,
				contentType: 'application/json',
				body: JSON.stringify({
					orders: [page2],
					status_counts: [{ canonical_status: 'PROCESSING', total: 1 }],
					webhook_inbox: { pending: 0, retry: 0, blocked: 0, oldest_pending_at: null },
					store_id: store
				})
			});
			return;
		}
		if (opts.slowListStore && !cursor && store === opts.slowListStore) {
			await new Promise((res) => setTimeout(res, 1200));
		}
		const rows = store ? [orderSummary(store)] : [];
		await r.fulfill({
			status: 200,
			contentType: 'application/json',
			body: JSON.stringify({
				orders: rows,
				next_cursor: store === STORE_A ? 'cursor-a' : null,
				status_counts: rows.length ? [{ canonical_status: 'PROCESSING', total: rows.length }] : [],
				webhook_inbox: { pending: 0, retry: 0, blocked: 0, oldest_pending_at: null },
				store_id: store || null
			})
		});
	});
}

async function login(page: Page, url = 'login') {
	await page.goto(url);
	await page.getByLabel(/اسم المستخدم/).fill(USER);
	await page.getByLabel(/كلمة المرور/).fill(PASS);
	await page.getByRole('button', { name: /دخول/ }).click();
	await expect(page.getByText('لوحة متابعة المبيعات')).toBeVisible({ timeout: 15000 });
}

async function openFirstDetail(page: Page) {
	await page.getByRole('button', { name: /عرض \/ View/ }).first().click();
}

function selector(page: Page) {
	return page.getByTestId('store-selector');
}

test('fresh login initializes the Store registry without a refresh', async ({ page }) => {
	await mockStoreScope(page);
	await login(page);
	await expect(selector(page).locator('option')).toHaveCount(3);
	await expect(selector(page).locator(`option[value="${STORE_B}"]`)).toHaveCount(1);
});

test('existing session page load shows Stores', async ({ page }) => {
	await mockStoreScope(page);
	await login(page);
	await page.goto('orders');
	await expect(selector(page).locator('option')).toHaveCount(3);
});

test('login with a valid Store in the URL selects it', async ({ page }) => {
	await mockStoreScope(page);
	await login(page, `login?store_id=${STORE_B}`);
	await expect(selector(page)).toHaveValue(STORE_B);
});

test('two Stores with equal display names stay distinct by UUID', async ({ page }) => {
	await mockStoreScope(page, { stores: [{ id: STORE_A, name: 'Same Name' }, { id: STORE_B, name: 'Same Name' }] });
	await login(page);
	await expect(selector(page).locator('option')).toHaveCount(3);
	await selector(page).selectOption(STORE_B);
	await expect(selector(page)).toHaveValue(STORE_B);
});

test('registry failure shows a truthful retry and recovers', async ({ page }) => {
	await mockStoreScope(page, { failFirstStoreList: true });
	await login(page);
	await expect(page.getByTestId('store-retry')).toBeVisible();
	await expect(selector(page).locator('option')).toHaveCount(1); // only All Stores
	await page.getByTestId('store-retry').click();
	await expect(selector(page).locator('option')).toHaveCount(3);
	await expect(page.getByTestId('store-retry')).toBeHidden();
});

test('logout and login reinitializes the registry', async ({ page }) => {
	await mockStoreScope(page);
	await login(page);
	await expect(selector(page).locator('option')).toHaveCount(3);
	await page.getByRole('button', { name: /خروج \/ Logout/ }).click();
	await expect(page.getByRole('heading', { name: 'تسجيل الدخول' })).toBeVisible();
	await login(page);
	await expect(selector(page).locator('option')).toHaveCount(3);
});

test('explicit Store change clears order detail', async ({ page }) => {
	await mockStoreScope(page);
	await login(page);
	await page.goto(`orders?store_id=${STORE_A}`);
	await openFirstDetail(page);
	await expect(page.getByText('alice-store-a@example.invalid')).toBeVisible();
	await selector(page).selectOption(STORE_B);
	await expect(page.getByText('alice-store-a@example.invalid')).toBeHidden();
	await expect(selector(page)).toHaveValue(STORE_B);
});

test('Back navigation returns to the previous Store and clears foreign detail', async ({ page }) => {
	await mockStoreScope(page);
	await login(page);
	await page.goto(`orders?store_id=${STORE_B}`);
	await selector(page).selectOption(STORE_A);
	await expect(selector(page)).toHaveValue(STORE_A);
	await openFirstDetail(page);
	await expect(page.getByText('alice-store-a@example.invalid')).toBeVisible();
	await page.goBack();
	await expect(selector(page)).toHaveValue(STORE_B);
	await expect(page.getByText('alice-store-a@example.invalid')).toBeHidden();
	await expect(page.getByText('B-900')).toBeVisible();
});

test('Forward navigation restores the next Store and clears the old detail', async ({ page }) => {
	await mockStoreScope(page);
	await login(page);
	await page.goto(`orders?store_id=${STORE_B}`);
	await selector(page).selectOption(STORE_A);
	await openFirstDetail(page);
	await expect(page.getByText('alice-store-a@example.invalid')).toBeVisible();
	await page.goBack();
	await expect(selector(page)).toHaveValue(STORE_B);
	await page.goForward();
	await expect(selector(page)).toHaveValue(STORE_A);
	await expect(page.getByText('alice-store-a@example.invalid')).toBeHidden();
});

test('slow Store A detail response cannot leak after switching to B', async ({ page }) => {
	await mockStoreScope(page, { slowDetailStore: STORE_A });
	await login(page);
	await page.goto(`orders?store_id=${STORE_A}`);
	await openFirstDetail(page);
	// Switch before the slow A detail resolves.
	await selector(page).selectOption(STORE_B);
	await expect(selector(page)).toHaveValue(STORE_B);
	await page.waitForTimeout(1600);
	await expect(page.getByText('alice-store-a@example.invalid')).toBeHidden();
	await expect(page.getByText('B-900')).toBeVisible();
});

test('slow Store A list response cannot restore A rows under B', async ({ page }) => {
	await mockStoreScope(page, { slowListStore: STORE_A });
	await login(page);
	await page.goto(`orders?store_id=${STORE_A}`);
	await selector(page).selectOption(STORE_B);
	await expect(selector(page)).toHaveValue(STORE_B);
	await page.waitForTimeout(1600);
	await expect(page.getByText('A-800')).toBeHidden();
	await expect(page.getByText('B-900')).toBeVisible();
});

test('slow Store A continuation cannot append after switching to B', async ({ page }) => {
	await mockStoreScope(page, { slowContinuationStore: STORE_A });
	await login(page);
	await page.goto(`orders?store_id=${STORE_A}`);
	await expect(page.getByText('A-800')).toBeVisible();
	await page.getByRole('button', { name: /تحميل المزيد/ }).click();
	await selector(page).selectOption(STORE_B);
	await expect(selector(page)).toHaveValue(STORE_B);
	await page.waitForTimeout(1600);
	await expect(page.getByText('A-801')).toHaveCount(0);
	await expect(page.getByText('A-800')).toHaveCount(0);
	await expect(page.getByText('B-900')).toBeVisible();
});

test('failed Store B request does not restore Store A data', async ({ page }) => {
	await mockStoreScope(page);
	await login(page);
	await page.goto(`orders?store_id=${STORE_A}`);
	await expect(page.getByText('A-800')).toBeVisible();
	// Make the B list fail; A data must not reappear after the scope change.
	await page.route('**/api/v1/dashboard/orders*', (r) => r.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: { code: 'INTERNAL', message: 'x' } }) }));
	await selector(page).selectOption(STORE_B);
	await expect(selector(page)).toHaveValue(STORE_B);
	await page.waitForTimeout(500);
	await expect(page.getByText('A-800')).toBeHidden();
});

test('slow Store A financial overview cannot overwrite the B scope', async ({ page }) => {
	await mockStoreScope(page);
	await page.route('**/api/v1/dashboard/overview*', async (r) => {
		const store = new URL(r.request().url()).searchParams.get('store_id') ?? '';
		if (store === STORE_A) await new Promise((res) => setTimeout(res, 1200));
		const body = overviewWith(store === STORE_A ? 111 : 222);
		await r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
	});
	await login(page);
	await page.goto(`?store_id=${STORE_A}`);
	await selector(page).selectOption(STORE_B);
	await expect(selector(page)).toHaveValue(STORE_B);
	await page.waitForTimeout(1600);
	await expect(page.getByText('222', { exact: true }).first()).toBeVisible();
	await expect(page.getByText('111', { exact: true })).toHaveCount(0);
});

test('dashboard layout stays within budget across reference viewports', async ({ page }) => {
	await mockStoreScope(page);
	await login(page);
	for (const vp of [{ width: 1366, height: 768 }, { width: 1440, height: 900 }, { width: 1536, height: 1024 }, { width: 1920, height: 1080 }]) {
		await page.setViewportSize(vp);
		await page.goto(`?store_id=${STORE_A}`);
		await expect(page.getByText('صافي المبيعات', { exact: true })).toBeVisible({ timeout: 15000 });
		await page.waitForTimeout(400);
		const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
		expect(overflow, `no horizontal overflow at ${vp.width}`).toBeLessThanOrEqual(1);
	}
});
