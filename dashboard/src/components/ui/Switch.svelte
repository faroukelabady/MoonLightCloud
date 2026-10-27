<script lang="ts">
	// An on/off setting: a labelled switch with an explanation under it.
	let {
		id,
		label,
		description = '',
		checked,
		onchange,
		disabled = false
	}: { id: string; label: string; description?: string; checked: boolean; onchange: (next: boolean) => void; disabled?: boolean } = $props();
</script>

<div class="switch-row">
	<div class="text">
		<label for={id} class="name">{label}</label>
		{#if description}<p id={`${id}-desc`} class="desc">{description}</p>{/if}
	</div>
	<button
		{id}
		type="button"
		role="switch"
		class="switch"
		class:on={checked}
		aria-checked={checked}
		aria-label={label}
		aria-describedby={description ? `${id}-desc` : undefined}
		{disabled}
		onclick={() => onchange(!checked)}
	><span class="knob" aria-hidden="true"></span></button>
</div>

<style>
	.switch-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 16px;
		padding: 12px 0;
		border-bottom: 1px solid var(--border);
	}
	.switch-row:last-child {
		border-bottom: 0;
	}
	.name {
		font-weight: 700;
		font-size: 0.9rem;
		cursor: pointer;
	}
	.desc {
		margin: 3px 0 0;
		color: var(--text-muted);
		font-size: 0.8rem;
		line-height: 1.5;
	}
	.switch {
		position: relative;
		flex-shrink: 0;
		width: 44px;
		height: 24px;
		padding: 0;
		border: 0;
		border-radius: 999px;
		background: #cbd5e1;
		transition: background 120ms ease;
	}
	.switch.on {
		background: var(--button-primary);
	}
	.knob {
		position: absolute;
		inset-block-start: 3px;
		inset-inline-start: 3px;
		width: 18px;
		height: 18px;
		border-radius: 50%;
		background: #fff;
		box-shadow: 0 1px 2px #0003;
		transition: inset-inline-start 120ms ease;
	}
	.on .knob {
		inset-inline-start: 23px;
	}
	.switch:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}
</style>
