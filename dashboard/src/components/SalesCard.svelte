<script lang="ts">
	import { arabic, t, tb } from '../lib/i18n.svelte.js';
	import Card from './Card.svelte';
	import Segmented from './Segmented.svelte';
	import Skeleton from './Skeleton.svelte';
	import EmptyState from './EmptyState.svelte';
	import WidgetError from './WidgetError.svelte';
	import { formatMinor } from '../lib/money.js';
	import type { OverviewResponse } from '../lib/api.js';

	let {
		data,
		mode,
		onmode,
		status,
		errStatus,
		onretry
	}: {
		data: OverviewResponse | null;
		mode: 'all' | 'EGP' | 'USD';
		onmode: (m: 'all' | 'EGP' | 'USD') => void;
		status: 'loading' | 'loaded' | 'empty' | 'error';
		errStatus: number | null;
		onretry: () => void;
	} = $props();

	function bucket() {
		if (!data) return null;
		if (mode !== 'all') return data.summary.currency_totals.find((b) => b.currency === mode) ?? null;
		return null;
	}

	// Net is the primary business KPI; gross and refunds stay visible so
	// the distinction is never hidden. All money stays string-exact.
	function grossMinor(): string | null {
		if (!data) return null;
		if (mode === 'all') return data.normalized.normalized_total_minor;
		return bucket()?.sales_total_minor ?? null;
	}

	function refundMinor(): string | null {
		if (!data) return null;
		if (mode === 'all') return data.normalized.normalized_refund_minor;
		return bucket()?.refund_total_minor ?? null;
	}

	function average(): { transactions: number; units: number; average_minor: string } | null {
		if (!data) return null;
		if (mode === 'all') return data.averages.all;
		if (mode === 'EGP') return data.averages.egp;
		return data.averages.usd;
	}

	let avgDisplay = $derived.by(() => {
		const avg = average();
		if (!avg) return '—';
		return formatMinor(avg.average_minor, mode === 'all' ? 'EGP' : mode);
	});
</script>

<Card ar="صافي المبيعات" en="Net sales">
	{#snippet actions()}
		<Segmented
			options={[
				{ value: 'all', ar: 'الكل', en: 'All' },
				{ value: 'EGP', ar: 'EGP' },
				{ value: 'USD', ar: 'USD' }
			]}
			value={mode}
			onchange={(v) => onmode(v as 'all' | 'EGP' | 'USD')}
		/>
	{/snippet}
	{#if status === 'loading'}
		<Skeleton />
	{:else if status === 'error'}
		<WidgetError status={errStatus} {onretry} />
	{:else if !data || (data.summary.transaction_count === 0 && data.summary.return_transaction_count === 0)}
		<EmptyState ar="لا توجد مبيعات في هذه الفترة" en="No sales in this period" />
	{:else}
		<div class="total num">
			{#if mode === 'all'}
				{formatMinor(data.normalized.normalized_net_minor, 'EGP')}
			{:else}
				{@const b = bucket()}
				{b ? formatMinor(b.net_sales_minor, mode) : '—'}
			{/if}
		</div>
		<div class="split num">
			<span>{tb('إجمالي المبيعات', 'Gross')}: {formatMinor(grossMinor() ?? '0', mode === 'all' ? 'EGP' : mode)}</span>
			<span class="refund">{tb('المرتجعات', 'Refunds')}: {formatMinor(refundMinor() ?? '0', mode === 'all' ? 'EGP' : mode)}</span>
		</div>
		{#if mode === 'all'}
			<div class="muted helper">{t('بعد تحويل مبيعات USD باستخدام سعر الصرف التاريخي لكل عملية بيع', "USD sales converted using each sale's historical FX rate")}</div>
			{#if arabic()}<div class="muted sub-en helper-en">USD sales converted using each sale's historical FX rate</div>{/if}
		{/if}
		<!-- Counts live in the Transactions and units card; this card keeps money. -->
		<div class="avg">
			<span class="avg-label">{t('متوسط قيمة العملية', 'Average transaction value')}{#if arabic()}<span class="muted sub-en">Average transaction value</span>{/if}</span>
			<span class="avg-value num" data-testid="average-value">{avgDisplay}</span>
		</div>
		{#if mode === 'all' && data.fx.has_usd}
			<div class="fx muted">
				{t('آخر سعر صرف مسجل', 'Latest historical FX rate')}: 1 USD = {data.fx.latest_rate ?? '?'} EGP
				<span class="sub-en">{t('Latest historical FX rate (not a live market rate)', '(not a live market rate)')}</span>
				{#if data.fx.multiple_rates_used}<span class="badge warn">{tb('أسعار تاريخية متعددة', 'Multiple historical rates used')}</span>{/if}
			</div>
		{/if}
	{/if}
</Card>

<style>
	.total {
		font-size: 1.7rem;
		font-weight: 700;
		margin: 0 0 2px;
	}
	.split {
		display: flex;
		flex-wrap: wrap;
		gap: 4px 12px;
		font-size: 0.8rem;
		color: var(--text-muted);
		margin-bottom: 2px;
	}
	.split .refund {
		color: var(--danger, #b42318);
	}
	.helper {
		font-size: 0.75rem;
		line-height: 1.4;
	}
	.helper-en {
		font-size: 0.7rem;
	}
	.avg {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 8px;
		margin-top: 10px;
		padding: 8px 10px;
		background: var(--surface-muted);
		border: 1px solid var(--border);
		border-radius: var(--radius-control);
	}
	.avg-label {
		font-size: 0.78rem;
		line-height: 1.35;
	}
	.avg-label .sub-en {
		display: block;
	}
	.avg-value {
		font-size: 1.05rem;
		font-weight: 700;
	}
	.fx {
		margin-top: 2px;
		font-size: 0.74rem;
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
		align-items: center;
	}
</style>
