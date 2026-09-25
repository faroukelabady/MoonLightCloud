<script lang="ts">
	import Card from './Card.svelte';
	import Skeleton from './Skeleton.svelte';
	import EmptyState from './EmptyState.svelte';
	import WidgetError from './WidgetError.svelte';
	import { relativeTime } from '../lib/time.js';
	import type { ActivityItem } from '../lib/api.js';

	let { items, status, errStatus, onretry }: { items: ActivityItem[]; status: 'loading' | 'loaded' | 'empty' | 'error'; errStatus: number | null; onretry: () => void } = $props();

	function kindLabel(k: string): [string, string, string] {
		if (k === 'blocked') return ['محظور', 'blocked', 'bad'];
		if (k === 'projected') return ['تمت المعالجة', 'projected', 'ok'];
		if (k === 'return_blocked') return ['مرتجع محظور', 'return blocked', 'bad'];
		if (k === 'return_projected') return ['تمت معالجة مرتجع', 'return projected', 'ok'];
		if (k === 'return_accepted') return ['تم استلام مرتجع', 'return accepted', ''];
		return ['تم الاستلام', 'accepted', ''];
	}

	function iconOf(kind: string): string {
		if (kind === 'blocked' || kind === 'return_blocked') return 'M12 8v5M12 16.5v.5M10.3 3.8L2.6 17a2 2 0 0 0 1.7 3h15.4a2 2 0 0 0 1.7-3L13.7 3.8a2 2 0 0 0-3.4 0z';
		if (kind === 'projected' || kind === 'return_projected') return 'M4 12.5l5 5L20 6.5';
		return 'M6 3h12v18l-3-2-3 2-3-2-3 2V3zM9 8h6M9 12h6';
	}
</script>

<Card ar="الأنشطة الأخيرة" en="Recent activities">
	{#if status === 'loading'}
		<Skeleton />
	{:else if status === 'error'}
		<WidgetError status={errStatus} {onretry} />
	{:else if items.length === 0}
		<EmptyState ar="لا توجد أنشطة بعد" en="No activities yet" />
	{:else}
		<ul class="feed">
			{#each items as it}
				{@const [ar, , cls] = kindLabel(it.kind)}
				<li>
					<span class="aic {cls}" aria-hidden="true"><svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d={iconOf(it.kind)} /></svg></span>
					<span class="tx">
						<span class="what num">{it.event_type}</span>
						{#if it.device_name}<span class="muted dev">{it.device_name}</span>{/if}
						{#if it.detail}<span class="muted num det">{it.detail}</span>{/if}
					</span>
					<span class="side">
						<span class="badge {cls}">{ar}</span>
						<span class="when muted">{relativeTime(it.timestamp, 'ar')}</span>
					</span>
				</li>
			{/each}
		</ul>
	{/if}
</Card>

<style>
	.feed {
		list-style: none;
		margin: 4px 0 0;
		padding: 0;
		display: flex;
		flex-direction: column;
	}
	.feed li {
		display: flex;
		gap: 8px;
		align-items: flex-start;
		padding: 7px 0;
		border-bottom: 1px solid var(--border);
	}
	.feed li:last-child {
		border-bottom: 0;
		padding-bottom: 0;
	}
	.aic {
		width: 26px;
		height: 26px;
		flex-shrink: 0;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		border-radius: 50%;
		background: var(--primary-soft);
		color: var(--primary);
	}
	.aic.ok {
		background: #dcfce7;
		color: var(--success);
	}
	.aic.bad {
		background: #fee2e2;
		color: var(--danger);
	}
	.tx {
		flex: 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
		font-size: 0.82rem;
	}
	.what {
		font-weight: 600;
	}
	.dev,
	.det {
		font-size: 0.74rem;
	}
	.side {
		display: flex;
		flex-direction: column;
		align-items: flex-end;
		gap: 2px;
		flex-shrink: 0;
	}
	.when {
		font-size: 0.72rem;
		white-space: nowrap;
	}
</style>
