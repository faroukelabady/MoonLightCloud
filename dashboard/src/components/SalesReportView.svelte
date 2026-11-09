<script lang="ts">
	import { ApiError, type PeriodParams } from '../lib/http.js';
	import { emptyFilter, reportsApi, type CurrencyMixRow, type FilterOption, type ReportDisplay, type ReportFilter, type ReportShop, type ReportSummaryRow, type SalesReport } from '../lib/reportsApi.js';
		import { formatInt, formatMicrorate, formatMinor } from '../lib/money.js';
	import { absoluteTime } from '../lib/time.js';
	import { lang, msg, t } from '../lib/i18n.svelte.js';
	import { parseMajor } from '../lib/zakat.js';
	import ReportSaleLines from './ReportSaleLines.svelte';
	import ReportLetterhead from './ReportLetterhead.svelte';
	import SingleSelect from './ui/SingleSelect.svelte';
	import MultiSelect from './ui/MultiSelect.svelte';
	import TextField from './ui/TextField.svelte';
	import MoneyField from './ui/MoneyField.svelte';

	// The sales report MoonLightRetail prints, read from synced Sales. The
	// period and Store come from the dashboard header; the currency and
	// extra costs belong to this report only and are never saved.
	let { params, store, storeName }: { params: PeriodParams; store: string; storeName: string } = $props();

	const PAGE = 50;
	let currency = $state<ReportDisplay>('EGP');
	let filter = $state<ReportFilter>(emptyFilter());
	let offset = $state(0);
	let report = $state<SalesReport | null>(null);
	let loading = $state(true);
	let error = $state('');
	let panel = $state<'overview' | 'classifications' | 'performance' | 'details'>('overview');

	type DraftCost = { id: number; description: string; amount: string };
	let drafts = $state<DraftCost[]>([]);
	let nextDraft = 1;
	let costs = $state<{ description: string; minor: bigint }[]>([]);
	let costsError = $state(false);

	let generation = 0;
	$effect(() => {
		// Reload when the header period or Store, the currency or the page changes.
		const request = { params: { ...params }, store, currency, filter: $state.snapshot(filter) as ReportFilter, offset };
		const current = ++generation;
		const controller = new AbortController();
		loading = true;
		error = '';
		reportsApi
			.sales(request.params, request.currency, request.filter, request.store, request.offset, PAGE, controller.signal)
			.then((value) => {
				if (current !== generation) return;
				report = value;
			})
			.catch((err) => {
				if (current !== generation || (err instanceof DOMException && err.name === 'AbortError')) return;
				error = failure(err);
			})
			.finally(() => {
				if (current === generation) loading = false;
			});
		return () => controller.abort();
	});

	function failure(err: unknown): string {
		if (err instanceof ApiError && err.detail === 'EXCHANGE_RATE_REQUIRED')
			return msg('salesReportView.thereIsNoUsdExchangeRate');
		if (err instanceof ApiError && err.detail === 'STORE_SCOPE_REQUIRED') return msg('salesReportView.chooseAStoreAtTheTop');
		if (err instanceof ApiError && err.code === 'FORBIDDEN') return msg('salesReportView.youDoNotHavePermissionTo');
		return msg('salesReportView.theReportCouldNotBeLoaded');
	}

	function setCurrency(value: string) {
		currency = value as ReportDisplay;
		offset = 0;
	}
	function setFilter(next: Partial<ReportFilter>) {
		filter = { ...filter, ...next };
		offset = 0;
	}
	function resetFilters() {
		filter = emptyFilter();
		currency = 'EGP';
		offset = 0;
	}
	// Choices come from the period's own sales, unfiltered.
	let filterOptions = $derived(report?.filter_options ?? { categories: [], subcategories: [], tags: [] });
	function optionsOf(list: FilterOption[]) {
		const labelOf = (o: FilterOption) => (lang() === 'ar' ? o.name_ar || o.name_en : o.name_en || o.name_ar);
		const counts = new Map<string, number>();
		for (const o of list) counts.set(labelOf(o), (counts.get(labelOf(o)) ?? 0) + 1);
		return list.map((o) => ({ value: o.id, label: counts.get(labelOf(o)) === 1 ? labelOf(o) : `${labelOf(o)} · ${o.id.slice(0, 8)}` }));
	}
	let filtered = $derived(filter.root_category_ids.length + filter.subcategory_ids.length + filter.tag_ids.length > 0 || filter.payment_method !== '');

	let summary = $derived(report?.summary ?? []);
	let any = (pick: (row: ReportSummaryRow) => boolean) => summary.some(pick);
	// The business toggles decide, as in MoonLightRetail; discounts and
	// refunds still show when the period holds them, so totals reconcile.
	// With tax off the server already left tax out of every total.
	let settings = $derived(report?.settings);
	let showTax = $derived(settings?.tax_enabled ?? false);
	let showDiscount = $derived((settings?.discount_enabled ?? false) || any((r) => r.discount_minor !== '0'));
	let showRefunds = $derived((settings?.returns_enabled ?? false) || any((r) => r.return_count > 0));
	let egpOnly = $derived(summary.length === 1 && summary[0].currency === 'EGP');
	let hasActivity = $derived(summary.some((r) => r.sales_count > 0 || r.return_count > 0));
	let extraTotal = $derived(costs.reduce((sum, c) => sum + c.minor, 0n));
	let adjustedProfit = $derived(report ? (BigInt(report.base.profit_minor) - extraTotal).toString() : '0');

	// Both currencies always appear, so "no USD sales" is stated, not implied.
	let currencyRows = $derived<CurrencyMixRow[]>(
		(['EGP', 'USD'] as const).map(
			(cur) =>
				report?.currency_mix.find((row) => row.currency === cur) ?? {
					currency: cur,
					sales_count: 0,
					total_minor: '0',
					report_currency: report && report.display_currency !== 'all' ? report.display_currency : cur,
					report_total_minor: '0'
				}
		)
	);
	let rates = $derived(report?.exchange_rate);
	// The column appears only when some Sale on the page was converted.
	let showRecorded = $derived((report?.sales ?? []).some((row) => row.rate_microrate !== null));
	function rateText(microrate: string) {
		return t(`الدولار = ${formatMicrorate(microrate)} جنيه`, `1 USD = ${formatMicrorate(microrate)} EGP`);
	}

	function money(minor: string, cur: 'EGP' | 'USD') {
		return formatMinor(minor, cur);
	}
	function name(row: { name_ar: string; name_en: string }) {
		return lang() === 'ar' ? row.name_ar || row.name_en : row.name_en || row.name_ar;
	}
	function day(date: string) {
		const [y, m, d] = date.split('-').map(Number);
		return new Intl.DateTimeFormat(lang() === 'ar' ? 'ar-EG' : 'en-GB', { dateStyle: 'medium', timeZone: 'UTC' }).format(new Date(Date.UTC(y, m - 1, d)));
	}
	function method(value: string) {
		return value === 'cash' ? msg('salesReportView.cash') : value === 'card' ? msg('salesReportView.card') : msg('salesReportView.other');
	}
	function missing(count: number) {
		return t(`التكلفة غير متاحة لعدد ${count} من البنود`, `Cost unavailable for ${count} item(s)`);
	}
	const periodNames: Record<string, [string, string]> = {
		today: ['اليوم', 'Today'],
		yesterday: ['أمس', 'Yesterday'],
		last_10_completed_days: ['آخر 10 أيام مكتملة', 'Last 10 completed days'],
		custom: ['فترة مخصصة', 'Custom range']
	};

	function addCost() {
		drafts = [...drafts, { id: nextDraft++, description: '', amount: '' }];
	}
	function removeCost(id: number) {
		drafts = drafts.filter((d) => d.id !== id);
	}
	function applyCosts() {
		const parsed = drafts.map((d) => ({ description: d.description.trim(), minor: parseMajor(d.amount) }));
		if (parsed.some((c) => !c.description || c.description.length > 120 || c.minor == null || c.minor <= 0n)) {
			costsError = true;
			return;
		}
		costsError = false;
		costs = parsed.map((c) => ({ description: c.description, minor: c.minor as bigint }));
	}

	// Business totals without refunds: one table carries sales, cost and profit.
	let totalSteps = $derived([
		{ ar: showTax ? 'المبيعات (شاملة الضريبة)' : 'المبيعات', en: showTax ? 'Sales (incl. tax)' : 'Sales', key: 'sales_minor' as const },
		...(showTax
			? [
					{ ar: 'الضريبة الأصلية', en: 'Tax charged', key: 'tax_minor' as const },
					{ ar: 'صافي الإيراد (دون الضريبة)', en: 'Net revenue (excl. tax)', key: 'net_revenue_minor' as const }
				]
			: []),
		{ ar: 'التكلفة التاريخية الأصلية', en: 'Original historical cost', key: 'cost_minor' as const },
		{ ar: showTax ? 'مجمل الربح (دون الضريبة)' : 'مجمل الربح', en: showTax ? 'Gross profit (excl. tax)' : 'Gross profit', key: 'profit_minor' as const, total: true }
	]);
	let revenueSteps = $derived([
		{ ar: 'المبيعات قبل الاسترداد', en: 'Sales before refunds', key: 'sales_minor' as const },
		{ ar: 'يُخصم: المبالغ المستردة', en: 'Less: refunds', key: 'refund_minor' as const },
		{ ar: 'المبيعات بعد الاسترداد', en: 'Sales after refunds', key: 'net_sales_minor' as const },
		...(showTax
			? [
					{ ar: 'الضريبة الأصلية', en: 'Tax charged', key: 'tax_minor' as const },
					{ ar: 'الضريبة المستردة', en: 'Tax refunded', key: 'refunded_tax_minor' as const },
					{ ar: 'صافي الضريبة', en: 'Net tax', key: 'net_tax_minor' as const }
				]
			: []),
		{ ar: 'صافي الإيراد', en: 'Net revenue', key: 'net_revenue_minor' as const, total: true }
	]);
	const costSteps = [
		{ ar: 'التكلفة التاريخية الأصلية', en: 'Original historical cost', key: 'cost_minor' as const },
		{ ar: 'يُخصم: تكلفة الأصناف المعادة للمخزون', en: 'Less: cost restored to stock', key: 'restocked_cost_minor' as const },
		{ ar: 'صافي التكلفة بعد إعادة المخزون', en: 'Net cost after restocking', key: 'net_cost_minor' as const },
		{ ar: 'مجمل الربح بعد المرتجعات', en: 'Gross profit after returns', key: 'profit_minor' as const, total: true }
	];
	let saleTotalSteps = $derived([
		{ ar: 'إجمالي الأصناف بالأسعار المتفق عليها', en: 'Items total at agreed prices', key: 'gross_minor' as const },
		{ ar: 'يُخصم: خصومات البيع', en: 'Less: sale discounts', key: 'discount_minor' as const },
		...(showTax ? [{ ar: 'يُضاف: الضريبة', en: 'Add: tax', key: 'tax_minor' as const }] : []),
		{ ar: 'المبيعات قبل الاسترداد', en: 'Sales before refunds', key: 'sales_minor' as const, total: true }
	]);
	const panels = [
		{ id: 'overview', ar: 'نظرة عامة', en: 'Overview' },
		{ id: 'classifications', ar: 'التصنيفات', en: 'Classifications' },
		{ id: 'performance', ar: 'المنتجات والمدفوعات', en: 'Products & payments' },
		{ id: 'details', ar: 'تفاصيل المبيعات', en: 'Sales details' }
	] as const;

</script>

{#snippet cost(row: { cost_minor: string; missing_cost_items: number; currency: 'EGP' | 'USD' })}
	<td><bdi class="num">{money(row.cost_minor, row.currency)}</bdi>{#if row.missing_cost_items > 0}<small>{missing(row.missing_cost_items)}</small>{/if}</td>
{/snippet}

{#snippet groupTable(rows: SalesReport['categories'], label: [string, string], products = false)}
	<div class="table-scroll">
		<table>
			<thead>
				<tr>
					<th>{t(...label)}</th>
					{#if products}<th>SKU</th>{/if}
					<th>{msg('salesReportView.itemsSold')}</th>
					{#if showRefunds}<th>{msg('salesReportView.itemsReturned')}</th>{/if}
					<th>{products ? msg('salesReportView.sales') : msg('salesReportView.attributedSales')}</th>
					<th>{msg('salesReportView.originalCost')}</th>
					{#if showRefunds}<th>{msg('salesReportView.refunds')}</th><th>{msg('salesReportView.netSales')}</th>{/if}
				</tr>
			</thead>
			<tbody>
				{#each rows as row (row.id + row.currency)}
					<tr>
						<th scope="row">{name(row)}</th>
						{#if products}<td class="mono"><bdi dir="ltr">{row.sku}</bdi></td>{/if}
						<td><bdi class="num">{formatInt(row.items_sold)}</bdi></td>
						{#if showRefunds}<td><bdi class="num">{formatInt(row.returned_items)}</bdi></td>{/if}
						<td><bdi class="num">{money(row.sales_minor, row.currency)}</bdi></td>
						{@render cost(row)}
						{#if showRefunds}<td><bdi class="num">{money(row.refund_minor, row.currency)}</bdi></td><td><bdi class="num">{money(row.net_sales_minor, row.currency)}</bdi></td>{/if}
					</tr>
				{:else}
					<tr><td colspan="8" class="muted">{msg('salesReportView.nothingInThisPeriod')}</td></tr>
				{/each}
			</tbody>
		</table>
	</div>
{/snippet}

{#snippet stepTable(steps: { ar: string; en: string; key: keyof ReportSummaryRow; total?: boolean }[], title: [string, string])}
	<table aria-label={t(...title)}>
		<thead><tr><th>{msg('salesReportView.step')}</th>{#each summary as row}<th>{row.currency}</th>{/each}</tr></thead>
		<tbody>
			{#each steps as step}
				<tr class:report-total-row={step.total}>
					<th scope="row">{t(step.ar, step.en)}</th>
					{#each summary as row}
						<td>
							<bdi class="num">{money(String(row[step.key]), row.currency)}</bdi>
							{#if step.key === 'cost_minor' && row.missing_cost_items > 0}<small>{missing(row.missing_cost_items)}</small>{/if}
						</td>
					{/each}
				</tr>
			{/each}
		</tbody>
	</table>
{/snippet}

<section class="card report-controls screen-only" aria-label={msg('salesReportView.reportRangeAndFilters')}>
	<div class="filter-head">
		<h2>{msg('salesReportView.reportRangeAndFilters')}</h2>
		<button type="button" class="btn-link" onclick={resetFilters}>{msg('salesReportView.reset')}</button>
	</div>
	<div class="report-filter-grid">
		<SingleSelect
			id="report-currency"
			label={msg('salesReportView.currency')}
			testid="report-currency"
			value={currency}
			options={[
				{ value: 'EGP', label: msg('salesReportView.egpUsdSalesConverted') },
				{ value: 'USD', label: msg('salesReportView.usdEgpSalesConverted') },
				{ value: 'all', label: msg('salesReportView.allEachCurrencySeparately') }
			]}
			onchange={setCurrency}
		/>
		<SingleSelect
			id="report-payment"
			label={msg('salesReportView.payment')}
			testid="report-payment"
			value={filter.payment_method}
			options={[
				{ value: '', label: msg('salesReportView.all') },
				{ value: 'cash', label: msg('salesReportView.cash') },
				{ value: 'card', label: msg('salesReportView.card') },
				{ value: 'other', label: msg('salesReportView.other') }
			]}
			onchange={(v) => setFilter({ payment_method: v as ReportFilter['payment_method'] })}
		/>
		<MultiSelect
			id="report-categories"
			label={msg('salesReportView.rootCategories')}
			testid="report-categories"
			emptyLabel={msg('salesReportView.allHistoricalCategories')}
			options={optionsOf(filterOptions.categories)}
			selected={filter.root_category_ids}
			onchange={(values) => setFilter({ root_category_ids: values })}
		/>
		<MultiSelect
			id="report-subcategories"
			label={msg('salesReportView.subcategories')}
			testid="report-subcategories"
			emptyLabel={msg('salesReportView.allHistoricalSubcategories')}
			options={optionsOf(filterOptions.subcategories)}
			selected={filter.subcategory_ids}
			onchange={(values) => setFilter({ subcategory_ids: values })}
		/>
		<MultiSelect
			id="report-tags"
			label={msg('salesReportView.tags')}
			testid="report-tags"
			emptyLabel={msg('salesReportView.allTags')}
			options={optionsOf(filterOptions.tags)}
			selected={filter.tag_ids}
			onchange={(values) => setFilter({ tag_ids: values })}
		/>
	</div>
	<p class="report-filter-note">
		{msg('salesReportView.classificationAndPaymentFiltersSelectComplete')}
		{#if currency === 'all'}
			{msg('salesReportView.eachCurrencyIsShownSeparatelyWithout')}
		{:else}
			{t(
				`تُعرض كل المبيعات بعملة ${currency}. تُحوَّل مبيعات الدولار بسعر الصرف المحفوظ مع كل بيع، ومبيعات الجنيه بسعر الصرف المسجل في الإعدادات وقت البيع.`,
				`All sales are shown in ${currency}. USD sales convert at the rate saved with each sale; EGP sales at the rate in Settings when they were made.`
			)}
		{/if}
		{msg('salesReportView.thePeriodAndStoreComeFrom')}
	</p>
	<fieldset class="report-extra-costs">
		<legend>{msg('salesReportView.manualExtraCosts')}</legend>
		{#if drafts.length === 0}<p class="muted">{msg('salesReportView.noExtraCostsAdded')}</p>{/if}
		{#each drafts as draft, index (draft.id)}
			<div class="report-extra-cost-row">
				<TextField id={`extra-cost-desc-${draft.id}`} label={`${msg('salesReportView.shortDescription')} ${index + 1}`} maxlength={120} bind:value={draft.description} />
				<MoneyField id={`extra-cost-amount-${draft.id}`} label={`${msg('salesReportView.amount')} ${index + 1}`} bind:value={draft.amount} />
				<button type="button" class="btn-danger btn-sm" onclick={() => removeCost(draft.id)}>{msg('salesReportView.remove')}</button>
			</div>
		{/each}
		<div class="btn-row">
			<button type="button" class="btn-secondary" disabled={drafts.length >= 50} onclick={addCost}>+ {msg('salesReportView.addCost')}</button>
			<button type="button" class="btn-secondary" onclick={applyCosts}>{msg('salesReportView.updateReportCosts')}</button>
		</div>
		<p class="muted">{msg('salesReportView.optionalEgpCostsForThisReport')}</p>
		{#if costsError}<p class="field-error" role="alert">{msg('salesReportView.everyCostNeedsAShortDescription')}</p>{/if}
	</fieldset>
</section>

{#if error}<div class="report-alert screen-only" role="alert">{error}</div>{/if}
{#if loading && !report}<div class="card muted" role="status">{msg('salesReportView.loadingReport')}</div>
{:else if report && !error}
	{#if hasActivity}
		<div class="report-tabs screen-only" role="tablist" aria-label={msg('salesReportView.reportSections')}>
			{#each panels as p}
				<button type="button" class="tab" role="tab" aria-selected={panel === p.id} class:active={panel === p.id} onclick={() => (panel = p.id)}>{t(p.ar, p.en)}</button>
			{/each}
		</div>
	{/if}
	<article id="report-printable" class="report-surface" class:stale={loading} aria-label={msg('salesReportView.salesReport')}>
		<ReportLetterhead title={msg('salesReportView.salesReport2')} shop={report.shop as ReportShop | null} {storeName}>
			<div><dt>{msg('salesReportView.period')}</dt><dd>{t(...(periodNames[report.period.kind] ?? [report.period.kind, report.period.kind]))}</dd></div>
			<div><dt>{msg('salesReportView.from')}</dt><dd>{day(report.period.start_date)}</dd></div>
			<div><dt>{msg('salesReportView.to')}</dt><dd>{day(report.period.end_date)}</dd></div>
			<div><dt>{msg('salesReportView.generated')}</dt><dd>{absoluteTime(report.generated_at, lang())}</dd></div>
		</ReportLetterhead>

		{#if !hasActivity}
			<div class="card report-empty">
				<h2>{msg('salesReportView.noSalesInThisPeriod')}</h2>
				<p class="muted">
					{filtered
						? msg('salesReportView.chooseAnotherPeriodOrClearThe')
						: msg('salesReportView.chooseAnotherPeriodOrStoreAt')}
				</p>
			</div>
		{:else}
			<div class="report-panel" class:active={panel === 'overview'}>
				<section aria-labelledby="report-summary-heading">
					<h2 id="report-summary-heading">{msg('salesReportView.overallSummary')}</h2>
					<div class="report-money-grid">
						{#if showRefunds}
							<div class="card report-money-detail">
								<h3>{msg('salesReportView.salesAndQuantities')}</h3>
								<table>
									<thead><tr><th>{msg('salesReportView.currency')}</th><th>{msg('salesReportView.numberOfSales')}</th><th>{msg('salesReportView.itemsSold')}</th><th>{msg('salesReportView.itemsReturned')}</th><th>{msg('salesReportView.netItems')}</th></tr></thead>
									<tbody>
										{#each summary as row}
											<tr><th scope="row">{row.currency}</th><td><bdi class="num">{formatInt(row.sales_count)}</bdi></td><td><bdi class="num">{formatInt(row.items_sold)}</bdi></td><td><bdi class="num">{formatInt(row.returned_items)}</bdi></td><td><bdi class="num">{formatInt(row.items_sold - row.returned_items)}</bdi></td></tr>
										{/each}
									</tbody>
								</table>
							</div>
						{:else}
							<div class="card report-money-detail">
								<h3>{egpOnly ? msg('salesReportView.businessTotalsEgp') : msg('salesReportView.businessTotals')}</h3>
								<table>
									<thead><tr><th>{msg('salesReportView.step')}</th>{#each summary as row}<th>{row.currency}</th>{/each}</tr></thead>
									<tbody>
										<tr><th scope="row">{msg('salesReportView.numberOfSales')}</th>{#each summary as row}<td><bdi class="num">{formatInt(row.sales_count)}</bdi></td>{/each}</tr>
										<tr><th scope="row">{msg('salesReportView.itemsSold')}</th>{#each summary as row}<td><bdi class="num">{formatInt(row.items_sold)}</bdi></td>{/each}</tr>
										{#each totalSteps as step}
											<tr class:report-total-row={step.total}>
												<th scope="row">{t(step.ar, step.en)}</th>
												{#each summary as row}<td><bdi class="num">{money(row[step.key], row.currency)}</bdi>{#if step.key === 'cost_minor' && row.missing_cost_items > 0}<small>{missing(row.missing_cost_items)}</small>{/if}</td>{/each}
											</tr>
										{/each}
										{#if egpOnly}
											<tr><th scope="row">{msg('salesReportView.manualExtraCosts')}</th><td><bdi class="num">{money(extraTotal.toString(), 'EGP')}</bdi></td></tr>
											<tr class="report-total-row"><th scope="row">{msg('salesReportView.netProfitAfterExtraCosts')}</th><td><bdi class="num">{money(adjustedProfit, 'EGP')}</bdi></td></tr>
										{/if}
									</tbody>
								</table>
								<p class="report-note">
									{showTax
										? msg('salesReportView.salesTaxNetRevenueNetRevenue')
										: msg('salesReportView.salesOriginalCostGrossProfitExtra')}
								</p>
							</div>
						{/if}
						{#if showRefunds || !egpOnly}
							<div class="card report-money-detail">
								<h3>{msg('salesReportView.businessTotalsEgp')}</h3>
								<table>
									<thead><tr><th>{msg('salesReportView.step')}</th><th>EGP</th></tr></thead>
									<tbody>
										<tr><th scope="row">{msg('salesReportView.salesInEgp')}</th><td><bdi class="num">{money(report.base.sales_minor, 'EGP')}</bdi></td></tr>
										<tr><th scope="row">{msg('salesReportView.grossProfitAfterCurrencyConversion')}</th><td><bdi class="num">{money(report.base.profit_minor, 'EGP')}</bdi></td></tr>
										<tr><th scope="row">{msg('salesReportView.manualExtraCosts')}</th><td><bdi class="num">{money(extraTotal.toString(), 'EGP')}</bdi></td></tr>
										<tr class="report-total-row"><th scope="row">{msg('salesReportView.netProfitAfterExtraCosts')}</th><td><bdi class="num">{money(adjustedProfit, 'EGP')}</bdi></td></tr>
									</tbody>
								</table>
								<p class="report-note">{msg('salesReportView.usdAmountsUseTheExchangeRate')}</p>
							</div>
						{/if}
						<div class="card report-money-detail report-currency-mix" data-testid="report-currency-mix">
							<h3>{msg('salesReportView.currenciesAndUsdRate')}</h3>
							<table aria-label={msg('salesReportView.currenciesAndUsdRate')}>
								<tbody>
									{#each currencyRows as row (row.currency)}
										<tr>
											<th scope="row">{t(`المبيعات المسجلة بـ ${row.currency}`, `Sales recorded in ${row.currency}`)}</th>
											<td>
												<bdi class="num">{money(row.total_minor, row.currency)}</bdi>
												<small>{t(`عدد عمليات البيع: ${formatInt(row.sales_count)}`, `Sales: ${formatInt(row.sales_count)}`)}</small>
												{#if row.report_currency !== row.currency && row.sales_count > 0}<small>{msg('salesReportView.inThisReport')}: <bdi class="num">{money(row.report_total_minor, row.report_currency)}</bdi></small>{/if}
											</td>
										</tr>
									{/each}
                                    {#each summary as total}
                                      <tr class="report-total-row"><th scope="row">{msg('salesReportView.convertedTotal', { currency: total.currency })}</th>
                                        <td><bdi class="num">{money(total.sales_minor, total.currency)}</bdi><small>{t(`عدد عمليات البيع: ${formatInt(total.sales_count)}`, `Sales: ${formatInt(total.sales_count)}`)}</small></td></tr>
                                    {/each}
									<tr>
										<th scope="row">{msg('salesReportView.latestUsdRate')}</th>
										<td>
											{#if rates?.latest}
												<bdi class="num">{rateText(rates.latest.rate_microrate)}</bdi>
												<small>{msg('salesReportView.inEffectSince')} {absoluteTime(rates.latest.effective_at, lang())}</small>
												<small>{rates.latest.source === 'cloud' ? msg('salesReportView.fromCloudSettings') : msg('salesReportView.fromTheLatestUsdSale')}</small>
											{:else}
												{msg('salesReportView.noUsdRateRecordedYet')}
											{/if}
										</td>
									</tr>
									{#if rates?.period_min_microrate != null && rates.period_max_microrate != null}
										<tr>
											<th scope="row">{msg('salesReportView.usdRatesUsedInThisPeriod')}</th>
											<td><bdi class="num">{rates.period_min_microrate === rates.period_max_microrate
												? rateText(rates.period_min_microrate)
												: t(`${formatMicrorate(rates.period_min_microrate)} – ${formatMicrorate(rates.period_max_microrate)} جنيه لكل دولار`, `${formatMicrorate(rates.period_min_microrate)} – ${formatMicrorate(rates.period_max_microrate)} EGP per USD`)}</bdi></td>
										</tr>
									{/if}
								</tbody>
							</table>
							<p class="report-note">{msg('salesReportView.convertedHint')}</p><p class="report-note">{msg('salesReportView.eachSaleConvertsAtTheUsd')}</p>
						</div>
					</div>
					{#if showRefunds}
						<section class="report-reconciliation" aria-labelledby="report-reconciliation-heading">
							<h3 id="report-reconciliation-heading">{msg('salesReportView.howTheFiguresAddUp')}</h3>
							<div class="report-money-grid">
								<div class="card report-money-detail">
									<h3>{msg('salesReportView.revenueAfterRefunds')}</h3>
									{@render stepTable(revenueSteps, ['الإيراد بعد الاسترداد', 'Revenue after refunds'])}
								</div>
								<div class="card report-money-detail">
									<h3>{msg('salesReportView.costAndGrossProfit')}</h3>
									{@render stepTable(costSteps, ['التكلفة ومجمل الربح', 'Cost and gross profit'])}
								</div>
							</div>
							<p class="report-note">{msg('salesReportView.onlyReturnedItemsPutBackInto')}</p>
						</section>
					{/if}
					<div class="report-money-grid">
						{#if costs.length > 0}
							<div class="card report-money-detail">
								<h3>{msg('salesReportView.extraCostBreakdown')}</h3>
								<table>
									<thead><tr><th>{msg('salesReportView.shortDescription')}</th><th>{msg('salesReportView.amount')}</th></tr></thead>
									<tbody>
										{#each costs as c}<tr><th scope="row">{c.description}</th><td><bdi class="num">{money(c.minor.toString(), 'EGP')}</bdi></td></tr>{/each}
									</tbody>
								</table>
							</div>
						{/if}
						{#if showDiscount}
							<div class="card report-money-detail">
								<h3>{msg('salesReportView.howSalesTotalsAreCalculated')}</h3>
								{@render stepTable(saleTotalSteps, ['كيف يُحسب إجمالي البيع', 'How sales totals are calculated'])}
							</div>
						{/if}
					</div>
					{#if report.unclassified_items > 0}
						<p class="report-warning">{t(`التصنيف التاريخي غير متاح لعدد ${report.unclassified_items} من البنود المباعة.`, `Historical classification is unavailable for ${report.unclassified_items} sold item(s).`)}</p>
					{/if}
				</section>

				<section class="card report-section" aria-labelledby="report-daily-heading">
					<h2 id="report-daily-heading">{msg('salesReportView.dailySales')}</h2>
					<div class="table-scroll">
						<table>
							<thead>
								<tr>
									<th>{msg('salesReportView.date')}</th><th>{msg('salesReportView.numberOfSales')}</th><th>{msg('salesReportView.itemsSold')}</th>
									{#if showRefunds}<th>{msg('salesReportView.itemsReturned')}</th>{/if}
									<th>{msg('salesReportView.sales')}</th><th>{msg('salesReportView.originalCost')}</th>
									{#if showRefunds}<th>{msg('salesReportView.refunds')}</th><th>{msg('salesReportView.netSales')}</th>{/if}
								</tr>
							</thead>
							<tbody>
								{#each report.daily as row (row.date + row.currency)}
									<tr>
										<td>{day(row.date)}</td><td><bdi class="num">{formatInt(row.sales_count)}</bdi></td><td><bdi class="num">{formatInt(row.items_sold)}</bdi></td>
										{#if showRefunds}<td><bdi class="num">{formatInt(row.returned_items)}</bdi></td>{/if}
										<td><bdi class="num">{money(row.sales_minor, row.currency)}</bdi></td>
										{@render cost(row)}
										{#if showRefunds}<td><bdi class="num">{money(row.refund_minor, row.currency)}</bdi></td><td><bdi class="num">{money(row.net_sales_minor, row.currency)}</bdi></td>{/if}
									</tr>
								{/each}
							</tbody>
						</table>
					</div>
				</section>
			</div>

			<div class="report-panel" class:active={panel === 'classifications'}>
				<div class="report-performance-grid">
					<section class="card report-section">
						<h2>{msg('salesReportView.categoryPerformance')}</h2>
						<p>{msg('salesReportView.attributedTotalsMayOverlapBecauseOne')}</p>
						{@render groupTable(report.categories, ['التصنيف الرئيسي', 'Category'])}
					</section>
					<section class="card report-section">
						<h2>{msg('salesReportView.subcategoryPerformance')}</h2>
						<p>{msg('salesReportView.attributedTotalsMayOverlapBecauseOne')}</p>
						{@render groupTable(report.subcategories, ['التصنيف الفرعي', 'Subcategory'])}
					</section>
				</div>
			</div>

			<div class="report-panel" class:active={panel === 'performance'}>
				<div class="report-performance-grid">
					<section class="card report-section">
						<h2>{msg('salesReportView.salesByTag')}</h2>
						<p>{msg('salesReportView.tagGroupsOverlapOneSaleItem')}</p>
						{@render groupTable(report.tags, ['الوسم', 'Tag'])}
					</section>
					<section class="card report-section">
						<h2>{msg('salesReportView.paymentMethods')}</h2>
						<div class="table-scroll">
							<table>
								<thead>
									<tr>
										<th>{msg('salesReportView.paymentMethod')}</th><th>{msg('salesReportView.payments')}</th><th>{msg('salesReportView.amount')}</th>
										{#if showRefunds}<th>{msg('salesReportView.refunds')}</th><th>{msg('salesReportView.netSales')}</th>{/if}
									</tr>
								</thead>
								<tbody>
									{#each report.payments as row (row.method + row.currency)}
										<tr>
											<th scope="row">{method(row.method)}</th><td><bdi class="num">{formatInt(row.payment_count)}</bdi></td>
											<td><bdi class="num">{money(row.amount_minor, row.currency)}</bdi></td>
											{#if showRefunds}<td><bdi class="num">{money(row.refund_minor, row.currency)}</bdi></td><td><bdi class="num">{money(row.net_minor, row.currency)}</bdi></td>{/if}
										</tr>
									{/each}
								</tbody>
							</table>
						</div>
					</section>
				</div>
				<section class="card report-section">
					<h2>{msg('salesReportView.productPerformance')}</h2>
					{@render groupTable(report.products, ['المنتج', 'Product'], true)}
				</section>
			</div>

			<div class="report-panel" class:active={panel === 'details'}>
				<section class="card report-section report-details" aria-labelledby="report-sales-heading">
					<h2 id="report-sales-heading">{msg('salesReportView.salesDetails')}</h2><p>{msg('salesReportView.lineContext.costHint')}</p>
					<div class="table-scroll">
						<table>
							<thead>
								<tr>
									<th>{msg('salesReportView.reference')}</th><th>{msg('salesReportView.date')}</th><th>{msg('salesReportView.product')}</th>
									<th>{msg('salesReportView.category')}</th><th>{msg('salesReportView.subcategory')}</th><th>{msg('salesReportView.items')}</th>
									<th>{msg('salesReportView.payment')}</th><th>{msg('salesReportView.originalCost')}</th><th>{msg('salesReportView.sales')}</th>
									{#if showRecorded}<th>{msg('salesReportView.recordedAs')}</th>{/if}
								</tr>
							</thead>
							<tbody>
								{#each report.sales as row (row.sale_id)}
									<tr>
										<td class="mono"><bdi dir="ltr">{row.sale_number}</bdi></td>
										<td>{absoluteTime(row.occurred_at, lang())}</td>
										<td>{row.products}<small class="mono" dir="ltr">{row.skus}</small></td>
										<td>{(lang() === 'ar' ? row.root_names_ar : row.root_names_en) || msg('salesReportView.classificationUnavailable')}</td>
										<td>{(lang() === 'ar' ? row.subcategory_names_ar : row.subcategory_names_en) || '—'}</td>
										<td><bdi class="num">{formatInt(row.items_sold)}</bdi></td>
										<td>{row.payment_methods.map(method).join(msg('salesReportView.text'))}</td>
										{@render cost(row)}
										<td><bdi class="num">{money(row.total_minor, row.currency)}</bdi></td>
										{#if showRecorded}
											<td>{#if row.rate_microrate !== null}<bdi class="num">{money(row.original_total_minor, row.original_currency)}</bdi><small>{msg('salesReportView.at')} <bdi class="num">{formatMicrorate(row.rate_microrate)}</bdi></small>{:else}—{/if}</td>
										{/if}
									</tr>
 {#if row.lines?.length}<tr><td class="sale-line-context-cell" colspan={showRecorded?10:9}><ReportSaleLines lines={row.lines} saleID={row.sale_id} saleNumber={row.sale_number} /></td></tr>{/if}
								{/each}
							</tbody>
						</table>
					</div>
					{#if report.sales_total_count > PAGE}
						<div class="pager">
							<span class="num">{offset + 1}–{Math.min(offset + report.sales.length, report.sales_total_count)} / {report.sales_total_count}</span>
							<button type="button" class="btn-secondary btn-sm screen-only" disabled={offset === 0} onclick={() => (offset = Math.max(0, offset - PAGE))}>{msg('salesReportView.previous')}</button>
							<button type="button" class="btn-secondary btn-sm screen-only" disabled={offset + PAGE >= report.sales_total_count} onclick={() => (offset += PAGE)}>{msg('salesReportView.next')}</button>
						</div>
					{/if}
				</section>
			</div>
		{/if}
	</article>
{/if}
