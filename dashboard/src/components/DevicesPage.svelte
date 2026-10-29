<script lang="ts">
	import { lang, msg } from '../lib/i18n.svelte.js';
	import { absoluteTime } from '../lib/time.js';
	import { onMount } from 'svelte';
	import { dashboardApi, ApiError } from '../lib/api';
	import type { DeviceRow, DeviceCommandWire, StoreRow } from '../lib/api';

	let devices: DeviceRow[] = [];
	let stores: StoreRow[] | null = null;
	let incidentSummary: Record<string, { open_count: number; max_severity: string }> = {};
	let loading = true;
	let error: string | null = null;
	let requesting: Record<string, boolean> = {};
	let notice: string | null = null;

	function userFacing(status: string): string {
		switch (status) {
			case 'pending':
			case 'leased':
			case 'accepted':
			case 'running':
			case 'completed':
			case 'failed':
				return msg(`devicesPage.command.${status}`);
			default:
				return status;
		}
	}

	function isTerminal(status: string): boolean {
		return status === 'completed' || status === 'failed';
	}

	// Latest terminal outcome from the actual API shape: active_command is
	// never terminal (one-active invariant covers non-terminal only), so
	// history comes from recent_commands, newest first, excluding the row
	// already shown as active.
	function lastTerminal(d: DeviceRow): DeviceCommandWire | null {
		const activeId = d.active_command?.id ?? null;
		for (const c of d.recent_commands ?? []) {
			if (c.id !== activeId && isTerminal(c.status)) return c;
		}
		return null;
	}

	function history(d: DeviceRow): DeviceCommandWire[] {
		const activeId = d.active_command?.id ?? null;
		return (d.recent_commands ?? []).filter((c) => c.id !== activeId).slice(0, 5);
	}

	function newKey(): string {
		if (typeof crypto !== 'undefined' && 'randomUUID' in crypto) return crypto.randomUUID();
		return `key-${Date.now()}-${Math.floor(Math.random() * 1e9)}`;
	}

	export async function refresh(signal?: AbortSignal) {
		loading = true;
		error = null;
		try {
			const res = await dashboardApi.devices(signal);
			devices = res.devices ?? [];
			try {
				const sres = await dashboardApi.stores(signal);
				stores = sres.stores ?? [];
			} catch {
				stores = null;
			}
			try {
				const sum = await dashboardApi.operationsSummary(signal);
				incidentSummary = Object.fromEntries((sum.devices ?? []).map((d) => [d.device_id, d]));
			} catch {
				incidentSummary = {};
			}
		} catch (e) {
			if (e instanceof ApiError) error = e.code;
			else error = 'INTERNAL';
		} finally {
			loading = false;
		}
	}

	onMount(() => {
		void refresh();
	});

	async function syncNow(deviceId: string, hasActive: boolean) {
		if (hasActive || requesting[deviceId]) return;
		requesting[deviceId] = true;
		notice = null;
		try {
			const res = await dashboardApi.syncRequest(deviceId, newKey());
			notice = res.created ? msg('devicesPage.syncQueued') : msg('devicesPage.syncAlreadyActive');
			await refresh();
		} catch (e) {
			if (e instanceof ApiError) notice = e.code;
			else notice = 'INTERNAL';
		} finally {
			requesting[deviceId] = false;
		}
	}
</script>

<section class="card devices" aria-label={msg('devicesPage.devices')}>
	<h2>{msg('devicesPage.devices')}</h2>
	<p class="hint">{msg('devicesPage.onlineMeansCloudHeardFromThe')}</p>
	{#if loading}<p>{msg('devicesPage.loading')}</p>
	{:else if error}<p role="alert">{msg('devicesPage.failed', { code: error })}</p>
	{:else if devices.length === 0}<p>{msg('devicesPage.noDevices')}</p>
	{:else}
		<div class="table-wrap">
			<table class="grid-table" data-testid="devices-table">
				<thead>
					<tr>
						<th scope="col">{msg('devicesPage.device')}</th>
						<th scope="col">{msg('devicesPage.store')}</th>
						<th scope="col">{msg('devicesPage.connection')}</th>
						<th scope="col">{msg('devicesPage.lastSeen')}</th>
						<th scope="col">{msg('devicesPage.lastData')}</th>
						<th scope="col">{msg('devicesPage.incidents')}</th>
						<th scope="col">{msg('devicesPage.sync')}</th>
					</tr>
				</thead>
				<tbody>
					{#each devices as d (d.device_id)}
						{@const active = d.active_command ?? null}
						{@const terminal = lastTerminal(d)}
						{@const past = history(d)}
						<tr data-testid="device-row" data-device={d.device_id}>
							<th scope="row">
								<div class="name">{d.name || d.device_id}</div>
								{#if d.name}<div class="mono sub" dir="ltr">{d.device_id}</div>{/if}
							</th>
							<td>
								{#if d.store_id}
									<span data-testid="device-store" title={d.store_id}>{d.store_name || d.store_id}</span>
								{:else}
									<span class="muted" data-testid="device-store-unbound" title={msg('devicesPage.unboundTitle')}>{msg('devicesPage.unbound')}</span>
								{/if}
							</td>
							<td>
								<span class="pill" class:ok={d.connectivity === 'online'} data-testid="connectivity">{d.connectivity === 'online' || d.connectivity === 'offline' ? msg(`devicesPage.connectivity.${d.connectivity}`) : d.connectivity}</span>
								{#if d.connectivity === 'online' && d.control && d.control !== 'polling'}
									<div class="warn" data-testid="control-indicator" title={d.last_poll_at ? msg('devicesPage.lastPoll', { at: d.last_poll_at }) : msg('devicesPage.neverPolled')}>
										{d.control === 'never_polled'
											? msg('devicesPage.syncingNotPolledYetSyncNow')
											: msg('devicesPage.syncingControlPollStaleSyncNow')}
									</div>
								{/if}
							</td>
							<td>{#if d.last_seen_at}<time datetime={d.last_seen_at} title={d.last_seen_at}>{absoluteTime(d.last_seen_at, lang())}</time>{:else}<span class="muted">{msg('devicesPage.never')}</span>{/if}</td>
							<td data-testid="last-data">{#if d.last_data_at}<time datetime={d.last_data_at} title={d.last_data_at}>{absoluteTime(d.last_data_at, lang())}</time>{:else}<span class="muted">{msg('devicesPage.noneYet')}</span>{/if}</td>
							<td>
								{#if incidentSummary[d.device_id]}
									<span class="pill warn-pill" data-testid="incident-badge" title={msg('devicesPage.openIncidents')}>{msg('devicesPage.incidentCount', { count: incidentSummary[d.device_id].open_count, severity: incidentSummary[d.device_id].max_severity })}</span>
								{:else}<span class="muted">—</span>{/if}
							</td>
							<td><div class="sync">
								{#if active}
									<span data-testid="command-status">{userFacing(active.status)}</span>
									<button class="btn-secondary btn-sm" disabled title={msg('devicesPage.syncAlreadyActive')}>{msg('devicesPage.syncNow')}</button>
								{:else}
									{#if terminal}
										<span data-testid="last-sync">{msg('devicesPage.lastSync', { status: userFacing(terminal.status) })}</span>
									{:else}
										<span class="muted" data-testid="command-status">{msg('devicesPage.idle')}</span>
									{/if}
									<button class="btn-primary btn-sm" data-testid="sync-now" disabled={!!requesting[d.device_id]} on:click={() => syncNow(d.device_id, false)}>{msg('devicesPage.syncNow')}</button>
									{#if past.length > 0}
										<details>
											<summary>{msg('devicesPage.history')} ({past.length})</summary>
											<ul data-testid="sync-history">
												{#each past as c (c.id)}
													<li>{userFacing(c.status)} — <time datetime={c.requested_at}>{absoluteTime(c.requested_at, lang())}</time></li>
												{/each}
											</ul>
										</details>
									{/if}
								{/if}
							</div></td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	{/if}
	{#if notice}<p role="status">{notice}</p>{/if}
	{#if stores !== null}
		<h3>{msg('devicesPage.stores')}</h3>
		{#if stores.length === 0}<p>{msg('devicesPage.noStores')}</p>
		{:else}
			<div class="table-wrap">
				<table class="grid-table" data-testid="stores-table">
					<thead>
						<tr>
							<th scope="col">{msg('devicesPage.store')}</th>
							<th scope="col">{msg('devicesPage.storeId')}</th>
							<th scope="col">{msg('devicesPage.devices')}</th>
							<th scope="col">{msg('devicesPage.timeZone')}</th>
							<th scope="col">{msg('devicesPage.status')}</th>
						</tr>
					</thead>
					<tbody>
						{#each stores as st (st.store_id)}
							<tr data-testid="store-row" data-store={st.store_id}>
								<th scope="row">{st.display_name}</th>
								<td class="mono" dir="ltr">{st.store_id}</td>
								<td>{msg('devicesPage.deviceCount', { count: st.device_count })}</td>
								<td dir="ltr">{st.timezone}</td>
								<td>{st.status === 'active' ? msg('devicesPage.storeActive') : st.status}</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		{/if}
	{/if}
</section>

<style>
	.hint {
		color: var(--muted, #666);
		font-size: 0.82rem;
	}
	.table-wrap {
		overflow-x: auto;
		border: 1px solid var(--border);
		border-radius: var(--radius-control);
		margin-block: 8px 16px;
	}
	.grid-table {
		width: 100%;
		border-collapse: collapse;
		font-size: 0.82rem;
	}
	.grid-table th,
	.grid-table td {
		padding: 8px 10px;
		text-align: start;
		vertical-align: top;
		border-bottom: 1px solid var(--border);
	}
	.grid-table tbody tr:last-child > * {
		border-bottom: 0;
	}
	.grid-table thead th {
		background: var(--surface-muted);
		font-size: 0.76rem;
		font-weight: 700;
		white-space: nowrap;
	}
	.grid-table tbody th {
		font-weight: 600;
	}
	.grid-table tbody tr:hover {
		background: #f8fafd;
	}
	.name {
		font-weight: 700;
	}
	.sub {
		font-size: 0.7rem;
		color: var(--text-muted);
		font-weight: 400;
	}
	.mono {
		font-family: ui-monospace, monospace;
	}
	.pill {
		display: inline-block;
		padding: 2px 8px;
		border-radius: 999px;
		background: var(--surface-muted);
		font-size: 0.76rem;
		font-weight: 600;
	}
	.pill.ok {
		background: #e8f6ee;
		color: #15803d;
	}
	.warn-pill {
		background: #fdf4e7;
		color: #92400e;
	}
	.warn {
		color: var(--warning, #a15c00);
		font-size: 0.74rem;
		margin-top: 4px;
	}
	.sync {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: 4px;
		min-width: 140px;
	}
	.sync ul {
		margin: 4px 0 0;
		padding-inline-start: 16px;
		font-size: 0.74rem;
	}
</style>
