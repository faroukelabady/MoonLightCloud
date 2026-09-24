<script lang="ts">
	import Card from './Card.svelte';
	import Segmented from './Segmented.svelte';
	import Skeleton from './Skeleton.svelte';
	import EmptyState from './EmptyState.svelte';
	import WidgetError from './WidgetError.svelte';
	import { formatMinor, formatInt } from '../lib/money.js';
	import type { BranchRow } from '../lib/api.js';

	let { rows, status, errStatus, onretry }: { rows: BranchRow[]; status: 'loading' | 'loaded' | 'empty' | 'error'; errStatus: number | null; onretry: () => void } = $props();

	let metric: 'value' | 'transactions' | 'units' = $state('value');

	function valueOf(r: BranchRow): string {
		if (metric === 'transactions') return formatInt(r.transactions);
		if (metric === 'units') return formatInt(r.units);
		return formatMinor(r.sales_total_minor, r.currency === 'USD' ? 'USD' : 'EGP');
	}

	// Exact bar ratios: amount and maximum stay BigInt so unsafe magnitudes
	// (>2^53) never collapse to zero. Only the bounded 0..10000 basis-point
	// result becomes a Number for the discrete width class. Maxima are
	// per-currency (raw EGP minor units are never compared to USD ones;
	// no FX conversion for bar width).
	function amountOf(r: BranchRow): bigint {
		if (metric === 'transactions') return BigInt(r.transactions);
		if (metric === 'units') return BigInt(r.units);
		return BigInt(r.sales_total_minor);
	}

	function maxOf(currency: string): bigint {
		let m = 1n;
		for (const r of rows) {
			if (r.currency !== currency) continue;
			const v = amountOf(r);
			if (v > m) m = v;
		}
		return m;
	}

	function bucketOf(r: BranchRow): number {
		const bp = (amountOf(r) * 10000n) / maxOf(r.currency);
		return Math.min(10, Math.round(Number(bp) / 1000));
	}
</script>

<Card ar="مبيعات الفروع والمتجر الإلكتروني" en="Sales by Branches and Online Store">
	{#if status === 'loading'}
		<Skeleton />
	{:else if status === 'error'}
		<WidgetError status={errStatus} {onretry} />
	{:else if rows.length === 0}
		<EmptyState ar="لا توجد فروع في هذه الفترة" en="No branches in this period" />
	{:else}
		<div class="cols muted"><span>الفرع / القناة<br /><span class="sub-en">Branch / Channel</span></span><span class="num">قيمة المبيعات<br /><span class="sub-en">Sales Value</span></span></div>
		<div class="bars">
			{#each rows as r}
				{@const bucket = bucketOf(r)}
				<div class="row">
					<div class="label">
						<div class="lname">{r.shop_name_ar}</div>
						<div class="muted lsub">{r.shop_name_en} · {r.channel}</div>
					</div>
					<div class="track" role="img" aria-label={`${r.shop_name_en}: ${valueOf(r)}`}><div class="fill w{bucket}"></div></div>
					<div class="num val">{valueOf(r)}</div>
				</div>
			{/each}
		</div>
		<Segmented
			options={[
				{ value: 'value', ar: 'قيمة المبيعات' },
				{ value: 'transactions', ar: 'المعاملات' },
				{ value: 'units', ar: 'الوحدات' }
			]}
			value={metric}
			onchange={(v) => (metric = v as 'value' | 'transactions' | 'units')}
		/>
	{/if}
</Card>

<style>
	.cols {
		display: flex;
		justify-content: space-between;
		font-size: 0.72rem;
		margin-bottom: 2px;
	}
	.cols .num {
		text-align: end;
	}
	.bars {
		display: flex;
		flex-direction: column;
		gap: 8px;
		margin: 6px 0 10px;
	}
	.row {
		display: flex;
		flex-wrap: wrap;
		gap: 6px 8px;
		align-items: center;
	}
	.label {
		flex: 1 1 96px;
		min-width: 0;
	}
	.lname {
		font-size: 0.82rem;
		font-weight: 600;
	}
	.lsub {
		font-size: 0.7rem;
	}
	.track {
		flex: 2 1 48px;
		min-width: 48px;
		height: 9px;
		background: #eef1f5;
		border-radius: 6px;
		overflow: hidden;
	}
	.fill {
		height: 100%;
		background: var(--primary);
	}
	.val {
		flex: 1 1 64px;
		min-width: 0;
		max-width: 100%;
		overflow-wrap: anywhere;
		text-align: end;
		font-weight: 700;
		font-size: 0.8rem;
	}
</style>
