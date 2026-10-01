<script lang="ts">
	import type { StoreRow } from '../lib/api.js';

	// StoreSelector: one coherent Store scope control shared by every
	// Store-aware page. Native <select> keeps keyboard access and RTL
	// behavior free; selection is by immutable Store UUID (never name —
	// two Stores may share a display name). Empty value = All Stores.
	let {
		stores,
		value,
		onchange
	}: {
		stores: StoreRow[];
		value: string;
		onchange: (id: string) => void;
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
</label>

<style>
	.storeselect {
		display: flex;
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
</style>
