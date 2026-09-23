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

<Card ar="حالة المزامنة مع المتجر" en="Sync health status">
	{#if status === 'loading'}
		<Skeleton />
	{:else if status === 'error'}
		<WidgetError status={errStatus} {onretry} />
	{:else if !health}
		<EmptyState ar="لا توجد بيانات مزامنة" en="No sync data" />
	{:else}
		{@const f = health.freshness}
		<div class="grid">
			<div>
				<div class="muted">آخر حدث مبيعات تم استلامه<br /><span class="sub-en">Latest Sale event received</span></div>
				<div class="big num" dir="ltr">
					{f.latest_sale_event_received_at ? absoluteTime(f.latest_sale_event_received_at, 'ar') : '—'}
				</div>
				<div class="muted">
					{f.latest_sale_event_received_at ? relativeTime(f.latest_sale_event_received_at, 'ar') : ''}
				</div>
			</div>
			<div>
				<div class="muted">في الانتظار / In queue</div>
				<div class="big num">{health.queue_count}</div>
				<div class="muted sub-en">pending {health.pending_count} · retrying {health.retry_count}</div>
			</div>
			<div>
				<div class="muted">محظورة / Blocked</div>
				<div class="big num">{f.blocked_sale_event_count}</div>
			</div>
		</div>
		<div class="status">
			{#if f.blocked_sale_event_count > 0}
				<span class="badge warn">توجد عناصر محظورة تحتاج مراجعة / Blocked items need review</span>
			{:else if f.cloud_projection_complete}
				<span class="badge ok">المعالجة السحابية مكتملة / Cloud processing complete</span>
			{:else}
				<span class="badge">جارٍ اللحاق / Catching up</span>
			{/if}
		</div>
		{#if health.last_error_code}
			<div class="muted">آخر خطأ: {health.last_error_label_ar ?? health.last_error_code}<br /><span class="sub-en">{health.last_error_label_en ?? ''}</span></div>
		{/if}
		<div class="muted note">
			الاكتمال السحابي لا يعني أن درج مكتب المتجر فارغ — السحابة لا ترى الأحداث التي لم تُرسل بعد.
			<br /><span class="sub-en">Cloud-complete does not prove the Retail outbox is empty.</span>
		</div>
		<button type="button" class="refresh" onclick={onrefresh}>تحديث الحالة / Refresh status</button>
	{/if}
</Card>

<style>
	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
		gap: 12px;
		margin-top: 8px;
	}
	.big {
		font-size: 1.2rem;
		font-weight: 700;
	}
	.status {
		margin-top: 10px;
	}
	.note {
		margin-top: 8px;
		font-size: 0.82rem;
	}
	.refresh {
		margin-top: 10px;
		background: var(--color-primary);
		color: #fff;
		border: 0;
		border-radius: 6px;
		padding: 7px 14px;
	}
</style>
