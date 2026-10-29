<script lang="ts">
	import { untrack } from 'svelte';
	import { lang, msg } from '../lib/i18n.svelte.js';
	import {
		commandScope,
		productClassificationCommand,
		productOnlinePolicyCommand,
		categoryOnlinePolicyCommand,
		productDetailsCommand,
		categoryDetailsCommand,
		categoryParentsCommand,
		tagDetailsCommand,
		productTypeDetailsCommand,
		productTypeStatusCommand,
		productTypeCreateCommand,
		productTypeAssignmentCommand,
		type CatalogCommandRequest,
		type CommandScope
	} from '../lib/catalogCommands.js';
	import { dashboardApi, ApiError } from '../lib/api.js';
	import { decimalToMinor, formatMinor, minorToDecimal } from '../lib/money.js';
	import SingleSelect from './ui/SingleSelect.svelte';
	import type { AdminProductRow, AdminProductDetail, AdminCategoryRow, AdminTagRow, AdminCommand, AdminProductType, AdminConfiguration, AdminPermissions } from '../lib/api.js';

	// CatalogAdminPage: the Cloud command/control surface for catalog
	// state. It edits nothing directly: every Save persists a typed,
	// revision-checked command (Change queued), Retail applies it, and
	// normal sync projects it back. Current values stay visible until
	// the projection converges; pending badges mark in-flight work.
	//
	// The shop decides in Retail what Cloud may change (permissions below);
	// product, price and catalog changes also need a fresh authenticator
	// code. All text comes from the locale files in the chosen language.
	let { store }: { store: string } = $props();

	type Tab = 'products' | 'types' | 'categories' | 'tags' | 'commands';
	let tab: Tab = $state('products');

	let epoch = 0;
	const requests: Record<string, number> = {};
	let notice = $state<{ kind: 'ok' | 'err'; text: string } | null>(null);
	// Unsaved choices in the editors' selectors, keyed by field and entity.
	let picks = $state<Record<string, string>>({});
	function say(kind: 'ok' | 'err', text: string) {
		notice = { kind, text };
	}
	const k = (key: string, params?: Record<string, string | number>) => msg(`catalogAdmin.${key}`, params);

	// pick shows a record's name in the chosen language, falling back to
	// Arabic when the English name is empty.
	function pick(ar: string | null | undefined, en: string | null | undefined): string {
		return (lang() === 'en' && en ? en : ar) || en || '';
	}

	function apiErrorText(err: unknown, fallback: string): string {
		if (err instanceof ApiError) {
			const reason = err.detail || err.code;
			switch (reason) {
				case 'REVISION_CONFLICT':
				case 'STORE_SCOPE_CONFLICT':
				case 'ENTITY_NOT_FOUND':
				case 'VALIDATION_FAILED':
				case 'UNSUPPORTED_COMMAND':
				case 'CLOUD_PERMISSION_DISABLED':
				case 'RATE_LIMITED':
					return k(`errors.${reason}`);
				default:
					return `${fallback} (${reason})`;
			}
		}
		return fallback;
	}

	// ---- shop permissions ----
	let permissions = $state<AdminPermissions | null>(null);
	let permissionsState = $state<'loading' | 'loaded' | 'error'>('loading');

	async function loadPermissions() {
		if (!store) return;
		const my = epoch;
		permissionsState = 'loading';
		try {
			const p = await dashboardApi.adminPermissions(store);
			if (my !== epoch) return;
			permissions = p;
			permissionsState = 'loaded';
		} catch {
			if (my !== epoch) return;
			permissions = null;
			permissionsState = 'error';
		}
	}

	// can: unknown permissions (read failed) leave the editors usable; the
	// server and Retail still refuse what the shop has not allowed.
	function can(scope: CommandScope): boolean {
		return permissionsState === 'error' || (permissions?.[scope] ?? false);
	}

	// ---- submitting (with the authenticator step for edits) ----
	let mfa = $state<{ request: CatalogCommandRequest; after?: () => void; note?: string } | null>(null);
	let mfaCode = $state('');
	let mfaError = $state('');
	let saving = $state(false);

	function queued(id: string, note?: string): string {
		return k('queued', { note: note ?? k('waitingForRetail'), id: id.slice(0, 8) });
	}

	async function submit(request: CatalogCommandRequest, after?: () => void, note?: string) {
		if (!store || saving) return;
		if (commandScope(request.type) !== 'online_selling') {
			mfa = { request, after, note };
			mfaCode = '';
			mfaError = '';
			return;
		}
		await send(request, undefined, after, note);
	}

	async function send(request: CatalogCommandRequest, code: string | undefined, after?: () => void, note?: string) {
		const actionEpoch = epoch;
		saving = true;
		try {
			const cmd = code ? await dashboardApi.adminCreateCommand(request, code) : await dashboardApi.adminCreateCommand(request);
			if (actionEpoch !== epoch) return;
			mfa = null;
			say('ok', queued(cmd.id, note));
			void loadCommands();
			after?.();
		} catch (err) {
			if (actionEpoch !== epoch) return;
			const reason = err instanceof ApiError ? err.detail || err.code : '';
			if (mfa && (reason === 'MFA_INVALID' || reason === 'MFA_REQUIRED')) {
				mfaError = k('mfa.invalid');
				mfaCode = '';
				return;
			}
			mfa = null;
			say('err', apiErrorText(err, k('errors.commandFailed')));
		} finally {
			if (actionEpoch === epoch) saving = false;
		}
	}

	function confirmMfa(event: SubmitEvent) {
		event.preventDefault();
		const code = mfaCode.replace(/\s/g, '');
		if (!mfa || !/^\d{6}$/.test(code)) {
			mfaError = k('mfa.format');
			return;
		}
		void send(mfa.request, code, mfa.after, mfa.note);
	}

	function cancelMfa() {
		if (!saving) mfa = null;
	}

	// ---- products ----
	let search = $state('');
	let products: AdminProductRow[] = $state([]);
	let productCursor: string | null = $state(null);
	let productsState = $state<'idle' | 'loading' | 'loading-more' | 'loaded' | 'empty' | 'error'>('idle');
	let selectedProduct: AdminProductDetail | null = $state(null);
	let productState = $state<'idle' | 'loading' | 'loaded' | 'error'>('idle');

	async function loadProducts(reset: boolean) {
		if (!store) return;
		const my = epoch;
		const request = requests.loadProducts = (requests.loadProducts ?? 0) + 1;
		productsState = reset ? 'loading' : 'loading-more';
		try {
			const v = await dashboardApi.adminProducts(store, search.trim(), reset ? null : productCursor);
			if (my !== epoch || request !== requests.loadProducts) return;
			products = reset ? v.products : [...products, ...v.products];
			productCursor = v.next_cursor && v.products.length > 0 ? v.next_cursor : null;
			productsState = products.length === 0 ? 'empty' : 'loaded';
		} catch {
			if (my !== epoch || request !== requests.loadProducts) return;
			productsState = 'error';
		}
	}

	async function openProduct(id: string) {
		if (!store) return;
		const my = epoch;
		const request = requests.openProduct = (requests.openProduct ?? 0) + 1;
		productState = 'loading';
		try {
			const v = await dashboardApi.adminProduct(store, id);
			if (my !== epoch || request !== requests.openProduct) return;
			selectedProduct = v;
			try {
				const cfgs = await dashboardApi.adminConfigurations(store, id);
				if (my !== epoch || request !== requests.openProduct) return;
				configurations = cfgs.configurations;
			} catch {
				if (my !== epoch || request !== requests.openProduct) return;
				configurations = [];
			}
			productState = 'loaded';
		} catch {
			if (my !== epoch || request !== requests.openProduct) return;
			productState = 'error';
		}
	}

	function field(id: string): string {
		return (document.getElementById(id) as HTMLInputElement | null)?.value ?? '';
	}

	function saveProductDetails() {
		if (!selectedProduct) return;
		const ar = field('pd-ar').trim();
		const en = field('pd-en').trim();
		// Prices are typed in pounds/dollars; the command carries exact minor units.
		const egp = decimalToMinor(field('pd-egp'));
		const usd = decimalToMinor(field('pd-usd'));
		const cost = decimalToMinor(field('pd-cost'));
		if (!ar) return say('err', k('errors.arabicNameRequired'));
		if (egp === null || usd === null || cost === null) return say('err', k('errors.priceFormat'));
		void submit(productDetailsCommand(store, selectedProduct, { arabic_name: ar, english_name: en, egp_price_cents: egp, usd_price_cents: usd, cost_cents: cost }));
	}

	function saveProductOnline(next: boolean) {
		if (selectedProduct) void submit(productOnlinePolicyCommand(store, selectedProduct, next));
	}

	function saveProductClassification() {
		if (!selectedProduct) return;
		const p = selectedProduct;
		const top = picks['top:' + p.product_id] ?? p.top_category_id;
		const subs = Array.from(document.querySelectorAll<HTMLInputElement>('.pc-sub-check:checked')).map((el) => el.value);
		const tagIds = Array.from(document.querySelectorAll<HTMLInputElement>('.pc-tag-check:checked')).map((el) => el.value);
		void submit(productClassificationCommand(store, p, { top_category_id: top, subcategory_ids: subs, tag_ids: tagIds }));
	}

	// Frame choices are shared and edited in Retail (ADR-049 1g); Cloud
	// shows the product's projected choices read-only.
	let configurations: AdminConfiguration[] = $state([]);

	// ---- categories ----
	let categories: AdminCategoryRow[] = $state([]);
	let categoriesState = $state<'idle' | 'loading' | 'loaded' | 'empty' | 'error'>('idle');
	let selectedCategory: AdminCategoryRow | null = $state(null);

	async function loadCategories() {
		if (!store) return;
		const my = epoch;
		const request = requests.loadCategories = (requests.loadCategories ?? 0) + 1;
		categoriesState = 'loading';
		try {
			const v = await dashboardApi.adminCategories(store);
			if (my !== epoch || request !== requests.loadCategories) return;
			categories = v.categories;
			categoriesState = categories.length === 0 ? 'empty' : 'loaded';
		} catch {
			if (my !== epoch || request !== requests.loadCategories) return;
			categoriesState = 'error';
		}
	}

	function saveCategoryOnline(next: boolean) {
		if (!selectedCategory) return;
		if (!next && !confirm(k('categoryDisableConfirm'))) return;
		void submit(categoryOnlinePolicyCommand(store, selectedCategory, next));
	}

	function saveCategoryDetails() {
		if (!selectedCategory) return;
		const c = selectedCategory;
		const ar = field('cd-ar').trim();
		const en = field('cd-en').trim();
		if (!ar) return say('err', k('errors.arabicNameRequired'));
		void submit(categoryDetailsCommand(store, c, { name_ar: ar, name_en: en, status: picks['cs:' + c.category_id] ?? c.status }));
	}

	function saveCategoryParents() {
		if (!selectedCategory) return;
		const checked = Array.from(document.querySelectorAll<HTMLInputElement>('.cat-parent-check:checked')).map((el) => el.value);
		void submit(categoryParentsCommand(store, selectedCategory, checked), undefined, k('retailChecksGraph'));
	}

	// ---- tags ----
	let tags: AdminTagRow[] = $state([]);
	let tagsState = $state<'idle' | 'loading' | 'loaded' | 'empty' | 'error'>('idle');
	let selectedTag: AdminTagRow | null = $state(null);

	async function loadTags() {
		if (!store) return;
		const my = epoch;
		const request = requests.loadTags = (requests.loadTags ?? 0) + 1;
		tagsState = 'loading';
		try {
			const v = await dashboardApi.adminTags(store);
			if (my !== epoch || request !== requests.loadTags) return;
			tags = v.tags;
			tagsState = tags.length === 0 ? 'empty' : 'loaded';
		} catch {
			if (my !== epoch || request !== requests.loadTags) return;
			tagsState = 'error';
		}
	}

	function saveTagDetails() {
		if (!selectedTag) return;
		const tag = selectedTag;
		const ar = field('td-ar').trim();
		const en = field('td-en').trim();
		const active = (document.getElementById('td-active') as HTMLInputElement | null)?.checked ?? tag.is_active;
		if (!ar) return say('err', k('errors.arabicNameRequired'));
		void submit(tagDetailsCommand(store, tag, { name_ar: ar, name_en: en, is_active: active }));
	}

	// ---- types (Phase 17-R2) ----
	let productTypes: AdminProductType[] = $state([]);
	let typesState = $state<'idle' | 'loading' | 'loaded' | 'empty' | 'error'>('idle');
	let selectedType: AdminProductType | null = $state(null);

	async function loadTypes() {
		if (!store) return;
		const my = epoch;
		const request = requests.loadTypes = (requests.loadTypes ?? 0) + 1;
		typesState = 'loading';
		try {
			const v = await dashboardApi.adminProductTypes(store);
			if (my !== epoch || request !== requests.loadTypes) return;
			productTypes = v.product_types;
			typesState = productTypes.length === 0 ? 'empty' : 'loaded';
		} catch {
			if (my !== epoch || request !== requests.loadTypes) return;
			typesState = 'error';
		}
	}

	function saveTypeDetails() {
		if (!selectedType) return;
		const ar = field('tt-ar').trim();
		const en = field('tt-en').trim();
		if (!ar || !en) return say('err', k('errors.labelsRequired'));
		void submit(productTypeDetailsCommand(store, selectedType, { name_ar: ar, name_en: en }));
	}

	function saveTypeStatus(next: boolean) {
		if (selectedType) void submit(productTypeStatusCommand(store, selectedType, next));
	}

	function createType() {
		const code = field('nt-code').trim().toLowerCase();
		const ar = field('nt-ar').trim();
		const en = field('nt-en').trim();
		if (!/^[a-z0-9_]{1,32}$/.test(code)) return say('err', k('errors.invalidCode'));
		if (!ar || !en) return say('err', k('errors.labelsRequired'));
		void submit(productTypeCreateCommand(store, { code, name_ar: ar, name_en: en, position: productTypes.length }), () => void loadTypes());
	}

	function assignProductType() {
		if (!selectedProduct) return;
		const p = selectedProduct;
		const typeId = picks['pt:' + p.product_id] ?? p.product_type_id ?? productTypes[0]?.type_id ?? '';
		if (!typeId) return say('err', k('errors.selectType'));
		void submit(productTypeAssignmentCommand(store, p, typeId));
	}

	// ---- commands ----
	let commands: AdminCommand[] = $state([]);
	let commandsState = $state<'idle' | 'loading' | 'loaded' | 'empty' | 'error'>('idle');
	let selectedCommand: AdminCommand | null = $state(null);

	async function loadCommands() {
		if (!store) return;
		const my = epoch;
		const request = requests.loadCommands = (requests.loadCommands ?? 0) + 1;
		commandsState = 'loading';
		try {
			const v = await dashboardApi.adminCommands(store, {});
			if (my !== epoch || request !== requests.loadCommands) return;
			commands = v.commands;
			commandsState = commands.length === 0 ? 'empty' : 'loaded';
		} catch {
			if (my !== epoch || request !== requests.loadCommands) return;
			commandsState = 'error';
		}
	}

	async function openCommand(id: string) {
		if (!store) return;
		const my = epoch;
		const request = requests.openCommand = (requests.openCommand ?? 0) + 1;
		const myStore = store;
		try {
			const v = await dashboardApi.adminCommand(store, id);
			if (my !== epoch || request !== requests.openCommand || store !== myStore) return;
			selectedCommand = v;
		} catch (err) {
			if (my !== epoch || request !== requests.openCommand || store !== myStore) return;
			say('err', apiErrorText(err, k('errors.loadFailed')));
		}
	}

	async function cancelCommand(id: string) {
		if (!store || saving) return;
		const actionEpoch = epoch;
		saving = true;
		try {
			const cancelled = await dashboardApi.adminCancelCommand(store, id);
			if (actionEpoch !== epoch) return;
			selectedCommand = cancelled;
			say('ok', k('commandCancelled'));
			void loadCommands();
		} catch (err) {
			if (actionEpoch !== epoch) return;
			say('err', apiErrorText(err, k('errors.cancelFailed')));
		} finally {
			if (actionEpoch === epoch) saving = false;
		}
	}

	const knownStates = ['pending', 'delivered', 'applied', 'converged', 'partial', 'conflict', 'rejected', 'blocked_capability', 'skipped_revoked', 'cancelled'];
	function stateLabel(a: string): string {
		return knownStates.includes(a) ? k(`states.${a}`) : a;
	}
	function resultLabel(code: string | undefined): string {
		if (!code) return '—';
		return code === 'CLOUD_PERMISSION_DENIED' ? k('errors.CLOUD_PERMISSION_DENIED') : code;
	}

	// Phase 17-R0 (ADR-0049): Product has no SKU — the row shows the
	// variant rollup: "1 variant / SKU: X" when the single variant row is
	// loaded (variant rows carry the SKU), otherwise "N variants".
	function variantSummary(p: { variant_count: number; variants?: { sku: string }[] }): string {
		const rows = p.variants ?? [];
		if (p.variant_count === 1 && rows.length === 1) return k('variantSku', { sku: rows[0].sku });
		return p.variant_count === 1 ? k('variantOne') : k('variantMany', { count: p.variant_count });
	}

	// Store race: a slow Store A response must never overwrite Store B.
	$effect(() => {
		const current = store;
		void current;
		epoch++;
		saving = false;
		mfa = null;
		permissions = null;
		permissionsState = 'loading';
		productState = 'idle';
		configurations = [];
		products = [];
		productCursor = null;
		selectedProduct = null;
		categories = [];
		selectedCategory = null;
		tags = [];
		selectedTag = null;
		productTypes = [];
		selectedType = null;
		commands = [];
		selectedCommand = null;
		notice = null;
		if (!store) return;
		untrack(() => {
			void loadPermissions();
			void loadProducts(true);
			void loadCategories();
			void loadTags();
			void loadTypes();
			void loadCommands();
		});
	});
	// Names, not truncated IDs, for the current type and top category.
	function typeName(id: string | null | undefined): string {
		const found = productTypes.find((x) => x.type_id === id);
		return found ? `${pick(found.name_ar, found.name_en)} (${found.code})` : id ? `${id.slice(0, 8)}…` : '—';
	}
	function categoryName(id: string | null | undefined): string {
		const found = categories.find((x) => x.category_id === id);
		return found ? pick(found.name_ar, found.name_en) : id ? `${id.slice(0, 8)}…` : '—';
	}
	const tabs: Tab[] = ['products', 'types', 'categories', 'tags', 'commands'];
	const scopes: CommandScope[] = ['online_selling', 'products', 'catalog'];
</script>

{#snippet yesNo(on: boolean)}
	<span class="pill" class:on>{on ? k('yes') : k('no')}</span>
{/snippet}
{#snippet pending(on: boolean)}
	{#if on}<span class="pill pending">{k('pendingChange')}</span>{:else}<span class="muted">—</span>{/if}
{/snippet}
{#snippet closeButton(onclose: () => void)}
	<button type="button" class="close" aria-label={k('close')} onclick={onclose}>×</button>
{/snippet}
{#snippet locked(scope: CommandScope)}
	{#if !can(scope)}<p class="notice locked-note" role="note">{k(`locked.${scope}`)}</p>{/if}
{/snippet}
{#snippet listState(state: string, empty: string, retry: () => void)}
	{#if state === 'loading'}<p class="muted">{k('loading')}</p>
	{:else if state === 'error'}<p class="muted">{k('loadingFailed')} <button class="btn-secondary btn-sm" type="button" onclick={retry}>{k('retry')}</button></p>
	{:else if state === 'empty'}<p class="muted">{empty}</p>
	{/if}
{/snippet}

<div class="admin card">
	{#if !store}
		<p class="muted">{k('selectStore')}</p>
	{:else}
		{#if notice}<p class="notice {notice.kind}" role="status">{notice.text}</p>{/if}
		<section class="permissions" aria-labelledby="catalog-permissions-heading" data-testid="catalog-permissions">
			<div class="permissions-head">
				<h3 id="catalog-permissions-heading">{k('permissions.title')}</h3>
				<p class="muted">{k('permissions.where')}</p>
			</div>
			{#if permissionsState === 'loading'}
				<p class="muted">{k('loading')}</p>
			{:else if permissionsState === 'error'}
				<p class="muted">{k('permissions.unknown')}</p>
			{:else if permissions}
				{#if permissions.devices === 0}<p class="notice locked-note" role="note">{k('permissions.noDevice')}</p>{/if}
				<ul class="permission-list">
					{#each scopes as scope (scope)}
						<li>
							<span>{k(`permissions.${scope}`)}</span>
							<span class="pill" class:on={permissions[scope]}>{permissions[scope] ? k('permissions.allowed') : k('permissions.off')}</span>
							{#if scope !== 'online_selling'}<span class="muted mfa-hint">{k('permissions.needsCode')}</span>{/if}
						</li>
					{/each}
				</ul>
			{/if}
		</section>
		<div class="tabs" role="tablist" aria-label={k('sections')}>
			{#each tabs as id (id)}
				<button class="tab" type="button" role="tab" aria-selected={tab === id} class:active={tab === id} onclick={() => (tab = id)}>{k(`tabs.${id}`)}</button>
			{/each}
		</div>

		{#if tab === 'products'}
			<form class="row search" onsubmit={(e) => { e.preventDefault(); void loadProducts(true); }}>
				<input id="admin-search" type="search" placeholder={k('searchPlaceholder')} bind:value={search} aria-label={k('searchProducts')} />
				<button class="btn-secondary btn-sm" type="submit">{k('search')}</button>
			</form>
			<div class="split" class:has-detail={!!selectedProduct || productState !== 'idle'}>
				<div class="list">
					{@render listState(productsState, k('noProducts'), () => void loadProducts(true))}
					{#if productsState === 'loaded' || productsState === 'loading-more'}
						<table class="data">
							<thead><tr><th>{k('variants')}</th><th>{k('name')}</th><th>{k('online')}</th><th>{k('state')}</th></tr></thead>
							<tbody>
								{#each products as p (p.product_id)}
									<tr class:selected={selectedProduct?.product_id === p.product_id}>
										<td class="num">{variantSummary(p)}</td>
										<td><button type="button" class="link" onclick={() => void openProduct(p.product_id)}>{pick(p.name_ar, p.name_en)}</button></td>
										<td>{@render yesNo(p.sell_online)}</td>
										<td>{@render pending(p.has_pending)}</td>
									</tr>
								{/each}
							</tbody>
						</table>
						{#if productCursor}<button class="btn-secondary btn-sm" type="button" onclick={() => void loadProducts(false)}>{k('more')}</button>{/if}
					{/if}
				</div>
				{#if productState === 'loading'}
					<p role="status">{k('loadingProduct')}</p>
				{:else if productState === 'error'}
					<p role="alert">{k('productLoadFailed')}</p>
				{/if}
				{#if selectedProduct}
					{@const p = selectedProduct}
					<section class="detail" aria-label={k('productEditor')}>
						<header class="detail-head">
							<h3>{pick(p.name_ar, p.name_en)} <span class="muted num">{variantSummary(p)}</span></h3>
							{@render closeButton(() => (selectedProduct = null))}
						</header>
						<dl class="facts">
							<div><dt>{k('catalogRevision')}</dt><dd class="num" dir="ltr">{p.catalog_revision}</dd></div>
							<div><dt>{k('salesPolicyRevision')}</dt><dd class="num" dir="ltr">{p.sales_policy_revision}</dd></div>
							<div><dt>{k('configurationRevision')}</dt><dd class="num" dir="ltr">{p.configuration_revision}</dd></div>
							<div><dt>{k('derivedStock')}</dt><dd class="num" dir="ltr">{p.derived_stock}</dd></div>
						</dl>
						{#if p.variants && p.variants.length > 0}
							<h4>{k('variants')}</h4>
							<table class="data">
								<thead><tr><th>{k('sku')}</th><th>{k('option')}</th><th>{k('stock')}</th><th>{k('state')}</th></tr></thead>
								<tbody>
									{#each p.variants as v (v.variant_id)}
										<tr>
											<td class="num" dir="ltr">{v.sku}</td>
											<td>{v.attributes.map((a) => `${pick(a.definition_name_ar, a.definition_name_en)}: ${pick(a.name_ar, a.name_en)}`).join(' · ') || '—'}</td>
											<td class="num" dir="ltr">{v.stock_quantity}</td>
											<td><span class="pill" class:on={!v.deleted && v.is_active}>{v.deleted ? k('deleted') : v.is_active ? k('active') : k('inactive')}</span></td>
										</tr>
									{/each}
								</tbody>
							</table>
						{/if}
						{#if p.has_pending}<p class="notice pending-note" role="status">{k('pendingProductNote')}</p>{/if}
						<h4>{k('detailsAndPrices')}</h4>
						{@render locked('products')}
						<fieldset class="plain" disabled={!can('products')}>
							<div class="grid">
								<label class="field">{k('arabicName')} <input id="pd-ar" type="text" value={p.name_ar} dir="rtl" /></label>
								<label class="field">{k('englishName')} <input id="pd-en" type="text" value={p.name_en ?? ''} dir="ltr" /></label>
								<label class="field">{k('priceEgp')} <input id="pd-egp" type="text" inputmode="decimal" value={minorToDecimal(p.egp_price_minor)} dir="ltr" /></label>
								<label class="field">{k('priceUsd')} <input id="pd-usd" type="text" inputmode="decimal" value={minorToDecimal(p.usd_price_minor ?? '0')} dir="ltr" /></label>
								<label class="field">{k('costEgp')} <input id="pd-cost" type="text" inputmode="decimal" value={minorToDecimal(p.cost_minor)} dir="ltr" /></label>
							</div>
							<button class="btn-primary btn-sm" type="button" disabled={saving} onclick={saveProductDetails}>{k('queueDetails')}</button>
						</fieldset>
						<h4>{k('productType')}</h4>
						{@render locked('catalog')}
						<p class="muted">{k('currentType')} <strong>{typeName(p.product_type_id)}</strong> — {k('typeCheckedByRetail')}</p>
						<div class="row">
							<SingleSelect
								id="pt-assign"
								label={k('assignType')}
								hideLabel
								disabled={!can('catalog')}
								value={picks['pt:' + p.product_id] ?? p.product_type_id ?? productTypes[0]?.type_id ?? ''}
								options={productTypes.map((x) => ({ value: x.type_id, label: `${pick(x.name_ar, x.name_en)} (${x.code})` }))}
								onchange={(v) => (picks['pt:' + p.product_id] = v)}
							/>
							<button class="btn-primary btn-sm" type="button" disabled={saving || !can('catalog')} onclick={assignProductType}>{k('queueType')}</button>
						</div>
						<h4>{k('onlineAvailability')}</h4>
						{@render locked('online_selling')}
						<p class="muted">{k('currentValue')} <span class="pill" class:on={p.sell_online}>{p.sell_online ? k('on') : k('off')}</span> — {k('offlineUnchanged')}</p>
						<button class="btn-secondary btn-sm" type="button" disabled={saving || !can('online_selling')} onclick={() => saveProductOnline(!p.sell_online)}>{p.sell_online ? k('disableOnline') : k('enableOnline')}</button>
						<h4>{k('classification')}</h4>
						<p class="muted">{k('currentTopCategory')} <strong>{categoryName(p.top_category_id)}</strong> — {k('retailValidates')}</p>
						<fieldset class="plain" disabled={!can('catalog')}>
							<SingleSelect
								id="pc-top"
								label={k('topCategory')}
								disabled={!can('catalog')}
								value={picks['top:' + p.product_id] ?? p.top_category_id}
								options={categories.map((c) => ({ value: c.category_id, label: pick(c.name_ar, c.name_en) }))}
								onchange={(v) => (picks['top:' + p.product_id] = v)}
							/>
							<fieldset>
								<legend>{k('subcategories')}</legend>
								{#each categories.filter((x) => x.category_id !== p.top_category_id) as c (c.category_id)}
									<label class="check"><input type="checkbox" class="pc-sub-check" value={c.category_id} checked={p.subcategory_ids.includes(c.category_id)} /> {pick(c.name_ar, c.name_en)}</label>
								{/each}
							</fieldset>
							<fieldset>
								<legend>{k('tags')}</legend>
								{#each tags as tg (tg.tag_id)}
									<label class="check"><input type="checkbox" class="pc-tag-check" value={tg.tag_id} checked={p.tag_ids.includes(tg.tag_id)} /> {pick(tg.name_ar, tg.name_en)}</label>
								{/each}
							</fieldset>
							<div><button class="btn-primary btn-sm" type="button" disabled={saving} onclick={saveProductClassification}>{k('queueClassification')}</button></div>
						</fieldset>
						<h4>{k('frameOptions')}</h4>
						<p class="muted">{k('frameOptionsReadOnly')}</p>
						{#if configurations.length === 0}<p class="muted">{k('noFrameOptions')}</p>
						{:else}
							<ul data-testid="frame-configurations">
								{#each configurations as c (c.id)}
									<li>{pick(c.style_name_ar, c.style_name_en)} / {pick(c.color_name_ar, c.color_name_en)} <span class="num">+{formatMinor(c.egp_delta_minor, 'EGP')}</span>{#if c.usd_delta_minor !== null}{' · '}<span class="num">+{formatMinor(c.usd_delta_minor, 'USD')}</span>{/if}{c.enabled ? '' : ` · ${k('inactive')}`}</li>
								{/each}
							</ul>
						{/if}
					</section>
				{/if}
			</div>
		{/if}

		{#if tab === 'types'}
			<div class="split" class:has-detail={!!selectedType}>
				<div class="list">
					{@render listState(typesState, k('noTypes'), () => void loadTypes())}
					{#if typesState === 'loaded'}
						<table class="data">
							<thead><tr><th>{k('type')}</th><th>{k('code')}</th><th>{k('active')}</th><th>{k('state')}</th></tr></thead>
							<tbody>
								{#each productTypes as pt (pt.type_id)}
									<tr class:selected={selectedType?.type_id === pt.type_id}>
										<td><button type="button" class="link" onclick={() => (selectedType = pt)}>{pick(pt.name_ar, pt.name_en)}</button></td>
										<td class="num" dir="ltr">{pt.code}</td>
										<td>{@render yesNo(pt.is_active)}</td>
										<td>{@render pending(!!pt.has_pending)}</td>
									</tr>
								{/each}
							</tbody>
						</table>
					{/if}
					<section class="detail create" aria-label={k('newType')}>
						<h3>{k('newType')}</h3>
						{@render locked('catalog')}
						<fieldset class="plain" disabled={!can('catalog')}>
							<div class="grid">
								<label class="field">{k('code')} <input id="nt-code" type="text" dir="ltr" placeholder="book" /></label>
								<label class="field">{k('arabicName')} <input id="nt-ar" type="text" dir="rtl" /></label>
								<label class="field">{k('englishName')} <input id="nt-en" type="text" dir="ltr" /></label>
							</div>
							<button class="btn-primary btn-sm" type="button" disabled={saving} onclick={createType}>{k('queueTypeCreation')}</button>
						</fieldset>
					</section>
				</div>
				{#if selectedType}
					{@const pt = selectedType}
					<section class="detail" aria-label={k('typeEditor')}>
						<header class="detail-head">
							<h3>{pick(pt.name_ar, pt.name_en)} <span class="muted num" dir="ltr">{pt.code} · {k('rev', { rev: pt.type_revision })}</span></h3>
							{@render closeButton(() => (selectedType = null))}
						</header>
						<p class="muted">{k('dimensions')}: <span dir="ltr">{pt.dimensions.join(', ') || '—'}</span> · {k('capabilities')}: <span dir="ltr">{pt.capabilities.join(', ') || '—'}</span></p>
						{@render locked('catalog')}
						<fieldset class="plain" disabled={!can('catalog')}>
							<div class="grid">
								<label class="field">{k('arabicName')} <input id="tt-ar" type="text" value={pt.name_ar} dir="rtl" /></label>
								<label class="field">{k('englishName')} <input id="tt-en" type="text" value={pt.name_en} dir="ltr" /></label>
							</div>
							<div class="row">
								<button class="btn-primary btn-sm" type="button" disabled={saving} onclick={saveTypeDetails}>{k('queueTypeChange')}</button>
								<button class="btn-secondary btn-sm" type="button" disabled={saving} onclick={() => saveTypeStatus(!pt.is_active)}>{pt.is_active ? k('deactivate') : k('activate')}</button>
							</div>
						</fieldset>
					</section>
				{/if}
			</div>
		{/if}

		{#if tab === 'categories'}
			<div class="split" class:has-detail={!!selectedCategory}>
				<div class="list">
					{@render listState(categoriesState, k('noCategories'), () => void loadCategories())}
					{#if categoriesState === 'loaded'}
						<table class="data">
							<thead><tr><th>{k('name')}</th><th>{k('status')}</th><th>{k('online')}</th><th>{k('state')}</th></tr></thead>
							<tbody>
								{#each categories as c (c.category_id)}
									<tr class:selected={selectedCategory?.category_id === c.category_id}>
										<td><button type="button" class="link" onclick={() => (selectedCategory = c)}>{pick(c.name_ar, c.name_en)}</button></td>
										<td><span class="pill" class:on={c.status === 'active'}>{c.status === 'active' || c.status === 'hidden' ? k(`categoryStatus.${c.status}`) : c.status}</span></td>
										<td>{@render yesNo(c.online_enabled)}</td>
										<td>{@render pending(c.has_pending)}</td>
									</tr>
								{/each}
							</tbody>
						</table>
					{/if}
				</div>
				{#if selectedCategory}
					{@const c = selectedCategory}
					<section class="detail" aria-label={k('categoryEditor')}>
						<header class="detail-head">
							<h3>{pick(c.name_ar, c.name_en)} <span class="muted num" dir="ltr">{k('rev', { rev: c.catalog_revision })}</span></h3>
							{@render closeButton(() => (selectedCategory = null))}
						</header>
						{#if c.has_pending}<p class="notice pending-note" role="status">{k('pendingNote')}</p>{/if}
						{@render locked('catalog')}
						<fieldset class="plain" disabled={!can('catalog')}>
							<div class="grid">
								<label class="field">{k('arabicName')} <input id="cd-ar" type="text" value={c.name_ar} dir="rtl" /></label>
								<label class="field">{k('englishName')} <input id="cd-en" type="text" value={c.name_en ?? ''} dir="ltr" /></label>
								<SingleSelect
									id="cd-status"
									label={k('status')}
									disabled={!can('catalog')}
									value={picks['cs:' + c.category_id] ?? c.status}
									options={[{ value: 'active', label: k('categoryStatus.active') }, { value: 'hidden', label: k('categoryStatus.hidden') }]}
									onchange={(v) => (picks['cs:' + c.category_id] = v)}
								/>
							</div>
							<button class="btn-primary btn-sm" type="button" disabled={saving} onclick={saveCategoryDetails}>{k('queueDetails')}</button>
						</fieldset>
						<h4>{k('categoryOnline')}</h4>
						{@render locked('online_selling')}
						<p class="muted">{k('categoryOnlineHint')}</p>
						<button class="btn-secondary btn-sm" type="button" disabled={saving || !can('online_selling')} onclick={() => saveCategoryOnline(!c.online_enabled)}>{c.online_enabled ? k('disableOnline') : k('enableOnline')}</button>
						<h4>{k('parents')}</h4>
						<p class="muted">{k('parentsHint')}</p>
						<fieldset class="plain" disabled={!can('catalog')}>
							<div class="checks">
								{#each categories.filter((x) => x.category_id !== c.category_id) as other (other.category_id)}
									<label class="check"><input type="checkbox" class="cat-parent-check" value={other.category_id} checked={c.parent_ids.includes(other.category_id)} /> {pick(other.name_ar, other.name_en)}</label>
								{/each}
							</div>
							<div><button class="btn-primary btn-sm" type="button" disabled={saving} onclick={saveCategoryParents}>{k('queueParents')}</button></div>
						</fieldset>
					</section>
				{/if}
			</div>
		{/if}

		{#if tab === 'tags'}
			<div class="split" class:has-detail={!!selectedTag}>
				<div class="list">
					{@render listState(tagsState, k('noTags'), () => void loadTags())}
					{#if tagsState === 'loaded'}
						<table class="data">
							<thead><tr><th>{k('name')}</th><th>{k('slug')}</th><th>{k('active')}</th><th>{k('state')}</th></tr></thead>
							<tbody>
								{#each tags as tg (tg.tag_id)}
									<tr class:selected={selectedTag?.tag_id === tg.tag_id}>
										<td><button type="button" class="link" onclick={() => (selectedTag = tg)}>{pick(tg.name_ar, tg.name_en)}</button></td>
										<td class="num" dir="ltr">{tg.slug}</td>
										<td>{@render yesNo(tg.is_active)}</td>
										<td>{@render pending(tg.has_pending)}</td>
									</tr>
								{/each}
							</tbody>
						</table>
					{/if}
				</div>
				{#if selectedTag}
					{@const tg = selectedTag}
					<section class="detail" aria-label={k('tagEditor')}>
						<header class="detail-head">
							<h3>{pick(tg.name_ar, tg.name_en)} <span class="muted num" dir="ltr">{k('rev', { rev: tg.catalog_revision })}</span></h3>
							{@render closeButton(() => (selectedTag = null))}
						</header>
						{@render locked('catalog')}
						<fieldset class="plain" disabled={!can('catalog')}>
							<div class="grid">
								<label class="field">{k('arabicName')} <input id="td-ar" type="text" value={tg.name_ar} dir="rtl" /></label>
								<label class="field">{k('englishName')} <input id="td-en" type="text" value={tg.name_en ?? ''} dir="ltr" /></label>
								<label class="check"><input id="td-active" type="checkbox" checked={tg.is_active} /> {k('active')}</label>
							</div>
							<button class="btn-primary btn-sm" type="button" disabled={saving} onclick={saveTagDetails}>{k('queueTag')}</button>
						</fieldset>
					</section>
				{/if}
			</div>
		{/if}

		{#if tab === 'commands'}
			<div class="split" class:has-detail={!!selectedCommand}>
				<div class="list">
					{@render listState(commandsState, k('noCommands'), () => void loadCommands())}
					{#if commandsState === 'loaded'}
						<table class="data">
							<thead><tr><th>{k('type')}</th><th>{k('status')}</th><th>{k('actor')}</th></tr></thead>
							<tbody>
								{#each commands as cmd (cmd.id)}
									<tr class:selected={selectedCommand?.id === cmd.id}>
										<td><button type="button" class="link" onclick={() => void openCommand(cmd.id)}><span dir="ltr">{cmd.type}</span></button></td>
										<td><span class="chip">{stateLabel(cmd.aggregate)}</span></td>
										<td>{cmd.actor}</td>
									</tr>
								{/each}
							</tbody>
						</table>
					{/if}
				</div>
				{#if selectedCommand}
					{@const cmd = selectedCommand}
					<section class="detail" aria-label={k('commandDetail')}>
						<header class="detail-head">
							<h3 dir="ltr">{cmd.type}</h3>
							{@render closeButton(() => (selectedCommand = null))}
						</header>
						<p><span class="chip">{stateLabel(cmd.aggregate)}</span> <span class="muted num" dir="ltr">{k('rev', { rev: cmd.expected_revision })}</span></p>
						{#if cmd.target_kind === 'create'}
							<p class="muted">
								{#if cmd.result_entity_id}
									{k('created')} — <span class="num" dir="ltr">{cmd.result_entity_id}</span>
								{:else}
									{k('createWaiting', { key: cmd.requested_key ?? '' })}
								{/if}
							</p>
						{/if}
						{#if cmd.targets}
							<table class="data">
								<thead><tr><th>{k('device')}</th><th>{k('status')}</th><th>{k('result')}</th></tr></thead>
								<tbody>
									{#each cmd.targets as tgt (tgt.id)}
										<tr>
											<td class="num" dir="ltr">{tgt.device_name || tgt.device_id.slice(0, 8)}</td>
											<td>{stateLabel(tgt.status)}{#if tgt.capable === false} — {k('states.blocked_capability')}{/if}</td>
											<td class="num">{resultLabel(tgt.result_code)}</td>
										</tr>
									{/each}
								</tbody>
							</table>
						{/if}
						{#if cmd.status === 'pending' && !cmd.targets?.some((x) => x.status === 'delivered' || x.status === 'applied')}
							<button class="btn-danger btn-sm" type="button" disabled={saving} onclick={() => void cancelCommand(cmd.id)}>{k('cancelPending')}</button>
						{/if}
					</section>
				{/if}
			</div>
		{/if}
	{/if}
</div>

{#if mfa}
	<div class="mfa-backdrop" role="presentation">
		<div class="mfa-dialog card" role="dialog" aria-modal="true" aria-labelledby="mfa-title" aria-describedby="mfa-hint">
		<form onsubmit={confirmMfa}>
			<h3 id="mfa-title">{k('mfa.title')}</h3>
			<p id="mfa-hint" class="muted">{k('mfa.hint')}</p>
			<label class="field">{k('mfa.code')}
				<!-- svelte-ignore a11y_autofocus -->
				<input id="mfa-code" type="text" inputmode="numeric" autocomplete="one-time-code" maxlength="7" dir="ltr" autofocus
					bind:value={mfaCode} disabled={saving} aria-invalid={mfaError ? 'true' : undefined} aria-describedby={mfaError ? 'mfa-error' : undefined} />
			</label>
			{#if mfaError}<p id="mfa-error" class="notice err" role="alert">{mfaError}</p>{/if}
			<div class="row end">
				<button class="btn-secondary btn-sm" type="button" disabled={saving} onclick={cancelMfa}>{k('mfa.cancel')}</button>
				<button class="btn-primary btn-sm" type="submit" disabled={saving}>{k('mfa.confirm')}</button>
			</div>
		</form>
		</div>
	</div>
{/if}

<style>
	.admin { display: flex; flex-direction: column; gap: 0.75rem; }
	.tabs { display: flex; gap: 1.25rem; border-bottom: 1px solid var(--border); }
	.tab { border: 0; border-bottom: 3px solid transparent; background: transparent; padding: 0.6rem 0.15rem; color: var(--text-muted); font-weight: 600; border-radius: 0; cursor: pointer; }
	.tab.active { border-bottom-color: var(--primary); color: var(--primary); }
	.row { display: flex; gap: 0.5rem; align-items: center; flex-wrap: wrap; }
	.row.end { justify-content: flex-end; }
	.search input { flex: 0 1 22rem; min-width: 12rem; padding: 6px 10px; border: 1px solid var(--field-border); border-radius: var(--radius-control); }
	/* List and editor side by side once something is open. */
	.split { display: grid; grid-template-columns: minmax(0, 1fr); gap: 1rem; align-items: start; }
	.split.has-detail { grid-template-columns: minmax(0, 1fr) minmax(0, 1.25fr); }
	.list { min-width: 0; display: flex; flex-direction: column; gap: 0.6rem; }
	.grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 0.6rem; margin: 0.5rem 0; align-items: end; }
	.field { display: flex; flex-direction: column; gap: 4px; font-size: 0.8rem; font-weight: 600; }
	.field input { width: 100%; box-sizing: border-box; }
	.detail { min-width: 0; border: 1px solid var(--border); border-radius: var(--radius-card); padding: 0.75rem 1rem; background: #fbfcfe; }
	.detail.create { background: transparent; }
	.detail h3 { margin: 0; font-size: 1rem; }
	.detail h4 { margin: 1rem 0 0.35rem; font-size: 0.86rem; padding-top: 0.6rem; border-top: 1px solid var(--border); }
	.detail-head { display: flex; justify-content: space-between; align-items: flex-start; gap: 0.5rem; margin-bottom: 0.5rem; }
	.close { border: 0; background: transparent; font-size: 1.3rem; line-height: 1; cursor: pointer; color: var(--text-muted); padding: 0 0.25rem; }
	.facts { display: grid; grid-template-columns: repeat(auto-fit, minmax(130px, 1fr)); gap: 0.4rem; margin: 0 0 0.5rem; }
	.facts div { background: var(--surface-muted); border-radius: var(--radius-control); padding: 0.35rem 0.5rem; }
	.facts dt { font-size: 0.7rem; color: var(--text-muted); }
	.facts dd { margin: 0; font-weight: 700; }
	.notice { margin: 0; padding: 0.5rem 0.75rem; border-radius: var(--radius-control); font-weight: 600; }
	.notice.ok { background: #e8f6ee; color: #15803d; }
	.notice.err { background: #fdeeee; color: #991b1b; }
	.notice.pending-note, .notice.locked-note { background: #fdf4e7; color: #92400e; }
	.notice.locked-note { margin: 0.25rem 0 0.5rem; font-weight: 500; font-size: 0.82rem; }
	.chip { border: 1px solid currentColor; border-radius: 0.4rem; padding: 0.1rem 0.5rem; }
	.pill { display: inline-block; padding: 1px 8px; border-radius: 999px; background: var(--surface-muted); color: var(--text-muted); font-size: 0.74rem; font-weight: 600; }
	.pill.on { background: #e8f6ee; color: #15803d; }
	.pill.pending { background: #fdf4e7; color: #92400e; }
	.link { background: none; border: none; padding: 0; cursor: pointer; color: var(--primary); font: inherit; font-weight: 600; text-align: start; }
	.link:hover { text-decoration: underline; }
	.checks { display: flex; flex-wrap: wrap; gap: 0.25rem 0.8rem; }
	.check { display: inline-flex; gap: 0.3rem; align-items: center; margin-inline-end: 0.8rem; }
	fieldset { border: 1px solid var(--border); border-radius: var(--radius-control); margin: 0.5rem 0; padding: 0.4rem 0.75rem; }
	fieldset.plain { border: 0; margin: 0; padding: 0; min-width: 0; }
	fieldset.plain:disabled { opacity: 0.6; }
	.muted { opacity: 0.75; }
	.num { font-variant-numeric: tabular-nums; }
	.permissions { display: grid; grid-template-columns: minmax(12rem, auto) minmax(0, 1fr); gap: 0.25rem 1rem; align-items: center; border: 1px solid var(--border); border-radius: var(--radius-card); padding: 0.6rem 0.9rem; background: var(--surface-muted); }
	.permissions-head h3 { margin: 0; font-size: 0.9rem; }
	.permissions-head p { margin: 0.1rem 0 0; font-size: 0.78rem; }
	.permissions > p.muted { margin: 0; }
	.permission-list { display: flex; flex-wrap: wrap; gap: 0.4rem 1.25rem; margin: 0; padding: 0; list-style: none; font-size: 0.84rem; }
	.permission-list li { display: inline-flex; gap: 0.4rem; align-items: center; }
	.mfa-hint { font-size: 0.74rem; }
	@media (max-width: 900px) { .permissions { grid-template-columns: minmax(0, 1fr); } }
	table.data { width: 100%; border-collapse: collapse; border: 1px solid var(--border); border-radius: var(--radius-control); font-size: 0.84rem; }
	table.data th, table.data td { text-align: start; padding: 0.45rem 0.6rem; border-bottom: 1px solid var(--border); }
	table.data thead th { background: var(--surface-muted); font-size: 0.76rem; font-weight: 700; }
	table.data tbody tr:hover { background: #f6f9fe; }
	table.data tbody tr.selected { background: var(--primary-soft); }
	.mfa-backdrop { position: fixed; inset: 0; background: rgb(15 23 42 / 0.45); display: grid; place-items: center; z-index: 50; padding: 1rem; }
	.mfa-dialog { width: min(24rem, 100%); padding: 1.25rem; }
	.mfa-dialog form { display: flex; flex-direction: column; gap: 0.75rem; }
	.mfa-dialog h3 { margin: 0; font-size: 1rem; }
	.mfa-dialog p { margin: 0; }
	.mfa-dialog input { font-size: 1.25rem; letter-spacing: 0.3em; text-align: center; padding: 0.4rem; }
	@media (max-width: 1100px) { .split.has-detail { grid-template-columns: minmax(0, 1fr); } }
</style>
