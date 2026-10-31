<script lang="ts">
	import Modal from './ui/Modal.svelte';
	import { authApi, authErrorText, type Me } from '../lib/authApi.js';
	import { msg } from '../lib/i18n.svelte.js';

	// One account control: the avatar opens a menu with the user, their role,
	// Change password (a dialog) and Log out.
	let { me, loggingOut = false, onlogout }: { me: Me; loggingOut?: boolean; onlogout: () => void } = $props();

	let open = $state(false);
	let root: HTMLDivElement | undefined = $state();
	let trigger: HTMLButtonElement | undefined = $state();

	let name = $derived(me.user.display_name || me.user.login);
	let initials = $derived(
		name
			.split(/\s+/)
			.filter(Boolean)
			.slice(0, 2)
			.map((part) => part[0])
			.join('')
			.toUpperCase() || '?'
	);

	function toggle() {
		open = !open;
	}
	function close(refocus = false) {
		open = false;
		if (refocus) trigger?.focus();
	}
	function outside(e: MouseEvent) {
		if (open && root && !root.contains(e.target as Node)) close();
	}
	function key(e: KeyboardEvent) {
		if (open && e.key === 'Escape') close(true);
	}

	// ---- change password ----
	let changing = $state(false);
	let current = $state('');
	let next = $state('');
	let confirmNext = $state('');
	let error = $state('');
	let done = $state('');
	let busy = $state(false);

	function openPassword() {
		close();
		current = next = confirmNext = error = done = '';
		changing = true;
	}

	async function savePassword(e: Event) {
		e.preventDefault();
		error = '';
		if (next.length < 12) {
			error = msg('userMenu.tooShort');
			return;
		}
		if (next !== confirmNext) {
			error = msg('userMenu.mismatch');
			return;
		}
		busy = true;
		try {
			await authApi.changePassword(current, next);
			current = next = confirmNext = '';
			done = msg('userMenu.changed');
		} catch (err) {
			error = authErrorText(err);
		} finally {
			busy = false;
		}
	}
</script>

<svelte:window onclick={outside} onkeydown={key} />

<div class="usermenu" bind:this={root}>
	<button
		type="button"
		class="avatar"
		bind:this={trigger}
		aria-haspopup="menu"
		aria-expanded={open}
		aria-label={`${msg('userMenu.account')}: ${name}`}
		title={name}
		data-testid="current-user"
		onclick={toggle}
	>
		<span aria-hidden="true">{initials}</span>
		<span class="online" aria-hidden="true"></span>
	</button>
	{#if open}
		<div class="panel" role="menu" aria-label={msg('userMenu.account')}>
			<div class="who">
				<span class="avatar big" aria-hidden="true">{initials}</span>
				<div class="names">
					<strong>{name}</strong>
					<span class="login" dir="ltr">{me.user.login}</span>
				</div>
			</div>
			<div class="role">
				<svg viewBox="0 0 24 24" width="15" height="15" fill="currentColor" aria-hidden="true"><path d="M12 2 4 5v6c0 5 3.4 9.4 8 11 4.6-1.6 8-6 8-11V5l-8-3zm-1.2 13.6L7.5 12.3l1.4-1.4 1.9 1.9 4.3-4.3 1.4 1.4-5.7 5.7z" /></svg>
				{me.user.role === 'owner' || me.user.role === 'admin' ? msg(`userMenu.role.${me.user.role}`) : me.user.role}
			</div>
			<button type="button" role="menuitem" class="item" onclick={openPassword} data-testid="change-password">
				<svg viewBox="0 0 24 24" width="17" height="17" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="5" y="11" width="14" height="9" rx="2" /><path d="M8 11V8a4 4 0 0 1 8 0v3" /></svg>
				<span>{msg('userMenu.changePassword')}</span>
				<svg class="chev" viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="m9 6 6 6-6 6" /></svg>
			</button>
			<button type="button" role="menuitem" class="item danger" disabled={loggingOut} onclick={onlogout} data-testid="logout">
				<svg viewBox="0 0 24 24" width="17" height="17" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M15 4h4v16h-4M10 16l4-4-4-4M14 12H4" /></svg>
				<span>{msg('userMenu.logout')}</span>
			</button>
		</div>
	{/if}
</div>

{#if changing}
	<Modal title={msg('userMenu.changePassword')} onclose={() => (changing = false)} closeLabel={msg('userMenu.cancel')} testid="password-dialog">
		{#if done}
			<p class="ok" role="status">{done}</p>
			<div class="row end"><button type="button" class="btn-primary" onclick={() => (changing = false)}>{msg('saleDetail.close')}</button></div>
		{:else}
			<form class="pwform" onsubmit={savePassword}>
				<p class="muted hint">{msg('userMenu.hint')}</p>
				<label class="field">{msg('userMenu.currentPassword')}<input type="password" autocomplete="current-password" bind:value={current} required disabled={busy} /></label>
				<label class="field">{msg('userMenu.newPassword')}<input type="password" autocomplete="new-password" minlength="12" bind:value={next} required disabled={busy} /></label>
				<label class="field">{msg('userMenu.confirmPassword')}<input type="password" autocomplete="new-password" minlength="12" bind:value={confirmNext} required disabled={busy} aria-invalid={error ? 'true' : undefined} /></label>
				{#if error}<p class="err" role="alert">{error}</p>{/if}
				<div class="row end">
					<button type="button" class="btn-secondary" disabled={busy} onclick={() => (changing = false)}>{msg('userMenu.cancel')}</button>
					<button type="submit" class="btn-primary" disabled={busy}>{msg('userMenu.save')}</button>
				</div>
			</form>
		{/if}
	</Modal>
{/if}

<style>
	.usermenu {
		position: relative;
	}
	.avatar {
		position: relative;
		width: 34px;
		height: 34px;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		border-radius: 50%;
		border: 2px solid var(--primary-soft);
		background: var(--primary);
		color: #fff;
		font-size: 0.78rem;
		font-weight: 800;
		cursor: pointer;
		padding: 0;
	}
	.avatar[aria-expanded='true'] {
		box-shadow: 0 0 0 3px var(--primary-soft);
	}
	.online {
		position: absolute;
		inset-inline-end: -1px;
		bottom: -1px;
		width: 10px;
		height: 10px;
		border-radius: 50%;
		background: var(--success);
		border: 2px solid #fff;
	}
	.panel {
		position: absolute;
		inset-inline-end: 0;
		top: calc(100% + 8px);
		z-index: 40;
		width: 280px;
		padding: 12px;
		border: 1px solid var(--border);
		border-radius: 14px;
		background: var(--surface, #fff);
		box-shadow: 0 12px 32px rgb(15 23 42 / 0.16);
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
	.who {
		display: flex;
		align-items: center;
		gap: 10px;
		padding-bottom: 10px;
		border-bottom: 1px solid var(--border);
	}
	.avatar.big {
		width: 44px;
		height: 44px;
		font-size: 0.95rem;
		cursor: default;
		flex: none;
	}
	.names {
		display: flex;
		flex-direction: column;
		min-width: 0;
	}
	.names strong {
		font-size: 0.95rem;
	}
	.login {
		font-size: 0.78rem;
		color: var(--text-muted);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.role {
		display: flex;
		align-items: center;
		gap: 8px;
		margin: 4px 0;
		padding: 8px 10px;
		border-radius: 10px;
		background: var(--surface-muted);
		color: var(--primary);
		font-size: 0.82rem;
		font-weight: 600;
	}
	.item {
		display: flex;
		align-items: center;
		gap: 10px;
		width: 100%;
		padding: 9px 10px;
		border: 0;
		border-radius: 10px;
		background: transparent;
		color: var(--text);
		font: inherit;
		font-size: 0.9rem;
		text-align: start;
		cursor: pointer;
	}
	.item:hover,
	.item:focus-visible {
		background: var(--surface-muted);
	}
	.item span {
		flex: 1;
	}
	.chev {
		color: var(--text-muted);
	}
	:global([dir='rtl']) .chev {
		transform: scaleX(-1);
	}
	.item.danger {
		color: var(--danger);
	}
	.pwform {
		display: flex;
		flex-direction: column;
		gap: 10px;
	}
	.field {
		display: flex;
		flex-direction: column;
		gap: 4px;
		font-size: 0.82rem;
		font-weight: 600;
	}
	.field input {
		padding: 7px 10px;
		border: 1px solid var(--field-border);
		border-radius: var(--radius-control);
	}
	.hint {
		margin: 0;
		font-size: 0.8rem;
	}
	.err {
		margin: 0;
		padding: 6px 10px;
		border-radius: var(--radius-control);
		background: #fdeeee;
		color: #991b1b;
		font-weight: 600;
	}
	.ok {
		margin: 0 0 12px;
		padding: 8px 10px;
		border-radius: var(--radius-control);
		background: #e8f6ee;
		color: #15803d;
		font-weight: 600;
	}
	.row {
		display: flex;
		gap: 8px;
	}
	.row.end {
		justify-content: flex-end;
	}
</style>
