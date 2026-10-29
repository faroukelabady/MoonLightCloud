<script lang="ts">
	import Card from './Card.svelte';
	import Skeleton from './Skeleton.svelte';
	import EmptyState from './EmptyState.svelte';
	import WidgetError from './WidgetError.svelte';
	import { msg } from '../lib/i18n.svelte.js';
	import { formatMinor } from '../lib/money.js';
	import type { LatestSale } from '../lib/api.js';

	let { sales, status, errStatus, onretry }: { sales: LatestSale[]; status: 'loading' | 'loaded' | 'empty' | 'error'; errStatus: number | null; onretry: () => void } = $props();
</script>

<Card ar="أحدث المبيعات" en="Latest Sales">
	<div class="muted label-gap foot-note">{msg('latestSalesCard.orderTrackingArrivesWithOnlineCommerce')}</div>
	{#if status === 'loading'}
		<Skeleton />
	{:else if status === 'error'}
		<WidgetError status={errStatus} {onretry} />
	{:else if sales.length === 0}
		<EmptyState ar="لا توجد مبيعات بعد" en="No sales yet" />
	{:else}
		<table class="data">
			<thead><tr><th>#</th><th>{msg('latestSalesCard.number')}</th><th>{msg('latestSalesCard.channel')}</th><th>{msg('latestSalesCard.cashier')}</th><th>{msg('latestSalesCard.total')}</th></tr></thead>
			<tbody>
				{#each sales as s, i}
					<tr>
						<td class="num rank">{i + 1}</td>
						<td class="num">{s.sale_number}</td>
						<td>{s.channel}</td>
						<td>{s.cashier_name ?? '—'}</td>
						<td class="num"><strong>{formatMinor(s.total_minor, s.currency === 'USD' ? 'USD' : 'EGP')}</strong></td>
					</tr>
				{/each}
			</tbody>
		</table>
	{/if}
</Card>

<style>
	.foot-note {
		font-size: 0.76rem;
	}
	.rank {
		color: var(--text-muted);
		width: 2ch;
	}
</style>
