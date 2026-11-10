<script lang="ts" module>
	import { formatInt } from '../lib/money.js';
	import type { OverviewResponse } from '../lib/api.js';

	// Shared with the Sales card's summary strip. Sales and returned units
	// follow the active currency mode (each mode's own counts, never mixed).
	// Return transactions have no per-currency count, so they stay
	// whole-period and say so in a currency mode.
	export function activityCounts(data: OverviewResponse | null, mode: 'all' | 'EGP' | 'USD') {
		if (!data) return { sales: { transactions: '—', units: '—' }, returns: { transactions: '—', units: '—' } };
		const avg = mode === 'EGP' ? data.averages.egp : data.averages.usd;
		const sales =
			mode === 'all'
				? { transactions: formatInt(data.summary.transaction_count), units: formatInt(data.summary.units_sold) }
				: { transactions: formatInt(avg.transactions), units: formatInt(avg.units) };
		const returnedUnits = mode === 'all' ? data.summary.units_returned : (data.summary.currency_totals.find((b) => b.currency === mode)?.returned_units ?? 0);
		return { sales, returns: { transactions: formatInt(data.summary.return_transaction_count), units: formatInt(returnedUnits) } };
	}
</script>

<script lang="ts">
	import { msg } from '../lib/i18n.svelte.js';

	// Sale and return transactions and units in one table (see activityCounts).
	let { data, mode }: { data: OverviewResponse | null; mode: 'all' | 'EGP' | 'USD' } = $props();

	let counts = $derived(activityCounts(data, mode));
	let sales = $derived(counts.sales);
	let returns = $derived(counts.returns);
	let scope = $derived(mode === 'all' ? msg('countsTable.scopeAll') : `${msg('countsTable.scope')}: ${mode}`);
</script>

<div class="counts">
	<table class="totals" data-testid="activity-totals" aria-label={msg('countsTable.transactionsAndUnits')}>
		<thead>
			<tr>
				<td></td>
				<th scope="col">{msg('countsTable.sales')}</th>
				<th scope="col">{msg('countsTable.returns')}</th>
			</tr>
		</thead>
		<tbody>
			<tr>
				<th scope="row">{msg('countsTable.transactions')}</th>
				<td class="num">{sales.transactions}</td>
				<td class="num ret">{returns.transactions}{#if mode !== 'all'}<sup class="note-mark" aria-hidden="true">*</sup>{/if}</td>
			</tr>
			<tr>
				<th scope="row">{msg('countsTable.units')}</th>
				<td class="num">{sales.units}</td>
				<td class="num ret">{returns.units}</td>
			</tr>
		</tbody>
	</table>
	<div class="muted ctx">{scope}</div>
	{#if mode !== 'all'}<div class="muted ctx" role="note">* {msg('countsTable.returnTransactionsCountEveryCurrency')}</div>{/if}
</div>

<style>
	.totals {
		width: 100%;
		border-collapse: collapse;
		table-layout: fixed;
	}
	.totals th,
	.totals td {
		padding: 6px 6px;
		text-align: center;
		border-bottom: 1px solid var(--border);
	}
	.totals tbody tr:last-child > * {
		border-bottom: 0;
	}
	.totals thead th {
		font-size: 0.78rem;
		font-weight: 600;
		color: var(--text-muted);
	}
	.totals tbody th {
		text-align: start;
		font-size: 0.82rem;
		font-weight: 600;
	}
	.totals td {
		font-size: 1.2rem;
		font-weight: 700;
		font-variant-numeric: tabular-nums;
	}
	.totals td.ret {
		color: var(--text-muted);
	}
	.note-mark {
		font-size: 0.7rem;
	}
	.ctx {
		font-size: 0.76rem;
		margin-top: 4px;
	}
</style>
