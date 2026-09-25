<script lang="ts">
	import Card from './Card.svelte';
	import Segmented from './Segmented.svelte';
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
		refundValues = null,
		netValues = null,
		exact,
		refundExact = null,
		rangeError,
		unit,
		status,
		errStatus,
		onretry,
		mode,
		onmode
	}: {
		titleAr: string;
		titleEn: string;
		labels: string[];
		values: number[];
		refundValues?: number[] | null;
		netValues?: number[] | null;
		exact: { date: string; amount_minor: string }[];
		refundExact?: { date: string; refund_minor: string }[] | null;
		rangeError: boolean;
		unit: string;
		errStatus: number | null;
		onretry: () => void;
		status: 'loading' | 'loaded' | 'empty' | 'error';
		mode?: 'all' | 'EGP' | 'USD';
		onmode?: (m: 'all' | 'EGP' | 'USD') => void;
	} = $props();

	let option: EChartsCoreOption | null = $derived.by(() => {
		if (status !== 'loaded' || values.length === 0) return null;
		const series: { name: string; type: 'line'; data: number[]; smooth: boolean; symbol: string; symbolSize: number; areaStyle?: { opacity: number }; lineStyle: { color: string; width: number }; itemStyle: { color: string } }[] = [
			{
				name: 'Gross / الإجمالي',
				type: 'line',
				data: values,
				smooth: true,
				symbol: 'circle',
				symbolSize: 6,
				areaStyle: { opacity: 0.15 },
				lineStyle: { color: '#1d5bd7', width: 2 },
				itemStyle: { color: '#1d5bd7' }
			}
		];
		if (refundValues && refundValues.length === values.length) {
			series.push({
				name: 'Refunds / المرتجعات',
				type: 'line',
				data: refundValues,
				smooth: true,
				symbol: 'circle',
				symbolSize: 6,
				lineStyle: { color: '#d92d20', width: 2 },
				itemStyle: { color: '#d92d20' }
			});
		}
		if (netValues && netValues.length === values.length) {
			series.push({
				name: 'Net / الصافي',
				type: 'line',
				data: netValues,
				smooth: true,
				symbol: 'circle',
				symbolSize: 6,
				lineStyle: { color: '#12805c', width: 2 },
				itemStyle: { color: '#12805c' }
			});
		}
		return {
			animation: false,
			grid: { left: 48, right: 16, top: 44, bottom: 28 },
			legend: { top: 0, textStyle: { fontSize: 11 } },
			xAxis: { type: 'category', data: labels, axisLabel: { fontSize: 11 } },
			yAxis: { type: 'value', name: unit, nameTextStyle: { fontSize: 11 } },
			tooltip: { trigger: 'axis', valueFormatter: (v: unknown) => `${v} ${unit}` },
			series
		} satisfies EChartsCoreOption;
	});
</script>

<Card ar={titleAr} en={`${titleEn} · ${unit}`}>
	{#snippet actions()}
		{#if mode && onmode}
			<Segmented
				size="sm"
				options={[
					{ value: 'all', ar: 'الكل' },
					{ value: 'EGP', ar: 'EGP' },
					{ value: 'USD', ar: 'USD' }
				]}
				value={mode}
				onchange={(v) => onmode?.(v as 'all' | 'EGP' | 'USD')}
			/>
		{/if}
	{/snippet}
	{#if status === 'loading'}
		<Skeleton />
	{:else if status === 'error'}
		<WidgetError status={errStatus} {onretry} />
	{:else if rangeError}
		<div data-testid="sales-trend-range-error" role="status">
			<EmptyState
				ar="تعذر عرض الرسم البياني لهذا النطاق"
				en="Chart value is outside the supported display range"
			/>
		</div>
		<ul class="exact">
			{#each exact as d, i}
				<li><span class="num" dir="ltr">{d.date}</span> — <span class="num">{d.amount_minor} {unit}</span>{#if refundExact?.[i]} <span class="num refund">مرتجعات {refundExact[i].refund_minor}</span>{/if}</li>
			{/each}
		</ul>
	{:else if values.length === 0}
		<EmptyState ar="لا توجد بيانات في هذه الفترة" en="No data in this period" />
	{:else}
		<div
			data-testid="sales-trend-chart"
			use:chart={option}
			class="chart"
			role="img"
			aria-label={`${titleAr}: ${labels.length} days, unit ${unit}`}
		></div>
	{/if}
</Card>

<style>
	.chart {
		height: 170px;
		direction: ltr;
	}
	.exact {
		list-style: none;
		margin: 8px 0 0;
		padding: 0;
		font-size: 0.85rem;
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
	.refund {
		color: var(--danger, #b42318);
	}
</style>
