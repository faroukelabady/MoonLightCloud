import { createHash, createHmac } from 'node:crypto';
import { mkdirSync, openSync, closeSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { expect, type Page } from '@playwright/test';

// Explicit E2E accounts (ADR-0053): there is NO default account. The runner
// (scripts/e2e-dashboard.sh) bootstraps an OWNER, enrolls MFA and exports
// E2E_DASHBOARD_USER / _PASSWORD / _TOTP_SECRET; it can also provision an
// ADMIN (E2E_ADMIN_USER / _PASSWORD / _TOTP_SECRET). Never production values.
export interface Account {
	user: string;
	password: string;
	secret: string;
}

function required(name: string): string {
	const v = process.env[name];
	if (!v) throw new Error(`${name} must be set (no default dashboard account exists; see scripts/e2e-dashboard.sh)`);
	return v;
}

export function owner(): Account {
	return { user: required('E2E_DASHBOARD_USER'), password: required('E2E_DASHBOARD_PASSWORD'), secret: required('E2E_DASHBOARD_TOTP_SECRET') };
}

export function hasAdmin(): boolean {
	return Boolean(process.env.E2E_ADMIN_USER && process.env.E2E_ADMIN_PASSWORD && process.env.E2E_ADMIN_TOTP_SECRET);
}

export function admin(): Account {
	return { user: required('E2E_ADMIN_USER'), password: required('E2E_ADMIN_PASSWORD'), secret: required('E2E_ADMIN_TOTP_SECRET') };
}

function base32(input: string): Buffer {
	const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';
	let bits = '';
	for (const c of input.replace(/[\s=]/g, '').toUpperCase()) bits += alphabet.indexOf(c).toString(2).padStart(5, '0');
	const out: number[] = [];
	for (let i = 0; i + 8 <= bits.length; i += 8) out.push(parseInt(bits.slice(i, i + 8), 2));
	return Buffer.from(out);
}

const STEP_MS = 30_000;

// RFC 6238 TOTP (SHA-1, 30 s, 6 digits) for an explicit time step.
export function totpAt(secret: string, step: number): string {
	const counter = Buffer.alloc(8);
	counter.writeBigUInt64BE(BigInt(step));
	const mac = createHmac('sha1', base32(secret)).update(counter).digest();
	const o = mac[mac.length - 1] & 0x0f;
	return String((mac.readUInt32BE(o) & 0x7fffffff) % 1_000_000).padStart(6, '0');
}

// The server accepts each TOTP step once per account (replay protection,
// ADR-0053). That is correct and must not be weakened, so the harness
// never reuses a step: the last consumed step per secret is recorded on
// disk (shared by every Playwright worker and by the runner, which records
// the enrollment step), and a login waits only until the next unused step.
function stateDir(): string {
	return process.env.E2E_TOTP_STATE_DIR ?? join(tmpdir(), 'moonlight-e2e-totp');
}

export function totpStateFile(secret: string): string {
	return join(stateDir(), createHash('sha256').update(secret).digest('hex').slice(0, 16));
}

function withLock<T>(file: string, fn: () => Promise<T>): Promise<T> {
	const lock = `${file}.lock`;
	const acquire = async (): Promise<void> => {
		for (;;) {
			try {
				closeSync(openSync(lock, 'wx'));
				return;
			} catch {
				// Stale lock from a crashed worker: steal after 2 steps.
				try {
					if (Date.now() - statSync(lock).mtimeMs > 2 * STEP_MS) rmSync(lock, { force: true });
				} catch {
					/* raced with the owner releasing it */
				}
				await new Promise((r) => setTimeout(r, 100));
			}
		}
	};
	return acquire().then(() => fn().finally(() => rmSync(lock, { force: true })));
}

// freshTotp returns a code from a time step not yet consumed by this
// harness for this secret, waiting (at most one step) when necessary.
export async function freshTotp(secret: string): Promise<string> {
	mkdirSync(stateDir(), { recursive: true });
	const file = totpStateFile(secret);
	return withLock(file, async () => {
		let last = -1;
		try {
			last = Number(readFileSync(file, 'utf8').trim());
		} catch {
			/* first use */
		}
		let step = Math.floor(Date.now() / STEP_MS);
		if (step <= last) {
			const wait = (last + 1) * STEP_MS - Date.now() + 250;
			await new Promise((r) => setTimeout(r, Math.max(wait, 0)));
			step = Math.floor(Date.now() / STEP_MS);
		}
		writeFileSync(file, String(step));
		return totpAt(secret, step);
	});
}

// uiLogin signs in through the real login + MFA screens.
export async function uiLogin(page: Page, url = 'login', account: Account = owner()) {
	await page.goto(url);
	await page.getByLabel(/اسم الدخول|Email or login/).fill(account.user);
	await page.getByLabel(/كلمة المرور|Password/).fill(account.password);
	await page.getByRole('button', { name: /دخول/ }).click();
	const code = page.getByLabel(/Authenticator code/);
	await expect(code).toBeVisible({ timeout: 15000 });
	await code.fill(await freshTotp(account.secret));
	await page.getByRole('button', { name: /Verify/ }).click();
	await expect(page.getByText('لوحة متابعة المبيعات')).toBeVisible({ timeout: 15000 });
}

// uiLogout signs out through the shell's Logout button (server-side
// revocation) and waits for the login screen.
export async function uiLogout(page: Page) {
	await page.getByRole('button', { name: /خروج \/ Logout/ }).click();
	await expect(page.getByRole('heading', { name: 'تسجيل الدخول' })).toBeVisible();
}

function origin(): string {
	return new URL(process.env.E2E_BASE_URL ?? 'http://127.0.0.1:8080/dashboard/').origin;
}

// apiLogin establishes a FULL session through the API (cookie in the
// browser context; over HTTPS this is the production __Host- Secure cookie).
export async function apiLogin(page: Page, account: Account = owner()) {
	const login = await page.request.post('/api/v1/dashboard/auth/login', {
		data: { login: account.user, password: account.password },
		headers: { Origin: origin() }
	});
	expect(login.status()).toBe(200);
	const { csrf_token } = (await login.json()) as { csrf_token: string };
	const verify = await page.request.post('/api/v1/dashboard/auth/mfa/verify', {
		data: { code: await freshTotp(account.secret) },
		headers: { Origin: origin(), 'X-CSRF-Token': csrf_token }
	});
	expect(verify.status()).toBe(200);
}
