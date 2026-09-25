<script lang="ts">
	import Card from './Card.svelte';
	import Skeleton from './Skeleton.svelte';
	import EmptyState from './EmptyState.svelte';
	import WidgetError from './WidgetError.svelte';
	import { relativeTime, absoluteTime } from '../lib/time.js';
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
			<span class="okic" aria-hidden="true"><svg viewBox="0 0 24 24" width="26" height="26" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><path d="M4 12.5l5 5L20 6.5" /></svg></span>
			<div>
				<div class="headline">متصل وإسقاط المبيعات محدّث</div>
				<div class="muted sub">Connected · sale projection up to date</div>
			</div>
		</div>
		<div class="rows">
			<div class="r">
				<span class="ric ok" aria-hidden="true"><svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><path d="M4 12.5l5 5L20 6.5" /></svg></span>
				<span class="rl">آخر حدث مبيعات تم استلامه<br /><span class="muted sub-en">Latest Sale event received</span></span>
				<span class="rv num" dir="ltr">{f.latest_sale_event_received_at ? absoluteTime(f.latest_sale_event_received_at, 'ar') : '—'}</span>
			</div>
			<div class="r">
				<span class="ric ok" aria-hidden="true"><svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><path d="M4 12.5l5 5L20 6.5" /></svg></span>
				<span class="rl">في الانتظار / In queue</span>
				<span class="rv num">{health.queue_count}</span>
			</div>
			<div class="r">
				<span class="ric ok" aria-hidden="true"><svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><path d="M4 12.5l5 5L20 6.5" /></svg></span>
				<span class="rl">محظورة / Blocked</span>
				<span class="rv num">{f.blocked_sale_event_count}</span>
			</div>
			<div class="r">
				<span class="ric ret" aria-hidden="true"><svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><path d="M4 9h13l-3-3M20 15H7l3 3" /></svg></span>
				<span class="rl">آخر حدث مرتجع تم استلامه<br /><span class="muted sub-en">Latest Return event received</span></span>
				<span class="rv num" dir="ltr">{f.latest_return_event_received_at ? absoluteTime(f.latest_return_event_received_at, 'ar') : '—'}</span>
			</div>
			<div class="r">
				<span class="ric ret" aria-hidden="true"><svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><path d="M4 9h13l-3-3M20 15H7l3 3" /></svg></span>
				<span class="rl">مرتجعات مسقطة / Returns projected</span>
				<span class="rv num">{health.return_processed_count}</span>
			</div>
			<div class="r">
				<span class="ric ret" aria-hidden="true"><svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><path d="M4 9h13l-3-3M20 15H7l3 3" /></svg></span>
				<span class="rl">مرتجعات محظورة / Returns blocked</span>
				<span class="rv num">{health.return_blocked_count}</span>
			</div>
		</div>
		<div class="rel muted">
			{f.latest_sale_event_received_at ? relativeTime(f.latest_sale_event_received_at, 'ar') : ''}
			<span class="sub-en">pending {health.pending_count} · retrying {health.retry_count} · returns pending {health.return_pending_count} · retrying {health.return_retry_count}</span>
		</div>
		<div class="status">
			{#if f.blocked_sale_event_count > 0}
				<span class="badge warn">توجد عناصر محظورة تحتاج مراجعة / Blocked items need review</span>
			{:else if f.cloud_projection_complete}
				<span class="badge ok">اكتمال إسقاط المبيعات السحابي / Cloud sale projection complete</span>
			{:else}
				<span class="badge">جارٍ اللحاق / Catching up</span>
			{/if}
			{#if f.return_blocked_count > 0}
				<span class="badge warn">توجد مرتجعات محظورة تحتاج مراجعة / Blocked returns need review</span>
			{:else if f.return_projection_complete}
				<span class="badge ok">اكتمال إسقاط المرتجعات السحابي / Cloud return projection complete</span>
			{/if}
		</div>
		{#if health.last_error_code}
			<div class="muted errmsg">آخر خطأ: {health.last_error_label_ar ?? health.last_error_code}<br /><span class="sub-en">{health.last_error_label_en ?? ''}</span></div>
		{/if}
		{#if health.return_last_error_code}
			<div class="muted errmsg">آخر خطأ مرتجعات: {health.return_last_error_label_ar ?? health.return_last_error_code}<br /><span class="sub-en">{health.return_last_error_label_en ?? ''}</span></div>
		{/if}
		<div class="muted note">
			الاكتمال السحابي لا يعني أن درج مكتب المتجر فارغ — السحابة لا ترى الأحداث التي لم تُرسل بعد.
			<br /><span class="sub-en">Cloud-complete does not prove the Retail outbox is empty.</span>
			<br />التقارير تشمل المرتجعات المسقطة سحابيًا؛ المرتجعات المعلقة أو المحظورة تظهر أعلاه.
			<br /><span class="sub-en">Reports include Cloud-projected returns; pending or blocked returns appear above.</span>
		</div>
		<button type="button" class="refresh" aria-label="تحديث الحالة / Refresh status" onclick={onrefresh}><span class="rb-ar">تحديث الحالة</span><span class="rb-en">Refresh status</span></button>
	{/if}
</Card>

<style>
	.hero {
		display: flex;
		align-items: center;
		gap: 8px;
		margin-bottom: 6px;
	}
	.okic {
		width: 36px;
		height: 36px;
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
		font-size: 0.88rem;
	}
	.sub {
		font-size: 0.72rem;
	}
	.rows {
		display: flex;
		flex-direction: column;
		border: 1px solid var(--border);
		border-radius: var(--radius-control);
		overflow: hidden;
	}
	.r {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 5px 8px;
		font-size: 0.78rem;
	}
	.r + .r {
		border-top: 1px solid var(--border);
	}
	.ric {
		width: 20px;
		height: 20px;
		flex-shrink: 0;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		border-radius: 50%;
	}
	.ric.ok {
		background: #dcfce7;
		color: var(--success);
	}
	.ric.ret {
		background: #fef3e2;
		color: #b45309;
	}
	.rl {
		flex: 1;
		min-width: 0;
	}
	.rv {
		font-weight: 700;
		font-size: 0.85rem;
	}
	.rel {
		margin-top: 6px;
		font-size: 0.78rem;
		display: flex;
		gap: 8px;
		flex-wrap: wrap;
	}
	.status {
		margin-top: 8px;
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
