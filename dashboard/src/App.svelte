<script lang="ts">
	import { onMount } from 'svelte';
	import Sidebar from './components/Sidebar.svelte';
	import PeriodSelector from './components/PeriodSelector.svelte';
	import SalesCard from './components/SalesCard.svelte';
	import MetricCard from './components/MetricCard.svelte';
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
	import type { PeriodParams, OverviewResponse, BranchRow, SyncHealth, ActivityItem, LatestSale, DailyMode, BreakdownMode, CategoryKind } from './lib/api.js';
	import { toChartNumber, formatInt } from './lib/money.js';

	type WidgetState = 'loading' | 'loaded' | 'empty' | 'error';

	let authed: boolean | null = $state(null);
	let operatorName = $state('');
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

	function urlFor(): string {
		const q = new URLSearchParams();
		q.set('period', params.period);
		if (params.from_date) q.set('from_date', params.from_date);
		if (params.to_date) q.set('to_date', params.to_date);
		q.set('currency', currency);
		return window.location.pathname + '?' + q.toString();
	}

	function onParams(p: PeriodParams) {
		params = p;
		// Deliberate filter application: push so Back/Forward restores it.
		window.history.pushState({}, '', urlFor());
		void reloadAll();
	}

	async function checkSession() {
		try {
			const me = await dashboardApi.me();
			operatorName = me.username ?? '';
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
	let trend: { labels: string[]; values: number[] } = $state({ labels: [], values: [] });
	let trendRangeError = $state(false);
	let trendExact: { date: string; amount_minor: string }[] = $state([]);
	let dailyMeta: { display_currency: string; normalized: boolean } = $state({ display_currency: 'EGP', normalized: true });
	let dailyState: WidgetState = $state('loading');
	let products: ProductDisplayRow[] = $state([]);
	let productsState: WidgetState = $state('loading');
	let categories: CategoryDisplayRow[] = $state([]);
	let catKind: CategoryKind = $state('root_category');
	let categoriesState: WidgetState = $state('loading');
	let branches: BranchRow[] = $state([]);
	let branchesState: WidgetState = $state('loading');
	let syncHealth: SyncHealth | null = $state(null);
	let syncState: WidgetState = $state('loading');
	let activity: ActivityItem[] = $state([]);
	let activityState: WidgetState = $state('loading');
	let latest: LatestSale[] = $state([]);
	let latestState: WidgetState = $state('loading');
	let overviewErr: number | null = $state(null);
	let dailyErr: number | null = $state(null);
	let productsErr: number | null = $state(null);
	let categoriesErr: number | null = $state(null);
	let branchesErr: number | null = $state(null);
	let syncErr: number | null = $state(null);
	let activityErr: number | null = $state(null);
	let latestErr: number | null = $state(null);
	function retryAll() {
		void reloadAll();
	}

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
		const done = async <T>(
			p: Promise<T>,
			apply: (v: T) => void,
			setState: (s: WidgetState) => void,
			setErr: (n: number | null) => void
		) => {
			try {
				apply(await p);
			} catch (err) {
				if (err instanceof DOMException && err.name === 'AbortError') return;
				if (requireAuth(err)) return;
				setState('error');
				setErr(err instanceof ApiError ? err.status : 0);
			}
		};
		const dailyMode: DailyMode = currency;
		const breakdownMode: BreakdownMode = currency === 'all' ? 'all' : 'native';
		await Promise.all([
			done(dashboardApi.overview(params, signal), (v) => {
				overview = v;
				overviewState = v.summary.transaction_count === 0 ? 'empty' : 'loaded';
			}, (s) => (overviewState = s), (n) => (overviewErr = n)),
			done(dashboardApi.daily(params, dailyMode, signal), (v) => {
				dailyMeta = { display_currency: v.display_currency, normalized: v.normalized };
				trendExact = v.days.map((d) => ({ date: d.date, amount_minor: d.amount_minor }));
				try {
					trend = {
						labels: v.days.map((d) => d.date),
						values: v.days.map((d) => toChartNumber(d.amount_minor))
					};
					trendRangeError = false;
				} catch {
					// Unsafe chart magnitude: exact daily data stays
					// available below; only visualization is limited.
					trend = { labels: [], values: [] };
					trendRangeError = true;
				}
				dailyState = emptyOf(v.days);
			}, (s) => (dailyState = s), (n) => (dailyErr = n)),
			done(dashboardApi.products(params, breakdownMode, currency === 'all' ? '' : currency, signal), (v) => {
				products = v.rows.map((r) => ({
					name: r.product_name,
					sku: r.sku,
					units: r.units,
					amount_minor: r.amount_minor
				}));
				productsState = emptyOf(products);
			}, (s) => (productsState = s), (n) => (productsErr = n)),
			done(dashboardApi.categories(params, catKind, breakdownMode, currency === 'all' ? '' : currency, signal), (v) => {
				categories = v.rows.map((r) => ({
					name: r.name_en ? `${r.name_ar} / ${r.name_en}` : r.name_ar,
					units: r.units,
					amount_minor: r.amount_minor
				}));
				categoriesState = emptyOf(categories);
			}, (s) => (categoriesState = s), (n) => (categoriesErr = n)),
			done(dashboardApi.branches(params, signal), (v) => {
				branches = v.rows;
				branchesState = emptyOf(v.rows);
			}, (s) => (branchesState = s), (n) => (branchesErr = n)),
			done(dashboardApi.syncHealth(signal), (v) => {
				syncHealth = v;
				syncState = 'loaded';
			}, (s) => (syncState = s), (n) => (syncErr = n)),
			done(dashboardApi.activity(signal), (v) => {
				activity = v.items;
				activityState = emptyOf(v.items);
			}, (s) => (activityState = s), (n) => (activityErr = n)),
			done(dashboardApi.latestSales(signal), (v) => {
				latest = v.sales;
				latestState = emptyOf(v.sales);
			}, (s) => (latestState = s), (n) => (latestErr = n))
		]);
	}

	function trendUnit(): string {
		return dailyMeta.normalized ? `${dailyMeta.display_currency} normalized` : dailyMeta.display_currency;
	}

	function setMode(m: 'all' | 'EGP' | 'USD') {
		currency = m;
		// Deliberate selection: push so Back/Forward restores it.
		window.history.pushState({}, '', urlFor());
		void reloadAll();
	}

	function setCatKind(k: CategoryKind) {
		catKind = k;
		void reloadAll();
	}

	async function logout() {
		await dashboardApi.logout();
		authed = false;
	}

	// KPI cards follow the same active-currency scoping as the reporting
	// service: All uses the whole-period summary, EGP/USD use their own
	// per-mode averages (own transaction counts — never mixed).
	let metricTxn = $derived.by(() => {
		if (!overview) return '—';
		if (currency === 'all') return formatInt(overview.summary.transaction_count);
		const avg = currency === 'EGP' ? overview.averages.egp : overview.averages.usd;
		return formatInt(avg.transactions);
	});
	let metricUnits = $derived.by(() => {
		if (!overview) return '—';
		if (currency === 'all') return formatInt(overview.summary.units_sold);
		const avg = currency === 'EGP' ? overview.averages.egp : overview.averages.usd;
		return formatInt(avg.units);
	});
	let scopeLabel = $derived(currency === 'all' ? 'النطاق: الكل' : `النطاق: ${currency}`);

	let branchContext = $derived.by(() => {
		if (branchesState === 'loading') return '…';
		if (branches.length === 0) return 'لا توجد فروع في هذه الفترة';
		const first = branches[0];
		const extra = branches.length > 1 ? ` +${branches.length - 1}` : '';
		return `${first.shop_name_ar}${extra}`;
	});

	let periodRange = $derived.by(() => {
		if (!overview) return '';
		const from = overview.period.start_local.slice(0, 10);
		const to = overview.period.end_local_exclusive.slice(0, 10);
		return `${from} → ${to}`;
	});

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
	<div class="muted pad">جارٍ التحميل… / Loading…</div>
{:else if !authed}
	<LoginPage onlogin={() => ((authed = true), reloadAll())} />
{:else}
	<div class="shell">
		<div class="sidewrap"><Sidebar {route} {navigate} /></div>
		<main class="main">
			<header class="topbar">
				<div class="brandblock">
					<div class="brandname">MoonLightCloud</div>
					<h1 class="dash-title">لوحة متابعة المبيعات</h1>
					<div class="muted brandsub">Your papyrus business insights in one place</div>
				</div>
				<div class="periodblock">
					<PeriodSelector {params} timezone={overview?.timezone ?? 'Africa/Cairo'} onchange={onParams} />
					{#if periodRange}<div class="muted range num" dir="ltr">{periodRange}</div>{/if}
				</div>
				<div class="actorblock">
					{#if branchContext}<div class="branchctx" title={branchContext}>{branchContext}</div>{/if}
					{#if operatorName}<div class="muted op">{operatorName}</div>{/if}
					<span class="langfuture" title="تبديل اللغة الكامل قريبًا / Full language switching is coming soon" aria-disabled="true">
						<svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="9" /><path d="M3 12h18M12 3a15 15 0 0 1 0 18M12 3a15 15 0 0 0 0 18" /></svg>
						English
					</span>
					<button type="button" class="logout" onclick={logout}>خروج / Logout</button>
				</div>
			</header>
			{#if fatal}<div role="alert">{fatal}</div>{/if}
			<div class="dash">
				{#if route === 'overview' || route === 'sales'}
					<div class="cell a-kpis">
						<MetricCard ar="عدد المعاملات" en="Transactions" value={metricTxn} context={scopeLabel} status={overviewState} icon="M4 7h13l-3-3M20 17H7l3 3" />
						<MetricCard ar="الوحدات المباعة" en="Units Sold" value={metricUnits} context={scopeLabel} status={overviewState} icon="M12 3l8 4.5v9L12 21l-8-4.5v-9L12 3zM12 12l8-4.5M12 12L4 7.5M12 12v9" />
					</div>
					<div class="cell a-sales">
						<SalesCard data={overview} mode={currency} onmode={setMode} status={overviewState} errStatus={overviewErr} onretry={retryAll} />
					</div>
				{/if}
				{#if route === 'overview' || route === 'sync'}
					<div class="cell a-sync">
						<SyncHealthCard health={syncHealth} status={syncState} errStatus={syncErr} onretry={retryAll} onrefresh={() => reloadAll()} />
					</div>
				{/if}
				{#if route === 'overview'}
					<div class="cell a-kindwide">
						<div class="kindswitch" role="group" aria-label="نوع الفئات / Category kind">
							<button type="button" class:active={catKind === 'root_category'} onclick={() => setCatKind('root_category')}>الفئات الرئيسية / Roots</button>
							<button type="button" class:active={catKind === 'subcategory'} onclick={() => setCatKind('subcategory')}>الفئات الفرعية / Subcategories</button>
						</div>
					</div>
				{/if}
				{#if route === 'overview' || route === 'categories'}
					{#if route !== 'overview'}
						<div class="cell a-kind">
							<div class="kindswitch" role="group" aria-label="نوع الفئات / Category kind">
								<button type="button" class:active={catKind === 'root_category'} onclick={() => setCatKind('root_category')}>الفئات الرئيسية / Roots</button>
								<button type="button" class:active={catKind === 'subcategory'} onclick={() => setCatKind('subcategory')}>الفئات الفرعية / Subcategories</button>
							</div>
						</div>
					{/if}
					<div class="cell a-cat">
						<CategoryCard rows={categories} kind={catKind} unit={currency === 'all' ? 'EGP normalized' : currency} status={categoriesState} errStatus={categoriesErr} onretry={retryAll} />
					</div>
				{/if}
				{#if route === 'overview' || route === 'products'}
					<div class="cell a-products">
						<TopProducts rows={products} money={currency === 'all' ? 'EGP' : currency} unit={currency === 'all' ? 'EGP normalized' : currency} status={productsState} errStatus={productsErr} onretry={retryAll} />
					</div>
				{/if}
				{#if route === 'overview' || route === 'sales' || route === 'daily'}
					<div class="cell a-trend">
						<TrendChart
							titleAr="الاتجاه اليومي للمبيعات"
							titleEn="Sales over time"
							labels={trend.labels}
							values={trend.values}
							exact={trendExact}
							rangeError={trendRangeError}
							unit={trendUnit()}
							status={dailyState}
							errStatus={dailyErr}
							onretry={retryAll}
							mode={currency}
							onmode={setMode}
						/>
					</div>
				{/if}
				{#if route === 'overview' || route === 'sales'}
					<div class="cell a-branch">
						<BranchCard rows={branches} status={branchesState} errStatus={branchesErr} onretry={retryAll} />
					</div>
				{/if}
				{#if route === 'overview' || route === 'sync'}
					<div class="cell a-latest">
						<LatestSalesCard sales={latest} status={latestState} errStatus={latestErr} onretry={retryAll} />
					</div>
				{/if}
				{#if route === 'overview' || route === 'sync'}
					<div class="cell a-act">
						<ActivityCard items={activity} status={activityState} errStatus={activityErr} onretry={retryAll} />
					</div>
				{/if}
			</div>
		</main>
	</div>
{/if}

<style>
	.shell {
		display: flex;
		min-height: 100vh;
	}
	.sidewrap {
		width: 248px;
		flex-shrink: 0;
	}
	.main {
		flex: 1;
		min-width: 0;
		padding: 0 16px 20px;
		display: flex;
		flex-direction: column;
		gap: 12px;
	}
	.topbar {
		display: flex;
		align-items: center;
		gap: 16px;
		background: var(--surface);
		border-bottom: 1px solid var(--border);
		margin: 0 -16px;
		padding: 10px 16px;
	}
	.brandblock {
		flex-shrink: 0;
	}
	.brandname {
		font-weight: 700;
		font-size: 1rem;
		color: var(--primary);
	}
	.dash-title {
		margin: 0;
		font-size: 0.85rem;
		font-weight: 700;
	}
	.brandsub {
		font-size: 0.72rem;
		line-height: 1.4;
	}
	.periodblock {
		flex: 1;
		min-width: 0;
	}
	.range {
		font-size: 0.75rem;
		margin-top: 2px;
	}
	.actorblock {
		display: flex;
		align-items: center;
		gap: 10px;
		flex-shrink: 0;
	}
	.branchctx {
		font-size: 0.82rem;
		font-weight: 600;
		max-width: 180px;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.op {
		font-size: 0.78rem;
	}
	.langfuture {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		font-size: 0.8rem;
		color: var(--text-muted);
		border: 1px solid var(--border);
		border-radius: var(--radius-control);
		padding: 5px 10px;
		opacity: 0.6;
		cursor: not-allowed;
	}
	.logout {
		background: transparent;
		border: 1px solid var(--border);
		border-radius: var(--radius-control);
		padding: 6px 12px;
		font-size: 0.82rem;
	}
	.dash {
		display: grid;
		grid-template-columns: repeat(12, 1fr);
		gap: 12px;
		align-items: start;
	}
	.cell {
		min-width: 0;
	}
	.a-sync { grid-column: span 3; }
	.a-sales { grid-column: span 6; }
	.a-kpis {
		grid-column: span 3;
		display: flex;
		flex-direction: column;
		gap: 12px;
	}
	.a-trend { grid-column: span 4; }
	.a-products { grid-column: span 4; }
	.a-cat { grid-column: span 4; }
	.a-kindwide { grid-column: span 12; }
	.a-kind { grid-column: span 12; }
	.a-act { grid-column: span 3; }
	.a-latest { grid-column: span 6; }
	.a-branch { grid-column: span 3; }
	.kindswitch {
		display: inline-flex;
		gap: 2px;
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: var(--radius-control);
		padding: 2px;
	}
	.kindswitch button {
		background: transparent;
		border: 0;
		border-radius: 4px;
		padding: 5px 12px;
		font-size: 0.8rem;
		color: var(--text-muted);
	}
	.kindswitch button.active {
		background: var(--primary);
		color: #fff;
		font-weight: 600;
	}
	@media (max-width: 1280px) {
		.a-sync { grid-column: span 4; }
		.a-sales { grid-column: span 8; }
		.a-kpis { grid-column: span 12; flex-direction: row; }
		.a-kpis > :global(*) { flex: 1; }
		.a-trend { grid-column: span 6; }
		.a-products { grid-column: span 6; }
		.a-cat { grid-column: span 12; }
		.a-act { grid-column: span 5; }
		.a-latest { grid-column: span 7; }
		.a-branch { grid-column: span 12; }
	}
	@media (max-width: 900px) {
		.shell {
			flex-direction: column;
		}
		.sidewrap {
			width: 100%;
		}
		.topbar {
			flex-wrap: wrap;
		}
		.periodblock {
			flex-basis: 100%;
			order: 3;
		}
		.dash > .cell {
			grid-column: span 12;
		}
		.a-kpis {
			flex-direction: column;
		}
	}
</style>
