<script lang="ts">
	import { ApiError } from '../lib/http.js';
	import { settingsApi, type Messaging, type MessagingProvider, type MessagingTemplate } from '../lib/settingsApi.js';
	import { absoluteTime } from '../lib/time.js';
	import { lang, msg, t } from '../lib/i18n.svelte.js';
	import SingleSelect from './ui/SingleSelect.svelte';
	import MultiSelect from './ui/MultiSelect.svelte';
	import TextField from './ui/TextField.svelte';

	// Sending the business report over WhatsApp and Telegram: which template
	// carries it, who receives it and when. Provider tokens stay on the
	// server; this page only lists the providers enabled there.
	let data = $state<Messaging | null>(null);
	let loading = $state(true);
	let busy = $state(false);
	let notice = $state<{ kind: 'ok' | 'err'; text: string } | null>(null);

	let recipient = $state({ label: '', provider_key: '', recipient: '', locale: 'ar' });
	let schedule = $state({ name: '', kind: 'daily', local_time: '21:00', anchor_local_date: '', recipient_ids: [] as string[] });
	let templateDrafts = $state<Record<string, { external_name: string; language: string }>>({});

	const TEMPLATES = [
		{ key: 'daily_business_report_v1', ar: 'التقرير اليومي', en: 'Daily report' },
		{ key: 'ten_day_business_report_v1', ar: 'تقرير العشرة أيام', en: '10-day report' }
	] as const;
	const LOCALES = [
		{ key: 'ar', ar: 'العربية', en: 'Arabic' },
		{ key: 'en', ar: 'الإنجليزية', en: 'English' }
	] as const;

	function errorText(err: unknown): string {
		if (err instanceof ApiError) {
			if (err.status === 401) return msg('authApi.sessionExpiredPleaseSignInAgain');
			if (err.detail === 'INVALID_RECIPIENT')
				return msg('messagingSettings.invalidAddressWhatsappNeedsAnInternational');
			if (err.detail === 'REPORT_NO_RECIPIENTS') return msg('messagingSettings.chooseAtLeastOneRecipient');
			if (err.code === 'CONFLICT') return msg('messagingSettings.aScheduleWithThisNameAlready');
			if (err.code === 'FORBIDDEN') return msg('messagingSettings.youDoNotHavePermissionTo');
			if (err.code === 'INVALID_INPUT' && err.detail) return err.detail;
		}
		return msg('messagingSettings.couldNotSaveTryAgain');
	}

	function key(provider: string, template: string, locale: string) {
		return `${provider}|${template}|${locale}`;
	}

	async function load() {
		loading = true;
		try {
			data = await settingsApi.messaging();
			if (!recipient.provider_key && data.providers[0]) recipient.provider_key = data.providers[0].key;
			const drafts: typeof templateDrafts = {};
			for (const p of data.providers)
				for (const tpl of TEMPLATES)
					for (const loc of LOCALES) {
						const saved = p.templates.find((m) => m.template_key === tpl.key && m.locale === loc.key);
						drafts[key(p.key, tpl.key, loc.key)] = { external_name: saved?.external_name ?? '', language: saved?.language ?? loc.key };
					}
			templateDrafts = drafts;
		} catch (err) {
			notice = { kind: 'err', text: errorText(err) };
		} finally {
			loading = false;
		}
	}
	$effect(() => {
		void load();
	});

	async function run(action: () => Promise<unknown>, ok: string) {
		if (busy) return;
		busy = true;
		notice = null;
		try {
			await action();
			notice = { kind: 'ok', text: ok };
			await load();
		} catch (err) {
			notice = { kind: 'err', text: errorText(err) };
		} finally {
			busy = false;
		}
	}

	function savedTemplate(p: MessagingProvider, template: string, locale: string): MessagingTemplate | undefined {
		return p.templates.find((m) => m.template_key === template && m.locale === locale);
	}

	function saveTemplate(p: MessagingProvider, template: MessagingTemplate['template_key'], locale: 'ar' | 'en') {
		const d = templateDrafts[key(p.key, template, locale)];
		// Telegram renders the composed text itself; WhatsApp sends an
		// approved template whose single parameter carries the report.
		const external = p.kind === 'telegram' ? 'telegram_text_v1' : d.external_name.trim();
		void run(
			() => settingsApi.setTemplate({ provider_key: p.key, template_key: template, locale, external_name: external, language: d.language.trim() || locale, params: ['report_body'], enabled: true }),
			msg('messagingSettings.templateSaved')
		);
	}

	function addRecipient(e: Event) {
		e.preventDefault();
		void run(async () => {
			await settingsApi.addRecipient({ ...recipient, label: recipient.label.trim(), recipient: recipient.recipient.trim() });
			recipient = { ...recipient, label: '', recipient: '' };
		}, msg('messagingSettings.recipientAdded'));
	}

	function addSchedule(e: Event) {
		e.preventDefault();
		void run(async () => {
			await settingsApi.addSchedule({ ...schedule, name: schedule.name.trim(), anchor_local_date: schedule.kind === 'ten_day' ? schedule.anchor_local_date : '' });
			schedule = { ...schedule, name: '', recipient_ids: [] };
		}, msg('messagingSettings.scheduleCreated'));
	}

	function sendNow(id: string) {
		const requestKey = typeof crypto !== 'undefined' && 'randomUUID' in crypto ? crypto.randomUUID() : `${Date.now()}-${Math.random()}`;
		void run(
			() => settingsApi.runSchedule(id, requestKey),
			data?.scheduler_enabled
				? msg('messagingSettings.theReportWillBeSentWithin')
				: msg('messagingSettings.queuedButTheSchedulerIsOff')
		);
	}

	function providerLabel(k: string) {
		const p = data?.providers.find((x) => x.key === k);
		return p ? `${p.kind === 'whatsapp' ? 'WhatsApp' : 'Telegram'} (${p.key})` : k;
	}
	function statusLabel(s: string) {
		const map: Record<string, [string, string]> = {
			pending: ['قيد الانتظار', 'Pending'],
			retry: ['إعادة المحاولة', 'Retrying'],
			completed: ['أُرسل', 'Sent'],
			blocked: ['متوقف', 'Blocked']
		};
		return t(...(map[s] ?? [s, s]));
	}
	let recipientOptions = $derived((data?.recipients ?? []).filter((r) => r.enabled).map((r) => ({ value: r.id, label: `${r.label} · ${providerLabel(r.provider_key)} ${r.recipient_masked}` })));
</script>

<section class="card messaging" aria-labelledby="messaging-title">
	<h3 id="messaging-title">{msg('messagingSettings.sendingReportsOverWhatsappAndTelegram')}</h3>
	<p class="sub">{msg('messagingSettings.aDailyOr10DaySummary')}</p>
	{#if notice}<div class="notice {notice.kind}" role={notice.kind === 'err' ? 'alert' : 'status'}>{notice.text}</div>{/if}

	{#if loading && !data}
		<p class="muted" role="status">{msg('messagingSettings.loading')}</p>
	{:else if data}
		{#if !data.scheduler_enabled}
			<p class="warn" data-testid="scheduler-off">
				{msg('messagingSettings.theSchedulerIsOffOnThe')}
			</p>
		{/if}
		{#if data.providers.length === 0}
			<p class="warn" data-testid="no-providers">
				{msg('messagingSettings.noMessagingProviderIsEnabledWhatsapp')}
			</p>
		{:else}
			<h4>{msg('messagingSettings.templates')}</h4>
			<p class="sub">
				{msg('messagingSettings.whatsappSendsAMetaApprovedTemplate')}
			</p>
			{#each data.providers as p (p.key)}
				<div class="provider" data-testid="provider-{p.kind}">
					<p class="provider-name">{providerLabel(p.key)}</p>
					<div class="template-grid">
						{#each TEMPLATES as tpl}
							{#each LOCALES as loc}
								{@const saved = savedTemplate(p, tpl.key, loc.key)}
								{@const d = templateDrafts[key(p.key, tpl.key, loc.key)]}
								<div class="template">
									<p class="template-title">
										{t(tpl.ar, tpl.en)} · {t(loc.ar, loc.en)}
										<span class="badge" class:ok={!!saved}>{saved ? msg('messagingSettings.set') : msg('messagingSettings.notSet')}</span>
									</p>
									{#if d}
										{#if p.kind === 'whatsapp'}
											<TextField id={`tpl-${p.key}-${tpl.key}-${loc.key}`} label={msg('messagingSettings.approvedTemplateName')} dir="ltr" bind:value={d.external_name} />
											<TextField id={`tpl-lang-${p.key}-${tpl.key}-${loc.key}`} label={msg('messagingSettings.languageCode')} dir="ltr" bind:value={d.language} />
										{/if}
										<button type="button" class="btn-secondary btn-sm" disabled={busy || (p.kind === 'whatsapp' && !d.external_name.trim())} onclick={() => saveTemplate(p, tpl.key, loc.key)}>
											{p.kind === 'telegram' ? msg('messagingSettings.useBuiltInText') : msg('messagingSettings.saveTemplate')}
										</button>
									{/if}
								</div>
							{/each}
						{/each}
					</div>
				</div>
			{/each}

			<h4>{msg('messagingSettings.recipients')}</h4>
			<form class="inline-form" onsubmit={addRecipient}>
				<TextField id="recipient-label" label={msg('messagingSettings.name')} maxlength={64} required bind:value={recipient.label} />
				<SingleSelect
					id="recipient-provider"
					label={msg('messagingSettings.provider')}
					value={recipient.provider_key}
					options={data.providers.map((p) => ({ value: p.key, label: providerLabel(p.key) }))}
					onchange={(v) => (recipient.provider_key = v)}
				/>
				<TextField
					id="recipient-address"
					label={data.providers.find((p) => p.key === recipient.provider_key)?.kind === 'telegram' ? msg('messagingSettings.chatIdOrUsername') : msg('messagingSettings.whatsappNumberInternational')}
					dir="ltr"
					required
					bind:value={recipient.recipient}
				/>
				<SingleSelect
					id="recipient-locale"
					label={msg('messagingSettings.reportLanguage')}
					value={recipient.locale}
					options={LOCALES.map((l) => ({ value: l.key, label: t(l.ar, l.en) }))}
					onchange={(v) => (recipient.locale = v)}
				/>
				<button type="submit" class="btn-primary" disabled={busy}>{msg('messagingSettings.add')}</button>
			</form>
			{#if data.recipients.length > 0}
				<div class="table-wrap">
					<table class="data">
						<thead><tr><th>{msg('messagingSettings.name')}</th><th>{msg('messagingSettings.provider')}</th><th>{msg('messagingSettings.address')}</th><th>{msg('messagingSettings.language')}</th><th>{msg('messagingSettings.status')}</th><th></th></tr></thead>
						<tbody>
							{#each data.recipients as r (r.id)}
								<tr data-testid="recipient-row">
									<td>{r.label}</td>
									<td>{providerLabel(r.provider_key)}</td>
									<td><bdi class="num">{r.recipient_masked}</bdi></td>
									<td>{r.locale === 'ar' ? msg('messagingSettings.arabic') : msg('messagingSettings.english')}</td>
									<td><span class="badge" class:ok={r.enabled}>{r.enabled ? msg('messagingSettings.on') : msg('messagingSettings.off')}</span></td>
									<td class="row-actions">
										<button type="button" class="btn-secondary btn-sm" disabled={busy} onclick={() => run(() => settingsApi.setRecipient(r.id, !r.enabled), msg('messagingSettings.recipientUpdated'))}>
											{r.enabled ? msg('messagingSettings.turnOff') : msg('messagingSettings.turnOn')}
										</button>
									</td>
								</tr>
							{/each}
						</tbody>
					</table>
				</div>
			{/if}

			<h4>{msg('messagingSettings.schedules')}</h4>
			<form class="inline-form" onsubmit={addSchedule}>
				<TextField id="schedule-name" label={msg('messagingSettings.name')} maxlength={64} required bind:value={schedule.name} />
				<SingleSelect
					id="schedule-kind"
					label={msg('messagingSettings.report')}
					value={schedule.kind}
					options={[
						{ value: 'daily', label: msg('messagingSettings.dailyThePreviousDay') },
						{ value: 'ten_day', label: msg('messagingSettings.every10Days') }
					]}
					onchange={(v) => (schedule.kind = v)}
				/>
				<TextField id="schedule-time" label={msg('messagingSettings.timeCairo')} type="time" required bind:value={schedule.local_time} />
				{#if schedule.kind === 'ten_day'}
					<TextField id="schedule-anchor" label={msg('messagingSettings.firstDayOfTheCycle')} type="date" required bind:value={schedule.anchor_local_date} />
				{/if}
				<MultiSelect
					id="schedule-recipients"
					label={msg('messagingSettings.recipients')}
					emptyLabel={msg('messagingSettings.chooseRecipients')}
					options={recipientOptions}
					selected={schedule.recipient_ids}
					onchange={(values) => (schedule.recipient_ids = values)}
				/>
				<button type="submit" class="btn-primary" disabled={busy || schedule.recipient_ids.length === 0}>{msg('messagingSettings.create')}</button>
			</form>
			{#if data.schedules.length > 0}
				<div class="table-wrap">
					<table class="data">
						<thead><tr><th>{msg('messagingSettings.name')}</th><th>{msg('messagingSettings.report')}</th><th>{msg('messagingSettings.time')}</th><th>{msg('messagingSettings.nextSend')}</th><th>{msg('messagingSettings.recipients')}</th><th>{msg('messagingSettings.status')}</th><th></th></tr></thead>
						<tbody>
							{#each data.schedules as s (s.id)}
								<tr data-testid="schedule-row">
									<td>{s.name}</td>
									<td>{s.kind === 'daily' ? msg('messagingSettings.daily') : msg('messagingSettings.every10Days')}</td>
									<td><bdi class="num">{s.local_time}</bdi></td>
									<td>{s.enabled ? absoluteTime(s.next_run_at, lang()) : '—'}</td>
									<td>{s.recipient_ids.map((id) => data?.recipients.find((r) => r.id === id)?.label ?? '…').join(msg('messagingSettings.text'))}</td>
									<td><span class="badge" class:ok={s.enabled}>{s.enabled ? msg('messagingSettings.on') : msg('messagingSettings.off')}</span></td>
									<td class="row-actions">
										<button type="button" class="btn-secondary btn-sm" disabled={busy} onclick={() => sendNow(s.id)}>{msg('messagingSettings.sendNow')}</button>
										<button type="button" class="btn-secondary btn-sm" disabled={busy} onclick={() => run(() => settingsApi.setSchedule(s.id, !s.enabled), msg('messagingSettings.scheduleUpdated'))}>
											{s.enabled ? msg('messagingSettings.pause') : msg('messagingSettings.resume')}
										</button>
									</td>
								</tr>
							{/each}
						</tbody>
					</table>
				</div>
			{/if}

			{#if data.runs.length > 0}
				<h4>{msg('messagingSettings.recentSends')}</h4>
				<div class="table-wrap">
					<table class="data">
						<thead><tr><th>{msg('messagingSettings.time')}</th><th>{msg('messagingSettings.schedule')}</th><th>{msg('messagingSettings.kind')}</th><th>{msg('messagingSettings.status')}</th><th>{msg('messagingSettings.attempts')}</th><th>{msg('messagingSettings.error')}</th></tr></thead>
						<tbody>
							{#each data.runs as r (r.id)}
								<tr>
									<td>{absoluteTime(r.scheduled_for, lang())}</td>
									<td>{data.schedules.find((s) => s.id === r.schedule_id)?.name ?? '…'}</td>
									<td>{r.kind === 'manual' ? msg('messagingSettings.manual') : msg('messagingSettings.scheduled')}</td>
									<td><span class="badge" class:ok={r.status === 'completed'} class:bad={r.status === 'blocked'}>{statusLabel(r.status)}</span></td>
									<td><bdi class="num">{r.attempt_count}</bdi></td>
									<td class="mono">{r.last_error_code || '—'}</td>
								</tr>
							{/each}
						</tbody>
					</table>
				</div>
			{/if}
		{/if}
	{/if}
</section>

<style>
	.messaging {
		display: flex;
		flex-direction: column;
		gap: 10px;
	}
	h3 {
		margin: 0;
		font-size: 1rem;
	}
	h4 {
		margin: 10px 0 0;
		font-size: 0.92rem;
	}
	.sub {
		max-width: 75ch;
		line-height: 1.5;
	}
	.warn {
		margin: 0;
		padding: 10px 12px;
		border-inline-start: 4px solid #d97706;
		border-radius: 6px;
		background: #fff7ed;
		color: #7c2d12;
		font-size: 0.85rem;
	}
	.notice {
		padding: 10px 14px;
		border-radius: var(--radius-card);
		font-size: 0.88rem;
	}
	.notice.ok {
		border: 1px solid #bbf7d0;
		background: #f0fdf4;
		color: var(--success);
	}
	.notice.err {
		border: 1px solid #fecaca;
		background: #fef2f2;
		color: var(--danger);
	}
	.provider {
		padding: 12px;
		border: 1px solid var(--border);
		border-radius: 8px;
	}
	.provider-name {
		margin: 0 0 8px;
		font-weight: 700;
	}
	.template-grid {
		display: grid;
		grid-template-columns: repeat(4, minmax(0, 1fr));
		gap: 12px;
	}
	.template {
		display: grid;
		gap: 8px;
		align-content: start;
	}
	.template-title {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
		align-items: center;
		margin: 0;
		font-size: 0.82rem;
		font-weight: 700;
	}
	.inline-form {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(170px, 1fr));
		gap: 12px;
		align-items: end;
	}
	.table-wrap {
		overflow-x: auto;
	}
	.row-actions {
		display: flex;
		gap: 6px;
		justify-content: flex-end;
		white-space: nowrap;
	}
	.mono {
		font-family: ui-monospace, Consolas, monospace;
		font-size: 0.78rem;
	}
	@media (max-width: 1100px) {
		.template-grid {
			grid-template-columns: repeat(2, minmax(0, 1fr));
		}
	}
</style>
