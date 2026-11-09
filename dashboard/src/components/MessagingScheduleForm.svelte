<script lang="ts">
	import { untrack } from 'svelte';
	import type { MessagingRecipient, MessagingSchedule } from '../lib/settingsApi.js';
	import { msg } from '../lib/i18n.svelte.js';
	import SingleSelect from './ui/SingleSelect.svelte';
	import MultiSelect from './ui/MultiSelect.svelte';
	import TextField from './ui/TextField.svelte';
	let { existing, recipients, busy, onsave, oncancel }: {
		existing: MessagingSchedule | null; recipients: MessagingRecipient[]; busy: boolean;
		onsave: (draft: { name: string; kind: string; local_time: string; anchor_local_date: string; recipient_ids: string[]; revision: number }) => Promise<void>;
		oncancel: () => void;
	} = $props();
	let name = $state(untrack(() => existing?.name ?? ''));
	let kind = $state(untrack(() => existing?.kind ?? 'ten_day'));
	let localTime = $state(untrack(() => existing?.local_time ?? '21:00'));
	let anchor = $state(untrack(() => existing?.anchor_local_date ?? ''));
	let selected = $state<string[]>(untrack(() => [...existing?.recipient_ids ?? []]));
	const revision = untrack(() => existing?.revision ?? 0);
	let options = $derived(recipients.filter((r) => r.enabled || selected.includes(r.id)).map((r) => ({ value: r.id, label: `${r.label} · ${r.recipient_masked}${r.enabled ? '' : ` (${msg('messagingSettings.off')})`}` })));
	async function save(e: Event) {
		e.preventDefault();
		await onsave({ name: name.trim(), kind, local_time: localTime, anchor_local_date: kind === 'ten_day' ? anchor : '', recipient_ids: selected, revision });
	}
</script>

<form onsubmit={save} data-testid="schedule-form">
	<div class="fields">
		<TextField id="schedule-name" label={msg('messagingSettings.name')} required maxlength={64} bind:value={name} />
		<SingleSelect id="schedule-kind" label={msg('messagingSettings.report')} value={kind} options={[{ value: 'daily', label: msg('messagingSettings.dailyThePreviousDay') }, { value: 'ten_day', label: msg('messagingSettings.every10Days') }]} onchange={(v) => kind = v as typeof kind} />
		<TextField id="schedule-time" label={msg('messagingSettings.timeCairo')} type="time" required bind:value={localTime} />
		{#if kind === 'ten_day'}<TextField id="schedule-anchor" label={msg('messagingSettings.firstDayOfTheCycle')} type="date" required bind:value={anchor} />{/if}
	</div>
	<p class="sub">{kind === 'ten_day' ? msg('messagingSettings.tenDayCycleHelp') : msg('messagingSettings.dailyCycleHelp')}</p>
	<MultiSelect id="schedule-recipients" label={msg('messagingSettings.recipients')} emptyLabel={msg('messagingSettings.chooseRecipients')} {options} {selected} onchange={(values) => selected = values} />
	{#if existing}<p class="sub">{msg('messagingSettings.scheduleEditHelp')}</p>{/if}
	<div class="actions">
		{#if existing}<button type="button" class="btn-secondary" disabled={busy} onclick={oncancel}>{msg('messagingSettings.cancel')}</button>{/if}
		<button type="submit" class="btn-primary" disabled={busy || selected.length === 0}>{existing ? msg('messagingSettings.saveChanges') : msg('messagingSettings.create')}</button>
	</div>
</form>

<style>
	form { display: grid; gap: 14px; }
	.fields { display: grid; grid-template-columns: repeat(auto-fit,minmax(170px,1fr)); gap: 12px; align-items: end; }
	.sub { margin: 0; line-height: 1.6; color: var(--text-muted); font-size: .86rem; }
	.actions { display: flex; justify-content: flex-end; gap: 8px; }
</style>
