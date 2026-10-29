<script lang="ts">
	import '../reports.css';
	import type { PeriodParams } from '../lib/http.js';
	import type { StoreRow } from '../lib/api.js';
	import { msg } from '../lib/i18n.svelte.js';
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
			<h2>{msg('reportsPage.reports')}</h2>
			<p class="sub">
				{view === 'sales'
					? msg('reportsPage.salesPerformanceFromTransactionsSyncedFrom')
					: msg('reportsPage.currentStockAndTheZakatCalculation')}
			</p>
		</div>
		<button type="button" class="btn-primary" onclick={() => window.print()}>
			{view === 'sales' ? msg('reportsPage.printReport') : msg('reportsPage.printCountSheet')}
		</button>
	</header>
	<div class="report-tabs screen-only" role="tablist" aria-label={msg('reportsPage.reportType')}>
		<button type="button" class="tab" role="tab" aria-selected={view === 'sales'} class:active={view === 'sales'} onclick={() => (view = 'sales')}>{msg('reportsPage.sales')}</button>
		<button type="button" class="tab" role="tab" aria-selected={view === 'stocktaking'} class:active={view === 'stocktaking'} onclick={() => (view = 'stocktaking')}>{msg('reportsPage.stocktakingZakat')}</button>
	</div>
	{#if view === 'sales'}
		<SalesReportView {params} store={scope} {storeName} />
	{:else}
		<StocktakingView store={scope} {storeName} />
	{/if}
</section>
