<script lang="ts">
	import Bilingual from './Bilingual.svelte';
	let { ar, en }: { ar: string; en?: string } = $props();
</script>

<div class="card">
	<Bilingual {ar} {en} />
	<div class="body">
		<slot />
	</div>
</div>

<style>
	.body {
		margin-top: 12px;
	}
</style>
