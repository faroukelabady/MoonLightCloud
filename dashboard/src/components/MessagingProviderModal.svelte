<script lang="ts">
	import { untrack } from 'svelte';
	import type { MessagingProvider, MessagingTemplate } from '../lib/settingsApi.js';
	import { msg } from '../lib/i18n.svelte.js';
	import Modal from './ui/Modal.svelte';
	import SingleSelect from './ui/SingleSelect.svelte';
	import TextField from './ui/TextField.svelte';
	let { provider, busy, error, onclose, onsave }: {
		provider: MessagingProvider; busy: boolean; error: string;
		onclose: () => void; onsave: (template: MessagingTemplate & { provider_key: string }) => Promise<void>;
	} = $props();
	let kind = $state<MessagingTemplate['template_key']>('ten_day_business_report_v1');
	let locale = $state<'ar' | 'en'>(untrack(() => provider.templates.find((m) => m.template_key === 'ten_day_business_report_v1')?.locale ?? 'ar'));
	let name = $state('');
	let language = $state('');
	$effect(() => {
		const saved = provider.templates.find((m) => m.template_key === kind && m.locale === locale);
		name = saved?.external_name ?? '';
		language = saved?.language ?? locale;
	});
	let example = $derived(locale === 'ar'
		? `${kind === 'ten_day_business_report_v1' ? 'تقرير المبيعات (10 أيام)' : 'تقرير المبيعات اليومي'}\n\nالفترة: ${kind === 'ten_day_business_report_v1' ? '2026-10-21 إلى ' : ''}2026-10-30\n\nالمعاملات: 20\n\nإجمالي المبيعات: EGP 1500.00\nالمرتجعات: EGP 100.00\nصافي المبيعات: EGP 1400.00\n\nالتكلفة: EGP 900.00`
		: `${kind === 'ten_day_business_report_v1' ? '10-Day Business Report' : 'Daily Business Report'}\n\nPeriod: ${kind === 'ten_day_business_report_v1' ? '2026-10-21 to ' : ''}2026-10-30\n\nTransactions: 20\n\nGross sales: EGP 1500.00\nRefunds: EGP 100.00\nNet sales: EGP 1400.00\n\nCost: EGP 900.00`);
	async function save(e: Event) {
		e.preventDefault();
		await onsave({ provider_key: provider.key, template_key: kind, locale, external_name: provider.kind === 'telegram' ? 'telegram_text_v1' : name.trim(), language: provider.kind === 'telegram' ? locale : language.trim(), params: ['report_body'], enabled: true });
	}
</script>

<Modal title={provider.kind === 'telegram' ? msg('messagingSettings.configureTelegram') : msg('messagingSettings.configureWhatsapp')} closeLabel={msg('messagingSettings.close')} {onclose} wide testid="provider-modal">
	<form onsubmit={save}>
		<p class="sub">{provider.kind === 'telegram' ? msg('messagingSettings.telegramTemplateHelp') : msg('messagingSettings.whatsappTemplateHelp')}</p>
		<p class="sub">{msg(provider.kind === 'telegram' ? 'messagingSettings.telegramChatHelp' : 'messagingSettings.whatsappNumberHelp')}</p>
		{#if error}<p class="error" role="alert">{error}</p>{/if}
		<section class="configured" aria-labelledby="configured-templates-title">
			<h4 id="configured-templates-title">{msg('messagingSettings.configuredTemplates')}</h4>
			{#if provider.templates.length === 0}<p class="sub">{msg('messagingSettings.noTemplates')}</p>{:else}
			<div class="table-wrap"><table class="data"><thead><tr><th>{msg('messagingSettings.report')}</th><th>{msg('messagingSettings.reportLanguage')}</th><th>{msg('messagingSettings.templateName')}</th><th>{msg('messagingSettings.languageCode')}</th><th>{msg('messagingSettings.status')}</th><th>{msg('messagingSettings.actions')}</th></tr></thead><tbody>
			{#each provider.templates as template (`${template.template_key}:${template.locale}`)}
				<tr><td>{msg(template.template_key === 'daily_business_report_v1' ? 'messagingSettings.daily' : 'messagingSettings.every10Days')}</td><td>{msg(template.locale === 'ar' ? 'messagingSettings.arabic' : 'messagingSettings.english')}</td><td><bdi>{template.external_name}</bdi></td><td><bdi>{template.language}</bdi></td><td>{msg(template.enabled ? 'messagingSettings.on' : 'messagingSettings.off')}</td><td><button type="button" class="btn-secondary btn-sm" disabled={busy} onclick={() => { kind = template.template_key; locale = template.locale; }}>{msg('messagingSettings.edit')}</button></td></tr>
			{/each}
			</tbody></table></div>{/if}
		</section>
		<div class="fields">
			<SingleSelect id="provider-report" label={msg('messagingSettings.report')} value={kind} options={[{ value: 'daily_business_report_v1', label: msg('messagingSettings.daily') }, { value: 'ten_day_business_report_v1', label: msg('messagingSettings.every10Days') }]} onchange={(v) => kind = v as typeof kind} />
			<SingleSelect id="provider-locale" label={msg('messagingSettings.reportLanguage')} value={locale} options={[{ value: 'ar', label: msg('messagingSettings.arabic') }, { value: 'en', label: msg('messagingSettings.english') }]} onchange={(v) => locale = v as typeof locale} />
			{#if provider.kind === 'whatsapp'}
				<TextField id="provider-template-name" label={msg('messagingSettings.approvedTemplateName')} required dir="ltr" bind:value={name} />
				<TextField id="provider-template-language" label={msg('messagingSettings.languageCode')} required dir="ltr" bind:value={language} />
			{/if}
		</div>
		<p class="sub">{msg(provider.templates.some((m) => m.template_key === kind && m.locale === locale && m.enabled) ? 'messagingSettings.set' : 'messagingSettings.notSet')}</p>
		{#if provider.kind === 'whatsapp'}<p class="sub">{msg('messagingSettings.exactMetaLanguage')}</p>{/if}
		<section class="preview" aria-label={msg('messagingSettings.exampleReport')}>
			<h4>{msg('messagingSettings.exampleReport')}</h4>
			<p class="sub">{msg('messagingSettings.exampleOnly')}{provider.kind === 'whatsapp' ? ` ${msg('messagingSettings.whatsappExampleHelp')}` : ''}</p>
			<pre dir={locale === 'ar' ? 'rtl' : 'ltr'}>{example}</pre>
		</section>
		<div class="actions"><button type="button" class="btn-secondary" disabled={busy} onclick={onclose}>{msg('messagingSettings.cancel')}</button><button type="submit" class="btn-primary" disabled={busy}>{provider.kind === 'telegram' ? msg('messagingSettings.useBuiltInText') : msg('messagingSettings.saveTemplate')}</button></div>
	</form>
</Modal>

<style>
	form { display: grid; gap: 16px; }
	.fields { display: grid; grid-template-columns: repeat(2,minmax(0,1fr)); gap: 12px; }
	.sub { margin: 0; color: var(--text-muted); line-height: 1.6; font-size: .86rem; }
	.preview { padding: 16px; border: 1px solid var(--border); border-radius: 10px; background: var(--bg); }
	h4 { margin: 0 0 8px; }
	pre { white-space: pre-wrap; overflow-wrap: anywhere; font: inherit; font-size: .86rem; line-height: 1.7; margin-bottom: 0; }
	.table-wrap { overflow-x: auto; }
	.table-wrap table { min-width: 580px; }
	.actions { display: flex; justify-content: flex-end; gap: 8px; }
	.error { color: var(--danger); }
	@media(max-width: 520px) { .fields { grid-template-columns: 1fr; } }
</style>
