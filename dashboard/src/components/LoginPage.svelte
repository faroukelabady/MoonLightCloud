<script lang="ts">
	import { arabic, lang, msg, setLang } from '../lib/i18n.svelte.js';
	import { onMount } from 'svelte';
	import { authApi, authErrorText, type AuthStage } from '../lib/api.js';
	import QrCode from './QrCode.svelte';
	import { downloadRecoveryCodes } from '../lib/recoveryCodes.js';

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
	// The key stays hidden behind the QR code until asked for.
	let showKey = $state(false);
	let error = $state<string | null>(null);
	let busy = $state(false);

	function grouped(secret: string): string {
		return secret.replace(/(.{4})/g, '$1 ').trim();
	}

	async function enterStage(s: AuthStage) {
		if (s === 'full') {
			onauthenticated();
			return;
		}
		code = '';
		if (s === 'mfa_pending') {
			view = 'mfa';
			return;
		}
		view = 'setup';
		try {
			const e = await authApi.enrollStart();
			enrollment = { secret: e.secret, otpauth_uri: e.otpauth_uri };
			showKey = false;
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
			error = msg('loginPage.passwordsDoNotMatch');
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
		<button type="button" class="langswitch" lang={arabic() ? 'en' : 'ar'} onclick={() => setLang(lang() === 'ar' ? 'en' : 'ar')}>{arabic() ? 'English' : 'العربية'}</button>
		{#if view === 'login'}
			<form onsubmit={submitLogin} data-testid="login-form">
				<h1>{msg('loginPage.signIn')}</h1>
				<label>{msg('loginPage.emailOrLogin')}<input name="login" autocomplete="username" bind:value={login} required /></label>
				<label
					>{msg('loginPage.password')}<input name="password" type="password" autocomplete="current-password" bind:value={password} required /></label
				>
				{#if error}<div class="err" role="alert">{error}</div>{/if}
				<button type="submit" disabled={busy}>{busy ? '…' : msg('loginPage.signIn2')}</button>
			</form>
		{:else if view === 'activate'}
			<form onsubmit={submitActivation} data-testid="activate-form">
				<h1>{msg('loginPage.activateAccount')}</h1>
				<p class="muted">{msg('loginPage.activateHint')}</p>
				<label
					>{msg('loginPage.password')}<input type="password" autocomplete="new-password" minlength="12" bind:value={password} required /></label
				>
				<label
					>{msg('loginPage.repeatPassword')}<input type="password" autocomplete="new-password" minlength="12" bind:value={password2} required /></label
				>
				{#if error}<div class="err" role="alert">{error}</div>{/if}
				<button type="submit" disabled={busy}>{busy ? '…' : msg('loginPage.activate')}</button>
			</form>
		{:else if view === 'mfa'}
			<form onsubmit={submitMFA} data-testid="mfa-form">
				<h1>{msg('loginPage.twoStepVerification')}</h1>
				{#if useRecovery}
					<label>{msg('loginPage.recoveryCode')}<input autocomplete="one-time-code" bind:value={code} required /></label>
				{:else}
					<label
						>{msg('loginPage.authenticatorCode')}<input
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
				<button type="submit" disabled={busy}>{busy ? '…' : msg('loginPage.verify')}</button>
				<button type="button" class="link" onclick={() => ((useRecovery = !useRecovery), (code = ''), (error = null))}>
					{useRecovery ? msg('loginPage.useAuthenticatorCode') : msg('loginPage.useARecoveryCode')}
				</button>
			</form>
		{:else if view === 'setup'}
			<form onsubmit={submitSetup} data-testid="setup-form">
				<h1>{msg('loginPage.setUpTwoStepVerification')}</h1>
				<p class="muted">
					{msg('loginPage.twoStepVerificationIsRequiredFor')}
				</p>
				{#if enrollment}
					<QrCode value={enrollment.otpauth_uri} label={msg('loginPage.qrCodeForYourAuthenticatorApp')} />
					<button type="button" class="link keytoggle" aria-expanded={showKey} aria-controls="totp-key" onclick={() => (showKey = !showKey)}>
						{showKey ? msg('loginPage.hideKey') : msg('loginPage.cantScanShowTheKey')}
					</button>
					{#if showKey}
						<div id="totp-key">
							<div class="secret" dir="ltr" data-testid="totp-secret">{grouped(enrollment.secret)}</div>
							<a class="muted small" href={enrollment.otpauth_uri} dir="ltr">{msg('loginPage.openInAuthenticator')}</a>
						</div>
					{/if}
				{/if}
				<label
					>{msg('loginPage.code')}<input inputmode="numeric" autocomplete="one-time-code" pattern="[0-9 ]*" maxlength="7" bind:value={code} required /></label
				>
				{#if error}<div class="err" role="alert">{error}</div>{/if}
				<button type="submit" disabled={busy || !enrollment}>{busy ? '…' : msg('loginPage.confirm')}</button>
			</form>
		{:else}
			<div data-testid="recovery-codes">
				<h1>{msg('loginPage.recoveryCodes')}</h1>
				<p class="muted">
					{msg('loginPage.saveTheseOneTimeRecoveryCodes')}
				</p>
				<ol class="codes" dir="ltr">
					{#each recoveryCodes as c (c)}<li><code>{c}</code></li>{/each}
				</ol>
				<button type="button" class="download" data-testid="download-codes" onclick={() => downloadRecoveryCodes(login, recoveryCodes)}>{msg('loginPage.downloadCodes')}</button>
				<label class="check"><input type="checkbox" bind:checked={saved} /> {msg('loginPage.iHaveSavedTheseCodes')}</label>
				<button type="button" disabled={!saved} onclick={() => ((recoveryCodes = []), onauthenticated())}>{msg('loginPage.continue')}</button>
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
	.login button.langswitch {
		display: block;
		width: auto;
		margin: 0 0 8px auto;
		background: transparent;
		color: var(--text);
		border: 1px solid var(--border);
		border-radius: var(--radius-control);
		padding: 3px 10px;
		font-size: 0.8rem;
		font-weight: 400;
		cursor: pointer;
	}
	.login label {
		display: flex;
		flex-direction: column;
		gap: 6px;
		margin-top: 12px;
		font-size: 0.78rem;
		font-weight: 600;
		color: var(--field-label);
	}
	.login label.check {
		flex-direction: row;
		align-items: center;
	}
	.login input:not([type='checkbox']) {
		min-height: 40px;
		font-size: 0.9rem;
	}
	.login button {
		margin-top: 14px;
		width: 100%;
		min-height: 40px;
		background: var(--button-primary);
		color: #fff;
		border: 0;
		border-radius: 8px;
		padding: 9px;
		font-weight: 700;
		box-shadow: 0 5px 14px #096ad433;
	}
	.login button:disabled {
		opacity: 0.5;
	}
	.login button.link {
		background: none;
		color: inherit;
		text-decoration: underline;
		margin-top: 8px;
	}
	.login button.download {
		background: transparent;
		color: var(--color-primary);
		border: 1px solid var(--color-primary);
	}
	.login button.keytoggle {
		display: block;
		margin: 4px auto 0;
		font-size: 0.85rem;
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
