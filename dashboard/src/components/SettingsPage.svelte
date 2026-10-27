<script lang="ts">
	import { ApiError } from '../lib/http.js';
	import { settingsApi, type ExchangeRate } from '../lib/settingsApi.js';
	import type { BusinessSettings } from '../lib/reportsApi.js';
	import { absoluteTime } from '../lib/time.js';
	import { lang, t } from '../lib/i18n.svelte.js';
	import Switch from './ui/Switch.svelte';
	import TextField from './ui/TextField.svelte';
	import MessagingSettings from './MessagingSettings.svelte';

	// Business-wide settings for every Store. Everyone who reads reports sees
	// them; only all-Stores users with settings.manage change them.
	let { canManage }: { canManage: boolean } = $props();

	let business = $state<BusinessSettings | null>(null);
	let draft = $state({ tax_enabled: false, discount_enabled: false, returns_enabled: false });
	let rates = $state<ExchangeRate[]>([]);
	let loading = $state(true);
	let busy = $state(false);
	let notice = $state<{ kind: 'ok' | 'err'; text: string } | null>(null);
	let rateInput = $state('');
	let rateAt = $state('');
	let rateNote = $state('');
	let rateError = $state('');
	let confirmDelete = $state<string | null>(null);

	let dirty = $derived(
		business !== null &&
			(draft.tax_enabled !== business.tax_enabled || draft.discount_enabled !== business.discount_enabled || draft.returns_enabled !== business.returns_enabled)
	);
	let current = $derived(rates.find((r) => Date.parse(r.effective_at) <= Date.now()));

	function errorText(err: unknown): string {
		if (err instanceof ApiError) {
			if (err.detail === 'SETTINGS_CHANGED') return t('غيّر شخص آخر الإعدادات للتو. أُعيد تحميلها؛ راجعها واحفظ مرة أخرى.', 'Someone else just changed these settings. They were reloaded; review and save again.');
			if (err.detail === 'RATE_TIME_TAKEN') return t('يوجد سعر يبدأ في نفس الوقت. اختر وقتاً آخر.', 'A rate already starts at that time. Choose another time.');
			if (err.code === 'FORBIDDEN') return t('ليست لديك صلاحية تغيير الإعدادات.', 'You do not have permission to change settings.');
			if (err.code === 'INVALID_INPUT' && err.detail) return err.detail;
		}
		return t('تعذر الحفظ. حاول مرة أخرى.', 'Could not save. Try again.');
	}

	function localNow(): string {
		const d = new Date();
		d.setMinutes(d.getMinutes() - d.getTimezoneOffset(), 0, 0);
		return d.toISOString().slice(0, 16);
	}

	async function load() {
		loading = true;
		try {
			const res = await settingsApi.get();
			business = res.business;
			draft = { tax_enabled: res.business.tax_enabled, discount_enabled: res.business.discount_enabled, returns_enabled: res.business.returns_enabled };
			rates = res.exchange_rates;
			if (!rateAt) rateAt = localNow();
		} catch (err) {
			notice = { kind: 'err', text: errorText(err) };
		} finally {
			loading = false;
		}
	}
	$effect(() => {
		void load();
	});

	async function saveToggles() {
		if (!business || busy) return;
		busy = true;
		notice = null;
		try {
			business = await settingsApi.saveBusiness({ ...draft, revision: business.revision });
			notice = { kind: 'ok', text: t('حُفظت الإعدادات. تطبق التقارير الإعدادات الجديدة فوراً.', 'Settings saved. Reports use them right away.') };
		} catch (err) {
			notice = { kind: 'err', text: errorText(err) };
			if (err instanceof ApiError && err.detail === 'SETTINGS_CHANGED') await load();
		} finally {
			busy = false;
		}
	}

	async function addRate(e: Event) {
		e.preventDefault();
		if (busy) return;
		rateError = '';
		if (!/^\d+(\.\d{1,6})?$/.test(rateInput.trim()) || Number(rateInput) <= 0) {
			rateError = t('أدخل سعراً موجباً بست منازل عشرية كحد أقصى، مثل 50.25.', 'Enter a positive rate with at most six decimals, such as 50.25.');
			return;
		}
		busy = true;
		notice = null;
		try {
			const added = await settingsApi.addRate({
				rate: rateInput.trim(),
				effective_at: rateAt ? new Date(rateAt).toISOString() : undefined,
				note: rateNote.trim() || undefined
			});
			rates = [added, ...rates].sort((a, b) => Date.parse(b.effective_at) - Date.parse(a.effective_at));
			rateInput = '';
			rateNote = '';
			rateAt = localNow();
			notice = { kind: 'ok', text: t('أُضيف سعر الصرف.', 'Exchange rate added.') };
		} catch (err) {
			rateError = errorText(err);
		} finally {
			busy = false;
		}
	}

	async function deleteRate(id: string) {
		if (busy) return;
		busy = true;
		notice = null;
		try {
			await settingsApi.deleteRate(id);
			rates = rates.filter((r) => r.id !== id);
			confirmDelete = null;
		} catch (err) {
			notice = { kind: 'err', text: errorText(err) };
		} finally {
			busy = false;
		}
	}
</script>

<section class="settings-page" aria-labelledby="settings-title">
	<header class="card settings-head">
		<h2 id="settings-title">{t('الإعدادات', 'Settings')}</h2>
		<p class="sub">{t('إعدادات تخص كل المتاجر: ما يظهر في التقارير، وأسعار الصرف، وإرسال التقارير.', 'Settings for every Store: what reports show, exchange rates and sending reports.')}</p>
	</header>
	{#if notice}<div class="notice {notice.kind}" role={notice.kind === 'err' ? 'alert' : 'status'}>{notice.text}</div>{/if}
	{#if !canManage}<p class="readonly muted">{t('للعرض فقط: يغيّر هذه الإعدادات المالك أو المسؤول الذي يصل إلى كل المتاجر.', 'View only: an owner or admin with access to every Store changes these settings.')}</p>{/if}

	{#if loading && !business}
		<div class="card muted" role="status">{t('جارٍ التحميل…', 'Loading…')}</div>
	{:else if business}
		<section class="card" aria-labelledby="toggles-title">
			<h3 id="toggles-title">{t('خصائص المبيعات في التقارير', 'Sales features in reports')}</h3>
			<p class="sub">{t('مثل إعدادات MoonLightRetail: الخاصية المعطلة تختفي من التقارير إلا إذا احتوت الفترة على مبالغ منها.', 'As in MoonLightRetail: a feature that is off disappears from reports unless the period still holds such amounts.')}</p>
			<Switch
				id="toggle-tax"
				label={t('الضريبة', 'Tax')}
				description={t('عند الإيقاف تُعرض المبيعات والمرتجعات دون الضريبة وتختفي صفوف الضريبة.', 'When off, sales and refunds are shown without tax and the tax rows disappear.')}
				checked={draft.tax_enabled}
				disabled={!canManage || busy}
				onchange={(v) => (draft.tax_enabled = v)}
			/>
			<Switch
				id="toggle-discount"
				label={t('خصومات البيع', 'Sale discounts')}
				description={t('يُظهر جدول طريقة حساب إجمالي البيع وخصوماته.', 'Shows the table explaining sale totals and their discounts.')}
				checked={draft.discount_enabled}
				disabled={!canManage || busy}
				onchange={(v) => (draft.discount_enabled = v)}
			/>
			<Switch
				id="toggle-returns"
				label={t('المرتجعات والإلغاء', 'Returns and voids')}
				description={t('يُظهر أعمدة المرتجعات والمبالغ المستردة وجدول تطابق الأرقام.', 'Shows the returns and refunds columns and the reconciliation tables.')}
				checked={draft.returns_enabled}
				disabled={!canManage || busy}
				onchange={(v) => (draft.returns_enabled = v)}
			/>
			{#if canManage}
				<div class="btn-row actions">
					<button type="button" class="btn-primary" disabled={!dirty || busy} onclick={saveToggles} data-testid="save-toggles">{t('حفظ', 'Save')}</button>
					{#if dirty}<button type="button" class="btn-secondary" disabled={busy} onclick={() => business && (draft = { tax_enabled: business.tax_enabled, discount_enabled: business.discount_enabled, returns_enabled: business.returns_enabled })}>{t('تراجع', 'Undo')}</button>{/if}
				</div>
			{/if}
		</section>

		<section class="card" aria-labelledby="rates-title">
			<h3 id="rates-title">{t('أسعار صرف الدولار', 'USD exchange rates')}</h3>
			<p class="sub">
				{t('عند عرض مبيعات الجنيه بالدولار تستخدم التقارير السعر المعمول به وقت كل بيع. مبيعات الدولار تحتفظ دائماً بالسعر المحفوظ معها في MoonLightRetail.', 'When EGP sales are shown in USD, reports use the rate in effect when each sale was made. USD sales always keep the rate saved with them in MoonLightRetail.')}
			</p>
			{#if current}
				<p class="current" data-testid="current-rate">{t('السعر الحالي', 'Current rate')}: <bdi class="num">1 USD = {current.rate} EGP</bdi></p>
			{/if}
			{#if canManage}
				<form class="rate-form" onsubmit={addRate}>
					<TextField id="rate-value" label={t('جنيه لكل دولار', 'EGP per USD')} inputmode="decimal" dir="ltr" placeholder="50.25" error={rateError} bind:value={rateInput} data-testid="rate-value" />
					<TextField id="rate-at" label={t('يبدأ من', 'Effective from')} type="datetime-local" bind:value={rateAt} />
					<TextField id="rate-note" label={t('ملاحظة (اختياري)', 'Note (optional)')} maxlength={120} bind:value={rateNote} />
					<button type="submit" class="btn-primary" disabled={busy}>{t('إضافة سعر', 'Add rate')}</button>
				</form>
			{/if}
			{#if rates.length === 0}
				<p class="muted">{t('لا توجد أسعار بعد. بدونها تُحوَّل مبيعات الجنيه بسعر أقرب بيع بالدولار في المتجر.', 'No rates yet. Without one, EGP sales convert at the rate of the Store’s nearest USD sale.')}</p>
			{:else}
				<div class="table-wrap">
					<table class="data">
						<thead><tr><th>{t('يبدأ من', 'Effective from')}</th><th>{t('السعر', 'Rate')}</th><th>{t('ملاحظة', 'Note')}</th>{#if canManage}<th></th>{/if}</tr></thead>
						<tbody>
							{#each rates as r (r.id)}
								<tr data-testid="rate-row">
									<td>{absoluteTime(r.effective_at, lang())}</td>
									<td><bdi class="num">{r.rate}</bdi></td>
									<td>{r.note || '—'}</td>
									{#if canManage}
										<td class="row-actions">
											{#if confirmDelete === r.id}
												<button type="button" class="btn-danger btn-sm" disabled={busy} onclick={() => deleteRate(r.id)}>{t('تأكيد الحذف', 'Confirm delete')}</button>
												<button type="button" class="btn-secondary btn-sm" onclick={() => (confirmDelete = null)}>{t('إلغاء', 'Cancel')}</button>
											{:else}
												<button type="button" class="btn-link" onclick={() => (confirmDelete = r.id)}>{t('حذف', 'Delete')}</button>
											{/if}
										</td>
									{/if}
								</tr>
							{/each}
						</tbody>
					</table>
				</div>
			{/if}
		</section>
	{/if}

	{#if canManage}
		<MessagingSettings />
	{/if}
</section>

<style>
	.settings-page {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}
	.settings-head h2,
	h3 {
		margin: 0;
	}
	h3 {
		font-size: 1rem;
	}
	.sub {
		max-width: 75ch;
		line-height: 1.5;
	}
	.actions {
		margin-top: 12px;
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
	.readonly {
		margin: 0;
	}
	.current {
		margin: 10px 0 0;
		font-weight: 700;
	}
	.rate-form {
		display: grid;
		grid-template-columns: minmax(0, 1fr) minmax(0, 1fr) minmax(0, 1.4fr) auto;
		gap: 12px;
		align-items: end;
		margin: 12px 0;
	}
	.table-wrap {
		overflow-x: auto;
	}
	.row-actions {
		display: flex;
		gap: 6px;
		justify-content: flex-end;
	}
	@media (max-width: 900px) {
		.rate-form {
			grid-template-columns: 1fr;
		}
	}
</style>
