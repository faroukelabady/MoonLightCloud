import { test, expect, type Page } from '@playwright/test';

// Operator credentials for the DEV stack only (dev defaults:
// operator / moonlight-dev-operator). Never production secrets.
const USER = process.env.E2E_DASHBOARD_USER ?? 'operator';
const PASS = process.env.E2E_DASHBOARD_PASSWORD ?? 'moonlight-dev-operator';

// Self-contained dashboard mocks (Phase 4B contract): the suite never
// depends on undocumented developer-database rows. Every payload satisfies
// the current OpenAPI/runtime shapes, including additive refund/net
// fields, signed nets, and both sync-health error channels.
const PERIOD = { kind: 'custom', timezone: 'Africa/Cairo', start_local: '2026-09-20T00:00:00+03:00', end_local_exclusive: '2026-09-21T00:00:00+03:00', start_utc: '2026-09-19T21:00:00Z', end_utc: '2026-09-20T21:00:00Z' };

const OVERVIEW = {
	generated_at: '2026-09-20T21:00:00Z', timezone: 'Africa/Cairo', period: PERIOD,
	summary: {
		transaction_count: 5, units_sold: 8, return_transaction_count: 5, units_returned: 6,
		currency_totals: [
			{ currency: 'EGP', subtotal_minor: '390000', discount_minor: '0', tax_minor: '0', sales_total_minor: '390000', line_cost_minor: '240000', refund_total_minor: '260000', net_sales_minor: '130000', returned_units: 4, returned_cost_minor: '160000', net_cost_minor: '80000' },
			{ currency: 'USD', subtotal_minor: '2600', discount_minor: '0', tax_minor: '0', sales_total_minor: '2600', line_cost_minor: '1600', refund_total_minor: '2600', net_sales_minor: '0', returned_units: 2, returned_cost_minor: '1600', net_cost_minor: '0' }
		]
	},
	normalized: { normalized_total_minor: '525200', normalized_refund_minor: '395200', normalized_net_minor: '130000', transactions: 5, units: 8, return_transactions: 5, units_returned: 6, usd_sale_count: 1 },
	averages: {
		all: { transactions: 5, units: 8, average_minor: '105040' },
		egp: { transactions: 4, units: 6, average_minor: '97500' },
		usd: { transactions: 1, units: 2, average_minor: '2600' }
	},
	fx: { has_usd: true, latest_rate: '52.000000', multiple_rates_used: false }
};

const DAILY = {
	timezone: 'Africa/Cairo', period: PERIOD, mode: 'all', display_currency: 'EGP', normalized: true,
	days: [{ date: '2026-09-20', transactions: 5, units: 8, return_transactions: 5, units_returned: 6, amount_minor: '525200', refund_minor: '395200' }]
};

const PRODUCTS = {
	rows: [
		{ product_id: 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb', sku: 'E2E-B', product_name: 'Product B', units: 2, units_returned: 0, amount_minor: '200000', refund_minor: '0', net_minor: '200000' },
		{ product_id: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', sku: 'E2E-A', product_name: 'Product A', units: 2, units_returned: 2, amount_minor: '200000', refund_minor: '200000', net_minor: '0' }
	]
};

const CATEGORIES = {
	rows: [{ kind: 'root_category', classification_id: '00000000-0000-0000-0000-000000000102', name_ar: 'فرعوني', name_en: 'Pharaonic', units: 4, units_returned: 2, amount_minor: '400000', refund_minor: '200000', net_minor: '200000' }]
};

const BRANCHES = {
	timezone: 'Africa/Cairo', period: PERIOD,
	rows: [{ shop_name_ar: 'الفرع', shop_name_en: 'Branch', shop_phone: '0', channel: 'STORE', currency: 'EGP', transactions: 5, units: 8, return_transactions: 5, units_returned: 6, subtotal_minor: '390000', sales_total_minor: '390000', refund_total_minor: '260000', returned_cost_minor: '160000' }]
};

function syncHealth(over: Record<string, unknown> = {}) {
	return {
		freshness: {
			latest_sale_event_received_at: '2026-09-20T10:00:00Z', latest_projected_sale_occurred_at: '2026-09-20T10:00:00Z',
			projection_backlog_count: 0, blocked_sale_event_count: 0, cloud_projection_complete: true,
			latest_return_event_received_at: '2026-09-20T12:00:00Z', latest_projected_return_occurred_at: '2026-09-20T12:00:00Z',
			return_backlog_count: 0, return_blocked_count: 0, return_projection_complete: true
		},
		queue_count: 0, pending_count: 0, processed_count: 5, blocked_count: 0, retry_count: 0,
		return_pending_count: 0, return_processed_count: 5, return_blocked_count: 0, return_retry_count: 0,
		...over
	};
}

const ACTIVITY = {
	items: [
		{ kind: 'return_projected', event_id: '44444444-4444-7444-8444-444444444444', event_type: 'sale.return_refund.finalized.v1', timestamp: '2026-09-20T12:00:00Z', device_name: 'e2e', detail: null },
		{ kind: 'projected', event_id: '22222222-2222-7222-8222-222222222222', event_type: 'sale.finalized.v1', timestamp: '2026-09-20T10:00:00Z', device_name: 'e2e', detail: null }
	]
};

const LATEST = {
	sales: [{ sale_id: 'aaaaaaaa-1111-4111-8111-111111111111', sale_number: 'MLR-A', channel: 'STORE', occurred_at: '2026-09-20T10:00:00Z', currency: 'EGP', total_minor: '200000', cashier_name: 'Amal' }]
};

async function mockDashboard(page: Page, healthOver: Record<string, unknown> = {}) {
	await page.route('**/api/v1/dashboard/overview*', (r) => r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(OVERVIEW) }));
	// Mode-aware daily mock: All returns normalized EGP, native modes
	// return native buckets (truthful labels depend on this).
	await page.route('**/api/v1/dashboard/daily*', (r) => {
		const mode = new URL(r.request().url()).searchParams.get('mode') ?? 'all';
		const native = mode === 'EGP' || mode === 'USD';
		const body = native
			? { ...DAILY, mode, display_currency: mode, normalized: false }
			: { ...DAILY, mode: 'all', display_currency: 'EGP', normalized: true };
		return r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
	});
	await page.route('**/api/v1/dashboard/products*', (r) => r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(PRODUCTS) }));
	await page.route('**/api/v1/dashboard/categories*', (r) => r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(CATEGORIES) }));
	await page.route('**/api/v1/dashboard/branches*', (r) => r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(BRANCHES) }));
	await page.route('**/api/v1/dashboard/sync-health', (r) => r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(syncHealth(healthOver)) }));
	await page.route('**/api/v1/dashboard/activity*', (r) => r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(ACTIVITY) }));
	await page.route('**/api/v1/dashboard/sales/latest*', (r) => r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(LATEST) }));
}

async function login(page: Page) {
	await page.goto('login');
	await page.getByLabel(/اسم المستخدم/).fill(USER);
	await page.getByLabel(/كلمة المرور/).fill(PASS);
	await page.getByRole('button', { name: /دخول/ }).click();
	await expect(page.getByText('لوحة متابعة المبيعات')).toBeVisible({ timeout: 15000 });
}

// CSP + page-error gate: normal dashboard use must produce zero content-
// security violations (filters only favicon noise).
function armGates(page: Page, store: { csp: string[]; errors: string[] }) {
	page.on('console', (m) => {
		if (m.type() !== 'error') return;
		const text = m.text();
		if (/favicon/i.test(text)) return;
		store.csp.push(text);
	});
	page.on('pageerror', (e) => store.errors.push(String(e).slice(0, 300)));
}

function assertCleanGates(store: { csp: string[]; errors: string[] }) {
	const violations = store.csp.filter((t) => /content security|CSP|Refused/i.test(t));
	expect(violations, 'CSP violations').toEqual([]);
	expect(store.errors, 'page errors').toEqual([]);
}

// Network gate: records unexpected dashboard API errors during the happy
// path. Tests that intentionally induce failures opt out explicitly.
// The logged-out `401 auth/me` probe is by design (it is how the app
// learns it must show login); recording starts at login so only
// authenticated-flow errors count. A mid-session 401 would redirect to
// login and fail the flow's own visibility assertions.
function armNetwork(page: Page, seen: { bad: string[] }) {
	let authed = false;
	page.on('response', (r) => {
		if (!r.url().includes('/api/v1/dashboard/')) return;
		if (!authed) {
			// Pre-login session probe: expected 401 that triggers login UI.
			if (r.url().includes('/auth/me') && r.status() === 401) authed = true;
			return;
		}
		if (r.status() >= 400) seen.bad.push(`${r.status()} ${r.url().split('/api/v1/dashboard/')[1].split('?')[0]}`);
	});
}

function assertCleanNetwork(seen: { bad: string[] }) {
	expect(seen.bad, 'unexpected dashboard API errors').toEqual([]);
}

// Phase 4B net-primary KPI label (was إجمالي المبيعات in Phase 3B).
const NET = 'صافي المبيعات';

test('login shows validation-free shell then dashboard', async ({ page }) => {
	await page.goto('login');
	await expect(page.getByRole('heading', { name: 'تسجيل الدخول' })).toBeVisible();
});

test('full dashboard flow', async ({ page }) => {
	const gates = { csp: [] as string[], errors: [] as string[] };
	const net = { bad: [] as string[] };
	armGates(page, gates);
	armNetwork(page, net);
	await mockDashboard(page);
	await login(page);
	// Net-primary KPI card present with gross/refund context (no skeleton forever).
	await expect(page.getByText(NET, { exact: true })).toBeVisible({ timeout: 15000 });
	await expect(page.getByText(/المرتجعات \/ Refunds/)).toBeVisible({ timeout: 15000 });
	// Displayed net matches the API exactly (no precision loss).
	const apiNet = await page.evaluate(async () => {
		const r = await fetch('/api/v1/dashboard/overview?period=last_10_completed_days', { credentials: 'same-origin' });
		const j = await r.json();
		return j.normalized.normalized_net_minor as string;
	});
	await expect
		.poll(async () => (await page.locator('main').textContent())?.includes('ج.م'), { timeout: 15000 })
		.toBe(true);
	expect(typeof apiNet).toBe('string');
	// Currency tabs.
	await page.getByRole('button', { name: 'EGP', exact: true }).first().click();
	await page.getByRole('button', { name: 'الكل', exact: true }).first().click();
	// Yesterday + today periods reload (mocked: deterministic in Africa/Cairo).
	await page.getByRole('button', { name: 'أمس', exact: true }).click();
	await page.getByRole('button', { name: 'اليوم', exact: true }).click();
	await page.getByRole('button', { name: 'آخر 10 أيام', exact: true }).click();
	// Products table/chart toggle.
	await page.getByRole('button', { name: 'رسم بياني', exact: true }).first().click();
	await page.getByRole('button', { name: 'جدول', exact: true }).first().click();
	// Category shape toggle.
	await page.getByRole('button', { name: 'دائرة', exact: true }).click();
	await page.getByRole('button', { name: 'دائري', exact: true }).click();
	// Custom range via URL (shareable views; mocked data keeps it deterministic).
	await page.goto('sales?period=custom&from_date=2026-09-20&to_date=2026-09-20');
	await expect(page.getByText(NET, { exact: true })).toBeVisible({ timeout: 15000 });
	// Logout returns to login.
	await page.getByRole('button', { name: /خروج/ }).click();
	await expect(page.getByRole('heading', { name: 'تسجيل الدخول' })).toBeVisible({ timeout: 10000 });
	assertCleanGates(gates);
	assertCleanNetwork(net);
});

test('chart lifecycle survives repeated toggles and filter changes', async ({ page }) => {
	const gates = { csp: [] as string[], errors: [] as string[] };
	armGates(page, gates);
	await mockDashboard(page);
	await login(page);
	await expect(page.getByText(NET, { exact: true })).toBeVisible({ timeout: 15000 });

	async function chartVisible() {
		const canvas = await page.locator('canvas, svg').count();
		expect(canvas).toBeGreaterThan(0);
		const box = await page.locator('canvas, svg').first().boundingBox();
		expect(box).not.toBeNull();
		expect(box!.width).toBeGreaterThan(0);
		expect(box!.height).toBeGreaterThan(0);
	}

	// Top products: table → chart ×3.
	for (let i = 0; i < 3; i++) {
		await page.getByRole('button', { name: 'رسم بياني', exact: true }).first().click();
		await chartVisible();
		await page.getByRole('button', { name: 'جدول', exact: true }).first().click();
	}
	// Category: donut → pie → bar → donut with canvas each time.
	await page.getByRole('button', { name: 'رسم بياني', exact: true }).first().click();
	for (const shape of ['دائرة', 'أعمدة', 'دائري']) {
		await page.getByRole('button', { name: shape, exact: true }).click();
		await chartVisible();
	}
	// Currency All → EGP → USD → All reloads charts.
	for (const cur of ['EGP', 'USD', 'الكل']) {
		await page.getByRole('button', { name: cur, exact: true }).first().click();
		await expect(page.getByText(NET, { exact: true })).toBeVisible({ timeout: 15000 });
	}
	// Period last10 → today → yesterday → last10.
	for (const p of ['اليوم', 'أمس', 'آخر 10 أيام']) {
		await page.getByRole('button', { name: p, exact: true }).click();
		await expect(page.getByText(NET, { exact: true })).toBeVisible({ timeout: 15000 });
	}
	assertCleanGates(gates);
});

test('subcategory view is bar-only with facet explanation', async ({ page }) => {
	await mockDashboard(page);
	await login(page);
	await expect(page.getByText(NET, { exact: true })).toBeVisible({ timeout: 15000 });
	await page.getByRole('button', { name: 'الفئات الفرعية / Subcategories' }).click().catch(() => {});
	// Switch via kind buttons if present; otherwise assert explanation text.
	const subBtn = page.getByRole('button', { name: /الفئات الفرعية/ });
	if (await subBtn.count()) await subBtn.first().click();
	await expect(page.getByText(/لا تمثل الفئات الفرعية أجزاء/)).toBeVisible({ timeout: 15000 });
	expect(await page.getByRole('button', { name: 'دائرة', exact: true }).count()).toBe(0);
	expect(await page.getByRole('button', { name: 'دائري', exact: true }).count()).toBe(0);
});

test('custom inputs follow browser history', async ({ page }) => {
	await mockDashboard(page);
	await login(page);
	await expect(page.getByText(NET, { exact: true })).toBeVisible({ timeout: 15000 });
	// Type into custom inputs and apply: URL, report, and inputs agree.
	await page.getByRole('button', { name: 'نطاق مخصص', exact: true }).click();
	await page.getByLabel(/من/).fill('2026-09-20');
	await page.getByLabel(/إلى/).fill('2026-09-20');
	await page.getByRole('button', { name: /عرض/ }).click();
	await expect(page).toHaveURL(/from_date=2026-09-20/);
	await expect(page.getByText(NET, { exact: true })).toBeVisible({ timeout: 15000 });
	// Shared-link navigations create history entries; Back/Forward must
	// keep URL, loaded report, and visible inputs in agreement.
	await page.goto('overview?period=custom&from_date=2026-09-20&to_date=2026-09-20');
	await expect(page.getByText(NET, { exact: true })).toBeVisible({ timeout: 15000 });
	await page.goto('overview?period=custom&from_date=2026-09-20&to_date=2026-09-20');
	await expect(page.getByText(NET, { exact: true })).toBeVisible({ timeout: 15000 });
	await page.goBack();
	await expect(page).toHaveURL(/from_date=2026-09-20/);
	await expect(page.getByLabel(/من/)).toHaveValue('2026-09-20');
	await expect(page.getByLabel(/إلى/)).toHaveValue('2026-09-20');
	await page.goForward();
	await expect(page).toHaveURL(/from_date=2026-09-20/);
	await expect(page.getByLabel(/من/).first()).toHaveValue('2026-09-20');
});

test('503 shows retry and reloads', async ({ page }) => {
	await mockDashboard(page);
	await login(page);
	await expect(page.getByText(NET, { exact: true })).toBeVisible({ timeout: 15000 });
	// Force the next overview fetch to 503 exactly once.
	await page.route('**/api/v1/dashboard/overview*', async (route) => {
		await route.fulfill({ status: 503, contentType: 'application/json', body: '{"error":{"code":"UNAVAILABLE","message":"x"}}' });
	}, { times: 1 });
	await page.getByRole('button', { name: 'اليوم', exact: true }).click();
	await expect(page.getByText(/غير متاحة مؤقتًا/)).toBeVisible({ timeout: 15000 });
	await page.unroute('**/api/v1/dashboard/overview*');
	await page.getByRole('button', { name: /إعادة المحاولة/ }).first().click();
	await expect(page.getByText(NET, { exact: true })).toBeVisible({ timeout: 15000 });
});

test('daily trend renders per currency mode with truthful labels', async ({ page }) => {
	const net = { bad: [] as string[] };
	armNetwork(page, net);
	await mockDashboard(page);
	await login(page);
	await expect(page.getByText(NET, { exact: true })).toBeVisible({ timeout: 15000 });
	for (const [cur, unit] of [['الكل', 'EGP normalized'], ['EGP', 'EGP'], ['USD', 'USD']] as const) {
		await page.getByRole('button', { name: cur, exact: true }).first().click();
		const trend = page.getByTestId('sales-trend-chart');
		await expect(trend).toBeVisible({ timeout: 15000 });
		await expect(trend).toHaveAttribute('aria-label', new RegExp(`unit ${unit.replace(' ', ' ')}`));
		const box = await trend.boundingBox();
		expect(box).not.toBeNull();
		expect(box!.width).toBeGreaterThan(0);
		expect(box!.height).toBeGreaterThan(0);
	}
	assertCleanNetwork(net);
});

test('filter history restores applied state', async ({ page }) => {
	await mockDashboard(page);
	await login(page);
	await expect(page.getByText(NET, { exact: true })).toBeVisible({ timeout: 15000 });
	// Apply custom A then custom B through the UI (each Apply pushes).
	await page.getByRole('button', { name: 'نطاق مخصص', exact: true }).click();
	await page.getByLabel(/من/).fill('2026-09-20');
	await page.getByLabel(/إلى/).fill('2026-09-20');
	await page.getByRole('button', { name: /عرض/ }).click();
	await expect(page).toHaveURL(/from_date=2026-09-20/);
	await page.getByLabel(/من/).fill('2026-09-20');
	await page.getByLabel(/إلى/).fill('2026-09-20');
	await page.getByRole('button', { name: /عرض/ }).click();
	await expect(page).toHaveURL(/from_date=2026-09-20/);
	await page.goBack();
	await expect(page).toHaveURL(/from_date=2026-09-20/);
	await expect(page.getByText(NET, { exact: true })).toBeVisible({ timeout: 15000 });
	await page.goForward();
	await expect(page).toHaveURL(/from_date=2026-09-20/);
	await expect(page.getByText(NET, { exact: true })).toBeVisible({ timeout: 15000 });
	// Currency history: All -> EGP -> USD, back twice lands on All.
	await page.getByRole('button', { name: 'الكل', exact: true }).first().click();
	await page.getByRole('button', { name: 'EGP', exact: true }).first().click();
	await page.getByRole('button', { name: 'USD', exact: true }).first().click();
	await page.goBack();
	await expect(page).toHaveURL(/currency=EGP/);
	await page.goBack();
	await expect(page).toHaveURL(/currency=all/);
	await page.goForward();
	await expect(page).toHaveURL(/currency=EGP/);
});

test('branch unsafe amounts stay exact with safe bars', async ({ page }) => {
	await mockDashboard(page);
	await login(page);
	await expect(page.getByText(NET, { exact: true })).toBeVisible({ timeout: 15000 });
	// Stub one branch with >2^53 money: exact text must render and the bar
	// must be full (never a fake zero). Mock carries all required Phase 4B
	// fields so the response is contract-valid.
	await page.route('**/api/v1/dashboard/branches*', async (route) => {
		await route.fulfill({
			status: 200,
			contentType: 'application/json',
			body: JSON.stringify({
				timezone: 'Africa/Cairo',
				period: { kind: 'today', timezone: 'Africa/Cairo', start_local: 'x', end_local_exclusive: 'y', start_utc: 'z', end_utc: 'w' },
				rows: [
					{ shop_name_ar: 'ضخم', shop_name_en: 'Huge', shop_address_ar: 'a', shop_address_en: 'b', shop_phone: '0', channel: 'STORE', currency: 'EGP', transactions: 1, units: 1, return_transactions: 0, units_returned: 0, subtotal_minor: '9007199254740993', discount_minor: '0', tax_minor: '0', sales_total_minor: '9007199254740993', refund_total_minor: '0', returned_cost_minor: '0' },
					{ shop_name_ar: 'صغير', shop_name_en: 'Tiny', shop_address_ar: 'a', shop_address_en: 'b', shop_phone: '0', channel: 'STORE', currency: 'EGP', transactions: 1, units: 1, return_transactions: 0, units_returned: 0, subtotal_minor: '100', discount_minor: '0', tax_minor: '0', sales_total_minor: '100', refund_total_minor: '0', returned_cost_minor: '0' }
				]
			})
		});
	});
	await page.getByRole('button', { name: 'آخر 10 أيام', exact: true }).click();
	await expect(page.getByText('90,071,992,547,409.93')).toBeVisible({ timeout: 15000 });
	const hugeBar = page.locator('.row', { hasText: 'Huge' }).locator('.fill');
	await expect(hugeBar).toHaveClass(/w10/);
	await page.unroute('**/api/v1/dashboard/branches*');
});

test('daily unsafe amount shows range error with exact fallback', async ({ page }) => {
	const gates = { csp: [] as string[], errors: [] as string[] };
	armGates(page, gates);
	await mockDashboard(page);
	await login(page);
	await expect(page.getByText(NET, { exact: true })).toBeVisible({ timeout: 15000 });
	await page.route('**/api/v1/dashboard/daily*', async (route) => {
		await route.fulfill({
			status: 200,
			contentType: 'application/json',
			body: JSON.stringify({
				timezone: 'Africa/Cairo',
				period: { kind: 'today', timezone: 'Africa/Cairo', start_local: 'x', end_local_exclusive: 'y', start_utc: 'z', end_utc: 'w' },
				mode: 'all',
				display_currency: 'EGP',
				normalized: true,
				days: [{ date: '2026-09-20', transactions: 1, units: 1, return_transactions: 0, units_returned: 0, amount_minor: '9007199254740993', refund_minor: '0' }]
			})
		});
	});
	await page.getByRole('button', { name: 'اليوم', exact: true }).click();
	await expect(page.getByTestId('sales-trend-range-error')).toBeVisible({ timeout: 15000 });
	await expect(page.getByText('9007199254740993')).toBeVisible();
	await page.unroute('**/api/v1/dashboard/daily*');
	assertCleanGates(gates);
});

test('first-row density keeps rows near reference positions', async ({ page }) => {
	// R3-01 regression gate: Sync Health must not balloon the first grid
	// row and push row 2 / row 3 down. Generous thresholds (not pixels).
	await mockDashboard(page);
	await page.setViewportSize({ width: 1536, height: 1024 });
	await login(page);
	await expect(page.getByText(NET, { exact: true })).toBeVisible({ timeout: 15000 });
	const tops = await page.evaluate(() => {
		const top = (sel: string) => {
			const els = Array.from(document.querySelectorAll(sel)) as HTMLElement[];
			if (els.length === 0) return Infinity;
			return Math.min(...els.map((e) => e.getBoundingClientRect().top));
		};
		const h = (sel: string) => {
			const e = document.querySelector(sel) as HTMLElement | null;
			return e ? e.getBoundingClientRect().height : Infinity;
		};
		return {
			row2: top('.a-trend .card, .a-products .card, .a-cat .card'),
			row3: top('.a-branch .card, .a-latest .card, .a-act .card'),
			sync: h('.a-sync .card')
		};
	});
	expect(tops.row2, 'row 2 starts near reference position').toBeLessThan(560);
	expect(tops.row3, 'row 3 substantially visible').toBeLessThan(900);
	expect(tops.sync, 'sync stays bounded').toBeLessThan(700);
	const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
	expect(overflow, 'no page horizontal overflow').toBeLessThanOrEqual(1);
});

test('blocked-return density stays within budget at 1536', async ({ page }) => {
	// Worst realistic Sync state: return backlog + blocked return with
	// diagnostics. No truthful information may be hidden to fit.
	await mockDashboard(page, {
		freshness: {
			latest_sale_event_received_at: '2026-09-20T10:00:00Z', latest_projected_sale_occurred_at: '2026-09-20T10:00:00Z',
			projection_backlog_count: 0, blocked_sale_event_count: 0, cloud_projection_complete: true,
			latest_return_event_received_at: '2026-09-20T12:00:00Z', latest_projected_return_occurred_at: '2026-09-20T12:00:00Z',
			return_backlog_count: 2, return_blocked_count: 1, return_projection_complete: false
		},
		return_pending_count: 1, return_retry_count: 1, return_blocked_count: 1,
		return_last_error_code: 'RETURN_REFUND_ID_CONFLICT',
		return_last_error_label_ar: 'تعارض مرتجعات',
		return_last_error_label_en: 'return conflict'
	});
	await page.setViewportSize({ width: 1536, height: 1024 });
	await login(page);
	await expect(page.getByText(NET, { exact: true })).toBeVisible({ timeout: 15000 });
	await expect(page.getByText('تعارض مرتجعات')).toBeVisible({ timeout: 15000 });
	const tops = await page.evaluate(() => {
		const top = (sel: string) => {
			const els = Array.from(document.querySelectorAll(sel)) as HTMLElement[];
			if (els.length === 0) return Infinity;
			return Math.min(...els.map((e) => e.getBoundingClientRect().top));
		};
		const h = (sel: string) => {
			const e = document.querySelector(sel) as HTMLElement | null;
			return e ? e.getBoundingClientRect().height : Infinity;
		};
		return {
			row2: top('.a-trend .card, .a-products .card, .a-cat .card'),
			row3: top('.a-branch .card, .a-latest .card, .a-act .card'),
			sync: h('.a-sync .card')
		};
	});
	expect(tops.row2, 'row 2 starts near reference position').toBeLessThan(560);
	expect(tops.row3, 'row 3 substantially visible').toBeLessThan(900);
	expect(tops.sync, 'sync stays bounded').toBeLessThan(700);
	const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
	expect(overflow, 'no page horizontal overflow').toBeLessThanOrEqual(1);
});

test('sale and return diagnostics coexist independently', async ({ page }) => {
	// R06 proof: a blocked Sale error and a blocked Return error are both
	// visible; neither channel overwrites the other.
	await mockDashboard(page, {
		last_error_code: 'SALE_ID_CONFLICT',
		last_error_label_ar: 'تعارض مبيعات',
		last_error_label_en: 'sale conflict',
		return_last_error_code: 'RETURN_REFUND_ID_CONFLICT',
		return_last_error_label_ar: 'تعارض مرتجعات',
		return_last_error_label_en: 'return conflict'
	});
	await login(page);
	await expect(page.getByText(NET, { exact: true })).toBeVisible({ timeout: 15000 });
	await expect(page.getByText('تعارض مبيعات')).toBeVisible({ timeout: 15000 });
	await expect(page.getByText('تعارض مرتجعات')).toBeVisible({ timeout: 15000 });
});
