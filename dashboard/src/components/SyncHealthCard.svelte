<script lang="ts">
	import { arabic, lang, t, tb } from '../lib/i18n.svelte.js';
	import Card from './Card.svelte';
	import Skeleton from './Skeleton.svelte';
	import EmptyState from './EmptyState.svelte';
	import WidgetError from './WidgetError.svelte';
	import { absoluteTime } from '../lib/time.js';
	import type { SyncHealth } from '../lib/api.js';

	let { health, status, errStatus, onretry, onrefresh }: { health: SyncHealth | null; status: 'loading' | 'loaded' | 'empty' | 'error'; errStatus: number | null; onretry: () => void; onrefresh: () => void } =
		$props();

	// The headline states the real condition: blocked events need someone,
	// a backlog is still catching up, and only a complete projection with
	// nothing blocked reads as up to date.
	let level = $derived.by((): 'ok' | 'catching' | 'blocked' => {
		const f = health?.freshness;
		if (!f) return 'catching';
		if (f.blocked_sale_event_count > 0 || f.return_blocked_count > 0) return 'blocked';
		return f.cloud_projection_complete && f.return_projection_complete ? 'ok' : 'catching';
	});
	const headlines = {
		ok: ['متصل · المبيعات والمرتجعات محدّثة', 'Connected · sales & returns up to date'],
		catching: ['متصل · جارٍ اللحاق بالأحداث', 'Connected · catching up'],
		blocked: ['يحتاج انتباه · أحداث محظورة', 'Needs attention · blocked events']
	} as const;
	const icons = {
		ok: 'M4 12.5l5 5L20 6.5',
		catching: 'M12 7v5l3 2M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0z',
		blocked: 'M12 8v5M12 16.5v.5M10.3 3.9 2.6 17.5A2 2 0 0 0 4.3 20.5h15.4a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z'
	} as const;
</script>

<Card ar="حالة المزامنة مع المتجر" en="Sync Health Status">
	{#snippet actions()}
		{#if status !== 'loading'}
			<button type="button" class="refresh" aria-label={tb('تحديث الحالة', 'Refresh status')} onclick={onrefresh}>
				<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M20 11a8 8 0 0 0-14.6-4.5L4 8M4 4v4h4M4 13a8 8 0 0 0 14.6 4.5L20 16M20 20v-4h-4" /></svg>
				{t('تحديث', 'Refresh')}
			</button>
		{/if}
	{/snippet}
	{#if status === 'loading'}
		<Skeleton />
	{:else if status === 'error'}
		<WidgetError status={errStatus} {onretry} />
	{:else if !health}
		<EmptyState ar="لا توجد بيانات مزامنة" en="No sync data" />
	{:else}
		{@const f = health.freshness}
		<div class="hero level-{level}" role="status" data-testid="sync-hero" data-level={level}>
			<span class="okic" aria-hidden="true"><svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><path d={icons[level]} /></svg></span>
			<div>
				<div class="headline">{t(headlines[level][0], headlines[level][1])}</div>
				{#if arabic()}<div class="sub">{headlines[level][1]}</div>{/if}
			</div>
		</div>
		<div class="grid">
			<div class="ghead" aria-hidden="true"></div>
			<div class="ghead">{t('مبيعات', 'Sales')}{#if arabic()}<span class="sub-en"> · Sales</span>{/if}</div>
			<div class="ghead">{t('مرتجعات', 'Returns')}{#if arabic()}<span class="sub-en"> · Returns</span>{/if}</div>
			<div class="gl">{t('آخر حدث', 'Latest event')}{#if arabic()}<br /><span class="muted sub-en">Latest event</span>{/if}</div>
			<div class="gv num" dir="ltr">{f.latest_sale_event_received_at ? absoluteTime(f.latest_sale_event_received_at, lang()) : '—'}</div>
			<div class="gv num" dir="ltr">{f.latest_return_event_received_at ? absoluteTime(f.latest_return_event_received_at, lang()) : '—'}</div>
			<div class="gl">{t('متأخر', 'Backlog')}{#if arabic()}<br /><span class="muted sub-en">Backlog</span>{/if}</div>
			<div class="gv num">{f.projection_backlog_count}<span class="muted det"> · {health.pending_count} pending · {health.retry_count} retry</span></div>
			<div class="gv num">{f.return_backlog_count}<span class="muted det"> · {health.return_pending_count} pending · {health.return_retry_count} retry</span></div>
			<div class="gl">{t('محظور', 'Blocked')}{#if arabic()}<br /><span class="muted sub-en">Blocked</span>{/if}</div>
			<div class="gv num">{f.blocked_sale_event_count}</div>
			<div class="gv num">{f.return_blocked_count}</div>
			<div class="gl">{t('الحالة', 'Status')}{#if arabic()}<br /><span class="muted sub-en">Status</span>{/if}</div>
			<div class="gv">
				{#if f.blocked_sale_event_count > 0}
					<span class="badge warn">{tb('محظورة', 'Blocked')}</span>
				{:else if f.cloud_projection_complete}
					<span class="badge ok">{tb('مكتمل', 'Complete')}</span>
				{:else}
					<span class="badge">{tb('يلحق', 'Catching up')}</span>
				{/if}
			</div>
			<div class="gv">
				{#if f.return_blocked_count > 0}
					<span class="badge warn">{tb('محظورة', 'Blocked')}</span>
				{:else if f.return_projection_complete}
					<span class="badge ok">{tb('مكتمل', 'Complete')}</span>
				{:else}
					<span class="badge">{tb('يلحق', 'Catching up')}</span>
				{/if}
			</div>
		</div>
		{#if health.last_error_code}
			<div class="muted errmsg">{t('آخر خطأ', 'Last error')}: {t(health.last_error_label_ar ?? health.last_error_code ?? '', health.last_error_label_en ?? health.last_error_code ?? '')}{#if arabic() && health.last_error_label_en}<span class="sub-en"> · {health.last_error_label_en}</span>{/if}</div>
		{/if}
		{#if health.return_last_error_code}
			<div class="muted errmsg">{t('آخر خطأ مرتجعات', 'Last return error')}: {t(health.return_last_error_label_ar ?? health.return_last_error_code ?? '', health.return_last_error_label_en ?? health.return_last_error_code ?? '')}{#if arabic() && health.return_last_error_label_en}<span class="sub-en"> · {health.return_last_error_label_en}</span>{/if}</div>
		{/if}
		<div class="muted note">
			{t('التقارير تشمل المرتجعات المسقطة؛ المعلقة أو المحظورة تظهر أعلاه. الاكتمال السحابي لا يعني فراغ درج المتجر.', 'Reports include projected returns; pending/blocked appear above. Cloud-complete ≠ empty Retail outbox.')}{#if arabic()}
			<span class="sub-en note-en">Reports include projected returns; pending/blocked appear above. Cloud-complete ≠ empty Retail outbox.</span>{/if}
		</div>
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
	.grid {
		display: grid;
		grid-template-columns: auto 1fr 1fr;
		gap: 2px 8px;
		align-items: center;
		border: 1px solid var(--border);
		border-radius: var(--radius-control);
		padding: 2px 8px;
		font-size: 0.76rem;
	}
	.ghead {
		font-weight: 700;
		font-size: 0.72rem;
		text-align: center;
		white-space: nowrap;
	}
	.gl {
		font-size: 0.74rem;
		line-height: 1.15;
	}
	.gv {
		font-weight: 700;
		font-size: 0.8rem;
		text-align: center;
		min-width: 0;
		overflow-wrap: anywhere;
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
