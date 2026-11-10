<script lang="ts">
	import { msg, t } from '../lib/i18n.svelte.js';
	import Card from './Card.svelte';
	import Skeleton from './Skeleton.svelte';
	import EmptyState from './EmptyState.svelte';
	import WidgetError from './WidgetError.svelte';
	import { activityCounts } from './CountsTable.svelte';
	import { formatMinor } from '../lib/money.js';
	import type { OverviewResponse } from '../lib/api.js';

	// One bordered sheet for the period's sales: a summary strip (net sales,
	// net profit, transactions and units with their returns) above the money
	// table (per recorded currency, plus the EGP total with every USD sale and
	// refund converted at its own rate). The currency mode comes from the top
	// bar. All money stays string-exact.
	let {
		data,
		mode,
		status,
		errStatus,
		onretry
	}: {
		data: OverviewResponse | null;
		mode: 'all' | 'EGP' | 'USD';
		status: 'loading' | 'loaded' | 'empty' | 'error';
		errStatus: number | null;
		onretry: () => void;
	} = $props();

	type Column = {
		key: string;
		label: [string, string];
		currency: 'EGP' | 'USD';
		sales: string;
		refunds: string;
		net: string;
		tax?: string;
		cost?: string;
		profit?: string;
		average: string;
		total?: boolean;
	};

	let columns = $derived.by((): Column[] => {
		if (!data) return [];
		const native = (cur: 'EGP' | 'USD'): Column => {
			const b = data.summary.currency_totals.find((x) => x.currency === cur);
			const p = data.profit?.by_currency.find((x) => x.currency === cur);
			const avg = cur === 'EGP' ? data.averages.egp : data.averages.usd;
			return {
				key: cur, label: [cur === 'EGP' ? 'جنيه' : 'دولار', cur], currency: cur,
				sales: b?.sales_total_minor ?? '0', refunds: b?.refund_total_minor ?? '0', net: b?.net_sales_minor ?? '0',
				tax: p?.net_tax_minor, cost: p?.net_cost_minor, profit: p?.profit_minor, average: avg?.average_minor ?? '0'
			};
		};
		if (mode !== 'all') return [native(mode)];
		const hasUSD = data.summary.currency_totals.some((b) => b.currency === 'USD');
		if (!hasUSD) return [native('EGP')];
		const egp = data.profit?.egp;
		return [
			native('EGP'),
			native('USD'),
			{
				key: 'total', label: ['الإجمالي بالجنيه', 'Total in EGP'], currency: 'EGP', total: true,
				sales: data.normalized.normalized_total_minor, refunds: data.normalized.normalized_refund_minor,
				net: data.normalized.normalized_net_minor, tax: egp?.net_tax_minor, cost: egp?.net_cost_minor,
				profit: egp?.profit_minor, average: data.averages.all.average_minor
			}
		];
	});
	// The headline column: the total when there is one, else the only column.
	let lead = $derived(columns[columns.length - 1]);
	let showTax = $derived(columns.some((c) => c.tax !== undefined && c.tax !== '0'));
	let hasProfit = $derived(columns.every((c) => c.profit !== undefined));

	type Row = { key: 'sales' | 'refunds' | 'net' | 'tax' | 'cost' | 'profit' | 'average'; ar: string; en: string; strong?: boolean; less?: boolean };
	let rows = $derived(
		[
			{ key: 'sales', ar: 'المبيعات', en: 'Sales' },
			{ key: 'refunds', ar: 'المرتجعات', en: 'Refunds', less: true },
			{ key: 'net', ar: 'صافي المبيعات', en: 'Net sales', strong: true },
			...(hasProfit && showTax ? [{ key: 'tax', ar: 'الضريبة (صافي)', en: 'Tax (net)', less: true }] : []),
			...(hasProfit ? [{ key: 'cost', ar: 'تكلفة البضاعة (صافي)', en: 'Cost of goods (net)', less: true }] : []),
			...(hasProfit ? [{ key: 'profit', ar: 'صافي الربح', en: 'Net profit', strong: true }] : []),
			{ key: 'average', ar: 'متوسط قيمة العملية', en: 'Average sale' }
		] as Row[]
	);
	let counts = $derived(activityCounts(data, mode));
	function cell(c: Column, key: Row['key']): string {
		const v = c[key];
		return v === undefined ? '—' : formatMinor(v, c.currency);
	}
</script>

<Card ar="المبيعات والربح" en="Sales and profit">
	{#if status === 'loading'}
		<Skeleton />
	{:else if status === 'error'}
		<WidgetError status={errStatus} {onretry} />
	{:else if !data || (data.summary.transaction_count === 0 && data.summary.return_transaction_count === 0)}
		<EmptyState ar="لا توجد مبيعات في هذه الفترة" en="No sales in this period" />
	{:else}
		<div class="sheet">
			<div class="strip" data-testid="sales-summary">
				<div class="tile lead">
					<span class="tile-label">{msg('salesCard.netSales')}</span>
					<span class="tile-value num" data-testid="net-sales">{formatMinor(lead.net, lead.currency)}</span>
				</div>
				{#if hasProfit}
					<div class="tile lead">
						<span class="tile-label">{msg('salesCard.netProfit')}</span>
						<span class="tile-value num profit" class:loss={lead.profit?.startsWith('-')} data-testid="net-profit">{cell(lead, 'profit')}</span>
					</div>
				{/if}
				<div class="tile" data-testid="sales-transactions">
					<span class="tile-label">{msg('countsTable.transactions')}</span>
					<span class="tile-value num">{counts.sales.transactions}</span>
					<span class="tile-sub num">{counts.returns.transactions} {msg('countsTable.returnsSuffix')}{#if mode !== 'all'}<sup aria-hidden="true">*</sup>{/if}</span>
				</div>
				<div class="tile" data-testid="sales-units">
					<span class="tile-label">{msg('countsTable.units')}</span>
					<span class="tile-value num">{counts.sales.units}</span>
					<span class="tile-sub num">{counts.returns.units} {msg('countsTable.returnedSuffix')}</span>
				</div>
			</div>
			<table class="money" data-testid="sales-money" aria-label={msg('salesCard.salesAndProfit')}>
				<thead>
					<tr>
						<td></td>
						{#each columns as c (c.key)}
							<th scope="col" class:total={c.total}>{t(...c.label)}</th>
						{/each}
					</tr>
				</thead>
				<tbody>
					{#each rows as r (r.key)}
						<tr class:strong={r.strong}>
							<th scope="row">{r.less ? '− ' : ''}{t(r.ar, r.en)}</th>
							{#each columns as c (c.key)}
								<td class="num" class:total={c.total} class:refund={r.key === 'refunds'} data-testid={r.key === 'average' && c === lead ? 'average-value' : undefined}>{cell(c, r.key)}</td>
							{/each}
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
		<div class="muted notes">
			{#if mode !== 'all'}
				<span role="note">* {msg('countsTable.returnTransactionsCountEveryCurrency')}</span>
			{/if}
			{#if columns.some((c) => c.total)}
				<span>{msg('salesCard.totalInEgpConvertsEachUsd')}</span>
			{/if}
			{#if hasProfit}
				<span>{msg('salesCard.netProfitNetSalesTaxCost')}</span>
			{/if}
			{#if mode === 'all' && data.fx.has_usd}
				<span>
					{msg('salesCard.latestRateOnASale')}: <bdi dir="ltr">1 USD = {data.fx.latest_rate ?? '?'} EGP</bdi>
					{#if data.fx.multiple_rates_used}<span class="badge warn">{msg('salesCard.multipleHistoricalRatesUsed')}</span>{/if}
				</span>
			{/if}
		</div>
	{/if}
</Card>

<style>
	/* One bordered sheet: the summary strip and the money table share the
	   frame, separated by rules, so the figures read as a single statement. */
	.sheet {
		border: 1px solid var(--border);
		border-radius: 10px;
		overflow: hidden;
		background: var(--surface);
	}
	.strip {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(120px, 1fr));
		border-bottom: 1px solid var(--border);
		background: var(--surface-muted);
	}
	.tile {
		display: flex;
		flex-direction: column;
		gap: 2px;
		padding: 10px 12px;
		border-inline-end: 1px solid var(--border);
		min-width: 0;
	}
	.tile:last-child {
		border-inline-end: 0;
	}
	.tile-label {
		font-size: 0.74rem;
		color: var(--text-muted);
		font-weight: 600;
	}
	.tile-value {
		font-size: 1.15rem;
		font-weight: 700;
		line-height: 1.2;
		white-space: nowrap;
	}
	.tile.lead .tile-value {
		font-size: 1.3rem;
	}
	.tile-value.profit {
		color: var(--success);
	}
	.tile-value.loss {
		color: var(--danger);
	}
	.tile-sub {
		font-size: 0.74rem;
		color: var(--text-muted);
	}
	.money {
		width: 100%;
		border-collapse: collapse;
		font-size: 0.82rem;
	}
	.money th,
	.money td {
		padding: 6px 10px;
		line-height: 1.25;
		border-bottom: 1px solid var(--border);
		text-align: end;
		white-space: nowrap;
	}
	.money th + th,
	.money td,
	.money thead td + th {
		border-inline-start: 1px solid var(--border);
	}
	.money thead th {
		font-size: 0.74rem;
		color: var(--text-muted);
		font-weight: 600;
		vertical-align: bottom;
		background: var(--surface);
	}
	.money tbody th {
		text-align: start;
		font-weight: 500;
		white-space: normal;
	}
	.money tbody tr:nth-child(even) > * {
		background: color-mix(in srgb, var(--surface-muted) 55%, transparent);
	}
	.money tr.strong th,
	.money tr.strong td {
		font-weight: 700;
		background: color-mix(in srgb, var(--primary) 6%, var(--surface));
	}
	.money td.total,
	.money th.total {
		background: var(--surface-muted);
	}
	.money td.refund {
		color: var(--danger, #b42318);
	}
	.money tbody tr:last-child > * {
		border-bottom: 0;
	}
	.notes {
		display: flex;
		flex-wrap: wrap;
		gap: 2px 10px;
		margin-top: 6px;
		font-size: 0.7rem;
		line-height: 1.3;
	}
</style>
