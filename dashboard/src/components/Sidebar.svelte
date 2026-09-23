<script lang="ts">
	let { route, navigate }: { route: string; navigate: (r: string) => void } = $props();

	const entries = [
		{ r: 'overview', ar: 'نظرة عامة', en: 'Overview', ready: true },
		{ r: 'sales', ar: 'ملخص المبيعات', en: 'Sales Summary', ready: true },
		{ r: 'daily', ar: 'الاتجاهات اليومية', en: 'Daily Trends', ready: true },
		{ r: 'products', ar: 'المنتجات', en: 'Products', ready: true },
		{ r: 'categories', ar: 'الفئات', en: 'Categories', ready: true },
		{ r: 'orders', ar: 'الطلبات', en: 'Orders', ready: false, note: 'قريبًا — Orders sync is a future phase' },
		{ r: 'reports', ar: 'التقارير', en: 'Reports', ready: false, note: 'قريبًا' },
		{ r: 'sync', ar: 'حالة المزامنة', en: 'Sync Health', ready: true },
		{ r: 'settings', ar: 'الإعدادات', en: 'Settings', ready: false, note: 'قريبًا' },
		{ r: 'help', ar: 'مركز المساعدة', en: 'Help Center', ready: false, note: 'قريبًا' }
	];
</script>

<nav class="sidebar" aria-label="التنقل الرئيسي / Main navigation">
	<div class="brand">
		<div class="brand-ar">مون لايت</div>
		<div class="brand-en">MoonLight Reports</div>
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
						<span class="t">{e.ar}</span>
						<span class="s">{e.en}</span>
					</button>
				{:else}
					<span class="future" title={e.note} aria-disabled="true">
						<span class="t">{e.ar}</span>
						<span class="s">{e.en} · {e.note ?? ''}</span>
					</span>
				{/if}
			</li>
		{/each}
	</ul>
</nav>

<style>
	.sidebar {
		background: var(--color-sidebar);
		color: #e5e9f2;
		min-height: 100vh;
		position: sticky;
		top: 0;
		height: 100vh;
		overflow-y: auto;
		padding: 16px 12px;
		border-radius: 0;
	}
	.brand {
		padding: 8px 8px 16px;
		border-bottom: 1px solid rgba(255, 255, 255, 0.12);
		margin-bottom: 12px;
	}
	.brand-ar {
		font-weight: 700;
		font-size: 1.1rem;
	}
	.brand-en {
		font-size: 0.75rem;
		color: #9aa5bd;
	}
	ul {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 2px;
	}
	button,
	.future {
		display: flex;
		flex-direction: column;
		width: 100%;
		text-align: start;
		background: transparent;
		border: 0;
		border-radius: 6px;
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
	.t {
		font-size: 0.95rem;
	}
	.s {
		font-size: 0.72rem;
		color: #9aa5bd;
	}
	.future {
		opacity: 0.45;
		cursor: not-allowed;
	}
	@media (max-width: 900px) {
		.sidebar {
			min-height: auto;
			height: auto;
			position: static;
		}
	}
</style>
