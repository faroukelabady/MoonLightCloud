import { createHmac } from 'node:crypto';
import { expect, type Page } from '@playwright/test';

// Explicit dev-stack credentials (ADR-0053): there is NO default account.
// Bootstrap one with `moonlight-cloud auth bootstrap-owner`, enroll MFA once,
// then export E2E_DASHBOARD_USER, E2E_DASHBOARD_PASSWORD and the TOTP secret
// shown at enrollment as E2E_DASHBOARD_TOTP_SECRET. Never production values.
function required(name: string): string {
	const v = process.env[name];
	if (!v) throw new Error(`${name} must be set (no default dashboard account exists)`);
	return v;
}

function base32(input: string): Buffer {
	const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';
	let bits = '';
	for (const c of input.replace(/[\s=]/g, '').toUpperCase()) bits += alphabet.indexOf(c).toString(2).padStart(5, '0');
	const out: number[] = [];
	for (let i = 0; i + 8 <= bits.length; i += 8) out.push(parseInt(bits.slice(i, i + 8), 2));
	return Buffer.from(out);
}

// RFC 6238 TOTP (SHA-1, 30 s, 6 digits).
export function totp(secret = required('E2E_DASHBOARD_TOTP_SECRET'), at = Date.now()): string {
	const counter = Buffer.alloc(8);
	counter.writeBigUInt64BE(BigInt(Math.floor(at / 30000)));
	const mac = createHmac('sha1', base32(secret)).update(counter).digest();
	const o = mac[mac.length - 1] & 0x0f;
	return String((mac.readUInt32BE(o) & 0x7fffffff) % 1_000_000).padStart(6, '0');
}

// uiLogin signs in through the real login + MFA screens.
export async function uiLogin(page: Page, url = 'login') {
	await page.goto(url);
	await page.getByLabel(/اسم الدخول|Email or login/).fill(required('E2E_DASHBOARD_USER'));
	await page.getByLabel(/كلمة المرور|Password/).fill(required('E2E_DASHBOARD_PASSWORD'));
	await page.getByRole('button', { name: /دخول/ }).click();
	await page.getByLabel(/Authenticator code/).fill(totp());
	await page.getByRole('button', { name: /Verify/ }).click();
	await expect(page.getByText('لوحة متابعة المبيعات')).toBeVisible({ timeout: 15000 });
}

// apiLogin establishes a FULL session through the API (cookie in context).
export async function apiLogin(page: Page) {
	const origin = new URL(process.env.E2E_BASE_URL ?? 'http://127.0.0.1:8080/dashboard/').origin;
	const login = await page.request.post('/api/v1/dashboard/auth/login', {
		data: { login: required('E2E_DASHBOARD_USER'), password: required('E2E_DASHBOARD_PASSWORD') },
		headers: { Origin: origin }
	});
	expect(login.status()).toBe(200);
	const { csrf_token } = (await login.json()) as { csrf_token: string };
	const verify = await page.request.post('/api/v1/dashboard/auth/mfa/verify', {
		data: { code: totp() },
		headers: { Origin: origin, 'X-CSRF-Token': csrf_token }
	});
	expect(verify.status()).toBe(200);
}
