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
	/* The body scrolls when a height cap (the overview grid) is reached, so a
	   long list never stretches its row; the title stays in view. */
	.card {
		display: flex;
		flex-direction: column;
		min-height: 0;
	}
	.card-head {
		flex-shrink: 0;
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: 8px;
	}
	.body {
		flex: 1;
		min-height: 0;
		margin-top: 10px;
		overflow: auto;
		scrollbar-width: thin;
	}
	.body :global(table.data thead th) {
		position: sticky;
		top: 0;
		z-index: 1;
		background: var(--surface);
	}
</style>
