<script lang="ts">
	import { onMount, type Snippet } from 'svelte';

	// A centered dialog over a dimmed page. Escape and the backdrop close it;
	// focus moves into the dialog and returns to the opener on close.
	let {
		title,
		onclose,
		wide = false,
		closeLabel,
		children,
		testid
	}: { title: string; onclose: () => void; wide?: boolean; closeLabel: string; children: Snippet; testid?: string } = $props();

	let dialog: HTMLDivElement | undefined = $state();
	const titleId = `modal-${Math.random().toString(36).slice(2, 10)}`;

	onMount(() => {
		const opener = document.activeElement as HTMLElement | null;
		const first = dialog?.querySelector<HTMLElement>('input, select, textarea, button:not(.modal-close)') ?? dialog;
		first?.focus();
		return () => opener?.focus?.();
	});

	function keydown(e: KeyboardEvent) {
		if (e.key === 'Escape') {
			e.stopPropagation();
			onclose();
			return;
		}
		if (e.key !== 'Tab' || !dialog) return;
		// Keep Tab inside the dialog.
		const items = Array.from(dialog.querySelectorAll<HTMLElement>('a[href], button:not([disabled]), input:not([disabled]), select, textarea, summary, [tabindex]:not([tabindex="-1"])'));
		if (items.length === 0) return;
		const first = items[0];
		const last = items[items.length - 1];
		if (e.shiftKey && document.activeElement === first) {
			e.preventDefault();
			last.focus();
		} else if (!e.shiftKey && document.activeElement === last) {
			e.preventDefault();
			first.focus();
		}
	}
</script>

<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
<div class="backdrop" onclick={(e) => e.target === e.currentTarget && onclose()}>
	<div class="dialog card" class:wide role="dialog" aria-modal="true" aria-labelledby={titleId} tabindex="-1" bind:this={dialog} onkeydown={keydown} data-testid={testid}>
		<header>
			<h3 id={titleId}>{title}</h3>
			<button type="button" class="modal-close" aria-label={closeLabel} onclick={onclose}>×</button>
		</header>
		<div class="content">{@render children()}</div>
	</div>
</div>

<style>
	.backdrop {
		position: fixed;
		inset: 0;
		z-index: 60;
		display: grid;
		place-items: center;
		padding: 16px;
		background: rgb(15 23 42 / 0.45);
	}
	.dialog {
		width: min(26rem, 100%);
		max-height: calc(100vh - 32px);
		display: flex;
		flex-direction: column;
		padding: 0;
		outline: none;
	}
	.dialog.wide {
		width: min(52rem, 100%);
	}
	header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 8px;
		padding: 14px 18px;
		border-bottom: 1px solid var(--border);
	}
	h3 {
		margin: 0;
		font-size: 1.02rem;
	}
	.modal-close {
		border: 0;
		background: transparent;
		font-size: 1.4rem;
		line-height: 1;
		cursor: pointer;
		color: var(--text-muted);
		padding: 0 4px;
	}
	.content {
		padding: 14px 18px 18px;
		overflow: auto;
	}
</style>
