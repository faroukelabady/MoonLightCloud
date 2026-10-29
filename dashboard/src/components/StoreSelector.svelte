<script lang="ts">
	import { msg } from '../lib/i18n.svelte.js';
	import type { StoreRow } from '../lib/api.js';
	import SingleSelect from './ui/SingleSelect.svelte';

	// StoreSelector: one coherent Store scope control shared by every
	// Store-aware page, using the shared Retail-style selector; selection
	// is by immutable Store UUID (never name —
	// two Stores may share a display name). Empty value = All Stores.
	// state='error' surfaces a registry failure with an explicit retry;
	// the current selection is never silently widened to global.
	let {
		stores,
		value,
		state = 'loaded',
		onchange,
		onretry = () => {}
	}: {
		stores: StoreRow[];
		value: string;
		state?: 'idle' | 'loading' | 'loaded' | 'error';
		onchange: (id: string) => void;
		onretry?: () => void;
	} = $props();

	function shortID(id: string): string {
		return id.length > 13 ? `${id.slice(0, 8)}…` : id;
	}
	// Store names only; a short ID is added just when two Stores share a
	// name, so the choices stay distinguishable.
	let options = $derived.by(() => {
		const counts = new Map<string, number>();
		for (const s of stores) counts.set(s.display_name, (counts.get(s.display_name) ?? 0) + 1);
		return [
			{ value: '', label: msg('storeSelector.allStores') },
			...stores.map((s) => ({
				value: s.store_id,
				label: (counts.get(s.display_name) ?? 0) > 1 ? `${s.display_name} (${shortID(s.store_id)})` : s.display_name
			}))
		];
	});
</script>

<div class="storeselect" aria-busy={state === 'loading'}>
	<div class="picker">
		<SingleSelect id="store-scope" label={msg('storeSelector.storeScope')} hideLabel testid="store-selector" {value} {options} {onchange} />
	</div>
	{#if state === 'error'}
		<span class="storeerror" role="alert">{msg('storeSelector.storesUnavailable')}</span>
		<button type="button" class="storeretry btn-secondary btn-sm" data-testid="store-retry" onclick={() => onretry()}>
			{msg('storeSelector.retry')}
		</button>
	{/if}
</div>

<style>
	/* Wraps instead of overflowing: the error and Retry move under the
	   selector rather than sliding beneath the account controls. */
	.storeselect {
		display: inline-flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.5rem;
		min-width: 0;
		max-width: 100%;
	}
	.picker {
		flex: 1 1 12rem;
		min-width: 10rem;
		max-width: 13rem;
	}
	.storeerror {
		font-size: 0.78rem;
		color: var(--danger);
	}
	.storeretry {
		min-height: 30px;
	}
</style>
