<script lang="ts">
	import { onMount } from 'svelte';
	import { authApi, authErrorText, type AuthStage } from '../lib/api.js';

	// Human sign-in (ADR-0053): password -> MFA (verify or first-time
	// enrollment) -> full session. Activation of a new account arrives as
	// /dashboard/#activate=<token> (a URL fragment never reaches the server or
	// Referer headers). Generic errors only: nothing reveals whether an
	// account exists, is disabled or has MFA before primary authentication.
	let { stage = null, onauthenticated }: { stage?: AuthStage | null; onauthenticated: () => void } = $props();

	type View = 'login' | 'activate' | 'mfa' | 'setup' | 'codes';
	let view = $state<View>('login');
	let login = $state('');
	let password = $state('');
	let password2 = $state('');
	let code = $state('');
	let useRecovery = $state(false);
	let activationToken = $state('');
	let enrollment = $state<{ secret: string; otpauth_uri: string } | null>(null);
	let recoveryCodes = $state<string[]>([]);
	let saved = $state(false);
	let error = $state<string | null>(null);
	let busy = $state(false);

	function grouped(secret: string): string {
		return secret.replace(/(.{4})/g, '$1 ').trim();
	}

	async function enterStage(s: AuthStage) {
		if (s === 'FULL') {
			onauthenticated();
			return;
		}
		code = '';
		if (s === 'MFA_PENDING') {
			view = 'mfa';
			return;
		}
		view = 'setup';
		try {
			const e = await authApi.enrollStart();
			enrollment = { secret: e.secret, otpauth_uri: e.otpauth_uri };
		} catch (err) {
			error = authErrorText(err);
		}
	}

	async function run(fn: () => Promise<void>) {
		if (busy) return;
		busy = true;
		error = null;
		try {
			await fn();
		} catch (err) {
			error = authErrorText(err);
		} finally {
			busy = false;
		}
	}

	function submitLogin(e: Event) {
		e.preventDefault();
		void run(async () => {
			try {
				const r = await authApi.login(login, password);
				await enterStage(r.stage);
			} finally {
				password = '';
			}
		});
	}

	function submitActivation(e: Event) {
		e.preventDefault();
		if (password !== password2) {
			error = 'كلمتا المرور غير متطابقتين. / Passwords do not match.';
			return;
		}
		void run(async () => {
			try {
				const r = await authApi.activate(activationToken, password);
				activationToken = '';
				await enterStage(r.stage);
			} finally {
				password = '';
				password2 = '';
			}
		});
	}

	function submitMFA(e: Event) {
		e.preventDefault();
		void run(async () => {
			const r = useRecovery ? await authApi.verifyRecovery(code) : await authApi.verifyTOTP(code);
			code = '';
			await enterStage(r.stage);
		});
	}

	function submitSetup(e: Event) {
		e.preventDefault();
		void run(async () => {
			const r = await authApi.enrollConfirm(code);
			code = '';
			enrollment = null;
			recoveryCodes = r.recovery_codes ?? [];
			view = 'codes';
		});
	}

	onMount(() => {
		const hash = new URLSearchParams(window.location.hash.replace(/^#/, ''));
		const token = hash.get('activate');
		if (token) {
			activationToken = token;
			view = 'activate';
			// Drop the one-time token from the address bar and history.
			window.history.replaceState({}, '', window.location.pathname + window.location.search);
		} else if (stage) {
			void enterStage(stage);
		}
	});
</script>

<div class="loginwrap">
	<div class="card login" data-testid="auth-card">
		{#if view === 'login'}
			<form onsubmit={submitLogin} data-testid="login-form">
				<h1>تسجيل الدخول</h1>
				<p class="muted">Sign in</p>
				<label>البريد أو اسم الدخول / Email or login<input name="login" autocomplete="username" bind:value={login} required /></label>
				<label
					>كلمة المرور / Password<input name="password" type="password" autocomplete="current-password" bind:value={password} required /></label
				>
				{#if error}<div class="err" role="alert">{error}</div>{/if}
				<button type="submit" disabled={busy}>{busy ? '…' : 'دخول / Sign in'}</button>
			</form>
		{:else if view === 'activate'}
			<form onsubmit={submitActivation} data-testid="activate-form">
				<h1>تفعيل الحساب</h1>
				<p class="muted">Activate your account — choose a password (at least 12 characters).</p>
				<label
					>كلمة المرور / Password<input type="password" autocomplete="new-password" minlength="12" bind:value={password} required /></label
				>
				<label
					>تأكيد كلمة المرور / Repeat password<input type="password" autocomplete="new-password" minlength="12" bind:value={password2} required /></label
				>
				{#if error}<div class="err" role="alert">{error}</div>{/if}
				<button type="submit" disabled={busy}>{busy ? '…' : 'تفعيل / Activate'}</button>
			</form>
		{:else if view === 'mfa'}
			<form onsubmit={submitMFA} data-testid="mfa-form">
				<h1>التحقق بخطوتين</h1>
				<p class="muted">Two-step verification</p>
				{#if useRecovery}
					<label>رمز الاسترداد / Recovery code<input autocomplete="one-time-code" bind:value={code} required /></label>
				{:else}
					<label
						>رمز التطبيق / Authenticator code<input
							inputmode="numeric"
							autocomplete="one-time-code"
							pattern="[0-9 ]*"
							maxlength="7"
							bind:value={code}
							required
						/></label
					>
				{/if}
				{#if error}<div class="err" role="alert">{error}</div>{/if}
				<button type="submit" disabled={busy}>{busy ? '…' : 'تحقق / Verify'}</button>
				<button type="button" class="link" onclick={() => ((useRecovery = !useRecovery), (code = ''), (error = null))}>
					{useRecovery ? 'استخدام رمز التطبيق / Use authenticator code' : 'استخدام رمز استرداد / Use a recovery code'}
				</button>
			</form>
		{:else if view === 'setup'}
			<form onsubmit={submitSetup} data-testid="setup-form">
				<h1>إعداد التحقق بخطوتين</h1>
				<p class="muted">
					Two-step verification is required for every account. Add this key to an authenticator app (manual entry), then enter the
					6-digit code.
				</p>
				{#if enrollment}
					<div class="secret" dir="ltr" data-testid="totp-secret">{grouped(enrollment.secret)}</div>
					<a class="muted small" href={enrollment.otpauth_uri} dir="ltr">otpauth:// — فتح في تطبيق المصادقة / open in authenticator</a>
				{/if}
				<label
					>الرمز / Code<input inputmode="numeric" autocomplete="one-time-code" pattern="[0-9 ]*" maxlength="7" bind:value={code} required /></label
				>
				{#if error}<div class="err" role="alert">{error}</div>{/if}
				<button type="submit" disabled={busy || !enrollment}>{busy ? '…' : 'تأكيد / Confirm'}</button>
			</form>
		{:else}
			<div data-testid="recovery-codes">
				<h1>رموز الاسترداد</h1>
				<p class="muted">
					Save these one-time recovery codes somewhere safe. Each works once if you lose your authenticator. They will not be shown
					again.
				</p>
				<ol class="codes" dir="ltr">
					{#each recoveryCodes as c (c)}<li><code>{c}</code></li>{/each}
				</ol>
				<label class="check"><input type="checkbox" bind:checked={saved} /> حفظت الرموز / I have saved these codes</label>
				<button type="button" disabled={!saved} onclick={() => ((recoveryCodes = []), onauthenticated())}>متابعة / Continue</button>
			</div>
		{/if}
	</div>
</div>

<style>
	.loginwrap {
		min-height: 100vh;
		display: flex;
		align-items: center;
		justify-content: center;
		background: var(--background);
		padding: 16px;
	}
	.login {
		width: min(400px, 100%);
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
	.login label.check {
		flex-direction: row;
		align-items: center;
	}
	.login input:not([type='checkbox']) {
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
	.login button:disabled {
		opacity: 0.6;
	}
	.login button.link {
		background: none;
		color: inherit;
		text-decoration: underline;
		margin-top: 8px;
	}
	.secret {
		margin-top: 12px;
		font-family: monospace;
		font-size: 1.05rem;
		letter-spacing: 0.04em;
		word-break: break-all;
		padding: 8px;
		border: 1px dashed var(--border);
		border-radius: 6px;
	}
	.small {
		font-size: 0.8rem;
		display: block;
		margin-top: 6px;
		word-break: break-all;
	}
	.codes {
		columns: 2;
		font-family: monospace;
	}
	.err {
		margin-top: 10px;
		color: var(--color-danger);
		font-size: 0.9rem;
	}
</style>
