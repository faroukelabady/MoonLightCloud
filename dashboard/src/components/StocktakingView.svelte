<script lang="ts">
	import { ApiError } from '../lib/http.js';
	import { reportsApi, type StockReport, type StockRow } from '../lib/reportsApi.js';
	import { formatInt, formatMinor } from '../lib/money.js';
	import { absoluteTime } from '../lib/time.js';
	import { lang, t } from '../lib/i18n.svelte.js';
	import { NISAB_GOLD_GRAMS, calculateZakat, parseMajor, type ZakatYear } from '../lib/zakat.js';
	import ReportLetterhead from './ReportLetterhead.svelte';

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
						? t('اختر متجراً من أعلى الصفحة.', 'Choose a Store at the top of the page.')
						: err instanceof ApiError && err.code === 'FORBIDDEN'
						? t('ليست لديك صلاحية عرض التقارير.', 'You do not have permission to view reports.')
						: t('تعذر تحميل الجرد. حاول مرة أخرى.', 'The stocktaking could not be loaded. Try again.');
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
	<p class="muted">{t('المخزون حسب آخر مزامنة من MoonLightRetail، مقيّماً بسعر التكلفة وسعر البيع الحالي بالجنيه المصري.', 'Stock on hand as last synced from MoonLightRetail, valued at cost and at the current EGP selling price.')}</p>
	<button type="button" class="btn-ghost" disabled={loading} onclick={() => (refresh += 1)}>{t('تحديث', 'Refresh')}</button>
</div>
{#if error}<div class="report-alert screen-only" role="alert">{error}</div>{/if}
{#if loading && !report}<div class="card muted" role="status">{t('جارٍ تحميل الجرد…', 'Loading stocktaking…')}</div>
{:else if report && !error}
	<article id="report-printable" class="report-surface stocktaking" class:stale={loading} aria-label={t('جرد المخزون والزكاة', 'Stocktaking & Zakat')}>
		<ReportLetterhead title={t('جرد المخزون والزكاة', 'Stocktaking & Zakat')} shop={report.shop} {storeName}>
			<div><dt>{t('منتجات في المخزون', 'Products in stock')}</dt><dd><bdi class="num">{formatInt(report.totals.products_in_stock)}</bdi></dd></div>
			<div><dt>{t('أكواد في المخزون', 'SKUs in stock')}</dt><dd><bdi class="num">{formatInt(report.totals.variants_in_stock)}</bdi></dd></div>
			<div><dt>{t('القطع الموجودة', 'Units on hand')}</dt><dd><bdi class="num">{formatInt(report.totals.units)}</bdi></dd></div>
			<div><dt>{t('وقت الإنشاء', 'Generated')}</dt><dd>{absoluteTime(report.generated_at, lang())}</dd></div>
		</ReportLetterhead>

		<section class="report-money-grid" aria-label={t('قيمة المخزون', 'Inventory value')}>
			<div class="card report-money-detail">
				<h3>{t('قيمة المخزون', 'Inventory value')}</h3>
				<table>
					<thead><tr><th>{t('الخطوة', 'Step')}</th><th>EGP</th></tr></thead>
					<tbody>
						<tr><th scope="row">{t('القيمة بسعر التكلفة', 'Value at cost')}</th><td><bdi class="num">{egp(report.totals.cost_minor)}</bdi></td></tr>
						<tr><th scope="row">{t('القيمة بسعر البيع', 'Value at selling price')}</th><td><bdi class="num">{egp(report.totals.retail_minor)}</bdi></td></tr>
						<tr class="report-total-row"><th scope="row">{t('الربح المتوقع عند بيع الكل', 'Expected margin if all sold')}</th><td><bdi class="num">{egp(margin)}</bdi></td></tr>
					</tbody>
				</table>
				<p class="report-note">{t('يُستخدم سعر التكلفة والبيع الخاص بكل كود إن وُجد، وإلا فسعر المنتج. لا تدخل أسعار الدولار في الحساب.', 'Each SKU uses its own cost and price when set, otherwise the product cost and price. USD prices are not included.')}</p>
			</div>
			<div class="card report-money-detail">
				<h3>{t('بيانات تحتاج مراجعة', 'Data to check')}</h3>
				<table>
					<thead><tr><th>{t('الخطوة', 'Step')}</th><th>{t('القطع', 'Units')}</th></tr></thead>
					<tbody>
						<tr><th scope="row">{t('قطع بلا سعر تكلفة', 'Units without a cost')}</th><td><bdi class="num">{formatInt(report.totals.units_without_cost)}</bdi></td></tr>
						<tr><th scope="row">{t('قطع بلا سعر بيع بالجنيه', 'Units without an EGP price')}</th><td><bdi class="num">{formatInt(report.totals.units_without_price)}</bdi></td></tr>
						<tr><th scope="row">{t('قطع لمنتجات أو أكواد غير نشطة', 'Units of inactive products or SKUs')}</th><td><bdi class="num">{formatInt(report.totals.inactive_units)}</bdi></td></tr>
					</tbody>
				</table>
				{#if report.totals.units_without_cost > 0 || report.totals.units_without_price > 0}
					<p class="report-warning">{t('بعض المخزون ليس له سعر تكلفة أو سعر بيع بالجنيه، لذلك لا تدخل قيمته في الإجمالي.', 'Some stock has no cost or EGP price, so its value is missing from the totals.')}</p>
				{/if}
			</div>
		</section>

		<section class="card report-section" aria-labelledby="zakat-heading">
			<h2 id="zakat-heading">{t('زكاة عروض التجارة', 'Zakat on trade goods')}</h2>
			<p>{t('تجب زكاة بضاعة المحل إذا حال عليها الحول وبلغ المجموع الخاضع للزكاة النصاب. أدخل سعر الذهب اليوم وأموال المحل الأخرى لحسابها.', 'Zakat on shop stock is due once a full year passes while the zakatable total stays at or above nisab. Enter today’s gold price and the shop’s other money to calculate it.')}</p>
			<div class="report-filter-grid screen-only">
				<label>
					<span>{t('تقييم البضاعة', 'Value stock at')}</span>
					<select bind:value={basis} data-testid="zakat-basis">
						<option value="retail">{t('بسعر البيع (القيمة السوقية)', 'Selling price (market value)')}</option>
						<option value="cost">{t('بسعر التكلفة', 'Cost price')}</option>
					</select>
				</label>
				<label>
					<span>{t('سنة الزكاة', 'Zakat year')}</span>
					<select bind:value={year}>
						<option value="hijri">{t('سنة هجرية — 2.5%', 'Hijri year — 2.5%')}</option>
						<option value="gregorian">{t('سنة ميلادية — 2.577%', 'Gregorian year — 2.577%')}</option>
					</select>
				</label>
				<label><span>{t('سعر جرام الذهب (ج.م)', 'Gold price per gram (EGP)')}</span><input inputmode="decimal" autocomplete="off" bind:value={goldGram} data-testid="zakat-gold" /></label>
				<label><span>{t('النقدية وأرصدة البنوك (ج.م)', 'Cash and bank balances (EGP)')}</span><input inputmode="decimal" autocomplete="off" bind:value={cash} /></label>
				<label><span>{t('ديون لك مرجوّ تحصيلها (ج.م)', 'Receivables you expect to collect (EGP)')}</span><input inputmode="decimal" autocomplete="off" bind:value={receivables} /></label>
				<label><span>{t('ديون عليك مستحقة (ج.م)', 'Debts due (EGP)')}</span><input inputmode="decimal" autocomplete="off" bind:value={debts} /></label>
			</div>
			<p class="report-note screen-only">{t('اترك الحقل فارغاً للصفر. هذه الأرقام لا تُحفظ ولا تُرسل.', 'Leave a field empty for zero. These figures are neither saved nor sent.')}</p>
			{#if invalidInput}<p class="field-error" role="alert">{t('أدخل مبالغ موجبة بمنزلتين عشريتين كحد أقصى.', 'Enter positive amounts with at most two decimals.')}</p>{/if}
			{#if zakat}
				<div class="table-scroll">
					<table aria-label={t('زكاة عروض التجارة', 'Zakat on trade goods')}>
						<thead><tr><th>{t('الخطوة', 'Step')}</th><th>EGP</th></tr></thead>
						<tbody>
							<tr><th scope="row">{basis === 'retail' ? t('البضاعة بسعر البيع', 'Stock at selling price') : t('البضاعة بسعر التكلفة', 'Stock at cost')}</th><td><bdi class="num">{egp(inventory)}</bdi></td></tr>
							<tr><th scope="row">{t('النقدية وأرصدة البنوك', 'Cash and bank balances')}</th><td><bdi class="num">{egp(amounts.cash ?? 0n)}</bdi></td></tr>
							<tr><th scope="row">{t('ديون لك مرجوّ تحصيلها', 'Receivables you expect to collect')}</th><td><bdi class="num">{egp(amounts.receivables ?? 0n)}</bdi></td></tr>
							<tr><th scope="row">{t('ديون عليك مستحقة', 'Debts due')}</th><td><bdi class="num">− {egp(amounts.debts ?? 0n)}</bdi></td></tr>
							<tr class="report-total-row"><th scope="row">{t('المجموع الخاضع للزكاة', 'Zakatable total')}</th><td><bdi class="num">{egp(zakat.base)}</bdi></td></tr>
							<tr><th scope="row">{t(`النصاب (${NISAB_GOLD_GRAMS} جرام ذهب)`, `Nisab (${NISAB_GOLD_GRAMS} g of gold)`)}</th><td><bdi class="num">{zakat.nisab == null ? t('أدخل سعر الذهب', 'Enter the gold price') : egp(zakat.nisab)}</bdi></td></tr>
							<tr class="report-total-row"><th scope="row">{year === 'hijri' ? t('الزكاة المستحقة (2.5%)', 'Zakat due (2.5%)') : t('الزكاة المستحقة (2.577%)', 'Zakat due (2.577%)')}</th><td><bdi class="num" data-testid="zakat-due">{egp(zakat.zakat)}</bdi></td></tr>
						</tbody>
					</table>
				</div>
				<p class="report-warning" role="status">
					{#if zakat.reachesNisab === null}{t('أدخل سعر جرام الذهب لمقارنة المجموع بالنصاب.', 'Enter the gold price per gram to check the total against nisab.')}
					{:else if zakat.reachesNisab}{t('بلغ المجموع النصاب، فتجب الزكاة إذا حال الحول.', 'The zakatable total reaches nisab, so zakat is due if a full year has passed.')}
					{:else}{t('المجموع أقل من النصاب، فلا زكاة عليه.', 'The zakatable total is below nisab, so no zakat is due on it.')}{/if}
				</p>
			{/if}
			<ul class="stocktaking-notes">
				<li>{t('تجب الزكاة بمرور عام هجري كامل من يوم بلوغ المال النصاب.', 'Zakat is due when a full lunar year passes from the day the total first reached nisab.')}</li>
				<li>{t('يرى جمهور العلماء تقويم عروض التجارة بقيمتها السوقية يوم وجوب الزكاة، ويُعرض سعر التكلفة للاسترشاد.', 'Most scholars value trade goods at their market value on the zakat date; cost price is shown for reference.')}</li>
				<li>{t('أثاث المحل ومعداته ومبناه ليست من عروض التجارة ولا تدخل في الحساب.', 'Shop fixtures, equipment and premises are not zakatable trade goods and are not included.')}</li>
				<li>{t('هذا تقدير للمساعدة في الاستعداد، فاستوثق من المبلغ النهائي مع عالم موثوق.', 'This is an estimate to help you prepare. Confirm the final amount with a trusted scholar.')}</li>
			</ul>
		</section>

		<section class="card report-section" aria-labelledby="stock-categories-heading">
			<h2 id="stock-categories-heading">{t('المخزون حسب التصنيف', 'Stock by category')}</h2>
			<div class="table-scroll">
				<table>
					<thead><tr><th>{t('التصنيف الرئيسي', 'Category')}</th><th>{t('منتجات في المخزون', 'Products in stock')}</th><th>{t('القطع الموجودة', 'Units on hand')}</th><th>{t('القيمة بسعر التكلفة', 'Value at cost')}</th><th>{t('القيمة بسعر البيع', 'Value at selling price')}</th></tr></thead>
					<tbody>
						{#each report.categories as row (row.id)}
							<tr><th scope="row">{pick(row.name_ar, row.name_en)}</th><td><bdi class="num">{formatInt(row.products_in_stock)}</bdi></td><td><bdi class="num">{formatInt(row.units)}</bdi></td><td><bdi class="num">{egp(row.cost_minor)}</bdi></td><td><bdi class="num">{egp(row.retail_minor)}</bdi></td></tr>
						{/each}
						<tr class="report-total-row"><th scope="row">{t('الإجمالي', 'Total')}</th><td><bdi class="num">{formatInt(report.totals.products_in_stock)}</bdi></td><td><bdi class="num">{formatInt(report.totals.units)}</bdi></td><td><bdi class="num">{egp(report.totals.cost_minor)}</bdi></td><td><bdi class="num">{egp(report.totals.retail_minor)}</bdi></td></tr>
					</tbody>
				</table>
			</div>
		</section>

		<section class="card report-section stocktaking-sheet" aria-labelledby="stock-sheet-heading">
			<h2 id="stock-sheet-heading">{t('كشف الجرد', 'Count sheet')}</h2>
			<p>{t('اطبع الكشف، واعدد كل رف، واكتب الكمية الفعلية. صحّح الفروق من شاشة المخزون في MoonLightRetail.', 'Print this sheet, count each shelf and write the counted quantity. Correct differences in MoonLightRetail’s Inventory screen.')}</p>
			<div class="table-scroll">
				<table>
					<thead>
						<tr>
							<th>SKU</th><th>{t('الصنف', 'Item')}</th><th>{t('الكمية في النظام', 'System quantity')}</th><th>{t('تكلفة القطعة', 'Unit cost')}</th>
							<th>{t('القيمة بسعر التكلفة', 'Value at cost')}</th><th>{t('سعر القطعة', 'Unit price')}</th><th>{t('القيمة بسعر البيع', 'Value at selling price')}</th><th>{t('الكمية الفعلية', 'Counted')}</th>
						</tr>
					</thead>
					<tbody>
						{#each report.items as item (item.variant_id)}
							<tr class:stocktaking-inactive={!item.active}>
								<td class="mono" dir="ltr">{item.sku}</td>
								<th scope="row">{itemName(item)}{#if !item.active}<small>{t('غير نشط', 'Inactive')}</small>{/if}</th>
								<td><bdi class="num">{formatInt(item.quantity)}</bdi></td>
								<td><bdi class="num">{egp(item.unit_cost_minor)}</bdi></td>
								<td><bdi class="num">{egp(item.cost_minor)}</bdi></td>
								<td><bdi class="num">{egp(item.unit_price_minor)}</bdi></td>
								<td><bdi class="num">{egp(item.retail_minor)}</bdi></td>
								<td class="stocktaking-count-cell"></td>
							</tr>
						{:else}
							<tr><td colspan="8" class="muted">{t('لا يوجد مخزون متزامن لهذا النطاق بعد.', 'No synced stock for this scope yet.')}</td></tr>
						{/each}
					</tbody>
				</table>
			</div>
		</section>
	</article>
{/if}
