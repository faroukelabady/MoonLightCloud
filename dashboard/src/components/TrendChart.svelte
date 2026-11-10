<script lang="ts">
	import { msg, t } from '../lib/i18n.svelte.js';
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
		onmode,
		embedded = false
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
		/** Rendered inside another card (PerformanceCard): no own card frame. */
		embedded?: boolean;
	} = $props();

	let option: EChartsCoreOption | null = $derived.by(() => {
		if (status !== 'loaded' || values.length === 0) return null;
		const series: { name: string; type: 'line'; data: number[]; smooth: boolean; symbol: string; symbolSize: number; areaStyle?: { opacity: number }; lineStyle: { color: string; width: number }; itemStyle: { color: string } }[] = [
			{
				name: msg('trendChart.gross'),
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
				name: msg('trendChart.refunds'),
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
				name: msg('trendChart.net'),
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

{#snippet modeSwitch()}
	{#if mode && onmode}
		<Segmented
			size="sm"
			options={[
				{ value: 'all', ar: 'الكل', en: 'All' },
				{ value: 'EGP', ar: 'EGP' },
				{ value: 'USD', ar: 'USD' }
			]}
			value={mode}
			onchange={(v) => onmode?.(v as 'all' | 'EGP' | 'USD')}
		/>
	{/if}
{/snippet}

{#snippet body()}
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
				<li><span class="num" dir="ltr">{d.date}</span> — <span class="num">{d.amount_minor} {unit}</span>{#if refundExact?.[i]} <span class="num refund">{msg('trendChart.refunds')} {refundExact[i].refund_minor}</span>{/if}</li>
			{/each}
		</ul>
	{:else if values.length === 0}
		<EmptyState ar="لا توجد بيانات في هذه الفترة" en="No data in this period" />
	{:else}
		<div
			data-testid="sales-trend-chart"
			use:chart={option}
			class="chart"
			class:tall={embedded}
			role="img"
			aria-label={`${titleAr}: ${labels.length} days, unit ${unit}`}
		></div>
	{/if}
{/snippet}

{#if embedded}
	<section class="pane" aria-label={t(titleAr, titleEn)}>
		<h3 class="pane-title">{t(titleAr, titleEn)} <span class="muted">· {unit}</span></h3>
		{@render body()}
	</section>
{:else}
	<Card ar={titleAr} en={`${titleEn} · ${unit}`} actions={mode && onmode ? modeSwitch : undefined}>
		{@render body()}
	</Card>
{/if}

<style>
	.chart {
		height: 170px;
		direction: ltr;
	}
	.chart.tall {
		height: 230px;
	}
	.pane-title {
		margin: 0 0 6px;
		font-size: 0.9rem;
		font-weight: 600;
	}
	.pane-title .muted {
		font-weight: 400;
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
