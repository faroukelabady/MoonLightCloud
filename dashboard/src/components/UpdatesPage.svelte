<script lang="ts">
	import { tb } from '../lib/i18n.svelte.js';
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
					return `${tb('توقيع أو بيان الإصدار مرفوض.', 'Release signature or manifest rejected')} (${reason}).`;
				case 'RELEASE_SEQUENCE_CONFLICT':
					return tb('تعارض: نفس التسلسل بمحتوى مختلف.', 'Conflict: same sequence with different content.');
				case 'RELEASE_TRUST_NOT_CONFIGURED':
					return tb('لم يتم تكوين مفاتيح الإصدار العامة.', 'Release public keys are not configured.');
				case 'RELEASE_REVOKED':
					return tb('الإصدار ملغى.', 'Release is revoked.');
				case 'ROLLOUT_STATE_CONFLICT':
					return tb('لا يمكن تنفيذ هذا الإجراء في الحالة الحالية.', 'Not allowed in the current rollout state.');
				case 'ROLLOUT_PERCENTAGE_CONFLICT':
					return tb('يجب أن تزيد النسبة وألا تتجاوز 100.', 'Percentage must increase and be at most 100.');
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
		if (!d.updater_reported) return tb('لم يبلغ بعد', 'Not reported (pre-Phase 18 Retail)');
		if (!d.updater_capable) return `${tb('غير مدعوم', 'Unsupported')}${d.unsupported_reason ? ` (${d.unsupported_reason})` : ''}`;
		return `${tb('مدعوم', 'Capable')} (protocol ${d.updater_protocol})`;
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
			notice = { kind: 'ok', text: `${tb('تم استيراد الإصدار', 'Imported release')} ${r.version} (#${r.release_sequence})` };
			envelopeText = '';
			urls = {};
			await loadReleases();
		} catch (e) {
			notice = { kind: 'err', text: errText(e, tb('فشل الاستيراد', 'Import failed')) };
		} finally {
			busy = false;
		}
	}

	async function setStatus(r: ReleaseView, status: 'active' | 'revoked') {
		if (status === 'revoked' && !confirm(`Revoke release ${r.version}? Devices that have not installed it will not receive it.`)) return;
		busy = true;
		try {
			await updatesApi.setReleaseStatus(r.id, status);
			notice = { kind: 'ok', text: status === 'revoked' ? tb('تم إلغاء الإصدار', 'Release revoked') : tb('تمت إعادة التفعيل', 'Release reactivated') };
			await loadReleases();
		} catch (e) {
			notice = { kind: 'err', text: errText(e, tb('فشل التغيير', 'Change failed')) };
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
			!confirm('Create a MANDATORY update rollout for ALL eligible devices in every Store? Devices install it automatically at their next safe point.')
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
			notice = { kind: 'ok', text: `${tb('تم إنشاء طرح كمسودة', 'Rollout created as draft')} (${r.target_count} devices)` };
			await loadRollouts(true);
		} catch (e) {
			notice = { kind: 'err', text: errText(e, tb('فشل الإنشاء', 'Create failed')) };
		} finally {
			busy = false;
		}
	}

	async function act(r: RolloutView, action: 'start' | 'pause' | 'resume' | 'cancel') {
		if (action === 'start' && r.mode === 'mandatory' && !confirm(`Start MANDATORY rollout of ${r.release_version} to ${r.target_count} device(s)?`)) return;
		if (action === 'cancel' && !confirm('Cancel this rollout? Devices already installing will finish or roll back safely.')) return;
		busy = true;
		try {
			await updatesApi.rolloutAction(r.id, action);
			await loadRollouts(true);
		} catch (e) {
			notice = { kind: 'err', text: errText(e, tb('فشل الإجراء', 'Action failed')) };
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
			notice = { kind: 'err', text: errText(e, tb('فشل التوسيع', 'Widen failed')) };
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
			if (my === epoch) notice = { kind: 'err', text: errText(e, tb('فشل التحميل', 'Load failed')) };
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
	<h2>{tb('التحديثات', 'Updates')}</h2>
	<p class="muted">
		{tb('يتم توقيع الإصدارات خارج السحابة؛ السحابة تتحقق فقط وتوزع.', 'Releases are signed offline; Cloud only verifies and distributes. Store-scoped views follow the Store selector.')}
	</p>
	<div class="tabs" role="tablist">
		<button class="tab" class:active={tab === 'fleet'} onclick={() => (tab = 'fleet')}>{tb('الأسطول', 'Fleet')}</button>
		<button class="tab" class:active={tab === 'releases'} onclick={() => (tab = 'releases')}>{tb('الإصدارات', 'Releases')}</button>
		<button class="tab" class:active={tab === 'rollouts'} onclick={() => (tab = 'rollouts')}>{tb('الطرح', 'Rollouts')}</button>
	</div>
	{#if notice}
		<p class="notice {notice.kind}" role="status" data-testid="updates-notice">{notice.text}</p>
	{/if}

	{#if tab === 'fleet'}
		{#if fleetState === 'error'}
			<p role="alert">{tb('تعذر تحميل الأسطول', 'Could not load fleet.')}</p>
		{:else if fleetState === 'loaded' && fleet.length === 0}
			<p class="muted">{tb('لا توجد أجهزة', 'No devices.')}</p>
		{:else}
			<table class="data">
				<thead>
					<tr><th>Device</th><th>Version</th><th>Updater</th><th>Device state</th><th>Target</th><th>Last seen</th></tr>
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
			{#if fleetCursor}<button class="btn-secondary btn-sm" onclick={() => loadFleet(false)} disabled={fleetState === 'loading'}>{tb('المزيد', 'More')}</button>{/if}
		{/if}
	{/if}

	{#if tab === 'releases'}
		<fieldset class="import">
			<legend>{tb('استيراد إصدار موقّع', 'Import signed release')}</legend>
			<label class="field">{tb('ملف الغلاف', 'Envelope file')} <input type="file" accept=".json,application/json" onchange={onEnvelopeFile} data-testid="envelope-file" /></label>
			<textarea rows="4" bind:value={envelopeText} placeholder="release-envelope.json" data-testid="envelope-text"></textarea>
			{#if envelopeText.trim() && !preview}
				<p role="alert">{tb('غلاف غير صالح', 'Not a release envelope.')}</p>
			{/if}
			{#if preview}
				<p class="muted">{tb('معاينة غير موثقة — يتحقق الخادم من التوقيع', 'Unverified preview — the server verifies the signature')}: {preview.version} (#{preview.sequence})</p>
				{#each preview.files as f}
					<label class="url">{f} <input type="url" placeholder="https://…" bind:value={urls[f]} data-testid="artifact-url" /></label>
				{/each}
				<button class="btn-primary" onclick={importRelease} disabled={busy} data-testid="import-release">{tb('استيراد', 'Import')}</button>
			{/if}
		</fieldset>
		{#if releasesState === 'error'}
			<p role="alert">{tb('تعذر تحميل الإصدارات', 'Could not load releases.')}</p>
		{:else}
			<table class="data">
				<thead>
					<tr><th>Version</th><th>Sequence</th><th>Key</th><th>Status</th><th>Imported</th><th></th></tr>
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
									<button class="btn-secondary btn-sm" onclick={() => setStatus(r, 'revoked')} disabled={busy}>{tb('إلغاء', 'Revoke')}</button>
								{:else}
									<button class="btn-secondary btn-sm" onclick={() => setStatus(r, 'active')} disabled={busy}>{tb('تفعيل', 'Reactivate')}</button>
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
			<legend>{tb('طرح جديد', 'New rollout')}</legend>
			<SingleSelect
				id="rollout-release"
				label={tb('الإصدار', 'Release')}
				testid="rollout-release"
				value={form.release_id}
				options={[{ value: '', label: '—' }, ...activeReleases.map((r) => ({ value: r.id, label: `${r.version} (#${r.release_sequence})`, dir: 'ltr' as const }))]}
				onchange={(v) => (form.release_id = v)}
			/>
			<SingleSelect
				id="rollout-scope"
				label={tb('النطاق', 'Scope')}
				testid="rollout-scope"
				value={form.scope}
				options={[
					{ value: 'store', label: tb('المتجر المحدد', 'Selected Store') },
					{ value: 'device', label: tb('جهاز', 'Device') },
					{ value: 'all', label: tb('كل الأجهزة', 'All devices') }
				]}
				onchange={(v) => (form.scope = v as typeof form.scope)}
			/>
			{#if form.scope === 'device'}
				<label class="field">Device ID <input bind:value={form.device_id} /></label>
			{/if}
			<SingleSelect
				id="rollout-mode"
				label={tb('الوضع', 'Mode')}
				testid="rollout-mode"
				value={form.mode}
				options={[{ value: 'optional', label: tb('اختياري', 'Optional') }, { value: 'mandatory', label: tb('إلزامي', 'Mandatory') }]}
				onchange={(v) => (form.mode = v as typeof form.mode)}
			/>
			<label class="field">% <input type="number" min="1" max="100" bind:value={form.percentage} /></label>
			{#if form.scope !== 'all' && !store}
				<p class="muted">{tb('اختر متجرًا أولاً', 'Choose a Store first.')}</p>
			{/if}
			<button class="btn-primary"
				onclick={createRollout}
				disabled={busy || !form.release_id || (form.scope !== 'all' && !store) || (form.scope === 'device' && !form.device_id.trim())}
				data-testid="create-rollout">{tb('إنشاء مسودة', 'Create draft')}</button
			>
		</fieldset>
		{#if rolloutsState === 'error'}
			<p role="alert">{tb('تعذر تحميل الطرح', 'Could not load rollouts.')}</p>
		{:else}
			<table class="data">
				<thead>
					<tr><th>Release</th><th>Scope</th><th>Mode</th><th>%</th><th>Status</th><th>Progress</th><th></th></tr>
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
								{#if r.status === 'draft'}<button class="btn-secondary btn-sm" onclick={() => act(r, 'start')} disabled={busy}>{tb('بدء', 'Start')}</button>{/if}
								{#if r.status === 'active'}<button class="btn-secondary btn-sm" onclick={() => act(r, 'pause')} disabled={busy}>{tb('إيقاف مؤقت', 'Pause')}</button>{/if}
								{#if r.status === 'paused'}<button class="btn-secondary btn-sm" onclick={() => act(r, 'resume')} disabled={busy}>{tb('استئناف', 'Resume')}</button>{/if}
								{#if (r.status === 'active' || r.status === 'paused' || r.status === 'draft') && r.percentage < 100}
									<button class="btn-secondary btn-sm" onclick={() => widen(r)} disabled={busy}>{tb('توسيع', 'Widen')}</button>
								{/if}
								{#if r.status !== 'completed' && r.status !== 'cancelled'}
									<button class="btn-secondary btn-sm" onclick={() => act(r, 'cancel')} disabled={busy}>{tb('إلغاء', 'Cancel')}</button>
								{/if}
								<button class="link" onclick={() => showTargets(r)}>{tb('الأجهزة', 'Devices')}</button>
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
			{#if rolloutsCursor}<button class="btn-secondary btn-sm" onclick={() => loadRollouts(false)} disabled={rolloutsState === 'loading'}>{tb('المزيد', 'More')}</button>{/if}
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
