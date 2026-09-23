import { defineConfig } from '@playwright/test';

// E2E runs against the local dev stack (./scripts/dev-up.sh):
// Go API on :8080 serving the built dashboard. Base URL is overridable.
export default defineConfig({
	testDir: './e2e',
	use: {
		baseURL: process.env.E2E_BASE_URL ?? 'http://127.0.0.1:8080/dashboard/'
	},
	retries: 0,
	workers: 1
});
