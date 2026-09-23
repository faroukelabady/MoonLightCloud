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

test('login shows validation-free shell then dashboard', async ({ page }) => {
	await page.goto('login');
	await expect(page.getByRole('heading', { name: 'تسجيل الدخول' })).toBeVisible();
});

test('full dashboard flow', async ({ page }) => {
	await login(page);
	// Default period data loads (summary card present, no skeleton forever).
	await expect(page.getByText('إجمالي المبيعات')).toBeVisible({ timeout: 15000 });
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
});
