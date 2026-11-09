<script lang="ts">
	import { arabic, msg, t } from '../lib/i18n.svelte.js';
	// Navigation mirrors the server-side permission map (ADR-0053). Hiding an
	// entry is UX only: every API re-checks permission and Store scope.
	let {
		route,
		navigate,
		permissions = [],
		allStores = false
	}: { route: string; navigate: (r: string) => void; permissions?: string[]; allStores?: boolean } = $props();

	// Business views first, then administration. Overview carries the sales
	// summary, daily trend, products, categories, tags and the online
	// comparison, so those have no entries of their own.
	const entries: { r: string; ar: string; en: string; icon: string; perm?: string; all?: boolean }[] = [
		{ r: 'overview', ar: 'نظرة عامة', en: 'Overview', icon: 'home' },
		{ r: 'reports', ar: 'التقارير', en: 'Reports', icon: 'print' },
		{ r: 'orders', ar: 'الطلبات عبر الإنترنت', en: 'Online Orders', icon: 'clip' },
		{ r: 'sync', ar: 'حالة المزامنة', en: 'Sync Health', icon: 'sync' },
		{ r: 'admin', ar: 'إدارة الكتالوج', en: 'Catalog Admin', icon: 'box', perm: 'catalog.read' },
		{ r: 'devices', ar: 'المتاجر', en: 'Stores', icon: 'store', perm: 'devices.read' },
		{ r: 'updates', ar: 'التحديثات', en: 'Updates', icon: 'download', perm: 'releases.read' },
		{ r: 'operations', ar: 'العمليات', en: 'Operations', icon: 'pulse', perm: 'operations.read', all: true },
		{ r: 'users', ar: 'المستخدمون والأمان', en: 'Users & Security', icon: 'users', perm: 'users.read' },
		{ r: 'settings', ar: 'الإعدادات', en: 'Settings', icon: 'gear' }
	];

	const icons: Record<string, string> = {
		home: 'M3 11.5 12 4l9 7.5M5.5 10.5V20h13v-9.5',
		print: 'M7 9V3h10v6M7 18H4v-8h16v8h-3M7 14h10v7H7z',
		clip: 'M9 4h6v4H9zM7 6H5v15h14V6h-2M9 12h6M9 16h6',
		sync: 'M20 11a8 8 0 0 0-14.6-4.5L4 8M4 4v4h4M4 13a8 8 0 0 0 14.6 4.5L20 16M20 20v-4h-4',
		box: 'M12 3l8 4.5v9L12 21l-8-4.5v-9L12 3zM12 12l8-4.5M12 12L4 7.5M12 12v9',
		store: 'M3 10l2-6h14l2 6M4 10v10h16V10M3 10h18M9 20v-6h6v6',
		device: 'M4 5h16v11H4zM9 20h6M12 16v4',
		download: 'M12 4v11M7 10l5 5 5-5M5 20h14',
		pulse: 'M3 12h4l3-7 4 14 3-7h4',
		gear: 'M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z',
		users: 'M9 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8zM2 21a7 7 0 0 1 14 0M16 3.5a4 4 0 0 1 0 7.5M18 14a6 6 0 0 1 4 7'
	};
</script>

<nav class="sidebar" aria-label={msg('sidebar.mainNavigation')}>
	<div class="brand">
		<div class="brand-mark" aria-hidden="true">
			<svg viewBox="0 0 24 24" width="22" height="22" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M20 13.5A8 8 0 0 1 10.5 4 6.5 6.5 0 1 0 20 13.5z" /><path d="M17 4l.6 1.7L19.3 6.3l-1.7.6L17 8.6l-.6-1.7-1.7-.6 1.7-.6L17 4z" /></svg>
		</div>
		<div>
			<div class="brand-ar">MoonLightCloud</div>
			{#if arabic()}<div class="brand-en">مون لايت كلاود</div>{/if}
		</div>
	</div>
	<ul>
		{#each entries.filter((e) => (!e.perm || permissions.includes(e.perm)) && (!e.all || allStores)) as e}
			<li>
				<button
					type="button"
					class:active={route === e.r}
					aria-current={route === e.r ? 'page' : undefined}
					onclick={() => navigate(e.r)}
				>
					<svg class="ic" viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d={icons[e.icon]} /></svg>
					<span class="tx">
						<span class="t">{t(e.ar, e.en)}</span>
					</span>
				</button>
			</li>
		{/each}
	</ul>
	<div class="foot">
		<div class="foot-ar">MoonLightCloud</div>
		{#if arabic()}<div class="foot-en">مون لايت كلاود</div>{/if}
	</div>
</nav>

<style>
	.sidebar {
		background: var(--sidebar-bg);
		color: #e5e9f2;
		min-height: 100vh;
		position: sticky;
		top: 0;
		height: 100vh;
		overflow-y: auto;
		padding: 14px 10px 12px;
		border-radius: 0;
		display: flex;
		flex-direction: column;
	}
	.brand {
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 6px 8px 14px;
		border-bottom: 1px solid rgba(255, 255, 255, 0.12);
		margin-bottom: 10px;
	}
	.brand-mark {
		width: 34px;
		height: 34px;
		flex-shrink: 0;
		display: flex;
		align-items: center;
		justify-content: center;
		background: rgba(255, 255, 255, 0.1);
		border-radius: 8px;
		color: #ffd97a;
	}
	.brand-ar {
		font-weight: 700;
		font-size: 0.98rem;
		color: #fff;
	}
	.brand-en {
		font-size: 0.72rem;
		color: #9aa5bd;
	}
	ul {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 2px;
		flex: 1;
	}
	button {
		display: flex;
		align-items: center;
		gap: 10px;
		width: 100%;
		text-align: start;
		background: transparent;
		border: 0;
		border-radius: var(--radius-control);
		color: inherit;
		padding: 8px 10px;
	}
	button:hover {
		background: rgba(255, 255, 255, 0.08);
	}
	button.active {
		background: var(--color-sidebar-active);
		color: #fff;
	}
	.ic {
		flex-shrink: 0;
		opacity: 0.9;
	}
	.tx {
		display: flex;
		flex-direction: column;
		min-width: 0;
	}
	.t {
		font-size: 0.9rem;
		line-height: 1.3;
	}
	.foot {
		margin-top: 12px;
		padding: 12px 8px 2px;
		border-top: 1px solid rgba(255, 255, 255, 0.12);
		text-align: center;
	}
	.foot-ar {
		font-weight: 700;
		font-size: 0.85rem;
		color: #fff;
	}
	.foot-en {
		font-size: 0.7rem;
		color: #9aa5bd;
	}
	@media (max-width: 900px) {
		.sidebar {
			min-height: auto;
			height: auto;
			position: static;
		}
		.foot {
			display: none;
		}
	}
</style>
