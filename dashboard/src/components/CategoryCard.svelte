<script lang="ts">
	import Card from './Card.svelte';
	import Segmented from './Segmented.svelte';
	import Skeleton from './Skeleton.svelte';
	import EmptyState from './EmptyState.svelte';
	import WidgetError from './WidgetError.svelte';
	import { toChartNumber } from '../lib/money.js';
	import { chart } from '../lib/chartAction.js';
	import type { EChartsCoreOption } from 'echarts/core';
	import type { CategoryKind } from '../lib/api.js';

	export interface CategoryDisplayRow {
		name: string;
		units: number;
		amount_minor: string;
	}

	let {
		rows,
		kind,
		unit,
		status,
		errStatus,
		onretry
	}: {
		rows: CategoryDisplayRow[];
		kind: CategoryKind;
		unit: string;
		status: 'loading' | 'loaded' | 'empty' | 'error';
		errStatus: number | null;
		onretry: () => void;
	} = $props();

	// M08: subcategories are non-additive facets — Pie/Donut would falsely
	// imply a partition of one whole. Bar only, with the facet explanation.
	// Switching kinds resets an invalid shape back to Bar.
	let shape: 'donut' | 'pie' | 'bar' = $state('donut');
	$effect(() => {
		kind;
		if (kind === 'subcategory' && shape !== 'bar') shape = 'bar';
	});

	// M04: validate chart inputs during preparation, never render.
	let chartError = $derived.by(() => {
		if (status !== 'loaded') return false;
		try {
			rows.forEach((r) => toChartNumber(r.amount_minor));
			return false;
		} catch {
			return true;
		}
	});

	let option: EChartsCoreOption | null = $derived.by(() => {
		if (status !== 'loaded' || chartError || rows.length === 0) return null;
		const data = rows.map((r) => ({ name: r.name, value: toChartNumber(r.amount_minor) }));
		if (shape === 'bar') {
			return {
				animation: false,
				grid: { left: 48, right: 16, top: 24, bottom: 60 },
				xAxis: { type: 'category', data: rows.map((r) => r.name), axisLabel: { fontSize: 10, rotate: 20 } },
				yAxis: { type: 'value', name: unit },
				tooltip: { trigger: 'axis' },
				series: [{ type: 'bar', data: data.map((d) => d.value), itemStyle: { color: '#1d5bd7' } }]
			} satisfies EChartsCoreOption;
		}
		return {
			animation: false,
			tooltip: { trigger: 'item' },
			series: [
				{
					type: 'pie',
					radius: shape === 'donut' ? ['45%', '70%'] : '65%',
					data,
					label: { fontSize: 11 }
				}
			]
		} satisfies EChartsCoreOption;
	});
</script>

<Card ar="أداء الفئات وقنوات البيع" en="Category & Sales Channel Performance">
	{#snippet actions()}
		{#if kind === 'root_category'}
			<Segmented
				options={[
					{ value: 'bar', ar: 'أعمدة' },
					{ value: 'pie', ar: 'دائرة' },
					{ value: 'donut', ar: 'دائري' }
				]}
				value={shape}
				onchange={(v) => (shape = v as 'donut' | 'pie' | 'bar')}
			/>
		{/if}
	{/snippet}
	{#if kind !== 'root_category'}
		<div class="muted facet">
			قد ينتمي المنتج إلى أكثر من فئة فرعية، لذلك لا تمثل الفئات الفرعية أجزاءً من إجمالي واحد — عرض بالأعمدة فقط.
			<br /><span class="sub-en">Products can belong to multiple subcategories, so subcategories do not form parts of a single whole — bar view only.</span>
		</div>
	{/if}
	{#if status === 'loading'}
		<Skeleton />
	{:else if status === 'error'}
		<WidgetError status={errStatus} {onretry} />
	{:else if rows.length === 0}
		<EmptyState ar="لا توجد فئات في هذه الفترة" en="No categories in this period" />
	{:else if chartError}
		<EmptyState
			ar="تعذر عرض الرسم البياني لهذا النطاق"
			en="Chart value is outside the supported display range"
		/>
	{:else}
		<div use:chart={option} class="chart" role="img" aria-label="Category performance chart"></div>
	{/if}
</Card>

<style>
	.chart {
		height: 250px;
		direction: ltr;
		margin-top: 8px;
	}
	.facet {
		font-size: 0.78rem;
		margin-bottom: 8px;
	}
</style>
