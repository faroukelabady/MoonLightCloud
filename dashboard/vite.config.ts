import { svelte } from '@sveltejs/vite-plugin-svelte';
import { defineConfig } from 'vite';

// Production is served by the Go server under /dashboard (see
// internal/adapter/http/dashboard_assets.go). assetsInlineLimit 0 keeps
// every asset a separate file so the strict Content-Security-Policy
// (no inline scripts/styles) holds. No CDN, no analytics.
export default defineConfig({
	plugins: [svelte()],
	base: '/dashboard/',
	build: {
		outDir: 'dist',
		emptyOutDir: true,
		assetsInlineLimit: 0,
		sourcemap: false
	},
	server: {
		port: 5173,
		proxy: {
			'/api': 'http://127.0.0.1:8080',
			'/health': 'http://127.0.0.1:8080'
		}
	},
	test: {
		environment: 'jsdom',
		exclude: ['e2e/**', 'node_modules/**']
	},
	resolve: process.env.VITEST
		? {
				// @testing-library/svelte mounts client components: force the
				// browser build of svelte under vitest (node resolution would
				// pick the server build and fail with lifecycle errors).
				conditions: ['browser']
			}
		: {}
});
