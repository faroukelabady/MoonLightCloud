<script lang="ts">
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
			notice = { kind: 'ok', text: 'تم تغيير كلمة المرور وإنهاء الجلسات الأخرى. / Password changed; other sessions were signed out.' };
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
	let form = $state({ login: '', display_name: '', role: 'ADMIN' as 'OWNER' | 'ADMIN', all_stores: false, store_ids: [] as string[] });

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
		if (form.role === 'OWNER' && !confirm(`Create ${form.login} as OWNER? Owners can administer accounts and security.`)) return;
		void act(async () => {
			const r = await usersApi.create({ ...form, store_ids: form.all_stores ? [] : form.store_ids });
			activation = { login: r.user.login, link: activationLink(r.activation_token) };
			form = { login: '', display_name: '', role: 'ADMIN', all_stores: false, store_ids: [] };
			await fetchUsers(true);
		});
	}

	function setStatus(u: UserView, status: 'ACTIVE' | 'DISABLED') {
		const verb = status === 'DISABLED' ? 'Disable' : 'Re-enable';
		if (!confirm(`${verb} ${u.login}? ${status === 'DISABLED' ? 'All of their sessions end immediately.' : ''}`)) return;
		void act(async () => {
			await usersApi.action(u.id, 'status', { status });
			await fetchUsers(true);
		});
	}

	function setRole(u: UserView, role: 'OWNER' | 'ADMIN') {
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
		if (u.all_stores) return 'كل المتاجر / All Stores';
		return u.store_ids.map((id) => stores.find((s) => s.store_id === id)?.display_name ?? id.slice(0, 8)).join('، ');
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
	<h2>المستخدمون والأمان / Users & Security</h2>
	{#if notice}<p class="notice {notice.kind}" role="status" data-testid="users-notice">{notice.text}</p>{/if}

	<fieldset>
		<legend>حسابي / My account — {me.user.display_name} ({me.user.role})</legend>
		<form class="row" onsubmit={changePassword}>
			<label>كلمة المرور الحالية / Current password<input type="password" autocomplete="current-password" bind:value={current} required /></label>
			<label
				>كلمة المرور الجديدة / New password<input type="password" autocomplete="new-password" minlength="12" bind:value={next} required /></label
			>
			<button type="submit" disabled={busy}>تغيير / Change</button>
		</form>
		<button type="button" onclick={regenerateCodes} disabled={busy} data-testid="regenerate-codes">
			إعادة إنشاء رموز الاسترداد / Regenerate recovery codes
		</button>
		{#if codes.length}
			<p class="muted">Shown once — store them safely:</p>
			<ol class="codes" dir="ltr">{#each codes as c (c)}<li><code>{c}</code></li>{/each}</ol>
		{/if}
	</fieldset>

	{#if canManage}
		<fieldset>
			<legend>إضافة مستخدم / Add user</legend>
			<form class="row" onsubmit={createUser} data-testid="create-user">
				<label>البريد أو اسم الدخول / Login<input bind:value={form.login} required /></label>
				<label>الاسم / Display name<input bind:value={form.display_name} required /></label>
				<label>
					الدور / Role
					<select bind:value={form.role}>
						<option value="ADMIN">مسؤول / ADMIN</option>
						{#if me.all_stores}<option value="OWNER">مالك / OWNER</option>{/if}
					</select>
				</label>
				{#if me.all_stores}
					<label class="check"><input type="checkbox" bind:checked={form.all_stores} /> كل المتاجر / All Stores</label>
				{/if}
				{#if !form.all_stores}
					<label>
						المتاجر / Stores
						<select multiple bind:value={form.store_ids} required>
							{#each stores as s (s.store_id)}<option value={s.store_id}>{s.display_name}</option>{/each}
						</select>
					</label>
				{/if}
				<button type="submit" disabled={busy}>إنشاء / Create</button>
			</form>
			{#if activation}
				<div class="activation" data-testid="activation-link">
					<p>
						رابط التفعيل لمرة واحدة لـ {activation.login} (صالح 24 ساعة). أرسله بطريقة آمنة؛ لن يظهر مجددًا. / One-time activation link for
						{activation.login} (valid 24 h). Send it securely; it will not be shown again.
					</p>
					<input readonly value={activation.link} dir="ltr" onfocus={(e) => (e.currentTarget as HTMLInputElement).select()} />
				</div>
			{/if}
		</fieldset>
	{/if}

	{#if canRead}
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
								{#if u.status === 'ACTIVE'}<button onclick={() => setStatus(u, 'DISABLED')}>تعطيل / Disable</button>{/if}
								{#if u.status === 'DISABLED'}<button onclick={() => setStatus(u, 'ACTIVE')}>تفعيل / Enable</button>{/if}
								{#if u.status === 'PENDING_SETUP'}<button onclick={() => reissue(u)}>رابط جديد / New link</button>{/if}
								{#if u.role === 'ADMIN' && me.all_stores}<button onclick={() => setRole(u, 'OWNER')}>ترقية / Make owner</button>{/if}
								{#if u.role === 'OWNER'}<button onclick={() => setRole(u, 'ADMIN')}>خفض / Make admin</button>{/if}
								{#if canSecurity && u.mfa_enabled}<button onclick={() => resetMFA(u)}>إعادة MFA / Reset MFA</button>{/if}
								<button onclick={() => revokeSessions(u)}>تسجيل الخروج / Sign out</button>
							{/if}
						</td>
					</tr>
				{/each}
			</tbody>
		</table>
		{#if cursor}<button onclick={() => loadUsers(false)} disabled={busy}>المزيد / More</button>{/if}
	{/if}

	{#if audit.length}
		<h3>سجل الأمان / Security audit (latest 50)</h3>
		<ul class="audit">
			{#each audit as a (a.id)}
				<li><span class="muted" dir="ltr">{new Date(a.occurred_at).toLocaleString()}</span> {a.action} · {a.outcome}{a.reason ? ` · ${a.reason}` : ''}</li>
			{/each}
		</ul>
	{/if}
</section>

<style>
	.users { display: flex; flex-direction: column; gap: 0.75rem; }
	fieldset { display: flex; flex-direction: column; gap: 0.5rem; }
	.row { display: flex; flex-wrap: wrap; gap: 0.5rem; align-items: end; }
	.row label { display: flex; flex-direction: column; gap: 4px; }
	.check { flex-direction: row !important; align-items: center; }
	.notice { font-weight: bold; }
	.codes { columns: 2; font-family: monospace; }
	.activation input { width: 100%; font-family: monospace; }
	.actions { display: flex; flex-wrap: wrap; gap: 0.3rem; }
	.muted { opacity: 0.75; }
	.audit { font-size: 0.85rem; max-height: 320px; overflow: auto; }
	table.data { width: 100%; border-collapse: collapse; display: block; overflow-x: auto; }
	table.data th, table.data td { text-align: start; padding: 0.3rem 0.5rem; }
</style>
