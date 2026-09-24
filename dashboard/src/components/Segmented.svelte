<script lang="ts">
	let {
		options,
		value,
		onchange,
		label,
		size
	}: {
		options: { value: string; ar: string; en?: string; aria?: string }[];
		value: string;
		onchange: (v: string) => void;
		label?: string;
		size?: 'md' | 'sm';
	} = $props();
</script>

{#if label}<div class="muted label-gap">{label}</div>{/if}
<div class="segmented" class:sm={size === 'sm'} role="group">
	{#each options as o}
		<button
			type="button"
			class:active={o.value === value}
			aria-pressed={o.value === value}
			aria-label={o.aria ?? o.ar}
			onclick={() => onchange(o.value)}
		>
			{o.ar}
		</button>
	{/each}
</div>

<style>
	.segmented {
		display: inline-flex;
		border: 1px solid var(--border);
		border-radius: var(--radius-control);
		overflow: hidden;
		background: var(--surface-muted);
		padding: 2px;
		gap: 2px;
		max-width: 100%;
	}
	.segmented button {
		border: 0;
		background: transparent;
		border-radius: 4px;
		padding: 4px 12px;
		font-size: 0.8rem;
		color: var(--text-muted);
		white-space: nowrap;
	}
	.segmented button.active {
		background: var(--primary);
		color: #fff;
		font-weight: 600;
	}
	.segmented.sm button {
		padding: 3px 8px;
		font-size: 0.72rem;
	}
</style>
