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
	// Draft period: clicking Custom reveals inputs without issuing any
	// request. Only Apply commits a custom range (both dates required),
	// so selecting Custom never fires transient 400s.
	let draftPeriod = $state(params.period);
	let customError: string | null = $state(null);

	// L01: keep visible inputs synchronized with authoritative URL/filter
	// state on history navigation. Syncs only when the params themselves
	// change (tracked by key), so typing in one field never clobbers the
	// other and active edits are never overwritten.
	let syncedKey = $state('');
	$effect(() => {
		const key = params.period + '|' + (params.from_date ?? '') + '|' + (params.to_date ?? '');
		if (key !== syncedKey) {
			syncedKey = key;
			draftPeriod = params.period;
			customFrom = params.from_date ?? '';
			customTo = params.to_date ?? '';
			customError = null;
		}
	});

	const periods = [
		{ v: 'last_10_completed_days', ar: 'آخر 10 أيام', en: 'Last 10 days' },
		{ v: 'yesterday', ar: 'أمس', en: 'Yesterday' },
		{ v: 'today', ar: 'اليوم', en: 'Today' },
		{ v: 'custom', ar: 'نطاق مخصص', en: 'Custom range' }
	];

	function pick(v: string) {
		draftPeriod = v;
		if (v !== 'custom') {
			onchange({ period: v });
		}
		// Custom only reveals the date inputs; Apply commits.
	}

	function applyCustom() {
		if (!/^\d{4}-\d{2}-\d{2}$/.test(customFrom) || !/^\d{4}-\d{2}-\d{2}$/.test(customTo)) {
			customError = 'أدخل تاريخين صحيحين بصيغة YYYY-MM-DD / Enter two valid YYYY-MM-DD dates';
			return;
		}
		if (customFrom > customTo) {
			customError = 'تاريخ البداية يجب أن يسبق تاريخ النهاية / Start date must not be after end date';
			return;
		}
		customError = null;
		onchange({ period: 'custom', from_date: customFrom, to_date: customTo });
	}
</script>

<div class="periodbar">
	<div class="modes" role="group" aria-label="الفترة / Period">
		{#each periods as p}
			<button
				type="button"
				class:active={draftPeriod === p.v}
				aria-pressed={draftPeriod === p.v}
				onclick={() => pick(p.v)}
			>
				{p.ar}
			</button>
		{/each}
	</div>
	{#if draftPeriod === 'custom'}
		<div class="custom">
			<label>من <span class="muted">from</span><input type="date" bind:value={customFrom} /></label>
			<label>إلى <span class="muted">to</span><input type="date" bind:value={customTo} /></label>
			<button type="button" class="apply" onclick={applyCustom}>عرض / Show</button>
		</div>
		{#if customError}<div class="err" role="alert">{customError}</div>{/if}
	{/if}
	<div class="tz muted" dir="ltr">{timezone}</div>
</div>

<style>
	.periodbar {
		display: flex;
		flex-wrap: wrap;
		gap: 8px 12px;
		align-items: center;
	}
	.modes {
		display: inline-flex;
		border: 1px solid var(--border);
		border-radius: var(--radius-control);
		overflow: hidden;
		background: var(--surface);
		padding: 2px;
		gap: 2px;
	}
	.modes button {
		border: 0;
		background: transparent;
		border-radius: 4px;
		padding: 5px 12px;
		font-size: 0.82rem;
		color: var(--text-muted);
	}
	.modes button.active {
		background: var(--primary);
		color: #fff;
		font-weight: 600;
	}
	.custom {
		display: flex;
		gap: 8px;
		align-items: center;
		flex-wrap: wrap;
	}
	.custom label {
		display: inline-flex;
		gap: 6px;
		align-items: center;
		font-size: 0.82rem;
	}
	.custom input {
		padding: 5px 8px;
		border: 1px solid var(--border);
		border-radius: var(--radius-control);
		font: inherit;
	}
	.err {
		color: var(--danger);
		font-size: 0.82rem;
	}
	.apply {
		background: var(--primary);
		color: #fff;
		border: 0;
		border-radius: var(--radius-control);
		padding: 6px 14px;
	}
	.tz {
		margin-inline-start: auto;
		font-size: 0.75rem;
	}
</style>
