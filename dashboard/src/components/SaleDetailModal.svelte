<script lang="ts">
	import { onMount } from 'svelte';
	import Modal from './ui/Modal.svelte';
	import { dashboardApi } from '../lib/api.js';
	import type { SaleDetail, SaleDetailLine } from '../lib/api.js';
	import { lang, msg, t } from '../lib/i18n.svelte.js';
	import { absoluteTime } from '../lib/time.js';
	import { formatMicrorate, formatMinor } from '../lib/money.js';

	// One sale with what each item sold for and what it cost. Costs are the
	// unit costs Retail recorded at sale time; a missing cost stays unknown.
	let { store, saleId, saleNumber, onclose }: { store: string; saleId: string; saleNumber: string; onclose: () => void } = $props();

	let sale = $state<SaleDetail | null>(null);
	let phase = $state<'loading' | 'loaded' | 'error'>('loading');

	async function load() {
		phase = 'loading';
		try {
			sale = await dashboardApi.saleDetail(store, saleId);
			phase = 'loaded';
		} catch {
			phase = 'error';
		}
	}
	onMount(() => void load());

	let currency = $derived<'EGP' | 'USD'>(sale?.currency === 'USD' ? 'USD' : 'EGP');
	const money = (minor: string) => formatMinor(minor, currency);
	// Exact minor-unit math: amounts are decimal strings, never floats.
	const minus = (a: string, b: string) => (BigInt(a) - BigInt(b)).toString();
	const sum = (values: string[]) => values.reduce((acc, v) => acc + BigInt(v), 0n).toString();

	function size(line: SaleDetailLine): string {
		return line.width_cm && line.height_cm ? `${line.width_cm}×${line.height_cm} cm` : '';
	}
	let itemsTotal = $derived(sale ? sum(sale.lines.map((l) => l.line_total_minor)) : '0');
</script>

<Modal title={msg('saleDetail.title', { number: saleNumber })} {onclose} wide closeLabel={msg('saleDetail.close')} testid="sale-detail">
	{#if phase === 'loading'}
		<p class="muted">{msg('saleDetail.loading')}</p>
	{:else if phase === 'error' || !sale}
		<p role="alert">{msg('saleDetail.failed')}</p>
		<button type="button" class="btn-secondary btn-sm" onclick={() => void load()}>{msg('saleDetail.retry')}</button>
	{:else}
		<dl class="facts">
			<div><dt>{msg('saleDetail.date')}</dt><dd dir="ltr" class="num">{absoluteTime(sale.occurred_at, lang())}</dd></div>
			<div><dt>{msg('saleDetail.cashier')}</dt><dd>{sale.cashier_name ?? '—'}</dd></div>
			<div><dt>{msg('saleDetail.shop')}</dt><dd>{t(sale.shop_name_ar, sale.shop_name_en || sale.shop_name_ar)}</dd></div>
		</dl>
		<div class="table-wrap">
			<table class="data">
				<thead>
					<tr>
						<th scope="col">{msg('saleDetail.item')}</th>
						<th scope="col" class="end">{msg('saleDetail.qty')}</th>
						<th scope="col" class="end">{msg('saleDetail.soldFor')}</th>
						<th scope="col" class="end">{msg('saleDetail.cost')}</th>
						<th scope="col" class="end">{msg('saleDetail.profit')}</th>
					</tr>
				</thead>
				<tbody>
					{#each sale.lines as line, i (i)}
						<tr data-testid="sale-line">
							<td>
								<div class="pname">{line.product_name}</div>
 {#each line.attributes??[] as a}<div class="muted sub">{lang()==='ar'?a.definition_name_ar:a.definition_name_en||a.definition_name_ar}: {lang()==='ar'?a.name_ar:a.name_en||a.name_ar}</div>{/each}
								<div class="muted sub" dir="ltr">{line.sku}{size(line) ? ` · ${size(line)}` : ''}</div>
							</td>
							<td class="num end">{line.quantity}</td>
							<td class="num end">
								<strong>{money(line.line_total_minor)}</strong>
 <div class="muted sub">{msg('salesReportView.lineContext.catalogPrice')}: {line.catalog_price_minor==null?msg('salesReportView.lineContext.notRecorded'):money(line.catalog_price_minor)}</div>
								{#if line.quantity > 1}<div class="muted sub">{msg('saleDetail.each', { amount: money(line.unit_price_minor) })}</div>{/if}
							</td>
							<td class="num end">
								{#if line.line_cost_minor !== null && line.unit_cost_minor !== null}
									{money(line.line_cost_minor)}
									{#if line.quantity > 1}<div class="muted sub">{msg('saleDetail.each', { amount: money(line.unit_cost_minor) })}</div>{/if}
								{:else}<span class="muted">{msg('saleDetail.unknown')}</span>{/if}
							</td>
							<td class="num end">
								{#if line.line_cost_minor !== null}
									{@const profit = minus(line.line_total_minor, line.line_cost_minor)}
									<span class:loss={profit.startsWith('-')}>{money(profit)}</span>
								{:else}—{/if}
							</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
		<p class="muted note">{msg('salesReportView.lineContext.costHint')}</p>
		<div class="totals">
			<dl>
				<div><dt>{msg('saleDetail.subtotal')}</dt><dd class="num">{money(sale.subtotal_minor)}</dd></div>
				{#if sale.discount_minor !== '0'}<div><dt>{msg('saleDetail.discount')}</dt><dd class="num">−{money(sale.discount_minor)}</dd></div>{/if}
				{#if sale.tax_minor !== '0'}<div><dt>{msg('saleDetail.tax')}</dt><dd class="num">{money(sale.tax_minor)}</dd></div>{/if}
				<div class="grand"><dt>{msg('saleDetail.total')}</dt><dd class="num">{money(sale.total_minor)}</dd></div>
			</dl>
			<dl class="profitbox" data-testid="sale-profit">
				<div><dt>{msg('saleDetail.itemsSold')}</dt><dd class="num">{money(itemsTotal)}</dd></div>
				<div><dt>{msg('saleDetail.totalCost')}</dt><dd class="num">{money(sale.cost_minor)}</dd></div>
				{#if sale.cost_complete}
					{@const profit = minus(itemsTotal, sale.cost_minor)}
					<div class="grand"><dt>{msg('saleDetail.grossProfit')}</dt><dd class="num" class:loss={profit.startsWith('-')}>{money(profit)}</dd></div>
				{:else}
					<p class="muted note">{msg('saleDetail.costIncomplete')}</p>
				{/if}
			</dl>
		</div>
		{#if sale.currency === 'USD' && sale.fx_rate_microrate}
			<p class="muted note" dir="ltr">{msg('saleDetail.fxRate', { rate: formatMicrorate(sale.fx_rate_microrate) })}</p>
		{/if}
	{/if}
</Modal>

<style>
	.facts {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
		gap: 8px;
		margin: 0 0 12px;
	}
	.facts div {
		background: var(--surface-muted);
		border-radius: var(--radius-control);
		padding: 6px 10px;
	}
	dt {
		font-size: 0.72rem;
		color: var(--text-muted);
	}
	dd {
		margin: 0;
		font-weight: 700;
	}
	.table-wrap {
		overflow-x: auto;
	}
	.end {
		text-align: end;
	}
	.pname {
		font-weight: 600;
	}
	.sub {
		font-size: 0.72rem;
		font-weight: 400;
	}
	.loss {
		color: var(--danger);
	}
	.totals {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
		gap: 12px;
		margin-top: 12px;
	}
	.totals dl {
		margin: 0;
		border: 1px solid var(--border);
		border-radius: var(--radius-card);
		padding: 8px 12px;
	}
	.totals dl > div {
		display: flex;
		justify-content: space-between;
		gap: 8px;
		padding: 3px 0;
	}
	.totals dt {
		font-size: 0.82rem;
		color: var(--text);
	}
	.totals .grand {
		border-top: 1px solid var(--border);
		margin-top: 4px;
		padding-top: 6px;
	}
	.profitbox {
		background: #f6fbf8;
	}
	.note {
		margin: 6px 0 0;
		font-size: 0.76rem;
	}
</style>
