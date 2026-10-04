<script lang="ts">
	import Card from './Card.svelte';
	import Skeleton from './Skeleton.svelte';
	import EmptyState from './EmptyState.svelte';
	import WidgetError from './WidgetError.svelte';
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
	const labels: Record<string, { ar: string; en: string }> = {
		CATALOG_MISSING_SKU: { ar: 'بدون رقم SKU', en: 'Missing SKU' },
		CATALOG_MISSING_CATEGORY: { ar: 'بدون فئة', en: 'Missing Category' },
		AVAILABILITY_NOT_READY: { ar: 'التوافر غير جاهز', en: 'Availability not ready' },
		COMMERCE_MAPPING_MISSING: { ar: 'غير مربوط', en: 'Unmapped' },
		COMMERCE_SYNC_AMBIGUOUS: { ar: 'يلحق الانتباه للمزامنة', en: 'Synchronization attention' },
		COMMERCE_STORE_CONFLICT: { ar: 'تعارض ملكية المتجر', en: 'Store ownership conflict' }
	};
	function label(code: string): string {
		const l = labels[code];
		return l ? `${l.ar} / ${l.en}` : code;
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
	<p class="note" role="note">صحة الكتالوج تعكس الحالة الحالية للكتالوج والتكامل؛ فترة التقرير المختارة لا ترشح هذه النتائج.<br /><span class="th-en">Catalog Health reflects current catalog/integration state; the selected reporting period does not filter it.</span></p>
	{#if status === 'loading'}
		<Skeleton />
	{:else if status === 'error'}
		<WidgetError status={errStatus} {onretry} />
	{:else if status === 'empty'}
		<EmptyState ar="لا توجد مشاكل في الكتالوج" en="No catalog issues found" />
	{:else if data}
		<div class="filters" role="group" aria-label="Provider filter / فلتر المزود">
			<label for="health-provider">المزود / <span class="th-en">Provider</span></label>
			<select
				id="health-provider"
				dir="ltr"
				value={provider}
				onchange={(e) => onprovider((e.currentTarget as HTMLSelectElement).value)}
			>
				<option value="">All providers / كل المزودين</option>
				{#each providers as p (p)}
					<option value={p}>{p}</option>
				{/each}
			</select>
		</div>
		<ul class="counts">
			{#each data.counts as c (c.reason_code)}
				<li>
					<span class="num">{formatInt(c.products)}</span>
					— {label(c.reason_code)}
					<span class="code num" dir="ltr">{c.reason_code}</span>
				</li>
			{/each}
		</ul>
		{#if data.detail.length === 0}
			<EmptyState ar="لا توجد مشاكل في الكتالوج ضمن هذا النطاق" en="No catalog issues found in this scope" />
		{:else}
			<div class="table-wrap">
				<table>
					<thead>
						<tr>
							<th scope="col">السبب / <span class="th-en">Reason</span></th>
							<th scope="col">SKU</th>
							<th scope="col">المنتج / <span class="th-en">Product</span></th>
							<th scope="col">المزود / <span class="th-en">Provider</span></th>
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
					عرض أول {formatInt(data.detail_limit)} من النتائج / <span class="th-en">Showing the first {formatInt(data.detail_limit)} rows; summary counts are complete.</span>
				</p>
			{/if}
		{/if}
		<p class="note" role="note">
			للتشخيص فقط — لا إصلاح تلقائي. مخزون المتجر هو المرجع؛ مخزون المزود حالة منشورة فقط.<br />
			<span class="th-en">Diagnostic only — no auto-fix. Retail inventory is authoritative; provider stock is downstream published state. Read-only: providers are never contacted live.</span>
		</p>
	{/if}
</Card>

<style>
	.filters {
		display: flex;
		gap: 0.5rem;
		align-items: center;
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
