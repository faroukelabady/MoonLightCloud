<script lang="ts">
	import '../reports.css';
	import type { PeriodParams } from '../lib/http.js';
	import type { StoreRow } from '../lib/api.js';
	import { t } from '../lib/i18n.svelte.js';
	import SalesReportView from './SalesReportView.svelte';
	import StocktakingView from './StocktakingView.svelte';

	// Reports mirrors MoonLightRetail's Reports screen: a printable sales
	// report and the stocktaking & zakat sheet, over the Stores in scope.
	let { params, store, stores }: { params: PeriodParams; store: string; stores: StoreRow[] } = $props();

	let view = $state<'sales' | 'stocktaking'>('sales');
	// The letterhead names a single Store: the selected one, or the only one.
	let storeName = $derived((store ? stores.find((s) => s.store_id === store) : stores.length === 1 ? stores[0] : undefined)?.display_name ?? '');
	let scope = $derived(store || (stores.length === 1 ? stores[0].store_id : ''));
</script>

<section class="reports-page">
	<header class="card reports-head screen-only">
		<div>
			<h2>{t('التقارير', 'Reports')}</h2>
			<p class="sub">
				{view === 'sales'
					? t('أداء المبيعات من العمليات المتزامنة من MoonLightRetail', 'Sales performance from transactions synced from MoonLightRetail')
					: t('المخزون الحالي وحساب الزكاة', 'Current stock and the zakat calculation')}
			</p>
		</div>
		<button type="button" class="btn-primary" onclick={() => window.print()}>
			{view === 'sales' ? t('طباعة التقرير', 'Print report') : t('طباعة كشف الجرد', 'Print count sheet')}
		</button>
	</header>
	<div class="report-tabs screen-only" role="tablist" aria-label={t('نوع التقرير', 'Report type')}>
		<button type="button" role="tab" aria-selected={view === 'sales'} class:active={view === 'sales'} onclick={() => (view = 'sales')}>{t('المبيعات', 'Sales')}</button>
		<button type="button" role="tab" aria-selected={view === 'stocktaking'} class:active={view === 'stocktaking'} onclick={() => (view = 'stocktaking')}>{t('الجرد والزكاة', 'Stocktaking & zakat')}</button>
	</div>
	{#if view === 'sales'}
		<SalesReportView {params} store={scope} {storeName} />
	{:else}
		<StocktakingView store={scope} {storeName} />
	{/if}
</section>
