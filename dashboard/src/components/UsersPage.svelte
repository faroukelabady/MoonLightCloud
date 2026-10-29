<script lang="ts">
	import { arabic, msg, msgb } from '../lib/i18n.svelte.js';
	import SingleSelect from './ui/SingleSelect.svelte';
	import MultiSelect from './ui/MultiSelect.svelte';
	import { downloadRecoveryCodes } from '../lib/recoveryCodes.js';
	import { onMount } from 'svelte';
	import { authApi, authErrorText, usersApi } from '../lib/api.js';
	import type { AuthAuditRow, Me, StoreRow, UserView } from '../lib/api.js';

	// Users & Security (ADR-0053). Everyone manages their own password and
	// recovery codes; OWNERs administer accounts. There is no public
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
	let current = $state('');
	let next = $state('');
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

	function changePassword(e: Event) {
		e.preventDefault();
		void act(async () => {
			await authApi.changePassword(current, next);
			current = '';
			next = '';
			notice = { kind: 'ok', text: msgb('usersPage.passwordChangedOtherSessionsWereSigned') };
		});
	}

	function regenerateCodes() {
		if (!confirm('Regenerate recovery codes? All previous recovery codes stop working immediately.')) return;
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
			notice = { kind: 'err', text: msgb('usersPage.chooseAtLeastOneStore') };
			return;
		}
		if (form.role === 'owner' && !confirm(`Create ${form.login} as OWNER? Owners can administer accounts and security.`)) return;
		void act(async () => {
			const r = await usersApi.create({ ...form, store_ids: form.all_stores ? [] : form.store_ids });
			activation = { login: r.user.login, link: activationLink(r.activation_token) };
			form = { login: '', display_name: '', role: 'admin', all_stores: false, store_ids: [] };
			await fetchUsers(true);
		});
	}

	function setStatus(u: UserView, status: 'active' | 'disabled') {
		const verb = status === 'disabled' ? 'Disable' : 'Re-enable';
		if (!confirm(`${verb} ${u.login}? ${status === 'disabled' ? 'All of their sessions end immediately.' : ''}`)) return;
		void act(async () => {
			await usersApi.action(u.id, 'status', { status });
			await fetchUsers(true);
		});
	}

	function setRole(u: UserView, role: 'owner' | 'admin') {
		if (!confirm(`Change ${u.login} to ${role}? Their sessions end and they must sign in again.`)) return;
		void act(async () => {
			await usersApi.action(u.id, 'role', { role });
			await fetchUsers(true);
			if (u.id === me.user.id) onsessionchange();
		});
	}

	function resetMFA(u: UserView) {
		if (!confirm(`Reset two-step verification for ${u.login}? They must re-enroll at next sign-in.`)) return;
		void act(async () => {
			await usersApi.action(u.id, 'mfa-reset');
			notice = { kind: 'ok', text: `MFA reset for ${u.login}.` };
		});
	}

	function revokeSessions(u: UserView) {
		if (!confirm(`Sign ${u.login} out everywhere?`)) return;
		void act(async () => {
			await usersApi.action(u.id, 'sessions-revoke');
			notice = { kind: 'ok', text: `Sessions revoked for ${u.login}.` };
		});
	}

	function reissue(u: UserView) {
		void act(async () => {
			const r = await usersApi.action<{ activation_token: string }>(u.id, 'activation');
			activation = { login: u.login, link: activationLink(r.activation_token) };
		});
	}

	function storeNames(u: UserView): string {
		if (u.all_stores) return msgb('usersPage.allStores');
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
		<h2>{msgb('usersPage.usersSecurity')}</h2>
	</header>
	{#if notice}<p class="notice {notice.kind}" role="status" data-testid="users-notice">{notice.text}</p>{/if}

	<section class="card panel">
		<h3>{msgb('usersPage.myAccount')} — {me.user.display_name} ({me.user.role})</h3>
		<form class="row" onsubmit={changePassword}>
			<label class="field">{msgb('usersPage.currentPassword')}<input type="password" autocomplete="current-password" bind:value={current} required /></label>
			<label class="field">{msgb('usersPage.newPassword')}<input type="password" autocomplete="new-password" minlength="12" bind:value={next} required /></label>
			<button class="btn-primary" type="submit" disabled={busy}>{msgb('usersPage.change')}</button>
		</form>
		<div class="btn-row">
			<button class="btn-secondary" type="button" onclick={regenerateCodes} disabled={busy} data-testid="regenerate-codes">
				{msgb('usersPage.regenerateRecoveryCodes')}
			</button>
		</div>
		{#if codes.length}
			<p class="muted">{msg('usersPage.shownOnceStoreThemSafely')}</p>
			<ol class="codes" dir="ltr">{#each codes as c (c)}<li><code>{c}</code></li>{/each}</ol>
			<div class="btn-row">
				<button class="btn-secondary" type="button" data-testid="download-codes" onclick={() => downloadRecoveryCodes(me.user.login, codes)}>{msgb('usersPage.downloadCodes')}</button>
			</div>
		{/if}
	</section>

	{#if canManage}
		<section class="card panel">
			<h3>{msgb('usersPage.addUser')}</h3>
			<form class="row" onsubmit={createUser} data-testid="create-user">
				<label class="field">{msgb('usersPage.login')}<input bind:value={form.login} required /></label>
				<label class="field">{msgb('usersPage.displayName')}<input bind:value={form.display_name} required /></label>
				<SingleSelect
					id="user-role"
					label={msgb('usersPage.role')}
					testid="user-role"
					value={form.role}
					options={[{ value: 'admin', label: msgb('usersPage.admin') }, ...(me.all_stores ? [{ value: 'owner', label: msgb('usersPage.owner') }] : [])]}
					onchange={(v) => (form.role = v as typeof form.role)}
				/>
				{#if me.all_stores}
					<label class="check"><input type="checkbox" bind:checked={form.all_stores} /> {msgb('usersPage.allStores')}</label>
				{/if}
				{#if !form.all_stores}
					<MultiSelect
						id="user-stores"
						label={msgb('usersPage.stores')}
						testid="user-stores"
						emptyLabel={msgb('usersPage.chooseAtLeastOneStore2')}
						options={stores.map((s) => ({ value: s.store_id, label: s.display_name }))}
						selected={form.store_ids}
						onchange={(values) => (form.store_ids = values)}
					/>
				{/if}
				<button class="btn-primary" type="submit" disabled={busy}>{msgb('usersPage.create')}</button>
			</form>
			{#if activation}
				<div class="activation" data-testid="activation-link">
					<p>
						{#if arabic()}رابط التفعيل لمرة واحدة لـ {activation.login} (صالح 24 ساعة). أرسله بطريقة آمنة؛ لن يظهر مجددًا. / {/if}One-time activation link for
						{activation.login} (valid 24 h). Send it securely; it will not be shown again.
					</p>
					<input readonly value={activation.link} dir="ltr" onfocus={(e) => (e.currentTarget as HTMLInputElement).select()} />
				</div>
			{/if}
		</section>
	{/if}

	{#if canRead}
		<section class="card panel">
		<h3>{msgb('usersPage.accounts')}</h3>
		<div class="table-wrap">
		<table class="data">
			<thead><tr><th>Login</th><th>Name</th><th>Role</th><th>Status</th><th>Stores</th><th>MFA</th><th></th></tr></thead>
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
								{#if u.status === 'active'}<button class="btn-secondary btn-sm" onclick={() => setStatus(u, 'disabled')}>{msgb('usersPage.disable')}</button>{/if}
								{#if u.status === 'disabled'}<button class="btn-secondary btn-sm" onclick={() => setStatus(u, 'active')}>{msgb('usersPage.enable')}</button>{/if}
								{#if u.status === 'pending_setup'}<button class="btn-secondary btn-sm" onclick={() => reissue(u)}>{msgb('usersPage.newLink')}</button>{/if}
								{#if u.role === 'admin' && me.all_stores}<button class="btn-secondary btn-sm" onclick={() => setRole(u, 'owner')}>{msgb('usersPage.makeOwner')}</button>{/if}
								{#if u.role === 'owner'}<button class="btn-secondary btn-sm" onclick={() => setRole(u, 'admin')}>{msgb('usersPage.makeAdmin')}</button>{/if}
								{#if canSecurity && u.mfa_enabled}<button class="btn-secondary btn-sm" onclick={() => resetMFA(u)}>{msgb('usersPage.resetMfa')}</button>{/if}
								<button class="btn-secondary btn-sm" onclick={() => revokeSessions(u)}>{msgb('usersPage.signOut')}</button>
							{/if}
						</td>
					</tr>
				{/each}
			</tbody>
		</table>
		</div>
		{#if cursor}<div class="btn-row"><button class="btn-secondary btn-sm" onclick={() => loadUsers(false)} disabled={busy}>{msgb('usersPage.more')}</button></div>{/if}
		</section>
	{/if}

	{#if audit.length}
		<section class="card panel">
			<h3>{msgb('usersPage.securityAuditLatest50')}</h3>
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
