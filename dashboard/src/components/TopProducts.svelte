<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import echarts from '../lib/echarts.js';
	import Card from './Card.svelte';
	import Segmented from './Segmented.svelte';
	import Skeleton from './Skeleton.svelte';
	import EmptyState from './EmptyState.svelte';
	import { formatMinor, toChartNumber } from '../lib/money.js';

	export interface ProductDisplayRow {
		name: string;
		sku: string;
		units: number;
		amount_minor: string;
	}

	let { rows, money, unit, status }: { rows: ProductDisplayRow[]; money: 'EGP' | 'USD'; unit: string; status: 'loading' | 'loaded' | 'empty' | 'error' } =
		$props();

	let view: 'table' | 'chart' = $state('table');
	let el: HTMLDivElement | null = null;
	let chart: echarts.ECharts | null = null;
	let ro: ResizeObserver | null = null;

	function render() {
		if (!el || status !== 'loaded' || view !== 'chart' || rows.length === 0) return;
		chart ??= echarts.init(el);
		chart.setOption(
			{
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
			},
			true
		);
	}

	onMount(() => {
		if (el) {
			ro = new ResizeObserver(() => chart?.resize());
			ro.observe(el);
		}
	});
	onDestroy(() => {
		ro?.disconnect();
		chart?.dispose();
		chart = null;
	});
	$effect(() => {
		rows;
		view;
		render();
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
		<EmptyState ar="تعذر تحميل البيانات" en="Could not load data" />
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
	{:else}
		<div bind:this={el} class="chart" role="img" aria-label="Top products chart"></div>
	{/if}
</Card>

<style>
	.chart {
		height: 260px;
		direction: ltr;
		margin-top: 8px;
	}
</style>
