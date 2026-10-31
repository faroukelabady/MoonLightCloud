<script lang="ts">
	import { msg } from '../lib/i18n.svelte.js';
	import SingleSelect from './ui/SingleSelect.svelte';
	import MultiSelect from './ui/MultiSelect.svelte';
	import { downloadRecoveryCodes } from '../lib/recoveryCodes.js';
	import { onMount } from 'svelte';
	import { authApi, authErrorText, usersApi } from '../lib/api.js';
	import type { AuthAuditRow, Me, StoreRow, UserView } from '../lib/api.js';

	// Users & Security (ADR-0053). Everyone manages their own recovery codes
	// here (the password changes from the account menu); OWNERs administer accounts. There is no public
	// registration: an OWNER creates the account and conveys the one-time
	// activation link out of band. Every action is re-authorized server-side;
	// sensitive ones require an explicit confirmation here.
	let { me, stores, onsessionchange }: { me: Me; stores: StoreRow[]; onsessionchange: () => void } = $props();

	let canRead = $derived(me.permissions.includes('users.read'));
	let canManage = $derived(me.permissions.includes('users.manage'));
	let canSecurity = $derived(me.permissions.includes('security.manage'));

	let notice = $state<{ kind: 'ok' | 'err'; text: string } | null>(null);
	let busy = $state(false);

	// ---- own account ----
	let codes = $state<string[]>([]);

	async function act(fn: () => Promise<void>) {
		if (busy) return;
		busy = true;
		notice = null;
		try {
			await fn();
		} catch (err) {
			notice = { kind: 'err', text: authErrorText(err) };
		} finally {
			busy = false;
		}
	}

	function regenerateCodes() {
		if (!confirm(msg('usersPage.confirmRegenerate'))) return;
		void act(async () => {
			codes = (await authApi.regenerateRecoveryCodes()).recovery_codes;
		});
	}

	// ---- administration ----
	let users = $state<UserView[]>([]);
	let cursor = $state('');
	let activation = $state<{ login: string; link: string } | null>(null);
	let form = $state({ login: '', display_name: '', role: 'admin' as 'owner' | 'admin', all_stores: false, store_ids: [] as string[] });

	async function fetchUsers(reset: boolean) {
		if (!canRead) return;
		const v = await usersApi.list(reset ? '' : cursor);
		users = reset ? v.users : [...users, ...v.users];
		cursor = v.next_cursor;
	}

	// loadUsers is the standalone (busy-guarded) load; mutations already run
	// inside act() and refresh with fetchUsers directly — calling loadUsers
	// there would be swallowed by act()'s busy guard and leave a stale list.
	async function loadUsers(reset: boolean) {
		await act(() => fetchUsers(reset));
	}

	function activationLink(token: string): string {
		return `${window.location.origin}/dashboard/#activate=${encodeURIComponent(token)}`;
	}

	function createUser(e: Event) {
		e.preventDefault();
		if (!form.all_stores && form.store_ids.length === 0) {
			notice = { kind: 'err', text: msg('usersPage.chooseAtLeastOneStore') };
			return;
		}
		if (form.role === 'owner' && !confirm(msg('usersPage.confirmCreateOwner', { login: form.login }))) return;
		void act(async () => {
			const r = await usersApi.create({ ...form, store_ids: form.all_stores ? [] : form.store_ids });
			activation = { login: r.user.login, link: activationLink(r.activation_token) };
			form = { login: '', display_name: '', role: 'admin', all_stores: false, store_ids: [] };
			await fetchUsers(true);
		});
	}

	function setStatus(u: UserView, status: 'active' | 'disabled') {
		if (!confirm(msg(status === 'disabled' ? 'usersPage.confirmDisable' : 'usersPage.confirmEnable', { login: u.login }))) return;
		void act(async () => {
			await usersApi.action(u.id, 'status', { status });
			await fetchUsers(true);
		});
	}

	function setRole(u: UserView, role: 'owner' | 'admin') {
		if (!confirm(msg('usersPage.confirmRole', { login: u.login, role: msg(`usersPage.role.${role}`) }))) return;
		void act(async () => {
			await usersApi.action(u.id, 'role', { role });
			await fetchUsers(true);
			if (u.id === me.user.id) onsessionchange();
		});
	}

	function resetMFA(u: UserView) {
		if (!confirm(msg('usersPage.confirmMfaReset', { login: u.login }))) return;
		void act(async () => {
			await usersApi.action(u.id, 'mfa-reset');
			notice = { kind: 'ok', text: msg('usersPage.mfaResetDone', { login: u.login }) };
		});
	}

	function revokeSessions(u: UserView) {
		if (!confirm(msg('usersPage.confirmSignOut', { login: u.login }))) return;
		void act(async () => {
			await usersApi.action(u.id, 'sessions-revoke');
			notice = { kind: 'ok', text: msg('usersPage.sessionsRevoked', { login: u.login }) };
		});
	}

	function reissue(u: UserView) {
		void act(async () => {
			const r = await usersApi.action<{ activation_token: string }>(u.id, 'activation');
			activation = { login: u.login, link: activationLink(r.activation_token) };
		});
	}

	function storeNames(u: UserView): string {
		if (u.all_stores) return msg('usersPage.allStores');
		return u.store_ids.map((id) => stores.find((s) => s.store_id === id)?.display_name ?? id.slice(0, 8)).join(msg('usersPage.text'));
	}

	// ---- security audit (all-Stores OWNERs) ----
	let audit = $state<AuthAuditRow[]>([]);
	async function loadAudit() {
		if (!canRead || !me.all_stores) return;
		try {
			audit = (await usersApi.audit()).events;
		} catch {
			audit = [];
		}
	}

	onMount(() => {
		void loadUsers(true);
		void loadAudit();
	});
</script>

<section class="users" data-testid="users-page">
	<header class="card">
		<h2>{msg('usersPage.usersSecurity')}</h2>
	</header>
	{#if notice}<p class="notice {notice.kind}" role="status" data-testid="users-notice">{notice.text}</p>{/if}

	<section class="card panel">
		<h3>{msg('usersPage.myAccount')} — {me.user.display_name} ({me.user.role})</h3>
		<div class="btn-row">
			<button class="btn-secondary" type="button" onclick={regenerateCodes} disabled={busy} data-testid="regenerate-codes">
				{msg('usersPage.regenerateRecoveryCodes')}
			</button>
		</div>
		{#if codes.length}
			<p class="muted">{msg('usersPage.shownOnceStoreThemSafely')}</p>
			<ol class="codes" dir="ltr">{#each codes as c (c)}<li><code>{c}</code></li>{/each}</ol>
			<div class="btn-row">
				<button class="btn-secondary" type="button" data-testid="download-codes" onclick={() => downloadRecoveryCodes(me.user.login, codes)}>{msg('usersPage.downloadCodes')}</button>
			</div>
		{/if}
	</section>

	{#if canManage}
		<section class="card panel">
			<h3>{msg('usersPage.addUser')}</h3>
			<form class="row" onsubmit={createUser} data-testid="create-user">
				<label class="field">{msg('usersPage.login')}<input bind:value={form.login} required /></label>
				<label class="field">{msg('usersPage.displayName')}<input bind:value={form.display_name} required /></label>
				<SingleSelect
					id="user-role"
					label={msg('usersPage.role')}
					testid="user-role"
					value={form.role}
					options={[{ value: 'admin', label: msg('usersPage.admin') }, ...(me.all_stores ? [{ value: 'owner', label: msg('usersPage.owner') }] : [])]}
					onchange={(v) => (form.role = v as typeof form.role)}
				/>
				{#if me.all_stores}
					<label class="check"><input type="checkbox" bind:checked={form.all_stores} /> {msg('usersPage.allStores')}</label>
				{/if}
				{#if !form.all_stores}
					<MultiSelect
						id="user-stores"
						label={msg('usersPage.stores')}
						testid="user-stores"
						emptyLabel={msg('usersPage.chooseAtLeastOneStore2')}
						options={stores.map((s) => ({ value: s.store_id, label: s.display_name }))}
						selected={form.store_ids}
						onchange={(values) => (form.store_ids = values)}
					/>
				{/if}
				<button class="btn-primary" type="submit" disabled={busy}>{msg('usersPage.create')}</button>
			</form>
			{#if activation}
				<div class="activation" data-testid="activation-link">
					<p>
						{msg('usersPage.activationLink', { login: activation.login })}
					</p>
					<input readonly value={activation.link} dir="ltr" onfocus={(e) => (e.currentTarget as HTMLInputElement).select()} />
				</div>
			{/if}
		</section>
	{/if}

	{#if canRead}
		<section class="card panel">
		<h3>{msg('usersPage.accounts')}</h3>
		<div class="table-wrap">
		<table class="data">
			<thead><tr><th>{msg('usersPage.colLogin')}</th><th>{msg('usersPage.colName')}</th><th>{msg('usersPage.colRole')}</th><th>{msg('usersPage.colStatus')}</th><th>{msg('usersPage.colStores')}</th><th>{msg('usersPage.colMfa')}</th><th></th></tr></thead>
			<tbody>
				{#each users as u (u.id)}
					<tr data-testid="user-row">
						<td dir="ltr">{u.login}</td>
						<td>{u.display_name}</td>
						<td>{u.role}</td>
						<td>{u.status}</td>
						<td>{storeNames(u)}</td>
						<td>{u.mfa_enabled ? '✓' : '—'}</td>
						<td class="actions">
							{#if canManage && u.id !== me.user.id}
								{#if u.status === 'active'}<button class="btn-secondary btn-sm" onclick={() => setStatus(u, 'disabled')}>{msg('usersPage.disable')}</button>{/if}
								{#if u.status === 'disabled'}<button class="btn-secondary btn-sm" onclick={() => setStatus(u, 'active')}>{msg('usersPage.enable')}</button>{/if}
								{#if u.status === 'pending_setup'}<button class="btn-secondary btn-sm" onclick={() => reissue(u)}>{msg('usersPage.newLink')}</button>{/if}
								{#if u.role === 'admin' && me.all_stores}<button class="btn-secondary btn-sm" onclick={() => setRole(u, 'owner')}>{msg('usersPage.makeOwner')}</button>{/if}
								{#if u.role === 'owner'}<button class="btn-secondary btn-sm" onclick={() => setRole(u, 'admin')}>{msg('usersPage.makeAdmin')}</button>{/if}
								{#if canSecurity && u.mfa_enabled}<button class="btn-secondary btn-sm" onclick={() => resetMFA(u)}>{msg('usersPage.resetMfa')}</button>{/if}
								<button class="btn-secondary btn-sm" onclick={() => revokeSessions(u)}>{msg('usersPage.signOut')}</button>
							{/if}
						</td>
					</tr>
				{/each}
			</tbody>
		</table>
		</div>
		{#if cursor}<div class="btn-row"><button class="btn-secondary btn-sm" onclick={() => loadUsers(false)} disabled={busy}>{msg('usersPage.more')}</button></div>{/if}
		</section>
	{/if}

	{#if audit.length}
		<section class="card panel">
			<h3>{msg('usersPage.securityAuditLatest50')}</h3>
			<ul class="audit">
				{#each audit as a (a.id)}
					<li><span class="muted" dir="ltr">{new Date(a.occurred_at).toLocaleString()}</span> {a.action} · {a.outcome}{a.reason ? ` · ${a.reason}` : ''}</li>
				{/each}
			</ul>
		</section>
	{/if}
</section>

<style>
	.users { display: flex; flex-direction: column; gap: 12px; }
	h2, h3 { margin: 0; }
	h2 { font-size: 1.1rem; }
	h3 { font-size: 1rem; }
	.panel { display: flex; flex-direction: column; gap: 12px; }
	.row { display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr)); gap: 12px; align-items: end; }
	.row .check { min-height: 38px; }
	.row button { justify-self: start; }
	.notice { margin: 0; padding: 10px 14px; border-radius: var(--radius-card); font-weight: 600; }
	.notice.ok { border: 1px solid #bbf7d0; background: #f0fdf4; color: var(--success); }
	.notice.err { border: 1px solid #fecaca; background: #fef2f2; color: var(--danger); }
	.codes { columns: 2; margin: 0; font-family: monospace; }
	.activation { display: grid; gap: 8px; }
	.activation p { margin: 0; }
	.activation input { width: 100%; font-family: monospace; }
	.actions { display: flex; flex-wrap: wrap; gap: 6px; }
	.muted { opacity: 0.75; }
	.audit { margin: 0; padding-inline-start: 18px; font-size: 0.85rem; max-height: 320px; overflow: auto; line-height: 1.7; }
	.table-wrap { overflow-x: auto; }
</style>
