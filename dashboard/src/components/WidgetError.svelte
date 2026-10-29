<script lang="ts">
	import { arabic, msg, msgb } from '../lib/i18n.svelte.js';
	let { status, onretry }: { status: number | null; onretry: () => void } = $props();
</script>

<div class="werr" role="alert">
	{#if status === 503}
		<div>{msg('widgetError.reportingIsTemporarilyUnavailable')}</div>
		{#if arabic()}<div class="muted sub-en">Reporting is temporarily unavailable</div>{/if}
		<button type="button" onclick={onretry}>{msgb('widgetError.retry')}</button>
	{:else}
		<div>{msg('widgetError.couldNotLoadData')}</div>
		{#if arabic()}<div class="muted sub-en">Could not load data</div>{/if}
	{/if}
</div>

<style>
	.werr {
		padding: 12px 4px;
	}
	.werr button {
		margin-top: 8px;
		background: var(--color-primary);
		color: #fff;
		border: 0;
		border-radius: 6px;
		padding: 7px 14px;
	}
</style>
