<script lang="ts">
	// An amount typed in major units with the currency shown inside the
	// field, as in MoonLightRetail. The value stays text; callers parse it.
	let {
		id,
		label,
		value = $bindable(''),
		currency = 'EGP',
		invalid = false,
		testid
	}: { id: string; label: string; value?: string; currency?: string; invalid?: boolean; testid?: string } = $props();
</script>

<label class="field" for={id}>
	<span>{label}</span>
	<span class="money-field" class:invalid>
		<span class="currency" aria-hidden="true">{currency}</span>
		<input {id} inputmode="decimal" autocomplete="off" dir="ltr" data-testid={testid} aria-invalid={invalid ? 'true' : undefined} bind:value />
	</span>
</label>

<style>
	.money-field {
		display: flex;
		align-items: stretch;
		min-height: 38px;
		border: 1px solid var(--field-border);
		border-radius: 8px;
		background: var(--surface);
		overflow: hidden;
	}
	.money-field:focus-within {
		border-color: var(--field-focus);
		box-shadow: var(--field-ring);
	}
	.money-field.invalid {
		border-color: var(--field-invalid);
		background: var(--field-invalid-bg);
	}
	.currency {
		display: flex;
		align-items: center;
		padding-inline: 10px;
		border-inline-end: 1px solid #edf0f4;
		background: var(--surface-muted);
		color: #526078;
		font-size: 0.78rem;
		font-weight: 700;
	}
	input {
		flex: 1;
		min-width: 0;
		border: 0;
		border-radius: 0;
		box-shadow: none;
		background: transparent;
	}
	input:focus,
	input:focus-visible {
		box-shadow: none;
	}
</style>
