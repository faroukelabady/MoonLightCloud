<script lang="ts">
	import Skeleton from './Skeleton.svelte';

	let {
		ar,
		en,
		value,
		context,
		status,
		icon
	}: {
		ar: string;
		en: string;
		value: string;
		context?: string;
		status: 'loading' | 'loaded' | 'empty' | 'error';
		icon: string;
	} = $props();
</script>

<div class="card metric">
	<div class="head">
		<span class="pic" aria-hidden="true">
			<svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d={icon} /></svg>
		</span>
		<span class="titles">
			<span class="ar">{ar}</span>
			<span class="en">{en}</span>
		</span>
	</div>
	{#if status === 'loading'}
		<Skeleton lines={1} />
	{:else}
		<div class="val num">{value}</div>
		{#if context}<div class="muted ctx">{context}</div>{/if}
	{/if}
</div>

<style>
	.metric {
		padding: 12px 14px;
	}
	.head {
		display: flex;
		align-items: center;
		gap: 8px;
	}
	.pic {
		width: 30px;
		height: 30px;
		flex-shrink: 0;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		border-radius: 8px;
		background: var(--primary-soft);
		color: var(--primary);
	}
	.titles {
		display: flex;
		flex-direction: column;
		line-height: 1.25;
	}
	.ar {
		font-weight: 600;
		font-size: 0.88rem;
	}
	.en {
		font-size: 0.72rem;
		color: var(--text-muted);
	}
	.val {
		font-size: 1.65rem;
		font-weight: 700;
		margin-top: 6px;
	}
	.ctx {
		font-size: 0.76rem;
		margin-top: 2px;
	}
</style>
