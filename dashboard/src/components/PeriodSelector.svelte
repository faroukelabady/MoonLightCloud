<script lang="ts">
	import type { PeriodParams } from '../lib/api.js';

	let {
		params,
		timezone,
		onchange
	}: {
		params: PeriodParams;
		timezone: string;
		onchange: (p: PeriodParams) => void;
	} = $props();

	let customFrom = $state(params.from_date ?? '');
	let customTo = $state(params.to_date ?? '');

	// L01: keep visible inputs synchronized with authoritative URL/filter
	// state on history navigation. Syncs only when the params themselves
	// change (tracked by key), so typing in one field never clobbers the
	// other and active edits are never overwritten.
	let syncedKey = $state('');
	$effect(() => {
		const key = params.period + '|' + (params.from_date ?? '') + '|' + (params.to_date ?? '');
		if (key !== syncedKey) {
			syncedKey = key;
			customFrom = params.from_date ?? '';
			customTo = params.to_date ?? '';
		}
	});

	const periods = [
		{ v: 'last_10_completed_days', ar: 'آخر 10 أيام', en: 'Last 10 days' },
		{ v: 'yesterday', ar: 'أمس', en: 'Yesterday' },
		{ v: 'today', ar: 'اليوم', en: 'Today' },
		{ v: 'custom', ar: 'نطاق مخصص', en: 'Custom range' }
	];

	function pick(v: string) {
		onchange({ period: v, from_date: customFrom || undefined, to_date: customTo || undefined });
	}

	function applyCustom() {
		onchange({ period: 'custom', from_date: customFrom || undefined, to_date: customTo || undefined });
	}
</script>

<div class="periodbar">
	<div class="modes" role="group" aria-label="الفترة / Period">
		{#each periods as p}
			<button
				type="button"
				class:active={params.period === p.v}
				aria-pressed={params.period === p.v}
				onclick={() => pick(p.v)}
			>
				{p.ar}
			</button>
		{/each}
	</div>
	{#if params.period === 'custom'}
		<div class="custom">
			<label>من <span class="muted">from</span><input type="date" bind:value={customFrom} /></label>
			<label>إلى <span class="muted">to</span><input type="date" bind:value={customTo} /></label>
			<button type="button" class="apply" onclick={applyCustom}>عرض / Show</button>
		</div>
	{/if}
	<div class="tz muted">المنطقة الزمنية للمتجر: {timezone} / Store timezone</div>
</div>

<style>
	.periodbar {
		display: flex;
		flex-wrap: wrap;
		gap: 12px;
		align-items: center;
		margin-bottom: 16px;
	}
	.modes {
		display: inline-flex;
		border: 1px solid var(--border);
		border-radius: 8px;
		overflow: hidden;
		background: var(--surface);
	}
	.modes button {
		border: 0;
		background: transparent;
		padding: 7px 14px;
		color: var(--color-muted);
	}
	.modes button.active {
		background: var(--color-primary);
		color: #fff;
	}
	.custom {
		display: flex;
		gap: 8px;
		align-items: center;
	}
	.custom input {
		padding: 6px 8px;
		border: 1px solid var(--border);
		border-radius: 6px;
	}
	.apply {
		background: var(--color-primary);
		color: #fff;
		border: 0;
		border-radius: 6px;
		padding: 7px 14px;
	}
	.tz {
		margin-inline-start: auto;
	}
</style>
