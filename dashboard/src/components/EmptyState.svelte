<script lang="ts">
	let { ar, en }: { ar: string; en?: string } = $props();
</script>

<div class="empty muted">
	<div>{ar}</div>
	{#if en}<div class="sub-en">{en}</div>{/if}
</div>

<style>
	.empty {
		padding: 16px 4px;
	}
</style>
