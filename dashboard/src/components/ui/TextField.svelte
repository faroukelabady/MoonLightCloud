<script lang="ts">
	import type { HTMLInputAttributes } from 'svelte/elements';

	// A labelled text input in MoonLightRetail's style, with an optional
	// hint and error under it. Other input attributes pass through.
	let {
		label,
		value = $bindable(''),
		hint = '',
		error = '',
		id,
		...rest
	}: { label: string; value?: string | number; hint?: string; error?: string; id: string } & Omit<HTMLInputAttributes, 'value' | 'id'> = $props();
</script>

<label class="field" for={id}>
	<span>{label}</span>
	<input {id} bind:value aria-invalid={error ? 'true' : undefined} aria-describedby={hint || error ? `${id}-note` : undefined} {...rest} />
	{#if error}<span id={`${id}-note`} class="field-error" role="alert">{error}</span>
	{:else if hint}<span id={`${id}-note`} class="field-hint">{hint}</span>{/if}
</label>
