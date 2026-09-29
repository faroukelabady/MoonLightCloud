<script lang="ts">
	import { onMount } from 'svelte';
	import { dashboardApi, ApiError } from '../lib/api';
	import type { DeviceRow, DeviceCommandWire } from '../lib/api';

	let devices: DeviceRow[] = [];
	let loading = true;
	let error: string | null = null;
	let requesting: Record<string, boolean> = {};
	let notice: string | null = null;

	function userFacing(status: string): string {
		switch (status) {
			case 'pending':
				return 'Queued';
			case 'leased':
				return 'Delivering';
			case 'accepted':
				return 'Received by device';
			case 'running':
				return 'Syncing';
			case 'completed':
				return 'Completed';
			case 'failed':
				return 'Failed';
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
			notice = res.created ? 'Sync requested — queued for device.' : 'Sync request already active.';
			await refresh();
		} catch (e) {
			if (e instanceof ApiError) notice = e.code;
			else notice = 'INTERNAL';
		} finally {
			requesting[deviceId] = false;
		}
	}
</script>

<section aria-label="Devices">
	<h2>Devices</h2>
	<p class="hint">ONLINE means the Cloud control plane heard from the Retail app recently. It does not prove data is current.</p>
	{#if loading}<p>Loading…</p>
	{:else if error}<p role="alert">Failed: {error}</p>
	{:else if devices.length === 0}<p>No devices registered.</p>
	{:else}
		<ul>
			{#each devices as d (d.device_id)}
				{@const active = d.active_command ?? null}
				{@const terminal = lastTerminal(d)}
				{@const past = history(d)}
				<li data-testid="device-row" data-device={d.device_id}>
					<strong>{d.name || d.device_id}</strong>
					<span data-testid="connectivity">{d.connectivity}</span>
					<span title="last seen">{d.last_seen_at ?? 'never'}</span>
					{#if active}
						<span data-testid="command-status">{userFacing(active.status)}</span>
						<button disabled title="A sync request is already active">Sync Now</button>
					{:else}
						{#if terminal}
							<span data-testid="last-sync">Last sync: {userFacing(terminal.status)}</span>
						{:else}
							<span data-testid="command-status">idle</span>
						{/if}
						<button data-testid="sync-now" disabled={!!requesting[d.device_id]} on:click={() => syncNow(d.device_id, false)}>Sync Now</button>
						{#if past.length > 0}
							<ul data-testid="sync-history">
								{#each past as c (c.id)}
									<li>{userFacing(c.status)} — {c.requested_at}</li>
								{/each}
							</ul>
						{/if}
					{/if}
				</li>
			{/each}
		</ul>
	{/if}
	{#if notice}<p role="status">{notice}</p>{/if}
</section>

<style>
	.hint {
		color: var(--muted, #666);
	}
</style>
