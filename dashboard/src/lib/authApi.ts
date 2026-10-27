import { tb } from './i18n.svelte.js';
import { ApiError, get, mutationHeaders, postJson, setCSRFToken } from './http';

// Phase 18 R1 human authentication & user administration (ADR-0053).
// Every response carrying a session also carries the CSRF token, kept in
// memory only. Nothing here is persisted in browser storage.

export type AuthStage = 'mfa_pending' | 'mfa_setup' | 'full';

export interface Me {
	authenticated: boolean;
	stage: AuthStage;
	csrf_token: string;
	user: { id: string; login: string; display_name: string; role: 'owner' | 'admin'; mfa_enabled: boolean };
	permissions: string[];
	all_stores: boolean;
	store_ids: string[];
	session: { expires_at: string; idle_timeout_seconds: number };
}

export interface SessionResult {
	stage: AuthStage;
	csrf_token: string;
	recovery_codes?: string[];
}

export interface UserView {
	id: string;
	login: string;
	display_name: string;
	role: 'owner' | 'admin';
	status: 'pending_setup' | 'active' | 'disabled';
	all_stores: boolean;
	store_ids: string[];
	mfa_enabled: boolean;
	created_at: string;
	updated_at: string;
}

async function authPost<T extends { csrf_token?: string }>(path: string, body: unknown): Promise<T> {
	const res = await fetch(path, {
		method: 'POST',
		headers: mutationHeaders({ 'content-type': 'application/json' }),
		credentials: 'same-origin',
		body: JSON.stringify(body)
	});
	let parsed: { error?: { code?: string; message?: string } } & T;
	try {
		parsed = await res.json();
	} catch {
		throw new ApiError(res.status, 'INTERNAL', `request failed (${res.status})`);
	}
	if (!res.ok) {
		throw new ApiError(res.status, parsed.error?.code ?? 'INTERNAL', `request failed (${res.status})`, parsed.error?.message ?? '');
	}
	if (parsed.csrf_token) setCSRFToken(parsed.csrf_token);
	return parsed;
}

export const authApi = {
	login: (login: string, password: string) => authPost<SessionResult>('/api/v1/dashboard/auth/login', { login, password }),
	activate: (token: string, password: string) => authPost<SessionResult>('/api/v1/dashboard/auth/activate', { token, password }),
	verifyTOTP: (code: string) => authPost<SessionResult>('/api/v1/dashboard/auth/mfa/verify', { code }),
	verifyRecovery: (recovery_code: string) => authPost<SessionResult>('/api/v1/dashboard/auth/mfa/verify', { recovery_code }),
	enrollStart: () => authPost<{ secret: string; otpauth_uri: string; csrf_token?: string }>('/api/v1/dashboard/auth/mfa/enroll/start', {}),
	enrollConfirm: (code: string) => authPost<SessionResult>('/api/v1/dashboard/auth/mfa/enroll/confirm', { code }),
	regenerateRecoveryCodes: () => authPost<{ recovery_codes: string[]; csrf_token?: string }>('/api/v1/dashboard/auth/mfa/recovery-codes', {}),
	changePassword: (current_password: string, new_password: string) =>
		authPost<SessionResult>('/api/v1/dashboard/auth/password', { current_password, new_password })
};

export type UserAction = 'activation' | 'role' | 'status' | 'memberships' | 'mfa-reset' | 'sessions-revoke';

export const usersApi = {
	list: (after = '', s?: AbortSignal) =>
		get<{ users: UserView[]; next_cursor: string }>(`/api/v1/dashboard/users${after ? `?after=${encodeURIComponent(after)}` : ''}`, s),
	create: (req: { login: string; display_name: string; role: 'owner' | 'admin'; all_stores: boolean; store_ids: string[] }) =>
		postJson<{ user: UserView; activation_token: string }>('/api/v1/dashboard/users', req),
	action: <T = UserView>(id: string, action: UserAction, body: Record<string, unknown> = {}) =>
		postJson<T>(`/api/v1/dashboard/users/${encodeURIComponent(id)}/${action}`, body),
	audit: (s?: AbortSignal) =>
		get<{ events: AuthAuditRow[]; next_before: number }>('/api/v1/dashboard/auth-audit?limit=50', s)
};

export interface AuthAuditRow {
	id: number;
	occurred_at: string;
	action: string;
	outcome: string;
	reason?: string;
	actor_user_id?: string;
	target_user_id?: string;
}

// Stable, human-readable auth error text. Generic by design: never reveals
// whether an account exists, is disabled or has MFA before primary auth.
export function authErrorText(err: unknown): string {
	const reason = err instanceof ApiError ? err.detail || err.code : '';
	switch (reason) {
		case 'INVALID_CREDENTIALS':
			return tb('البريد أو كلمة المرور غير صحيحة.', 'Invalid email or password.');
		case 'RATE_LIMITED':
			return tb('محاولات كثيرة — حاول لاحقًا.', 'Too many attempts, try again later.');
		case 'INVALID_MFA_CODE':
			return tb('رمز التحقق غير صحيح.', 'Invalid verification code.');
		case 'WEAK_PASSWORD':
			return tb('كلمة المرور يجب أن تكون 12 حرفًا على الأقل.', 'Password must be at least 12 characters.');
		case 'ACTIVATION_INVALID':
			return tb('رابط التفعيل غير صالح أو منتهي.', 'Activation link is invalid or expired.');
		case 'AUTH_REQUIRED':
		case 'UNAUTHORIZED':
			return tb('انتهت الجلسة — سجّل الدخول مجددًا.', 'Session expired, please sign in again.');
		case 'FORBIDDEN':
		case 'STORE_FORBIDDEN':
			return tb('غير مصرح لك.', 'Not authorized.');
		case 'LAST_OWNER':
			return tb('يجب أن يبقى مالك نشط واحد على الأقل.', 'At least one active owner must remain.');
		case 'LOGIN_TAKEN':
			return tb('اسم الدخول مستخدم.', 'That login already exists.');
		default:
			return tb('تعذر إكمال العملية.', 'The operation could not be completed.');
	}
}

