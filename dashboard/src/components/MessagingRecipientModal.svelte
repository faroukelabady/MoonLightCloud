<script lang="ts">
	import { untrack } from 'svelte';
	import type { MessagingProvider, MessagingRecipientDetail } from '../lib/settingsApi.js';
	import { msg } from '../lib/i18n.svelte.js';
	import Modal from './ui/Modal.svelte';
	import SingleSelect from './ui/SingleSelect.svelte';
	import TextField from './ui/TextField.svelte';
	let { providers, existing, busy, error, onclose, onsave }: {
		providers: MessagingProvider[]; existing: MessagingRecipientDetail | null; busy: boolean; error: string;
		onclose: () => void; onsave: (draft: { label: string; provider_key: string; recipient: string; locale: string }) => Promise<void>;
	} = $props();
	let label = $state(untrack(() => existing?.label ?? ''));
	let providerKey = $state(untrack(() => existing?.provider_key ?? providers[0]?.key ?? ''));
	let address = $state(untrack(() => existing?.recipient ?? ''));
	let locale = $state(untrack(() => existing?.locale ?? 'ar'));
	let kind = $derived(providers.find((p) => p.key === providerKey)?.kind ?? 'telegram');
	let invalidLeadingZero = $derived(kind === 'whatsapp' && /^0/.test(address.replace(/[ +\-]/g, '')));
	async function save(e: Event) { e.preventDefault(); if (!invalidLeadingZero) await onsave({ label: label.trim(), provider_key: providerKey, recipient: address.trim(), locale }); }
</script>

<Modal title={existing ? msg('messagingSettings.editRecipient') : msg('messagingSettings.addRecipient')} closeLabel={msg('messagingSettings.close')} {onclose} wide testid="recipient-modal">
	<form onsubmit={save}>
		{#if error}<p class="error" role="alert">{error}</p>{/if}
		<div class="fields">
			<TextField id="recipient-label" label={msg('messagingSettings.name')} maxlength={64} required bind:value={label} />
			<SingleSelect id="recipient-provider" label={msg('messagingSettings.provider')} value={providerKey} disabled={!!existing} options={providers.map((p) => ({ value: p.key, label: p.kind === 'telegram' ? 'Telegram' : 'WhatsApp' }))} onchange={(v) => providerKey = v} />
			<TextField id="recipient-address" label={kind === 'telegram' ? msg('messagingSettings.chatIdOrUsername') : msg('messagingSettings.whatsappNumberInternational')} required dir="ltr" bind:value={address} />
			<SingleSelect id="recipient-locale" label={msg('messagingSettings.reportLanguage')} value={locale} options={[{ value: 'ar', label: msg('messagingSettings.arabic') }, { value: 'en', label: msg('messagingSettings.english') }]} onchange={(v) => locale = v as typeof locale} />
		</div>
		<p class="hint" class:error={invalidLeadingZero} data-testid="recipient-address-help">{kind === 'telegram' ? msg('messagingSettings.telegramChatHelp') : msg('messagingSettings.whatsappNumberHelp')}</p>
		{#if invalidLeadingZero}<p class="error" role="alert">{msg('messagingSettings.leadingZeroError')}</p>{/if}
		<p class="hint">{msg('messagingSettings.recipientEditHelp')}</p>
		<div class="actions"><button type="button" class="btn-secondary" disabled={busy} onclick={onclose}>{msg('messagingSettings.cancel')}</button><button type="submit" class="btn-primary" disabled={busy || invalidLeadingZero}>{existing ? msg('messagingSettings.saveChanges') : msg('messagingSettings.add')}</button></div>
	</form>
</Modal>

<style>
	form { display: grid; gap: 16px; }
	.fields { display: grid; grid-template-columns: repeat(2,minmax(0,1fr)); gap: 12px; }
	.hint { margin: 0; padding: 12px; border-radius: 8px; background: var(--bg); line-height: 1.7; font-size: .86rem; color: var(--text-muted); }
	.actions { display: flex; justify-content: flex-end; gap: 8px; }
	.error { color: var(--danger); }
	@media(max-width: 520px) { .fields { grid-template-columns: 1fr; } }
</style>
