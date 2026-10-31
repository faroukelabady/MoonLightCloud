<script lang="ts">
	import { lang, msg, t } from '../lib/i18n.svelte.js';
	import Card from './Card.svelte';
	import Skeleton from './Skeleton.svelte';
	import EmptyState from './EmptyState.svelte';
	import WidgetError from './WidgetError.svelte';
	import { relativeTime } from '../lib/time.js';
	import { formatInt } from '../lib/money.js';
	import type { SyncHealth, SyncSummary, SyncCategoryKey } from '../lib/api.js';

	let {
		health,
		summary = null,
		status,
		errStatus,
		onretry,
		onsync,
		canSync = false,
		syncing = false,
		notice = '',
		noShop = false
	}: {
		health: SyncHealth | null;
		// Events received in the selected period; null when that read failed.
		summary?: SyncSummary | null;
		status: 'loading' | 'loaded' | 'empty' | 'error';
		errStatus: number | null;
		onretry: () => void;
		// Sends sync_now to the Store's devices (canSync) or refreshes the view.
		onsync: () => void;
		canSync?: boolean;
		syncing?: boolean;
		notice?: string;
		// No Store is registered yet, so nothing can sync.
		noShop?: boolean;
	} = $props();

	// The headline states the real condition: blocked events need someone,
	// a backlog is still catching up, and only a complete projection with
	// nothing blocked reads as processed. Cloud cannot see the Retail outbox,
	// so it never claims that every shop record arrived.
	let level = $derived.by((): 'ok' | 'catching' | 'blocked' | 'noshop' => {
		if (noShop) return 'noshop';
		const f = health?.freshness;
		if (!f) return 'catching';
		if (f.blocked_sale_event_count > 0 || f.return_blocked_count > 0) return 'blocked';
		return f.cloud_projection_complete && f.return_projection_complete ? 'ok' : 'catching';
	});
	const icons = {
		ok: 'M4 12.5l5 5L20 6.5',
		catching: 'M12 7v5l3 2M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0z',
		blocked: 'M12 8v5M12 16.5v.5M10.3 3.9 2.6 17.5A2 2 0 0 0 4.3 20.5h15.4a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z',
		noshop: 'M4 10l1.5-5h13L20 10M4 10v9h16v-9M4 10h16M9 19v-5h6v5'
	} as const;

	// Sales and returns go blocked on their own; the other kinds have no
	// blocked count of their own here.
	function categoryBlocked(c: SyncCategoryKey): boolean {
		const f = health?.freshness;
		if (!f) return false;
		if (c === 'sales') return f.blocked_sale_event_count > 0;
		if (c === 'returns') return f.return_blocked_count > 0;
		return false;
	}

	let queued = $derived(health ? health.pending_count + health.retry_count + health.return_pending_count + health.return_retry_count : 0);
	let blocked = $derived(health ? health.freshness.blocked_sale_event_count + health.freshness.return_blocked_count : 0);
	// Sale and return errors are separate channels: both show when both exist.
	let lastErrors = $derived.by(() => {
		const out: string[] = [];
		if (health?.last_error_code) out.push(t(health.last_error_label_ar ?? health.last_error_code, health.last_error_label_en ?? health.last_error_code));
		if (health?.return_last_error_code) out.push(t(health.return_last_error_label_ar ?? health.return_last_error_code, health.return_last_error_label_en ?? health.return_last_error_code));
		return out;
	});
</script>

<Card ar={msg('syncHealthCard.title')}>
	{#if status === 'loading'}
		<Skeleton />
	{:else if status === 'error'}
		<WidgetError status={errStatus} {onretry} />
	{:else if !health && !noShop}
		<EmptyState ar="لا توجد بيانات مزامنة" en="No sync data" />
	{:else}
		<div class="wrap">
			<div class="grid">
			<div class="hero level-{level}" role="status" data-testid="sync-hero" data-level={level}>
				<span class="bigic" aria-hidden="true"><svg viewBox="0 0 24 24" width="30" height="30" fill="none" stroke="currentColor" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round"><path d={icons[level]} /></svg></span>
				<p class="headline">{msg(`syncHealthCard.level.${level}`)}</p>
				{#if noShop || !health}
					<p class="muted hint">{msg('syncHealthCard.registerAShopsDeviceOnThe')}</p>
				{:else}
					<button type="button" class="syncbtn" disabled={syncing} onclick={onsync} data-testid="sync-now">
						<svg class:spin={syncing} viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M20 11a8 8 0 0 0-14.6-4.5L4 8M4 4v4h4M4 13a8 8 0 0 0 14.6 4.5L20 16M20 20v-4h-4" /></svg>
						{syncing ? msg('syncHealthCard.syncing') : canSync ? msg('syncHealthCard.syncNow') : msg('syncHealthCard.refresh')}
					</button>
					{#if notice}<p class="notice" role="status">{notice}</p>{/if}
				{/if}
			</div>
			{#if health}
				{#if summary}
					<ul class="cats" data-testid="sync-categories">
						{#each summary.categories as c (c.category)}
							{@const bad = categoryBlocked(c.category)}
							<li>
								<span class="dot" class:bad aria-hidden="true"><svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"><path d={bad ? icons.blocked : icons.ok} /></svg></span>
								<span class="name">{msg(`syncHealthCard.category.${c.category}`)}</span>
								<span class="count num">{msg('syncHealthCard.inPeriod', { count: formatInt(c.synced) })}</span>
								<span class="when muted">{c.last_received_at ? relativeTime(c.last_received_at, lang()) : msg('syncHealthCard.never')}</span>
							</li>
						{/each}
					</ul>
				{:else}
					<p class="muted hint">{msg('syncHealthCard.summaryUnavailable')}</p>
				{/if}
			{/if}
		</div>
		</div>
		{#if health}
			<div class="foot">
				<div class="tile">
					<svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="9" /><path d="M12 7v5l3 2" /></svg>
					<span class="label">{msg('syncHealthCard.lastSync')}</span>
					<strong class="value">{summary?.last_received_at ? relativeTime(summary.last_received_at, lang()) : '—'}</strong>
				</div>
				<div class="tile">
					<svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 3 3 8l9 5 9-5-9-5zM3 13l9 5 9-5M3 17.5l9 5 9-5" /></svg>
					<span class="label">{msg('syncHealthCard.inQueue')}</span>
					<strong class="value num">{msg('syncHealthCard.items', { count: formatInt(queued) })}</strong>
				</div>
				{#if blocked > 0}
					<div class="tile bad">
						<svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d={icons.blocked} /></svg>
						<span class="label">{msg('syncHealthCard.blocked')}</span>
						<strong class="value num">{formatInt(blocked)}</strong>
					</div>
				{/if}
			</div>
			{#each lastErrors as error, i (i)}<p class="errmsg">{msg('syncHealthCard.lastError')}: {error}</p>{/each}
		{/if}
	{/if}
</Card>

<style>
	/* Narrow (the Overview column): status on top, kinds below. Wide (the
	   Sync page): status beside the kinds. */
	.wrap {
		container-type: inline-size;
	}
	.grid {
		display: grid;
		grid-template-columns: minmax(0, 1fr);
		gap: 10px;
		align-items: stretch;
	}
	.hero {
		--tone: var(--success);
		--tone-soft: #e8f6ee;
		--tone-text: #15803d;
		display: grid;
		grid-template-columns: auto minmax(0, 1fr);
		align-items: center;
		gap: 8px 12px;
		padding: 12px;
		border-radius: var(--radius-card);
		background: var(--tone-soft);
	}
	.hero.level-catching {
		--tone: var(--warning);
		--tone-soft: #fdf4e7;
		--tone-text: #92400e;
	}
	.hero.level-blocked {
		--tone: var(--danger);
		--tone-soft: #fdeeee;
		--tone-text: #991b1b;
	}
	.hero.level-noshop {
		--tone: #ca8a04;
		--tone-soft: #fef3c7;
		--tone-text: #854d0e;
	}
	.bigic {
		width: 44px;
		height: 44px;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		border-radius: 50%;
		background: var(--tone);
		color: #fff;
	}
	.headline {
		margin: 0;
		font-weight: 800;
		font-size: 0.98rem;
		line-height: 1.3;
		color: var(--tone-text);
	}
	.hint {
		grid-column: 1 / -1;
		margin: 0;
		font-size: 0.8rem;
	}
	.syncbtn {
		grid-column: 1 / -1;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		gap: 8px;
		width: 100%;
		margin-top: 4px;
		padding: 10px 14px;
		border: 0;
		border-radius: var(--radius-control);
		background: var(--primary);
		color: #fff;
		font-size: 0.95rem;
		font-weight: 700;
		cursor: pointer;
	}
	.syncbtn:disabled {
		opacity: 0.7;
		cursor: progress;
	}
	.spin {
		animation: spin 1s linear infinite;
	}
	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}
	.notice {
		grid-column: 1 / -1;
		margin: 0;
		font-size: 0.76rem;
		color: var(--tone-text);
	}
	.cats {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
	}
	.cats li {
		display: grid;
		grid-template-columns: auto minmax(0, 1fr) auto;
		grid-template-areas: 'dot name when' 'dot count when';
		column-gap: 10px;
		align-items: center;
		padding: 8px 2px;
		border-bottom: 1px solid var(--border);
	}
	.cats li:last-child {
		border-bottom: 0;
	}
	.dot {
		grid-area: dot;
		width: 28px;
		height: 28px;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		border-radius: 50%;
		background: var(--success);
		color: #fff;
	}
	.dot.bad {
		background: var(--danger);
	}
	.name {
		grid-area: name;
		font-weight: 700;
		font-size: 0.86rem;
	}
	.count {
		grid-area: count;
		font-size: 0.74rem;
		color: var(--text-muted);
	}
	.when {
		grid-area: when;
		font-size: 0.78rem;
		white-space: nowrap;
	}
	.foot {
		display: flex;
		flex-wrap: wrap;
		margin-top: 12px;
		border: 1px solid var(--border);
		border-radius: var(--radius-card);
	}
	.tile {
		flex: 1 1 140px;
		display: grid;
		grid-template-columns: auto minmax(0, 1fr);
		grid-template-areas: 'icon label' 'icon value';
		column-gap: 10px;
		align-items: center;
		padding: 8px 12px;
		color: var(--primary);
	}
	.tile + .tile {
		border-inline-start: 1px solid var(--border);
	}
	.tile.bad {
		color: var(--danger);
	}
	.tile svg {
		grid-area: icon;
	}
	.label {
		grid-area: label;
		font-size: 0.74rem;
		color: var(--text-muted);
	}
	.value {
		grid-area: value;
		font-size: 0.9rem;
		color: var(--text);
	}
	.errmsg {
		margin: 8px 0 0;
		font-size: 0.8rem;
		color: #991b1b;
	}
	@container (min-width: 560px) {
		.grid {
			grid-template-columns: minmax(0, 1fr) minmax(0, 1.1fr);
			gap: 14px;
		}
		.hero {
			display: flex;
			flex-direction: column;
			align-items: flex-start;
			justify-content: center;
			gap: 12px;
		}
		.bigic {
			width: 56px;
			height: 56px;
		}
	}
</style>
