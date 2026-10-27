<script lang="ts">
	import type { SelectOption } from './SingleSelect.svelte';

	// MoonLightRetail's multi-value selector: the chosen values show as chips
	// under the field (click a chip to remove it) and the list opens with a
	// checkbox per option. Empty means "all" (emptyLabel).
	let {
		id,
		label,
		options,
		selected,
		onchange,
		emptyLabel,
		disabled = false,
		testid
	}: {
		id: string;
		label: string;
		options: SelectOption[];
		selected: string[];
		onchange: (values: string[]) => void;
		emptyLabel: string;
		disabled?: boolean;
		testid?: string;
	} = $props();

	let open = $state(false);
	let chosen = $derived(options.filter((o) => selected.includes(o.value)));

	function toggleValue(value: string) {
		onchange(selected.includes(value) ? selected.filter((v) => v !== value) : [...selected, value]);
	}

	function closeWhenFocusLeaves(event: FocusEvent) {
		const next = event.relatedTarget as Node | null;
		if (!next || !(event.currentTarget as HTMLElement).contains(next)) open = false;
	}

	function popupKeydown(event: KeyboardEvent) {
		if (event.key !== 'Escape') return;
		event.preventDefault();
		open = false;
		requestAnimationFrame(() => document.getElementById(`${id}-trigger`)?.focus());
	}
</script>

<div class="multi-select" onfocusout={closeWhenFocusLeaves}>
	<span class="field-label" id={`${id}-label`}>{label}</span>
	<div class="control" class:expanded={open}>
		<button
			id={`${id}-trigger`}
			type="button"
			class="trigger"
			data-testid={testid}
			aria-haspopup="listbox"
			aria-expanded={open}
			aria-controls={`${id}-popup`}
			aria-labelledby={`${id}-label ${id}-trigger`}
			{disabled}
			onclick={() => (open = !open)}
		>
			<span class="prompt">{chosen.length === 0 ? emptyLabel : `${chosen.length} / ${options.length}`}</span>
			<span class="chevron" aria-hidden="true"></span>
		</button>
		{#if chosen.length > 0}
			<div class="chips">
				{#each chosen as option (option.value)}
					<button type="button" class="chip" aria-label={`${label}: ${option.label} ×`} onclick={() => toggleValue(option.value)}>
						<span dir={option.dir}>{option.label}</span><span aria-hidden="true">×</span>
					</button>
				{/each}
			</div>
		{/if}
	</div>
	{#if open}
		<div id={`${id}-popup`} class="popup" role="listbox" aria-multiselectable="true" aria-labelledby={`${id}-label`} tabindex="-1" onkeydown={popupKeydown}>
			{#each options as option (option.value)}
				<label class="option" class:checked={selected.includes(option.value)}>
					<input type="checkbox" data-value={option.value} checked={selected.includes(option.value)} onchange={() => toggleValue(option.value)} />
					<span dir={option.dir}>{option.label}</span>
				</label>
			{:else}
				<p class="empty">{emptyLabel}</p>
			{/each}
		</div>
	{/if}
</div>

<style>
	.multi-select {
		position: relative;
		display: grid;
		gap: 6px;
		min-width: 0;
	}
	.field-label {
		font-size: 0.78rem;
		font-weight: 600;
		color: var(--field-label);
	}
	.control {
		border: 1px solid var(--field-border);
		border-radius: 8px;
		background: var(--surface);
		overflow: hidden;
	}
	.control:hover {
		border-color: var(--field-border-hover);
	}
	.control:focus-within,
	.control.expanded {
		border-color: var(--field-focus);
		box-shadow: var(--field-ring);
	}
	.trigger {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 10px;
		width: 100%;
		min-height: 38px;
		padding: 8px 12px;
		border: 0;
		background: transparent;
		color: var(--text);
		font-size: 0.88rem;
		text-align: start;
		cursor: pointer;
	}
	.trigger:focus-visible {
		outline: none;
	}
	.chevron {
		flex-shrink: 0;
		width: 8px;
		height: 8px;
		margin-top: -4px;
		border-right: 1.6px solid #34445e;
		border-bottom: 1.6px solid #34445e;
		transform: rotate(45deg);
	}
	.expanded .chevron {
		margin-top: 4px;
		transform: rotate(225deg);
	}
	.chips {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
		max-height: 86px;
		overflow-y: auto;
		padding: 8px 10px;
		border-top: 1px solid #edf0f4;
	}
	.chip {
		display: inline-flex;
		align-items: center;
		gap: 5px;
		padding: 3px 9px;
		border: 1px solid #1971cf;
		border-radius: 999px;
		background: #eff7ff;
		color: #1260ad;
		font-size: 0.78rem;
		cursor: pointer;
	}
	.popup {
		position: absolute;
		z-index: 40;
		inset-block-start: 100%;
		inset-inline: 0;
		max-height: 280px;
		overflow: auto;
		display: grid;
		gap: 2px;
		margin-top: 5px;
		padding: 6px;
		border: 1px solid #bfc8d4;
		border-radius: 8px;
		background: var(--surface);
		box-shadow: 0 14px 30px #16243b24;
	}
	.option {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 7px 9px;
		border-radius: 7px;
		color: #34445e;
		font-size: 0.86rem;
		cursor: pointer;
	}
	.option:hover,
	.option.checked {
		background: #f0f7ff;
		color: #1260ad;
	}
	.empty {
		margin: 8px;
		color: var(--text-muted);
		font-size: 0.82rem;
	}
</style>
