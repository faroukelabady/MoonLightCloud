<script lang="ts">
	let {
		options,
		value,
		onchange,
		label
	}: {
		options: { value: string; ar: string; en?: string }[];
		value: string;
		onchange: (v: string) => void;
		label?: string;
	} = $props();
</script>

{#if label}<div class="muted label-gap">{label}</div>{/if}
<div class="segmented" role="group">
	{#each options as o}
		<button
			type="button"
			class:active={o.value === value}
			aria-pressed={o.value === value}
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
		border-radius: 8px;
		overflow: hidden;
		background: #f8fafc;
	}
	.segmented button {
		border: 0;
		background: transparent;
		padding: 6px 14px;
		color: var(--color-muted);
	}
	.segmented button.active {
		background: var(--color-primary);
		color: #fff;
	}
</style>
