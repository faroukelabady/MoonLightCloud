<script lang="ts">
	import { onMount } from 'svelte';
	import { dashboardApi, ApiError } from '../lib/api';
	import type { IncidentRow } from '../lib/api';

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
		<label>State
			<select data-testid="filter-state" bind:value={filterState} on:change={applyFilters}>
				<option value="">All</option>
				<option value="open">Open</option>
				<option value="acknowledged">Acknowledged</option>
				<option value="resolved">Resolved</option>
			</select>
		</label>
		<label>Severity
			<select data-testid="filter-severity" bind:value={filterSeverity} on:change={applyFilters}>
				<option value="">All</option>
				<option value="warning">Warning</option>
				<option value="urgent">Urgent</option>
			</select>
		</label>
		<label>Rule
			<select data-testid="filter-rule" bind:value={filterRule} on:change={applyFilters}>
				<option value="">All</option>
				<option value="DEVICE_OFFLINE">Device offline</option>
				<option value="DEVICE_SYNC_FAILED">Sync failed</option>
				<option value="DEVICE_SYNC_STALE">Sync stale</option>
				<option value="BUSINESS_REPORT_BLOCKED">Report blocked</option>
				<option value="BUSINESS_REPORT_STALE">Report stale</option>
				<option value="NOTIFICATION_BLOCKED">Notification blocked</option>
				<option value="NOTIFICATION_AMBIGUOUS">Notification ambiguous</option>
				<option value="NOTIFICATION_RETRY_STALE">Notification retry stale</option>
			</select>
		</label>
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
					{#if i.state === 'open'}
						<button data-testid="ack" disabled={!!acting[i.id]} on:click={() => ack(i.id)}>Acknowledge</button>
					{/if}
					{#if i.state !== 'resolved'}
						<button data-testid="resolve" disabled={!!acting[i.id]} on:click={() => resolve(i.id)}>Resolve</button>
					{/if}
				</li>
			{/each}
		</ul>
		{#if next}
			<button data-testid="more" on:click={() => load(true)}>More</button>
		{/if}
	{/if}
	{#if notice}<p role="status">{notice}</p>{/if}
</section>

<style>
	.hint {
		color: var(--muted, #666);
	}
	.filters {
		display: flex;
		gap: 1rem;
		margin-bottom: 1rem;
	}
</style>
