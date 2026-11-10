<script lang="ts">
	import Card from './Card.svelte';
	import TrendChart from './TrendChart.svelte';
	import CategoryCard, { type CategoryDisplayRow } from './CategoryCard.svelte';
	import type { ComponentProps } from 'svelte';
	import type { CategoryKind } from '../lib/api.js';

	// Overview "Sales performance": how sales moved over time (gross, refunds
	// and net per day) beside where they came from (net by category), under
	// one header so the two views of the same period read together.
	let {
		trend,
		categories,
		kind,
		onkind,
		categoryUnit,
		money,
		categoriesStatus,
		categoriesErr,
		onretry
	}: {
		trend: Omit<ComponentProps<typeof TrendChart>, 'embedded'>;
		categories: CategoryDisplayRow[];
		kind: CategoryKind;
		onkind: (k: CategoryKind) => void;
		categoryUnit: string;
		money: 'EGP' | 'USD';
		categoriesStatus: 'loading' | 'loaded' | 'empty' | 'error';
		categoriesErr: number | null;
		onretry: () => void;
	} = $props();
</script>

<div class="perf" data-testid="performance-card">
	<Card ar="أداء المبيعات" en="Sales performance">
		<div class="panes">
			<div class="pane trend"><TrendChart {...trend} embedded /></div>
			<div class="pane cats">
				<CategoryCard
					rows={categories}
					{kind}
					{onkind}
					unit={categoryUnit}
					{money}
					status={categoriesStatus}
					errStatus={categoriesErr}
					{onretry}
					embedded
				/>
			</div>
		</div>
	</Card>
</div>

<style>
	.panes {
		display: grid;
		grid-template-columns: minmax(0, 1.25fr) minmax(0, 1fr);
		gap: 18px;
	}
	.pane.cats {
		border-inline-start: 1px solid var(--border, #e4e7ec);
		padding-inline-start: 18px;
	}
	@media (max-width: 1100px) {
		.panes {
			grid-template-columns: 1fr;
		}
		.pane.cats {
			border-inline-start: 0;
			padding-inline-start: 0;
			border-top: 1px solid var(--border, #e4e7ec);
			padding-top: 12px;
		}
	}
</style>
