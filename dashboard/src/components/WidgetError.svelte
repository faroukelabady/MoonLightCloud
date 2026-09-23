<script lang="ts">
	let { status, onretry }: { status: number | null; onretry: () => void } = $props();
</script>

<div class="werr" role="alert">
	{#if status === 503}
		<div>خدمة التقارير غير متاحة مؤقتًا</div>
		<div class="muted sub-en">Reporting is temporarily unavailable</div>
		<button type="button" onclick={onretry}>إعادة المحاولة / Retry</button>
	{:else}
		<div>تعذر تحميل البيانات</div>
		<div class="muted sub-en">Could not load data</div>
	{/if}
</div>

<style>
	.werr {
		padding: 12px 4px;
	}
	.werr button {
		margin-top: 8px;
		background: var(--color-primary);
		color: #fff;
		border: 0;
		border-radius: 6px;
		padding: 7px 14px;
	}
</style>
