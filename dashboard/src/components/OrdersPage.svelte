<script lang="ts">
	import { msg } from '../lib/i18n.svelte.js';
	import Card from './Card.svelte';
	import Skeleton from './Skeleton.svelte';
	import EmptyState from './EmptyState.svelte';
	import WidgetError from './WidgetError.svelte';
	import SingleSelect from './ui/SingleSelect.svelte';
	import { formatMinor } from '../lib/money.js';
	import type { OrderSummary, OrderDetail, OrderStatusCount, WebhookInboxStats } from '../lib/api.js';

	let {
		orders,
		counts,
		inbox,
		status,
		errStatus,
		filterStatus,
		filterProvider,
		nextCursor,
		more,
		moreErr,
		onloadmore,
		onstatus,
		onprovider,
		selected,
		detailStatus,
		detailErr,
		onselect,
		onretry
	}: {
		orders: OrderSummary[];
		counts: OrderStatusCount[];
		inbox: WebhookInboxStats | null;
		status: 'loading' | 'loaded' | 'empty' | 'error';
		errStatus: number | null;
		filterStatus: string;
		filterProvider: string;
		nextCursor: string | null;
		more: 'idle' | 'loading' | 'error';
		moreErr: number | null;
		onloadmore: () => void;
		onstatus: (s: string) => void;
		onprovider: (p: string) => void;
		selected: OrderDetail | null;
		detailStatus: 'idle' | 'loading' | 'loaded' | 'error';
		detailErr: number | null;
		onselect: (order: OrderSummary | null) => void;
		onretry: () => void;
	} = $props();

	const statuses = ['pending', 'processing', 'on_hold', 'completed', 'cancelled', 'refunded', 'failed', 'unknown', 'deleted'];

	function money(o: OrderSummary): string {
		return formatMinor(o.total_minor, o.currency === 'USD' ? 'USD' : 'EGP');
	}
	function lineMoney(total: string, currency: string): string {
		return formatMinor(total, currency === 'USD' ? 'USD' : 'EGP');
	}
</script>

<Card ar="الطلبات عبر الإنترنت" en="Online Orders">
	<div class="muted label-gap foot-note">{msg('ordersPage.readOnlyWoocommerceOrdersNeverSales')}</div>
	<div class="filters">
		<SingleSelect
			id="orders-status"
			label={msg('ordersPage.status')}
			testid="orders-status"
			value={filterStatus}
			options={[{ value: '', label: msg('ordersPage.all') }, ...statuses.map((s) => ({ value: s, label: s, dir: 'ltr' as const }))]}
			onchange={onstatus}
		/>
		<label class="field">{msg('ordersPage.provider')}
			<input value={filterProvider} placeholder="website" oninput={(e) => onprovider((e.target as HTMLInputElement).value)} />
		</label>
	</div>
	{#if counts.length > 0}
		<div class="chips" role="status">
			{#each counts as c}<span class="chip">{c.canonical_status}: {c.total}</span>{/each}
			{#if inbox}<span class="chip warn" title={msg('ordersPage.webhookInbox')}>{msg('ordersPage.inbox')} <bdi dir="ltr">{inbox.pending}/{inbox.retry}/{inbox.blocked}</bdi></span>{/if}
		</div>
	{/if}
	{#if status === 'loading'}
		<Skeleton />
	{:else if status === 'error'}
		<WidgetError status={errStatus} {onretry} />
	{:else if orders.length === 0}
		<EmptyState ar="لا توجد طلبات عبر الإنترنت" en="No online orders yet" />
	{:else}
		<table class="data">
			<thead><tr><th>#</th><th>{msg('ordersPage.number')}</th><th>{msg('ordersPage.status')}</th><th>{msg('ordersPage.total')}</th><th>{msg('ordersPage.mapping')}</th><th></th></tr></thead>
			<tbody>
				{#each orders as o (o.provider_key + '/' + o.external_order_id)}
					<tr>
						<td class="num">{o.external_order_id}</td>
						<td class="num">{o.order_number || '—'}</td>
						<td>{o.canonical_status}{#if o.provider_deleted} <span class="tag">{msg('ordersPage.deleted')}</span>{/if}</td>
						<td class="num"><strong>{money(o)}</strong> {o.currency}</td>
						<td>{#if o.mapping_complete}<span class="ok">{msg('ordersPage.complete')}</span>{:else}<span class="warn">{msg('ordersPage.incomplete')} ({o.unmapped_line_count})</span>{/if}</td>
						<td><button class="link" aria-label={msg('ordersPage.viewOrder', { number: o.order_number || o.external_order_id })} onclick={() => onselect(o)}>{msg('ordersPage.view')}</button></td>
					</tr>
				{/each}
			</tbody>
		</table>
		{#if nextCursor}
			<div class="more">
				<button class="link" disabled={more === 'loading'} onclick={onloadmore}>
					{more === 'loading' ? msg('ordersPage.loading') : msg('ordersPage.loadMore')}
				</button>
			</div>
		{/if}
		{#if more === 'error'}
			<WidgetError status={moreErr} onretry={onloadmore} />
		{/if}
	{/if}
	{#if selected}
		<div class="drawer" role="dialog" aria-label={msg('ordersPage.orderDetail')}>
			<div class="drawer-head">
				<strong>{msg('ordersPage.order')} {selected.summary.order_number || selected.summary.external_order_id}</strong>
				<button class="link" onclick={() => onselect(null)}>{msg('ordersPage.close')}</button>
			</div>
			{#if detailStatus === 'loading'}
				<Skeleton />
			{:else if detailStatus === 'error'}
				<WidgetError status={detailErr} onretry={() => onselect(selected.summary)} />
			{:else if detailStatus === 'loaded'}
				{#if !selected.summary.mapping_complete}
					<div class="alert" role="alert">{msg('ordersPage.containsProductsNotMappedToMoonlight')}</div>
				{/if}
				{#if selected.summary.provider_deleted}
					<div class="alert" role="status">{msg('ordersPage.deletedAtProviderLastKnownDetails')}</div>
				{/if}
				<dl class="grid">
					<div><dt>{msg('ordersPage.status')}</dt><dd>{selected.summary.canonical_status} ({selected.summary.provider_status})</dd></div>
					<div><dt>{msg('ordersPage.total')}</dt><dd><strong>{lineMoney(selected.summary.total_minor, selected.summary.currency)}</strong> {selected.summary.currency}</dd></div>
					<div><dt>{msg('ordersPage.customer')}</dt><dd>{selected.customer_first_name} {selected.customer_last_name}</dd></div>
					<div><dt>{msg('ordersPage.contact')}</dt><dd dir="ltr">{selected.customer_email || selected.customer_phone || '—'}</dd></div>
					<div><dt>{msg('ordersPage.payment')}</dt><dd>{selected.payment_method_title || selected.payment_method || '—'}</dd></div>
				</dl>
				<table class="data">
					<thead><tr><th>{msg('ordersPage.item')}</th><th>SKU</th><th>{msg('ordersPage.qty')}</th><th>{msg('ordersPage.total')}</th><th>{msg('ordersPage.mapped')}</th></tr></thead>
					<tbody>
						{#each selected.lines as line}
							<tr>
								<td>{line.name}</td>
								<td>{line.sku || '—'}</td>
								<td class="num">{line.quantity}</td>
								<td class="num">{lineMoney(line.total_minor, selected.summary.currency)}</td>
								<td>{#if line.mapped}<span class="ok">✓</span>{:else}<span class="warn">—</span>{/if}</td>
							</tr>
						{/each}
					</tbody>
				</table>
				{#if selected.status_history.length > 0}
					<ol class="history">
						{#each selected.status_history as h}
							<li>rev {h.order_revision}: {h.canonical_status} ({h.provider_status})</li>
						{/each}
					</ol>
				{/if}
			{/if}
		</div>
	{/if}
</Card>

<style>
	.foot-note {
		font-size: 0.76rem;
	}
	.filters {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 220px));
		gap: 12px;
		margin-bottom: 0.5rem;
	}
	.more {
		margin-top: 0.5rem;
	}
	.chips {
		display: flex;
		gap: 0.4rem;
		flex-wrap: wrap;
		margin-bottom: 0.5rem;
	}
	.chip {
		font-size: 0.75rem;
		padding: 0.1rem 0.5rem;
		border: 1px solid var(--border, #ccc);
		border-radius: 1rem;
	}
	.tag,
	.ok,
	.warn {
		font-size: 0.75rem;
	}
	.ok {
		color: var(--ok, #1a7f37);
	}
	.warn {
		color: var(--warn, #9a6700);
	}
	.alert {
		border: 1px solid var(--warn, #9a6700);
		border-radius: 0.4rem;
		padding: 0.4rem 0.6rem;
		margin-bottom: 0.5rem;
	}
	.link {
		background: none;
		border: none;
		color: var(--link, #0969da);
		cursor: pointer;
		padding: 0;
	}
	.drawer {
		margin-top: 0.75rem;
		border-top: 1px solid var(--border, #ccc);
		padding-top: 0.5rem;
	}
	.drawer-head {
		display: flex;
		justify-content: space-between;
		align-items: center;
		margin-bottom: 0.5rem;
	}
	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(10rem, 1fr));
		gap: 0.4rem 1rem;
		margin: 0.5rem 0;
	}
	.grid > div {
		display: contents;
	}
	.grid dt {
		color: var(--text-muted);
	}
	.grid dd {
		margin: 0;
	}
	.history {
		margin: 0.5rem 0 0 1.2rem;
		padding: 0;
		font-size: 0.82rem;
	}
</style>
