<script lang="ts">
	import { sub, t } from '../lib/i18n.svelte.js';
	let { ar, en }: { ar: string; en?: string } = $props();
</script>

<div class="bilingual">
	<div class="ar">{t(ar, en ?? ar)}</div>
	{#if sub(en)}<div class="en">{sub(en)}</div>{/if}
</div>

<style>
	.bilingual .ar {
		font-weight: 600;
	}
	.bilingual .en {
		font-size: 0.78rem;
		color: var(--color-muted);
		margin-top: 2px;
	}
</style>
