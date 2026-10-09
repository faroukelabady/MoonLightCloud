import { request, type APIRequestContext } from '@playwright/test';
import { freshTotp } from '../auth';

// Optional E2E account provisioning (E2E_PROVISION=1, used by
// scripts/e2e-dashboard.sh against a fresh throwaway Cloud). Everything goes
// through the real API: the bootstrapped OWNER enrolls MFA, and, when
// E2E_ADMIN_USER/_PASSWORD are given, the OWNER creates a Store-restricted
// ADMIN who activates and enrolls. The resulting TOTP secrets are handed to
// the test workers via process.env only (never written to disk).
// Without E2E_PROVISION the suite uses pre-provisioned accounts as-is.

function originOf(base: string): string {
	return new URL(base).origin;
}

async function enroll(ctx: APIRequestContext, origin: string, csrf: string): Promise<string> {
	const start = await ctx.post('/api/v1/dashboard/auth/mfa/enroll/start', { data: {}, headers: { Origin: origin, 'X-CSRF-Token': csrf } });
	if (start.status() !== 200) throw new Error(`mfa enroll start: ${start.status()}`);
	const { secret } = (await start.json()) as { secret: string };
	const confirm = await ctx.post('/api/v1/dashboard/auth/mfa/enroll/confirm', {
		data: { code: await freshTotp(secret) },
		headers: { Origin: origin, 'X-CSRF-Token': csrf }
	});
	const body = (await confirm.json()) as { stage?: string };
	if (confirm.status() !== 200 || body.stage !== 'FULL') throw new Error(`mfa enroll confirm: ${confirm.status()} ${body.stage}`);
	return secret;
}

export default async function globalSetup(): Promise<void> {
	if (process.env.E2E_PROVISION !== '1') return;
	const base = process.env.E2E_BASE_URL;
	if (!base) throw new Error('E2E_PROVISION=1 requires E2E_BASE_URL');
	const origin = originOf(base);

	const ownerCtx = await request.newContext({ baseURL: origin });
	try {
		const login = await ownerCtx.post('/api/v1/dashboard/auth/login', {
			data: { login: process.env.E2E_DASHBOARD_USER, password: process.env.E2E_DASHBOARD_PASSWORD },
			headers: { Origin: origin }
		});
		const lb = (await login.json()) as { stage?: string; csrf_token: string };
		if (login.status() !== 200 || lb.stage !== 'MFA_SETUP') throw new Error(`owner first login: ${login.status()} ${lb.stage} (expected a fresh, unenrolled OWNER)`);
		const ownerSecret = await enroll(ownerCtx, origin, lb.csrf_token);
		process.env.E2E_DASHBOARD_TOTP_SECRET = ownerSecret;

		const adminUser = process.env.E2E_ADMIN_USER;
		const adminPassword = process.env.E2E_ADMIN_PASSWORD;
		if (!adminUser || !adminPassword) return;
		const store = process.env.E2E_ADMIN_STORE;
		if (!store) throw new Error('E2E_ADMIN_USER requires E2E_ADMIN_STORE (the single Store the ADMIN may access)');
		// The enrollment rotated the OWNER session; re-read the CSRF token.
		const me = (await (await ownerCtx.get('/api/v1/dashboard/auth/me')).json()) as { csrf_token: string };
		const created = await ownerCtx.post('/api/v1/dashboard/users', {
			data: { login: adminUser, display_name: 'E2E Admin', role: 'ADMIN', all_stores: false, store_ids: [store] },
			headers: { Origin: origin, 'X-CSRF-Token': me.csrf_token }
		});
		if (created.status() !== 201 && created.status() !== 200) throw new Error(`create ADMIN: ${created.status()} ${await created.text()}`);
		const { activation_token } = (await created.json()) as { activation_token: string };

		const adminCtx = await request.newContext({ baseURL: origin });
		try {
			const act = await adminCtx.post('/api/v1/dashboard/auth/activate', {
				data: { token: activation_token, password: adminPassword },
				headers: { Origin: origin }
			});
			const ab = (await act.json()) as { csrf_token: string };
			if (act.status() !== 200) throw new Error(`activate ADMIN: ${act.status()}`);
			process.env.E2E_ADMIN_TOTP_SECRET = await enroll(adminCtx, origin, ab.csrf_token);
		} finally {
			await adminCtx.dispose();
		}
	} finally {
		await ownerCtx.dispose();
	}
}
