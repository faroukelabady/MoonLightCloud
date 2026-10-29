<script lang="ts">
	import { msg } from '../lib/i18n.svelte.js';
	import SingleSelect from './ui/SingleSelect.svelte';
	import { untrack } from 'svelte';
	import { updatesApi, updateStateLabel, previewEnvelope, ApiError } from '../lib/api.js';
	import type { ReleaseView, RolloutView, FleetDeviceRow, UpdateTargetView } from '../lib/api.js';

	// UpdatesPage: Phase 18 release registry + fleet rollout control
	// (ADR-0051/0052). Cloud never builds or signs releases: an import is
	// the signed envelope from offline release tooling plus artifact URLs,
	// verified server-side against the configured public keys. Device
	// states shown here are exactly what Retail reported; "Succeeded" only
	// appears after Retail confirmed healthy startup.
	let { store }: { store: string } = $props();

	type Tab = 'fleet' | 'releases' | 'rollouts';
	let tab: Tab = $state('fleet');
	let epoch = 0;
	let notice = $state<{ kind: 'ok' | 'err'; text: string } | null>(null);
	let busy = $state(false);

	// The server's wire code is generic (INVALID_INPUT/CONFLICT); the
	// stable reason travels in the error message (ApiError.detail).
	function errText(err: unknown, fallback: string): string {
		if (err instanceof ApiError) {
			const reason = err.detail;
			switch (reason) {
				case 'UPDATE_SIGNATURE_INVALID':
				case 'UPDATE_KEY_UNKNOWN':
				case 'UPDATE_MANIFEST_INVALID':
				case 'UPDATE_MANIFEST_UNSUPPORTED':
					return `${msg('updatesPage.releaseSignatureOrManifestRejected')} (${reason}).`;
				case 'RELEASE_SEQUENCE_CONFLICT':
					return msg('updatesPage.conflictSameSequenceWithDifferentContent');
				case 'RELEASE_TRUST_NOT_CONFIGURED':
					return msg('updatesPage.releasePublicKeysAreNotConfigured');
				case 'RELEASE_REVOKED':
					return msg('updatesPage.releaseIsRevoked');
				case 'ROLLOUT_STATE_CONFLICT':
					return msg('updatesPage.notAllowedInTheCurrentRollout');
				case 'ROLLOUT_PERCENTAGE_CONFLICT':
					return msg('updatesPage.percentageMustIncreaseAndBeAt');
			}
			return `${fallback} (${err.code}${reason ? `: ${reason}` : ''})`;
		}
		return fallback;
	}

	function when(ts: string | undefined): string {
		return ts ? new Date(ts).toLocaleString() : '—';
	}

	// ---- fleet ----
	let fleet: FleetDeviceRow[] = $state([]);
	let fleetCursor = $state('');
	let fleetState = $state<'idle' | 'loading' | 'loaded' | 'error'>('idle');

	async function loadFleet(reset: boolean) {
		const my = epoch;
		fleetState = 'loading';
		try {
			const v = await updatesApi.fleet(store, reset ? undefined : fleetCursor);
			if (my !== epoch) return;
			fleet = reset ? v.devices : [...fleet, ...v.devices];
			fleetCursor = v.next_cursor;
			fleetState = 'loaded';
		} catch {
			if (my === epoch) fleetState = 'error';
		}
	}

	function capability(d: FleetDeviceRow): string {
		if (!d.updater_reported) return msg('updatesPage.notReportedPrePhase18Retail');
		if (!d.updater_capable) return `${msg('updatesPage.unsupported')}${d.unsupported_reason ? ` (${d.unsupported_reason})` : ''}`;
		return `${msg('updatesPage.capable')} (protocol ${d.updater_protocol})`;
	}

	// ---- releases ----
	let releases: ReleaseView[] = $state([]);
	let releasesState = $state<'idle' | 'loading' | 'loaded' | 'error'>('idle');
	let envelopeText = $state('');
	let urls: Record<string, string> = $state({});
	let preview = $derived(previewEnvelope(envelopeText.trim()));

	async function loadReleases() {
		const my = epoch;
		releasesState = 'loading';
		try {
			const v = await updatesApi.releases();
			if (my !== epoch) return;
			releases = v.releases ?? [];
			releasesState = 'loaded';
		} catch {
			if (my === epoch) releasesState = 'error';
		}
	}

	async function onEnvelopeFile(ev: Event) {
		const file = (ev.currentTarget as HTMLInputElement).files?.[0];
		if (file) envelopeText = await file.text();
	}

	async function importRelease() {
		if (!preview) return;
		busy = true;
		try {
			const artifactURLs: Record<string, string> = {};
			for (const f of preview.files) if (urls[f]?.trim()) artifactURLs[f] = urls[f].trim();
			const r = await updatesApi.importRelease(envelopeText.trim(), artifactURLs);
			notice = { kind: 'ok', text: `${msg('updatesPage.importedRelease')} ${r.version} (#${r.release_sequence})` };
			envelopeText = '';
			urls = {};
			await loadReleases();
		} catch (e) {
			notice = { kind: 'err', text: errText(e, msg('updatesPage.importFailed')) };
		} finally {
			busy = false;
		}
	}

	async function setStatus(r: ReleaseView, status: 'active' | 'revoked') {
		if (status === 'revoked' && !confirm(msg('updatesPage.confirmRevoke', { version: r.version }))) return;
		busy = true;
		try {
			await updatesApi.setReleaseStatus(r.id, status);
			notice = { kind: 'ok', text: status === 'revoked' ? msg('updatesPage.releaseRevoked') : msg('updatesPage.releaseReactivated') };
			await loadReleases();
		} catch (e) {
			notice = { kind: 'err', text: errText(e, msg('updatesPage.changeFailed')) };
		} finally {
			busy = false;
		}
	}

	// ---- rollouts ----
	let rollouts: RolloutView[] = $state([]);
	let rolloutsCursor = $state('');
	let rolloutsState = $state<'idle' | 'loading' | 'loaded' | 'error'>('idle');
	let form = $state({ release_id: '', scope: 'store' as 'device' | 'store' | 'all', device_id: '', mode: 'optional' as 'optional' | 'mandatory', percentage: 10 });
	let openTargets = $state<{ id: string; rows: UpdateTargetView[] } | null>(null);

	async function loadRollouts(reset: boolean) {
		const my = epoch;
		rolloutsState = 'loading';
		try {
			const v = await updatesApi.rollouts(store, reset ? undefined : rolloutsCursor);
			if (my !== epoch) return;
			rollouts = reset ? v.rollouts : [...rollouts, ...v.rollouts];
			rolloutsCursor = v.next_cursor;
			rolloutsState = 'loaded';
		} catch {
			if (my === epoch) rolloutsState = 'error';
		}
	}

	async function createRollout() {
		if (busy || !form.release_id || (form.scope !== 'all' && !store) || (form.scope === 'device' && !form.device_id.trim())) return;
		if (
			form.mode === 'mandatory' &&
			form.scope === 'all' &&
			!confirm(msg('updatesPage.confirmMandatoryAll'))
		)
			return;
		busy = true;
		try {
			const r = await updatesApi.createRollout({
				release_id: form.release_id,
				scope: form.scope,
				store_id: form.scope !== 'all' ? store : undefined,
				device_id: form.scope === 'device' ? form.device_id.trim() : undefined,
				mode: form.mode,
				percentage: Number(form.percentage)
			});
			notice = { kind: 'ok', text: `${msg('updatesPage.rolloutCreatedAsDraft')} (${r.target_count} devices)` };
			await loadRollouts(true);
		} catch (e) {
			notice = { kind: 'err', text: errText(e, msg('updatesPage.createFailed')) };
		} finally {
			busy = false;
		}
	}

	async function act(r: RolloutView, action: 'start' | 'pause' | 'resume' | 'cancel') {
		if (action === 'start' && r.mode === 'mandatory' && !confirm(msg('updatesPage.confirmStartMandatory', { version: r.release_version, count: r.target_count }))) return;
		if (action === 'cancel' && !confirm(msg('updatesPage.confirmCancel'))) return;
		busy = true;
		try {
			await updatesApi.rolloutAction(r.id, action);
			await loadRollouts(true);
		} catch (e) {
			notice = { kind: 'err', text: errText(e, msg('updatesPage.actionFailed')) };
		} finally {
			busy = false;
		}
	}

	async function widen(r: RolloutView) {
		const raw = prompt(`New percentage (> ${r.percentage}, ≤ 100)`, String(Math.min(100, r.percentage * 2 || 10)));
		if (raw === null) return;
		busy = true;
		try {
			await updatesApi.widenRollout(r.id, Number(raw));
			await loadRollouts(true);
		} catch (e) {
			notice = { kind: 'err', text: errText(e, msg('updatesPage.widenFailed')) };
		} finally {
			busy = false;
		}
	}

	async function showTargets(r: RolloutView) {
		const my = epoch;
		try {
			const v = await updatesApi.targets(r.id, store);
			if (my !== epoch) return; // scope changed while loading
			openTargets = { id: r.id, rows: v.targets };
		} catch (e) {
			if (my === epoch) notice = { kind: 'err', text: errText(e, msg('updatesPage.loadFailed')) };
		}
	}

	function reloadTab() {
		epoch++;
		if (tab === 'fleet') void loadFleet(true);
		else if (tab === 'releases') void loadReleases();
		else {
			void loadReleases();
			void loadRollouts(true);
		}
	}

	// A Store change drops every Store-scoped list before reloading, so the
	// previous Store's devices/rollouts are never shown under the new scope
	// while its request is pending or after it fails. Releases are global.
	let scopedStore = untrack(() => store);
	$effect(() => {
		void store;
		void tab;
		untrack(() => {
			openTargets = null;
			if (store !== scopedStore) {
				scopedStore = store;
				form.device_id = '';
				fleet = [];
				fleetCursor = '';
				fleetState = 'idle';
				rollouts = [];
				rolloutsCursor = '';
				rolloutsState = 'idle';
			}
			reloadTab();
		});
	});

	let activeReleases = $derived(releases.filter((r) => r.status === 'active'));
</script>

<section class="updates card" data-testid="updates-page">
	<h2>{msg('updatesPage.updates')}</h2>
	<p class="muted">
		{msg('updatesPage.releasesAreSignedOfflineCloudOnly')}
	</p>
	<div class="tabs" role="tablist">
		<button class="tab" class:active={tab === 'fleet'} onclick={() => (tab = 'fleet')}>{msg('updatesPage.fleet')}</button>
		<button class="tab" class:active={tab === 'releases'} onclick={() => (tab = 'releases')}>{msg('updatesPage.releases')}</button>
		<button class="tab" class:active={tab === 'rollouts'} onclick={() => (tab = 'rollouts')}>{msg('updatesPage.rollouts')}</button>
	</div>
	{#if notice}
		<p class="notice {notice.kind}" role="status" data-testid="updates-notice">{notice.text}</p>
	{/if}

	{#if tab === 'fleet'}
		{#if fleetState === 'error'}
			<p role="alert">{msg('updatesPage.couldNotLoadFleet')}</p>
		{:else if fleetState === 'loaded' && fleet.length === 0}
			<p class="muted">{msg('updatesPage.noDevices')}</p>
		{:else}
			<table class="data">
				<thead>
					<tr><th>{msg('updatesPage.colDevice')}</th><th>{msg('updatesPage.colVersion')}</th><th>{msg('updatesPage.colUpdater')}</th><th>{msg('updatesPage.colDeviceState')}</th><th>{msg('updatesPage.colTarget')}</th><th>{msg('updatesPage.colLastSeen')}</th></tr>
				</thead>
				<tbody>
					{#each fleet as d (d.device_id)}
						<tr data-testid="fleet-row" data-device={d.device_id}>
							<td>{d.device_name || d.device_id}</td>
							<td class="num">{d.version ? `${d.version} (#${d.release_sequence})` : '—'}</td>
							<td>{capability(d)}</td>
							<td>{d.update_state || '—'}{d.update_error ? ` · ${d.update_error}` : ''}</td>
							<td data-testid="fleet-target">
								{#if d.target_state}
									{updateStateLabel(d.target_state)} → {d.target_version} (#{d.target_sequence}){d.target_error ? ` · ${d.target_error}` : ''}
								{:else}
									{updateStateLabel(undefined)}
								{/if}
							</td>
							<td>{when(d.last_seen_at)}</td>
						</tr>
					{/each}
				</tbody>
			</table>
			{#if fleetCursor}<button class="btn-secondary btn-sm" onclick={() => loadFleet(false)} disabled={fleetState === 'loading'}>{msg('updatesPage.more')}</button>{/if}
		{/if}
	{/if}

	{#if tab === 'releases'}
		<fieldset class="import">
			<legend>{msg('updatesPage.importSignedRelease')}</legend>
			<label class="field">{msg('updatesPage.envelopeFile')} <input type="file" accept=".json,application/json" onchange={onEnvelopeFile} data-testid="envelope-file" /></label>
			<textarea rows="4" bind:value={envelopeText} placeholder="release-envelope.json" data-testid="envelope-text"></textarea>
			{#if envelopeText.trim() && !preview}
				<p role="alert">{msg('updatesPage.notAReleaseEnvelope')}</p>
			{/if}
			{#if preview}
				<p class="muted">{msg('updatesPage.unverifiedPreviewTheServerVerifiesThe')}: {preview.version} (#{preview.sequence})</p>
				{#each preview.files as f}
					<label class="url">{f} <input type="url" placeholder="https://…" bind:value={urls[f]} data-testid="artifact-url" /></label>
				{/each}
				<button class="btn-primary" onclick={importRelease} disabled={busy} data-testid="import-release">{msg('updatesPage.import')}</button>
			{/if}
		</fieldset>
		{#if releasesState === 'error'}
			<p role="alert">{msg('updatesPage.couldNotLoadReleases')}</p>
		{:else}
			<table class="data">
				<thead>
					<tr><th>{msg('updatesPage.colVersion')}</th><th>{msg('updatesPage.colSequence')}</th><th>{msg('updatesPage.colKey')}</th><th>{msg('updatesPage.colStatus')}</th><th>{msg('updatesPage.colImported')}</th><th></th></tr>
				</thead>
				<tbody>
					{#each releases as r (r.id)}
						<tr data-testid="release-row">
							<td>{r.version} <span class="muted mono">{r.build_commit.slice(0, 12)}</span></td>
							<td class="num">#{r.release_sequence}{r.min_installed_sequence ? ` (min #${r.min_installed_sequence})` : ''}</td>
							<td class="mono">{r.key_id}</td>
							<td>{r.status}</td>
							<td>{when(r.imported_at)} · {r.imported_by}</td>
							<td>
								{#if r.status === 'active'}
									<button class="btn-secondary btn-sm" onclick={() => setStatus(r, 'revoked')} disabled={busy}>{msg('updatesPage.revoke')}</button>
								{:else}
									<button class="btn-secondary btn-sm" onclick={() => setStatus(r, 'active')} disabled={busy}>{msg('updatesPage.reactivate')}</button>
								{/if}
							</td>
						</tr>
					{/each}
				</tbody>
			</table>
		{/if}
	{/if}

	{#if tab === 'rollouts'}
		<fieldset class="create">
			<legend>{msg('updatesPage.newRollout')}</legend>
			<SingleSelect
				id="rollout-release"
				label={msg('updatesPage.release')}
				testid="rollout-release"
				value={form.release_id}
				options={[{ value: '', label: '—' }, ...activeReleases.map((r) => ({ value: r.id, label: `${r.version} (#${r.release_sequence})`, dir: 'ltr' as const }))]}
				onchange={(v) => (form.release_id = v)}
			/>
			<SingleSelect
				id="rollout-scope"
				label={msg('updatesPage.scope')}
				testid="rollout-scope"
				value={form.scope}
				options={[
					{ value: 'store', label: msg('updatesPage.selectedStore') },
					{ value: 'device', label: msg('updatesPage.device') },
					{ value: 'all', label: msg('updatesPage.allDevices') }
				]}
				onchange={(v) => (form.scope = v as typeof form.scope)}
			/>
			{#if form.scope === 'device'}
				<label class="field">{msg('updatesPage.deviceId')} <input bind:value={form.device_id} dir="ltr" /></label>
			{/if}
			<SingleSelect
				id="rollout-mode"
				label={msg('updatesPage.mode')}
				testid="rollout-mode"
				value={form.mode}
				options={[{ value: 'optional', label: msg('updatesPage.optional') }, { value: 'mandatory', label: msg('updatesPage.mandatory') }]}
				onchange={(v) => (form.mode = v as typeof form.mode)}
			/>
			<label class="field">% <input type="number" min="1" max="100" bind:value={form.percentage} /></label>
			{#if form.scope !== 'all' && !store}
				<p class="muted">{msg('updatesPage.chooseAStoreFirst')}</p>
			{/if}
			<button class="btn-primary"
				onclick={createRollout}
				disabled={busy || !form.release_id || (form.scope !== 'all' && !store) || (form.scope === 'device' && !form.device_id.trim())}
				data-testid="create-rollout">{msg('updatesPage.createDraft')}</button
			>
		</fieldset>
		{#if rolloutsState === 'error'}
			<p role="alert">{msg('updatesPage.couldNotLoadRollouts')}</p>
		{:else}
			<table class="data">
				<thead>
					<tr><th>{msg('updatesPage.colRelease')}</th><th>{msg('updatesPage.colScope')}</th><th>{msg('updatesPage.colMode')}</th><th>%</th><th>{msg('updatesPage.colStatus')}</th><th>{msg('updatesPage.colProgress')}</th><th></th></tr>
				</thead>
				<tbody>
					{#each rollouts as r (r.id)}
						<tr data-testid="rollout-row" data-status={r.status}>
							<td>{r.release_version} (#{r.release_sequence})</td>
							<td>{r.scope}{r.store_id ? ` · ${r.store_id.slice(0, 8)}` : ''}{r.device_id ? ` · ${r.device_id.slice(0, 8)}` : ''}</td>
							<td>{r.mode}</td>
							<td class="num">{r.percentage}</td>
							<td>{r.status}</td>
							<td class="counts">
								{#each Object.entries(r.counts ?? {}) as [state, n]}<span class="chip">{updateStateLabel(state)}: {n}</span>{/each}
							</td>
							<td class="actions">
								{#if r.status === 'draft'}<button class="btn-secondary btn-sm" onclick={() => act(r, 'start')} disabled={busy}>{msg('updatesPage.start')}</button>{/if}
								{#if r.status === 'active'}<button class="btn-secondary btn-sm" onclick={() => act(r, 'pause')} disabled={busy}>{msg('updatesPage.pause')}</button>{/if}
								{#if r.status === 'paused'}<button class="btn-secondary btn-sm" onclick={() => act(r, 'resume')} disabled={busy}>{msg('updatesPage.resume')}</button>{/if}
								{#if (r.status === 'active' || r.status === 'paused' || r.status === 'draft') && r.percentage < 100}
									<button class="btn-secondary btn-sm" onclick={() => widen(r)} disabled={busy}>{msg('updatesPage.widen')}</button>
								{/if}
								{#if r.status !== 'completed' && r.status !== 'cancelled'}
									<button class="btn-secondary btn-sm" onclick={() => act(r, 'cancel')} disabled={busy}>{msg('updatesPage.cancel')}</button>
								{/if}
								<button class="link" onclick={() => showTargets(r)}>{msg('updatesPage.devices')}</button>
							</td>
						</tr>
						{#if openTargets?.id === r.id}
							<tr>
								<td colspan="7">
									<ul class="targets">
										{#each openTargets.rows as t (t.id)}
											<li data-testid="target-row">
												{t.device_name || t.device_id}: {updateStateLabel(t.state)}{t.last_error ? ` · ${t.last_error}` : ''} · attempts {t.attempt_count}
											</li>
										{/each}
									</ul>
								</td>
							</tr>
						{/if}
					{/each}
				</tbody>
			</table>
			{#if rolloutsCursor}<button class="btn-secondary btn-sm" onclick={() => loadRollouts(false)} disabled={rolloutsState === 'loading'}>{msg('updatesPage.more')}</button>{/if}
		{/if}
	{/if}
</section>

<style>
	.updates { display: flex; flex-direction: column; gap: 0.75rem; }
	.tabs { display: flex; gap: 0.5rem; flex-wrap: wrap; }
	fieldset { display: flex; flex-wrap: wrap; gap: 0.5rem; align-items: end; }
	fieldset textarea { width: 100%; font-family: monospace; }
	.url { display: flex; gap: 0.5rem; align-items: center; width: 100%; }
	.url input { flex: 1; min-width: 0; }
	.notice { font-weight: bold; }
	.chip { border: 1px solid currentColor; border-radius: 0.4rem; padding: 0.1rem 0.5rem; margin-inline-end: 0.3rem; white-space: nowrap; }
	.link { background: none; border: none; padding: 0; cursor: pointer; text-decoration: underline; color: inherit; font: inherit; }
	.actions { display: flex; gap: 0.3rem; flex-wrap: wrap; }
	.muted { opacity: 0.75; }
	.mono { font-family: monospace; }
	.num { font-variant-numeric: tabular-nums; }
	table.data { width: 100%; border-collapse: collapse; display: block; overflow-x: auto; }
	table.data th, table.data td { text-align: start; padding: 0.3rem 0.5rem; }
</style>
