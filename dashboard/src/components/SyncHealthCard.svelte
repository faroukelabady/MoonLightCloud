<script lang="ts">
	import Card from './Card.svelte';
	import Skeleton from './Skeleton.svelte';
	import EmptyState from './EmptyState.svelte';
	import WidgetError from './WidgetError.svelte';
	import { absoluteTime } from '../lib/time.js';
	import type { SyncHealth } from '../lib/api.js';

	let { health, status, errStatus, onretry, onrefresh }: { health: SyncHealth | null; status: 'loading' | 'loaded' | 'empty' | 'error'; errStatus: number | null; onretry: () => void; onrefresh: () => void } =
		$props();
</script>

<Card ar="حالة المزامنة مع المتجر" en="Sync Health Status">
	{#if status === 'loading'}
		<Skeleton />
	{:else if status === 'error'}
		<WidgetError status={errStatus} {onretry} />
	{:else if !health}
		<EmptyState ar="لا توجد بيانات مزامنة" en="No sync data" />
	{:else}
		{@const f = health.freshness}
		<div class="hero">
			<span class="okic" aria-hidden="true"><svg viewBox="0 0 24 24" width="22" height="22" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><path d="M4 12.5l5 5L20 6.5" /></svg></span>
			<div>
				<div class="headline">متصل · المبيعات والمرتجعات محدّثة</div>
				<div class="muted sub">Connected · sales & returns up to date</div>
			</div>
		</div>
		<div class="grid">
			<div class="ghead" aria-hidden="true"></div>
			<div class="ghead">مبيعات<br /><span class="sub-en">Sales</span></div>
			<div class="ghead">مرتجعات<br /><span class="sub-en">Returns</span></div>
			<div class="gl">آخر حدث<br /><span class="muted sub-en">Latest event</span></div>
			<div class="gv num" dir="ltr">{f.latest_sale_event_received_at ? absoluteTime(f.latest_sale_event_received_at, 'ar') : '—'}</div>
			<div class="gv num" dir="ltr">{f.latest_return_event_received_at ? absoluteTime(f.latest_return_event_received_at, 'ar') : '—'}</div>
			<div class="gl">متأخر<br /><span class="muted sub-en">Backlog</span></div>
			<div class="gv num">{f.projection_backlog_count}<span class="muted det"> · {health.pending_count} pending · {health.retry_count} retry</span></div>
			<div class="gv num">{f.return_backlog_count}<span class="muted det"> · {health.return_pending_count} pending · {health.return_retry_count} retry</span></div>
			<div class="gl">محظور<br /><span class="muted sub-en">Blocked</span></div>
			<div class="gv num">{f.blocked_sale_event_count}</div>
			<div class="gv num">{f.return_blocked_count}</div>
			<div class="gl">الحالة<br /><span class="muted sub-en">Status</span></div>
			<div class="gv">
				{#if f.blocked_sale_event_count > 0}
					<span class="badge warn">محظورة / Blocked</span>
				{:else if f.cloud_projection_complete}
					<span class="badge ok">مكتمل / Complete</span>
				{:else}
					<span class="badge">يلحق / Catching up</span>
				{/if}
			</div>
			<div class="gv">
				{#if f.return_blocked_count > 0}
					<span class="badge warn">محظورة / Blocked</span>
				{:else if f.return_projection_complete}
					<span class="badge ok">مكتمل / Complete</span>
				{:else}
					<span class="badge">يلحق / Catching up</span>
				{/if}
			</div>
		</div>
		{#if health.last_error_code}
			<div class="muted errmsg">آخر خطأ: {health.last_error_label_ar ?? health.last_error_code}<br /><span class="sub-en">{health.last_error_label_en ?? ''}</span></div>
		{/if}
		{#if health.return_last_error_code}
			<div class="muted errmsg">آخر خطأ مرتجعات: {health.return_last_error_label_ar ?? health.return_last_error_code}<br /><span class="sub-en">{health.return_last_error_label_en ?? ''}</span></div>
		{/if}
		<div class="muted note">
			التقارير تشمل المرتجعات المسقطة؛ المعلقة أو المحظورة تظهر أعلاه. الاكتمال السحابي لا يعني فراغ درج المتجر.
			<br /><span class="sub-en">Reports include projected returns; pending/blocked appear above. Cloud-complete ≠ empty Retail outbox.</span>
		</div>
		<button type="button" class="refresh" aria-label="تحديث الحالة / Refresh status" onclick={onrefresh}><span class="rb-ar">تحديث الحالة</span><span class="rb-en">Refresh status</span></button>
	{/if}
</Card>

<style>
	.hero {
		display: flex;
		align-items: center;
		gap: 8px;
		margin-bottom: 4px;
	}
	.okic {
		width: 30px;
		height: 30px;
		flex-shrink: 0;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		border-radius: 50%;
		background: var(--success);
		color: #fff;
	}
	.headline {
		font-weight: 700;
		font-size: 0.85rem;
	}
	.sub {
		font-size: 0.7rem;
	}
	.grid {
		display: grid;
		grid-template-columns: auto 1fr 1fr;
		gap: 2px 8px;
		align-items: center;
		border: 1px solid var(--border);
		border-radius: var(--radius-control);
		padding: 4px 8px;
		font-size: 0.76rem;
	}
	.ghead {
		font-weight: 700;
		font-size: 0.72rem;
		text-align: center;
	}
	.gl {
		font-size: 0.74rem;
		line-height: 1.25;
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
	.errmsg {
		margin-top: 6px;
		font-size: 0.82rem;
	}
	.note {
		margin-top: 6px;
		font-size: 0.74rem;
	}
	.refresh {
		margin-top: 8px;
		width: 100%;
		background: var(--primary);
		color: #fff;
		border: 0;
		border-radius: var(--radius-control);
		padding: 7px 10px;
		font-weight: 600;
		line-height: 1.35;
	}
	.rb-ar {
		display: block;
		font-size: 0.86rem;
	}
	.rb-en {
		display: block;
		font-size: 0.7rem;
		font-weight: 400;
		opacity: 0.9;
	}
</style>
