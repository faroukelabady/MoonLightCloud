<script lang="ts">
	import { arabic, t } from '../lib/i18n.svelte.js';
	import Card from './Card.svelte';
	import Segmented from './Segmented.svelte';
	import Skeleton from './Skeleton.svelte';
	import EmptyState from './EmptyState.svelte';
	import WidgetError from './WidgetError.svelte';
	import { formatMinor, formatInt } from '../lib/money.js';
	import type { BranchRow } from '../lib/api.js';

	let { rows, status, errStatus, onretry }: { rows: BranchRow[]; status: 'loading' | 'loaded' | 'empty' | 'error'; errStatus: number | null; onretry: () => void } = $props();

	type Currency = 'EGP' | 'USD';
	type Metric = 'value' | 'transactions' | 'units';
	let metric: Metric = $state('value');

	// The API returns one row per branch and currency; the card shows one
	// row per branch with an EGP and a USD column (no FX conversion).
	type Branch = { key: string; first: BranchRow; byCurrency: Partial<Record<Currency, BranchRow>> };
	let branches = $derived.by(() => {
		const out: Branch[] = [];
		const index = new Map<string, Branch>();
		for (const r of rows) {
			const key = [r.shop_name_ar, r.shop_name_en, r.shop_phone, r.channel].join('\u0000');
			let branch = index.get(key);
			if (!branch) {
				branch = { key, first: r, byCurrency: {} };
				index.set(key, branch);
				out.push(branch);
			}
			branch.byCurrency[currencyOf(r)] = r;
		}
		return out;
	});

	function currencyOf(r: BranchRow): Currency {
		return r.currency === 'USD' ? 'USD' : 'EGP';
	}

	function valueOf(r: BranchRow): string {
		if (metric === 'transactions') return formatInt(r.transactions);
		if (metric === 'units') return formatInt(r.units);
		return formatMinor(r.sales_total_minor, currencyOf(r));
	}

	function refundOf(r: BranchRow): string {
		if (metric === 'transactions') return formatInt(r.return_transactions);
		if (metric === 'units') return formatInt(r.units_returned);
		return formatMinor(r.refund_total_minor, currencyOf(r));
	}

	// Exact bar ratios: amount and maximum stay BigInt so unsafe magnitudes
	// (>2^53) never collapse to zero. Only the bounded 0..10000 basis-point
	// result becomes a Number for the discrete width class. Maxima are
	// per-currency column (raw EGP minor units are never compared to USD).
	function amountOf(r: BranchRow): bigint {
		if (metric === 'transactions') return BigInt(r.transactions);
		if (metric === 'units') return BigInt(r.units);
		return BigInt(r.sales_total_minor);
	}

	let maxima = $derived.by(() => {
		const m: Record<Currency, bigint> = { EGP: 1n, USD: 1n };
		for (const r of rows) {
			const v = amountOf(r);
			if (v > m[currencyOf(r)]) m[currencyOf(r)] = v;
		}
		return m;
	});

	function bucketOf(r: BranchRow): number {
		const bp = (amountOf(r) * 10000n) / maxima[currencyOf(r)];
		return Math.min(10, Math.round(Number(bp) / 1000));
	}

	let refundLabel = $derived(metric === 'value' ? t('مرتجعات', 'Refunds') : t('مرتجع', 'Returned'));
</script>

<Card ar="مبيعات الفروع والمتجر الإلكتروني" en="Sales by Branches and Online Store">
	{#if status === 'loading'}
		<Skeleton />
	{:else if status === 'error'}
		<WidgetError status={errStatus} {onretry} />
	{:else if rows.length === 0}
		<EmptyState ar="لا توجد فروع في هذه الفترة" en="No branches in this period" />
	{:else}
		<table class="branches" data-testid="branch-table">
			<thead>
				<tr>
					<th scope="col">{t('الفرع / القناة', 'Branch / Channel')}{#if arabic()}<span class="sub-en">Branch / Channel</span>{/if}</th>
					<th scope="col" class="num">EGP</th>
					<th scope="col" class="num">USD</th>
				</tr>
			</thead>
			<tbody>
				{#each branches as b (b.key)}
					<tr>
						<th scope="row">
							<div class="lname">{t(b.first.shop_name_ar, b.first.shop_name_en || b.first.shop_name_ar)}</div>
							<div class="muted lsub">{arabic() ? `${b.first.shop_name_en} · ${b.first.channel}` : b.first.channel}</div>
						</th>
						{#each ['EGP', 'USD'] as const as cur}
							{@const r = b.byCurrency[cur]}
							<td class="num">
								{#if r}
									<div class="val">{valueOf(r)}</div>
									<div class="track" role="img" aria-label={`${b.first.shop_name_en} ${cur}: ${valueOf(r)}`}><div class="fill w{bucketOf(r)}"></div></div>
									<div class="muted refund">{refundLabel} {refundOf(r)}</div>
								{:else}
									<span class="muted">—</span>
								{/if}
							</td>
						{/each}
					</tr>
				{/each}
			</tbody>
		</table>
		<Segmented
			options={[
				{ value: 'value', ar: 'قيمة المبيعات', en: 'Sales value' },
				{ value: 'transactions', ar: 'المعاملات', en: 'Transactions' },
				{ value: 'units', ar: 'الوحدات', en: 'Units' }
			]}
			value={metric}
			onchange={(v) => (metric = v as Metric)}
		/>
	{/if}
</Card>

<style>
	.branches {
		width: 100%;
		border-collapse: collapse;
		table-layout: fixed;
		margin-bottom: 10px;
	}
	.branches th,
	.branches td {
		padding: 6px 4px;
		border-bottom: 1px solid var(--border);
		vertical-align: top;
		min-width: 0;
		overflow-wrap: anywhere;
	}
	.branches thead th {
		font-size: 0.72rem;
		font-weight: 600;
		color: var(--text-muted);
		text-align: start;
		vertical-align: bottom;
	}
	.branches thead th:first-child {
		width: 38%;
	}
	.branches .num {
		text-align: end;
	}
	.sub-en {
		display: block;
		font-size: 0.66rem;
		font-weight: 400;
	}
	.branches tbody th {
		text-align: start;
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
		font-size: 0.8rem;
		font-variant-numeric: tabular-nums;
	}
	.track {
		height: 6px;
		margin: 4px 0 3px;
		background: #eef1f5;
		border-radius: 6px;
		overflow: hidden;
	}
	.fill {
		height: 100%;
		background: var(--primary);
		margin-inline-start: auto;
	}
	.refund {
		font-size: 0.68rem;
	}
</style>
