<script lang="ts">
	import { ApiError } from '../lib/http.js';
	import { reportsApi, type StockReport, type StockRow } from '../lib/reportsApi.js';
	import { formatInt, formatMinor } from '../lib/money.js';
	import { absoluteTime } from '../lib/time.js';
	import { lang, msg, t } from '../lib/i18n.svelte.js';
	import { NISAB_GOLD_GRAMS, calculateZakat, parseMajor, type ZakatYear } from '../lib/zakat.js';
	import ReportLetterhead from './ReportLetterhead.svelte';
	import SingleSelect from './ui/SingleSelect.svelte';
	import MoneyField from './ui/MoneyField.svelte';

	// Stock on hand as last synced from MoonLightRetail, valued in EGP, with
	// the zakat calculator. Zakat inputs stay on this page and are never sent.
	let { store, storeName }: { store: string; storeName: string } = $props();

	let report = $state<StockReport | null>(null);
	let loading = $state(true);
	let error = $state('');
	let refresh = $state(0);

	let basis = $state<'retail' | 'cost'>('retail');
	let year = $state<ZakatYear>('hijri');
	let goldGram = $state('');
	let cash = $state('');
	let receivables = $state('');
	let debts = $state('');

	let generation = 0;
	$effect(() => {
		const scope = store;
		void refresh;
		const current = ++generation;
		const controller = new AbortController();
		loading = true;
		error = '';
		reportsApi
			.stock(scope, controller.signal)
			.then((value) => {
				if (current === generation) report = value;
			})
			.catch((err) => {
				if (current !== generation || (err instanceof DOMException && err.name === 'AbortError')) return;
				error =
					err instanceof ApiError && err.detail === 'STORE_SCOPE_REQUIRED'
						? msg('stocktakingView.chooseAStoreAtTheTop')
						: err instanceof ApiError && err.code === 'FORBIDDEN'
						? msg('stocktakingView.youDoNotHavePermissionTo')
						: msg('stocktakingView.theStocktakingCouldNotBeLoaded');
			})
			.finally(() => {
				if (current === generation) loading = false;
			});
		return () => controller.abort();
	});

	let amounts = $derived({ goldGram: parseMajor(goldGram), cash: parseMajor(cash), receivables: parseMajor(receivables), debts: parseMajor(debts) });
	let invalidInput = $derived(Object.values(amounts).some((v) => v === undefined));
	let inventory = $derived(report ? BigInt(basis === 'retail' ? report.totals.retail_minor : report.totals.cost_minor) : 0n);
	let zakat = $derived(
		report && !invalidInput
			? calculateZakat({
					inventory,
					cash: amounts.cash ?? 0n,
					receivables: amounts.receivables ?? 0n,
					debts: amounts.debts ?? 0n,
					goldGram: amounts.goldGram ?? null,
					year
				})
			: null
	);
	let margin = $derived(report ? (BigInt(report.totals.retail_minor) - BigInt(report.totals.cost_minor)).toString() : '0');

	function egp(minor: string | bigint | null | undefined) {
		return minor == null ? '—' : formatMinor(minor.toString(), 'EGP');
	}
	function pick(ar: string, en: string) {
		return (lang() === 'ar' ? ar || en : en || ar).trim();
	}
	function itemName(item: StockRow) {
		const variant = pick(item.variant_name_ar, item.variant_name_en);
		const product = pick(item.product_name_ar, item.product_name_en);
		return variant ? `${product} — ${variant}` : product;
	}
</script>

<div class="stocktaking-toolbar screen-only">
	<p class="muted">{msg('stocktakingView.stockOnHandAsLastSynced')}</p>
	<button type="button" class="btn-secondary" disabled={loading} onclick={() => (refresh += 1)}>{msg('stocktakingView.refresh')}</button>
</div>
{#if error}<div class="report-alert screen-only" role="alert">{error}</div>{/if}
{#if loading && !report}<div class="card muted" role="status">{msg('stocktakingView.loadingStocktaking')}</div>
{:else if report && !error}
	<article id="report-printable" class="report-surface stocktaking" class:stale={loading} aria-label={msg('stocktakingView.stocktakingZakat')}>
		<ReportLetterhead title={msg('stocktakingView.stocktakingZakat')} shop={report.shop} {storeName}>
			<div><dt>{msg('stocktakingView.productsInStock')}</dt><dd><bdi class="num">{formatInt(report.totals.products_in_stock)}</bdi></dd></div>
			<div><dt>{msg('stocktakingView.skusInStock')}</dt><dd><bdi class="num">{formatInt(report.totals.variants_in_stock)}</bdi></dd></div>
			<div><dt>{msg('stocktakingView.unitsOnHand')}</dt><dd><bdi class="num">{formatInt(report.totals.units)}</bdi></dd></div>
			<div><dt>{msg('stocktakingView.generated')}</dt><dd>{absoluteTime(report.generated_at, lang())}</dd></div>
		</ReportLetterhead>

		<section class="report-money-grid" aria-label={msg('stocktakingView.inventoryValue')}>
			<div class="card report-money-detail">
				<h3>{msg('stocktakingView.inventoryValue')}</h3>
				<table>
					<thead><tr><th>{msg('stocktakingView.step')}</th><th>EGP</th></tr></thead>
					<tbody>
						<tr><th scope="row">{msg('stocktakingView.valueAtCost')}</th><td><bdi class="num">{egp(report.totals.cost_minor)}</bdi></td></tr>
						<tr><th scope="row">{msg('stocktakingView.valueAtSellingPrice')}</th><td><bdi class="num">{egp(report.totals.retail_minor)}</bdi></td></tr>
						<tr class="report-total-row"><th scope="row">{msg('stocktakingView.expectedMarginIfAllSold')}</th><td><bdi class="num">{egp(margin)}</bdi></td></tr>
					</tbody>
				</table>
				<p class="report-note">{msg('stocktakingView.eachSkuUsesItsOwnCost')}</p>
			</div>
			<div class="card report-money-detail">
				<h3>{msg('stocktakingView.dataToCheck')}</h3>
				<table>
					<thead><tr><th>{msg('stocktakingView.step')}</th><th>{msg('stocktakingView.units')}</th></tr></thead>
					<tbody>
						<tr><th scope="row">{msg('stocktakingView.unitsWithoutACost')}</th><td><bdi class="num">{formatInt(report.totals.units_without_cost)}</bdi></td></tr>
						<tr><th scope="row">{msg('stocktakingView.unitsWithoutAnEgpPrice')}</th><td><bdi class="num">{formatInt(report.totals.units_without_price)}</bdi></td></tr>
						<tr><th scope="row">{msg('stocktakingView.unitsOfInactiveProductsOrSkus')}</th><td><bdi class="num">{formatInt(report.totals.inactive_units)}</bdi></td></tr>
					</tbody>
				</table>
				{#if report.totals.units_without_cost > 0 || report.totals.units_without_price > 0}
					<p class="report-warning">{msg('stocktakingView.someStockHasNoCostOr')}</p>
				{/if}
			</div>
		</section>

		<section class="card report-section" aria-labelledby="zakat-heading">
			<h2 id="zakat-heading">{msg('stocktakingView.zakatOnTradeGoods')}</h2>
			<p>{msg('stocktakingView.zakatOnShopStockIsDue')}</p>
			<div class="report-filter-grid screen-only">
				<SingleSelect
					id="zakat-basis"
					label={msg('stocktakingView.valueStockAt')}
					testid="zakat-basis"
					value={basis}
					options={[
						{ value: 'retail', label: msg('stocktakingView.sellingPriceMarketValue') },
						{ value: 'cost', label: msg('stocktakingView.costPrice') }
					]}
					onchange={(v) => (basis = v as typeof basis)}
				/>
				<SingleSelect
					id="zakat-year"
					label={msg('stocktakingView.zakatYear')}
					value={year}
					options={[
						{ value: 'hijri', label: msg('stocktakingView.hijriYear25') },
						{ value: 'gregorian', label: msg('stocktakingView.gregorianYear2577') }
					]}
					onchange={(v) => (year = v as ZakatYear)}
				/>
				<MoneyField id="zakat-gold" label={msg('stocktakingView.goldPricePerGram')} testid="zakat-gold" invalid={amounts.goldGram === undefined} bind:value={goldGram} />
				<MoneyField id="zakat-cash" label={msg('stocktakingView.cashAndBankBalances')} invalid={amounts.cash === undefined} bind:value={cash} />
				<MoneyField id="zakat-receivables" label={msg('stocktakingView.receivablesYouExpectToCollect')} invalid={amounts.receivables === undefined} bind:value={receivables} />
				<MoneyField id="zakat-debts" label={msg('stocktakingView.debtsDue')} invalid={amounts.debts === undefined} bind:value={debts} />
			</div>
			<p class="report-note screen-only">{msg('stocktakingView.leaveAFieldEmptyForZero')}</p>
			{#if invalidInput}<p class="field-error" role="alert">{msg('stocktakingView.enterPositiveAmountsWithAtMost')}</p>{/if}
			{#if zakat}
				<div class="table-scroll">
					<table aria-label={msg('stocktakingView.zakatOnTradeGoods')}>
						<thead><tr><th>{msg('stocktakingView.step')}</th><th>EGP</th></tr></thead>
						<tbody>
							<tr><th scope="row">{basis === 'retail' ? msg('stocktakingView.stockAtSellingPrice') : msg('stocktakingView.stockAtCost')}</th><td><bdi class="num">{egp(inventory)}</bdi></td></tr>
							<tr><th scope="row">{msg('stocktakingView.cashAndBankBalances')}</th><td><bdi class="num">{egp(amounts.cash ?? 0n)}</bdi></td></tr>
							<tr><th scope="row">{msg('stocktakingView.receivablesYouExpectToCollect')}</th><td><bdi class="num">{egp(amounts.receivables ?? 0n)}</bdi></td></tr>
							<tr><th scope="row">{msg('stocktakingView.debtsDue')}</th><td><bdi class="num">− {egp(amounts.debts ?? 0n)}</bdi></td></tr>
							<tr class="report-total-row"><th scope="row">{msg('stocktakingView.zakatableTotal')}</th><td><bdi class="num">{egp(zakat.base)}</bdi></td></tr>
							<tr><th scope="row">{t(`النصاب (${NISAB_GOLD_GRAMS} جرام ذهب)`, `Nisab (${NISAB_GOLD_GRAMS} g of gold)`)}</th><td><bdi class="num">{zakat.nisab == null ? msg('stocktakingView.enterTheGoldPrice') : egp(zakat.nisab)}</bdi></td></tr>
							<tr class="report-total-row"><th scope="row">{year === 'hijri' ? msg('stocktakingView.zakatDue25') : msg('stocktakingView.zakatDue2577')}</th><td><bdi class="num" data-testid="zakat-due">{egp(zakat.zakat)}</bdi></td></tr>
						</tbody>
					</table>
				</div>
				<p class="report-warning" role="status">
					{#if zakat.reachesNisab === null}{msg('stocktakingView.enterTheGoldPricePerGram')}
					{:else if zakat.reachesNisab}{msg('stocktakingView.theZakatableTotalReachesNisabSo')}
					{:else}{msg('stocktakingView.theZakatableTotalIsBelowNisab')}{/if}
				</p>
			{/if}
			<ul class="stocktaking-notes">
				<li>{msg('stocktakingView.zakatIsDueWhenAFull')}</li>
				<li>{msg('stocktakingView.mostScholarsValueTradeGoodsAt')}</li>
				<li>{msg('stocktakingView.shopFixturesEquipmentAndPremisesAre')}</li>
				<li>{msg('stocktakingView.thisIsAnEstimateToHelp')}</li>
			</ul>
		</section>

		<section class="card report-section" aria-labelledby="stock-categories-heading">
			<h2 id="stock-categories-heading">{msg('stocktakingView.stockByCategory')}</h2>
			<div class="table-scroll">
				<table>
					<thead><tr><th>{msg('stocktakingView.category')}</th><th>{msg('stocktakingView.productsInStock')}</th><th>{msg('stocktakingView.unitsOnHand')}</th><th>{msg('stocktakingView.valueAtCost')}</th><th>{msg('stocktakingView.valueAtSellingPrice')}</th></tr></thead>
					<tbody>
						{#each report.categories as row (row.id)}
							<tr><th scope="row">{pick(row.name_ar, row.name_en)}</th><td><bdi class="num">{formatInt(row.products_in_stock)}</bdi></td><td><bdi class="num">{formatInt(row.units)}</bdi></td><td><bdi class="num">{egp(row.cost_minor)}</bdi></td><td><bdi class="num">{egp(row.retail_minor)}</bdi></td></tr>
						{/each}
						<tr class="report-total-row"><th scope="row">{msg('stocktakingView.total')}</th><td><bdi class="num">{formatInt(report.totals.products_in_stock)}</bdi></td><td><bdi class="num">{formatInt(report.totals.units)}</bdi></td><td><bdi class="num">{egp(report.totals.cost_minor)}</bdi></td><td><bdi class="num">{egp(report.totals.retail_minor)}</bdi></td></tr>
					</tbody>
				</table>
			</div>
		</section>

		<section class="card report-section stocktaking-sheet" aria-labelledby="stock-sheet-heading">
			<h2 id="stock-sheet-heading">{msg('stocktakingView.countSheet')}</h2>
			<p>{msg('stocktakingView.printThisSheetCountEachShelf')}</p>
			<div class="table-scroll">
				<table>
					<thead>
						<tr>
							<th>SKU</th><th>{msg('stocktakingView.item')}</th><th>{msg('stocktakingView.systemQuantity')}</th><th>{msg('stocktakingView.unitCost')}</th>
							<th>{msg('stocktakingView.valueAtCost')}</th><th>{msg('stocktakingView.unitPrice')}</th><th>{msg('stocktakingView.valueAtSellingPrice')}</th><th>{msg('stocktakingView.counted')}</th>
						</tr>
					</thead>
					<tbody>
						{#each report.items as item (item.variant_id)}
							<tr class:stocktaking-inactive={!item.active}>
								<td class="mono" dir="ltr">{item.sku}</td>
								<th scope="row">{itemName(item)}{#if !item.active}<small>{msg('stocktakingView.inactive')}</small>{/if}</th>
								<td><bdi class="num">{formatInt(item.quantity)}</bdi></td>
								<td><bdi class="num">{egp(item.unit_cost_minor)}</bdi></td>
								<td><bdi class="num">{egp(item.cost_minor)}</bdi></td>
								<td><bdi class="num">{egp(item.unit_price_minor)}</bdi></td>
								<td><bdi class="num">{egp(item.retail_minor)}</bdi></td>
								<td class="stocktaking-count-cell"></td>
							</tr>
						{:else}
							<tr><td colspan="8" class="muted">{msg('stocktakingView.noSyncedStockForThisScope')}</td></tr>
						{/each}
					</tbody>
				</table>
			</div>
		</section>
	</article>
{/if}
