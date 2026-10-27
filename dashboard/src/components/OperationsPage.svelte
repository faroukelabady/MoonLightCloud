<script lang="ts">
	import { onMount } from 'svelte';
	import { dashboardApi, ApiError } from '../lib/api';
	import type { IncidentRow } from '../lib/api';
	import SingleSelect from './ui/SingleSelect.svelte';

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
			notice = 'Acknowledge failed.';
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
			if (e instanceof ApiError && e.status === 409) notice = 'Condition still active — resolve is blocked until it clears.';
			else notice = 'Resolve failed.';
		} finally {
			acting[id] = false;
		}
	}
</script>

<section aria-label="Operations">
	<h2>Operations</h2>
	<p class="hint">Open and acknowledged incidents need operator attention. Acknowledge suppresses the unacknowledged state only — it never claims recovery.</p>
	<div class="filters">
		<SingleSelect id="filter-state" label="State" testid="filter-state" value={filterState}
			options={[{ value: '', label: 'All' }, { value: 'open', label: 'Open' }, { value: 'acknowledged', label: 'Acknowledged' }, { value: 'resolved', label: 'Resolved' }]}
			onchange={(v) => { filterState = v; applyFilters(); }} />
		<SingleSelect id="filter-severity" label="Severity" testid="filter-severity" value={filterSeverity}
			options={[{ value: '', label: 'All' }, { value: 'warning', label: 'Warning' }, { value: 'urgent', label: 'Urgent' }]}
			onchange={(v) => { filterSeverity = v; applyFilters(); }} />
		<SingleSelect id="filter-rule" label="Rule" testid="filter-rule" value={filterRule}
			options={[
				{ value: '', label: 'All' },
				{ value: 'DEVICE_OFFLINE', label: 'Device offline' },
				{ value: 'DEVICE_SYNC_FAILED', label: 'Sync failed' },
				{ value: 'DEVICE_SYNC_STALE', label: 'Sync stale' },
				{ value: 'BUSINESS_REPORT_BLOCKED', label: 'Report blocked' },
				{ value: 'BUSINESS_REPORT_STALE', label: 'Report stale' },
				{ value: 'NOTIFICATION_BLOCKED', label: 'Notification blocked' },
				{ value: 'NOTIFICATION_AMBIGUOUS', label: 'Notification ambiguous' },
				{ value: 'NOTIFICATION_RETRY_STALE', label: 'Notification retry stale' }
			]}
			onchange={(v) => { filterRule = v; applyFilters(); }} />
	</div>
	{#if loading}<p>Loading…</p>
	{:else if error}<p role="alert">Failed: {error}</p>
	{:else if incidents.length === 0}<p>No incidents match.</p>
	{:else}
		<ul>
			{#each incidents as i (i.id)}
				<li data-testid="incident-row" data-id={i.id}>
					<strong data-testid="incident-rule">{i.rule}</strong>
					<span data-testid="incident-severity">{i.severity}</span>
					<span data-testid="incident-state">{i.state}</span>
					<span title="subject">{i.subject_type}:{i.subject_id.slice(0, 8)}</span>
					<span title="opened">{i.opened_at}</span>
					{#if (i.state !== 'resolved' && !i.open_intent_materialized) || (i.state === 'resolved' && !i.resolved_intent_materialized)}
						<span data-testid="intent-review" title="Alert intent predates atomic writes; reconcile explicitly if review warrants it">needs review</span>
					{/if}
					{#if i.state === 'open'}
						<button class="btn-secondary btn-sm" data-testid="ack" disabled={!!acting[i.id]} on:click={() => ack(i.id)}>Acknowledge</button>
					{/if}
					{#if i.state !== 'resolved'}
						<button class="btn-secondary btn-sm" data-testid="resolve" disabled={!!acting[i.id]} on:click={() => resolve(i.id)}>Resolve</button>
					{/if}
				</li>
			{/each}
		</ul>
		{#if next}
			<button class="btn-secondary btn-sm" data-testid="more" on:click={() => load(true)}>More</button>
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
