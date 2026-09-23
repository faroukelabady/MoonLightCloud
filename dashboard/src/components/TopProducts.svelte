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
					itemStyle: { color: '#1d4ed8' }
				}
			]
		} satisfies EChartsCoreOption;
	});
</script>

<Card ar="أعلى المنتجات مبيعًا" en="Top selling products">
	<Segmented
		options={[
			{ value: 'table', ar: 'جدول' },
			{ value: 'chart', ar: 'رسم بياني' }
		]}
		value={view}
		onchange={(v) => (view = v as 'table' | 'chart')}
	/>
	{#if status === 'loading'}
		<Skeleton />
	{:else if status === 'error'}
		<WidgetError status={errStatus} {onretry} />
	{:else if rows.length === 0}
		<EmptyState ar="لا توجد منتجات في هذه الفترة" en="No products in this period" />
	{:else if view === 'table'}
		<table class="data">
			<thead><tr><th>المنتج / Product</th><th>الوحدات / Units</th><th>المبيعات / Sales ({unit})</th></tr></thead>
			<tbody>
				{#each rows as r}
					<tr>
						<td>{r.name}<br /><span class="muted num">{r.sku}</span></td>
						<td class="num">{r.units}</td>
						<td class="num">{formatMinor(r.amount_minor, money)}</td>
					</tr>
				{/each}
			</tbody>
		</table>
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
		height: 260px;
		direction: ltr;
		margin-top: 8px;
	}
</style>
