<script lang="ts">
	import type { Snippet } from 'svelte';
	import type { ReportShop } from '../lib/reportsApi.js';
	import { lang, t } from '../lib/i18n.svelte.js';

	// Printed only: report title, the shop's letterhead (from its newest
	// synced Sale) and the facts row passed in as <div><dt/><dd/></div>.
	// storeName is empty for the all-Stores scope, which has no single shop.
	let { title, shop, storeName, children }: { title: string; shop: ReportShop | null; storeName: string; children: Snippet } = $props();

	function pick(ar: string, en: string) {
		return (lang() === 'ar' ? ar || en : en || ar).trim();
	}
	let brand = $derived(storeName ? (shop ? pick(shop.name_ar, shop.name_en) : '') || storeName : t('كل المتاجر', 'All Stores'));
	let address = $derived(storeName && shop ? pick(shop.address_ar, shop.address_en) : '');
</script>

<header class="report-print-header">
	<div class="report-print-masthead">
		<h1>{title}</h1>
		<div class="report-shop">
			<p class="report-brand">{brand}</p>
			{#if address}<p>{address}</p>{/if}
			{#if storeName && shop?.phone}<p class="mono" dir="ltr">{shop.phone}</p>{/if}
			<p class="muted">MoonLightCloud</p>
		</div>
	</div>
	<dl>
		{@render children()}
	</dl>
</header>
