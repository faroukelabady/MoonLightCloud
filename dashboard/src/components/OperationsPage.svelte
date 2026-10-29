<script lang="ts">
	import { onMount } from 'svelte';
	import { dashboardApi, ApiError } from '../lib/api';
	import type { IncidentRow } from '../lib/api';
	import SingleSelect from './ui/SingleSelect.svelte';
	import { msg } from '../lib/i18n.svelte.js';
	const k = (key: string, params?: Record<string, string | number>) => msg(`operationsPage.${key}`, params);
	const rules = ['DEVICE_OFFLINE', 'DEVICE_SYNC_FAILED', 'DEVICE_SYNC_STALE', 'BUSINESS_REPORT_BLOCKED', 'BUSINESS_REPORT_STALE', 'NOTIFICATION_BLOCKED', 'NOTIFICATION_AMBIGUOUS', 'NOTIFICATION_RETRY_STALE'];
	const states = ['open', 'acknowledged', 'resolved'];
	const severities = ['warning', 'urgent'];
	// Known codes read in the chosen language; anything new shows as sent.
	const label = (group: string, list: string[], value: string) => (list.includes(value) ? k(`${group}.${value}`) : value);

	let incidents: IncidentRow[] = [];
	let loading = true;
	let error: string | null = null;
	let notice: string | null = null;
	let next: string | null = null;
	let filterState = '';
	let filterSeverity = '';
	let filterRule = '';
	let acting: Record<string, boolean> = {};

	async function load(more = false, signal?: AbortSignal) {
		if (!more) {
			loading = true;
			incidents = [];
			next = null;
		}
		error = null;
		try {
			const res = await dashboardApi.incidents(
				{ state: filterState || undefined, severity: filterSeverity || undefined, rule: filterRule || undefined, limit: 20, cursor: more ? next : undefined },
				signal
			);
			if (more) incidents = [...incidents, ...(res.incidents ?? [])];
			else incidents = res.incidents ?? [];
			next = res.next_cursor || null;
		} catch (e) {
			if (e instanceof ApiError) error = e.code;
			else error = 'INTERNAL';
		} finally {
			loading = false;
		}
	}

	onMount(() => {
		void load();
	});

	function applyFilters() {
		void load();
	}

	async function ack(id: string) {
		if (acting[id]) return;
		acting[id] = true;
		notice = null;
		try {
			const updated = await dashboardApi.incidentAck(id);
			incidents = incidents.map((i) => (i.id === id ? updated : i));
		} catch {
			notice = k('ackFailed');
		} finally {
			acting[id] = false;
		}
	}

	async function resolve(id: string) {
		if (acting[id]) return;
		acting[id] = true;
		notice = null;
		try {
			const updated = await dashboardApi.incidentResolve(id);
			incidents = incidents.map((i) => (i.id === id ? updated : i));
		} catch (e) {
			if (e instanceof ApiError && e.status === 409) notice = k('stillActive');
			else notice = k('resolveFailed');
		} finally {
			acting[id] = false;
		}
	}
</script>

<section class="card" aria-label={k('title')}>
	<h2>{k('title')}</h2>
	<p class="hint">{k('hint')}</p>
	<div class="filters">
		<SingleSelect id="filter-state" label={k('state')} testid="filter-state" value={filterState}
			options={[{ value: '', label: k('all') }, ...states.map((value) => ({ value, label: k(`states.${value}`) }))]}
			onchange={(v) => { filterState = v; applyFilters(); }} />
		<SingleSelect id="filter-severity" label={k('severity')} testid="filter-severity" value={filterSeverity}
			options={[{ value: '', label: k('all') }, ...severities.map((value) => ({ value, label: k(`severities.${value}`) }))]}
			onchange={(v) => { filterSeverity = v; applyFilters(); }} />
		<SingleSelect id="filter-rule" label={k('rule')} testid="filter-rule" value={filterRule}
			options={[{ value: '', label: k('all') }, ...rules.map((value) => ({ value, label: k(`rules.${value}`) }))]}
			onchange={(v) => { filterRule = v; applyFilters(); }} />
	</div>
	{#if loading}<p>{k('loading')}</p>
	{:else if error}<p role="alert">{k('failed', { code: error })}</p>
	{:else if incidents.length === 0}<p>{k('none')}</p>
	{:else}
		<ul>
			{#each incidents as i (i.id)}
				<li data-testid="incident-row" data-id={i.id}>
					<strong data-testid="incident-rule" title={i.rule}>{label('rules', rules, i.rule)}</strong>
					<span data-testid="incident-severity">{label('severities', severities, i.severity)}</span>
					<span data-testid="incident-state">{label('states', states, i.state)}</span>
					<span title={k('subject')} dir="ltr">{i.subject_type}:{i.subject_id.slice(0, 8)}</span>
					<span title={k('opened')} dir="ltr">{i.opened_at}</span>
					{#if (i.state !== 'resolved' && !i.open_intent_materialized) || (i.state === 'resolved' && !i.resolved_intent_materialized)}
						<span data-testid="intent-review" title={k('needsReviewHint')}>{k('needsReview')}</span>
					{/if}
					{#if i.state === 'open'}
						<button class="btn-secondary btn-sm" data-testid="ack" disabled={!!acting[i.id]} on:click={() => ack(i.id)}>{k('acknowledge')}</button>
					{/if}
					{#if i.state !== 'resolved'}
						<button class="btn-secondary btn-sm" data-testid="resolve" disabled={!!acting[i.id]} on:click={() => resolve(i.id)}>{k('resolve')}</button>
					{/if}
				</li>
			{/each}
		</ul>
		{#if next}
			<button class="btn-secondary btn-sm" data-testid="more" on:click={() => load(true)}>{k('more')}</button>
		{/if}
	{/if}
	{#if notice}<p role="status">{notice}</p>{/if}
</section>

<style>
	.hint {
		color: var(--muted, #666);
	}
	.filters {
		display: grid;
		grid-template-columns: repeat(3, minmax(0, 220px));
		gap: 12px;
		margin-bottom: 1rem;
	}
</style>
