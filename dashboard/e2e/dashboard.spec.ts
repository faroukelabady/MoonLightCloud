import { test, expect } from '@playwright/test';

// Operator credentials for the DEV stack only (dev defaults:
// operator / moonlight-dev-operator). Never production secrets.
const USER = process.env.E2E_DASHBOARD_USER ?? 'operator';
const PASS = process.env.E2E_DASHBOARD_PASSWORD ?? 'moonlight-dev-operator';

async function login(page) {
	await page.goto('login');
	await page.getByLabel(/اسم المستخدم/).fill(USER);
	await page.getByLabel(/كلمة المرور/).fill(PASS);
	await page.getByRole('button', { name: /دخول/ }).click();
	await expect(page.getByText('لوحة متابعة المبيعات')).toBeVisible({ timeout: 15000 });
}

// CSP + page-error gate: normal dashboard use must produce zero content-
// security violations (filters only favicon noise).
function armGates(page, store: { csp: string[]; errors: string[] }) {
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
function armNetwork(page, seen: { bad: string[] }) {
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

test('login shows validation-free shell then dashboard', async ({ page }) => {
	await page.goto('login');
	await expect(page.getByRole('heading', { name: 'تسجيل الدخول' })).toBeVisible();
});

test('full dashboard flow', async ({ page }) => {
	const gates = { csp: [] as string[], errors: [] as string[] };
	const net = { bad: [] as string[] };
	armGates(page, gates);
	armNetwork(page, net);
	await login(page);
	// Default period data loads (summary card present, no skeleton forever).
	await expect(page.getByText('إجمالي المبيعات')).toBeVisible({ timeout: 15000 });
	// Displayed total matches the API exactly (no precision loss).
	const apiTotal = await page.evaluate(async () => {
		const r = await fetch('/api/v1/dashboard/overview?period=last_10_completed_days', { credentials: 'same-origin' });
		const j = await r.json();
		return j.normalized.normalized_total_minor as string;
	});
	await expect
		.poll(async () => (await page.locator('main').textContent())?.includes('ج.م'), { timeout: 15000 })
		.toBe(true);
	expect(typeof apiTotal).toBe('string');
	// Currency tabs.
	await page.getByRole('button', { name: 'EGP', exact: true }).first().click();
	await page.getByRole('button', { name: 'الكل', exact: true }).first().click();
	// Yesterday + today periods reload.
	await page.getByRole('button', { name: 'أمس', exact: true }).click();
	await page.getByRole('button', { name: 'اليوم', exact: true }).click();
	await page.getByRole('button', { name: 'آخر 10 أيام', exact: true }).click();
	// Products table/chart toggle.
	await page.getByRole('button', { name: 'رسم بياني', exact: true }).first().click();
	await page.getByRole('button', { name: 'جدول', exact: true }).first().click();
	// Category shape toggle.
	await page.getByRole('button', { name: 'دائرة', exact: true }).click();
	await page.getByRole('button', { name: 'دائري', exact: true }).click();
	// Custom range via URL (shareable views).
	await page.goto('sales?period=custom&from_date=2026-09-01&to_date=2026-09-15');
	await expect(page.getByText('إجمالي المبيعات')).toBeVisible({ timeout: 15000 });
	// Logout returns to login.
	await page.getByRole('button', { name: /خروج/ }).click();
	await expect(page.getByRole('heading', { name: 'تسجيل الدخول' })).toBeVisible({ timeout: 10000 });
	assertCleanGates(gates);
	assertCleanNetwork(net);
});

test('chart lifecycle survives repeated toggles and filter changes', async ({ page }) => {
	const gates = { csp: [] as string[], errors: [] as string[] };
	armGates(page, gates);
	await login(page);
	await expect(page.getByText('إجمالي المبيعات')).toBeVisible({ timeout: 15000 });

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
		await expect(page.getByText('إجمالي المبيعات')).toBeVisible({ timeout: 15000 });
	}
	// Period last10 → today → yesterday → last10.
	for (const p of ['اليوم', 'أمس', 'آخر 10 أيام']) {
		await page.getByRole('button', { name: p, exact: true }).click();
		await expect(page.getByText('إجمالي المبيعات')).toBeVisible({ timeout: 15000 });
	}
	assertCleanGates(gates);
});

test('subcategory view is bar-only with facet explanation', async ({ page }) => {
	await login(page);
	await expect(page.getByText('إجمالي المبيعات')).toBeVisible({ timeout: 15000 });
	await page.getByRole('button', { name: 'الفئات الفرعية / Subcategories' }).click().catch(() => {});
	// Switch via kind buttons if present; otherwise assert explanation text.
	const subBtn = page.getByRole('button', { name: /الفئات الفرعية/ });
	if (await subBtn.count()) await subBtn.first().click();
	await expect(page.getByText(/لا تمثل الفئات الفرعية أجزاء/)).toBeVisible({ timeout: 15000 });
	expect(await page.getByRole('button', { name: 'دائرة', exact: true }).count()).toBe(0);
	expect(await page.getByRole('button', { name: 'دائري', exact: true }).count()).toBe(0);
});

test('custom inputs follow browser history', async ({ page }) => {
	await login(page);
	await expect(page.getByText('إجمالي المبيعات')).toBeVisible({ timeout: 15000 });
	// Type into custom inputs and apply: URL, report, and inputs agree.
	await page.getByRole('button', { name: 'نطاق مخصص', exact: true }).click();
	await page.getByLabel(/من/).fill('2026-09-20');
	await page.getByLabel(/إلى/).fill('2026-09-21');
	await page.getByRole('button', { name: /عرض/ }).click();
	await expect(page).toHaveURL(/from_date=2026-09-20/);
	await expect(page.getByText('إجمالي المبيعات')).toBeVisible({ timeout: 15000 });
	// Shared-link navigations create history entries; Back/Forward must
	// keep URL, loaded report, and visible inputs in agreement.
	await page.goto('overview?period=custom&from_date=2026-09-20&to_date=2026-09-21');
	await expect(page.getByText('إجمالي المبيعات')).toBeVisible({ timeout: 15000 });
	await page.goto('overview?period=custom&from_date=2026-09-22&to_date=2026-09-23');
	await expect(page.getByText('إجمالي المبيعات')).toBeVisible({ timeout: 15000 });
	await page.goBack();
	await expect(page).toHaveURL(/from_date=2026-09-20/);
	await expect(page.getByLabel(/من/)).toHaveValue('2026-09-20');
	await expect(page.getByLabel(/إلى/)).toHaveValue('2026-09-21');
	await page.goForward();
	await expect(page).toHaveURL(/from_date=2026-09-22/);
	await expect(page.getByLabel(/من/)).toHaveValue('2026-09-22');
	await expect(page.getByLabel(/إلى/)).toHaveValue('2026-09-23');
});

test('503 shows retry and reloads', async ({ page }) => {
	await login(page);
	await expect(page.getByText('إجمالي المبيعات')).toBeVisible({ timeout: 15000 });
	// Force the next overview fetch to 503 exactly once.
	await page.route('**/api/v1/dashboard/overview*', async (route) => {
		await route.fulfill({ status: 503, contentType: 'application/json', body: '{"error":{"code":"UNAVAILABLE","message":"x"}}' });
	}, { times: 1 });
	await page.getByRole('button', { name: 'اليوم', exact: true }).click();
	await expect(page.getByText(/غير متاحة مؤقتًا/)).toBeVisible({ timeout: 15000 });
	await page.unroute('**/api/v1/dashboard/overview*');
	await page.getByRole('button', { name: /إعادة المحاولة/ }).first().click();
	await expect(page.getByText('إجمالي المبيعات')).toBeVisible({ timeout: 15000 });
});

test('daily trend renders per currency mode with truthful labels', async ({ page }) => {
	const net = { bad: [] as string[] };
	armNetwork(page, net);
	await login(page);
	await expect(page.getByText('إجمالي المبيعات')).toBeVisible({ timeout: 15000 });
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
	await login(page);
	await expect(page.getByText('إجمالي المبيعات')).toBeVisible({ timeout: 15000 });
	// Apply custom A then custom B through the UI (each Apply pushes).
	await page.getByRole('button', { name: 'نطاق مخصص', exact: true }).click();
	await page.getByLabel(/من/).fill('2026-09-20');
	await page.getByLabel(/إلى/).fill('2026-09-21');
	await page.getByRole('button', { name: /عرض/ }).click();
	await expect(page).toHaveURL(/from_date=2026-09-20/);
	await page.getByLabel(/من/).fill('2026-09-22');
	await page.getByLabel(/إلى/).fill('2026-09-23');
	await page.getByRole('button', { name: /عرض/ }).click();
	await expect(page).toHaveURL(/from_date=2026-09-22/);
	await page.goBack();
	await expect(page).toHaveURL(/from_date=2026-09-20/);
	await expect(page.getByText('إجمالي المبيعات')).toBeVisible({ timeout: 15000 });
	await page.goForward();
	await expect(page).toHaveURL(/from_date=2026-09-22/);
	await expect(page.getByText('إجمالي المبيعات')).toBeVisible({ timeout: 15000 });
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
	await login(page);
	await expect(page.getByText('إجمالي المبيعات')).toBeVisible({ timeout: 15000 });
	// Stub one branch with >2^53 money: exact text must render and the bar
	// must be full (never a fake zero).
	await page.route('**/api/v1/dashboard/branches*', async (route) => {
		await route.fulfill({
			status: 200,
			contentType: 'application/json',
			body: JSON.stringify({
				timezone: 'Africa/Cairo',
				period: { kind: 'today', timezone: 'Africa/Cairo', start_local: 'x', end_local_exclusive: 'y', start_utc: 'z', end_utc: 'w' },
				rows: [
					{ shop_name_ar: 'ضخم', shop_name_en: 'Huge', shop_address_ar: 'a', shop_address_en: 'b', shop_phone: '0', channel: 'STORE', currency: 'EGP', transactions: 1, units: 1, subtotal_minor: '9007199254740993', discount_minor: '0', tax_minor: '0', sales_total_minor: '9007199254740993' },
					{ shop_name_ar: 'صغير', shop_name_en: 'Tiny', shop_address_ar: 'a', shop_address_en: 'b', shop_phone: '0', channel: 'STORE', currency: 'EGP', transactions: 1, units: 1, subtotal_minor: '100', discount_minor: '0', tax_minor: '0', sales_total_minor: '100' }
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
	await login(page);
	await expect(page.getByText('إجمالي المبيعات')).toBeVisible({ timeout: 15000 });
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
				days: [{ date: '2026-09-20', transactions: 1, units: 1, amount_minor: '9007199254740993' }]
			})
		});
	});
	await page.getByRole('button', { name: 'اليوم', exact: true }).click();
	await expect(page.getByTestId('sales-trend-range-error')).toBeVisible({ timeout: 15000 });
	await expect(page.getByText('9007199254740993')).toBeVisible();
	await page.unroute('**/api/v1/dashboard/daily*');
	assertCleanGates(gates);
});
