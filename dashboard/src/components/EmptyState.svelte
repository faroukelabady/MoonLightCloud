<script lang="ts">
	import { t } from '../lib/i18n.svelte.js';
	let { ar, en }: { ar: string; en?: string } = $props();
</script>

<div class="empty muted">
	<div>{t(ar, en ?? ar)}</div>
</div>

<style>
	.empty {
		padding: 16px 4px;
	}
</style>
