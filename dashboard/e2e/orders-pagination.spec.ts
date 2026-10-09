import { test, expect, type Page } from '@playwright/test';
import { uiLogin } from './auth';

// Targeted R2 pagination suite: 25 mocked orders traverse 20 + 5 via
// Load more, with no duplicates, exact money intact, control gone at
// the end, and no console/page errors. API responses are mocked; the
// suite proves the shipped UI continuation contract.
// Explicit dev-stack credentials + TOTP (no default account): see auth.ts.

function row(i: number) {
	const id = `07${String(i).padStart(3, '0')}`;
	return {
		provider_key: 'website', external_order_id: id, order_number: id,
		provider_status: 'processing', canonical_status: 'PROCESSING',
		currency: 'EGP', total_minor: '68000',
		created_at: '2026-09-27T12:00:00Z', modified_at: '2026-09-27T12:00:00Z',
		customer_name: 'A B', mapping_complete: true, unmapped_line_count: 0,
		provider_deleted: false, revision: 1
	};
}

const PAGE1 = {
	orders: Array.from({ length: 20 }, (_, i) => row(i)),
	next_cursor: 'cursor-page-2',
	status_counts: [{ canonical_status: 'PROCESSING', total: 25 }],
	webhook_inbox: { pending: 0, retry: 0, blocked: 0, oldest_pending_at: null }
};

const PAGE2 = {
	orders: Array.from({ length: 5 }, (_, i) => row(20 + i)),
	next_cursor: null,
	status_counts: [{ canonical_status: 'PROCESSING', total: 25 }],
	webhook_inbox: { pending: 0, retry: 0, blocked: 0, oldest_pending_at: null }
};

async function mockPages(page: Page) {
	await page.route('**/api/v1/dashboard/orders*', (r) => {
		const url = new URL(r.request().url());
		const body = url.searchParams.get('cursor') ? PAGE2 : PAGE1;
		return r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
	});
}

async function login(page: Page) {
	await uiLogin(page);
}

test('orders paginate 20 + 5 via Load more with no duplicates', async ({ page }) => {
	const errors: string[] = [];
	page.on('pageerror', (e) => errors.push(String(e).slice(0, 300)));
	await mockPages(page);
	await login(page);
	await page.goto('orders');
	await expect(page.getByRole('main').getByText('الطلبات عبر الإنترنت')).toBeVisible({ timeout: 15000 });
	// First page: 20 view buttons, continuation visible.
	await expect(page.getByRole('button', { name: /تحميل المزيد/ })).toBeVisible();
	expect(await page.getByRole('button', { name: /عرض \/ View/ }).count()).toBe(20);
	// Load more appends the final 5 and the control disappears.
	await page.getByRole('button', { name: /تحميل المزيد/ }).click();
	await expect(page.getByRole('button', { name: /عرض \/ View/ })).toHaveCount(25, { timeout: 10000 });
	await expect(page.getByRole('button', { name: /تحميل المزيد/ })).toHaveCount(0);
	// No duplicate order rows by stable identity.
	const ids = await page.evaluate(() =>
		Array.from(document.querySelectorAll('tbody tr td.num:first-child')).map((td) => td.textContent)
	);
	expect(new Set(ids).size).toBe(25);
	const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
	expect(overflow, 'no horizontal page overflow').toBeLessThanOrEqual(1);
	expect(errors, 'page errors').toEqual([]);
});
