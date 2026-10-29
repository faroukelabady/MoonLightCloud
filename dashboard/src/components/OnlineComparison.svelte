<script lang="ts">
	import { msg } from '../lib/i18n.svelte.js';
	import Card from './Card.svelte';
	import Skeleton from './Skeleton.svelte';
	import EmptyState from './EmptyState.svelte';
	import WidgetError from './WidgetError.svelte';
	import { formatMinor, formatInt } from '../lib/money.js';
	import type { OrderAnalyticsResponse, OverviewResponse } from '../lib/api.js';

	let {
		online,
		retail,
		status,
		errStatus,
		onretry,
		currency = 'all',
		bare = false
	}: {
		online: OrderAnalyticsResponse | null;
		retail: OverviewResponse | null;
		status: 'loading' | 'loaded' | 'empty' | 'error';
		errStatus: number | null;
		onretry: () => void;
		currency?: 'all' | 'EGP' | 'USD';
		// bare renders the comparison inside another card (no frame).
		bare?: boolean;
	} = $props();

	function cur(c: string): 'EGP' | 'USD' {
		return c === 'USD' ? 'USD' : 'EGP';
	}
</script>

{#snippet body()}
	{#if status === 'loading'}
		<Skeleton />
	{:else if status === 'error'}
		<WidgetError status={errStatus} {onretry} />
	{:else if status === 'empty'}
		<EmptyState ar="لا توجد طلبات عبر الإنترنت في هذه الفترة" en="No online orders in this period" />
	{:else if online}
		<div class="compare">
			<section aria-label={msg('onlineComparison.finalizedRetailSales')}>
				<h3>{msg('onlineComparison.finalizedRetailSales')}</h3>
				{#if retail}
					<p class="num">{formatInt(retail.summary.transaction_count)} <span class="th-en">{msg('onlineComparison.transactionsAllCurrencies')}</span></p>
					{#each retail.summary.currency_totals.filter((t) => currency === 'all' || t.currency === currency) as total (total.currency)}
						<p class="num" dir="ltr">
							{formatMinor(total.net_sales_minor, cur(total.currency))}
							<span class="th-en">{msg('onlineComparison.netIn', { currency: total.currency })}</span>
						</p>
					{/each}
				{/if}
			</section>
			<section aria-label={msg('onlineComparison.onlineOrdersOperational')}>
				<h3>{msg('onlineComparison.onlineOrdersOperational')}</h3>
				{#each online.currency_totals.filter((t) => currency === 'all' || t.currency === currency) as total (total.currency)}
					<p class="num" dir="ltr">
						{formatMinor(total.value_minor, cur(total.currency))}
						<span class="th-en">{msg('onlineComparison.orderValueIn', { currency: total.currency, orders: formatInt(total.orders) })}</span>
					</p>
				{/each}
				{#if online.currency_totals.length === 0}
					<p class="th-en">{msg('onlineComparison.noOrderValue')}</p>
				{/if}
			</section>
		</div>
		<h4>{msg('onlineComparison.orderStatus')}</h4>
		<table>
			<thead>
				<tr>
					<th scope="col">{msg('onlineComparison.status')}</th>
					<th scope="col">{msg('onlineComparison.orders')}</th>
				</tr>
			</thead>
			<tbody>
				{#each online.status_counts as s (s.canonical_status)}
					<tr>
						<td class="num" dir="ltr">{s.canonical_status}</td>
						<td class="num">{formatInt(s.orders)}</td>
					</tr>
				{/each}
			</tbody>
		</table>
		<h4>{msg('onlineComparison.byProvider')}</h4>
		<table>
			<thead>
				<tr>
					<th scope="col">{msg('onlineComparison.provider')}</th>
					<th scope="col">{msg('onlineComparison.currency')}</th>
					<th scope="col">{msg('onlineComparison.orders')}</th>
					<th scope="col">{msg('onlineComparison.orderValue')}</th>
				</tr>
			</thead>
			<tbody>
				{#each online.provider_totals as p (`${p.provider_key}-${p.currency}`)}
					<tr>
						<td class="num" dir="ltr">{p.provider_key}</td>
						<td class="num" dir="ltr">{p.currency}</td>
						<td class="num">{formatInt(p.orders)}</td>
						<td class="num" dir="ltr">{formatMinor(p.value_minor, cur(p.currency))}</td>
					</tr>
				{/each}
			</tbody>
		</table>
		<p class="note" role="note">
			{msg('onlineComparison.onlineOrdersAreOperationalProviderOrders')}
		</p>
	{/if}
{/snippet}

{#if bare}
	<section class="bare" aria-labelledby="online-compare-heading">
		<h3 id="online-compare-heading" class="bare-title">{msg('onlineComparison.finalizedSalesVsOnlineOrders')}</h3>
		{@render body()}
	</section>
{:else}
	<Card ar="المبيعات النهائية مقابل الطلبات عبر الإنترنت" en="Finalized Retail Sales vs Online Orders">
		{@render body()}
	</Card>
{/if}

<style>
	.bare-title {
		font-size: 0.9rem;
		margin: 0 0 0.4rem;
	}
	.compare {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 1rem;
	}
	h3,
	h4 {
		margin: 0.4rem 0;
		font-size: 0.95rem;
	}
	table {
		width: 100%;
		border-collapse: collapse;
		margin-bottom: 0.5rem;
	}
	th,
	td {
		padding: 0.3rem 0.5rem;
		text-align: start;
		border-bottom: 1px solid var(--border, #ddd);
	}
	.th-en {
		opacity: 0.7;
		font-size: 0.8em;
	}
	.note {
		margin-top: 0.6rem;
		font-size: 0.82rem;
		opacity: 0.85;
	}
</style>
