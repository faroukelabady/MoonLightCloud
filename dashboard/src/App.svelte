<script lang="ts">
	import { onMount } from 'svelte';
	import Sidebar from './components/Sidebar.svelte';
	import PeriodSelector from './components/PeriodSelector.svelte';
	import SalesCard from './components/SalesCard.svelte';
	import Segmented from './components/Segmented.svelte';
import TrendChart from './components/TrendChart.svelte';
	import TopProducts from './components/TopProducts.svelte';
	import type { ProductDisplayRow } from './components/TopProducts.svelte';
	import CategoryCard from './components/CategoryCard.svelte';
	import type { CategoryDisplayRow } from './components/CategoryCard.svelte';
	import BranchCard from './components/BranchCard.svelte';
	import TagCard from './components/TagCard.svelte';
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
	import ReportsPage from './components/ReportsPage.svelte';
	import SettingsPage from './components/SettingsPage.svelte';
	import LoginPage from './components/LoginPage.svelte';
	import { createRequestScope } from './lib/requestScope';
	import { dashboardApi, ApiError, isStoreID, setCSRFToken } from './lib/api.js';
	import type { PeriodParams, OverviewResponse, BranchRow, SyncHealth, ActivityItem, LatestSale, DailyMode, BreakdownMode, CategoryKind, OrderSummary, OrderDetail, OrderStatusCount, WebhookInboxStats, StoreRow, Me, AuthStage } from './lib/api.js';
	import StoreSelector from './components/StoreSelector.svelte';
	import { toChartNumber, subMinor } from './lib/money.js';
	import { arabic, lang, msg, setLang, t } from './lib/i18n.svelte.js';

	type WidgetState = 'loading' | 'loaded' | 'empty' | 'error';

	let authed: boolean | null = $state(null);
	let loggingOut = $state(false);
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

	const requests = createRequestScope<'widgets' | 'orders' | 'more' | 'detail'>();
	const sessionRequests = createRequestScope<'session'>();

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
			admin: 'admin',
			reports: 'reports',
			settings: 'settings',
			sync: 'sync'
		};
		// Former pages whose content now lives in Overview: their links
		// keep working and the address bar shows the page they land on.
		const merged = ['sales', 'daily', 'products', 'categories'];
		if (merged.includes(path)) window.history.replaceState({}, '', '/dashboard/' + window.location.search);
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
		if (loggingOut) return false;
		const owner = sessionRequests.begin('session');
		try {
			const current = await dashboardApi.me(owner.signal);
			if (!owner.current()) return false;
			me = current;
			operatorName = current.user.display_name;
			pendingStage = current.stage === 'full' ? null : current.stage;
			authed = current.stage === 'full';
			return authed;
		} catch {
			if (!owner.current()) return false;
			me = null;
			pendingStage = null;
			authed = false;
			return false;
		}
	}

	async function onAuthenticated() {
		if (await checkSession()) await initAuthenticated();
	}

	function requireAuth(err: unknown): boolean {
		if (err instanceof ApiError && err.status === 401) {
			requests.invalidate();
			sessionRequests.invalidate();
			storesEpoch += 1;
			resetScopeState();
			setCSRFToken('');
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
		if (loggingOut) return;
		const current = sessionRequests.capture('session');
		requests.invalidate();
		readRoute();
		resetScopeState();
		storesEpoch += 1; // invalidate any prior in-flight registry load
		await loadStores();
		if (!current() || !authed) return;
		// A Store-restricted human has no "all Stores" view: default to its
		// first Store (the server refuses aggregate reads anyway).
		if (me && !me.all_stores && (!store || !me.store_ids.includes(store))) {
			store = me.store_ids[0] ?? '';
			window.history.replaceState({}, '', urlFor());
		} else if (store && storesState === 'loaded' && !stores.some((s) => s.store_id === store)) {
			// A Store the registry does not list (for example a bookmark from
			// before a database reset) would show every card empty: fall back
			// to all Stores instead.
			store = '';
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
		if (!authed || loggingOut) return;
		requests.cancel('more');
		const { signal, current } = requests.begin('orders');
		ordersState = 'loading';
		ordersMore = 'idle';
		ordersMoreErr = null;
		try {
			const v = await dashboardApi.orders(orderFilterStatus, orderFilterProvider, null, store, signal);
			if (!current()) return;
			orderList = v.orders;
			orderCursor = v.next_cursor;
			orderCounts = v.status_counts;
			orderInbox = v.webhook_inbox;
			ordersState = v.orders.length === 0 ? 'empty' : 'loaded';
		} catch (err) {
			if (!current()) return;
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
		if (!authed || loggingOut || !orderCursor || ordersMore === 'loading') return;
		const { signal, current } = requests.begin('more');
		ordersMore = 'loading';
		ordersMoreErr = null;
		try {
			const v = await dashboardApi.orders(orderFilterStatus, orderFilterProvider, orderCursor, store, signal);
			// A scope change while the continuation was in flight must not
			// append the previous Store's rows to the new Store's list.
			if (!current()) return;
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
			if (!current()) return;
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
		if (loggingOut) return;
		requests.cancel('detail');
		orderSelected = null;
		if (!order) {
			orderDetailState = 'idle';
			return;
		}
		orderDetailState = 'loading';
		const { signal, current } = requests.begin('detail');
		try {
			const detail = await dashboardApi.orderDetail(order.provider_key, order.external_order_id, store, signal);
			// Drop a detail response that belongs to a superseded scope.
			if (!current()) return;
			orderSelected = detail;
			orderDetailState = 'loaded';
		} catch (err) {
			if (!current()) return;
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
		if (!authed || loggingOut) return;
		fatal = null;
		requests.invalidate();
		const { signal, current } = requests.begin('widgets');
		const ordersOwner = requests.begin('orders');
		orderSelected = null;
		orderDetailState = 'idle';
		orderDetailErr = null;
		ordersMore = 'idle';
		ordersMoreErr = null;
		overviewState = dailyState = productsState = categoriesState = branchesState = syncState = activityState = latestState =
			ordersState = tagState = onlineState = healthState = 'loading';
		const done = async <T>(
			p: Promise<T>,
			apply: (v: T) => void,
			setState: (s: WidgetState) => void,
			setErr: (n: number | null) => void,
			currentOwner: () => boolean = current
		) => {
			try {
				const v = await p;
				if (!currentOwner()) return;
				apply(v);
			} catch (err) {
				if (!currentOwner()) return;
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
					name: t(r.name_ar, r.name_en || r.name_ar),
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
			done(dashboardApi.orders(orderFilterStatus, orderFilterProvider, null, store, ordersOwner.signal), (v) => {
				orderList = v.orders;
				orderCursor = v.next_cursor;
				ordersMore = 'idle';
				ordersMoreErr = null;
				orderCounts = v.status_counts;
				orderInbox = v.webhook_inbox;
				ordersState = emptyOf(v.orders);
			}, (s) => (ordersState = s), (n) => (ordersErr = n), ordersOwner.current),
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
		if (loggingOut) return;
		loggingOut = true;
		requests.invalidate();
		sessionRequests.invalidate();
		storesEpoch += 1;
		try {
			await dashboardApi.logout();
		} finally {
			requests.invalidate();
			sessionRequests.invalidate();
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
			// The next login starts from all Stores (or the user's own
			// Store): the address no longer carries this session's choice.
			store = '';
			const q = new URLSearchParams(window.location.search);
			q.delete('store_id');
			window.history.replaceState({}, '', window.location.pathname + (q.size ? `?${q}` : ''));
			loggingOut = false;
		}
	}

let branchContext = $derived.by(() => {
		if (branchesState === 'loading') return '…';
		if (branches.length === 0) return '';
		// Rows are one per branch and currency: count branches, not rows, so
		// a shop selling in EGP and USD is still one branch (same key as the
		// branch card).
		const distinct = new Set(branches.map((r) => [r.shop_name_ar, r.shop_name_en, r.shop_phone, r.channel].join('\u0000'))).size;
		const first = branches[0];
		const extra = distinct > 1 ? ` +${distinct - 1}` : '';
		return `${t(first.shop_name_ar, first.shop_name_en || first.shop_name_ar)}${extra}`;
	});

	// The applied period's first and last local dates (the end is
	// exclusive on the wire), shown in the period's date fields.
	let periodDates = $derived.by(() => {
		if (!overview) return null;
		const end = new Date(`${overview.period.end_local_exclusive.slice(0, 10)}T00:00:00Z`);
		end.setUTCDate(end.getUTCDate() - 1);
		return { from: overview.period.start_local.slice(0, 10), to: end.toISOString().slice(0, 10) };
	});

	onMount(() => {
		readRoute();
		// Back/Forward is a scope transition like any other: the shared
		// handler resets scope-dependent state before reloading.
		const onHistory = () => applyRouteTransition();
		window.addEventListener('popstate', onHistory);
		void (async () => {
			if (await checkSession()) await initAuthenticated();
		})();
		return () => {
			window.removeEventListener('popstate', onHistory);
			requests.dispose();
			sessionRequests.dispose();
			storesEpoch += 1;
		};
	});
</script>

{#if authed === null}
	<div class="muted pad">{msg('app.loading')}</div>
{:else if !authed}
	<LoginPage stage={pendingStage} onauthenticated={() => void onAuthenticated()} />
{:else}
	<div class="shell">
		<div class="sidewrap"><Sidebar {route} {navigate} permissions={me?.permissions ?? []} allStores={me?.all_stores ?? false} /></div>
		<main class="main">
			<header class="topbar">
				<div class="brandblock">
					<div class="brandname">MoonLightCloud</div>
					<h1 class="dash-title">{msg('app.salesDashboard')}</h1>
					<div class="muted brandsub">{msg('app.tagline')}</div>
				</div>
				<div class="periodblock">
					<PeriodSelector {params} timezone={overview?.timezone ?? 'Africa/Cairo'} onchange={onParams} showTimezone={false} range={periodDates} />
					<!-- One row: period buttons, dates, Store, currency, then the account block. -->
					<div class="scopecol">
					<StoreSelector {stores} value={store} state={storesState} onchange={onStore} onretry={() => void loadStores()} />
					{#if route === 'overview'}
						<!-- One currency choice for every Overview card. -->
						<div class="currencyctl" data-testid="currency-mode" role="group" aria-label={msg('app.currency')} title={msg('app.currency')}>
							<Segmented
								size="sm"
								options={[
									{ value: 'all', ar: 'الكل', en: 'All' },
									{ value: 'EGP', ar: 'EGP' },
									{ value: 'USD', ar: 'USD' }
								]}
								value={currency}
								onchange={(v) => setMode(v as 'all' | 'EGP' | 'USD')}
							/>
						</div>
					{/if}
					</div>
					<!-- Same row, at the end: branch context and account controls. -->
					<div class="actorblock">
						{#if branchContext}<div class="branchctx" title={branchContext}>{branchContext}</div>{/if}
						<div class="actorrow">
						{#if operatorName}
							<div class="userchip" data-testid="current-user">
								<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="8" r="4" /><path d="M4 21a8 8 0 0 1 16 0" /></svg>
								<span>{operatorName}</span>
							</div>
						{/if}
						<button type="button" class="langswitch" lang={arabic() ? 'en' : 'ar'} title={msg('app.text')} onclick={() => setLang(lang() === 'ar' ? 'en' : 'ar')}>
							<svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="9" /><path d="M3 12h18M12 3a15 15 0 0 1 0 18M12 3a15 15 0 0 0 0 18" /></svg>
							{arabic() ? 'English' : 'العربية'}
						</button>
						<button type="button" class="logout" disabled={loggingOut} onclick={logout}>
							<svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M15 4h4v16h-4M10 16l4-4-4-4M14 12H4" /></svg>
							{msg('app.logout')}
						</button>
						</div>
					</div>
				</div>
			</header>
			{#if fatal}<div role="alert">{fatal}</div>{/if}
			<div class="dash" class:ov={route === 'overview'}>
				{#if route === 'overview' || route === 'sync'}
					<!-- First in the grid: the reading-order start, so top right in
					     Arabic and top left in English. -->
					<div class="cell a-sync">
						<SyncHealthCard health={syncHealth} status={syncState} errStatus={syncErr} onretry={retryAll} onrefresh={() => reloadAll()} noShop={storesState === 'loaded' && stores.length === 0} />
					</div>
				{/if}
				{#if route === 'overview'}
					<div class="cell a-sales">
						<SalesCard data={overview} mode={currency} status={overviewState} errStatus={overviewErr} onretry={retryAll} />
					</div>
				{/if}
				{#if route === 'sync'}
					<div class="cell a-sync">
						<CatalogHealthCard data={health} status={healthState} errStatus={healthErr} onretry={retryAll} provider={healthProvider} onprovider={setHealthProvider} />
					</div>
				{/if}
				{#if route === 'overview'}
					<div class="cell a-cat">
						<CategoryCard rows={categories} kind={catKind} onkind={setCatKind} unit={currency === 'all' ? 'EGP normalized' : currency} status={categoriesState} errStatus={categoriesErr} onretry={retryAll} />
					</div>
				{/if}
				{#if route === 'overview'}
					<div class="cell a-tags">
						<TagCard data={tagList} status={tagState} errStatus={tagErr} onretry={retryAll} currency={currency} />
					</div>
				{/if}
				{#if route === 'overview'}
					<div class="cell a-products">
						<TopProducts rows={products} money={currency === 'all' ? 'EGP' : currency} unit={currency === 'all' ? 'EGP normalized' : currency} status={productsState} errStatus={productsErr} onretry={retryAll} />
					</div>
				{/if}
				{#if route === 'overview'}
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
						/>
					</div>
				{/if}
				{#if route === 'overview'}
					<div class="cell a-branch">
						<BranchCard
							rows={branches}
							status={branchesState}
							errStatus={branchesErr}
							onretry={retryAll}
							mode={currency}
							{online}
							retail={overview}
							onlineStatus={overviewState === 'error' || onlineState === 'error' ? 'error' : overviewState === 'loading' || onlineState === 'loading' ? 'loading' : onlineState}
							onlineErr={overviewState === 'error' ? overviewErr : onlineErr}
						/>
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
				{#if route === 'reports'}
					<div class="cell a-orders">
						<ReportsPage {params} {store} {stores} />
					</div>
				{/if}
				{#if route === 'settings' && me}
					<div class="cell a-orders">
						<SettingsPage canManage={me.permissions.includes('settings.manage') && me.all_stores} />
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
	/* Brand, then one row with every filter and the account controls. */
	.topbar {
		display: grid;
		grid-template-columns: auto minmax(0, 1fr);
		align-items: center;
		gap: 6px 16px;
		background: var(--surface);
		border-bottom: 1px solid var(--border);
		margin: 0 -16px;
		padding: 10px 16px;
	}
	.brandblock {
		grid-row: 1;
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
		grid-row: 1;
		grid-column: 2;
		min-width: 0;
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 6px 8px;
	}
	.periodblock :global(.modes button) {
		padding-inline: 10px;
	}
	/* The period selector and the scope column dissolve into this row so
	   all four controls share one centre line. */
	.periodblock :global(.periodbar),
	.scopecol {
		display: contents;
	}
	.periodblock :global(.periodbar .err),
	.periodblock :global(.periodbar .tz) {
		order: 10;
		flex-basis: 100%;
	}
	.currencyctl {
		display: inline-flex;
		align-items: center;
		gap: 6px;
	}
	/* The account block keeps together and sits at the row's end; it
	   wraps as one piece when the row is too narrow. */
	.actorblock {
		display: flex;
		align-items: center;
		gap: 8px;
		margin-inline-start: auto;
	}
	.actorrow {
		display: flex;
		align-items: center;
		gap: 8px;
	}
	.branchctx {
		font-size: 0.82rem;
		font-weight: 600;
		max-width: 100%;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.userchip {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		padding: 5px 2px;
		color: var(--text);
		font-size: 0.82rem;
		font-weight: 700;
		max-width: 160px;
	}
	.userchip span {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.userchip svg {
		flex-shrink: 0;
		color: var(--primary);
	}
	/* Every top-bar control shares the language button's height, in both
	   languages (Arabic glyphs would otherwise make some controls taller). */
	.topbar :global(.modes),
	.topbar :global(.segmented),
	.topbar :global(.periodbar input),
	.topbar :global(.periodbar .apply),
	.topbar :global(.single-select .trigger),
	.userchip,
	.branchctx,
	.langswitch,
	.logout {
		height: var(--topbar-control-h);
		min-height: 0;
		box-sizing: border-box;
	}
	.topbar :global(.modes),
	.topbar :global(.segmented) {
		align-items: stretch;
	}
	.topbar :global(.modes button),
	.topbar :global(.segmented button),
	.topbar :global(.periodbar .apply),
	.topbar :global(.single-select .trigger) {
		display: inline-flex;
		align-items: center;
		padding-block: 0;
		line-height: 1;
	}
	.topbar :global(.periodbar input) {
		padding-block: 0;
	}
	.branchctx {
		display: inline-flex;
		align-items: center;
	}
	/* Language and logout share one button shape: blue and red. */
	.langswitch,
	.logout {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		font-size: 0.8rem;
		font-weight: 600;
		color: #fff;
		border: 0;
		border-radius: var(--radius-control);
		padding: 6px 12px;
		cursor: pointer;
	}
	.langswitch {
		background: var(--primary);
	}
	.langswitch:hover {
		background: var(--color-primary-dark);
	}
	.logout {
		background: var(--danger);
	}
	.logout:hover {
		background: #991b1b;
	}
	.logout:disabled {
		opacity: 0.6;
		cursor: default;
	}
	/* Every row of the 12-column grid is filled and its cards stretch to the
	same height, so no card leaves an empty band beside or below it. The
	overview orders cards explicitly; other routes show two cards per row
	with KPIs and wide tables (orders, devices, admin) spanning the grid. */
	.dash {
		display: grid;
		grid-template-columns: repeat(12, minmax(0, 1fr));
		gap: 12px;
		align-items: stretch;
	}
	.cell {
		min-width: 0;
		display: flex;
		flex-direction: column;
	}
	.cell > :global(.card) {
		flex: 1;
		min-height: 0;
	}
	/* Overview cards keep one readable height per row; longer lists and
	   tables scroll inside their card instead of stretching the page. */
	.dash.ov .cell {
		max-height: 440px;
	}
	.dash.ov .a-orders {
		max-height: 560px;
	}
	/* The branch card also holds the online comparison. */
	.dash.ov .a-branch,
	.dash.ov .a-tags {
		max-height: 640px;
	}
	.cell { grid-column: span 6; }
	.a-orders { grid-column: span 12; }
	/* Sync health opens the overview at the reading-order start (top right
	   in Arabic, top left in English) and is raised so it is seen first. */
	.dash.ov .a-sync > :global(.card) {
		border: 1px solid #b9cdf3;
		border-top: 4px solid var(--primary);
		box-shadow: 0 8px 22px rgba(29, 91, 215, 0.14);
	}
	.dash.ov .a-sync { order: 1; grid-column: span 4; }
	.dash.ov .a-sales { order: 2; grid-column: span 8; }
	.dash.ov .a-trend { order: 3; grid-column: span 6; }
	.dash.ov .a-products { order: 4; grid-column: span 3; }
	.dash.ov .a-cat { order: 5; grid-column: span 3; }
	.dash.ov .a-branch { order: 6; grid-column: span 7; }
	.dash.ov .a-tags { order: 7; grid-column: span 5; }
	.dash.ov .a-latest { order: 8; grid-column: span 6; }
	.dash.ov .a-act { order: 9; grid-column: span 6; }
	.dash.ov .a-orders { order: 10; }
	@media (max-width: 1280px) {
		.dash.ov .a-sync { order: 1; grid-column: span 5; }
		.dash.ov .a-sales { order: 2; grid-column: span 7; }
		.dash.ov .a-trend { order: 3; grid-column: span 7; }
		.dash.ov .a-products { order: 4; grid-column: span 5; }
		.dash.ov .a-cat { order: 5; grid-column: span 5; }
		.dash.ov .a-branch { order: 6; grid-column: span 7; }
		.dash.ov .a-tags { order: 7; grid-column: span 5; }
		.dash.ov .a-latest { order: 8; grid-column: span 7; }
		.dash.ov .a-act { order: 9; grid-column: span 12; }
	}
	@media (max-width: 900px) {
		.shell {
			flex-direction: column;
		}
		.sidewrap {
			width: 100%;
		}
		/* Phones: brand, filters and account controls stack. */
		.topbar {
			grid-template-columns: minmax(0, 1fr);
		}
		.brandblock,
		.periodblock {
			grid-row: auto;
			grid-column: 1;
		}
		.dash > .cell,
		.dash.ov > .cell {
			grid-column: span 12;
		}
	}
</style>
