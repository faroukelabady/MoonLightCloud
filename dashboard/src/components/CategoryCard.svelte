<script lang="ts">
	import { msg, t } from '../lib/i18n.svelte.js';
	import Card from './Card.svelte';
	import Segmented from './Segmented.svelte';
	import Skeleton from './Skeleton.svelte';
	import EmptyState from './EmptyState.svelte';
	import WidgetError from './WidgetError.svelte';
	import { formatMinor, toChartNumber } from '../lib/money.js';
	import { chart } from '../lib/chartAction.js';
	import type { EChartsCoreOption } from 'echarts/core';
	import type { CategoryKind } from '../lib/api.js';

	export interface CategoryDisplayRow {
		name: string;
		units: number;
		units_returned: number;
		amount_minor: string;
		refund_minor: string;
		net_minor: string;
	}

	let {
		rows,
		kind,
		onkind,
		unit,
		status,
		errStatus,
		onretry,
		embedded = false,
		money = 'EGP'
	}: {
		rows: CategoryDisplayRow[];
		kind: CategoryKind;
		onkind: (k: CategoryKind) => void;
		unit: string;
		status: 'loading' | 'loaded' | 'empty' | 'error';
		errStatus: number | null;
		onretry: () => void;
		/** Rendered inside another card (PerformanceCard): no own card frame,
		    plus a ranked legend with each category's net and share. */
		embedded?: boolean;
		money?: 'EGP' | 'USD';
	} = $props();

	const palette = ['#1d5bd7', '#12805c', '#d97706', '#7c3aed', '#db2777', '#0891b2', '#65a30d', '#64748b'];
	// Share of the positive net total, exact (BigInt) to one decimal place.
	let legend = $derived.by(() => {
		const positive = rows.reduce((sum, r) => (BigInt(r.net_minor || '0') > 0n ? sum + BigInt(r.net_minor) : sum), 0n);
		return rows.map((r, i) => {
			const net = BigInt(r.net_minor || '0');
			const permille = positive > 0n && net > 0n ? Number((net * 1000n) / positive) : 0;
			return { name: r.name, net: r.net_minor, share: permille / 10, color: palette[i % palette.length] };
		});
	});

	// M08: subcategories are non-additive facets — Pie/Donut would falsely
	// imply a partition of one whole. Bar only, with the facet explanation.
	// Switching kinds resets an invalid shape back to Bar.
	let shape: 'donut' | 'pie' | 'bar' = $state('donut');
	$effect(() => {
		kind;
		if (kind === 'subcategory' && shape !== 'bar') shape = 'bar';
	});

	// M04: validate chart inputs during preparation, never render.
	// Net-ranked with gross and refund visible; facet semantics preserved.
	let chartError = $derived.by(() => {
		if (status !== 'loaded') return false;
		try {
			rows.forEach((r) => toChartNumber(r.net_minor));
			return false;
		} catch {
			return true;
		}
	});

	let option: EChartsCoreOption | null = $derived.by(() => {
		if (status !== 'loaded' || chartError || rows.length === 0) return null;
		const data = rows.map((r) => ({ name: r.name, value: toChartNumber(r.net_minor) }));
		if (shape === 'bar') {
			return {
				animation: false,
				grid: { left: 48, right: 16, top: 24, bottom: 60 },
				xAxis: { type: 'category', data: rows.map((r) => r.name), axisLabel: { fontSize: 10, rotate: 20 } },
				yAxis: { type: 'value', name: unit },
				tooltip: { trigger: 'axis' },
				series: [{ type: 'bar', data: data.map((d, i) => ({ value: d.value, itemStyle: { color: palette[i % palette.length] } })) }]
			} satisfies EChartsCoreOption;
		}
		return {
			animation: false,
			tooltip: { trigger: 'item' },
			series: [
				{
					type: 'pie',
					radius: shape === 'donut' ? ['45%', '70%'] : '65%',
					data: data.map((d, i) => ({ ...d, itemStyle: { color: palette[i % palette.length] } })),
					label: embedded ? { show: false } : { fontSize: 11 }
				}
			]
		} satisfies EChartsCoreOption;
	});
</script>

{#snippet body()}
	<div class="kindrow">
		<Segmented
			size="sm"
			options={[
				{ value: 'root_category', ar: 'رئيسية', en: 'Roots', aria: msg('categoryCard.roots') },
				{ value: 'subcategory', ar: 'فرعية', en: 'Subcategories', aria: msg('categoryCard.subcategories') }
			]}
			value={kind}
			onchange={(v) => onkind(v as CategoryKind)}
		/>
		{#if kind === 'root_category'}
			<Segmented
				size="sm"
				options={[
					{ value: 'bar', ar: 'أعمدة', en: 'Bar' },
					{ value: 'pie', ar: 'دائرة', en: 'Pie' },
					{ value: 'donut', ar: 'دائري', en: 'Donut' }
				]}
				value={shape}
				onchange={(v) => (shape = v as 'donut' | 'pie' | 'bar')}
			/>
		{/if}
	</div>
	{#if kind !== 'root_category'}
		<div class="muted facet">
			{msg('categoryCard.productsCanBelongToMultipleSubcategories')}
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
		<div class="catbody" class:split={embedded}>
			<div use:chart={option} class="chart" role="img" aria-label={msg('categoryCard.categoryPerformanceChart')}></div>
			{#if embedded}
				<ol class="legend" data-testid="category-legend">
					{#each legend as l}
						<li>
							<span class="dot" style:background={l.color}></span>
							<span class="lname" title={l.name}>{l.name}</span>
							<span class="num lnet" dir="ltr">{formatMinor(l.net, money)}</span>
							<span class="num lshare" dir="ltr">{l.share.toFixed(1)}%</span>
						</li>
					{/each}
				</ol>
			{/if}
		</div>
	{/if}
{/snippet}

{#if embedded}
	<section class="pane" aria-label={t('أداء الفئات (صافي المبيعات)', 'Category Performance (net)')}>
		<h3 class="pane-title">{t('أداء الفئات (صافي المبيعات)', 'Category performance (net)')}</h3>
		{@render body()}
	</section>
{:else}
	<Card ar="أداء الفئات (صافي المبيعات)" en="Category Performance (net)">
		{@render body()}
	</Card>
{/if}

<style>
	.chart {
		height: 170px;
		direction: ltr;
		margin-top: 8px;
	}
	.pane-title {
		margin: 0 0 6px;
		font-size: 0.9rem;
		font-weight: 600;
	}
	.catbody.split {
		display: grid;
		grid-template-columns: minmax(140px, 1fr) minmax(0, 1.2fr);
		gap: 10px;
		align-items: center;
	}
	.legend {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 5px;
		font-size: 0.8rem;
		max-height: 190px;
		overflow: auto;
	}
	.legend li {
		display: grid;
		grid-template-columns: 10px minmax(0, 1fr) auto 3.4em;
		gap: 6px;
		align-items: center;
	}
	.dot {
		width: 10px;
		height: 10px;
		border-radius: 3px;
	}
	.lname {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.lshare {
		color: var(--muted, #667085);
		text-align: end;
	}
	.facet {
		font-size: 0.78rem;
		margin-bottom: 8px;
	}
	.kindrow {
		display: flex;
		flex-wrap: wrap;
		gap: 4px 6px;
		align-items: center;
		margin-bottom: 8px;
	}
</style>
