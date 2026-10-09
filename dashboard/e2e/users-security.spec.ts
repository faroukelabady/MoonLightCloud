import { test, expect } from '@playwright/test';
import { admin, hasAdmin, uiLogin } from './auth';

// Phase 18 Users & Security surface (ADR-0053) against the live server:
// real password + TOTP sign-in, real user list and real account creation
// (a throwaway E2E database; scripts/e2e-dashboard.sh). Nothing here is
// mocked: permissions come from the server's /auth/me and every action is
// re-authorized server-side.

const usersNav = /Users & Security/;

test('OWNER opens Users & Security and sees the accounts', async ({ page }) => {
	await uiLogin(page);
	await page.getByRole('button', { name: usersNav }).click();
	await expect(page).toHaveURL(/\/dashboard\/users/);
	const users = page.getByTestId('users-page');
	await expect(users).toBeVisible();
	await expect(users.getByText(/My account/)).toContainText('OWNER');
	const rows = page.getByTestId('user-row');
	await expect(rows.filter({ hasText: process.env.E2E_DASHBOARD_USER as string })).toHaveCount(1);
	await expect(rows.filter({ hasText: process.env.E2E_DASHBOARD_USER as string })).toContainText('OWNER');
	if (hasAdmin()) {
		const adminRow = rows.filter({ hasText: admin().user });
		await expect(adminRow).toContainText('ADMIN');
		await expect(adminRow).toContainText('ACTIVE');
		// OWNER-only management controls are present for other accounts.
		await expect(adminRow.getByRole('button', { name: /Disable/ })).toBeVisible();
		await expect(adminRow.getByRole('button', { name: /Make owner/ })).toBeVisible();
	}
});

test('OWNER begins ADMIN creation and receives a one-time activation link', async ({ page }) => {
	await uiLogin(page, 'users');
	const form = page.getByTestId('create-user');
	await expect(form).toBeVisible();
	const login = `pending-${Date.now()}@e2e.test`;
	await form.getByLabel(/Login/).fill(login);
	await form.getByLabel(/Display name/).fill('Pending Admin');
	await form.getByLabel(/Role/).selectOption('ADMIN');
	await form.locator('select[multiple]').selectOption({ label: 'Store A' });
	await form.getByRole('button', { name: /Create/ }).click();
	const link = page.getByTestId('activation-link');
	await expect(link).toContainText(login);
	await expect(link.locator('input')).toHaveValue(/\/dashboard\/#activate=/);
	const row = page.getByTestId('user-row').filter({ hasText: login });
	await expect(row).toContainText('ADMIN');
	await expect(row).toContainText('PENDING_SETUP');
	await expect(row.getByRole('button', { name: /New link/ })).toBeVisible();
});

test('ADMIN does not receive OWNER-only controls', async ({ page }) => {
	test.skip(!hasAdmin(), 'requires a provisioned ADMIN (E2E_ADMIN_*; scripts/e2e-dashboard.sh)');
	await uiLogin(page, 'login', admin());
	// Navigation mirrors the server permission map: no Users & Security.
	await expect(page.getByRole('button', { name: /Updates/ })).toBeVisible();
	await expect(page.getByRole('button', { name: usersNav })).toHaveCount(0);
	// Even when opened directly, only the personal section renders.
	await page.goto('users');
	const users = page.getByTestId('users-page');
	await expect(users).toBeVisible();
	await expect(users.getByText(/My account/)).toContainText('ADMIN');
	await expect(page.getByTestId('create-user')).toHaveCount(0);
	await expect(page.getByTestId('user-row')).toHaveCount(0);
	await expect(users.getByRole('button', { name: /Make owner|Reset MFA|Disable/ })).toHaveCount(0);
	// And the server refuses the OWNER-only API regardless of the UI.
	expect((await page.request.get('/api/v1/dashboard/users')).status()).toBe(403);
	expect((await page.request.get('/api/v1/dashboard/auth-audit')).status()).toBe(403);
});
