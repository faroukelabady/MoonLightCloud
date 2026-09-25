import { defineConfig } from '@playwright/test';

// E2E runs against the local dev stack (./scripts/dev-up.sh):
// Go API on :8080 serving the built dashboard. Base URL is overridable.
export default defineConfig({
	testDir: './e2e',
	use: {
		baseURL: process.env.E2E_BASE_URL ?? 'http://127.0.0.1:8080/dashboard/',
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
