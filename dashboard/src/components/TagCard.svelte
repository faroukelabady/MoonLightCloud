<script lang="ts">
	import Card from './Card.svelte';
	import Skeleton from './Skeleton.svelte';
	import EmptyState from './EmptyState.svelte';
	import WidgetError from './WidgetError.svelte';
	import { formatMinor, formatInt } from '../lib/money.js';
	import type { TagListResponse } from '../lib/api.js';

	let {
		data,
		status,
		errStatus,
		onretry,
		currency
	}: {
		data: TagListResponse | null;
		status: 'loading' | 'loaded' | 'empty' | 'error';
		errStatus: number | null;
		onretry: () => void;
		currency: 'all' | 'EGP' | 'USD';
	} = $props();

	// Top Tags ranks by net line sales per currency bucket (the canonical
	// report ranking); rows carry historical Tag snapshot identity.
</script>

<Card ar={currency === 'all' ? 'أعلى الوسوم (عدد الوحدات)' : 'أعلى الوسوم (صافي المبيعات)'} en={currency === 'all' ? 'Top Tags by Units' : 'Top Tags by Net Sales'}>
	{#if status === 'loading'}
		<Skeleton />
	{:else if status === 'error'}
		<WidgetError status={errStatus} {onretry} />
	{:else if status === 'empty'}
		<EmptyState ar="لا توجد مبيعات وسوم في هذه الفترة" en="No Tag sales in this period" />
	{:else if data}
		<div class="table-wrap">
			<table>
				<thead>
					<tr>
						<th scope="col">#</th>
						<th scope="col">الوسم<br /><span class="th-en">Tag</span></th>
						<th scope="col">الوحدات<br /><span class="th-en">Units</span></th>
						<th scope="col">الإيراد<br /><span class="th-en">Gross</span></th>
						<th scope="col">المرتجعات<br /><span class="th-en">Refunds</span></th>
						<th scope="col">الصافي<br /><span class="th-en">Net</span></th>
					</tr>
				</thead>
				<tbody>
					{#each data.rows as row, i (JSON.stringify([row.tag_id, row.tag_slug, row.name_ar, row.name_en]))}
						<tr>
							<td class="num">{i + 1}</td>
							<td>
								{row.name_ar ?? row.tag_slug ?? row.tag_id ?? '—'}
								{#if row.name_en}<br /><span class="th-en">{row.name_en}</span>{/if}
								{#if row.tag_slug}<span class="num" dir="ltr"> ({row.tag_slug})</span>{/if}
							</td>
							<td class="num">{formatInt(row.units)}</td>
							<td class="num">
								{#each currency === 'all' ? row.currencies : row.currencies.filter((c) => c.currency === currency) as bucket (bucket.currency)}
									<span dir="ltr">{formatMinor(bucket.line_sales_minor, bucket.currency === 'USD' ? 'USD' : 'EGP')}</span>
									{#if currency === 'all'}<span class="th-en"> {bucket.currency}</span><br />{/if}
								{/each}
							</td>
							<td class="num">
								{#each currency === 'all' ? row.currencies : row.currencies.filter((c) => c.currency === currency) as bucket (bucket.currency)}
									<span dir="ltr">{formatMinor(bucket.line_refund_minor, bucket.currency === 'USD' ? 'USD' : 'EGP')}</span>
									{#if currency === 'all'}<span class="th-en"> {bucket.currency}</span><br />{/if}
								{/each}
							</td>
							<td class="num">
								{#each currency === 'all' ? row.currencies : row.currencies.filter((c) => c.currency === currency) as bucket (bucket.currency)}
									<span dir="ltr">{formatMinor(bucket.net_minor, bucket.currency === 'USD' ? 'USD' : 'EGP')}</span>
									{#if currency === 'all'}<span class="th-en"> {bucket.currency}</span><br />{/if}
								{/each}
							</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
		<p class="note" role="note">
			قد يساهم بند البيع في عدة وسوم، لذا تتداخل مجموعات الوسوم ويجب عدم جمع إجمالياتها لحساب إيراد النشاط.<br />
			<span class="th-en">Tag totals overlap (one line can carry several tags) and must not be summed to derive total business revenue.</span>
		</p>
	{/if}
</Card>

<style>
	.table-wrap {
		overflow-x: auto;
	}
	table {
		width: 100%;
		border-collapse: collapse;
	}
	th,
	td {
		padding: 0.35rem 0.5rem;
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
