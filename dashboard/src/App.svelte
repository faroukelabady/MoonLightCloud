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
	import TagCard from './components/TagCard.svelte';
	import OnlineComparison from './components/OnlineComparison.svelte';
	import CatalogHealthCard from './components/CatalogHealthCard.svelte';
	import type { TagListResponse, OrderAnalyticsResponse, CatalogHealthResponse } from './lib/api.js';
	import SyncHealthCard from './components/SyncHealthCard.svelte';
	import ActivityCard from './components/ActivityCard.svelte';
	import LatestSalesCard from './components/LatestSalesCard.svelte';
	import OrdersPage from './components/OrdersPage.svelte';
	import DevicesPage from './components/DevicesPage.svelte';
	import OperationsPage from './components/OperationsPage.svelte';
	import CatalogAdminPage from './components/CatalogAdminPage.svelte';
	import UpdatesPage from './components/UpdatesPage.svelte';
	import UsersPage from './components/UsersPage.svelte';
	import LoginPage from './components/LoginPage.svelte';
	import { dashboardApi, ApiError, isStoreID } from './lib/api.js';
	import type { PeriodParams, OverviewResponse, BranchRow, SyncHealth, ActivityItem, LatestSale, DailyMode, BreakdownMode, CategoryKind, OrderSummary, OrderDetail, OrderStatusCount, WebhookInboxStats, StoreRow, Me, AuthStage } from './lib/api.js';
	import StoreSelector from './components/StoreSelector.svelte';
	import { toChartNumber, formatInt, subMinor } from './lib/money.js';

	type WidgetState = 'loading' | 'loaded' | 'empty' | 'error';

	let authed: boolean | null = $state(null);
	// Current human (ADR-0053): stage, permissions and Store access from
	// /auth/me, held in memory only. UX gating; the server re-checks all.
	let me: Me | null = $state(null);
	let pendingStage: AuthStage | null = $state(null);
	let operatorName = $state('');
	let route = $state('overview');
	let params: PeriodParams = $state({ period: 'last_10_completed_days' });
	let currency: 'all' | 'EGP' | 'USD' = $state('all');
	let store = $state('');
	let stores: StoreRow[] = $state([]);
	let fatal = $state<string | null>(null);

	let storesState: 'idle' | 'loading' | 'loaded' | 'error' = $state('idle');

	// scopeEpoch fences stale async responses: every freshSignal (any new
	// load or scope transition) advances it, and callers drop results whose
	// captured epoch is no longer current. Abort alone cannot stop a
	// response that already resolved before the abort.
	let aborters: AbortController[] = [];
	let scopeEpoch = 0;
	function freshSignal(): AbortSignal {
		for (const c of aborters) c.abort();
		aborters = [];
		scopeEpoch += 1;
		const c = new AbortController();
		aborters.push(c);
		return c.signal;
	}

	function readRoute() {
		const path = window.location.pathname.replace(/^\/dashboard\/?/, '') || 'overview';
		const map: Record<string, string> = {
			'': 'overview',
			overview: 'overview',
			orders: 'orders',
			devices: 'devices',
			operations: 'operations',
			users: 'users',
			updates: 'updates',
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
		const sid = q.get('store_id') ?? '';
		store = sid && isStoreID(sid) ? sid : '';
	}

	function navigate(r: string) {
		window.history.pushState({}, '', '/dashboard/' + (r === 'overview' ? '' : r) + window.location.search);
		applyRouteTransition();
	}

	function urlFor(storeOverride?: string): string {
		const q = new URLSearchParams();
		q.set('period', params.period);
		if (params.from_date) q.set('from_date', params.from_date);
		if (params.to_date) q.set('to_date', params.to_date);
		q.set('currency', currency);
		const effectiveStore = storeOverride === undefined ? store : storeOverride;
		if (effectiveStore) q.set('store_id', effectiveStore);
		return window.location.pathname + '?' + q.toString();
	}

	// resetScopeState invalidates every Store-scoped chain so no previous
	// Store's list row, pagination cursor, selected detail, or error can
	// survive a scope change. Called by every scope transition, including
	// history navigation and authenticated (re)initialization.
	function resetScopeState() {
		orderSelected = null;
		orderDetailState = 'idle';
		orderDetailErr = null;
		orderCursor = null;
		ordersMore = 'idle';
		ordersMoreErr = null;
		orderList = [];
		orderCounts = [];
		orderInbox = null;
		ordersErr = null;
	}

	// applyRouteTransition is the single Store-scope transition path for
	// the explicit selector, browser Back/Forward, URL navigation, and
	// refresh initialization. It re-reads the URL, resets scope-dependent
	// state when the scope actually changed, and reloads under a fresh
	// epoch so a stale response cannot restore the previous Store.
	function applyRouteTransition() {
		const previous = store;
		readRoute();
		if (store !== previous) resetScopeState();
		void reloadAll();
	}

	function onStore(id: string) {
		window.history.pushState({}, '', urlFor(id && isStoreID(id) ? id : ''));
		applyRouteTransition();
	}

	function onParams(p: PeriodParams) {
		params = p;
		// Deliberate filter application: push so Back/Forward restores it.
		window.history.pushState({}, '', urlFor());
		void reloadAll();
	}

	async function checkSession() {
		try {
			const current = await dashboardApi.me();
			me = current;
			operatorName = current.user.display_name;
			pendingStage = current.stage === 'FULL' ? null : current.stage;
			authed = current.stage === 'FULL';
		} catch {
			me = null;
			pendingStage = null;
			authed = false;
		}
	}

	async function onAuthenticated() {
		await checkSession();
		if (authed) await initAuthenticated();
	}

	function requireAuth(err: unknown): boolean {
		if (err instanceof ApiError && err.status === 401) {
			authed = false;
			me = null;
			pendingStage = null;
			return true;
		}
		return false;
	}

	// loadStores fetches the Store registry. Failure is surfaced truthfully
	// (storesState='error') and never resets the current selection to
	// global; a late response from an earlier initialization is dropped by
	// its epoch so it cannot override a newer scope choice.
	let storesEpoch = 0;
	async function loadStores() {
		const epoch = ++storesEpoch;
		storesState = 'loading';
		try {
			const v = await dashboardApi.stores();
			if (epoch !== storesEpoch) return;
			stores = v.stores;
			storesState = 'loaded';
		} catch {
			if (epoch !== storesEpoch) return;
			stores = [];
			storesState = 'error';
		}
	}

	// initAuthenticated is the shared post-authentication path for an
	// existing session at mount, a successful login, and session recovery:
	// apply the URL scope, clear any prior scope-dependent state, load the
	// registry, then load business reads.
	async function initAuthenticated() {
		readRoute();
		resetScopeState();
		storesEpoch += 1; // invalidate any prior in-flight registry load
		await loadStores();
		// A Store-restricted human has no "all Stores" view: default to its
		// first Store (the server refuses aggregate reads anyway).
		if (me && !me.all_stores && (!store || !me.store_ids.includes(store))) {
			store = me.store_ids[0] ?? '';
			window.history.replaceState({}, '', urlFor());
		}
		await reloadAll();
	}

	// ---- widget data ----
	let overview: OverviewResponse | null = $state(null);
	let overviewState: WidgetState = $state('loading');
	let trend: { labels: string[]; values: number[] } = $state({ labels: [], values: [] });
	let trendRefund: { labels: string[]; values: number[] } = $state({ labels: [], values: [] });
	let trendNet: { labels: string[]; values: number[] } = $state({ labels: [], values: [] });
	let trendRangeError = $state(false);
	let trendExact: { date: string; amount_minor: string }[] = $state([]);
	let trendRefundExact: { date: string; refund_minor: string }[] = $state([]);
	let dailyMeta: { display_currency: string; normalized: boolean } = $state({ display_currency: 'EGP', normalized: true });
	let dailyState: WidgetState = $state('loading');
	let products: ProductDisplayRow[] = $state([]);
	let productsState: WidgetState = $state('loading');
	let categories: CategoryDisplayRow[] = $state([]);
	let catKind: CategoryKind = $state('root_category');
	let categoriesState: WidgetState = $state('loading');
	let tagList: TagListResponse | null = $state(null);
	let tagState: WidgetState = $state('loading');
	let tagErr: number | null = $state(null);
	let online: OrderAnalyticsResponse | null = $state(null);
	let onlineState: WidgetState = $state('loading');
	let onlineErr: number | null = $state(null);
	let health: CatalogHealthResponse | null = $state(null);
	let healthState: WidgetState = $state('loading');
	let healthErr: number | null = $state(null);
	let healthProvider = $state('');
	let branches: BranchRow[] = $state([]);
	let branchesState: WidgetState = $state('loading');
	let syncHealth: SyncHealth | null = $state(null);
	let syncState: WidgetState = $state('loading');
	let activity: ActivityItem[] = $state([]);
	let activityState: WidgetState = $state('loading');
	let latest: LatestSale[] = $state([]);
	let latestState: WidgetState = $state('loading');
	let orderList: OrderSummary[] = $state([]);
	let orderCounts: OrderStatusCount[] = $state([]);
	let orderInbox: WebhookInboxStats | null = $state(null);
	let ordersState: WidgetState = $state('loading');
	let ordersErr: number | null = $state(null);
	let orderCursor: string | null = $state(null);
	let ordersMore: 'idle' | 'loading' | 'error' = $state('idle');
	let ordersMoreErr: number | null = $state(null);
	let orderFilterStatus = $state('');
	let orderFilterProvider = $state('');
	let orderSelected: OrderDetail | null = $state(null);
	let orderDetailState: 'idle' | 'loading' | 'loaded' | 'error' = $state('idle');
	let orderDetailErr: number | null = $state(null);
	let overviewErr: number | null = $state(null);
	let dailyErr: number | null = $state(null);
	let productsErr: number | null = $state(null);
	let categoriesErr: number | null = $state(null);
	let branchesErr: number | null = $state(null);
	let syncErr: number | null = $state(null);
	let activityErr: number | null = $state(null);
	let latestErr: number | null = $state(null);

	async function reloadOrders() {
		if (!authed) return;
		const signal = freshSignal();
		const epoch = scopeEpoch;
		ordersState = 'loading';
		ordersMore = 'idle';
		ordersMoreErr = null;
		try {
			const v = await dashboardApi.orders(orderFilterStatus, orderFilterProvider, null, store, signal);
			if (epoch !== scopeEpoch) return;
			orderList = v.orders;
			orderCursor = v.next_cursor;
			orderCounts = v.status_counts;
			orderInbox = v.webhook_inbox;
			ordersState = v.orders.length === 0 ? 'empty' : 'loaded';
		} catch (err) {
			if (epoch !== scopeEpoch) return;
			if (err instanceof DOMException && err.name === 'AbortError') return;
			if (requireAuth(err)) return;
			ordersState = 'error';
			ordersErr = err instanceof ApiError ? err.status : 0;
		}
	}

	// orderKey is the stable row identity for append dedupe and render keys.
	function orderKey(o: OrderSummary): string {
		return `${o.provider_key}/${o.external_order_id}`;
	}

	async function loadMoreOrders() {
		if (!authed || !orderCursor || ordersMore === 'loading') return;
		const signal = freshSignal();
		const epoch = scopeEpoch;
		ordersMore = 'loading';
		ordersMoreErr = null;
		try {
			const v = await dashboardApi.orders(orderFilterStatus, orderFilterProvider, orderCursor, store, signal);
			// A scope change while the continuation was in flight must not
			// append the previous Store's rows to the new Store's list.
			if (epoch !== scopeEpoch) return;
			const seen = new Set(orderList.map(orderKey));
			for (const row of v.orders) {
				if (!seen.has(orderKey(row))) {
					seen.add(orderKey(row));
					orderList.push(row);
				}
			}
			orderCursor = v.next_cursor;
			ordersMore = 'idle';
		} catch (err) {
			if (epoch !== scopeEpoch) return;
			if (err instanceof DOMException && err.name === 'AbortError') {
				ordersMore = 'idle';
				return;
			}
			if (requireAuth(err)) {
				ordersMore = 'idle';
				return;
			}
			// Loaded rows stay visible; only the continuation fails.
			ordersMore = 'error';
			ordersMoreErr = err instanceof ApiError ? err.status : 0;
		}
	}

	async function selectOrder(order: OrderSummary | null) {
		orderSelected = null;
		if (!order) {
			orderDetailState = 'idle';
			return;
		}
		orderDetailState = 'loading';
		const signal = freshSignal();
		const epoch = scopeEpoch;
		try {
			const detail = await dashboardApi.orderDetail(order.provider_key, order.external_order_id, store, signal);
			// Drop a detail response that belongs to a superseded scope.
			if (epoch !== scopeEpoch) return;
			orderSelected = detail;
			orderDetailState = 'loaded';
		} catch (err) {
			if (epoch !== scopeEpoch) return;
			if (err instanceof DOMException && err.name === 'AbortError') return;
			if (requireAuth(err)) return;
			orderDetailState = 'error';
			orderDetailErr = err instanceof ApiError ? err.status : 0;
		}
	}
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
		const epoch = scopeEpoch;
		overviewState = dailyState = productsState = categoriesState = branchesState = syncState = activityState = latestState =
			ordersState = tagState = onlineState = healthState = 'loading';
		const done = async <T>(
			p: Promise<T>,
			apply: (v: T) => void,
			setState: (s: WidgetState) => void,
			setErr: (n: number | null) => void
		) => {
			try {
				const v = await p;
				if (epoch !== scopeEpoch) return;
				apply(v);
			} catch (err) {
				if (epoch !== scopeEpoch) return;
				if (err instanceof DOMException && err.name === 'AbortError') return;
				if (requireAuth(err)) return;
				setState('error');
				setErr(err instanceof ApiError ? err.status : 0);
			}
		};
		const dailyMode: DailyMode = currency;
		const breakdownMode: BreakdownMode = currency === 'all' ? 'all' : 'native';
		await Promise.all([
			done(dashboardApi.overview(params, store, signal), (v) => {
				overview = v;
				overviewState = v.summary.transaction_count === 0 && v.summary.return_transaction_count === 0 ? 'empty' : 'loaded';
			}, (s) => (overviewState = s), (n) => (overviewErr = n)),
			done(dashboardApi.daily(params, dailyMode, store, signal), (v) => {
				dailyMeta = { display_currency: v.display_currency, normalized: v.normalized };
				trendExact = v.days.map((d) => ({ date: d.date, amount_minor: d.amount_minor }));
				trendRefundExact = v.days.map((d) => ({ date: d.date, refund_minor: d.refund_minor }));
				try {
					trend = {
						labels: v.days.map((d) => d.date),
						values: v.days.map((d) => toChartNumber(d.amount_minor))
					};
					trendRefund = {
						labels: v.days.map((d) => d.date),
						values: v.days.map((d) => toChartNumber(d.refund_minor))
					};
					trendNet = {
						labels: v.days.map((d) => d.date),
						values: v.days.map((d) => toChartNumber(subMinor(d.amount_minor, d.refund_minor)))
					};
					trendRangeError = false;
				} catch {
					// Unsafe chart magnitude: exact daily data stays
					// available below; only visualization is limited.
					trend = { labels: [], values: [] };
					trendRefund = { labels: [], values: [] };
					trendNet = { labels: [], values: [] };
					trendRangeError = true;
				}
				dailyState = emptyOf(v.days);
			}, (s) => (dailyState = s), (n) => (dailyErr = n)),
			done(dashboardApi.products(params, breakdownMode, currency === 'all' ? '' : currency, store, signal), (v) => {
				products = v.rows.map((r) => ({
					name: r.product_name,
					sku: r.sku,
					units: r.units,
					units_returned: r.units_returned,
					amount_minor: r.amount_minor,
					refund_minor: r.refund_minor,
					net_minor: r.net_minor,
					store_id: r.store_id ?? null
				}));
				productsState = emptyOf(products);
			}, (s) => (productsState = s), (n) => (productsErr = n)),
			done(dashboardApi.categories(params, catKind, breakdownMode, currency === 'all' ? '' : currency, store, signal), (v) => {
				categories = v.rows.map((r) => ({
					name: r.name_en ? `${r.name_ar} / ${r.name_en}` : r.name_ar,
					units: r.units,
					units_returned: r.units_returned,
					amount_minor: r.amount_minor,
					refund_minor: r.refund_minor,
					net_minor: r.net_minor
				}));
				categoriesState = emptyOf(categories);
			}, (s) => (categoriesState = s), (n) => (categoriesErr = n)),
			done(dashboardApi.branches(params, store, signal), (v) => {
				branches = v.rows;
				branchesState = emptyOf(v.rows);
			}, (s) => (branchesState = s), (n) => (branchesErr = n)),
			done(dashboardApi.syncHealth(signal), (v) => {
				syncHealth = v;
				syncState = 'loaded';
			}, (s) => (syncState = s), (n) => (syncErr = n)),
			done(dashboardApi.activity(store, signal), (v) => {
				activity = v.items;
				activityState = emptyOf(v.items);
			}, (s) => (activityState = s), (n) => (activityErr = n)),
			done(dashboardApi.latestSales(store, signal), (v) => {
				latest = v.sales;
				latestState = emptyOf(v.sales);
			}, (s) => (latestState = s), (n) => (latestErr = n)),
			done(dashboardApi.orders(orderFilterStatus, orderFilterProvider, null, store, signal), (v) => {
				orderList = v.orders;
				orderCursor = v.next_cursor;
				ordersMore = 'idle';
				ordersMoreErr = null;
				orderCounts = v.status_counts;
				orderInbox = v.webhook_inbox;
				ordersState = emptyOf(v.orders);
			}, (s) => (ordersState = s), (n) => (ordersErr = n)),
			done(dashboardApi.tags(params, currency === 'all' ? '' : currency, store, 10, signal), (v) => {
				tagList = v;
				tagState = v.rows.length === 0 ? 'empty' : 'loaded';
			}, (s) => (tagState = s), (n) => (tagErr = n)),
			done(dashboardApi.orderSummary(params, store, '', currency === 'all' ? '' : currency, signal), (v) => {
				online = v;
				onlineState =
					v.currency_totals.length === 0 && v.status_counts.length === 0 ? 'empty' : 'loaded';
			}, (s) => (onlineState = s), (n) => (onlineErr = n)),
			done(dashboardApi.catalogHealth(store, healthProvider, signal), (v) => {
				health = v;
				const total = v.counts.reduce((acc, c) => acc + c.products, 0);
				healthState = total === 0 && v.detail.length === 0 ? 'empty' : 'loaded';
			}, (s) => (healthState = s), (n) => (healthErr = n))
		]);
	}

	function setHealthProvider(p: string) {
		healthProvider = p;
		void reloadAll();
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
		me = null;
		pendingStage = null;
		operatorName = '';
		// Do not leak one session's Store registry or scoped rows into the
		// next login; re-authentication re-reads the URL and reloads. The
		// epoch bump also drops a registry response still in flight.
		storesEpoch += 1;
		stores = [];
		storesState = 'idle';
		resetScopeState();
	}

	// KPI cards follow the same active-currency scoping as the reporting
	// service: All uses the whole-period summary, EGP/USD use their own
	// per-mode averages (own transaction counts — never mixed).
	// Transaction counts never conflate sales with returns.
	let metricTxn = $derived.by(() => {
		if (!overview) return '—';
		if (currency === 'all') return formatInt(overview.summary.transaction_count);
		const avg = currency === 'EGP' ? overview.averages.egp : overview.averages.usd;
		return formatInt(avg.transactions);
	});
	let metricRetTxn = $derived.by(() => {
		if (!overview) return '—';
		return formatInt(overview.summary.return_transaction_count);
	});
	let metricUnits = $derived.by(() => {
		if (!overview) return '—';
		if (currency === 'all') return formatInt(overview.summary.units_sold);
		const avg = currency === 'EGP' ? overview.averages.egp : overview.averages.usd;
		return formatInt(avg.units);
	});
	let metricRetUnits = $derived.by(() => {
		if (!overview) return '—';
		return formatInt(overview.summary.units_returned);
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
		// Back/Forward is a scope transition like any other: the shared
		// handler resets scope-dependent state before reloading.
		const onHistory = () => applyRouteTransition();
		window.addEventListener('popstate', onHistory);
		void (async () => {
			await checkSession();
			if (authed) await initAuthenticated();
		})();
		return () => window.removeEventListener('popstate', onHistory);
	});
</script>

{#if authed === null}
	<div class="muted pad">جارٍ التحميل… / Loading…</div>
{:else if !authed}
	<LoginPage stage={pendingStage} onauthenticated={() => void onAuthenticated()} />
{:else}
	<div class="shell">
		<div class="sidewrap"><Sidebar {route} {navigate} permissions={me?.permissions ?? []} allStores={me?.all_stores ?? false} /></div>
		<main class="main">
			<header class="topbar">
				<div class="brandblock">
					<div class="brandname">MoonLightCloud</div>
					<h1 class="dash-title">لوحة متابعة المبيعات</h1>
					<div class="muted brandsub">Your business insights in one place</div>
				</div>
				<div class="periodblock">
					<PeriodSelector {params} timezone={overview?.timezone ?? 'Africa/Cairo'} onchange={onParams} />
					<StoreSelector {stores} value={store} state={storesState} onchange={onStore} onretry={() => void loadStores()} />
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
			<div class="dash" class:ov={route === 'overview'}>
				{#if route === 'overview' || route === 'sales'}
					<div class="cell a-kpis">
						<MetricCard ar="معاملات البيع" en="Sale Transactions" value={metricTxn} context={scopeLabel} status={overviewState} icon="M4 7h13l-3-3M20 17H7l3 3" />
						<MetricCard ar="معاملات المرتجعات" en="Return Transactions" value={metricRetTxn} context={scopeLabel} status={overviewState} icon="M4 9h13l-3-3M20 15H7l3 3" />
						<MetricCard ar="الوحدات المباعة" en="Units Sold" value={metricUnits} context={scopeLabel} status={overviewState} icon="M12 3l8 4.5v9L12 21l-8-4.5v-9L12 3zM12 12l8-4.5M12 12L4 7.5M12 12v9" />
						<MetricCard ar="الوحدات المرتجعة" en="Units Returned" value={metricRetUnits} context={scopeLabel} status={overviewState} icon="M12 3l8 4.5v9L12 21l-8-4.5v-9L12 3zM12 12l8-4.5M12 12L4 7.5M12 12v9" />
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
				{#if route === 'sync'}
					<div class="cell a-sync">
						<CatalogHealthCard data={health} status={healthState} errStatus={healthErr} onretry={retryAll} provider={healthProvider} onprovider={setHealthProvider} />
					</div>
				{/if}
				{#if route === 'overview' || route === 'categories'}
					<div class="cell a-cat">
						<CategoryCard rows={categories} kind={catKind} onkind={setCatKind} unit={currency === 'all' ? 'EGP normalized' : currency} status={categoriesState} errStatus={categoriesErr} onretry={retryAll} />
					</div>
				{/if}
				{#if route === 'categories'}
					<div class="cell a-cat">
						<TagCard data={tagList} status={tagState} errStatus={tagErr} onretry={retryAll} currency={currency} />
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
							titleAr="الاتجاه اليومي (إجمالي / مرتجعات / صافي)"
							titleEn="Gross / refunds / net over time"
							labels={trend.labels}
							values={trend.values}
							refundValues={trendRefund.values}
							netValues={trendNet.values}
							exact={trendExact}
							refundExact={trendRefundExact}
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
				{#if route === 'sales'}
					<div class="cell a-branch">
						<OnlineComparison online={online} retail={overview} status={overviewState === 'error' || onlineState === 'error' ? 'error' : overviewState === 'loading' || onlineState === 'loading' ? 'loading' : onlineState} errStatus={overviewState === 'error' ? overviewErr : onlineErr} {currency} onretry={retryAll} />
					</div>
				{/if}
				{#if route === 'overview' || route === 'sync'}
					<div class="cell a-latest">
						<LatestSalesCard sales={latest} status={latestState} errStatus={latestErr} onretry={retryAll} />
					</div>
				{/if}
				{#if route === 'overview' || route === 'orders'}
					<div class="cell a-orders">
						<OrdersPage
							orders={orderList}
							counts={orderCounts}
							inbox={orderInbox}
							status={ordersState}
							errStatus={ordersErr}
							filterStatus={orderFilterStatus}
							filterProvider={orderFilterProvider}
							nextCursor={orderCursor}
							more={ordersMore}
							moreErr={ordersMoreErr}
							onloadmore={() => void loadMoreOrders()}
							onstatus={(s) => {
								orderFilterStatus = s;
								void reloadOrders();
							}}
							onprovider={(p) => {
								orderFilterProvider = p;
								void reloadOrders();
							}}
							selected={orderSelected}
							detailStatus={orderDetailState}
							detailErr={orderDetailErr}
							onselect={selectOrder}
							onretry={() => void reloadOrders()}
						/>
					</div>
				{/if}
				{#if route === 'devices'}
					<div class="cell a-orders">
						<DevicesPage />
					</div>
				{/if}
				{#if route === 'operations'}
					<div class="cell a-orders">
						<OperationsPage />
					</div>
				{/if}
				{#if route === 'admin'}
					<div class="cell a-orders">
						<CatalogAdminPage {store} />
					</div>
				{/if}
				{#if route === 'updates'}
					<div class="cell a-orders">
						<UpdatesPage {store} />
					</div>
				{/if}
				{#if route === 'users' && me}
					<div class="cell a-orders">
						<UsersPage {me} {stores} onsessionchange={() => void checkSession()} />
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
		/* F06: keep the period controls and the Store selector on one
		row when space allows (they wrap only when they genuinely do not
		fit), so adding the selector never inflates the topbar and pushes
		the dashboard grid past its density budget. */
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 6px 12px;
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
	/* Overview composition: the tall Sync Health card spans rows 1-2 in
	its own track so it never defines sibling row heights (R3-01). Row 2
	cards use 3-col spans to share the remaining 9 columns. Other routes
	keep simple flow to avoid placement holes. */
	.dash.ov .a-sync { grid-row: span 2; }
	.dash.ov .a-trend { grid-column: span 3; }
	.dash.ov .a-products { grid-column: span 3; }
	.dash.ov .a-cat { grid-column: span 3; }
	.a-sales { grid-column: span 6; }
	.a-kpis {
		grid-column: span 3;
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 12px;
		align-content: start;
	}
	.a-trend { grid-column: span 4; }
	.a-products { grid-column: span 4; }
	.a-cat { grid-column: span 4; }
	.a-act { grid-column: span 3; }
	.a-latest { grid-column: span 6; }
	.a-branch { grid-column: span 3; }
	/* Wide read-only tables (orders/devices/operations) span the full grid
	so their min-content width can never overflow the viewport on the
	overview composition (F06: no horizontal overflow). */
	.a-orders { grid-column: span 12; }
	@media (max-width: 1280px) {
		.a-sync { grid-column: span 4; grid-row: auto; }
		.dash.ov .a-trend { grid-column: span 6; }
		.dash.ov .a-products { grid-column: span 6; }
		.dash.ov .a-cat { grid-column: span 12; }
		.a-sales { grid-column: span 8; }
		.a-kpis { grid-column: span 12; grid-template-columns: repeat(4, 1fr); }
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
			grid-row: auto;
		}
		.dash.ov > .cell {
			grid-column: span 12;
		}
		.a-kpis {
			grid-template-columns: 1fr;
		}
	}
</style>
