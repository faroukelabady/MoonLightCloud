<script lang="ts">
	import { ApiError, type PeriodParams } from '../lib/http.js';
	import { emptyFilter, reportsApi, type CurrencyMixRow, type FilterOption, type ReportDisplay, type ReportFilter, type ReportShop, type ReportSummaryRow, type SalesReport } from '../lib/reportsApi.js';
		import { formatInt, formatMicrorate, formatMinor } from '../lib/money.js';
	import { absoluteTime } from '../lib/time.js';
	import { lang, t } from '../lib/i18n.svelte.js';
	import { parseMajor } from '../lib/zakat.js';
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
			return t(
				'لا يوجد سعر صرف للدولار بعد. أضف سعراً من صفحة الإعدادات.',
				'There is no USD exchange rate yet. Add one on the Settings page.'
			);
		if (err instanceof ApiError && err.detail === 'STORE_SCOPE_REQUIRED') return t('اختر متجراً من أعلى الصفحة.', 'Choose a Store at the top of the page.');
		if (err instanceof ApiError && err.code === 'FORBIDDEN') return t('ليست لديك صلاحية عرض التقارير.', 'You do not have permission to view reports.');
		return t('تعذر تحميل التقرير. حاول مرة أخرى.', 'The report could not be loaded. Try again.');
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
		return value === 'cash' ? t('نقدي', 'Cash') : value === 'card' ? t('بطاقة', 'Card') : t('أخرى', 'Other');
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
					<th>{t('القطع المباعة', 'Items sold')}</th>
					{#if showRefunds}<th>{t('البنود المرتجعة', 'Items returned')}</th>{/if}
					<th>{products ? t('إجمالي المبيعات', 'Sales') : t('المبيعات المنسوبة', 'Attributed sales')}</th>
					<th>{t('التكلفة الأصلية', 'Original cost')}</th>
					{#if showRefunds}<th>{t('المرتجعات', 'Refunds')}</th><th>{t('صافي المبيعات', 'Net sales')}</th>{/if}
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
					<tr><td colspan="8" class="muted">{t('لا توجد بيانات في هذه الفترة.', 'Nothing in this period.')}</td></tr>
				{/each}
			</tbody>
		</table>
	</div>
{/snippet}

{#snippet stepTable(steps: { ar: string; en: string; key: keyof ReportSummaryRow; total?: boolean }[], title: [string, string])}
	<table aria-label={t(...title)}>
		<thead><tr><th>{t('الخطوة', 'Step')}</th>{#each summary as row}<th>{row.currency}</th>{/each}</tr></thead>
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

<section class="card report-controls screen-only" aria-label={t('فترة التقرير والتصفية', 'Report range and filters')}>
	<div class="filter-head">
		<h2>{t('فترة التقرير والتصفية', 'Report range and filters')}</h2>
		<button type="button" class="btn-link" onclick={resetFilters}>{t('إعادة الضبط', 'Reset')}</button>
	</div>
	<div class="report-filter-grid">
		<SingleSelect
			id="report-currency"
			label={t('العملة', 'Currency')}
			testid="report-currency"
			value={currency}
			options={[
				{ value: 'EGP', label: t('جنيه مصري (مع تحويل مبيعات الدولار)', 'EGP (USD sales converted)') },
				{ value: 'USD', label: t('دولار أمريكي (مع تحويل مبيعات الجنيه)', 'USD (EGP sales converted)') },
				{ value: 'all', label: t('الكل (كل عملة على حدة)', 'All (each currency separately)') }
			]}
			onchange={setCurrency}
		/>
		<SingleSelect
			id="report-payment"
			label={t('الدفع', 'Payment')}
			testid="report-payment"
			value={filter.payment_method}
			options={[
				{ value: '', label: t('الكل', 'All') },
				{ value: 'cash', label: t('نقدي', 'Cash') },
				{ value: 'card', label: t('بطاقة', 'Card') },
				{ value: 'other', label: t('أخرى', 'Other') }
			]}
			onchange={(v) => setFilter({ payment_method: v as ReportFilter['payment_method'] })}
		/>
		<MultiSelect
			id="report-categories"
			label={t('التصنيفات الرئيسية', 'Root categories')}
			testid="report-categories"
			emptyLabel={t('كل التصنيفات التاريخية', 'All historical categories')}
			options={optionsOf(filterOptions.categories)}
			selected={filter.root_category_ids}
			onchange={(values) => setFilter({ root_category_ids: values })}
		/>
		<MultiSelect
			id="report-subcategories"
			label={t('التصنيفات الفرعية', 'Subcategories')}
			testid="report-subcategories"
			emptyLabel={t('كل التصنيفات الفرعية التاريخية', 'All historical subcategories')}
			options={optionsOf(filterOptions.subcategories)}
			selected={filter.subcategory_ids}
			onchange={(values) => setFilter({ subcategory_ids: values })}
		/>
		<MultiSelect
			id="report-tags"
			label={t('الوسوم', 'Tags')}
			testid="report-tags"
			emptyLabel={t('كل الوسوم', 'All tags')}
			options={optionsOf(filterOptions.tags)}
			selected={filter.tag_ids}
			onchange={(values) => setFilter({ tag_ids: values })}
		/>
	</div>
	<p class="report-filter-note">
		{t('تختار تصفية التصنيف والدفع عمليات البيع الكاملة التي تحتوي بنداً تاريخياً مطابقاً؛ وتظل إجماليات العملية والمدفوعات كاملة.', 'Classification and payment filters select complete sales containing a matching historical item; complete sale totals and payments remain intact.')}
		{#if currency === 'all'}
			{t('تُعرض كل عملة بشكل منفصل دون تحويل.', 'Each currency is shown separately, without conversion.')}
		{:else}
			{t(
				`تُعرض كل المبيعات بعملة ${currency}. تُحوَّل مبيعات الدولار بسعر الصرف المحفوظ مع كل بيع، ومبيعات الجنيه بسعر الصرف المسجل في الإعدادات وقت البيع.`,
				`All sales are shown in ${currency}. USD sales convert at the rate saved with each sale; EGP sales at the rate in Settings when they were made.`
			)}
		{/if}
		{t('الفترة والمتجر من أعلى الصفحة.', 'The period and Store come from the top of the page.')}
	</p>
	<fieldset class="report-extra-costs">
		<legend>{t('تكاليف إضافية يدوية', 'Manual extra costs')}</legend>
		{#if drafts.length === 0}<p class="muted">{t('لم تتم إضافة تكاليف إضافية.', 'No extra costs added.')}</p>{/if}
		{#each drafts as draft, index (draft.id)}
			<div class="report-extra-cost-row">
				<TextField id={`extra-cost-desc-${draft.id}`} label={`${t('وصف مختصر', 'Short description')} ${index + 1}`} maxlength={120} bind:value={draft.description} />
				<MoneyField id={`extra-cost-amount-${draft.id}`} label={`${t('المبلغ', 'Amount')} ${index + 1}`} bind:value={draft.amount} />
				<button type="button" class="btn-danger btn-sm" onclick={() => removeCost(draft.id)}>{t('حذف', 'Remove')}</button>
			</div>
		{/each}
		<div class="btn-row">
			<button type="button" class="btn-secondary" disabled={drafts.length >= 50} onclick={addCost}>+ {t('إضافة تكلفة', 'Add cost')}</button>
			<button type="button" class="btn-secondary" onclick={applyCosts}>{t('تحديث تكاليف التقرير', 'Update report costs')}</button>
		</div>
		<p class="muted">{t('تكاليف اختيارية بالجنيه المصري لهذا التقرير ونسخة الطباعة فقط، ولا يتم حفظها.', 'Optional EGP costs for this report and printout only. They are not saved.')}</p>
		{#if costsError}<p class="field-error" role="alert">{t('يجب أن تحتوي كل تكلفة على وصف مختصر ومبلغ موجب بمنزلتين عشريتين كحد أقصى.', 'Every cost needs a short description and a positive amount with at most two decimals.')}</p>{/if}
	</fieldset>
</section>

{#if error}<div class="report-alert screen-only" role="alert">{error}</div>{/if}
{#if loading && !report}<div class="card muted" role="status">{t('جارٍ تحميل التقرير…', 'Loading report…')}</div>
{:else if report && !error}
	{#if hasActivity}
		<div class="report-tabs screen-only" role="tablist" aria-label={t('أقسام التقرير', 'Report sections')}>
			{#each panels as p}
				<button type="button" class="tab" role="tab" aria-selected={panel === p.id} class:active={panel === p.id} onclick={() => (panel = p.id)}>{t(p.ar, p.en)}</button>
			{/each}
		</div>
	{/if}
	<article id="report-printable" class="report-surface" class:stale={loading} aria-label={t('تقرير المبيعات', 'Sales report')}>
		<ReportLetterhead title={t('تقرير المبيعات', 'Sales Report')} shop={report.shop as ReportShop | null} {storeName}>
			<div><dt>{t('الفترة', 'Period')}</dt><dd>{t(...(periodNames[report.period.kind] ?? [report.period.kind, report.period.kind]))}</dd></div>
			<div><dt>{t('من', 'From')}</dt><dd>{day(report.period.start_date)}</dd></div>
			<div><dt>{t('إلى', 'To')}</dt><dd>{day(report.period.end_date)}</dd></div>
			<div><dt>{t('وقت الإنشاء', 'Generated')}</dt><dd>{absoluteTime(report.generated_at, lang())}</dd></div>
		</ReportLetterhead>

		{#if !hasActivity}
			<div class="card report-empty">
				<h2>{t('لا توجد مبيعات في هذه الفترة', 'No sales in this period')}</h2>
				<p class="muted">
					{filtered
						? t('اختر فترة أخرى أو أزل تصفيات التقرير.', 'Choose another period or clear the report filters.')
						: t('اختر فترة أخرى أو متجراً آخر من أعلى الصفحة.', 'Choose another period or Store at the top of the page.')}
				</p>
			</div>
		{:else}
			<div class="report-panel" class:active={panel === 'overview'}>
				<section aria-labelledby="report-summary-heading">
					<h2 id="report-summary-heading">{t('الملخص العام', 'Overall summary')}</h2>
					<div class="report-money-grid">
						{#if showRefunds}
							<div class="card report-money-detail">
								<h3>{t('عمليات البيع والكميات', 'Sales and quantities')}</h3>
								<table>
									<thead><tr><th>{t('العملة', 'Currency')}</th><th>{t('عدد عمليات البيع', 'Number of sales')}</th><th>{t('القطع المباعة', 'Items sold')}</th><th>{t('البنود المرتجعة', 'Items returned')}</th><th>{t('صافي البنود', 'Net items')}</th></tr></thead>
									<tbody>
										{#each summary as row}
											<tr><th scope="row">{row.currency}</th><td><bdi class="num">{formatInt(row.sales_count)}</bdi></td><td><bdi class="num">{formatInt(row.items_sold)}</bdi></td><td><bdi class="num">{formatInt(row.returned_items)}</bdi></td><td><bdi class="num">{formatInt(row.items_sold - row.returned_items)}</bdi></td></tr>
										{/each}
									</tbody>
								</table>
							</div>
						{:else}
							<div class="card report-money-detail">
								<h3>{egpOnly ? t('إجماليات المتجر · جنيه مصري', 'Business totals · EGP') : t('إجماليات المتجر', 'Business totals')}</h3>
								<table>
									<thead><tr><th>{t('الخطوة', 'Step')}</th>{#each summary as row}<th>{row.currency}</th>{/each}</tr></thead>
									<tbody>
										<tr><th scope="row">{t('عدد عمليات البيع', 'Number of sales')}</th>{#each summary as row}<td><bdi class="num">{formatInt(row.sales_count)}</bdi></td>{/each}</tr>
										<tr><th scope="row">{t('القطع المباعة', 'Items sold')}</th>{#each summary as row}<td><bdi class="num">{formatInt(row.items_sold)}</bdi></td>{/each}</tr>
										{#each totalSteps as step}
											<tr class:report-total-row={step.total}>
												<th scope="row">{t(step.ar, step.en)}</th>
												{#each summary as row}<td><bdi class="num">{money(row[step.key], row.currency)}</bdi>{#if step.key === 'cost_minor' && row.missing_cost_items > 0}<small>{missing(row.missing_cost_items)}</small>{/if}</td>{/each}
											</tr>
										{/each}
										{#if egpOnly}
											<tr><th scope="row">{t('تكاليف إضافية يدوية', 'Manual extra costs')}</th><td><bdi class="num">{money(extraTotal.toString(), 'EGP')}</bdi></td></tr>
											<tr class="report-total-row"><th scope="row">{t('صافي الربح بعد التكاليف الإضافية', 'Net profit after extra costs')}</th><td><bdi class="num">{money(adjustedProfit, 'EGP')}</bdi></td></tr>
										{/if}
									</tbody>
								</table>
								<p class="report-note">
									{showTax
										? t('المبيعات − الضريبة = صافي الإيراد. صافي الإيراد − التكلفة الأصلية = مجمل الربح. تُخصم التكاليف الإضافية بعد ذلك.', 'Sales − tax = net revenue. Net revenue − original cost = gross profit. Extra costs are deducted afterward.')
										: t('المبيعات − التكلفة الأصلية = مجمل الربح. تُخصم التكاليف الإضافية بعد ذلك.', 'Sales − original cost = gross profit. Extra costs are deducted afterward.')}
								</p>
							</div>
						{/if}
						{#if showRefunds || !egpOnly}
							<div class="card report-money-detail">
								<h3>{t('إجماليات المتجر · جنيه مصري', 'Business totals · EGP')}</h3>
								<table>
									<thead><tr><th>{t('الخطوة', 'Step')}</th><th>EGP</th></tr></thead>
									<tbody>
										<tr><th scope="row">{t('المبيعات بالجنيه المصري', 'Sales in EGP')}</th><td><bdi class="num">{money(report.base.sales_minor, 'EGP')}</bdi></td></tr>
										<tr><th scope="row">{t('مجمل الربح بعد تحويل العملات', 'Gross profit after currency conversion')}</th><td><bdi class="num">{money(report.base.profit_minor, 'EGP')}</bdi></td></tr>
										<tr><th scope="row">{t('تكاليف إضافية يدوية', 'Manual extra costs')}</th><td><bdi class="num">{money(extraTotal.toString(), 'EGP')}</bdi></td></tr>
										<tr class="report-total-row"><th scope="row">{t('صافي الربح بعد التكاليف الإضافية', 'Net profit after extra costs')}</th><td><bdi class="num">{money(adjustedProfit, 'EGP')}</bdi></td></tr>
									</tbody>
								</table>
								<p class="report-note">{t('تُحوّل مبالغ الدولار بسعر الصرف المحفوظ مع كل بيع أو استرداد.', 'USD amounts use the exchange rate saved with each sale or refund.')}</p>
							</div>
						{/if}
						<div class="card report-money-detail report-currency-mix" data-testid="report-currency-mix">
							<h3>{t('العملات وسعر الدولار', 'Currencies and USD rate')}</h3>
							<table aria-label={t('العملات وسعر الدولار', 'Currencies and USD rate')}>
								<tbody>
									{#each currencyRows as row (row.currency)}
										<tr>
											<th scope="row">{t(`المبيعات المسجلة بـ ${row.currency}`, `Sales recorded in ${row.currency}`)}</th>
											<td>
												<bdi class="num">{money(row.total_minor, row.currency)}</bdi>
												<small>{t(`عدد عمليات البيع: ${formatInt(row.sales_count)}`, `Sales: ${formatInt(row.sales_count)}`)}</small>
												{#if row.report_currency !== row.currency && row.sales_count > 0}<small>{t('في هذا التقرير', 'In this report')}: <bdi class="num">{money(row.report_total_minor, row.report_currency)}</bdi></small>{/if}
											</td>
										</tr>
									{/each}
									<tr>
										<th scope="row">{t('آخر سعر للدولار', 'Latest USD rate')}</th>
										<td>
											{#if rates?.latest}
												<bdi class="num">{rateText(rates.latest.rate_microrate)}</bdi>
												<small>{t('سارٍ منذ', 'In effect since')} {absoluteTime(rates.latest.effective_at, lang())}</small>
												<small>{rates.latest.source === 'cloud' ? t('من إعدادات السحابة', 'From Cloud settings') : t('من آخر بيع بالدولار', 'From the latest USD sale')}</small>
											{:else}
												{t('لم يُسجل سعر للدولار بعد', 'No USD rate recorded yet')}
											{/if}
										</td>
									</tr>
									{#if rates?.period_min_microrate != null && rates.period_max_microrate != null}
										<tr>
											<th scope="row">{t('أسعار الدولار المستخدمة في هذه الفترة', 'USD rates used in this period')}</th>
											<td><bdi class="num">{rates.period_min_microrate === rates.period_max_microrate
												? rateText(rates.period_min_microrate)
												: t(`${formatMicrorate(rates.period_min_microrate)} – ${formatMicrorate(rates.period_max_microrate)} جنيه لكل دولار`, `${formatMicrorate(rates.period_min_microrate)} – ${formatMicrorate(rates.period_max_microrate)} EGP per USD`)}</bdi></td>
										</tr>
									{/if}
								</tbody>
							</table>
							<p class="report-note">{t(
								'يُحوَّل كل بيع بسعر الدولار المحفوظ معه، أو بالسعر الساري يوم البيع. يتغير السعر من يوم لآخر، لذا قد تستخدم المبيعات السابقة سعراً مختلفاً عن آخر سعر.',
								'Each sale converts at the USD rate saved on it, or the rate in effect on the day it was made. The rate changes from day to day, so past sales can use a different rate from the latest one.'
							)}</p>
						</div>
					</div>
					{#if showRefunds}
						<section class="report-reconciliation" aria-labelledby="report-reconciliation-heading">
							<h3 id="report-reconciliation-heading">{t('كيف تتطابق الأرقام', 'How the figures add up')}</h3>
							<div class="report-money-grid">
								<div class="card report-money-detail">
									<h3>{t('الإيراد بعد الاسترداد', 'Revenue after refunds')}</h3>
									{@render stepTable(revenueSteps, ['الإيراد بعد الاسترداد', 'Revenue after refunds'])}
								</div>
								<div class="card report-money-detail">
									<h3>{t('التكلفة ومجمل الربح', 'Cost and gross profit')}</h3>
									{@render stepTable(costSteps, ['التكلفة ومجمل الربح', 'Cost and gross profit'])}
								</div>
							</div>
							<p class="report-note">{t('تُعكس تكلفة المرتجعات المعادة للمخزون فقط. الاسترداد دون إعادة للمخزون يخفض الإيراد مع بقاء التكلفة.', 'Only returned items put back into stock reverse cost. Refunds without restocking reduce revenue; their cost remains.')}</p>
						</section>
					{/if}
					<div class="report-money-grid">
						{#if costs.length > 0}
							<div class="card report-money-detail">
								<h3>{t('تفاصيل التكاليف الإضافية', 'Extra-cost breakdown')}</h3>
								<table>
									<thead><tr><th>{t('وصف مختصر', 'Short description')}</th><th>{t('المبلغ', 'Amount')}</th></tr></thead>
									<tbody>
										{#each costs as c}<tr><th scope="row">{c.description}</th><td><bdi class="num">{money(c.minor.toString(), 'EGP')}</bdi></td></tr>{/each}
									</tbody>
								</table>
							</div>
						{/if}
						{#if showDiscount}
							<div class="card report-money-detail">
								<h3>{t('كيف يُحسب إجمالي البيع', 'How sales totals are calculated')}</h3>
								{@render stepTable(saleTotalSteps, ['كيف يُحسب إجمالي البيع', 'How sales totals are calculated'])}
							</div>
						{/if}
					</div>
					{#if report.unclassified_items > 0}
						<p class="report-warning">{t(`التصنيف التاريخي غير متاح لعدد ${report.unclassified_items} من البنود المباعة.`, `Historical classification is unavailable for ${report.unclassified_items} sold item(s).`)}</p>
					{/if}
				</section>

				<section class="card report-section" aria-labelledby="report-daily-heading">
					<h2 id="report-daily-heading">{t('المبيعات اليومية', 'Daily sales')}</h2>
					<div class="table-scroll">
						<table>
							<thead>
								<tr>
									<th>{t('التاريخ', 'Date')}</th><th>{t('عدد عمليات البيع', 'Number of sales')}</th><th>{t('القطع المباعة', 'Items sold')}</th>
									{#if showRefunds}<th>{t('البنود المرتجعة', 'Items returned')}</th>{/if}
									<th>{t('إجمالي المبيعات', 'Sales')}</th><th>{t('التكلفة الأصلية', 'Original cost')}</th>
									{#if showRefunds}<th>{t('المرتجعات', 'Refunds')}</th><th>{t('صافي المبيعات', 'Net sales')}</th>{/if}
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
						<h2>{t('أداء التصنيفات الرئيسية', 'Category performance')}</h2>
						<p>{t('قد تتداخل الإجماليات المنسوبة لأن البند قد ينتمي إلى أكثر من تصنيف. المبالغ هي قيمة البنود قبل خصم البيع والضريبة.', 'Attributed totals may overlap because one item can have several classifications. Amounts are item totals before sale discounts and tax.')}</p>
						{@render groupTable(report.categories, ['التصنيف الرئيسي', 'Category'])}
					</section>
					<section class="card report-section">
						<h2>{t('أداء التصنيفات الفرعية', 'Subcategory performance')}</h2>
						<p>{t('قد تتداخل الإجماليات المنسوبة لأن البند قد ينتمي إلى أكثر من تصنيف. المبالغ هي قيمة البنود قبل خصم البيع والضريبة.', 'Attributed totals may overlap because one item can have several classifications. Amounts are item totals before sale discounts and tax.')}</p>
						{@render groupTable(report.subcategories, ['التصنيف الفرعي', 'Subcategory'])}
					</section>
				</div>
			</div>

			<div class="report-panel" class:active={panel === 'performance'}>
				<div class="report-performance-grid">
					<section class="card report-section">
						<h2>{t('المبيعات حسب الوسم', 'Sales by tag')}</h2>
						<p>{t('مجموعات الوسوم متداخلة: صنف البيع الواحد قد يحمل عدة وسوم، لذا قد تتجاوز مجاميعها الإجمالي.', 'Tag groups overlap: one sale item may carry several tags, so group totals can exceed the overall total.')}</p>
						{@render groupTable(report.tags, ['الوسم', 'Tag'])}
					</section>
					<section class="card report-section">
						<h2>{t('طرق الدفع', 'Payment methods')}</h2>
						<div class="table-scroll">
							<table>
								<thead>
									<tr>
										<th>{t('طريقة الدفع', 'Payment method')}</th><th>{t('عدد الدفعات', 'Payments')}</th><th>{t('المبلغ', 'Amount')}</th>
										{#if showRefunds}<th>{t('المرتجعات', 'Refunds')}</th><th>{t('صافي المبيعات', 'Net sales')}</th>{/if}
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
					<h2>{t('أداء المنتجات', 'Product performance')}</h2>
					{@render groupTable(report.products, ['المنتج', 'Product'], true)}
				</section>
			</div>

			<div class="report-panel" class:active={panel === 'details'}>
				<section class="card report-section report-details" aria-labelledby="report-sales-heading">
					<h2 id="report-sales-heading">{t('تفاصيل المبيعات', 'Sales details')}</h2>
					<div class="table-scroll">
						<table>
							<thead>
								<tr>
									<th>{t('المرجع', 'Reference')}</th><th>{t('التاريخ', 'Date')}</th><th>{t('المنتج', 'Product')}</th>
									<th>{t('التصنيف الرئيسي', 'Category')}</th><th>{t('التصنيف الفرعي', 'Subcategory')}</th><th>{t('القطع', 'Items')}</th>
									<th>{t('الدفع', 'Payment')}</th><th>{t('التكلفة الأصلية', 'Original cost')}</th><th>{t('إجمالي المبيعات', 'Sales')}</th>
									{#if showRecorded}<th>{t('المسجّل', 'Recorded as')}</th>{/if}
								</tr>
							</thead>
							<tbody>
								{#each report.sales as row (row.sale_id)}
									<tr>
										<td class="mono"><bdi dir="ltr">{row.sale_number}</bdi></td>
										<td>{absoluteTime(row.occurred_at, lang())}</td>
										<td>{row.products}<small class="mono" dir="ltr">{row.skus}</small></td>
										<td>{(lang() === 'ar' ? row.root_names_ar : row.root_names_en) || t('التصنيف غير متاح', 'Classification unavailable')}</td>
										<td>{(lang() === 'ar' ? row.subcategory_names_ar : row.subcategory_names_en) || '—'}</td>
										<td><bdi class="num">{formatInt(row.items_sold)}</bdi></td>
										<td>{row.payment_methods.map(method).join(t('، ', ', '))}</td>
										{@render cost(row)}
										<td><bdi class="num">{money(row.total_minor, row.currency)}</bdi></td>
										{#if showRecorded}
											<td>{#if row.rate_microrate !== null}<bdi class="num">{money(row.original_total_minor, row.original_currency)}</bdi><small>{t('بسعر', 'at')} <bdi class="num">{formatMicrorate(row.rate_microrate)}</bdi></small>{:else}—{/if}</td>
										{/if}
									</tr>
								{/each}
							</tbody>
						</table>
					</div>
					{#if report.sales_total_count > PAGE}
						<div class="pager">
							<span class="num">{offset + 1}–{Math.min(offset + report.sales.length, report.sales_total_count)} / {report.sales_total_count}</span>
							<button type="button" class="btn-secondary btn-sm screen-only" disabled={offset === 0} onclick={() => (offset = Math.max(0, offset - PAGE))}>{t('السابق', 'Previous')}</button>
							<button type="button" class="btn-secondary btn-sm screen-only" disabled={offset + PAGE >= report.sales_total_count} onclick={() => (offset += PAGE)}>{t('التالي', 'Next')}</button>
						</div>
					{/if}
				</section>
			</div>
		{/if}
	</article>
{/if}
