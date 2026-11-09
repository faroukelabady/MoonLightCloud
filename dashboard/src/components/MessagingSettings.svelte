<script lang="ts">
	import { onDestroy } from 'svelte';
	import { ApiError } from '../lib/http.js';
	import { settingsApi, type Messaging, type MessagingProvider, type MessagingTemplate, type MessagingRecipientDetail, type MessagingRecipient, type MessagingSchedule, type MessagingRun } from '../lib/settingsApi.js';
	import { absoluteTime } from '../lib/time.js';
	import { deliveryState, runSendState, sendSucceeded, sendFailed } from '../lib/messagingDelivery.js';
	import { lang, msg } from '../lib/i18n.svelte.js';
	import Modal from './ui/Modal.svelte';
	import MessagingProviderModal from './MessagingProviderModal.svelte';
	import MessagingProviderIcon from './MessagingProviderIcon.svelte';
	import MessagingRecipientModal from './MessagingRecipientModal.svelte';
	import MessagingScheduleForm from './MessagingScheduleForm.svelte';

	let data = $state<Messaging | null>(null);
	let loading = $state(true);
	let busy = $state(false);
	let notice = $state<{ kind: 'ok' | 'err'; text: string } | null>(null);
	let providerKey = $state('');
	let recipientOpen = $state(false);
	let recipientEditing = $state<MessagingRecipientDetail | null>(null);
	let editingSchedule = $state<MessagingSchedule | null>(null);
	let formVersion = $state(0);
	let modalError = $state('');
	let removing = $state<{ kind: 'recipient' | 'schedule'; id: string; name: string } | null>(null);
	let schedulePanel: HTMLElement | undefined = $state();
	let rows = $state<MessagingRun[]>([]);
	let nextCursor = $state('');
	let remainingRuns = $state(0);
	let cursors = $state<string[]>(['']);
	let historyBusy = $state(false);
	let historyStale = $state(false);
	let historyRequest: AbortController | undefined;
	let historyEpoch = 0;
	let selectedProvider = $derived(data?.providers.find((p) => p.key === providerKey));
	const PAGE_SIZE = 10;
	onDestroy(() => { historyEpoch++; historyRequest?.abort(); });

	function errorText(err: unknown): string {
		if (err instanceof ApiError) {
			if (err.status === 401) return msg('authApi.sessionExpiredPleaseSignInAgain');
			if (err.detail === 'WHATSAPP_COUNTRY_CODE_REQUIRED') return msg('messagingSettings.leadingZeroError');
			if (err.detail === 'INVALID_RECIPIENT') return msg('messagingSettings.invalidAddressWhatsappNeedsAnInternational');
			if (err.detail === 'REPORT_NO_RECIPIENTS') return msg('messagingSettings.chooseAtLeastOneRecipient');
			if (err.detail === 'SCHEDULE_CHANGED' || err.detail === 'RECIPIENT_CHANGED') return msg('messagingSettings.configurationChanged');
			if (err.code === 'CONFLICT') return msg('messagingSettings.aScheduleWithThisNameAlready');
			if (err.code === 'FORBIDDEN') return msg('messagingSettings.youDoNotHavePermissionTo');
			if (err.code === 'INVALID_INPUT' && err.detail) return err.detail;
		}
		return msg('messagingSettings.couldNotSaveTryAgain');
	}

	async function load() {
		loading = true;
		try {
			data = await settingsApi.messaging();
			historyEpoch++;
			historyRequest?.abort();
			historyBusy = false;
			historyStale = false;
			cursors = [''];
			rows = data.runs.slice(0, PAGE_SIZE);
			nextCursor = data.runs_next_cursor ?? (data.runs.length > PAGE_SIZE ? rows[rows.length - 1].id : '');
			remainingRuns = data.runs_remaining ?? Math.max(0, data.runs.length-PAGE_SIZE);
		} catch (err) { notice = { kind: 'err', text: errorText(err) }; }
		finally { loading = false; }
	}
	$effect(() => { void load(); });
	$effect(() => {
		const timer = window.setInterval(() => {
			if (data && !loading && !busy && !historyBusy && document.visibilityState === 'visible') void historyPage('current', true);
		}, 5000);
		return () => window.clearInterval(timer);
	});

	async function run(action: () => Promise<unknown>, ok: string, modal = false): Promise<boolean> {
		if (busy) return false;
		busy = true; notice = null; modalError = '';
		try {
			await action();
			notice = { kind: 'ok', text: ok };
			await load();
			return true;
		} catch (err) {
			if (modal) modalError = errorText(err);
			else notice = { kind: 'err', text: errorText(err) };
			return false;
		} finally { busy = false; }
	}

	function configure(kind: MessagingProvider['kind']) {
		providerKey = data?.providers.find((p) => p.kind === kind)?.key ?? '';
		modalError = '';
	}
	async function saveTemplate(template: MessagingTemplate & { provider_key: string }) {
		if (await run(() => settingsApi.setTemplate(template), msg('messagingSettings.templateSaved'), true)) providerKey = '';
	}
	function addRecipient() { recipientEditing = null; modalError = ''; recipientOpen = true; }
	async function editRecipient(recipient: MessagingRecipient) {
		if (busy) return;
		busy = true; modalError = ''; notice = null;
		try { recipientEditing = await settingsApi.getRecipient(recipient.id); recipientOpen = true; }
		catch (err) { notice = { kind: 'err', text: errorText(err) }; }
		finally { busy = false; }
	}
	async function saveRecipient(draft: { label: string; provider_key: string; recipient: string; locale: string }) {
		const id = recipientEditing?.id;
		if (await run(() => id ? settingsApi.updateRecipient(id, { label: draft.label, recipient: draft.recipient, locale: draft.locale }) : settingsApi.addRecipient(draft), msg(id ? 'messagingSettings.recipientUpdated' : 'messagingSettings.recipientAdded'), true)) recipientOpen = false;
	}
	function editSchedule(schedule: MessagingSchedule) {
		editingSchedule = schedule;
		schedulePanel?.scrollIntoView?.({ behavior: 'smooth', block: 'start' });
	}
	async function saveSchedule(draft: { name: string; kind: string; local_time: string; anchor_local_date: string; recipient_ids: string[]; revision: number }) {
		const id = editingSchedule?.id;
		if (await run(() => id ? settingsApi.updateSchedule(id, draft) : settingsApi.addSchedule({ name: draft.name, kind: draft.kind, local_time: draft.local_time, anchor_local_date: draft.anchor_local_date, recipient_ids: draft.recipient_ids }), msg(id ? 'messagingSettings.scheduleUpdated' : 'messagingSettings.scheduleCreated'))) { editingSchedule = null; formVersion++; }
	}
	function sendNow(id: string) {
		const key = typeof crypto !== 'undefined' && 'randomUUID' in crypto ? crypto.randomUUID() : `${Date.now()}-${Math.random()}`;
		void run(() => settingsApi.runSchedule(id, key), msg(data?.scheduler_enabled ? 'messagingSettings.theReportWillBeSentWithin' : 'messagingSettings.queuedButTheSchedulerIsOff'));
	}
	async function confirmDelete() {
		if (!removing) return;
		const target = removing;
		if (await run(() => target.kind === 'recipient' ? settingsApi.deleteRecipient(target.id) : settingsApi.deleteSchedule(target.id), msg('messagingSettings.configurationRemoved'), true)) {
			if (editingSchedule?.id === target.id) editingSchedule = null;
			removing = null;
		}
	}
	async function historyPage(direction: 'next' | 'previous' | 'refresh' | 'current', quiet = false) {
		if (historyBusy) return;
		const target = direction === 'next' ? [...cursors, nextCursor] : direction === 'previous' ? cursors.slice(0, -1) : direction === 'current' ? [...cursors] : [''];
		if (!target.length || direction === 'next' && !nextCursor) return;
		historyRequest?.abort();
		const request = new AbortController(); historyRequest = request;
		const epoch = ++historyEpoch;
		historyBusy = true;
		try {
			const page = await settingsApi.runPage(target[target.length - 1], request.signal);
			if (epoch !== historyEpoch) return;
			rows = page.runs; nextCursor = page.next_cursor; remainingRuns = page.remaining_runs; cursors = target; historyStale = false;
		} catch (err) { if (epoch === historyEpoch && !request.signal.aborted) { historyStale = true; if (!quiet) notice = { kind: 'err', text: errorText(err) }; } }
		finally { if (epoch === historyEpoch) historyBusy = false; }
	}
	function providerLabel(key: string) { return data?.providers.find((p) => p.key === key)?.kind === 'whatsapp' ? 'WhatsApp' : 'Telegram'; }

</script>

<div class="messaging" aria-labelledby="messaging-title">
	<section class="setup-panel">
		<header><div><h3 id="messaging-title">{msg('messagingSettings.sendingReportsOverWhatsappAndTelegram')}</h3><p class="sub">{msg('messagingSettings.aDailyOr10DaySummary')}</p></div></header>
		{#if notice}<div class="notice {notice.kind}" role={notice.kind === 'err' ? 'alert' : 'status'}>{notice.text}</div>{/if}
		{#if loading && !data}<p class="sub" role="status">{msg('messagingSettings.loading')}</p>{/if}
		{#if data && !data.scheduler_enabled}<p class="warn" data-testid="scheduler-off">{msg('messagingSettings.theSchedulerIsOffOnThe')}</p>{/if}
		{#if data && data.providers.length === 0}<p class="warn" data-testid="no-providers">{msg('messagingSettings.noMessagingProviderIsEnabledWhatsapp')}</p>{/if}

	</section>

	{#if data}
		<section class="card" aria-labelledby="recipients-title" data-testid="recipients-panel">
			<header class="recipient-header"><div><h3 id="recipients-title">{msg('messagingSettings.recipients')}</h3><p class="sub">{msg('messagingSettings.recipientsPanelHelp')}</p></div><div class="recipient-actions"><button type="button" class="btn-primary" disabled={busy || data.providers.length === 0} onclick={addRecipient}>{msg('messagingSettings.addRecipient')}</button>
				<button type="button" class="channel whatsapp" data-testid="provider-whatsapp" disabled={!data.providers.some((p) => p.kind === 'whatsapp') || busy} onclick={() => configure('whatsapp')}><MessagingProviderIcon kind="whatsapp" /><span>{msg('messagingSettings.configureWhatsapp')}</span></button>
				<button type="button" class="channel telegram" data-testid="provider-telegram" disabled={!data.providers.some((p) => p.kind === 'telegram') || busy} onclick={() => configure('telegram')}><MessagingProviderIcon kind="telegram" /><span>{msg('messagingSettings.configureTelegram')}</span></button>
			</div></header>
			{#if data.recipients.length === 0}<p class="empty">{msg('messagingSettings.noRecipients')}</p>{:else}
				<div class="table-wrap"><table class="data"><thead><tr><th>{msg('messagingSettings.name')}</th><th>{msg('messagingSettings.provider')}</th><th>{msg('messagingSettings.address')}</th><th>{msg('messagingSettings.language')}</th><th>{msg('messagingSettings.status')}</th><th>{msg('messagingSettings.actions')}</th></tr></thead><tbody>
				{#each data.recipients as recipient (recipient.id)}
					<tr data-testid="recipient-row"><td>{recipient.label}</td><td>{providerLabel(recipient.provider_key)}</td><td><bdi class="num">{recipient.recipient_masked}</bdi></td><td>{msg(recipient.locale === 'ar' ? 'messagingSettings.arabic' : 'messagingSettings.english')}</td><td><span class="badge" class:ok={recipient.enabled}>{msg(recipient.enabled ? 'messagingSettings.on' : 'messagingSettings.off')}</span></td><td><div class="row-actions"><button type="button" class="btn-secondary btn-sm" disabled={busy} onclick={() => editRecipient(recipient)}>{msg('messagingSettings.edit')}</button><button type="button" class="btn-secondary btn-sm" disabled={busy} onclick={() => run(() => settingsApi.setRecipient(recipient.id, !recipient.enabled), msg('messagingSettings.recipientUpdated'))}>{msg(recipient.enabled ? 'messagingSettings.turnOff' : 'messagingSettings.turnOn')}</button><button type="button" class="btn-secondary btn-sm danger" disabled={busy} onclick={() => { modalError = ''; removing = { kind: 'recipient', id: recipient.id, name: recipient.label }; }}>{msg('messagingSettings.delete')}</button></div></td></tr>
				{/each}
				</tbody></table></div>
			{/if}
		</section>

		<section class="card" aria-labelledby="schedule-editor-title" bind:this={schedulePanel} data-testid="schedule-editor-panel">
			<header><div><h3 id="schedule-editor-title">{msg(editingSchedule ? 'messagingSettings.editSchedule' : 'messagingSettings.newSchedule')}</h3><p class="sub">{msg('messagingSettings.schedulePanelHelp')}</p></div></header>
			{#key `${editingSchedule?.id ?? 'new'}:${editingSchedule?.revision ?? 0}:${formVersion}`}<MessagingScheduleForm existing={editingSchedule} recipients={data.recipients} {busy} onsave={saveSchedule} oncancel={() => editingSchedule = null} />{/key}
		</section>

		<section class="card" aria-labelledby="schedules-title" data-testid="schedules-panel">
			<header><div><h3 id="schedules-title">{msg('messagingSettings.configuredSchedules')}</h3><p class="sub">{msg('messagingSettings.scheduleTableHelp')}</p></div></header>
			{#if data.schedules.length === 0}<p class="empty">{msg('messagingSettings.noSchedules')}</p>{:else}
				<div class="table-wrap"><table class="data"><thead><tr><th>{msg('messagingSettings.name')}</th><th>{msg('messagingSettings.report')}</th><th>{msg('messagingSettings.time')}</th><th>{msg('messagingSettings.nextSend')}</th><th>{msg('messagingSettings.recipients')}</th><th>{msg('messagingSettings.status')}</th><th>{msg('messagingSettings.actions')}</th></tr></thead><tbody>
				{#each data.schedules as schedule (schedule.id)}
					<tr data-testid="schedule-row"><td>{schedule.name}</td><td>{msg(schedule.kind === 'daily' ? 'messagingSettings.daily' : 'messagingSettings.every10Days')}</td><td><bdi class="num">{schedule.local_time}</bdi></td><td>{schedule.enabled ? absoluteTime(schedule.next_run_at, lang()) : '—'}</td><td>{schedule.recipient_ids.map((id) => data?.recipients.find((r) => r.id === id)?.label ?? msg('messagingSettings.removedRecipient')).join(msg('messagingSettings.text'))}</td><td><span class="badge" class:ok={schedule.enabled}>{msg(schedule.enabled ? 'messagingSettings.on' : 'messagingSettings.off')}</span></td><td><div class="row-actions"><button type="button" class="btn-secondary btn-sm" disabled={busy} onclick={() => sendNow(schedule.id)}>{msg('messagingSettings.sendNow')}</button><button type="button" class="btn-secondary btn-sm" disabled={busy} onclick={() => editSchedule(schedule)}>{msg('messagingSettings.edit')}</button><button type="button" class="btn-secondary btn-sm" disabled={busy} onclick={() => run(() => settingsApi.setSchedule(schedule.id, !schedule.enabled), msg('messagingSettings.scheduleUpdated'))}>{msg(schedule.enabled ? 'messagingSettings.pause' : 'messagingSettings.resume')}</button><button type="button" class="btn-secondary btn-sm danger" disabled={busy} onclick={() => { modalError = ''; removing = { kind: 'schedule', id: schedule.id, name: schedule.name }; }}>{msg('messagingSettings.delete')}</button></div></td></tr>
				{/each}
				</tbody></table></div>
			{/if}
		</section>

		<section class="card" aria-labelledby="recent-sends-title" data-testid="recent-sends-panel">
			<header><div><h3 id="recent-sends-title">{msg('messagingSettings.recentSends')}</h3><p class="sub" data-testid="report-queue-explanation">{msg('messagingSettings.deliveryHelp')}</p></div><button type="button" class="btn-secondary" disabled={historyBusy || loading} onclick={() => historyPage('refresh')}>{msg('messagingSettings.refresh')}</button></header>
			{#if historyStale}<p class="warn" role="status">{msg('messagingSettings.deliveryStale')}</p>{/if}
			{#if rows.length === 0}<p class="empty">{msg('messagingSettings.noSends')}</p>{:else}
				<div class="table-wrap" aria-busy={historyBusy}><table class="data"><thead><tr><th>{msg('messagingSettings.time')}</th><th>{msg('messagingSettings.schedule')}</th><th>{msg('messagingSettings.kind')}</th><th>{msg('messagingSettings.status')}</th><th>{msg('messagingSettings.messageDelivery')}</th><th>{msg('messagingSettings.attempts')}</th><th>{msg('messagingSettings.error')}</th></tr></thead><tbody>
				{#each rows as run (run.id)}<tr data-testid="report-run-row"><td>{absoluteTime(run.scheduled_for, lang())}</td><td>{run.schedule_name || data.schedules.find((s) => s.id === run.schedule_id)?.name || msg('messagingSettings.removedSchedule')}</td><td>{msg(run.kind === 'manual' ? 'messagingSettings.manual' : 'messagingSettings.scheduled')}</td><td><span class="badge" class:ok={sendSucceeded(runSendState(run))} class:bad={sendFailed(runSendState(run))} class:uncertain={runSendState(run) === 'uncertain'}>{msg(`messagingSettings.delivery.${runSendState(run)}`)}</span></td><td><ul class="delivery-list">
						{#each run.deliveries ?? [] as delivery (delivery.id)}
							<li><div><strong>{delivery.provider_kind === 'whatsapp' ? 'WhatsApp' : delivery.provider_kind === 'telegram' ? 'Telegram' : delivery.provider_key}</strong> · {delivery.recipient_label} <bdi>{delivery.recipient_masked}</bdi></div><span class="badge" class:ok={sendSucceeded(deliveryState(delivery))} class:bad={sendFailed(deliveryState(delivery))} class:uncertain={deliveryState(delivery) === 'uncertain'}>{msg(`messagingSettings.delivery.${deliveryState(delivery)}`)}</span>{#if delivery.last_error_code}<bdi class="delivery-error mono">{delivery.last_error_code}</bdi>{/if}</li>
						{:else}<li class="sub">{msg('messagingSettings.delivery.unknown')}</li>{/each}
					</ul></td><td><bdi class="num">{run.attempt_count}</bdi></td><td class="mono">{run.last_error_code || '—'}</td></tr>{/each}
				</tbody></table></div>
			{/if}
			<nav class="pagination" aria-label={msg('messagingSettings.sendHistoryPages')}><button type="button" class="btn-secondary" disabled={historyBusy || cursors.length === 1} onclick={() => historyPage('previous')}>{msg('messagingSettings.previous')}</button><span aria-live="polite">{msg('messagingSettings.page')} <bdi>{cursors.length}</bdi> · {msg(remainingRuns === 0 ? 'messagingSettings.lastPage' : remainingRuns <= PAGE_SIZE ? 'messagingSettings.pageRemaining' : 'messagingSettings.pagesRemaining', { count: Math.ceil(remainingRuns / PAGE_SIZE) })}</span><button type="button" class="btn-secondary" disabled={historyBusy || !nextCursor} onclick={() => historyPage('next')}>{msg('messagingSettings.next')}</button></nav>
		</section>
	{/if}
</div>

{#if selectedProvider}<MessagingProviderModal provider={selectedProvider} {busy} error={modalError} onclose={() => { if (!busy) providerKey = ''; }} onsave={saveTemplate} />{/if}
{#if recipientOpen && data}<MessagingRecipientModal providers={data.providers} existing={recipientEditing} {busy} error={modalError} onclose={() => { if (!busy) recipientOpen = false; }} onsave={saveRecipient} />{/if}
{#if removing}
	<Modal title={msg(removing.kind === 'recipient' ? 'messagingSettings.deleteRecipient' : 'messagingSettings.deleteSchedule')} closeLabel={msg('messagingSettings.close')} onclose={() => { if (!busy) removing = null; }} testid="delete-configuration-modal">
		<p><strong>{removing.name}</strong></p><p class="sub">{msg(removing.kind === 'recipient' ? 'messagingSettings.deleteRecipientHelp' : 'messagingSettings.deleteScheduleHelp')}</p>
		{#if modalError}<p class="notice err" role="alert">{modalError}</p>{/if}
		<div class="row-actions"><button type="button" class="btn-secondary" disabled={busy} onclick={() => removing = null}>{msg('messagingSettings.cancel')}</button><button type="button" class="btn-primary" disabled={busy} onclick={confirmDelete}>{msg('messagingSettings.delete')}</button></div>
	</Modal>
{/if}

<style>
	.messaging { display: grid; gap: 20px; }
	.card { display: grid; gap: 16px; min-width: 0; }
	header { display: flex; justify-content: space-between; align-items: start; gap: 16px; }
	h3 { margin: 0; font-size: 1rem; }
	.sub { margin: 6px 0 0; max-width: 80ch; line-height: 1.6; font-size: .86rem; color: var(--text-muted); }
	.setup-panel { display: grid; gap: 12px; }
	.recipient-header { flex-wrap: wrap; }
	.recipient-header > div:first-child { flex: 1 1 220px; }
	.recipient-actions { flex: 0 0 auto; max-width: 100%; display: flex; flex-wrap: wrap; align-items: center; justify-content: flex-end; gap: 8px; }
	.channel { display: inline-flex; align-items: center; justify-content: center; gap: 8px; padding: 10px 14px; border: 1px solid transparent; border-radius: var(--radius-control); font: inherit; font-size: .86rem; font-weight: 700; cursor: pointer; }
	.whatsapp { background: #075e54; color: #fff; }
	.telegram { background: #007ab8; color: #fff; }
	.channel :global(svg) { flex: none; color: white; }
	.channel:hover:not(:disabled) { filter: brightness(.94); }
	.channel:focus-visible { outline: 3px solid var(--primary); outline-offset: 3px; }
	.channel:disabled { opacity: .5; cursor: default; }
	.warn { margin: 0; padding: 12px; border-inline-start: 4px solid #d97706; border-radius: 6px; background: #fff7ed; color: #7c2d12; font-size: .85rem; }
	.notice { padding: 12px 14px; border-radius: var(--radius-card); font-size: .88rem; }
	.notice.ok { border: 1px solid #bbf7d0; background: #f0fdf4; color: var(--success); }
	.notice.err { border: 1px solid #fecaca; background: #fef2f2; color: var(--danger); }
	.delivery-list { list-style: none; margin: 0; padding: 0; display: grid; gap: 10px; min-width: 230px; }
	.delivery-list li { display: grid; gap: 4px; justify-items: start; }
	.delivery-list strong { font-weight: 600; }
	.delivery-error { color: var(--danger); }
	.badge.uncertain { background: #fff7ed; color: #9a3412; border-color: #fed7aa; }
	.table-wrap { overflow-x: auto; }
	.table-wrap table { min-width: 680px; }
	.row-actions { display: flex; flex-wrap: wrap; gap: 6px; justify-content: flex-end; }
	.danger { color: var(--danger); }
	.mono { font-family: ui-monospace,Consolas,monospace; font-size: .78rem; overflow-wrap: anywhere; }
	.empty { margin: 0; padding: 20px; text-align: center; color: var(--text-muted); border: 1px dashed var(--border); border-radius: 8px; }
	.pagination { display: flex; justify-content: flex-end; gap: 14px; align-items: center; }
	@media(max-width: 600px) { header { flex-direction: column; } .recipient-actions { justify-content: flex-start; } .pagination { justify-content: space-between; } }
</style>
