import echarts from './echarts.js';
import type { EChartsCoreOption } from 'echarts/core';

// Chart lifecycle primitive (M03): every ECharts instance is bound to the
// exact DOM element the action attaches to. When Svelte removes or replaces
// the element (view toggles, conditional blocks), the action's destroy
// disposes that instance; a new element gets a brand-new instance. The old
// `chart ??=` pattern is banned: it reused disposed instances across
// element swaps and left charts blank.
//
// Usage: <div use:chart={option} ...></div> where option is null to render
// nothing. Data updates flow through update() → setOption (no re-init).
export function chart(node: HTMLElement, option: EChartsCoreOption | null) {
	const instance = echarts.init(node);
	const ro = new ResizeObserver(() => {
		if (!instance.isDisposed()) instance.resize();
	});
	ro.observe(node);
	if (option) instance.setOption(option, true);
	return {
		update(next: EChartsCoreOption | null) {
			if (next && !instance.isDisposed()) instance.setOption(next, true);
		},
		destroy() {
			ro.disconnect();
			if (!instance.isDisposed()) instance.dispose();
		}
	};
}
