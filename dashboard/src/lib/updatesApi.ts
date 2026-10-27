import { tb } from './i18n.svelte.js';
import { get, postJson, q } from './http';

// Phase 18 release registry + fleet rollout control (ADR-0051/0052).
// The dashboard never invents release identity: imports send the signed
// envelope exactly as produced by release tooling plus artifact URLs.

export interface ReleaseArtifact {
	os: string;
	arch: string;
	package: string;
	file_name: string;
	size: number;
	sha256: string;
	url: string;
}

export interface ReleaseView {
	id: string;
	manifest_digest: string;
	release_sequence: number;
	version: string;
	build_commit: string;
	min_installed_sequence: number;
	key_id: string;
	status: 'active' | 'revoked';
	imported_by: string;
	imported_at: string;
	status_changed_by: string;
	status_changed_at: string;
	artifacts: ReleaseArtifact[];
}

export interface RolloutView {
	id: string;
	release_id: string;
	release_version: string;
	release_sequence: number;
	scope: 'device' | 'store' | 'all';
	store_id?: string;
	device_id?: string;
	mode: 'optional' | 'mandatory';
	percentage: number;
	status: 'draft' | 'active' | 'paused' | 'completed' | 'cancelled';
	not_before?: string;
	target_count: number;
	created_by: string;
	created_at: string;
	updated_at: string;
	counts?: Record<string, number>;
}

export interface UpdateTargetView {
	id: string;
	rollout_id: string;
	device_id: string;
	device_name: string;
	store_id: string;
	state: string;
	attempt_count: number;
	next_attempt_at?: string;
	last_error?: string;
	delivered_at?: string;
	finished_at?: string;
	updated_at: string;
}

export interface FleetDeviceRow {
	device_id: string;
	device_name: string;
	device_status: string;
	store_id: string;
	last_seen_at?: string;
	version?: string;
	build_commit?: string;
	release_sequence: number;
	os?: string;
	arch?: string;
	updater_protocol: number;
	updater_capable: boolean;
	updater_reported: boolean;
	unsupported_reason?: string;
	update_state?: string;
	update_error?: string;
	reported_at?: string;
	target_id?: string;
	target_state?: string;
	target_error?: string;
	target_version?: string;
	target_sequence?: number;
	rollout_id?: string;
}

export interface CreateRolloutRequest {
	release_id: string;
	scope: 'device' | 'store' | 'all';
	store_id?: string;
	device_id?: string;
	mode: 'optional' | 'mandatory';
	percentage: number;
	not_before?: string;
}


export const updatesApi = {
	releases: (s?: AbortSignal) => get<{ releases: ReleaseView[] }>('/api/v1/dashboard/releases', s),
	importRelease: (envelope: string, artifactURLs: Record<string, string>) =>
		postJson<ReleaseView>('/api/v1/dashboard/releases', { envelope, artifact_urls: artifactURLs }),
	setReleaseStatus: (id: string, status: 'active' | 'revoked') =>
		postJson<ReleaseView>(`/api/v1/dashboard/releases/${encodeURIComponent(id)}/status`, { status }),
	rollouts: (storeID: string, cursor?: string, s?: AbortSignal) =>
		get<{ rollouts: RolloutView[]; next_cursor: string }>(`/api/v1/dashboard/rollouts${q({ store_id: storeID, cursor })}`, s),
	createRollout: (req: CreateRolloutRequest) => postJson<RolloutView>('/api/v1/dashboard/rollouts', req),
	rolloutAction: (id: string, action: 'start' | 'pause' | 'resume' | 'cancel') =>
		postJson<RolloutView>(`/api/v1/dashboard/rollouts/${encodeURIComponent(id)}/${action}`, {}),
	widenRollout: (id: string, percentage: number) =>
		postJson<RolloutView>(`/api/v1/dashboard/rollouts/${encodeURIComponent(id)}/percentage`, { percentage }),
	targets: (id: string, storeID: string, cursor?: string, s?: AbortSignal) =>
		get<{ targets: UpdateTargetView[]; next_cursor: string }>(
			`/api/v1/dashboard/rollouts/${encodeURIComponent(id)}/targets${q({ store_id: storeID, cursor })}`, s
		),
	fleet: (storeID: string, cursor?: string, s?: AbortSignal) =>
		get<{ devices: FleetDeviceRow[]; next_cursor: string }>(`/api/v1/dashboard/fleet${q({ store_id: storeID, cursor })}`, s)
};

// Truthful operator-facing target labels (prompt §60): "Succeeded" only
// after Retail itself reports success.
export function updateStateLabel(state: string | undefined): string {
	switch (state) {
		case undefined:
		case '':
			return tb('غير مستهدف', 'Not targeted');
		case 'not_selected':
			return tb('خارج المرحلة الحالية', 'Not in current stage');
		case 'pending':
			return tb('بانتظار التسليم', 'Pending');
		case 'delivered':
			return tb('تم التسليم', 'Delivered');
		case 'downloading':
			return tb('جارٍ التنزيل', 'Downloading');
		case 'verified':
			return tb('تم التحقق', 'Verified');
		case 'waiting_safe_boundary':
			return tb('بانتظار نقطة آمنة', 'Waiting for safe boundary');
		case 'installing':
			return tb('جارٍ التثبيت', 'Installing');
		case 'awaiting_health':
			return tb('فحص التشغيل', 'Checking health');
		case 'succeeded':
			return tb('تم التحديث', 'Succeeded');
		case 'already_compliant':
			return tb('محدَّث مسبقًا', 'Already compliant');
		case 'failed':
			return tb('فشل', 'Failed');
		case 'rolled_back':
			return tb('تم التراجع', 'Rolled back');
		case 'manual_action_required':
			return tb('يتطلب تدخلًا يدويًا', 'Manual action required');
		case 'skipped_newer':
			return tb('إصدار أحدث مثبت', 'Skipped (newer installed)');
		case 'unsupported':
			return tb('غير مدعوم', 'Unsupported');
		case 'cancelled':
			return tb('أُلغي', 'Cancelled');
		default:
			return state;
	}
}

// Decodes the signed manifest for DISPLAY ONLY (artifact file names for
// URL entry). The server verifies the signature; nothing here is trusted.
export function previewEnvelope(text: string): { version: string; sequence: number; files: string[] } | null {
	try {
		const env = JSON.parse(text) as { manifest?: string };
		if (!env.manifest) return null;
		const manifest = JSON.parse(atob(env.manifest)) as {
			version: string;
			release_sequence: number;
			artifacts: { file_name: string }[];
		};
		return { version: manifest.version, sequence: manifest.release_sequence, files: manifest.artifacts.map((a) => a.file_name) };
	} catch {
		return null;
	}
}

