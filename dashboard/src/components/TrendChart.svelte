<script lang="ts">
	import Card from './Card.svelte';
	import Skeleton from './Skeleton.svelte';
	import EmptyState from './EmptyState.svelte';
	import WidgetError from './WidgetError.svelte';
	import { chart } from '../lib/chartAction.js';
	import type { EChartsCoreOption } from 'echarts/core';

	let {
		titleAr,
		titleEn,
		labels,
		values,
		unit,
		status,
		errStatus,
		onretry
	}: {
		titleAr: string;
		titleEn: string;
		labels: string[];
		values: number[];
		unit: string;
		errStatus: number | null;
		onretry: () => void;
		status: 'loading' | 'loaded' | 'empty' | 'error';
	} = $props();

	let option: EChartsCoreOption | null = $derived.by(() => {
		if (status !== 'loaded' || values.length === 0) return null;
		return {
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
		} satisfies EChartsCoreOption;
	});
</script>

<Card ar={titleAr} en={titleEn}>
	{#if status === 'loading'}
		<Skeleton />
	{:else if status === 'error'}
		<WidgetError status={errStatus} {onretry} />
	{:else if values.length === 0}
		<EmptyState ar="لا توجد بيانات في هذه الفترة" en="No data in this period" />
	{:else}
		<div
			use:chart={option}
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
