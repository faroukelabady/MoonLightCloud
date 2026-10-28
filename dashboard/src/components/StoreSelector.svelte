<script lang="ts">
	import { arabic, tb } from '../lib/i18n.svelte.js';
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
	let options = $derived([
		{ value: '', label: tb('كل المتاجر', 'All Stores') },
		...stores.map((s) => ({ value: s.store_id, label: `${s.display_name} (${shortID(s.store_id)})`, dir: 'ltr' as const }))
	]);
</script>

<div class="storeselect" aria-busy={state === 'loading'}>
	<span class="storelabel" aria-hidden="true">{tb('المتجر', 'Store')}</span>
	<div class="picker">
		<SingleSelect id="store-scope" label={arabic() ? 'Store scope / نطاق المتجر' : 'Store scope'} hideLabel testid="store-selector" {value} {options} {onchange} />
	</div>
	{#if state === 'error'}
		<span class="storeerror" role="alert">{tb('تعذر تحميل المتاجر', 'Stores unavailable')}</span>
		<button type="button" class="storeretry btn-secondary btn-sm" data-testid="store-retry" onclick={() => onretry()}>
			{tb('إعادة المحاولة', 'Retry')}
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
	.storelabel {
		font-size: 0.85rem;
		opacity: 0.85;
	}
	.picker {
		flex: 1 1 12rem;
		min-width: 10rem;
		max-width: 16rem;
	}
	.storeerror {
		font-size: 0.78rem;
		color: var(--danger);
	}
	.storeretry {
		min-height: 30px;
	}
</style>
