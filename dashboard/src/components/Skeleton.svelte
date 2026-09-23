<script lang="ts">
	let { lines = 3 }: { lines?: number } = $props();
</script>

<div aria-busy="true" aria-label="جارٍ التحميل / Loading">
	{#each Array(lines) as _, i (i)}
		<div class="skeleton" style="height: 14px; margin: 8px 0;"></div>
	{/each}
</div>
