<script lang="ts">
	import type { StoreRow } from '../lib/api.js';

	// StoreSelector: one coherent Store scope control shared by every
	// Store-aware page. Native <select> keeps keyboard access and RTL
	// behavior free; selection is by immutable Store UUID (never name —
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
</script>

<label class="storeselect" dir="rtl">
	<span class="storelabel">المتجر / Store</span>
	<select
		data-testid="store-selector"
		aria-label="Store scope / نطاق المتجر"
		aria-busy={state === 'loading'}
		value={value}
		onchange={(e) => onchange((e.currentTarget as HTMLSelectElement).value)}
	>
		<option value="">كل المتاجر / All Stores</option>
		{#each stores as s (s.store_id)}
			<option value={s.store_id} dir="ltr">
				{s.display_name} ({shortID(s.store_id)})
			</option>
		{/each}
	</select>
	{#if state === 'error'}
		<span class="storeerror" role="alert">تعذر تحميل المتاجر / Stores unavailable</span>
		<button type="button" class="storeretry" data-testid="store-retry" onclick={() => onretry()}>
			إعادة المحاولة / Retry
		</button>
	{/if}
</label>

<style>
	.storeselect {
		display: inline-flex;
		align-items: center;
		gap: 0.5rem;
	}
	.storelabel {
		font-size: 0.85rem;
		opacity: 0.85;
	}
	select {
		font-family: monospace;
		direction: ltr;
		max-width: 16rem;
	}
	.storeerror {
		font-size: 0.78rem;
		color: var(--danger);
	}
	.storeretry {
		background: transparent;
		border: 1px solid var(--border);
		border-radius: var(--radius-control);
		padding: 4px 10px;
		font-size: 0.78rem;
	}
</style>
