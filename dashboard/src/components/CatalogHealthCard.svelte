<script lang="ts">
	import { msg } from '../lib/i18n.svelte.js';
	import Card from './Card.svelte';
	import Skeleton from './Skeleton.svelte';
	import WidgetError from './WidgetError.svelte';
	import SingleSelect from './ui/SingleSelect.svelte';
	import { formatInt } from '../lib/money.js';
	import type { CatalogHealthResponse } from '../lib/api.js';

	let {
		data,
		status,
		errStatus,
		onretry,
		provider,
		onprovider
	}: {
		data: CatalogHealthResponse | null;
		status: 'loading' | 'loaded' | 'empty' | 'error';
		errStatus: number | null;
		onretry: () => void;
		provider: string;
		onprovider: (provider: string) => void;
	} = $props();

	// Stable reason codes are the API contract; each one shows a short
	// problem and one action in plain words. An unknown code shows as is.
	const known = new Set([
		'CATALOG_MISSING_CATEGORY',
		'AVAILABILITY_NOT_READY',
		'COMMERCE_MAPPING_MISSING',
		'COMMERCE_SYNC_AMBIGUOUS',
		'COMMERCE_STORE_CONFLICT',
		'CATEGORY_ONLINE_DISABLED',
		'PRODUCT_NO_ACTIVE_VARIANTS',
		'PRODUCT_NO_SELLABLE_VARIANT',
		'VARIANT_DUPLICATE_COMBINATION',
		'VARIANT_MISSING_SKU',
		'VARIANT_MAPPING_MISSING',
		'VARIANT_INTENTIONALLY_OFFLINE'
	]);
	function label(code: string): string {
		return known.has(code) ? msg(`catalogHealthCard.reasons.${code}.label`) : code;
	}
	function fix(code: string): string {
		return known.has(code) ? msg(`catalogHealthCard.reasons.${code}.fix`) : '';
	}
	// Provider options come from durable rows only (never invented).
	let providers = $derived(Array.from(new Set([...(data?.providers ?? []), ...(data?.provider_key ? [data.provider_key] : [])])).sort());
	let counts = $derived((data?.counts ?? []).filter((c) => c.products > 0).sort((a, b) => b.products - a.products));
</script>

<Card ar={msg('catalogHealthCard.title')}>
	{#if status === 'loading'}
		<Skeleton />
	{:else if status === 'error'}
		<WidgetError status={errStatus} {onretry} />
	{:else if data}
		<p class="intro">{msg('catalogHealthCard.intro')}</p>
		{#if providers.length > 1}
			<div class="filters" role="group" aria-label={msg('catalogHealthCard.providerFilter')}>
				<SingleSelect
					id="health-provider"
					label={msg('catalogHealthCard.provider')}
					value={provider}
					options={[{ value: '', label: msg('catalogHealthCard.allProviders') }, ...providers.map((p) => ({ value: p, label: p, dir: 'ltr' as const }))]}
					onchange={onprovider}
				/>
			</div>
		{/if}
		{#if counts.length === 0}
			<p class="clean" role="status" data-testid="catalog-clean">
				<span class="okic" aria-hidden="true"><svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"><path d="M4 12.5l5 5L20 6.5" /></svg></span>
				{msg('catalogHealthCard.noIssues')}
			</p>
		{:else}
			<ul class="issues" data-testid="catalog-issues">
				{#each counts as c (c.reason_code)}
					<li>
						<span class="badge num">{formatInt(c.products)}</span>
						<span class="what">
							<strong>{label(c.reason_code)}</strong>
							{#if fix(c.reason_code)}<span class="muted fix">{fix(c.reason_code)}</span>{/if}
						</span>
					</li>
				{/each}
			</ul>
		{/if}
		{#if data.detail.length > 0}
			<details class="products">
				<summary>{msg('catalogHealthCard.showProducts', { count: formatInt(data.detail.length) })}</summary>
				<div class="table-wrap">
					<table class="data">
						<thead>
							<tr>
								<th scope="col">{msg('catalogHealthCard.product')}</th>
								<th scope="col">SKU</th>
								<th scope="col">{msg('catalogHealthCard.problem')}</th>
								{#if providers.length > 1}<th scope="col">{msg('catalogHealthCard.provider')}</th>{/if}
							</tr>
						</thead>
						<tbody>
							{#each data.detail as item, i (`${item.reason_code}-${item.product_id ?? i}-${item.provider_key}`)}
								<tr>
									<td>{item.name ?? '—'}</td>
									<td class="num" dir="ltr">{item.sku ?? '—'}</td>
									<td>{label(item.reason_code)}</td>
									{#if providers.length > 1}<td class="num" dir="ltr">{item.provider_key || '—'}</td>{/if}
								</tr>
							{/each}
						</tbody>
					</table>
				</div>
				{#if data.detail_truncated}
					<p class="note" role="status">{msg('catalogHealthCard.truncated', { count: formatInt(data.detail_limit) })}</p>
				{/if}
			</details>
		{/if}
		<p class="note">{msg('catalogHealthCard.footnote')}</p>
	{/if}
</Card>

<style>
	.intro {
		margin: 0 0 8px;
		font-size: 0.84rem;
	}
	.filters {
		max-width: 260px;
		margin-bottom: 8px;
	}
	.clean {
		display: flex;
		align-items: center;
		gap: 8px;
		margin: 0;
		padding: 8px 10px;
		border-radius: var(--radius-control);
		background: #e8f6ee;
		color: #15803d;
		font-weight: 700;
	}
	.okic {
		width: 22px;
		height: 22px;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		border-radius: 50%;
		background: var(--success);
		color: #fff;
	}
	.issues {
		list-style: none;
		margin: 0;
		padding: 0;
	}
	.issues li {
		display: flex;
		gap: 10px;
		align-items: flex-start;
		padding: 7px 0;
		border-bottom: 1px solid var(--border);
	}
	.issues li:last-child {
		border-bottom: 0;
	}
	.badge {
		min-width: 2.2rem;
		padding: 2px 8px;
		border-radius: 999px;
		background: #fdf4e7;
		color: #92400e;
		font-weight: 800;
		text-align: center;
	}
	.what {
		display: flex;
		flex-direction: column;
		font-size: 0.86rem;
	}
	.fix {
		font-size: 0.78rem;
	}
	.products {
		margin-top: 8px;
	}
	.products summary {
		cursor: pointer;
		font-weight: 600;
		color: var(--primary);
		font-size: 0.84rem;
	}
	.table-wrap {
		overflow-x: auto;
		margin-top: 6px;
	}
	.note {
		margin: 8px 0 0;
		font-size: 0.76rem;
		color: var(--text-muted);
	}
</style>
