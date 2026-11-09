<script lang="ts">
	import { onMount } from 'svelte';
	import DeviceManagementDialog from './DeviceManagementDialog.svelte';
	import { lang, msg } from '../lib/i18n.svelte.js';
	import { absoluteTime } from '../lib/time.js';
	import { dashboardApi, ApiError } from '../lib/api';
	import type { DeviceRow, DeviceCommandWire, StoreRow } from '../lib/api';

	let { store = '', canManage = true, allStores = false, onstoreschange = (_stores:StoreRow[])=>{} }: { store?: string; canManage?: boolean; allStores?:boolean; onstoreschange?:(stores:StoreRow[])=>void } = $props();
 let management = $state<{kind:'store'|'device'|'rotate'|'revoke';storeID?:string;storeName?:string;deviceID?:string;deviceName?:string}|null>(null);
	let devices = $state<DeviceRow[]>([]);
	let stores = $state<StoreRow[] | null>(null);
	let incidentSummary = $state<Record<string, { open_count: number; max_severity: string }>>({});
	let incidentsAvailable = $state(false);
	let controlEnabled = $state(true);
	let loaded = $state(false);
	let loading = $state(true);
	let error = $state<string | null>(null);
	let requesting = $state<Record<string, boolean>>({});
	let notice = $state<string | null>(null);
	let opened = $state<Record<string, boolean>>({});
	let request: AbortController | undefined;
	let epoch = 0;
	let disposed = false;
	type Group = { key: string; id: string; name: string; timezone?: string; status?: string; devices: DeviceRow[] };
	let groups = $derived.by(() => {
		const map = new Map<string, Group>();
		for (const st of stores ?? []) map.set(st.store_id, { key: st.store_id, id: st.store_id, name: st.display_name, timezone: st.timezone, status: st.status, devices: [] });
		for (const d of devices) {
			const key = d.store_id || 'unassigned';
			if (!map.has(key)) map.set(key, { key, id: d.store_id ?? '', name: d.store_name || d.store_id || '', devices: [] });
			map.get(key)!.devices.push(d);
		}
		return [...map.values()].filter((group) => !store || group.id === store);
	});
	function userFacing(status: string): string {
		return ['pending', 'leased', 'accepted', 'running', 'completed', 'failed'].includes(status) ? msg(`devicesPage.command.${status}`) : msg('devicesPage.unknown');
	}
	function lastTerminal(d: DeviceRow): DeviceCommandWire | null {
		return (d.recent_commands ?? []).find((c) => c.id !== d.active_command?.id && ['completed', 'failed'].includes(c.status)) ?? null;
	}
	function history(d: DeviceRow) { return (d.recent_commands ?? []).filter((c) => c.id !== d.active_command?.id).slice(0, 5); }
	function errorText(e: unknown) {
		if (e instanceof ApiError && e.detail === 'DEVICE_CONTROL_DISABLED') return msg('devicesPage.controlDisabled');
		return msg('devicesPage.failed', { code: e instanceof ApiError ? e.code : 'INTERNAL' });
	}

	export async function refresh(signal?: AbortSignal) {
		request?.abort();
		const own = new AbortController(); request = own;
		const abort = () => own.abort();
		if (signal?.aborted) own.abort(); else signal?.addEventListener('abort', abort, { once: true });
		const current = ++epoch;
		loading = true;
		try {
			const [dev, directory, incidents] = await Promise.allSettled([
				dashboardApi.devices(own.signal), dashboardApi.stores(own.signal), dashboardApi.operationsSummary(own.signal)
			]);
			if (disposed || current !== epoch || own.signal.aborted) return;
			if (dev.status === 'rejected') { error = errorText(dev.reason); return; }
			devices = dev.value.devices ?? [];
			controlEnabled = dev.value.control_enabled ?? true;
			stores = directory.status === 'fulfilled' && directory.value ? directory.value.stores ?? [] : null;
			if(stores)onstoreschange(stores);
			incidentsAvailable = incidents.status === 'fulfilled' && !!incidents.value;
			incidentSummary = incidents.status === 'fulfilled' && incidents.value ? Object.fromEntries((incidents.value.devices ?? []).map((d) => [d.device_id, d])) : {};
			error = null; loaded = true;
		} catch (e) { if (!disposed && current === epoch && !own.signal.aborted) error = errorText(e); }
		finally { signal?.removeEventListener('abort', abort); if (!disposed && current === epoch) loading = false; }
	}
	onMount(() => {
		void refresh();
		const timer = window.setInterval(() => {
			if (!loading && !Object.values(requesting).some(Boolean) && document.visibilityState === 'visible') void refresh();
		}, 5000);
		return () => { disposed = true; epoch++; request?.abort(); window.clearInterval(timer); };
	});
	async function syncNow(d: DeviceRow) {
		if (!canManage || !controlEnabled || d.lifecycle !== 'active' || d.active_command || requesting[d.device_id]) return;
		requesting[d.device_id] = true; notice = null;
		try {
			const key = crypto.randomUUID();
			const res = await dashboardApi.syncRequest(d.device_id, key);
			if (disposed) return;
			notice = msg(res.created ? 'devicesPage.syncQueued' : 'devicesPage.syncAlreadyActive');
			await refresh();
		} catch (e) { if (!disposed) notice = errorText(e); }
		finally { if (!disposed) requesting[d.device_id] = false; }
	}
</script>

<section class="stores-page" aria-labelledby="stores-title">
	<header class="card page-head">
		<div><h2 id="stores-title">{msg('devicesPage.stores')}</h2><p class="hint">{msg('devicesPage.storePurpose')}</p></div>
		<div class="page-actions">{#if canManage && allStores}<button type="button" class="btn-primary" onclick={() => management={kind:'store'}}>{msg('deviceManagement.store')}</button>{/if}<button type="button" class="btn-secondary" disabled={loading} onclick={() => refresh()}>{msg('devicesPage.refresh')}</button></div>
	</header>
	<div class="card guide">
		<div><h3>{msg('devicesPage.devicePurposeTitle')}</h3><p>{msg('devicesPage.devicePurpose')}</p></div>
		<div><h3>{msg('devicesPage.syncPurposeTitle')}</h3><p>{msg('devicesPage.syncPurpose')}</p></div>
	</div>
	{#if !controlEnabled}<p class="warning" role="status" data-testid="control-disabled">{msg('devicesPage.controlDisabled')}</p>{/if}
	{#if loaded && stores === null}<p class="warning" role="status">{msg('devicesPage.directoryUnavailable')}</p>{/if}
	{#if error}<p class="warning" role="alert">{error}{#if loaded} {msg('devicesPage.lastKnown')}{/if}</p>{/if}
	{#if notice}<p class="notice" role="status">{notice}</p>{/if}
	{#if loading && !loaded}<p role="status">{msg('devicesPage.loading')}</p>{:else if loaded && groups.length === 0}<div class="card empty">{msg('devicesPage.noStores')}</div>{/if}
	{#each groups as group (group.key)}
		<details class="store-group" open={opened[group.key] ?? groups.length === 1} ontoggle={(event) => opened[group.key] = event.currentTarget.open} data-testid="store-group" data-store={group.id}>
			<summary>
				<span class="store-icon" aria-hidden="true"><svg viewBox="0 0 24 24" width="24" height="24" fill="none" stroke="currentColor" stroke-width="1.8"><path d="M3 10l2-6h14l2 6M4 10v10h16V10M3 10h18M9 20v-6h6v6" /></svg></span>
				<span class="store-heading"><strong>{group.id ? group.name : msg('devicesPage.unassignedDevices')}</strong><span class="hint">{group.id ? msg('devicesPage.registeredStore') : msg('devicesPage.unboundTitle')}{#if group.timezone} · <bdi>{group.timezone}</bdi>{/if}</span></span>
				{#if group.status}<span class="pill" class:ok={group.status === 'active'}>{msg(group.status === 'active' ? 'devicesPage.storeActive' : 'devicesPage.storeInactive')}</span>{/if}
				<span class="device-count">{msg('devicesPage.deviceCount', { count: group.devices.length })}</span>
				<svg class="chevron" viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="m6 9 6 6 6-6" /></svg>
			</summary>
			<div class="store-body">
				{#if group.id}<p class="store-id">{msg('devicesPage.storeId')}: <bdi class="mono">{group.id}</bdi></p>{/if}
                {#if canManage && group.id && group.status === 'active'}<button type="button" class="btn-primary btn-sm" onclick={() => management={kind:'device',storeID:group.id,storeName:group.name}}>{msg('deviceManagement.device')}</button>{/if}
				<p class="hint">{msg('devicesPage.onlineMeansCloudHeardFromThe')}</p>
				{#if !group.devices.length}<p class="empty">{msg('devicesPage.noDevicesForStore')}</p>{:else}
					<div class="table-wrap"><table class="grid-table" data-testid="devices-table">
						<caption class="sr-only">{msg('devicesPage.devicesFor', { name: group.name || msg('devicesPage.unassignedDevices') })}</caption>
						<thead><tr><th scope="col">{msg('devicesPage.device')}</th><th scope="col">{msg('devicesPage.connection')}</th><th scope="col">{msg('devicesPage.lastSeen')}</th><th scope="col">{msg('devicesPage.lastData')}</th><th scope="col">{msg('devicesPage.controlChannel')}</th><th scope="col">{msg('devicesPage.incidents')}</th><th scope="col">{msg('devicesPage.sync')}</th>{#if canManage}<th scope="col">{msg('deviceManagement.actions')}</th>{/if}</tr></thead>
						<tbody>{#each group.devices as d (d.device_id)}
							{@const active = d.active_command ?? null}{@const terminal = lastTerminal(d)}{@const past = history(d)}
							<tr data-testid="device-row" data-device={d.device_id}>
								<th scope="row"><div class="name">{d.name || msg('devicesPage.unnamedDevice')}</div><bdi class="mono sub">{d.device_id}</bdi>{#if d.lifecycle !== 'active'}<div class="pill">{msg('devicesPage.revoked')}</div>{/if}</th>
								<td><span class="pill" class:ok={d.connectivity === 'online'} data-testid="connectivity">{msg(`devicesPage.connectivity.${['online','offline','never_seen'].includes(d.connectivity) ? d.connectivity : 'unknown'}`)}</span></td>
								<td>{#if d.last_seen_at}<time datetime={d.last_seen_at}>{absoluteTime(d.last_seen_at, lang())}</time>{:else}<span class="muted">{msg('devicesPage.never')}</span>{/if}</td>
								<td data-testid="last-data">{#if d.last_data_at}<time datetime={d.last_data_at}>{absoluteTime(d.last_data_at, lang())}</time>{:else}<span class="muted">{msg('devicesPage.noneYet')}</span>{/if}</td>
								<td><span class="pill" class:ok={d.control === 'polling' && controlEnabled}>{msg(!controlEnabled ? 'devicesPage.control.off' : `devicesPage.control.${d.control ?? 'never_polled'}`)}</span>{#if controlEnabled && d.control !== 'polling'}<p class="warn" data-testid="control-indicator">{msg(d.control === 'poll_stale' ? 'devicesPage.syncingControlPollStaleSyncNow' : 'devicesPage.syncingNotPolledYetSyncNow')}</p>{/if}</td>
								<td>{#if incidentSummary[d.device_id]?.open_count}<span class="pill warn-pill" data-testid="incident-badge">{msg('devicesPage.incidentCount', { count: incidentSummary[d.device_id].open_count, severity: incidentSummary[d.device_id].max_severity })}</span>{:else}<span class="muted">{incidentsAvailable ? '—' : msg('devicesPage.unknown')}</span>{/if}</td>
								<td><div class="sync">
									{#if active}<span data-testid="command-status">{userFacing(active.status)}</span>{:else if terminal}<span data-testid="last-sync">{msg('devicesPage.lastSync', { status: userFacing(terminal.status) })}</span>{:else}<span class="muted" data-testid="command-status">{msg('devicesPage.idle')}</span>{/if}
									{#if canManage}<button class="btn-primary btn-sm" data-testid="sync-now" disabled={!controlEnabled || !!active || !!requesting[d.device_id] || d.lifecycle !== 'active'} onclick={() => syncNow(d)}>{msg('devicesPage.syncNow')}</button>{/if}
									{#if !active && past.length}<details class="history"><summary>{msg('devicesPage.history')} ({past.length})</summary><ul data-testid="sync-history">{#each past as c (c.id)}<li>{userFacing(c.status)} — <time datetime={c.requested_at}>{absoluteTime(c.requested_at, lang())}</time>{#if c.result_code}<bdi class="mono"> {c.result_code}</bdi>{/if}</li>{/each}</ul></details>{/if}
								</div></td>
                            {#if canManage}<td>{#if d.lifecycle === 'active' && (d.store_id || allStores)}<div class="device-actions">{#if d.store_id}<button type="button" class="btn-secondary btn-sm" onclick={() => management={kind:'rotate',deviceID:d.device_id,deviceName:d.name}}>{msg('deviceManagement.rotate')}</button>{/if}<button type="button" class="btn-secondary btn-sm revoke" onclick={() => management={kind:'revoke',deviceID:d.device_id,deviceName:d.name}}>{msg('deviceManagement.revoke')}</button></div>{/if}</td>{/if}
							</tr>
						{/each}</tbody>
					</table></div>
				{/if}
			</div>
		</details>
	{/each}
</section>

{#if management}<DeviceManagementDialog {...management} onclose={() => management=null} onchanged={() => {notice=msg('deviceManagement.saved');void refresh();}} />{/if}
<style>
 .page-actions,.device-actions{display:flex;gap:8px;flex-wrap:wrap;} .device-actions{flex-direction:column;align-items:start;}.revoke{color:#b91c1c;}

	.stores-page { display: grid; gap: 16px; }
	.page-head { display: flex; align-items: start; justify-content: space-between; gap: 16px; }
	h2, h3, p { margin: 0; }
	h2 { font-size: 1.15rem; } h3 { font-size: .9rem; margin-bottom: 6px; }
	.hint, .sub { color: var(--text-muted); font-size: .8rem; line-height: 1.65; font-weight: 400; }
	.guide { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 20px; border-inline-start: 4px solid var(--primary); }
	.guide p { color: var(--text-muted); font-size: .83rem; line-height: 1.7; }
	.store-group { background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius-card); overflow: hidden; }
	.store-group > summary { display: flex; gap: 12px; align-items: center; padding: 16px; cursor: pointer; list-style: none; }
	.store-group > summary::-webkit-details-marker { display: none; }
	.store-group > summary:focus-visible { outline: 3px solid var(--primary); outline-offset: -3px; }
	.store-icon { display: grid; place-items: center; width: 44px; height: 44px; background: var(--surface-muted); color: var(--primary); border-radius: 10px; flex: none; }
	.store-heading { display: grid; gap: 3px; flex: 1; min-width: 0; }
	.store-heading strong { overflow-wrap: anywhere; } .device-count { font-size: .8rem; white-space: nowrap; color: var(--text-muted); }
	.chevron { flex: none; } .store-group[open] > summary .chevron { transform: rotate(180deg); }
	.store-group[open] > summary { border-bottom: 1px solid var(--border); }
	.store-body { padding: 16px; min-width: 0; } .store-id { font-size: .72rem; color: var(--text-muted); margin-bottom: 6px; overflow-wrap: anywhere; }
	.table-wrap { overflow-x: auto; border: 1px solid var(--border); border-radius: var(--radius-control); margin-top: 14px; }
	.grid-table { width: 100%; min-width: 940px; border-collapse: collapse; font-size: .82rem; }
	.grid-table th, .grid-table td { padding: 12px; text-align: start; vertical-align: top; border-bottom: 1px solid var(--border); }
	.grid-table tbody tr:last-child > * { border-bottom: 0; }
	.grid-table thead th { background: var(--surface-muted); color: var(--text-muted); font-size: .76rem; white-space: nowrap; }
	.name { font-weight: 700; } .sub { font-size: .68rem; overflow-wrap: anywhere; } .mono { font-family: ui-monospace, monospace; }
	.pill { display: inline-block; padding: 3px 9px; border-radius: 999px; background: var(--surface-muted); font-size: .76rem; font-weight: 600; white-space: nowrap; }
	.pill.ok { background: #e8f6ee; color: #15803d; } .warn-pill { background: #fdf4e7; color: #92400e; }
	.warn { color: #92400e; font-size: .74rem; margin-top: 6px; max-width: 26ch; line-height: 1.6; }
	.warning { padding: 12px 16px; background: #fff7ed; border: 1px solid #fed7aa; color: #9a3412; border-radius: var(--radius-control); font-size: .84rem; }
	.notice { padding: 12px 16px; background: var(--surface-muted); border-radius: var(--radius-control); font-size: .84rem; }
	.sync { display: grid; justify-items: start; gap: 8px; min-width: 120px; font-size: .8rem; }
	.history { max-width: 260px; } .history ul { padding-inline-start: 16px; margin: 6px 0 0; font-size: .74rem; }
	.empty { padding: 20px; color: var(--text-muted); }
	.sr-only { position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px; overflow: hidden; clip: rect(0,0,0,0); white-space: nowrap; border: 0; }
	@media (max-width: 700px) { .guide { grid-template-columns: 1fr; } .store-group > summary { flex-wrap: wrap; } }
</style>
