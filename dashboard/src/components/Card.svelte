<script lang="ts">
	import Bilingual from './Bilingual.svelte';
	import type { Snippet } from 'svelte';
	let { ar, en, actions, children }: { ar: string; en?: string; actions?: Snippet; children?: Snippet } = $props();
</script>

<div class="card">
	<div class="card-head">
		<Bilingual {ar} {en} />
		{#if actions}<div class="actions">{@render actions()}</div>{/if}
	</div>
	<div class="body">
		{#if children}{@render children()}{/if}
	</div>
</div>

<style>
	.card-head {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: 8px;
	}
	.body {
		margin-top: 10px;
	}
</style>
