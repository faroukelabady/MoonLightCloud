<script lang="ts">
	import { ApiError } from '../lib/http.js';
	import { settingsApi, type ExchangeRate } from '../lib/settingsApi.js';
	import type { BusinessSettings } from '../lib/reportsApi.js';
	import { absoluteTime } from '../lib/time.js';
	import { lang, msg } from '../lib/i18n.svelte.js';
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
	let showRateForm = $state(false);

	let dirty = $derived(
		business !== null &&
			(draft.tax_enabled !== business.tax_enabled || draft.discount_enabled !== business.discount_enabled || draft.returns_enabled !== business.returns_enabled)
	);
	let current = $derived(rates.find((r) => Date.parse(r.effective_at) <= Date.now()));

	function errorText(err: unknown): string {
		if (err instanceof ApiError) {
			if (err.detail === 'SETTINGS_CHANGED') return msg('settingsPage.someoneElseJustChangedTheseSettings');
			if (err.detail === 'RATE_TIME_TAKEN') return msg('settingsPage.aRateAlreadyStartsAtThat');
			if (err.code === 'FORBIDDEN') return msg('settingsPage.youDoNotHavePermissionTo');
			if (err.code === 'INVALID_INPUT' && err.detail) return err.detail;
		}
		return msg('settingsPage.couldNotSaveTryAgain');
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
			notice = { kind: 'ok', text: msg('settingsPage.settingsSavedReportsUseThemRight') };
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
			rateError = msg('settingsPage.enterAPositiveRateWithAt');
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
			notice = { kind: 'ok', text: msg('settingsPage.exchangeRateAdded') };
			showRateForm = false;
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
		<h2 id="settings-title">{msg('settingsPage.settings')}</h2>
		<p class="sub">{msg('settingsPage.settingsForEveryStoreWhatReports')}</p>
	</header>
	{#if notice}<div class="notice {notice.kind}" role={notice.kind === 'err' ? 'alert' : 'status'}>{notice.text}</div>{/if}
	{#if !canManage}<p class="readonly muted">{msg('settingsPage.viewOnlyAnOwnerOrAdmin')}</p>{/if}

	{#if loading && !business}
		<div class="card muted" role="status">{msg('settingsPage.loading')}</div>
	{:else if business}
		<section class="card" aria-labelledby="toggles-title">
			<h3 id="toggles-title">{msg('settingsPage.salesFeaturesInReports')}</h3>
			<p class="sub">{msg('settingsPage.asInMoonlightretailAFeatureThat')}</p>
			<Switch
				id="toggle-tax"
				label={msg('settingsPage.tax')}
				description={msg('settingsPage.whenOffSalesAndRefundsAre')}
				checked={draft.tax_enabled}
				disabled={!canManage || busy}
				onchange={(v) => (draft.tax_enabled = v)}
			/>
			<Switch
				id="toggle-discount"
				label={msg('settingsPage.saleDiscounts')}
				description={msg('settingsPage.showsTheTableExplainingSaleTotals')}
				checked={draft.discount_enabled}
				disabled={!canManage || busy}
				onchange={(v) => (draft.discount_enabled = v)}
			/>
			<Switch
				id="toggle-returns"
				label={msg('settingsPage.returnsAndVoids')}
				description={msg('settingsPage.showsTheReturnsAndRefundsColumns')}
				checked={draft.returns_enabled}
				disabled={!canManage || busy}
				onchange={(v) => (draft.returns_enabled = v)}
			/>
			{#if canManage}
				<div class="btn-row actions">
					<button type="button" class="btn-primary" disabled={!dirty || busy} onclick={saveToggles} data-testid="save-toggles">{msg('settingsPage.save')}</button>
					{#if dirty}<button type="button" class="btn-secondary" disabled={busy} onclick={() => business && (draft = { tax_enabled: business.tax_enabled, discount_enabled: business.discount_enabled, returns_enabled: business.returns_enabled })}>{msg('settingsPage.undo')}</button>{/if}
				</div>
			{/if}
		</section>

		<!-- Laid out like the MoonLightRetail rate card: title beside a short
		     description, the rate in effect, one action, recent rates below. -->
		<section class="card rates" aria-labelledby="rates-title">
			<header class="rates-head">
				<h3 id="rates-title">{msg('settingsPage.usdRateTitle')}</h3>
				<p class="sub muted">{msg('settingsPage.usdRateSub')}</p>
			</header>
			{#if current}
				<p class="current" data-testid="current-rate">
					<bdi class="num"><strong>1 USD = {current.rate} EGP</strong></bdi>
					<span class="muted">{msg('settingsPage.rateSince', { time: absoluteTime(current.effective_at, lang()) })}</span>
					{#if current.note}<span class="muted">· {msg('settingsPage.rateSource', { note: current.note })}</span>{/if}
				</p>
			{:else}
				<p class="current muted">{msg('settingsPage.noCurrentRate')}</p>
			{/if}
			{#if canManage}
				{#if showRateForm}
					<form class="rate-form" onsubmit={addRate}>
						<TextField id="rate-value" label={msg('settingsPage.egpPerUsd')} inputmode="decimal" dir="ltr" placeholder="50.25" error={rateError} bind:value={rateInput} data-testid="rate-value" />
						<TextField id="rate-at" label={msg('settingsPage.effectiveFrom')} type="datetime-local" bind:value={rateAt} />
						<TextField id="rate-note" label={msg('settingsPage.noteOptional')} maxlength={120} bind:value={rateNote} />
						<div class="btn-row">
							<button type="submit" class="btn-primary" disabled={busy}>{msg('settingsPage.addRate')}</button>
							<button type="button" class="btn-secondary" disabled={busy} onclick={() => (showRateForm = false)}>{msg('settingsPage.hideForm')}</button>
						</div>
					</form>
				{:else}
					<button type="button" class="btn-primary new-rate" onclick={() => (showRateForm = true)} data-testid="new-rate">{msg('settingsPage.newRate')}</button>
				{/if}
			{/if}
			<p class="muted small">{msg('settingsPage.whenEgpSalesAreShownIn')}</p>
			{#if rates.length === 0}
				<p class="muted">{msg('settingsPage.noRatesYetWithoutOneEgp')}</p>
			{:else}
				<details class="recent" open>
					<summary>{msg('settingsPage.recentRates')}</summary>
					<ul>
						{#each rates as r (r.id)}
							<li data-testid="rate-row">
								<span class="line">
									<bdi class="num">{r.rate}</bdi>
									<span class="muted">·</span>
									<span class="num">{absoluteTime(r.effective_at, lang())}</span>
									{#if r.note}<span class="muted">· {r.note}</span>{/if}
								</span>
								{#if canManage}
									<span class="row-actions">
										{#if confirmDelete === r.id}
											<button type="button" class="btn-danger btn-sm" disabled={busy} onclick={() => deleteRate(r.id)}>{msg('settingsPage.confirmDelete')}</button>
											<button type="button" class="btn-secondary btn-sm" onclick={() => (confirmDelete = null)}>{msg('settingsPage.cancel')}</button>
										{:else}
											<button type="button" class="btn-link" onclick={() => (confirmDelete = r.id)}>{msg('settingsPage.delete')}</button>
										{/if}
									</span>
								{/if}
							</li>
						{/each}
					</ul>
				</details>
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
	.rates-head {
		display: flex;
		flex-wrap: wrap;
		align-items: baseline;
		gap: 4px 16px;
		padding-bottom: 12px;
		border-bottom: 1px solid var(--border);
	}
	.rates-head h3 {
		font-size: 1.2rem;
	}
	.rates-head .sub {
		margin: 0;
		font-size: 0.84rem;
	}
	.current {
		display: flex;
		flex-wrap: wrap;
		align-items: baseline;
		gap: 6px;
		margin: 16px 0;
		font-size: 1rem;
	}
	.new-rate {
		padding: 10px 18px;
	}
	.small {
		font-size: 0.8rem;
		margin: 14px 0 10px;
	}
	.recent summary {
		cursor: pointer;
		font-weight: 600;
	}
	.recent ul {
		margin: 10px 0 0;
		padding-inline-start: 22px;
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
	.recent li {
		display: flex;
		flex-wrap: wrap;
		justify-content: space-between;
		align-items: center;
		gap: 8px;
	}
	.recent .line {
		display: inline-flex;
		flex-wrap: wrap;
		gap: 6px;
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
