// Per-component async ownership. Abort reduces work; current() also rejects
// already-resolved or non-cancellable responses from an obsolete UI lifetime.
export function createRequestScope<Channel extends string>() {
	let generation = 0;
	let disposed = false;
	const channels = new Map<Channel, AbortController>();
	function cancel(channel: Channel) {
		channels.get(channel)?.abort();
		channels.delete(channel);
	}
	function invalidate() {
		generation += 1;
		for (const controller of channels.values()) controller.abort();
		channels.clear();
	}
	return {
		begin(channel: Channel) {
			cancel(channel);
			const controller = new AbortController();
			channels.set(channel, controller);
			const parent = generation;
			return { signal: controller.signal, current: () => !disposed && parent === generation && channels.get(channel) === controller };
		},
		capture(channel: Channel) {
			const controller = channels.get(channel), parent = generation;
			return () => !disposed && parent === generation && channels.get(channel) === controller;
		},
		cancel,
		invalidate,
		dispose() { invalidate(); disposed = true; },
	};
}
