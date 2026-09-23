<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import echarts from '../lib/echarts.js';
	import Card from './Card.svelte';
	import Skeleton from './Skeleton.svelte';
	import EmptyState from './EmptyState.svelte';

	let {
		titleAr,
		titleEn,
		labels,
		values,
		unit,
		status
	}: {
		titleAr: string;
		titleEn: string;
		labels: string[];
		values: number[];
		unit: string;
		status: 'loading' | 'loaded' | 'empty' | 'error';
	} = $props();

	let el: HTMLDivElement | null = null;
	let chart: echarts.ECharts | null = null;
	let ro: ResizeObserver | null = null;

	function render() {
		if (!el || status !== 'loaded') return;
		chart ??= echarts.init(el);
		chart.setOption(
			{
				animation: false,
				grid: { left: 48, right: 16, top: 24, bottom: 28 },
				xAxis: { type: 'category', data: labels, axisLabel: { fontSize: 11 } },
				yAxis: { type: 'value', name: unit, nameTextStyle: { fontSize: 11 } },
				tooltip: { trigger: 'axis', valueFormatter: (v: unknown) => `${v} ${unit}` },
				series: [
					{
						name: unit,
						type: 'line',
						data: values,
						smooth: true,
						symbol: 'circle',
						areaStyle: { opacity: 0.15 },
						lineStyle: { color: '#1d4ed8' },
						itemStyle: { color: '#1d4ed8' }
					}
				]
			},
			true
		);
	}

	onMount(() => {
		render();
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
		labels;
		values;
		unit;
		status;
		render();
	});
</script>

<Card ar={titleAr} en={titleEn}>
	{#if status === 'loading'}
		<Skeleton />
	{:else if status === 'error'}
		<EmptyState ar="تعذر تحميل البيانات" en="Could not load data" />
	{:else if values.length === 0}
		<EmptyState ar="لا توجد بيانات في هذه الفترة" en="No data in this period" />
	{:else}
		<div
			bind:this={el}
			class="chart"
			role="img"
			aria-label={`${titleAr}: ${labels.length} days, unit ${unit}`}
		></div>
	{/if}
</Card>

<style>
	.chart {
		height: 260px;
		direction: ltr;
	}
</style>
