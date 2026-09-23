<script lang="ts">
	import Card from './Card.svelte';
	import Segmented from './Segmented.svelte';
	import Skeleton from './Skeleton.svelte';
	import EmptyState from './EmptyState.svelte';
	import { formatMinor, formatInt } from '../lib/money.js';
	import type { OverviewResponse } from '../lib/api.js';

	let {
		data,
		mode,
		onmode,
		status
	}: {
		data: OverviewResponse | null;
		mode: 'all' | 'EGP' | 'USD';
		onmode: (m: 'all' | 'EGP' | 'USD') => void;
		status: 'loading' | 'loaded' | 'empty' | 'error';
	} = $props();

	function bucket() {
		if (!data) return null;
		if (mode !== 'all') return data.summary.currency_totals.find((b) => b.currency === mode) ?? null;
		return null;
	}

	function allTotal(): string | null {
		if (!data || mode !== 'all') return null;
		return formatMinor(data.normalized.normalized_total_minor, 'EGP');
	}
</script>

<Card ar="إجمالي المبيعات" en="Total sales">
	<Segmented
		options={[
			{ value: 'all', ar: 'الكل' },
			{ value: 'EGP', ar: 'EGP' },
			{ value: 'USD', ar: 'USD' }
		]}
		value={mode}
		onchange={(v) => onmode(v as 'all' | 'EGP' | 'USD')}
	/>
	{#if status === 'loading'}
		<Skeleton />
	{:else if status === 'error'}
		<EmptyState ar="تعذر تحميل البيانات" en="Could not load data" />
	{:else if !data || data.summary.transaction_count === 0}
		<EmptyState ar="لا توجد مبيعات في هذه الفترة" en="No sales in this period" />
	{:else}
		<div class="total num">
			{#if mode === 'all'}
				{allTotal()}
			{:else}
				{@const b = bucket()}
				{b ? formatMinor(String(b.sales_total_minor), mode) : '—'}
			{/if}
		</div>
		{#if mode === 'all'}
			<div class="muted">بعد تحويل مبيعات USD باستخدام سعر الصرف التاريخي لكل عملية بيع</div>
			<div class="muted sub-en">USD sales converted using each sale's historical FX rate</div>
		{/if}
		<div class="kpis">
			<div class="kpi">
				<div class="kpi-label">عدد المعاملات<br /><span class="muted">Transactions</span></div>
				<div class="kpi-value num">{formatInt(data.summary.transaction_count)}</div>
			</div>
			<div class="kpi">
				<div class="kpi-label">متوسط قيمة العملية<br /><span class="muted">Average transaction value</span></div>
				<div class="kpi-value num">
					{#if mode === 'all'}
						{formatMinor(
							String(
								BigInt(data.normalized.normalized_total_minor) /
									BigInt(Math.max(1, data.summary.transaction_count))
							),
							'EGP'
						)}
					{:else}
						{@const b2 = bucket()}
						{b2
							? formatMinor(
									String(
										BigInt(b2.sales_total_minor) / BigInt(Math.max(1, data.summary.transaction_count))
									),
									mode
								)
							: '—'}
					{/if}
				</div>
			</div>
			<div class="kpi">
				<div class="kpi-label">الوحدات المباعة<br /><span class="muted">Units Sold</span></div>
				<div class="kpi-value num">{formatInt(data.summary.units_sold)}</div>
			</div>
		</div>
		{#if mode === 'all' && data.fx.has_usd}
			<div class="fx muted">
				آخر سعر صرف مسجل: 1 USD = {data.fx.latest_rate ?? '?'} EGP
				<span class="sub-en">Latest historical FX rate (not a live market rate)</span>
				{#if data.fx.multiple_rates_used}<span class="badge warn">أسعار تاريخية متعددة / Multiple historical rates used</span>{/if}
			</div>
		{/if}
	{/if}
</Card>

<style>
	.total {
		font-size: 1.8rem;
		font-weight: 700;
		margin: 10px 0 4px;
	}
	.sub-en {
		font-size: 0.75rem;
	}
	.kpis {
		display: flex;
		gap: 16px;
		margin-top: 12px;
		flex-wrap: wrap;
	}
	.kpi-label {
		font-size: 0.85rem;
	}
	.kpi-value {
		font-size: 1.15rem;
		font-weight: 600;
	}
	.fx {
		margin-top: 10px;
		font-size: 0.85rem;
	}
</style>
