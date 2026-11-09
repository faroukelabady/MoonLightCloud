import type { MessagingDelivery, MessagingRun } from './settingsApi.js';

export type SendState = 'preparing' | 'queued' | 'sending' | 'retrying' | 'accepted' | 'sent' | 'delivered' | 'read' | 'failed' | 'partial_failure' | 'uncertain' | 'unknown';

// A report being completed only means notification handoff succeeded. Read the
// notification result; never turn report completion into delivery confirmation.
export function deliveryState(delivery: MessagingDelivery): SendState {
	if (delivery.report_status === 'blocked' || delivery.delivery_status === 'failed' || delivery.dispatch_status === 'blocked') return 'failed';
	if (delivery.dispatch_status === 'ambiguous') return 'uncertain';
	if (delivery.delivery_status === 'read') return 'read';
	if (delivery.delivery_status === 'delivered') return 'delivered';
	if (delivery.delivery_status === 'sent') return 'sent';
	if (delivery.dispatch_status === 'accepted') return delivery.provider_kind === 'telegram' ? 'sent' : 'accepted';
	if (delivery.dispatch_status === 'retry') return 'retrying';
	if (delivery.dispatch_status === 'pending') return delivery.send_started ? 'sending' : 'queued';
	return delivery.report_status === 'pending' ? 'preparing' : 'unknown';
}

export function runSendState(run: MessagingRun): SendState {
	const states = (run.deliveries ?? []).map(deliveryState);
	if (!states.length) return run.status === 'blocked' ? 'failed' : run.status === 'retry' ? 'retrying' : run.status === 'pending' ? 'preparing' : 'unknown';
	const failed = states.filter((s) => s === 'failed').length;
	if (failed) return failed === states.length ? 'failed' : 'partial_failure';
	if (states.includes('uncertain')) return 'uncertain';
	if (states.every((s) => s === 'read')) return 'read';
	if (states.every((s) => s === 'delivered' || s === 'read')) return 'delivered';
	if (states.every(sendSucceeded)) return states.includes('accepted') ? 'accepted' : 'sent';
	if (states.includes('retrying')) return 'retrying';
	if (states.includes('sending')) return 'sending';
	if (states.includes('unknown')) return 'unknown';
	return states.includes('preparing') ? 'preparing' : 'queued';
}

export function sendSucceeded(state: SendState): boolean {
	return ['accepted', 'sent', 'delivered', 'read'].includes(state);
}
export function sendFailed(state: SendState): boolean {
	return state === 'failed' || state === 'partial_failure';
}
