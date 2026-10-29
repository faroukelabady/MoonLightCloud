<script lang="ts">
	import { msgb } from '../lib/i18n.svelte.js';
	let { lines = 3 }: { lines?: number } = $props();
</script>

<div aria-busy="true" aria-label={msgb('skeleton.loading')}>
	{#each Array(lines) as _, i (i)}
		<div class="skeleton skel-line"></div>
	{/each}
</div>
