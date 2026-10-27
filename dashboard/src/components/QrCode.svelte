<script lang="ts">
	import { encode } from 'uqr';

	// Renders a QR code as one SVG path from the encoded module matrix. Built
	// in the page (no image request, no inline styles), so the secret it
	// carries never leaves the browser and the strict CSP holds.
	let { value, label }: { value: string; label: string } = $props();

	let qr = $derived(encode(value, { ecc: 'M', border: 4 }));
	let path = $derived.by(() => {
		let d = '';
		qr.data.forEach((row, y) =>
			row.forEach((dark, x) => {
				if (dark) d += `M${x} ${y}h1v1h-1z`;
			})
		);
		return d;
	});
</script>

<svg class="qr" viewBox={`0 0 ${qr.size} ${qr.size}`} role="img" aria-label={label} shape-rendering="crispEdges" data-testid="totp-qr">
	<rect width={qr.size} height={qr.size} fill="#fff" />
	<path d={path} fill="#000" />
</svg>

<style>
	.qr {
		display: block;
		width: min(220px, 100%);
		height: auto;
		margin: 12px auto 4px;
		border: 1px solid var(--border);
		border-radius: var(--radius-control);
	}
</style>
