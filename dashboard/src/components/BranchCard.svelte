<script lang="ts">
	import Card from './Card.svelte';
	import Segmented from './Segmented.svelte';
	import Skeleton from './Skeleton.svelte';
	import EmptyState from './EmptyState.svelte';
	import { formatMinor, formatInt } from '../lib/money.js';
	import type { BranchRow } from '../lib/api.js';

	let { rows, status }: { rows: BranchRow[]; status: 'loading' | 'loaded' | 'empty' | 'error' } = $props();

	let metric: 'value' | 'transactions' | 'units' = $state('value');

	function valueOf(r: BranchRow): string {
		if (metric === 'transactions') return formatInt(r.transactions);
		if (metric === 'units') return formatInt(r.units);
		return formatMinor(r.sales_total_minor, r.currency === 'USD' ? 'USD' : 'EGP');
	}

	function numOf(r: BranchRow): number {
		const v =
			metric === 'transactions' ? r.transactions : metric === 'units' ? r.units : Number(r.sales_total_minor);
		return Number.isSafeInteger(v) && v >= 0 ? v : 0;
	}

	function maxValue(): number {
		let m = 1;
		for (const r of rows) {
			const v = numOf(r);
			if (v > m) m = v;
		}
		return m;
	}
</script>

<Card ar="مبيعات الفروع والمتجر الإلكتروني" en="Sales by branches and online store">
	<Segmented
		options={[
			{ value: 'value', ar: 'قيمة المبيعات' },
			{ value: 'transactions', ar: 'المعاملات' },
			{ value: 'units', ar: 'الوحدات' }
		]}
		value={metric}
		onchange={(v) => (metric = v as 'value' | 'transactions' | 'units')}
	/>
	{#if status === 'loading'}
		<Skeleton />
	{:else if status === 'error'}
		<EmptyState ar="تعذر تحميل البيانات" en="Could not load data" />
	{:else if rows.length === 0}
		<EmptyState ar="لا توجد فروع في هذه الفترة" en="No branches in this period" />
	{:else}
		<div class="bars">
			{#each rows as r}
				{@const pct = Math.min(100, (numOf(r) / maxValue()) * 100)}
				<div class="row">
					<div class="label">
						<div>{r.shop_name_ar}</div>
						<div class="muted">{r.shop_name_en} · {r.channel}</div>
					</div>
					<div class="track"><div class="fill" style="width: {pct}%"></div></div>
					<div class="num val">{valueOf(r)}</div>
				</div>
			{/each}
		</div>
	{/if}
</Card>

<style>
	.bars {
		display: flex;
		flex-direction: column;
		gap: 10px;
		margin-top: 10px;
	}
	.row {
		display: grid;
		grid-template-columns: minmax(140px, 220px) 1fr auto;
		gap: 10px;
		align-items: center;
	}
	.track {
		height: 10px;
		background: #eef1f5;
		border-radius: 6px;
		overflow: hidden;
	}
	.fill {
		height: 100%;
		background: var(--color-primary);
	}
	.val {
		min-width: 90px;
		text-align: end;
		font-weight: 600;
	}
</style>
