import { describe, expect, it } from 'vitest';
import { deliveryState, runSendState } from './messagingDelivery.js';
import type { MessagingDelivery, MessagingRun } from './settingsApi.js';
const delivery = (v: Partial<MessagingDelivery> = {}): MessagingDelivery => ({ id: 'd1', provider_key: 'whatsapp-main', provider_kind: 'whatsapp', recipient_label: 'Owner', recipient_masked: '…5678', report_status: 'enqueued', dispatch_status: 'pending', delivery_status: 'unknown', last_error_code: '', attempt_count: 0, send_started: false, updated_at: '', ...v });
const run = (deliveries: MessagingDelivery[]): MessagingRun => ({ id: 'r1', schedule_id: 's1', schedule_name: 'Daily', kind: 'manual', status: 'completed', scheduled_for: '', period_start: '', period_end: '', attempt_count: 1, last_error_code: '', deliveries });
describe('truthful report message status', () => {
	it.each([
		[{}, 'queued'],
		[{ send_started: true }, 'sending'],
		[{ dispatch_status: 'retry' }, 'retrying'],
		[{ dispatch_status: 'blocked' }, 'failed'],
		[{ report_status: 'blocked' }, 'failed'],
		[{ dispatch_status: 'ambiguous' }, 'uncertain'],
		[{ dispatch_status: 'accepted', delivery_status: 'accepted' }, 'accepted'],
		[{ provider_kind: 'telegram', dispatch_status: 'accepted', delivery_status: 'accepted' }, 'sent'],
		[{ dispatch_status: 'accepted', delivery_status: 'sent' }, 'sent'],
		[{ dispatch_status: 'accepted', delivery_status: 'delivered' }, 'delivered'],
		[{ dispatch_status: 'accepted', delivery_status: 'read' }, 'read'],
		[{ dispatch_status: 'accepted', delivery_status: 'failed' }, 'failed'],
	] as const)('maps durable outcome %j to %s', (input, expected) => expect(deliveryState(delivery(input))).toBe(expected));
	it('never turns report completion or an unknown state into successful delivery', () => {
		expect(runSendState(run([]))).toBe('unknown');
		expect(deliveryState(delivery({ dispatch_status: 'unexpected' }))).toBe('unknown');
	});
	it('keeps mixed failures and uncertain outcomes visible', () => {
		const success = delivery({ dispatch_status: 'accepted', delivery_status: 'delivered' });
		expect(runSendState(run([success, delivery({ dispatch_status: 'blocked' })]))).toBe('partial_failure');
		expect(runSendState(run([success, delivery({ dispatch_status: 'ambiguous' })]))).toBe('uncertain');
	});
	it('requires every recipient to have delivery/read confirmation before using those labels', () => {
		const read = delivery({ delivery_status: 'read' });
		expect(runSendState(run([read, delivery({ dispatch_status: 'accepted' })]))).toBe('accepted');
		expect(runSendState(run([read, delivery({ delivery_status: 'delivered' })]))).toBe('delivered');
		expect(runSendState(run([read, read]))).toBe('read');
	});
});
