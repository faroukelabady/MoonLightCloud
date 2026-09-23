<script lang="ts">
	import { dashboardApi, ApiError } from '../lib/api.js';

	let { onlogin }: { onlogin: () => void } = $props();

	let username = $state('');
	let password = $state('');
	let error = $state<string | null>(null);
	let busy = $state(false);

	async function submit(e: Event) {
		e.preventDefault();
		if (busy) return;
		busy = true;
		error = null;
		try {
			await dashboardApi.login(username, password);
			onlogin();
		} catch (err) {
			error =
				err instanceof ApiError && err.status === 429
					? 'محاولات كثيرة — حاول لاحقًا / Too many attempts, try later'
					: 'بيانات الدخول غير صحيحة / Invalid operator credentials';
		} finally {
			busy = false;
			password = '';
		}
	}
</script>

<div class="loginwrap">
	<form class="card login" onsubmit={submit}>
		<h1>تسجيل الدخول</h1>
		<p class="muted">Operator login</p>
		<label>اسم المستخدم / Username<input name="username" autocomplete="username" bind:value={username} required /></label>
		<label>كلمة المرور / Password<input name="password" type="password" autocomplete="current-password" bind:value={password} required /></label>
		{#if error}<div class="err" role="alert">{error}</div>{/if}
		<button type="submit" disabled={busy}>{busy ? '...' : 'دخول / Login'}</button>
	</form>
</div>

<style>
	.loginwrap {
		min-height: 100vh;
		display: flex;
		align-items: center;
		justify-content: center;
		background: var(--background);
	}
	.login {
		width: min(360px, 90vw);
	}
	.login h1 {
		margin: 0;
	}
	.login label {
		display: flex;
		flex-direction: column;
		gap: 4px;
		margin-top: 12px;
		font-size: 0.9rem;
	}
	.login input {
		padding: 8px 10px;
		border: 1px solid var(--border);
		border-radius: 6px;
	}
	.login button {
		margin-top: 14px;
		width: 100%;
		background: var(--color-primary);
		color: #fff;
		border: 0;
		border-radius: 6px;
		padding: 9px;
	}
	.err {
		margin-top: 10px;
		color: var(--color-danger);
		font-size: 0.9rem;
	}
</style>
