<script lang="ts">
	import { arabic, tb } from '../lib/i18n.svelte.js';

	// queued confirms a command waiting for Retail to apply it.
	function queued(id: string, ar = 'بانتظار الكاشير', en = 'waiting for Retail'): string {
		return arabic() ? `تم إدراج التغيير في قائمة الانتظار — ${ar}. الأمر ${id.slice(0, 8)}… / Change queued — ${en}.` : `Change queued — ${en}. Command ${id.slice(0, 8)}…`;
	}
	import { untrack } from 'svelte';
	import { productClassificationCommand, productOnlinePolicyCommand, categoryOnlinePolicyCommand, productDetailsCommand, categoryDetailsCommand, categoryParentsCommand, tagDetailsCommand, productTypeDetailsCommand, productTypeStatusCommand, productTypeCreateCommand, productTypeAssignmentCommand } from '../lib/catalogCommands.js';
	import { dashboardApi, ApiError } from '../lib/api.js';
	import SingleSelect from './ui/SingleSelect.svelte';
	import type { AdminProductRow, AdminProductDetail, AdminCategoryRow, AdminTagRow, AdminCommand, AdminProductType, AdminConfiguration } from '../lib/api.js';

	// CatalogAdminPage: the Cloud command/control surface for catalog
	// state. It edits nothing directly: every Save persists a typed,
	// revision-checked command (Change queued), Retail applies it, and
	// normal sync projects it back. Current values stay visible until
	// the projection converges; pending badges mark in-flight work.
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

	function apiErrorText(err: unknown, fallback: string): string {
		if (err instanceof ApiError) {
			switch (err.code) {
				case 'REVISION_CONFLICT':
					return tb('تعارض مراجعة — تغيّر العنصر في الكاشير بعد تحميل الصفحة. حدّث قبل إعادة المحاولة.', 'Revision conflict — refresh before retrying.');
				case 'STORE_SCOPE_CONFLICT':
					return tb('نطاق متجر غير صالح.', 'Invalid Store scope.');
				case 'ENTITY_NOT_FOUND':
					return tb('العنصر غير موجود.', 'Entity not found.');
				case 'VALIDATION_FAILED':
					return tb('تحقق غير صالح — راجع القيم.', 'Invalid values.');
				case 'UNSUPPORTED_COMMAND':
					return tb('أمر غير مدعوم.', 'Unsupported command.');
				default:
					return `${fallback} (${err.code})`;
			}
		}
		return fallback;
	}

	// ---- products ----
	let search = $state('');
	let products: AdminProductRow[] = $state([]);
	let productCursor: string | null = $state(null);
	let productsState = $state<'idle' | 'loading' | 'loading-more' | 'loaded' | 'empty' | 'error'>('idle');
	let selectedProduct: AdminProductDetail | null = $state(null);
	let productState = $state<'idle' | 'loading' | 'loaded' | 'error'>('idle');
	let saving = $state(false);

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

	async function saveProductDetails() {
		if (!selectedProduct || !store || saving) return;
		const p = selectedProduct;
		const ar = (document.getElementById('pd-ar') as HTMLInputElement)?.value.trim() ?? '';
		const en = (document.getElementById('pd-en') as HTMLInputElement)?.value.trim() ?? '';
		const egp = (document.getElementById('pd-egp') as HTMLInputElement)?.value.trim() ?? '';
		const usd = (document.getElementById('pd-usd') as HTMLInputElement)?.value.trim() ?? '';
		const cost = (document.getElementById('pd-cost') as HTMLInputElement)?.value.trim() ?? '';
		if (!ar) return say('err', tb('الاسم العربي مطلوب.', 'Arabic name required.'));
		for (const [label, v] of [['EGP', egp], ['USD', usd], ['cost', cost]] as const) {
			if (!/^\d+$/.test(v)) return say('err', arabic() ? `قيمة ${label} يجب أن تكون أرقامًا صحيحة (وحدات صغرى). / ${label} must be integer minor units.` : `${label} must be integer minor units.`);
		}
		const actionEpoch = epoch;
		saving = true;
		try {
			const cmd = await dashboardApi.adminCreateCommand(productDetailsCommand(store, p, { arabic_name: ar, english_name: en, egp_price_cents: egp, usd_price_cents: usd, cost_cents: cost }));
			if (actionEpoch !== epoch) return;
			say('ok', queued(cmd.id));
			void loadCommands();
		} catch (err) {
			if (actionEpoch !== epoch) return;
			say('err', apiErrorText(err, tb('فشل إنشاء الأمر.', 'Command failed.')));
		} finally {
			if (actionEpoch === epoch) saving = false;
		}
	}

	async function saveProductOnline(next: boolean) {
		if (!selectedProduct || !store || saving) return;
		const p = selectedProduct;
		const actionEpoch = epoch;
		saving = true;
		try {
			const cmd = await dashboardApi.adminCreateCommand(productOnlinePolicyCommand(store, p, next));
			if (actionEpoch !== epoch) return;
			say('ok', queued(cmd.id));
			void loadCommands();
		} catch (err) {
			if (actionEpoch !== epoch) return;
			say('err', apiErrorText(err, tb('فشل إنشاء الأمر.', 'Command failed.')));
		} finally {
			if (actionEpoch === epoch) saving = false;
		}
	}

	async function saveProductClassification() {
		if (!selectedProduct || !store || saving) return;
		const p = selectedProduct;
		const top = picks['top:' + p.product_id] ?? p.top_category_id;
		const subs = Array.from(document.querySelectorAll<HTMLInputElement>('.pc-sub-check:checked')).map((el) => el.value);
		const tagIds = Array.from(document.querySelectorAll<HTMLInputElement>('.pc-tag-check:checked')).map((el) => el.value);
		const actionEpoch = epoch;
		saving = true;
		try {
			const cmd = await dashboardApi.adminCreateCommand(productClassificationCommand(store, p, { top_category_id: top, subcategory_ids: subs, tag_ids: tagIds }));
			if (actionEpoch !== epoch) return;
			say('ok', queued(cmd.id));
			void loadCommands();
		} catch (err) {
			if (actionEpoch !== epoch) return;
			say('err', apiErrorText(err, tb('فشل إنشاء الأمر.', 'Command failed.')));
		} finally {
			if (actionEpoch === epoch) saving = false;
		}
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

	async function saveCategoryOnline(next: boolean) {
		if (!selectedCategory || !store || saving) return;
		if (!next) {
			const ok = confirm(tb('تعطيل هذه الفئة يزيل المنتجات المتأثرة من القنوات الإلكترونية دون تغيير قيمة sell_online لكل منتج. متابعة؟', 'Disabling removes affected Products from online channels without changing each Product\u2019s own sell_online. Continue?'));
			if (!ok) return;
		}
		const c = selectedCategory;
		const actionEpoch = epoch;
		saving = true;
		try {
			const cmd = await dashboardApi.adminCreateCommand(categoryOnlinePolicyCommand(store, c, next));
			if (actionEpoch !== epoch) return;
			say('ok', queued(cmd.id));
			void loadCommands();
		} catch (err) {
			if (actionEpoch !== epoch) return;
			say('err', apiErrorText(err, tb('فشل إنشاء الأمر.', 'Command failed.')));
		} finally {
			if (actionEpoch === epoch) saving = false;
		}
	}

	async function saveCategoryDetails() {
		if (!selectedCategory || !store || saving) return;
		const c = selectedCategory;
		const ar = (document.getElementById('cd-ar') as HTMLInputElement)?.value.trim() ?? '';
		const en = (document.getElementById('cd-en') as HTMLInputElement)?.value.trim() ?? '';
		const status = picks['cs:' + c.category_id] ?? c.status;
		if (!ar) return say('err', tb('الاسم العربي مطلوب.', 'Arabic name required.'));
		const actionEpoch = epoch;
		saving = true;
		try {
			const cmd = await dashboardApi.adminCreateCommand(categoryDetailsCommand(store, c, { name_ar: ar, name_en: en, status }));
			if (actionEpoch !== epoch) return;
			say('ok', queued(cmd.id));
			void loadCommands();
		} catch (err) {
			if (actionEpoch !== epoch) return;
			say('err', apiErrorText(err, tb('فشل إنشاء الأمر.', 'Command failed.')));
		} finally {
			if (actionEpoch === epoch) saving = false;
		}
	}

	async function saveCategoryParents() {
		if (!selectedCategory || !store || saving) return;
		const c = selectedCategory;
		const checked = Array.from(document.querySelectorAll<HTMLInputElement>('.cat-parent-check:checked')).map((el) => el.value);
		const actionEpoch = epoch;
		saving = true;
		try {
			const cmd = await dashboardApi.adminCreateCommand(categoryParentsCommand(store, c, checked));
			if (actionEpoch !== epoch) return;
			say('ok', queued(cmd.id, 'يتحقق الكاشير من عدم وجود دورات', 'Retail validates the graph'));
			void loadCommands();
		} catch (err) {
			if (actionEpoch !== epoch) return;
			say('err', apiErrorText(err, tb('فشل إنشاء الأمر.', 'Command failed.')));
		} finally {
			if (actionEpoch === epoch) saving = false;
		}
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

	async function saveTagDetails() {
		if (!selectedTag || !store || saving) return;
		const t = selectedTag;
		const ar = (document.getElementById('td-ar') as HTMLInputElement)?.value.trim() ?? '';
		const en = (document.getElementById('td-en') as HTMLInputElement)?.value.trim() ?? '';
		const active = (document.getElementById('td-active') as HTMLInputElement)?.checked ?? t.is_active;
		if (!ar) return say('err', tb('الاسم العربي مطلوب.', 'Arabic name required.'));
		const actionEpoch = epoch;
		saving = true;
		try {
			const cmd = await dashboardApi.adminCreateCommand(tagDetailsCommand(store, t, { name_ar: ar, name_en: en, is_active: active }));
			if (actionEpoch !== epoch) return;
			say('ok', queued(cmd.id));
			void loadCommands();
		} catch (err) {
			if (actionEpoch !== epoch) return;
			say('err', apiErrorText(err, tb('فشل إنشاء الأمر.', 'Command failed.')));
		} finally {
			if (actionEpoch === epoch) saving = false;
		}
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

	async function saveTypeDetails() {
		if (!selectedType || !store || saving) return;
		const t = selectedType;
		const ar = (document.getElementById('tt-ar') as HTMLInputElement)?.value.trim() ?? '';
		const en = (document.getElementById('tt-en') as HTMLInputElement)?.value.trim() ?? '';
		if (!ar || !en) return say('err', tb('التسميات مطلوبة.', 'Labels required.'));
		const actionEpoch = epoch;
		saving = true;
		try {
			const cmd = await dashboardApi.adminCreateCommand(productTypeDetailsCommand(store, t, { name_ar: ar, name_en: en }));
			if (actionEpoch !== epoch) return;
			say('ok', queued(cmd.id));
			void loadCommands();
		} catch (err) {
			if (actionEpoch !== epoch) return;
			say('err', apiErrorText(err, tb('فشل إنشاء الأمر.', 'Command failed.')));
		} finally {
			if (actionEpoch === epoch) saving = false;
		}
	}

	async function saveTypeStatus(next: boolean) {
		if (!selectedType || !store || saving) return;
		const t = selectedType;
		const actionEpoch = epoch;
		saving = true;
		try {
			const cmd = await dashboardApi.adminCreateCommand(productTypeStatusCommand(store, t, next));
			if (actionEpoch !== epoch) return;
			say('ok', queued(cmd.id));
			void loadCommands();
		} catch (err) {
			if (actionEpoch !== epoch) return;
			say('err', apiErrorText(err, tb('فشل إنشاء الأمر.', 'Command failed.')));
		} finally {
			if (actionEpoch === epoch) saving = false;
		}
	}

	async function createType() {
		if (!store || saving) return;
		const code = (document.getElementById('nt-code') as HTMLInputElement)?.value.trim().toLowerCase() ?? '';
		const ar = (document.getElementById('nt-ar') as HTMLInputElement)?.value.trim() ?? '';
		const en = (document.getElementById('nt-en') as HTMLInputElement)?.value.trim() ?? '';
		if (!/^[a-z0-9_]{1,32}$/.test(code)) return say('err', tb('رمز غير صالح.', 'Invalid code.'));
		if (!ar || !en) return say('err', tb('التسميات مطلوبة.', 'Labels required.'));
		const actionEpoch = epoch;
		saving = true;
		try {
			const cmd = await dashboardApi.adminCreateCommand(productTypeCreateCommand(store, { code, name_ar: ar, name_en: en, position: productTypes.length }));
			if (actionEpoch !== epoch) return;
			say('ok', queued(cmd.id));
			void loadCommands();
			void loadTypes();
		} catch (err) {
			if (actionEpoch !== epoch) return;
			say('err', apiErrorText(err, tb('فشل إنشاء الأمر.', 'Command failed.')));
		} finally {
			if (actionEpoch === epoch) saving = false;
		}
	}

	async function assignProductType() {
		if (!selectedProduct || !store || saving) return;
		const p = selectedProduct;
		const typeId = picks['pt:' + p.product_id] ?? p.product_type_id ?? productTypes[0]?.type_id ?? '';
		if (!typeId) return say('err', tb('اختر النوع.', 'Select a type.'));
		const actionEpoch = epoch;
		saving = true;
		try {
			const cmd = await dashboardApi.adminCreateCommand(productTypeAssignmentCommand(store, p, typeId));
			if (actionEpoch !== epoch) return;
			say('ok', queued(cmd.id));
			void loadCommands();
		} catch (err) {
			if (actionEpoch !== epoch) return;
			say('err', apiErrorText(err, tb('فشل إنشاء الأمر.', 'Command failed.')));
		} finally {
			if (actionEpoch === epoch) saving = false;
		}
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
			say('err', apiErrorText(err, tb('فشل تحميل الأمر.', 'Load failed.')));
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
			say('ok', tb('تم إلغاء الأمر المعلق.', 'Pending command cancelled.'));
			void loadCommands();
		} catch (err) {
			if (actionEpoch !== epoch) return;
			say('err', apiErrorText(err, tb('فشل الإلغاء.', 'Cancel failed.')));
		} finally {
			if (actionEpoch === epoch) saving = false;
		}
	}

	function aggregateLabel(a: string): string {
		switch (a) {
			case 'pending': return tb('بانتظار الكاشير', 'Pending');
			case 'delivered': return tb('تم التسليم — بانتظار التطبيق', 'Delivered');
			case 'applied': return tb('مطبق في الكاشير — بانتظار المزامنة', 'Applied — syncing');
			case 'converged': return tb('مكتمل ومتزامن', 'Converged');
			case 'partial': return tb('جزئي — راجع الأجهزة', 'Partial');
			case 'conflict': return tb('تعارض — حدّث قبل إعادة المحاولة', 'Conflict');
			case 'blocked_capability': return tb('يتطلب تحديث الكاشير', 'Retail update required');
			case 'skipped_revoked': return tb('تم تخطي الجهاز الملغى أو المنقول', 'Revoked or rebound device skipped');
			case 'cancelled': return tb('ملغي', 'Cancelled');
			default: return a;
		}
	}

	// Phase 17-R0 (ADR-0049): Product has no SKU — the row shows the
	// variant rollup: "1 variant / SKU: X" when the single variant row is
	// loaded (variant rows carry the SKU), otherwise "N variants".
	function variantSummary(p: { variant_count: number; variants?: { sku: string }[] }): string {
		const rows = p.variants ?? [];
		if (p.variant_count === 1 && rows.length === 1) {
			return `1 variant / SKU: ${rows[0].sku}`;
		}
		return `${p.variant_count} variant${p.variant_count === 1 ? '' : 's'}`;
	}

	// Store race: a slow Store A response must never overwrite Store B.
	$effect(() => {
		const current = store;
		void current;
		epoch++;
		saving = false;
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
			void loadProducts(true);
			void loadCategories();
			void loadTags();
			void loadTypes();
			void loadCommands();
		});
	});
</script>

<div class="admin">
	{#if !store}
		<p class="muted">{tb('اختر المتجر أولًا من منتقي المتجر.', 'Select a Store first.')}</p>
	{:else}
		{#if notice}<p class="notice {notice.kind}" role="status">{notice.text}</p>{/if}
		<div class="tabs" role="tablist" aria-label="Catalog admin sections">
			{#each [['products', tb('المنتجات', 'Products')], ['types', tb('الأنواع', 'Types')], ['categories', tb('الفئات', 'Categories')], ['tags', tb('الوسوم', 'Tags')], ['commands', tb('الأوامر', 'Commands')]] as [id, label]}
				<button class="tab" type="button" role="tab" aria-selected={tab === id} class:active={tab === id} onclick={() => (tab = id as typeof tab)}>{label}</button>
			{/each}
		</div>

		{#if tab === 'products'}
			<div class="row">
				<input id="admin-search" type="search" placeholder={tb('SKU أو الاسم', 'SKU or name')} bind:value={search} aria-label="Search products" />
				<button class="btn-secondary btn-sm" type="button" onclick={() => void loadProducts(true)}>{tb('بحث', 'Search')}</button>
			</div>
			{#if productsState === 'loading'}<p class="muted">{tb('جارٍ التحميل…', 'Loading…')}</p>
			{:else if productsState === 'error'}<p class="muted">{arabic() ? 'فشل التحميل.' : 'Loading failed.'} <button class="btn-secondary btn-sm" type="button" onclick={() => void loadProducts(true)}>{tb('إعادة', 'Retry')}</button></p>
			{:else if productsState === 'empty'}<p class="muted">{tb('لا توجد منتجات.', 'No products.')}</p>
			{:else}
				<table class="data">
					<thead><tr><th>{tb('المتغيرات', 'Variants')}</th><th>{tb('الاسم', 'Name')}</th><th>{tb('أونلاين', 'Online')}</th><th>{tb('الحالة', 'State')}</th></tr></thead>
					<tbody>
						{#each products as p (p.product_id)}
							<tr>
								<td class="num" dir="ltr">{variantSummary(p)}</td>
								<td><button type="button" class="link" onclick={() => void openProduct(p.product_id)}>{p.name_ar}</button></td>
								<td>{p.sell_online ? tb('نعم', 'Yes') : tb('لا', 'No')}</td>
								<td>{p.has_pending ? tb('تغيير معلق', 'Pending change') : '—'}</td>
							</tr>
						{/each}
					</tbody>
				</table>
				{#if productCursor}<button class="btn-secondary btn-sm" type="button" onclick={() => void loadProducts(false)}>{tb('المزيد', 'More')}</button>{/if}
			{/if}
			{#if productState === 'loading'}
			<p role="status">{tb('جارٍ تحميل المنتج…', 'Loading Product…')}</p>
		{:else if productState === 'error'}
			<p role="alert">{tb('تعذر تحميل المنتج.', 'Product could not be loaded.')}</p>
		{/if}
		{#if selectedProduct}
				{@const p = selectedProduct}
				<section class="detail" aria-label="Product editor">
					<h3>{p.name_ar} <span class="muted num" dir="ltr">{variantSummary(p)}</span></h3>
					<p class="muted">{arabic() ? 'مراجعة الكتالوج' : 'Catalog revision'} <span class="num" dir="ltr">{p.catalog_revision}</span> · {arabic() ? 'سياسة البيع' : 'Sales policy'} <span class="num" dir="ltr">{p.sales_policy_revision}</span> · {arabic() ? 'التكوينات' : 'Configurations'} <span class="num" dir="ltr">{p.configuration_revision}</span> · {arabic() ? 'المخزون المشتق (قراءة فقط)' : 'Derived stock (read-only)'} <span class="num" dir="ltr">{p.derived_stock}</span></p>
					{#if p.variants && p.variants.length > 0}
						<h4>{tb('المتغيرات', 'Variants')}</h4>
						<table class="data">
							<thead><tr><th>SKU</th><th>{tb('الخيار', 'Option')}</th><th>{tb('المخزون', 'Stock')}</th><th>{tb('الحالة', 'State')}</th></tr></thead>
							<tbody>
								{#each p.variants as v (v.variant_id)}
									<tr>
										<td class="num" dir="ltr">{v.sku}</td>
										<td>{v.attributes.map((a) => `${a.definition_name_ar}: ${a.name_ar}`).join(' · ') || '—'}</td>
										<td class="num" dir="ltr">{v.stock_quantity}</td>
										<td>{v.deleted ? tb('محذوف', 'Deleted') : v.is_active ? tb('نشط', 'Active') : tb('معطّل', 'Inactive')}</td>
									</tr>
								{/each}
							</tbody>
						</table>
					{/if}
					{#if p.has_pending}<p class="notice ok" role="status">{tb('يوجد تغيير معلق — القيم الحالية أدناه هي المعروضة من السحابة، وليست نجاحًا بعد.', 'Pending change exists — values below are current projections, not success.')}</p>{/if}
					<h4>{tb('البيانات والأسعار', 'Details &amp; prices')}</h4>
					<div class="grid">
						<label class="field">{tb('الاسم العربي', 'Arabic name')} <input id="pd-ar" type="text" value={p.name_ar} /></label>
						<label class="field">{tb('الاسم الإنجليزي', 'English name')} <input id="pd-en" type="text" value={p.name_en ?? ''} dir="ltr" /></label>
						<label class="field">{tb('سعر الجنيه (وحدات صغرى)', 'EGP minor')} <input id="pd-egp" type="text" inputmode="numeric" value={p.egp_price_minor} dir="ltr" /></label>
						<label class="field">{tb('سعر الدولار (وحدات صغرى)', 'USD minor')} <input id="pd-usd" type="text" inputmode="numeric" value={p.usd_price_minor ?? '0'} dir="ltr" /></label>
						<label class="field">{tb('التكلفة (وحدات صغرى)', 'Cost minor')} <input id="pd-cost" type="text" inputmode="numeric" value={p.cost_minor} dir="ltr" /></label>
					</div>
					<button class="btn-secondary btn-sm" type="button" disabled={saving} onclick={() => void saveProductDetails()}>{tb('إدراج تعديل البيانات', 'Queue details change')}</button>
					<h4>{tb('النوع', 'Product type')}</h4>
					<p class="muted">{arabic() ? 'النوع الحالي:' : 'Current type:'} <span dir="ltr">{p.product_type_id?.slice(0, 8) ?? '—'}…</span> {tb('— التغيير يخضع لتحقق التوافق في الكاشير.', 'Assignment is compatibility-checked by Retail.')}</p>
					<div class="row">
						<SingleSelect
							id="pt-assign"
							label="Assign product type"
							hideLabel
							value={picks['pt:' + p.product_id] ?? p.product_type_id ?? productTypes[0]?.type_id ?? ''}
							options={productTypes.map((t) => ({ value: t.type_id, label: `${t.name_ar} (${t.code})` }))}
							onchange={(v) => (picks['pt:' + p.product_id] = v)}
						/>
						<button class="btn-secondary btn-sm" type="button" disabled={saving} onclick={() => void assignProductType()}>{tb('إدراج تغيير النوع', 'Queue type change')}</button>
					</div>
					<h4>{tb('التوفر أونلاين', 'Online availability')}</h4>
					<p class="muted">{arabic() ? 'القيمة الحالية:' : 'Current value:'} {p.sell_online ? tb('مفعّل', 'On') : tb('معطّل', 'Off')} — {arabic() ? 'التبديل لا يغيّر sell_offline.' : 'Switching does not change sell_offline.'}</p>
					<button class="btn-secondary btn-sm" type="button" disabled={saving} onclick={() => void saveProductOnline(!p.sell_online)}>{p.sell_online ? tb('تعطيل البيع أونلاين', 'Disable online') : tb('تفعيل البيع أونلاين', 'Enable online')}</button>
					<h4>{tb('التصنيف', 'Classification')}</h4>
					<p class="muted">{arabic() ? 'الفئة العليا الحالية:' : 'Current top category:'} <span dir="ltr">{p.top_category_id.slice(0, 8)}…</span> {tb('— يستخدم الكاشير نفس مدققات العمل.', 'Retail reuses canonical validators.')}</p>
					<SingleSelect
						id="pc-top"
						label={tb('الفئة العليا', 'Top category')}
						value={picks['top:' + p.product_id] ?? p.top_category_id}
						options={categories.map((c) => ({ value: c.category_id, label: c.name_ar }))}
						onchange={(v) => (picks['top:' + p.product_id] = v)}
					/>
					<fieldset>
						<legend>{tb('الفئات الفرعية', 'Subcategories')}</legend>
						{#each categories.filter((x) => x.category_id !== p.top_category_id) as c (c.category_id)}
							<label class="check"><input type="checkbox" class="pc-sub-check" value={c.category_id} checked={p.subcategory_ids.includes(c.category_id)} /> {c.name_ar}</label>
						{/each}
					</fieldset>
					<fieldset>
						<legend>{tb('الوسوم', 'Tags')}</legend>
						{#each tags as t (t.tag_id)}
							<label class="check"><input type="checkbox" class="pc-tag-check" value={t.tag_id} checked={p.tag_ids.includes(t.tag_id)} /> {t.name_ar}</label>
						{/each}
					</fieldset>
					<div><button class="btn-secondary btn-sm" type="button" disabled={saving} onclick={() => void saveProductClassification()}>{tb('إدراج تعديل التصنيف', 'Queue classification change')}</button></div>
					<h4>{tb('خيارات الإطار', 'Frame options')}</h4>
					<p class="muted">{tb('تُدار خيارات الإطار في تطبيق الكاشير وتظهر هنا للعرض فقط.', 'Frame choices are managed in the Retail app and shown here read-only.')}</p>
					{#if configurations.length === 0}<p class="muted">{tb('لا توجد خيارات.', 'No frame choices.')}</p>
					{:else}
						<ul data-testid="frame-configurations">
							{#each configurations as c (c.id)}
								<li>{c.style_name_ar} / {c.color_name_ar} <span class="num" dir="ltr">+{c.egp_delta_minor} EGP{c.usd_delta_minor !== null ? ` · +${c.usd_delta_minor} USD` : ''}</span>{c.enabled ? '' : tb('· معطّل', 'disabled')}</li>
							{/each}
						</ul>
					{/if}
				</section>
			{/if}
		{/if}

		{#if tab === 'types'}
			{#if typesState === 'loading'}<p class="muted">{tb('جارٍ التحميل…', 'Loading…')}</p>
			{:else if typesState === 'error'}<p class="muted">{arabic() ? 'فشل التحميل.' : 'Loading failed.'} <button class="btn-secondary btn-sm" type="button" onclick={() => void loadTypes()}>{tb('إعادة', 'Retry')}</button></p>
			{:else if typesState === 'empty'}<p class="muted">{tb('لا توجد أنواع.', 'No types.')}</p>
			{:else}
				<table class="data">
					<thead><tr><th>{tb('النوع', 'Type')}</th><th>{tb('الرمز', 'Code')}</th><th>{tb('نشط', 'Active')}</th><th>{tb('الحالة', 'State')}</th></tr></thead>
					<tbody>
						{#each productTypes as t (t.type_id)}
							<tr>
								<td><button type="button" class="link" onclick={() => (selectedType = t)}>{t.name_ar}</button></td>
								<td class="num" dir="ltr">{t.code}</td>
								<td>{t.is_active ? tb('نعم', 'Yes') : tb('لا', 'No')}</td>
								<td>{t.has_pending ? tb('تغيير معلق', 'Pending change') : '—'}</td>
							</tr>
						{/each}
					</tbody>
				</table>
			{/if}
			{#if selectedType}
				{@const t = selectedType}
				<section class="detail" aria-label="Type editor">
					<h3>{t.name_ar} <span class="muted num" dir="ltr">{t.code} · rev {t.type_revision}</span></h3>
					<p class="muted">{arabic() ? 'الأبعاد' : 'Dimensions'}: {t.dimensions.join(', ') || '—'} · {arabic() ? 'القدرات' : 'Capabilities'}: {t.capabilities.join(', ') || '—'}</p>
					<div class="grid">
						<label class="field">{tb('الاسم العربي', 'Arabic name')} <input id="tt-ar" type="text" value={t.name_ar} /></label>
						<label class="field">{tb('الاسم الإنجليزي', 'English name')} <input id="tt-en" type="text" value={t.name_en} dir="ltr" /></label>
					</div>
					<div class="row">
						<button class="btn-secondary btn-sm" type="button" disabled={saving} onclick={() => void saveTypeDetails()}>{tb('إدراج تعديل النوع', 'Queue type change')}</button>
						<button class="btn-secondary btn-sm" type="button" disabled={saving} onclick={() => void saveTypeStatus(!t.is_active)}>{t.is_active ? tb('تعطيل', 'Deactivate') : tb('تفعيل', 'Activate')}</button>
					</div>
				</section>
			{/if}
			<section class="detail" aria-label="Create type">
				<h3>{tb('نوع جديد', 'New type')}</h3>
				<div class="grid">
					<label class="field">{tb('الرمز', 'Code')} <input id="nt-code" type="text" dir="ltr" placeholder="book" /></label>
					<label class="field">{tb('الاسم العربي', 'Arabic name')} <input id="nt-ar" type="text" /></label>
					<label class="field">{tb('الاسم الإنجليزي', 'English name')} <input id="nt-en" type="text" dir="ltr" /></label>
				</div>
				<button class="btn-secondary btn-sm" type="button" disabled={saving} onclick={() => void createType()}>{tb('إدراج إنشاء النوع', 'Queue type creation')}</button>
			</section>
		{/if}

		{#if tab === 'categories'}
			{#if categoriesState === 'loading'}<p class="muted">{tb('جارٍ التحميل…', 'Loading…')}</p>
			{:else if categoriesState === 'error'}<p class="muted">{arabic() ? 'فشل التحميل.' : 'Loading failed.'} <button class="btn-secondary btn-sm" type="button" onclick={() => void loadCategories()}>{tb('إعادة', 'Retry')}</button></p>
			{:else if categoriesState === 'empty'}<p class="muted">{tb('لا توجد فئات.', 'No categories.')}</p>
			{:else}
				<table class="data">
					<thead><tr><th>{tb('الاسم', 'Name')}</th><th>{tb('الحالة', 'Status')}</th><th>{tb('أونلاين', 'Online')}</th><th>{tb('الحالة', 'State')}</th></tr></thead>
					<tbody>
						{#each categories as c (c.category_id)}
							<tr>
								<td><button type="button" class="link" onclick={() => (selectedCategory = c)}>{c.name_ar}</button></td>
								<td>{c.status}</td>
								<td>{c.online_enabled ? tb('نعم', 'Yes') : tb('لا', 'No')}</td>
								<td>{c.has_pending ? tb('تغيير معلق', 'Pending change') : '—'}</td>
							</tr>
						{/each}
					</tbody>
				</table>
			{/if}
			{#if selectedCategory}
				{@const c = selectedCategory}
				<section class="detail" aria-label="Category editor">
					<h3>{c.name_ar} <span class="muted num" dir="ltr">rev {c.catalog_revision}</span></h3>
					{#if c.has_pending}<p class="notice ok" role="status">{tb('يوجد تغيير معلق.', 'Pending change exists.')}</p>{/if}
					<div class="grid">
						<label class="field">{tb('الاسم العربي', 'Arabic name')} <input id="cd-ar" type="text" value={c.name_ar} /></label>
						<label class="field">{tb('الاسم الإنجليزي', 'English name')} <input id="cd-en" type="text" value={c.name_en ?? ''} dir="ltr" /></label>
						<SingleSelect
							id="cd-status"
							label={tb('الحالة', 'Status')}
							value={picks['cs:' + c.category_id] ?? c.status}
							options={[{ value: 'active', label: 'active' }, { value: 'hidden', label: 'hidden' }]}
							onchange={(v) => (picks['cs:' + c.category_id] = v)}
						/>
					</div>
					<button class="btn-secondary btn-sm" type="button" disabled={saving} onclick={() => void saveCategoryDetails()}>{tb('إدراج تعديل البيانات', 'Queue details change')}</button>
					<h4>{tb('التوفر أونلاين للفئة', 'Category online availability')}</h4>
					<p class="muted">{arabic() ? 'تعطيل الفئة يزيل المنتجات المتأثرة من القنوات الإلكترونية دون تغيير قيمة sell_online لكل منتج.' : 'Disabling the category removes affected products from online channels without changing each product’s sell_online.'}</p>
					<button class="btn-secondary btn-sm" type="button" disabled={saving} onclick={() => void saveCategoryOnline(!c.online_enabled)}>{c.online_enabled ? tb('تعطيل أونلاين', 'Disable online') : tb('تفعيل أونلاين', 'Enable online')}</button>
					<h4>{tb('الآباء', 'Parents')}</h4>
					<p class="muted">{tb('الكاشير يتحقق نهائيًا من عدم وجود دورات وحدود العمق.', 'Retail is the final DAG validator.')}</p>
					{#each categories.filter((x) => x.category_id !== c.category_id) as other (other.category_id)}
						<label class="check"><input type="checkbox" class="cat-parent-check" value={other.category_id} checked={c.parent_ids.includes(other.category_id)} /> {other.name_ar}</label>
					{/each}
					<div><button class="btn-secondary btn-sm" type="button" disabled={saving} onclick={() => void saveCategoryParents()}>{tb('إدراج تعديل الآباء', 'Queue parents change')}</button></div>
				</section>
			{/if}
		{/if}

		{#if tab === 'tags'}
			{#if tagsState === 'loading'}<p class="muted">{tb('جارٍ التحميل…', 'Loading…')}</p>
			{:else if tagsState === 'error'}<p class="muted">{arabic() ? 'فشل التحميل.' : 'Loading failed.'} <button class="btn-secondary btn-sm" type="button" onclick={() => void loadTags()}>{tb('إعادة', 'Retry')}</button></p>
			{:else if tagsState === 'empty'}<p class="muted">{tb('لا توجد وسوم.', 'No tags.')}</p>
			{:else}
				<table class="data">
					<thead><tr><th>{tb('الاسم', 'Name')}</th><th>slug</th><th>{tb('نشط', 'Active')}</th><th>{tb('الحالة', 'State')}</th></tr></thead>
					<tbody>
						{#each tags as t (t.tag_id)}
							<tr>
								<td><button type="button" class="link" onclick={() => (selectedTag = t)}>{t.name_ar}</button></td>
								<td class="num" dir="ltr">{t.slug}</td>
								<td>{t.is_active ? tb('نعم', 'Yes') : tb('لا', 'No')}</td>
								<td>{t.has_pending ? tb('تغيير معلق', 'Pending change') : '—'}</td>
							</tr>
						{/each}
					</tbody>
				</table>
			{/if}
			{#if selectedTag}
				{@const t = selectedTag}
				<section class="detail" aria-label="Tag editor">
					<h3>{t.name_ar} <span class="muted num" dir="ltr">rev {t.catalog_revision}</span></h3>
					<div class="grid">
						<label class="field">{tb('الاسم العربي', 'Arabic name')} <input id="td-ar" type="text" value={t.name_ar} /></label>
						<label class="field">{tb('الاسم الإنجليزي', 'English name')} <input id="td-en" type="text" value={t.name_en ?? ''} dir="ltr" /></label>
						<label class="check"><input id="td-active" type="checkbox" checked={t.is_active} /> {tb('نشط', 'Active')}</label>
					</div>
					<button class="btn-secondary btn-sm" type="button" disabled={saving} onclick={() => void saveTagDetails()}>{tb('إدراج تعديل الوسم', 'Queue tag change')}</button>
				</section>
			{/if}
		{/if}

		{#if tab === 'commands'}
			{#if commandsState === 'loading'}<p class="muted">{tb('جارٍ التحميل…', 'Loading…')}</p>
			{:else if commandsState === 'error'}<p class="muted">{arabic() ? 'فشل التحميل.' : 'Loading failed.'} <button class="btn-secondary btn-sm" type="button" onclick={() => void loadCommands()}>{tb('إعادة', 'Retry')}</button></p>
			{:else if commandsState === 'empty'}<p class="muted">{tb('لا توجد أوامر.', 'No commands.')}</p>
			{:else}
				<table class="data">
					<thead><tr><th>{tb('النوع', 'Type')}</th><th>{tb('الحالة', 'Aggregate')}</th><th>{tb('الفاعل', 'Actor')}</th></tr></thead>
					<tbody>
						{#each commands as cmd (cmd.id)}
							<tr>
								<td><button type="button" class="link" onclick={() => void openCommand(cmd.id)}><span dir="ltr">{cmd.type}</span></button></td>
								<td><span class="chip">{aggregateLabel(cmd.aggregate)}</span></td>
								<td>{cmd.actor}</td>
							</tr>
						{/each}
					</tbody>
				</table>
			{/if}
			{#if selectedCommand}
				{@const cmd = selectedCommand}
				<section class="detail" aria-label="Command detail">
					<h3 dir="ltr">{cmd.type}</h3>
					<p><span class="chip">{aggregateLabel(cmd.aggregate)}</span> <span class="muted num" dir="ltr">rev {cmd.expected_revision}</span></p>
					{#if cmd.target_kind === 'create'}
						<p class="muted">
							{#if cmd.result_entity_id}
								{tb('تم الإنشاء', 'Created')} — <span class="num" dir="ltr">{cmd.result_entity_id}</span>
							{:else}
								{#if arabic()}إنشاء “{cmd.requested_key}” — بانتظار الكاشير / {/if}Create “{cmd.requested_key}” — waiting for Retail
							{/if}
						</p>
					{/if}
					{#if cmd.targets}
						<table class="data">
							<thead><tr><th>{tb('الجهاز', 'Device')}</th><th>{tb('الحالة', 'Status')}</th><th>{tb('الكود', 'Code')}</th></tr></thead>
							<tbody>
								{#each cmd.targets as tgt (tgt.id)}
									<tr>
										<td class="num" dir="ltr">{tgt.device_name || tgt.device_id.slice(0, 8)}</td>
										<td>{aggregateLabel(tgt.status)}{#if tgt.capable === false} — {tb('يتطلب تحديث الكاشير', 'Retail update required')}{/if}</td>
										<td class="num" dir="ltr">{tgt.result_code ?? '—'}</td>
									</tr>
								{/each}
							</tbody>
						</table>
					{/if}
					{#if cmd.status === 'pending' && !cmd.targets?.some(t => t.status === 'delivered' || t.status === 'applied')}
						<button class="btn-secondary btn-sm" type="button" disabled={saving} onclick={() => void cancelCommand(cmd.id)}>{tb('إلغاء الأمر المعلق', 'Cancel pending command')}</button>
					{/if}
				</section>
			{/if}
		{/if}
	{/if}
</div>

<style>
	.admin { display: flex; flex-direction: column; gap: 0.75rem; }
	.tabs { display: flex; gap: 0.5rem; }
	.row { display: flex; gap: 0.5rem; align-items: center; }
	.grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 0.5rem; margin: 0.5rem 0; }
	.detail { border-top: 1px solid currentColor; padding-top: 0.5rem; }
	.notice.ok { color: inherit; font-weight: bold; }
	.notice.err { color: inherit; font-weight: bold; }
	.chip { border: 1px solid currentColor; border-radius: 0.4rem; padding: 0.1rem 0.5rem; }
	.link { background: none; border: none; padding: 0; cursor: pointer; text-decoration: underline; color: inherit; font: inherit; }
	.check { display: inline-flex; gap: 0.3rem; align-items: center; margin-inline-end: 0.8rem; }
	.muted { opacity: 0.75; }
	.num { font-variant-numeric: tabular-nums; }
	table.data { width: 100%; border-collapse: collapse; }
	table.data th, table.data td { text-align: start; padding: 0.3rem 0.5rem; }
</style>
