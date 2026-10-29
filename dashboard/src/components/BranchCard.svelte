<script lang="ts">
	import { arabic, msg, t } from '../lib/i18n.svelte.js';
	import Card from './Card.svelte';
	import Skeleton from './Skeleton.svelte';
	import EmptyState from './EmptyState.svelte';
	import WidgetError from './WidgetError.svelte';
	import OnlineComparison from './OnlineComparison.svelte';
	import { formatMinor, formatInt } from '../lib/money.js';
	import type { BranchRow, OrderAnalyticsResponse, OverviewResponse } from '../lib/api.js';

	type State = 'loading' | 'loaded' | 'empty' | 'error';
	type Mode = 'all' | 'EGP' | 'USD';
	// One card for where sales happen: a table of branches and the online
	// store (sales per currency, transactions, units), then the comparison
	// of finalized sales with operational online orders. The currency mode
	// comes from the top bar; nothing is converted.
	let {
		rows,
		status,
		errStatus,
		onretry,
		mode = 'all',
		online = null,
		retail = null,
		onlineStatus = 'empty',
		onlineErr = null
	}: {
		rows: BranchRow[];
		status: State;
		errStatus: number | null;
		onretry: () => void;
		mode?: Mode;
		online?: OrderAnalyticsResponse | null;
		retail?: OverviewResponse | null;
		onlineStatus?: State;
		onlineErr?: number | null;
	} = $props();

	type Currency = 'EGP' | 'USD';
	let currencies = $derived<Currency[]>(mode === 'all' ? ['EGP', 'USD'] : [mode]);

	// The API returns one row per branch and currency; the table shows one
	// row per branch. Counts add up across the currencies shown.
	type Branch = { key: string; first: BranchRow; byCurrency: Partial<Record<Currency, BranchRow>>; transactions: number; units: number };
	let branches = $derived.by(() => {
		const out: Branch[] = [];
		const index = new Map<string, Branch>();
		for (const r of rows) {
			const cur: Currency = r.currency === 'USD' ? 'USD' : 'EGP';
			if (!currencies.includes(cur)) continue;
			const key = [r.shop_name_ar, r.shop_name_en, r.shop_phone, r.channel].join('\u0000');
			let branch = index.get(key);
			if (!branch) {
				branch = { key, first: r, byCurrency: {}, transactions: 0, units: 0 };
				index.set(key, branch);
				out.push(branch);
			}
			branch.byCurrency[cur] = r;
			branch.transactions += r.transactions;
			branch.units += r.units;
		}
		return out;
	});
</script>

<Card ar="المبيعات حسب الفرع والمتجر الإلكتروني" en="Sales by branch and online store">
	{#if status === 'loading'}
		<Skeleton />
	{:else if status === 'error'}
		<WidgetError status={errStatus} {onretry} />
	{:else if branches.length === 0}
		<EmptyState ar="لا توجد فروع في هذه الفترة" en="No branches in this period" />
	{:else}
		<div class="table-wrap">
			<table class="branches" data-testid="branch-table">
				<thead>
					<tr>
						<th scope="col">{msg('branchCard.branchChannel')}{#if arabic()}<span class="sub-en">Branch / Channel</span>{/if}</th>
						{#each currencies as cur}
							<th scope="col" class="num">{t(`المبيعات ${cur}`, `Sales ${cur}`)}</th>
						{/each}
						<th scope="col" class="num">{msg('branchCard.transactions')}{#if arabic()}<span class="sub-en">Transactions</span>{/if}</th>
						<th scope="col" class="num">{msg('branchCard.units')}{#if arabic()}<span class="sub-en">Units</span>{/if}</th>
					</tr>
				</thead>
				<tbody>
					{#each branches as b (b.key)}
						<tr>
							<th scope="row">
								<div class="lname">{t(b.first.shop_name_ar, b.first.shop_name_en || b.first.shop_name_ar)}</div>
								<div class="muted lsub">{arabic() ? `${b.first.shop_name_en} · ${b.first.channel}` : b.first.channel}</div>
							</th>
							{#each currencies as cur}
								{@const r = b.byCurrency[cur]}
								<td class="num">
									{#if r}
										<div class="val">{formatMinor(r.sales_total_minor, cur)}</div>
										{#if r.refund_total_minor !== '0'}<div class="refund">{msg('branchCard.refunds')} {formatMinor(r.refund_total_minor, cur)}</div>{/if}
									{:else}
										<span class="muted">—</span>
									{/if}
								</td>
							{/each}
							<td class="num val">{formatInt(b.transactions)}</td>
							<td class="num val">{formatInt(b.units)}</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	{/if}
	<div class="online">
		<OnlineComparison bare {online} {retail} status={onlineStatus} errStatus={onlineErr} currency={mode} {onretry} />
	</div>
</Card>

<style>
	.table-wrap {
		overflow-x: auto;
	}
	.branches {
		width: 100%;
		border-collapse: collapse;
	}
	.branches th,
	.branches td {
		padding: 6px;
		border-bottom: 1px solid var(--border);
		vertical-align: top;
	}
	.branches thead th {
		font-size: 0.72rem;
		font-weight: 600;
		color: var(--text-muted);
		text-align: start;
		vertical-align: bottom;
		white-space: nowrap;
	}
	.branches .num {
		text-align: end;
	}
	.branches tbody th {
		text-align: start;
		font-weight: 400;
	}
	.sub-en {
		display: block;
		font-size: 0.66rem;
		font-weight: 400;
	}
	.lname {
		font-size: 0.82rem;
		font-weight: 600;
	}
	.lsub {
		font-size: 0.7rem;
	}
	.val {
		font-weight: 700;
		font-size: 0.82rem;
		font-variant-numeric: tabular-nums;
		white-space: nowrap;
	}
	.refund {
		font-size: 0.68rem;
		color: var(--danger, #b42318);
		white-space: nowrap;
	}
	.online {
		margin-top: 12px;
		padding-top: 10px;
		border-top: 1px solid var(--border);
	}
</style>
