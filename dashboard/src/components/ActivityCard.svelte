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
		return ['تم الاستلام', 'accepted', ''];
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
				{@const [ar, en, cls] = kindLabel(it.kind)}
				<li>
					<span class="badge {cls}">{ar}</span>
					<span class="what num">{it.event_type}</span>
					{#if it.device_name}<span class="muted">{it.device_name}</span>{/if}
					{#if it.detail}<span class="muted num">{it.detail}</span>{/if}
					<span class="when muted">{relativeTime(it.timestamp, 'ar')}</span>
				</li>
			{/each}
		</ul>
	{/if}
</Card>

<style>
	.feed {
		list-style: none;
		margin: 8px 0 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 8px;
	}
	.feed li {
		display: flex;
		gap: 8px;
		align-items: baseline;
		flex-wrap: wrap;
		border-bottom: 1px solid var(--border);
		padding-bottom: 8px;
	}
	.when {
		margin-inline-start: auto;
	}
</style>
