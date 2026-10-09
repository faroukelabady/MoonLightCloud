import { defineConfig } from '@playwright/test';

// E2E runs against a live Cloud serving the built dashboard. The canonical
// runner is scripts/e2e-dashboard.sh: a production-mode Cloud image behind a
// local HTTPS endpoint (so the real __Host-/Secure/HttpOnly/SameSite=Strict
// session cookie is exercised), with explicit OWNER/ADMIN accounts.
// Base URL is overridable.
//
// Local TLS trust is pinned, not disabled: the runner generates a per-run
// CA + leaf certificate; Chromium trusts exactly that leaf's SPKI
// (E2E_TLS_SPKI) and Node's request context trusts the run's CA through
// NODE_EXTRA_CA_CERTS. Other certificate errors still fail.
const spki = process.env.E2E_TLS_SPKI;

export default defineConfig({
	testDir: './e2e',
	// No-op unless E2E_PROVISION=1 (runner-provisioned throwaway accounts).
	globalSetup: './e2e/support/global-setup.ts',
	// One login may wait up to one 30 s TOTP step (single-use steps; see
	// e2e/auth.ts), and some tests sign in twice.
	timeout: 120_000,
	use: {
		baseURL: process.env.E2E_BASE_URL ?? 'http://127.0.0.1:8080/dashboard/',
		...(spki ? { launchOptions: { args: [`--ignore-certificate-errors-spki-list=${spki}`] } } : {}),
		// Four-viewport responsive matrix (§76): E2E_VIEWPORT=WxH, e.g.
		// E2E_VIEWPORT=1536x1024. Unset keeps the Playwright default.
		...(process.env.E2E_VIEWPORT ? viewportOf(process.env.E2E_VIEWPORT) : {})
	},
	retries: 0,
	workers: 1
});

function viewportOf(spec: string): { viewport: { width: number; height: number } } {
	const m = /^(\d+)x(\d+)$/.exec(spec.trim());
	if (!m) throw new Error(`bad E2E_VIEWPORT ${JSON.stringify(spec)}: want WxH`);
	return { viewport: { width: Number(m[1]), height: Number(m[2]) } };
}
