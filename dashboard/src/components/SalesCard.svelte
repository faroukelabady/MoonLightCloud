<script lang="ts">
	import Card from './Card.svelte';
	import Segmented from './Segmented.svelte';
	import Skeleton from './Skeleton.svelte';
	import EmptyState from './EmptyState.svelte';
	import WidgetError from './WidgetError.svelte';
	import { formatMinor, formatInt } from '../lib/money.js';
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

	function unitsReturned(): number {
		if (!data) return 0;
		if (mode === 'all') return data.normalized.units_returned;
		return bucket()?.returned_units ?? 0;
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

	let txnDisplay = $derived.by(() => {
		const avg = average();
		return formatInt(avg?.transactions ?? data?.summary.transaction_count ?? 0);
	});
</script>

<Card ar="صافي المبيعات" en="Net sales">
	{#snippet actions()}
		<Segmented
			options={[
				{ value: 'all', ar: 'الكل' },
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
			<span>إجمالي المبيعات / Gross: {formatMinor(grossMinor() ?? '0', mode === 'all' ? 'EGP' : mode)}</span>
			<span class="refund">المرتجعات / Refunds: {formatMinor(refundMinor() ?? '0', mode === 'all' ? 'EGP' : mode)}</span>
		</div>
		{#if mode === 'all'}
			<div class="muted helper">بعد تحويل مبيعات USD باستخدام سعر الصرف التاريخي لكل عملية بيع</div>
			<div class="muted sub-en helper-en">USD sales converted using each sale's historical FX rate</div>
		{/if}
		<div class="kpis">
			<div class="kpi">
				<div class="kpi-ic" aria-hidden="true"><svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M4 7h13l-3-3M20 17H7l3 3" /></svg></div>
				<div class="kpi-label">عدد المعاملات<br /><span class="muted">Transactions</span></div>
				<div class="kpi-value num">{txnDisplay}</div>
			</div>
			<div class="kpi">
				<div class="kpi-ic" aria-hidden="true"><svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M6 3h12v18l-3-2-3 2-3-2-3 2V3zM9 8h6M9 12h6" /></svg></div>
				<div class="kpi-label">متوسط قيمة العملية<br /><span class="muted">Average transaction value</span></div>
				<div class="kpi-value num">{avgDisplay}</div>
			</div>
			<div class="kpi">
				<div class="kpi-ic" aria-hidden="true"><svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3l8 4.5v9L12 21l-8-4.5v-9L12 3zM12 12l8-4.5M12 12L4 7.5M12 12v9" /></svg></div>
				<div class="kpi-label">الوحدات المباعة<br /><span class="muted">Units Sold</span></div>
				<div class="kpi-value num">{formatInt(average()?.units ?? data.summary.units_sold)}</div>
			</div>
			<div class="kpi">
				<div class="kpi-label">الوحدات المرتجعة<br /><span class="muted">Units Returned</span></div>
				<div class="kpi-value num">{formatInt(unitsReturned())}</div>
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
	.kpis {
		display: grid;
		grid-template-columns: repeat(3, 1fr);
		gap: 6px;
		margin-top: 8px;
	}
	.kpi {
		background: var(--surface-muted);
		border: 1px solid var(--border);
		border-radius: var(--radius-control);
		padding: 4px 6px;
		min-width: 0;
	}
	.kpi-ic {
		color: var(--primary);
	}
	.kpi-label {
		font-size: 0.74rem;
		margin-top: 2px;
		line-height: 1.35;
	}
	.kpi-value {
		font-size: 1rem;
		font-weight: 700;
		margin-top: 2px;
	}
	.fx {
		margin-top: 2px;
		font-size: 0.74rem;
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
		align-items: center;
	}
	@media (max-width: 700px) {
		.kpis {
			grid-template-columns: 1fr;
		}
	}
</style>
