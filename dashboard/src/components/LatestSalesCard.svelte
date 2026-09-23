<script lang="ts">
	import Card from './Card.svelte';
	import Skeleton from './Skeleton.svelte';
	import EmptyState from './EmptyState.svelte';
	import { formatMinor } from '../lib/money.js';
	import type { LatestSale } from '../lib/api.js';

	let { sales, status }: { sales: LatestSale[]; status: 'loading' | 'loaded' | 'empty' | 'error' } = $props();
</script>

<Card ar="أحدث المبيعات" en="Latest sales">
	<div class="muted" style="margin-bottom: 6px;">تتبع الطلبات سيتوفر عند تفعيل التكامل مع المتجر الإلكتروني / Order tracking arrives with online-commerce integration</div>
	{#if status === 'loading'}
		<Skeleton />
	{:else if status === 'error'}
		<EmptyState ar="تعذر تحميل المبيعات" en="Could not load sales" />
	{:else if sales.length === 0}
		<EmptyState ar="لا توجد مبيعات بعد" en="No sales yet" />
	{:else}
		<table class="data">
			<thead><tr><th>الرقم / Number</th><th>القناة / Channel</th><th>الكاشير / Cashier</th><th>الإجمالي / Total</th></tr></thead>
			<tbody>
				{#each sales as s}
					<tr>
						<td class="num">{s.sale_number}</td>
						<td>{s.channel}</td>
						<td>{s.cashier_name ?? '—'}</td>
						<td class="num">{formatMinor(s.total_minor, s.currency === 'USD' ? 'USD' : 'EGP')}</td>
					</tr>
				{/each}
			</tbody>
		</table>
	{/if}
</Card>
