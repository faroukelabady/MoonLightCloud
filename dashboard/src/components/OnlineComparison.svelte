<script lang="ts">
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
		currency = 'all'
	}: {
		online: OrderAnalyticsResponse | null;
		retail: OverviewResponse | null;
		status: 'loading' | 'loaded' | 'empty' | 'error';
		errStatus: number | null;
		onretry: () => void;
		currency?: 'all' | 'EGP' | 'USD';
	} = $props();

	function cur(c: string): 'EGP' | 'USD' {
		return c === 'USD' ? 'USD' : 'EGP';
	}
</script>

<Card ar="البيعات النهائية مقابل الطلبات عبر الإنترنت" en="Finalized Retail Sales vs Online Orders">
	{#if status === 'loading'}
		<Skeleton />
	{:else if status === 'error'}
		<WidgetError status={errStatus} {onretry} />
	{:else if status === 'empty'}
		<EmptyState ar="لا توجد طلبات عبر الإنترنت في هذه الفترة" en="No online orders in this period" />
	{:else if online}
		<div class="compare">
			<section aria-label="Finalized Retail Sales / المبيعات النهائية">
				<h3>المبيعات النهائية (نقدية)<br /><span class="th-en">Finalized Retail Sales</span></h3>
				{#if retail}
					<p class="num">{formatInt(retail.summary.transaction_count)} <span class="th-en">transactions (all Retail currencies) / معاملات بكل العملات</span></p>
					{#each retail.summary.currency_totals.filter((t) => currency === 'all' || t.currency === currency) as total (total.currency)}
						<p class="num" dir="ltr">
							{formatMinor(total.net_sales_minor, cur(total.currency))}
							<span class="th-en">net {total.currency}</span>
						</p>
					{/each}
				{/if}
			</section>
			<section aria-label="Online Orders / الطلبات عبر الإنترنت">
				<h3>الطلبات عبر الإنترنت (تشغيلية)<br /><span class="th-en">Online Orders (operational)</span></h3>
				{#each online.currency_totals.filter((t) => currency === 'all' || t.currency === currency) as total (total.currency)}
					<p class="num" dir="ltr">
						{formatMinor(total.value_minor, cur(total.currency))}
						<span class="th-en">order value {total.currency} · {formatInt(total.orders)} orders</span>
					</p>
				{/each}
				{#if online.currency_totals.length === 0}
					<p class="th-en">No online order value in this period.</p>
				{/if}
			</section>
		</div>
		<h4>حالة الطلبات / <span class="th-en">Order status</span></h4>
		<table>
			<thead>
				<tr>
					<th scope="col">الحالة / <span class="th-en">Status</span></th>
					<th scope="col">الطلبات / <span class="th-en">Orders</span></th>
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
		<h4>حسب المزود / <span class="th-en">By provider</span></h4>
		<table>
			<thead>
				<tr>
					<th scope="col">المزود / <span class="th-en">Provider</span></th>
					<th scope="col">العملة / <span class="th-en">Currency</span></th>
					<th scope="col">الطلبات / <span class="th-en">Orders</span></th>
					<th scope="col">قيمة الطلبات / <span class="th-en">Order value</span></th>
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
			الطلبات عبر الإنترنت هي طلبات تشغيلية من المزودين، وقد لا تمثل إيرادًا مُعترفًا به إضافةً إلى المبيعات النهائية.<br />
			<span class="th-en">Online orders are operational provider orders and may not represent additional recognized revenue beyond finalized Retail Sales. The two sources are shown separately and never summed.</span>
		</p>
	{/if}
</Card>

<style>
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
