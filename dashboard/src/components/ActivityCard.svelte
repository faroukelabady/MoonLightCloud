<script lang="ts">
	import Card from './Card.svelte';
	import Skeleton from './Skeleton.svelte';
	import EmptyState from './EmptyState.svelte';
	import WidgetError from './WidgetError.svelte';
	import { relativeTime } from '../lib/time.js';
	import { lang, msg, t } from '../lib/i18n.svelte.js';
	import { dashboardApi } from '../lib/api.js';
	import type { ActivityItem } from '../lib/api.js';

	// items is the first page the page loaded; later pages load here. The
	// list scrolls inside the card, so the card keeps its height.
	let {
		items,
		hasMore = false,
		store = '',
		status,
		errStatus,
		onretry
	}: {
		items: ActivityItem[];
		hasMore?: boolean;
		store?: string;
		status: 'loading' | 'loaded' | 'empty' | 'error';
		errStatus: number | null;
		onretry: () => void;
	} = $props();

	const pageSize = 10;
	let page = $state(0);
	let pageItems = $state<ActivityItem[] | null>(null);
	let pageMore = $state(false);
	let pageState = $state<'idle' | 'loading' | 'error'>('idle');
	let request = 0;
	let feed: HTMLUListElement | undefined = $state();

	// A reload of the first page (new Store or period) starts again at page 1.
	$effect(() => {
		void items;
		void store;
		request++;
		page = 0;
		pageItems = null;
		pageState = 'idle';
	});

	let shown = $derived(page === 0 ? items : (pageItems ?? []));
	let more = $derived(page === 0 ? hasMore : pageMore);

	async function go(next: number) {
		if (next < 0 || pageState === 'loading') return;
		const my = ++request;
		if (next === 0) {
			page = 0;
			pageItems = null;
			pageState = 'idle';
			feed?.scrollTo?.({ top: 0 });
			return;
		}
		pageState = 'loading';
		try {
			const res = await dashboardApi.activityPage(store, next * pageSize);
			if (my !== request) return;
			page = next;
			pageItems = res.items;
			pageMore = res.has_more;
			pageState = 'idle';
			feed?.scrollTo?.({ top: 0 });
		} catch {
			if (my !== request) return;
			pageState = 'error';
		}
	}

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
	{:else if shown.length === 0 && page === 0}
		<EmptyState ar="لا توجد أنشطة بعد" en="No activities yet" />
	{:else}
		<ul class="feed" bind:this={feed} data-testid="activity-feed">
			{#each shown as it (it.kind + it.event_id)}
				{@const [ar, en, cls] = kindLabel(it.kind)}
				<li>
					<span class="aic {cls}" aria-hidden="true"><svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d={iconOf(it.kind)} /></svg></span>
					<span class="tx">
						<span class="what num">{it.event_type}</span>
						{#if it.device_name}<span class="muted dev">{it.device_name}</span>{/if}
						{#if it.detail}<span class="muted num det">{it.detail}</span>{/if}
					</span>
					<span class="side">
						<span class="badge {cls}">{t(ar, en)}</span>
						<span class="when muted">{relativeTime(it.timestamp, lang())}</span>
					</span>
				</li>
			{/each}
		</ul>
		{#if pageState === 'error'}<p class="muted pgerr" role="alert">{msg('activityCard.loadFailed')}</p>{/if}
		{#if page > 0 || more}
			<nav class="pager" aria-label={msg('activityCard.page', { page: page + 1 })}>
				<button type="button" class="btn-secondary btn-sm" disabled={page === 0 || pageState === 'loading'} onclick={() => void go(page - 1)}>{msg('activityCard.previous')}</button>
				<span class="num muted">{msg('activityCard.page', { page: page + 1 })}</span>
				<button type="button" class="btn-secondary btn-sm" disabled={!more || pageState === 'loading'} onclick={() => void go(page + 1)}>{msg('activityCard.next')}</button>
			</nav>
		{/if}
	{/if}
</Card>

<style>
	.feed {
		list-style: none;
		margin: 4px 0 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		max-height: 340px;
		overflow-y: auto;
		scrollbar-width: thin;
		padding-inline-end: 4px;
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
	.pager {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 8px;
		margin-top: 8px;
		padding-top: 8px;
		border-top: 1px solid var(--border);
		font-size: 0.8rem;
	}
	.pgerr {
		margin: 6px 0 0;
		font-size: 0.8rem;
	}
</style>
