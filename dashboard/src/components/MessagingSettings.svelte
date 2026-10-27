<script lang="ts">
	import { ApiError } from '../lib/http.js';
	import { settingsApi, type Messaging, type MessagingProvider, type MessagingTemplate } from '../lib/settingsApi.js';
	import { absoluteTime } from '../lib/time.js';
	import { lang, t } from '../lib/i18n.svelte.js';
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
			if (err.detail === 'INVALID_RECIPIENT')
				return t('رقم أو معرف غير صالح: واتساب يحتاج رقماً دولياً (مثل 201012345678)، وتيليجرام معرف محادثة رقمياً أو @اسم_مستخدم.', 'Invalid address: WhatsApp needs an international number (such as 201012345678); Telegram a numeric chat ID or @username.');
			if (err.detail === 'REPORT_NO_RECIPIENTS') return t('اختر مستلماً واحداً على الأقل.', 'Choose at least one recipient.');
			if (err.code === 'CONFLICT') return t('يوجد جدول بنفس الاسم.', 'A schedule with this name already exists.');
			if (err.code === 'FORBIDDEN') return t('ليست لديك صلاحية تغيير إرسال التقارير.', 'You do not have permission to change report delivery.');
			if (err.code === 'INVALID_INPUT' && err.detail) return err.detail;
		}
		return t('تعذر الحفظ. حاول مرة أخرى.', 'Could not save. Try again.');
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
			t('حُفظ القالب.', 'Template saved.')
		);
	}

	function addRecipient(e: Event) {
		e.preventDefault();
		void run(async () => {
			await settingsApi.addRecipient({ ...recipient, label: recipient.label.trim(), recipient: recipient.recipient.trim() });
			recipient = { ...recipient, label: '', recipient: '' };
		}, t('أُضيف المستلم.', 'Recipient added.'));
	}

	function addSchedule(e: Event) {
		e.preventDefault();
		void run(async () => {
			await settingsApi.addSchedule({ ...schedule, name: schedule.name.trim(), anchor_local_date: schedule.kind === 'ten_day' ? schedule.anchor_local_date : '' });
			schedule = { ...schedule, name: '', recipient_ids: [] };
		}, t('أُنشئ الجدول.', 'Schedule created.'));
	}

	function sendNow(id: string) {
		const requestKey = typeof crypto !== 'undefined' && 'randomUUID' in crypto ? crypto.randomUUID() : `${Date.now()}-${Math.random()}`;
		void run(
			() => settingsApi.runSchedule(id, requestKey),
			data?.scheduler_enabled
				? t('سيُرسل التقرير خلال دقيقة تقريباً.', 'The report will be sent within about a minute.')
				: t('سُجل الطلب، لكن المُجدول متوقف فلن يُرسل حتى يُفعّل.', 'Queued, but the scheduler is off, so nothing is sent until it is enabled.')
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
	<h3 id="messaging-title">{t('إرسال التقارير عبر واتساب وتيليجرام', 'Sending reports over WhatsApp and Telegram')}</h3>
	<p class="sub">{t('ملخص يومي أو كل عشرة أيام لكل المتاجر يُرسل في الوقت المحدد بتوقيت القاهرة.', 'A daily or 10-day summary for every Store, sent at the chosen Cairo time.')}</p>
	{#if notice}<div class="notice {notice.kind}" role={notice.kind === 'err' ? 'alert' : 'status'}>{notice.text}</div>{/if}

	{#if loading && !data}
		<p class="muted" role="status">{t('جارٍ التحميل…', 'Loading…')}</p>
	{:else if data}
		{#if !data.scheduler_enabled}
			<p class="warn" data-testid="scheduler-off">
				{t('المُجدول متوقف على الخادم، فلن تُرسل أي تقارير. فعّله بضبط BUSINESS_REPORTS_ENABLED=true ثم أعد تشغيل Cloud.', 'The scheduler is off on the server, so no report is sent. Turn it on with BUSINESS_REPORTS_ENABLED=true and restart Cloud.')}
			</p>
		{/if}
		{#if data.providers.length === 0}
			<p class="warn" data-testid="no-providers">
				{t('لا يوجد مزود رسائل مفعّل. تُضبط بيانات واتساب وتيليجرام (الرموز السرية) في إعدادات الخادم، انظر docs/operations/notifications-whatsapp.md و notifications-telegram.md.', 'No messaging provider is enabled. WhatsApp and Telegram credentials (secret tokens) are set in the server environment; see docs/operations/notifications-whatsapp.md and notifications-telegram.md.')}
			</p>
		{:else}
			<h4>{t('القوالب', 'Templates')}</h4>
			<p class="sub">
				{t('واتساب يرسل قالباً معتمداً من Meta بمعامل واحد يحمل نص التقرير. تيليجرام يرسل النص مباشرة.', 'WhatsApp sends a Meta-approved template with one parameter carrying the report text. Telegram sends the text directly.')}
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
										<span class="badge" class:ok={!!saved}>{saved ? t('مضبوط', 'Set') : t('غير مضبوط', 'Not set')}</span>
									</p>
									{#if d}
										{#if p.kind === 'whatsapp'}
											<TextField id={`tpl-${p.key}-${tpl.key}-${loc.key}`} label={t('اسم القالب المعتمد', 'Approved template name')} dir="ltr" bind:value={d.external_name} />
											<TextField id={`tpl-lang-${p.key}-${tpl.key}-${loc.key}`} label={t('رمز اللغة', 'Language code')} dir="ltr" bind:value={d.language} />
										{/if}
										<button type="button" class="btn-secondary btn-sm" disabled={busy || (p.kind === 'whatsapp' && !d.external_name.trim())} onclick={() => saveTemplate(p, tpl.key, loc.key)}>
											{p.kind === 'telegram' ? t('استخدام النص المدمج', 'Use built-in text') : t('حفظ القالب', 'Save template')}
										</button>
									{/if}
								</div>
							{/each}
						{/each}
					</div>
				</div>
			{/each}

			<h4>{t('المستلمون', 'Recipients')}</h4>
			<form class="inline-form" onsubmit={addRecipient}>
				<TextField id="recipient-label" label={t('الاسم', 'Name')} maxlength={64} required bind:value={recipient.label} />
				<SingleSelect
					id="recipient-provider"
					label={t('المزود', 'Provider')}
					value={recipient.provider_key}
					options={data.providers.map((p) => ({ value: p.key, label: providerLabel(p.key) }))}
					onchange={(v) => (recipient.provider_key = v)}
				/>
				<TextField
					id="recipient-address"
					label={data.providers.find((p) => p.key === recipient.provider_key)?.kind === 'telegram' ? t('معرف المحادثة أو @اسم', 'Chat ID or @username') : t('رقم واتساب الدولي', 'WhatsApp number (international)')}
					dir="ltr"
					required
					bind:value={recipient.recipient}
				/>
				<SingleSelect
					id="recipient-locale"
					label={t('لغة التقرير', 'Report language')}
					value={recipient.locale}
					options={LOCALES.map((l) => ({ value: l.key, label: t(l.ar, l.en) }))}
					onchange={(v) => (recipient.locale = v)}
				/>
				<button type="submit" class="btn-primary" disabled={busy}>{t('إضافة', 'Add')}</button>
			</form>
			{#if data.recipients.length > 0}
				<div class="table-wrap">
					<table class="data">
						<thead><tr><th>{t('الاسم', 'Name')}</th><th>{t('المزود', 'Provider')}</th><th>{t('العنوان', 'Address')}</th><th>{t('اللغة', 'Language')}</th><th>{t('الحالة', 'Status')}</th><th></th></tr></thead>
						<tbody>
							{#each data.recipients as r (r.id)}
								<tr data-testid="recipient-row">
									<td>{r.label}</td>
									<td>{providerLabel(r.provider_key)}</td>
									<td><bdi class="num">{r.recipient_masked}</bdi></td>
									<td>{r.locale === 'ar' ? t('العربية', 'Arabic') : t('الإنجليزية', 'English')}</td>
									<td><span class="badge" class:ok={r.enabled}>{r.enabled ? t('مفعّل', 'On') : t('معطّل', 'Off')}</span></td>
									<td class="row-actions">
										<button type="button" class="btn-secondary btn-sm" disabled={busy} onclick={() => run(() => settingsApi.setRecipient(r.id, !r.enabled), t('حُدّث المستلم.', 'Recipient updated.'))}>
											{r.enabled ? t('تعطيل', 'Turn off') : t('تفعيل', 'Turn on')}
										</button>
									</td>
								</tr>
							{/each}
						</tbody>
					</table>
				</div>
			{/if}

			<h4>{t('الجداول', 'Schedules')}</h4>
			<form class="inline-form" onsubmit={addSchedule}>
				<TextField id="schedule-name" label={t('الاسم', 'Name')} maxlength={64} required bind:value={schedule.name} />
				<SingleSelect
					id="schedule-kind"
					label={t('التقرير', 'Report')}
					value={schedule.kind}
					options={[
						{ value: 'daily', label: t('يومي (اليوم السابق)', 'Daily (the previous day)') },
						{ value: 'ten_day', label: t('كل عشرة أيام', 'Every 10 days') }
					]}
					onchange={(v) => (schedule.kind = v)}
				/>
				<TextField id="schedule-time" label={t('الوقت (القاهرة)', 'Time (Cairo)')} type="time" required bind:value={schedule.local_time} />
				{#if schedule.kind === 'ten_day'}
					<TextField id="schedule-anchor" label={t('أول يوم في الدورة', 'First day of the cycle')} type="date" required bind:value={schedule.anchor_local_date} />
				{/if}
				<MultiSelect
					id="schedule-recipients"
					label={t('المستلمون', 'Recipients')}
					emptyLabel={t('اختر المستلمين', 'Choose recipients')}
					options={recipientOptions}
					selected={schedule.recipient_ids}
					onchange={(values) => (schedule.recipient_ids = values)}
				/>
				<button type="submit" class="btn-primary" disabled={busy || schedule.recipient_ids.length === 0}>{t('إنشاء', 'Create')}</button>
			</form>
			{#if data.schedules.length > 0}
				<div class="table-wrap">
					<table class="data">
						<thead><tr><th>{t('الاسم', 'Name')}</th><th>{t('التقرير', 'Report')}</th><th>{t('الوقت', 'Time')}</th><th>{t('الإرسال القادم', 'Next send')}</th><th>{t('المستلمون', 'Recipients')}</th><th>{t('الحالة', 'Status')}</th><th></th></tr></thead>
						<tbody>
							{#each data.schedules as s (s.id)}
								<tr data-testid="schedule-row">
									<td>{s.name}</td>
									<td>{s.kind === 'daily' ? t('يومي', 'Daily') : t('كل عشرة أيام', 'Every 10 days')}</td>
									<td><bdi class="num">{s.local_time}</bdi></td>
									<td>{s.enabled ? absoluteTime(s.next_run_at, lang()) : '—'}</td>
									<td>{s.recipient_ids.map((id) => data?.recipients.find((r) => r.id === id)?.label ?? '…').join(t('، ', ', '))}</td>
									<td><span class="badge" class:ok={s.enabled}>{s.enabled ? t('مفعّل', 'On') : t('معطّل', 'Off')}</span></td>
									<td class="row-actions">
										<button type="button" class="btn-secondary btn-sm" disabled={busy} onclick={() => sendNow(s.id)}>{t('إرسال الآن', 'Send now')}</button>
										<button type="button" class="btn-secondary btn-sm" disabled={busy} onclick={() => run(() => settingsApi.setSchedule(s.id, !s.enabled), t('حُدّث الجدول.', 'Schedule updated.'))}>
											{s.enabled ? t('إيقاف', 'Pause') : t('استئناف', 'Resume')}
										</button>
									</td>
								</tr>
							{/each}
						</tbody>
					</table>
				</div>
			{/if}

			{#if data.runs.length > 0}
				<h4>{t('آخر الإرسالات', 'Recent sends')}</h4>
				<div class="table-wrap">
					<table class="data">
						<thead><tr><th>{t('الوقت', 'Time')}</th><th>{t('الجدول', 'Schedule')}</th><th>{t('النوع', 'Kind')}</th><th>{t('الحالة', 'Status')}</th><th>{t('المحاولات', 'Attempts')}</th><th>{t('الخطأ', 'Error')}</th></tr></thead>
						<tbody>
							{#each data.runs as r (r.id)}
								<tr>
									<td>{absoluteTime(r.scheduled_for, lang())}</td>
									<td>{data.schedules.find((s) => s.id === r.schedule_id)?.name ?? '…'}</td>
									<td>{r.kind === 'manual' ? t('يدوي', 'Manual') : t('مجدول', 'Scheduled')}</td>
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
