<script lang="ts">
	import { t } from '../lib/i18n.svelte.js';
	let { ar, en }: { ar: string; en?: string } = $props();
</script>

<div class="bilingual">
	<div class="ar">{t(ar, en ?? ar)}</div>
</div>

<style>
	.bilingual .ar {
		font-weight: 600;
	}
</style>
