<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import echarts from '../lib/echarts.js';
	import Card from './Card.svelte';
	import Segmented from './Segmented.svelte';
	import Skeleton from './Skeleton.svelte';
	import EmptyState from './EmptyState.svelte';
	import { toChartNumber } from '../lib/money.js';

	export interface CategoryDisplayRow {
		name: string;
		units: number;
		amount_minor: string;
	}

	let {
		rows,
		kind,
		money,
		unit,
		status
	}: {
		rows: CategoryDisplayRow[];
		kind: 'root' | 'subcategory';
		money: 'EGP' | 'USD';
		unit: string;
		status: 'loading' | 'loaded' | 'empty' | 'error';
	} = $props();

	let shape: 'donut' | 'pie' | 'bar' = $state('donut');
	let el: HTMLDivElement | null = null;
	let chart: echarts.ECharts | null = null;
	let ro: ResizeObserver | null = null;

	function render() {
		if (!el || status !== 'loaded' || rows.length === 0) return;
		chart ??= echarts.init(el);
		const data = rows.map((r) => ({ name: r.name, value: toChartNumber(r.amount_minor) }));
		const base = {
			animation: false,
			tooltip: { trigger: shape === 'bar' ? 'axis' : ('item' as const) }
		};
		if (shape === 'bar') {
			chart.setOption(
				{
					...base,
					grid: { left: 48, right: 16, top: 24, bottom: 60 },
					xAxis: { type: 'category', data: rows.map((r) => r.name), axisLabel: { fontSize: 10, rotate: 20 } },
					yAxis: { type: 'value', name: unit },
					series: [{ type: 'bar', data: data.map((d) => d.value), itemStyle: { color: '#1d4ed8' } }]
				},
				true
			);
		} else {
			chart.setOption(
				{
					...base,
					series: [
						{
							type: 'pie',
							radius: shape === 'donut' ? ['45%', '70%'] : '65%',
							data,
							label: { fontSize: 11 }
						}
					]
				},
				true
			);
		}
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
		shape;
		render();
	});
</script>

<Card ar="أداء الفئات والفئات الفرعية" en="Category & subcategory performance">
	<Segmented
		options={[
			{ value: 'donut', ar: 'دائري' },
			{ value: 'pie', ar: 'دائرة' },
			{ value: 'bar', ar: 'أعمدة' }
		]}
		value={shape}
		onchange={(v) => (shape = v as 'donut' | 'pie' | 'bar')}
	/>
	{#if kind === 'subcategory'}
		<div class="muted" style="margin-top: 6px;">
			قد يُحتسب المنتج في أكثر من فئة فرعية / A product may count in multiple subcategories
		</div>
	{/if}
	{#if status === 'loading'}
		<Skeleton />
	{:else if status === 'error'}
		<EmptyState ar="تعذر تحميل البيانات" en="Could not load data" />
	{:else if rows.length === 0}
		<EmptyState ar="لا توجد فئات في هذه الفترة" en="No categories in this period" />
	{:else}
		<div bind:this={el} class="chart" role="img" aria-label="Category performance chart"></div>
	{/if}
</Card>

<style>
	.chart {
		height: 260px;
		direction: ltr;
		margin-top: 8px;
	}
</style>
