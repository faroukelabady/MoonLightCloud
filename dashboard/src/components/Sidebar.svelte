<script lang="ts">
	let { route, navigate }: { route: string; navigate: (r: string) => void } = $props();

	const entries = [
		{ r: 'overview', ar: 'نظرة عامة', en: 'Overview', ready: true, icon: 'home' },
		{ r: 'sales', ar: 'ملخص المبيعات', en: 'Sales Summary', ready: true, icon: 'chart' },
		{ r: 'daily', ar: 'الاتجاهات اليومية', en: 'Daily Trends', ready: true, icon: 'trend' },
		{ r: 'products', ar: 'المنتجات', en: 'Products', ready: true, icon: 'box' },
		{ r: 'categories', ar: 'الفئات', en: 'Categories', ready: true, icon: 'layers' },
		{ r: 'orders', ar: 'الطلبات', en: 'Orders', ready: false, note: 'قريبًا — Orders sync is a future phase', icon: 'clip' },
		{ r: 'reports', ar: 'التقارير', en: 'Reports', ready: false, note: 'قريبًا', icon: 'report' },
		{ r: 'sync', ar: 'حالة المزامنة', en: 'Sync Health', ready: true, icon: 'cloud' },
		{ r: 'settings', ar: 'الإعدادات', en: 'Settings', ready: false, note: 'قريبًا', icon: 'gear' },
		{ r: 'help', ar: 'مركز المساعدة', en: 'Help Center', ready: false, note: 'قريبًا', icon: 'help' }
	];

	const icons: Record<string, string> = {
		home: 'M3 11.5 12 4l9 7.5M5.5 10.5V20h13v-9.5',
		chart: 'M4 20V10M10 20V4M16 20v-8M21 20H3',
		trend: 'M3 17l6-6 4 4 8-8M15 7h6v6',
		box: 'M12 3l8 4.5v9L12 21l-8-4.5v-9L12 3zM12 12l8-4.5M12 12L4 7.5M12 12v9',
		layers: 'M12 3l9 5-9 5-9-5 9-5zM3 13l9 5 9-5',
		clip: 'M9 4h6v4H9zM7 6H5v15h14V6h-2M9 12h6M9 16h6',
		report: 'M6 3h9l4 4v14H6zM14 3v5h5M9 13h6M9 17h6',
		cloud: 'M7 18a4 4 0 1 1 .6-7.96A5.5 5.5 0 0 1 18.3 12H18a3 3 0 0 1 0 6H7z',
		gear: 'M12 8.5A3.5 3.5 0 1 0 12 15.5 3.5 3.5 0 0 0 12 8.5zM19 12a7 7 0 0 0-.1-1.2l2-1.5-2-3.4-2.3 1a7 7 0 0 0-2-1.2L14.2 3H9.8l-.4 2.7a7 7 0 0 0-2 1.2l-2.3-1-2 3.4 2 1.5a7 7 0 0 0 0 2.4l-2 1.5 2 3.4 2.3-1a7 7 0 0 0 2 1.2l.4 2.7h4.4l.4-2.7a7 7 0 0 0 2-1.2l2.3 1 2-3.4-2-1.5c.06-.4.1-.8.1-1.2z',
		help: 'M12 3a9 9 0 1 0 9 9M9.5 9.5A2.5 2.5 0 1 1 12 12.5V14M12 17.5v.5'
	};
</script>

<nav class="sidebar" aria-label="التنقل الرئيسي / Main navigation">
	<div class="brand">
		<div class="brand-mark" aria-hidden="true">
			<svg viewBox="0 0 24 24" width="22" height="22" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M20 13.5A8 8 0 0 1 10.5 4 6.5 6.5 0 1 0 20 13.5z" /><path d="M17 4l.6 1.7L19.3 6.3l-1.7.6L17 8.6l-.6-1.7-1.7-.6 1.7-.6L17 4z" /></svg>
		</div>
		<div>
			<div class="brand-ar">MoonLightCloud</div>
			<div class="brand-en">مون لايت كلاود</div>
		</div>
	</div>
	<ul>
		{#each entries as e}
			<li>
				{#if e.ready}
					<button
						type="button"
						class:active={route === e.r}
						aria-current={route === e.r ? 'page' : undefined}
						onclick={() => navigate(e.r)}
					>
						<svg class="ic" viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d={icons[e.icon]} /></svg>
						<span class="tx">
							<span class="t">{e.ar}</span>
							<span class="s">{e.en}</span>
						</span>
					</button>
				{:else}
					<span class="future" title={e.note} aria-disabled="true">
						<svg class="ic" viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d={icons[e.icon]} /></svg>
						<span class="tx">
							<span class="t">{e.ar}</span>
							<span class="s">{e.en} · {e.note ?? ''}</span>
						</span>
					</span>
				{/if}
			</li>
		{/each}
	</ul>
	<div class="foot">
		<div class="foot-ar">MoonLightCloud</div>
		<div class="foot-en">مون لايت كلاود</div>
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
	button,
	.future {
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
	button.active .s {
		color: rgba(255, 255, 255, 0.82);
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
	.s {
		font-size: 0.7rem;
		color: #9aa5bd;
		line-height: 1.3;
	}
	.future {
		opacity: 0.45;
		cursor: not-allowed;
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
