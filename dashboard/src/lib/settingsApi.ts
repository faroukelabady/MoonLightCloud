// Business-wide settings: report toggles, Cloud exchange rates and report
// delivery over WhatsApp/Telegram. Provider credentials never reach here.
import { get, sendJson } from './http.js';
import type { BusinessSettings } from './reportsApi.js';

export interface ExchangeRate {
	id: string;
	rate: string;
	rate_microrate: number;
	effective_at: string;
	note: string;
	created_at: string;
}

export interface MessagingTemplate {
	template_key: 'daily_business_report_v1' | 'ten_day_business_report_v1';
	locale: 'ar' | 'en';
	external_name: string;
	language: string;
	params: string[];
	enabled: boolean;
}

export interface MessagingProvider {
	key: string;
	kind: 'whatsapp' | 'telegram';
	templates: MessagingTemplate[];
}

export interface MessagingRecipient {
	id: string;
	label: string;
	provider_key: string;
	recipient_masked: string;
	locale: 'ar' | 'en';
	enabled: boolean;
}

export interface MessagingSchedule {
	id: string;
	name: string;
	kind: 'daily' | 'ten_day';
	local_time: string;
	anchor_local_date: string;
	enabled: boolean;
	next_run_at: string;
	recipient_ids: string[];
}

export interface MessagingRun {
	id: string;
	schedule_id: string;
	kind: 'scheduled' | 'manual';
	status: 'pending' | 'retry' | 'completed' | 'blocked';
	scheduled_for: string;
	period_start: string;
	period_end: string;
	attempt_count: number;
	last_error_code: string;
}

export interface Messaging {
	scheduler_enabled: boolean;
	providers: MessagingProvider[];
	recipients: MessagingRecipient[];
	schedules: MessagingSchedule[];
	runs: MessagingRun[];
}

const base = '/api/v1/dashboard/settings';

export const settingsApi = {
	get: (s?: AbortSignal) => get<{ business: BusinessSettings; exchange_rates: ExchangeRate[] }>(base, s),
	saveBusiness: (b: Pick<BusinessSettings, 'tax_enabled' | 'discount_enabled' | 'returns_enabled' | 'revision'>) =>
		sendJson<BusinessSettings>('PUT', `${base}/business`, b),
	addRate: (r: { rate: string; effective_at?: string; note?: string }) => sendJson<ExchangeRate>('POST', `${base}/exchange-rates`, r),
	deleteRate: (id: string) => sendJson<{ deleted: boolean }>('DELETE', `${base}/exchange-rates/${encodeURIComponent(id)}`, {}),
	messaging: (s?: AbortSignal) => get<Messaging>(`${base}/messaging`, s),
	addRecipient: (r: { label: string; provider_key: string; recipient: string; locale: string }) =>
		sendJson<MessagingRecipient>('POST', `${base}/messaging/recipients`, r),
	setRecipient: (id: string, enabled: boolean) =>
		sendJson<{ enabled: boolean }>('POST', `${base}/messaging/recipients/${encodeURIComponent(id)}/${enabled ? 'enable' : 'disable'}`, {}),
	addSchedule: (s: { name: string; kind: string; local_time: string; anchor_local_date: string; recipient_ids: string[] }) =>
		sendJson<MessagingSchedule>('POST', `${base}/messaging/schedules`, s),
	setSchedule: (id: string, enabled: boolean) =>
		sendJson<{ enabled: boolean }>('POST', `${base}/messaging/schedules/${encodeURIComponent(id)}/${enabled ? 'enable' : 'disable'}`, {}),
	runSchedule: (id: string, requestKey: string) =>
		sendJson<{ run_id: string; created: boolean; scheduler_enabled: boolean }>('POST', `${base}/messaging/schedules/${encodeURIComponent(id)}/run`, { request_key: requestKey }),
	setTemplate: (t: MessagingTemplate & { provider_key: string }) => sendJson<MessagingTemplate>('PUT', `${base}/messaging/templates`, t)
};
