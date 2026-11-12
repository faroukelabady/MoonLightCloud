import { msg } from './i18n.svelte.js';
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
			return msg('updatesApi.notTargeted');
		case 'not_selected':
			return msg('updatesApi.notInCurrentStage');
		case 'pending':
			return msg('updatesApi.pending');
		case 'delivered':
			return msg('updatesApi.delivered');
		case 'downloading':
			return msg('updatesApi.downloading');
		case 'verified':
			return msg('updatesApi.verified');
		case 'waiting_safe_boundary':
			return msg('updatesApi.waitingForSafeBoundary');
		case 'installing':
			return msg('updatesApi.installing');
		case 'awaiting_health':
			return msg('updatesApi.checkingHealth');
		case 'succeeded':
			return msg('updatesApi.succeeded');
		case 'already_compliant':
			return msg('updatesApi.alreadyCompliant');
		case 'failed':
			return msg('updatesApi.failed');
		case 'rolled_back':
			return msg('updatesApi.rolledBack');
		case 'manual_action_required':
			return msg('updatesApi.manualActionRequired');
		case 'skipped_newer':
			return msg('updatesApi.skippedNewerInstalled');
		case 'unsupported':
			return msg('updatesApi.unsupported');
		case 'cancelled':
			return msg('updatesApi.cancelled');
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


// Reads either the envelope alone or CI's public cloud-import.json. This is
// display/input preparation only; all trust checks remain on the server.
export function readReleaseImportFile(text: string): { envelope: string; urls: Record<string, string> } {
	try {
		const value = JSON.parse(text) as { envelope?: unknown; artifact_urls?: unknown };
		if (value && value.envelope && typeof value.artifact_urls === 'object' && value.artifact_urls !== null && !Array.isArray(value.artifact_urls)) {
			const envelope = typeof value.envelope === 'string' ? value.envelope : JSON.stringify(value.envelope);
			const preview = previewEnvelope(envelope);
			if (!preview) return { envelope: text, urls: {} };
			const input = value.artifact_urls as Record<string, unknown>;
			const urls: Record<string, string> = {};
			for (const name of preview.files) if (typeof input[name] === 'string') urls[name] = input[name];
			return { envelope, urls };
		}
	} catch { /* The existing invalid-envelope preview explains malformed files. */ }
	return { envelope: text, urls: {} };
}
