<script lang="ts">
	import Card from './Card.svelte';
	import Segmented from './Segmented.svelte';
	import Skeleton from './Skeleton.svelte';
	import EmptyState from './EmptyState.svelte';
	import WidgetError from './WidgetError.svelte';
	import { formatMinor, toChartNumber } from '../lib/money.js';
	import { chart } from '../lib/chartAction.js';
	import type { EChartsCoreOption } from 'echarts/core';

	export interface ProductDisplayRow {
		name: string;
		sku: string;
		units: number;
		amount_minor: string;
	}

	let { rows, money, unit, status, errStatus, onretry }: { rows: ProductDisplayRow[]; money: 'EGP' | 'USD'; unit: string; status: 'loading' | 'loaded' | 'empty' | 'error'; errStatus: number | null; onretry: () => void } =
		$props();

	let view: 'table' | 'chart' = $state('table');

	// M04: chart inputs are validated during data preparation (not render).
	// Unsafe magnitudes yield a stable widget error; exact table values
	// stay visible and the page never crashes.
	let chartError = $derived.by(() => {
		if (status !== 'loaded' || view !== 'chart') return false;
		try {
			rows.forEach((r) => toChartNumber(r.amount_minor));
			return false;
		} catch {
			return true;
		}
	});

	let option: EChartsCoreOption | null = $derived.by(() => {
		if (status !== 'loaded' || view !== 'chart' || chartError || rows.length === 0) return null;
		return {
			animation: false,
			grid: { left: 48, right: 16, top: 24, bottom: 60 },
			xAxis: {
				type: 'category',
				data: rows.map((r) => r.name),
				axisLabel: { fontSize: 10, rotate: 20 }
			},
			yAxis: { type: 'value', name: unit },
			tooltip: { trigger: 'item' },
			series: [
				{
					type: 'bar',
					data: rows.map((r) => toChartNumber(r.amount_minor)),
					itemStyle: { color: '#1d5bd7' }
				}
			]
		} satisfies EChartsCoreOption;
	});
</script>

<Card ar="أعلى المنتجات مبيعًا" en="Top Selling Products">
	{#snippet actions()}
		<Segmented
			size="sm"
			options={[
				{ value: 'table', ar: 'جدول' },
				{ value: 'chart', ar: 'رسم بياني' }
			]}
			value={view}
			onchange={(v) => (view = v as 'table' | 'chart')}
		/>
	{/snippet}
	{#if status === 'loading'}
		<Skeleton />
	{:else if status === 'error'}
		<WidgetError status={errStatus} {onretry} />
	{:else if rows.length === 0}
		<EmptyState ar="لا توجد منتجات في هذه الفترة" en="No products in this period" />
	{:else if view === 'table'}
		<div class="tscroll">
		<table class="data">
			<thead><tr><th>#</th><th>المنتج<br /><span class="th-en">Product</span></th><th>الكمية المباعة<br /><span class="th-en">Units Sold</span></th><th>قيمة المبيعات<br /><span class="th-en">Sales value ({unit})</span></th></tr></thead>
			<tbody>
				{#each rows as r, i}
					<tr>
						<td class="num rank">{i + 1}</td>
						<td>
							<span class="pname"><span class="thumb" aria-hidden="true">{r.name.slice(0, 1)}</span>{r.name}</span><br /><span class="muted num sku">{r.sku}</span>
						</td>
						<td class="num">{r.units}</td>
						<td class="num">{formatMinor(r.amount_minor, money)}</td>
					</tr>
				{/each}
			</tbody>
		</table>
		</div>
	{:else if chartError}
		<EmptyState
			ar="تعذر عرض الرسم البياني لهذا النطاق"
			en="Chart value is outside the supported display range"
		/>
	{:else}
		<div use:chart={option} class="chart" role="img" aria-label="Top products chart"></div>
	{/if}
</Card>

<style>
	.chart {
		height: 170px;
		direction: ltr;
		margin-top: 8px;
	}
	.tscroll {
		overflow-x: auto;
	}
	.tscroll table.data {
		min-width: 440px;
		table-layout: fixed;
	}
	.tscroll table.data th:nth-child(1),
	.tscroll table.data td:nth-child(1) {
		width: 28px;
	}
	.tscroll table.data th:nth-child(3),
	.tscroll table.data td:nth-child(3) {
		width: 90px;
	}
	.tscroll table.data th:nth-child(4),
	.tscroll table.data td:nth-child(4) {
		width: 100px;
	}
	.tscroll table.data th,
	.tscroll table.data td {
		padding: 4px 6px;
		font-size: 0.8rem;
	}
	.tscroll table.data th {
		font-size: 0.7rem;
		line-height: 1.25;
	}
	.tscroll .th-en {
		font-size: 0.66rem;
		color: var(--text-muted);
		font-weight: 400;
	}
	.tscroll table.data td.num {
		white-space: nowrap;
	}
	.rank {
		color: var(--text-muted);
		width: 2ch;
	}
	.pname {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		font-size: 0.8rem;
	}
	.thumb {
		width: 22px;
		height: 22px;
		flex-shrink: 0;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		border-radius: 6px;
		background: #f3ead3;
		color: #8a6d2b;
		font-weight: 700;
		font-size: 0.8rem;
	}
	.sku {
		font-size: 0.7rem;
	}
</style>
