<script lang="ts">
	import { onMount } from 'svelte';
	import Sidebar from './components/Sidebar.svelte';
	import PeriodSelector from './components/PeriodSelector.svelte';
	import SalesCard from './components/SalesCard.svelte';
	import TrendChart from './components/TrendChart.svelte';
	import TopProducts from './components/TopProducts.svelte';
	import type { ProductDisplayRow } from './components/TopProducts.svelte';
	import CategoryCard from './components/CategoryCard.svelte';
	import type { CategoryDisplayRow } from './components/CategoryCard.svelte';
	import BranchCard from './components/BranchCard.svelte';
	import SyncHealthCard from './components/SyncHealthCard.svelte';
	import ActivityCard from './components/ActivityCard.svelte';
	import LatestSalesCard from './components/LatestSalesCard.svelte';
	import LoginPage from './components/LoginPage.svelte';
	import { dashboardApi, ApiError } from './lib/api.js';
	import type { PeriodParams, OverviewResponse, DailyRow, BranchRow, SyncHealth, ActivityItem, LatestSale } from './lib/api.js';
	import { toChartNumber } from './lib/money.js';

	type WidgetState = 'loading' | 'loaded' | 'empty' | 'error';

	let authed: boolean | null = $state(null);
	let route = $state('overview');
	let params: PeriodParams = $state({ period: 'last_10_completed_days' });
	let currency: 'all' | 'EGP' | 'USD' = $state('all');
	let fatal = $state<string | null>(null);

	let aborters: AbortController[] = [];
	function freshSignal(): AbortSignal {
		for (const c of aborters) c.abort();
		aborters = [];
		const c = new AbortController();
		aborters.push(c);
		return c.signal;
	}

	function readRoute() {
		const path = window.location.pathname.replace(/^\/dashboard\/?/, '') || 'overview';
		const map: Record<string, string> = {
			'': 'overview',
			overview: 'overview',
			sales: 'sales',
			daily: 'daily',
			products: 'products',
			categories: 'categories',
			sync: 'sync'
		};
		route = map[path] ?? 'overview';
		const q = new URLSearchParams(window.location.search);
		const p = q.get('period');
		if (p) params = { period: p, from_date: q.get('from_date') ?? undefined, to_date: q.get('to_date') ?? undefined };
		const c = q.get('currency');
		if (c === 'EGP' || c === 'USD' || c === 'all') currency = c;
	}

	function navigate(r: string) {
		window.history.pushState({}, '', '/dashboard/' + (r === 'overview' ? '' : r) + window.location.search);
		readRoute();
		void reloadAll();
	}

	function syncUrl() {
		const q = new URLSearchParams();
		q.set('period', params.period);
		if (params.from_date) q.set('from_date', params.from_date);
		if (params.to_date) q.set('to_date', params.to_date);
		q.set('currency', currency);
		window.history.replaceState({}, '', window.location.pathname + '?' + q.toString());
	}

	function onParams(p: PeriodParams) {
		params = p;
		syncUrl();
		void reloadAll();
	}

	async function checkSession() {
		try {
			await dashboardApi.me();
			authed = true;
		} catch {
			authed = false;
		}
	}

	function requireAuth(err: unknown): boolean {
		if (err instanceof ApiError && err.status === 401) {
			authed = false;
			return true;
		}
		return false;
	}

	// ---- widget data ----
	let overview: OverviewResponse | null = $state(null);
	let overviewState: WidgetState = $state('loading');
	let daily: DailyRow[] = $state([]);
	let dailyState: WidgetState = $state('loading');
	let products: ProductDisplayRow[] = $state([]);
	let productsState: WidgetState = $state('loading');
	let categories: CategoryDisplayRow[] = $state([]);
	let catKind: 'root' | 'subcategory' = $state('root');
	let categoriesState: WidgetState = $state('loading');
	let branches: BranchRow[] = $state([]);
	let branchesState: WidgetState = $state('loading');
	let syncHealth: SyncHealth | null = $state(null);
	let syncState: WidgetState = $state('loading');
	let activity: ActivityItem[] = $state([]);
	let activityState: WidgetState = $state('loading');
	let latest: LatestSale[] = $state([]);
	let latestState: WidgetState = $state('loading');

	function emptyOf<T>(v: T[] | null | undefined): WidgetState {
		if (!v) return 'error';
		return v.length === 0 ? 'empty' : 'loaded';
	}

	async function reloadAll() {
		if (!authed) return;
		fatal = null;
		const signal = freshSignal();
		overviewState = dailyState = productsState = categoriesState = branchesState = syncState = activityState = latestState =
			'loading';
		const done = async <T>(p: Promise<T>, apply: (v: T) => void, setState: (s: WidgetState) => void) => {
			try {
				apply(await p);
			} catch (err) {
				if (err instanceof DOMException && err.name === 'AbortError') return;
				if (requireAuth(err)) return;
				setState('error');
			}
		};
		const mode = currency === 'all' ? 'all' : 'native';
		await Promise.all([
			done(dashboardApi.overview(params, signal), (v) => {
				overview = v;
				overviewState = v.summary.transaction_count === 0 ? 'empty' : 'loaded';
			}, (s) => (overviewState = s)),
			done(dashboardApi.daily(params, signal), (v) => {
				daily = v.days;
				dailyState = emptyOf(v.days);
			}, (s) => (dailyState = s)),
			done(dashboardApi.products(params, mode, currency === 'all' ? '' : currency, signal), (v) => {
				products = v.rows.map((r) => ({
					name: r.product_name,
					sku: r.sku,
					units: r.units,
					amount_minor: r.amount_minor
				}));
				productsState = emptyOf(products);
			}, (s) => (productsState = s)),
			done(dashboardApi.categories(params, catKind, mode, currency === 'all' ? '' : currency, signal), (v) => {
				categories = v.rows.map((r) => ({
					name: r.name_en ? `${r.name_ar} / ${r.name_en}` : r.name_ar,
					units: r.units,
					amount_minor: r.amount_minor
				}));
				categoriesState = emptyOf(categories);
			}, (s) => (categoriesState = s)),
			done(dashboardApi.branches(params, signal), (v) => {
				branches = v.rows;
				branchesState = emptyOf(v.rows);
			}, (s) => (branchesState = s)),
			done(dashboardApi.syncHealth(signal), (v) => {
				syncHealth = v;
				syncState = 'loaded';
			}, (s) => (syncState = s)),
			done(dashboardApi.activity(signal), (v) => {
				activity = v.items;
				activityState = emptyOf(v.items);
			}, (s) => (activityState = s)),
			done(dashboardApi.latestSales(signal), (v) => {
				latest = v.sales;
				latestState = emptyOf(v.sales);
			}, (s) => (latestState = s))
		]);
	}

	function trendUnit(): string {
		return currency === 'all' ? 'EGP normalized' : currency;
	}

	function trendData(): { labels: string[]; values: number[] } {
		try {
			return {
				labels: daily.map((d) => d.date),
				values: daily.map((d) => toChartNumber(d.normalized_minor))
			};
		} catch {
			dailyState = 'error';
			return { labels: [], values: [] };
		}
	}

	function setMode(m: 'all' | 'EGP' | 'USD') {
		currency = m;
		syncUrl();
		void reloadAll();
	}

	function setCatKind(k: 'root' | 'subcategory') {
		catKind = k;
		void reloadAll();
	}

	async function logout() {
		await dashboardApi.logout();
		authed = false;
	}

	onMount(() => {
		readRoute();
		window.addEventListener('popstate', () => {
			readRoute();
			void reloadAll();
		});
		void (async () => {
			await checkSession();
			if (authed) await reloadAll();
		})();
	});
</script>

{#if authed === null}
	<div class="muted" style="padding: 24px;">جارٍ التحميل… / Loading…</div>
{:else if !authed}
	<LoginPage onlogin={() => ((authed = true), reloadAll())} />
{:else}
	<div class="shell">
		<div class="sidewrap"><Sidebar {route} {navigate} /></div>
		<main class="main">
			<header class="topbar">
				<div>
					<h1>لوحة متابعة المبيعات</h1>
					<div class="muted">MoonLight sales dashboard · {overview?.timezone ?? ''}</div>
				</div>
				<button type="button" class="logout" onclick={logout}>خروج / Logout</button>
			</header>
			{#if fatal}<div role="alert">{fatal}</div>{/if}
			<PeriodSelector {params} timezone={overview?.timezone ?? 'Africa/Cairo'} onchange={onParams} />
			{#if route === 'overview' || route === 'sales'}
				<SalesCard data={overview} mode={currency} onmode={setMode} status={overviewState} />
			{/if}
			{#if route === 'overview' || route === 'sales' || route === 'daily'}
				<div class="grid">
					<TrendChart
						titleAr="الاتجاه اليومي للمبيعات"
						titleEn="Sales over time"
						labels={trendData().labels}
						values={trendData().values}
						unit={trendUnit()}
						status={dailyState}
					/>
				</div>
			{/if}
			{#if route === 'overview' || route === 'products'}
				<TopProducts rows={products} money={currency === 'all' ? 'EGP' : currency} unit={currency === 'all' ? 'EGP normalized' : currency} status={productsState} />
			{/if}
			{#if route === 'overview' || route === 'categories'}
				<div class="kindswitch">
					<button type="button" class:active={catKind === 'root'} onclick={() => setCatKind('root')}>الفئات الرئيسية / Roots</button>
					<button type="button" class:active={catKind === 'subcategory'} onclick={() => setCatKind('subcategory')}>الفئات الفرعية / Subcategories</button>
				</div>
				<CategoryCard rows={categories} kind={catKind} money={currency === 'all' ? 'EGP' : currency} unit={currency === 'all' ? 'EGP normalized' : currency} status={categoriesState} />
			{/if}
			{#if route === 'overview' || route === 'sales'}
				<BranchCard rows={branches} status={branchesState} />
			{/if}
			{#if route === 'overview' || route === 'sync'}
				<SyncHealthCard health={syncHealth} status={syncState} onrefresh={() => reloadAll()} />
				<ActivityCard items={activity} status={activityState} />
				<LatestSalesCard sales={latest} status={latestState} />
			{/if}
		</main>
	</div>
{/if}

<style>
	.shell {
		display: flex;
		min-height: 100vh;
	}
	.sidewrap {
		width: 240px;
		flex-shrink: 0;
	}
	.main {
		flex: 1;
		padding: 20px 24px;
		max-width: 1200px;
		display: flex;
		flex-direction: column;
		gap: 16px;
	}
	.topbar {
		display: flex;
		justify-content: space-between;
		align-items: center;
	}
	.topbar h1 {
		margin: 0;
		font-size: 1.4rem;
	}
	.logout {
		background: transparent;
		border: 1px solid var(--border);
		border-radius: 6px;
		padding: 7px 14px;
	}
	.grid {
		display: grid;
		gap: 16px;
	}
	.kindswitch {
		display: inline-flex;
		gap: 4px;
	}
	.kindswitch button {
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: 6px;
		padding: 6px 12px;
		color: var(--color-muted);
	}
	.kindswitch button.active {
		background: var(--color-primary);
		color: #fff;
		border-color: var(--color-primary);
	}
	@media (max-width: 900px) {
		.shell {
			flex-direction: column;
		}
		.sidewrap {
			width: 100%;
		}
		.main {
			padding: 12px;
		}
	}
</style>
