<script lang="ts">
	let { ar, en }: { ar: string; en?: string } = $props();
</script>

<div class="bilingual">
	<div class="ar">{ar}</div>
	{#if en}<div class="en">{en}</div>{/if}
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
