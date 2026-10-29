<script lang="ts">
	import { lang, msg, t } from '../lib/i18n.svelte.js';
	import Card from './Card.svelte';
	import Skeleton from './Skeleton.svelte';
	import EmptyState from './EmptyState.svelte';
	import WidgetError from './WidgetError.svelte';
	import { absoluteTime } from '../lib/time.js';
	import type { SyncHealth } from '../lib/api.js';

	let {
		health,
		status,
		errStatus,
		onretry,
		onrefresh,
		noShop = false
	}: {
		health: SyncHealth | null;
		status: 'loading' | 'loaded' | 'empty' | 'error';
		errStatus: number | null;
		onretry: () => void;
		onrefresh: () => void;
		// No Store is registered yet, so nothing can sync.
		noShop?: boolean;
	} = $props();

	// The headline states the real condition: blocked events need someone,
	// a backlog is still catching up, and only a complete projection with
	// nothing blocked reads as up to date.
	let level = $derived.by((): 'ok' | 'catching' | 'blocked' | 'noshop' => {
		if (noShop) return 'noshop';
		const f = health?.freshness;
		if (!f) return 'catching';
		if (f.blocked_sale_event_count > 0 || f.return_blocked_count > 0) return 'blocked';
		return f.cloud_projection_complete && f.return_projection_complete ? 'ok' : 'catching';
	});
	const headlines = {
		ok: ['متصل · المبيعات والمرتجعات محدّثة', 'Connected · sales & returns up to date'],
		catching: ['متصل · جارٍ اللحاق بالأحداث', 'Connected · catching up'],
		blocked: ['يحتاج انتباه · أحداث محظورة', 'Needs attention · blocked events'],
		noshop: ['لا يوجد متجر مُعدّ بعد', 'No shop configured yet']
	} as const;
	const icons = {
		ok: 'M4 12.5l5 5L20 6.5',
		catching: 'M12 7v5l3 2M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0z',
		blocked: 'M12 8v5M12 16.5v.5M10.3 3.9 2.6 17.5A2 2 0 0 0 4.3 20.5h15.4a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z',
		noshop: 'M4 10l1.5-5h13L20 10M4 10v9h16v-9M4 10h16M9 19v-5h6v5'
	} as const;
</script>

<Card ar="حالة المزامنة مع المتجر" en="Sync Health Status">
	{#snippet actions()}
		{#if status !== 'loading'}
			<button type="button" class="refresh" aria-label={msg('syncHealthCard.refreshStatus')} onclick={onrefresh}>
				<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M20 11a8 8 0 0 0-14.6-4.5L4 8M4 4v4h4M4 13a8 8 0 0 0 14.6 4.5L20 16M20 20v-4h-4" /></svg>
				{msg('syncHealthCard.refresh')}
			</button>
		{/if}
	{/snippet}
	{#if status === 'loading'}
		<Skeleton />
	{:else if status === 'error'}
		<WidgetError status={errStatus} {onretry} />
	{:else if !health && !noShop}
		<EmptyState ar="لا توجد بيانات مزامنة" en="No sync data" />
	{:else}
		<div class="hero level-{level}" role="status" data-testid="sync-hero" data-level={level}>
			<span class="okic" aria-hidden="true"><svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><path d={icons[level]} /></svg></span>
			<div>
				<div class="headline">{t(headlines[level][0], headlines[level][1])}</div>
			</div>
		</div>
		{#if noShop || !health}
			<p class="muted noshop-hint">{msg('syncHealthCard.registerAShopsDeviceOnThe')}</p>
		{:else}
		{@const f = health.freshness}
		<table class="health" data-testid="sync-table">
			<thead>
				<tr>
					<td></td>
					<th scope="col">{msg('syncHealthCard.sales')}</th>
					<th scope="col">{msg('syncHealthCard.returns')}</th>
				</tr>
			</thead>
			<tbody>
				<tr>
					<th scope="row">{msg('syncHealthCard.latestEvent')}</th>
					<td class="num" dir="ltr">{f.latest_sale_event_received_at ? absoluteTime(f.latest_sale_event_received_at, lang()) : '—'}</td>
					<td class="num" dir="ltr">{f.latest_return_event_received_at ? absoluteTime(f.latest_return_event_received_at, lang()) : '—'}</td>
				</tr>
				<tr>
					<th scope="row">{msg('syncHealthCard.backlog')}</th>
					<td class="num"><strong>{f.projection_backlog_count}</strong><span class="muted det">{health.pending_count} pending · {health.retry_count} retry</span></td>
					<td class="num"><strong>{f.return_backlog_count}</strong><span class="muted det">{health.return_pending_count} pending · {health.return_retry_count} retry</span></td>
				</tr>
				<tr>
					<th scope="row">{msg('syncHealthCard.blocked')}</th>
					<td class="num"><strong>{f.blocked_sale_event_count}</strong></td>
					<td class="num"><strong>{f.return_blocked_count}</strong></td>
				</tr>
				<tr>
					<th scope="row">{msg('syncHealthCard.status')}</th>
					<td>{#if f.blocked_sale_event_count > 0}<span class="badge warn">{msg('syncHealthCard.blocked2')}</span>{:else if f.cloud_projection_complete}<span class="badge ok">{msg('syncHealthCard.complete')}</span>{:else}<span class="badge">{msg('syncHealthCard.catchingUp')}</span>{/if}</td>
					<td>{#if f.return_blocked_count > 0}<span class="badge warn">{msg('syncHealthCard.blocked2')}</span>{:else if f.return_projection_complete}<span class="badge ok">{msg('syncHealthCard.complete')}</span>{:else}<span class="badge">{msg('syncHealthCard.catchingUp')}</span>{/if}</td>
				</tr>
			</tbody>
		</table>
		{#if health.last_error_code}
			<div class="muted errmsg">{msg('syncHealthCard.lastError')}: {t(health.last_error_label_ar ?? health.last_error_code ?? '', health.last_error_label_en ?? health.last_error_code ?? '')}</div>
		{/if}
		{#if health.return_last_error_code}
			<div class="muted errmsg">{msg('syncHealthCard.lastReturnError')}: {t(health.return_last_error_label_ar ?? health.return_last_error_code ?? '', health.return_last_error_label_en ?? health.return_last_error_code ?? '')}</div>
		{/if}
		<div class="muted note">
			{msg('syncHealthCard.reportsIncludeProjectedReturnsPendingBlocked')}
		</div>
		{/if}
	{/if}
</Card>

<style>
	.hero {
		--tone: var(--success);
		--tone-soft: #e8f6ee;
		--tone-text: #15803d;
		display: flex;
		align-items: center;
		gap: 10px;
		margin-bottom: 6px;
		padding: 6px 10px;
		border-radius: var(--radius-control);
		background: var(--tone-soft);
		border-inline-start: 4px solid var(--tone);
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
	.okic {
		width: 30px;
		height: 30px;
		flex-shrink: 0;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		border-radius: 50%;
		background: var(--tone);
		color: #fff;
	}
	.headline {
		font-weight: 800;
		font-size: 0.95rem;
		line-height: 1.25;
		color: var(--tone-text);
	}
	.sub {
		font-size: 0.74rem;
		line-height: 1.25;
		color: var(--text-muted);
	}
	.health {
		width: 100%;
		border-collapse: collapse;
		table-layout: fixed;
		font-size: 0.76rem;
	}
	.health th,
	.health td {
		padding: 3px 4px;
		border-bottom: 1px solid var(--border);
		text-align: center;
		vertical-align: middle;
		line-height: 1.2;
		overflow-wrap: anywhere;
	}
	.health tbody tr:last-child > * {
		border-bottom: 0;
	}
	.health thead th {
		font-size: 0.72rem;
		font-weight: 700;
		white-space: nowrap;
	}
	.health tbody th {
		text-align: start;
		font-weight: 600;
		font-size: 0.74rem;
		width: 30%;
	}
	.health strong {
		font-size: 0.82rem;
	}
	.health .det {
		display: block;
	}
	.hero.level-noshop {
		--tone: #ca8a04;
		--tone-soft: #fef3c7;
		--tone-text: #854d0e;
	}
	.noshop-hint {
		margin: 4px 0 0;
		font-size: 0.8rem;
	}
	.det {
		font-weight: 400;
		font-size: 0.68rem;
	}
	/* English sublabels sit inline here, and line heights are tight, so
	   the worst sync state still fits the first overview row (the E2E
	   first-row density gate measures it in headless Chromium). */
	.errmsg {
		margin-top: 4px;
		font-size: 0.82rem;
		line-height: 1.35;
	}
	.note {
		margin-top: 4px;
		font-size: 0.7rem;
		line-height: 1.35;
	}
	.note-en {
		display: block;
	}
	/* In the card header, which saves a row of height in the card body. */
	.refresh {
		display: inline-flex;
		align-items: center;
		gap: 5px;
		background: var(--primary);
		color: #fff;
		border: 0;
		border-radius: var(--radius-control);
		padding: 5px 10px;
		font-size: 0.8rem;
		font-weight: 600;
		white-space: nowrap;
	}
</style>
