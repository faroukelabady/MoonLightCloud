<script lang="ts">
	import { msg, t } from '../lib/i18n.svelte.js';
	import Card from './Card.svelte';
	import Skeleton from './Skeleton.svelte';
	import EmptyState from './EmptyState.svelte';
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

	// Stable reason codes are the API contract; bilingual labels are display only.
	// Phase 17-R0 retired the product-level CATALOG_MISSING_SKU (Product
	// has no SKU authority anywhere — ADR-0049); VARIANT_MISSING_SKU is
	// the single SKU health code.
	const labels: Record<string, { ar: string; en: string }> = {
		CATALOG_MISSING_CATEGORY: { ar: 'بدون فئة', en: 'Missing Category' },
		AVAILABILITY_NOT_READY: { ar: 'التوافر غير جاهز', en: 'Availability not ready' },
		COMMERCE_MAPPING_MISSING: { ar: 'غير مربوط', en: 'Unmapped' },
		COMMERCE_SYNC_AMBIGUOUS: { ar: 'يلحق الانتباه للمزامنة', en: 'Synchronization attention' },
		COMMERCE_STORE_CONFLICT: { ar: 'تعارض ملكية المتجر', en: 'Store ownership conflict' },
		CATEGORY_ONLINE_DISABLED: { ar: 'مغلق عبر الإنترنت بسياسة الفئة', en: 'Disabled online by Category' },
		PRODUCT_NO_ACTIVE_VARIANTS: { ar: 'بدون متغيرات نشطة', en: 'No active variants' },
		PRODUCT_NO_SELLABLE_VARIANT: { ar: 'بدون متغير قابل للبيع', en: 'No sellable variant' },
		VARIANT_DUPLICATE_COMBINATION: { ar: 'تكرار تركيبة المتغير', en: 'Duplicate variant combination' },
		VARIANT_MISSING_SKU: { ar: 'المتغير بدون رقم SKU', en: 'Variant missing SKU' },
		VARIANT_MAPPING_MISSING: { ar: 'المتغير غير مربوط', en: 'Variant unmapped' },
		VARIANT_INTENTIONALLY_OFFLINE: { ar: 'المتغير مغلق عمدًا', en: 'Variant intentionally offline' }
	};
	function label(code: string): string {
		const l = labels[code];
		return l ? t(l.ar, l.en) : code;
	}
	// Provider options come from durable rows only (never invented).
	let providers = $derived(
		Array.from(
			new Set([
				...(data?.providers ?? []),
				...(data?.provider_key ? [data.provider_key] : [])
			])
		).sort()
	);
</script>

<Card ar="صحة الكتالوج (تشخيص)" en="Catalog Health (diagnostics)">
	<p class="note" role="note">{msg('catalogHealthCard.catalogHealthReflectsCurrentCatalogIntegration')}</p>
	{#if status === 'loading'}
		<Skeleton />
	{:else if status === 'error'}
		<WidgetError status={errStatus} {onretry} />
	{:else if data}
		<div class="filters" role="group" aria-label={msg('catalogHealthCard.providerFilter')}>
			<SingleSelect
				id="health-provider"
				label={msg('catalogHealthCard.provider')}
				value={provider}
				options={[{ value: '', label: msg('catalogHealthCard.allProviders') }, ...providers.map((p) => ({ value: p, label: p, dir: 'ltr' as const }))]}
				onchange={onprovider}
			/>
		</div>
		{#if status === 'empty'}
			<EmptyState ar="لا توجد مشاكل في الكتالوج ضمن هذا النطاق" en="No catalog issues found in this scope" />
		{/if}
		<ul class="counts">
			{#each data.counts as c (c.reason_code)}
				<li>
					<span class="num">{formatInt(c.products)}</span>
					— {label(c.reason_code)}
					<span class="code num" dir="ltr">{c.reason_code}</span>
				</li>
			{/each}
		</ul>
		{#if data.detail.length > 0}
			<div class="table-wrap">
				<table>
					<thead>
						<tr>
							<th scope="col">{msg('catalogHealthCard.reason')}</th>
							<th scope="col">SKU</th>
							<th scope="col">{msg('catalogHealthCard.product')}</th>
							<th scope="col">{msg('catalogHealthCard.provider')}</th>
						</tr>
					</thead>
					<tbody>
						{#each data.detail as item, i (`${item.reason_code}-${item.product_id ?? i}-${item.provider_key}`)}
							<tr>
								<td>{label(item.reason_code)}</td>
								<td class="num" dir="ltr">{item.sku ?? '—'}</td>
								<td>{item.name ?? '—'}{#if item.product_id}<br /><span class="num th-en" dir="ltr">{item.product_id}</span>{/if}</td>
								<td class="num" dir="ltr">{item.provider_key || '—'}</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
			{#if data.detail_truncated}
				<p class="note" role="status">
					{msg('catalogHealthCard.truncated', { count: formatInt(data.detail_limit) })}
				</p>
			{/if}
		{/if}
		<p class="note" role="note">
			{msg('catalogHealthCard.diagnosticOnlyNoAutoFixRetail')}
		</p>
	{/if}
</Card>

<style>
	.filters {
		max-width: 260px;
		margin-bottom: 0.5rem;
	}
	.counts {
		list-style: none;
		padding: 0;
		margin: 0 0 0.5rem;
	}
	.counts li {
		padding: 0.15rem 0;
	}
	.code {
		opacity: 0.65;
		font-size: 0.78em;
	}
	.table-wrap {
		overflow-x: auto;
	}
	table {
		width: 100%;
		border-collapse: collapse;
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
