<script lang="ts">
	import { t, tb } from '../lib/i18n.svelte.js';
	import type { PeriodParams } from '../lib/api.js';

	let {
		params,
		timezone,
		onchange,
		showTimezone = true,
		range = null
	}: {
		params: PeriodParams;
		timezone: string;
		onchange: (p: PeriodParams) => void;
		showTimezone?: boolean;
		// The applied period's first and last local dates, shown in the
		// (disabled) date fields while a preset period is selected.
		range?: { from: string; to: string } | null;
	} = $props();

	// Neutral initial state: the L01 effect below is the single source that
	// copies the authoritative params into these editable fields, so the
	// component never captures a stale initial prop reference (Svelte
	// state_referenced_locally warning) while behavior stays identical.
	let customFrom = $state('');
	let customTo = $state('');
	// Draft period: clicking Custom reveals inputs without issuing any
	// request. Only Apply commits a custom range (both dates required),
	// so selecting Custom never fires transient 400s.
	let draftPeriod = $state('last_10_completed_days');
	let customError: string | null = $state(null);
	let custom = $derived(draftPeriod === 'custom');

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
		// Custom starts from the dates on screen, ready to adjust.
		if (v === 'custom' && range) {
			if (!customFrom) customFrom = range.from;
			if (!customTo) customTo = range.to;
		}
		if (v !== 'custom') {
			onchange({ period: v });
		}
		// Custom only reveals the date inputs; Apply commits.
	}

	function applyCustom() {
		if (!/^\d{4}-\d{2}-\d{2}$/.test(customFrom) || !/^\d{4}-\d{2}-\d{2}$/.test(customTo)) {
			customError = tb('أدخل تاريخين صحيحين بصيغة YYYY-MM-DD', 'Enter two valid YYYY-MM-DD dates');
			return;
		}
		if (customFrom > customTo) {
			customError = tb('تاريخ البداية يجب أن يسبق تاريخ النهاية', 'Start date must not be after end date');
			return;
		}
		customError = null;
		onchange({ period: 'custom', from_date: customFrom, to_date: customTo });
	}
</script>

<div class="periodbar">
	<div class="modes" role="group" aria-label={tb('الفترة', 'Period')}>
		{#each periods as p}
			<button
				type="button"
				class:active={draftPeriod === p.v}
				aria-pressed={draftPeriod === p.v}
				onclick={() => pick(p.v)}
			>
				{t(p.ar, p.en)}
			</button>
		{/each}
	</div>
	<!-- Always visible: a preset period shows its dates here, disabled;
	     Custom range makes them editable. -->
	<div class="custom" class:locked={!custom} title={timezone}>
		{#if custom}
			<label>{t('من', 'From')}<input type="date" bind:value={customFrom} /></label>
			<label>{t('إلى', 'To')}<input type="date" bind:value={customTo} /></label>
		{:else}
			<label>{t('من', 'From')}<input type="date" value={range?.from ?? ''} disabled /></label>
			<label>{t('إلى', 'To')}<input type="date" value={range?.to ?? ''} disabled /></label>
		{/if}
		<button type="button" class="apply" disabled={!custom} onclick={applyCustom}>{t('عرض', 'Show')}</button>
	</div>
	{#if customError}<div class="err" role="alert">{customError}</div>{/if}
	{#if showTimezone}<div class="tz muted" dir="ltr">{timezone}</div>{/if}
</div>

<style>
	/* The period buttons with their date fields underneath. */
	.periodbar {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: 4px;
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
	.custom input:disabled,
	.apply:disabled {
		opacity: 0.55;
		cursor: not-allowed;
	}
	.custom input {
		width: 8rem;
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
		font-size: 0.8rem;
		font-weight: 600;
	}
	.tz {
		margin-inline-start: auto;
		font-size: 0.75rem;
	}
</style>
