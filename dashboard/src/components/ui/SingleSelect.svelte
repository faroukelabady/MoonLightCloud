<script lang="ts" module>
	export type SelectOption = { value: string; label: string; disabled?: boolean; dir?: 'ltr' | 'rtl' };
</script>

<script lang="ts">
	// MoonLightRetail's single-value selector: a white field with a chevron
	// that opens a list of options, the chosen one ticked. The trigger
	// carries the chosen value in data-value for tests and e2e.

	let {
		id,
		label,
		value,
		options,
		onchange,
		disabled = false,
		hideLabel = false,
		invalid = false,
		testid,
		describedBy
	}: {
		id: string;
		label: string;
		value: string;
		options: SelectOption[];
		onchange: (value: string) => void;
		disabled?: boolean;
		hideLabel?: boolean;
		invalid?: boolean;
		testid?: string;
		describedBy?: string;
	} = $props();

	let open = $state(false);
	let selected = $derived(options.find((o) => o.value === value));
	let triggerID = $derived(`${id}-trigger`);
	let popupID = $derived(`${id}-popup`);

	function focusOption(target: 'selected' | 'first' | 'last') {
		requestAnimationFrame(() => {
			const popup = document.getElementById(popupID);
			const items = Array.from(popup?.querySelectorAll<HTMLElement>('[role="option"]:not([disabled])') ?? []);
			const chosen = target === 'selected' ? (items.find((el) => el.getAttribute('aria-selected') === 'true') ?? items[0]) : target === 'first' ? items[0] : items.at(-1);
			chosen?.focus();
		});
	}

	function toggle() {
		open = !open;
		if (open) focusOption('selected');
	}

	function triggerKeydown(event: KeyboardEvent) {
		if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return;
		event.preventDefault();
		open = true;
		focusOption('selected');
	}

	function popupKeydown(event: KeyboardEvent) {
		const items = Array.from((event.currentTarget as HTMLElement).querySelectorAll<HTMLElement>('[role="option"]:not([disabled])'));
		const at = items.indexOf(document.activeElement as HTMLElement);
		if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
			event.preventDefault();
			const next = event.key === 'ArrowDown' ? Math.min(at + 1, items.length - 1) : Math.max(at - 1, 0);
			items[next]?.focus();
		} else if (event.key === 'Home' || event.key === 'End') {
			event.preventDefault();
			focusOption(event.key === 'Home' ? 'first' : 'last');
		} else if (event.key === 'Escape') {
			event.preventDefault();
			close();
		}
	}

	function close() {
		open = false;
		requestAnimationFrame(() => document.getElementById(triggerID)?.focus());
	}

	function choose(next: string) {
		if (next !== value) onchange(next);
		close();
	}

	function closeWhenFocusLeaves(event: FocusEvent) {
		const next = event.relatedTarget as Node | null;
		if (!next || !(event.currentTarget as HTMLElement).contains(next)) open = false;
	}
</script>

<div class="single-select" class:invalid onfocusout={closeWhenFocusLeaves}>
	<span class="field-label" class:visually-hidden={hideLabel} id={`${id}-label`}>{label}</span>
	<div class="control" class:expanded={open}>
		<button
			id={triggerID}
			type="button"
			class="trigger"
			data-testid={testid}
			data-value={value}
			aria-haspopup="listbox"
			aria-expanded={open}
			aria-controls={popupID}
			aria-labelledby={`${id}-label ${triggerID}`}
			aria-describedby={describedBy}
			{disabled}
			onclick={toggle}
			onkeydown={triggerKeydown}
		>
			<span class="prompt" dir={selected?.dir}>{selected?.label ?? ''}</span>
			<span class="chevron" aria-hidden="true"></span>
		</button>
	</div>
	{#if open}
		<div id={popupID} class="popup" role="listbox" aria-labelledby={`${id}-label`} tabindex="-1" onkeydown={popupKeydown}>
			{#each options as option (option.value)}
				<button
					type="button"
					role="option"
					data-value={option.value}
					dir={option.dir}
					aria-selected={value === option.value}
					class:selected={value === option.value}
					disabled={option.disabled}
					onclick={() => choose(option.value)}>{option.label}</button
				>
			{/each}
		</div>
	{/if}
</div>

<style>
	.single-select {
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
		transition: border-color 120ms ease, box-shadow 120ms ease;
	}
	.control:hover {
		border-color: var(--field-border-hover);
	}
	.control:focus-within,
	.control.expanded {
		border-color: var(--field-focus);
		box-shadow: var(--field-ring);
	}
	.invalid .control {
		border-color: var(--field-invalid);
		background: var(--field-invalid-bg);
		box-shadow: 0 0 0 3px #c8433515;
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
		border-radius: 8px;
		background: transparent;
		color: var(--text);
		font-size: 0.88rem;
		text-align: start;
		cursor: pointer;
	}
	.trigger:focus-visible {
		outline: none;
	}
	.trigger:disabled {
		cursor: not-allowed;
		opacity: 0.6;
	}
	.prompt {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.chevron {
		flex-shrink: 0;
		width: 8px;
		height: 8px;
		margin-top: -4px;
		border-right: 1.6px solid #34445e;
		border-bottom: 1.6px solid #34445e;
		transform: rotate(45deg);
		transition: transform 120ms ease;
	}
	.expanded .chevron {
		margin-top: 4px;
		transform: rotate(225deg);
	}
	.popup {
		position: absolute;
		z-index: 40;
		inset-block-start: 100%;
		inset-inline: 0;
		min-width: 100%;
		max-height: 300px;
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
	.popup button {
		padding: 8px 10px;
		border: 0;
		border-radius: 7px;
		background: transparent;
		color: #34445e;
		font-size: 0.86rem;
		text-align: start;
		cursor: pointer;
	}
	.popup button:hover,
	.popup button:focus-visible,
	.popup button.selected {
		background: #f0f7ff;
		color: #1260ad;
		outline: none;
	}
	.popup button.selected::before {
		content: '✓';
		margin-inline-end: 8px;
		font-weight: 800;
	}
	.popup button:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}
	.visually-hidden {
		position: absolute;
		width: 1px;
		height: 1px;
		overflow: hidden;
		clip-path: inset(50%);
		white-space: nowrap;
	}
</style>
