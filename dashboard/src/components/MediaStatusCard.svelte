<script lang="ts">
	import Card from './Card.svelte';
	import Skeleton from './Skeleton.svelte';
	import WidgetError from './WidgetError.svelte';
	import { lang, msg } from '../lib/i18n.svelte.js';
	import { relativeTime } from '../lib/time.js';
	import { formatInt } from '../lib/money.js';
	import type { MediaStatus } from '../lib/api.js';

	// The product image store (Cloudflare R2 in production): Cloud checks the
	// newest file with a HEAD request and counts the files of the scope.
	let {
		data,
		status,
		errStatus,
		onretry,
		oncheck
	}: {
		data: MediaStatus | null;
		status: 'loading' | 'loaded' | 'empty' | 'error';
		errStatus: number | null;
		onretry: () => void;
		oncheck: () => void;
	} = $props();

	function bytes(n: number): string {
		if (n < 1024) return `${n} B`;
		if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
		if (n < 1024 * 1024 * 1024) return `${(n / (1024 * 1024)).toFixed(1)} MB`;
		return `${(n / (1024 * 1024 * 1024)).toFixed(2)} GB`;
	}
	const tone = { ok: 'ok', empty: 'idle', missing: 'warn', error: 'bad' } as const;
	const icon = {
		ok: 'M4 12.5l5 5L20 6.5',
		empty: 'M4 5h16v14H4zM4 15l4-4 5 5 3-3 4 4M15 9h.01',
		missing: 'M12 8v5M12 16.5v.5M10.3 3.9 2.6 17.5A2 2 0 0 0 4.3 20.5h15.4a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z',
		error: 'M6 6l12 12M18 6 6 18'
	} as const;
</script>

<Card ar={msg('mediaStatusCard.title')}>
	{#snippet actions()}
		{#if status === 'loaded'}
			<button type="button" class="btn-secondary btn-sm" onclick={oncheck}>{msg('mediaStatusCard.recheck')}</button>
		{/if}
	{/snippet}
	{#if status === 'loading'}
		<Skeleton />
	{:else if status === 'error' || !data}
		<WidgetError status={errStatus} {onretry} />
	{:else}
		<div class="state {tone[data.check]}" role="status" data-testid="media-check" data-check={data.check}>
			<span class="ic" aria-hidden="true"><svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><path d={icon[data.check]} /></svg></span>
			<div>
				<div class="head">
					<strong>{data.storage === 'r2' || data.storage === 'local' ? msg(`mediaStatusCard.storage.${data.storage}`) : data.storage}</strong>
					<span class="pill">{msg(`mediaStatusCard.check.${data.check}`)}</span>
				</div>
				<p class="hint">{msg(`mediaStatusCard.hint.${data.check}`)}</p>
				{#if data.check !== 'empty'}
					<p class="muted small">{msg('mediaStatusCard.checkedAt', { time: relativeTime(data.checked_at, lang()), ms: formatInt(data.check_ms) })}</p>
				{/if}
			</div>
		</div>
		{#if data.storage === 'r2' && data.bucket_usage_state}
			<section class="bucket" aria-label={msg('mediaStatusCard.bucketUsage')} data-testid="bucket-usage">
				<strong>{msg('mediaStatusCard.bucketUsage')}</strong>
				{#if data.bucket_usage}
					<p class="num">{msg(data.bucket_usage.complete ? 'mediaStatusCard.bucketTotals' : 'mediaStatusCard.bucketPartial', { count: formatInt(data.bucket_usage.files) })} · <bdi dir="ltr">{bytes(data.bucket_usage.bytes)}</bdi>{#if !data.bucket_usage.complete} ({msg('mediaStatusCard.partialListing')}){/if}</p>
				{:else}<p class="muted small">{msg(data.bucket_usage_state === 'scope_limited' ? 'mediaStatusCard.bucketScoped' : 'mediaStatusCard.bucketUnavailable')}</p>{/if}
			</section>
		{/if}
		<details class="diagnostics" data-testid="media-details"><summary>{msg('mediaStatusCard.details')}</summary>
		<dl class="tiles">
			<div>
				<dt>{msg('mediaStatusCard.stored')}</dt>
				<dd class="num">{formatInt(data.stored_files)} <span class="muted small" dir="ltr">{bytes(data.stored_bytes)}</span></dd>
			</div>
			<div>
				<dt>{msg('mediaStatusCard.uploadedInPeriod')}</dt>
				<dd class="num">{formatInt(data.uploaded_in_range)}</dd>
			</div>
			<div>
				<dt>{msg('mediaStatusCard.lastUpload')}</dt>
				<dd>{data.last_upload_at ? relativeTime(data.last_upload_at, lang()) : msg('mediaStatusCard.none')}</dd>
			</div>
			<div class:warnbox={data.waiting_images > 0}>
				<dt>{msg('mediaStatusCard.waiting')}</dt>
				<dd class="num">{formatInt(data.waiting_images)} <span class="muted small">/ {formatInt(data.gallery_images)}</span></dd>
			</div>
		</dl>
		</details>
	{/if}
</Card>

<style>
	.state {
		--tone: var(--success);
		--soft: #e8f6ee;
		display: flex;
		gap: 10px;
		align-items: flex-start;
		padding: 10px 12px;
		border-radius: var(--radius-card);
		background: var(--soft);
	}
	.state.idle {
		--tone: var(--text-muted);
		--soft: var(--surface-muted);
	}
	.state.warn {
		--tone: var(--warning);
		--soft: #fdf4e7;
	}
	.state.bad {
		--tone: var(--danger);
		--soft: #fdeeee;
	}
	.ic {
		flex: none;
		width: 34px;
		height: 34px;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		border-radius: 50%;
		background: var(--tone);
		color: #fff;
	}
	.head {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 8px;
	}
	.pill {
		padding: 1px 10px;
		border-radius: 999px;
		background: var(--tone);
		color: #fff;
		font-size: 0.74rem;
		font-weight: 700;
	}
	.hint {
		margin: 4px 0 0;
		font-size: 0.82rem;
	}
	.small {
		font-size: 0.74rem;
		font-weight: 400;
		margin: 2px 0 0;
	}
	.diagnostics { margin-top: 12px; }
	.diagnostics summary { cursor: pointer; font-size: .8rem; font-weight: 600; }
	.bucket { padding: 12px; margin-top: 10px; border: 1px solid var(--border); border-radius: var(--radius-control); }
	.bucket p { margin: 4px 0 0; }
	.tiles {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(130px, 1fr));
		gap: 8px;
		margin: 10px 0 0;
	}
	.tiles div {
		padding: 8px 10px;
		border: 1px solid var(--border);
		border-radius: var(--radius-control);
	}
	.tiles .warnbox {
		background: #fdf4e7;
	}
	dt {
		font-size: 0.72rem;
		color: var(--text-muted);
	}
	dd {
		margin: 2px 0 0;
		font-weight: 800;
		font-size: 0.95rem;
	}
</style>
