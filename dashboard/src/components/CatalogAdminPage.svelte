<script lang="ts">
	import { untrack } from 'svelte';
	import { dashboardApi, ApiError } from '../lib/api.js';
	import type { AdminProductRow, AdminProductDetail, AdminCategoryRow, AdminTagRow, AdminCommand } from '../lib/api.js';

	// CatalogAdminPage: the Cloud command/control surface for catalog
	// state. It edits nothing directly: every Save persists a typed,
	// revision-checked command (Change queued), Retail applies it, and
	// normal sync projects it back. Current values stay visible until
	// the projection converges; pending badges mark in-flight work.
	let { store }: { store: string } = $props();

	type Tab = 'products' | 'categories' | 'tags' | 'commands';
	let tab: Tab = $state('products');

	let epoch = 0;
	const requests: Record<string, number> = {};
	let notice = $state<{ kind: 'ok' | 'err'; text: string } | null>(null);
	function say(kind: 'ok' | 'err', text: string) {
		notice = { kind, text };
	}

	function apiErrorText(err: unknown, fallback: string): string {
		if (err instanceof ApiError) {
			switch (err.code) {
				case 'REVISION_CONFLICT':
					return 'تعارض مراجعة — تغيّر العنصر في الكاشير بعد تحميل الصفحة. حدّث قبل إعادة المحاولة. / Revision conflict — refresh before retrying.';
				case 'STORE_SCOPE_CONFLICT':
					return 'نطاق متجر غير صالح. / Invalid Store scope.';
				case 'ENTITY_NOT_FOUND':
					return 'العنصر غير موجود. / Entity not found.';
				case 'VALIDATION_FAILED':
					return 'تحقق غير صالح — راجع القيم. / Invalid values.';
				case 'UNSUPPORTED_COMMAND':
					return 'أمر غير مدعوم. / Unsupported command.';
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
				configKey = 0;
				configRows = cfgs.configurations.map((c) => ({
					key: ++configKey,
					id: c.id,
					style_code: c.style_code,
					style_ar: c.style_name_ar,
					color_code: c.color_code,
					color_ar: c.color_name_ar,
					egp: c.egp_delta_minor,
					usd: c.usd_delta_minor ?? '',
					enabled: c.enabled
				}));
			} catch {
				if (my !== epoch || request !== requests.openProduct) return;
				configRows = [];
			}
			productState = 'loaded';
		} catch {
			if (my !== epoch || request !== requests.openProduct) return;
			productState = 'error';
		}
	}

	function productPayloadBase(extra: Record<string, unknown>, revKey: string, rev: number) {
		return { ...extra, [revKey]: rev };
	}

	async function saveProductDetails() {
		if (!selectedProduct || !store || saving) return;
		const p = selectedProduct;
		const ar = (document.getElementById('pd-ar') as HTMLInputElement)?.value.trim() ?? '';
		const en = (document.getElementById('pd-en') as HTMLInputElement)?.value.trim() ?? '';
		const egp = (document.getElementById('pd-egp') as HTMLInputElement)?.value.trim() ?? '';
		const usd = (document.getElementById('pd-usd') as HTMLInputElement)?.value.trim() ?? '';
		const cost = (document.getElementById('pd-cost') as HTMLInputElement)?.value.trim() ?? '';
		if (!ar) return say('err', 'الاسم العربي مطلوب. / Arabic name required.');
		for (const [label, v] of [['EGP', egp], ['USD', usd], ['cost', cost]] as const) {
			if (!/^\d+$/.test(v)) return say('err', `قيمة ${label} يجب أن تكون أرقامًا صحيحة (وحدات صغرى). / ${label} must be integer minor units.`);
		}
		const actionEpoch = epoch;
		saving = true;
		try {
			const cmd = await dashboardApi.adminCreateCommand({
				store_id: store,
				type: 'catalog.product.details.update.v1',
				entity_id: p.product_id,
				expected_revision: p.catalog_revision,
				payload: productPayloadBase({
					product_id: p.product_id,
					arabic_name: ar,
					arabic_description: p.description_ar,
					english_name: en,
					english_description: p.description_en,
					width_cm: p.width_cm ?? 1,
					height_cm: p.height_cm ?? 1,
					egp_price_cents: egp,
					usd_price_cents: usd,
					cost_cents: cost,
					top_category_id: p.top_category_id,
					subcategory_ids: p.subcategory_ids,
					tag_ids: p.tag_ids
				}, 'expected_catalog_revision', p.catalog_revision)
			});
			if (actionEpoch !== epoch) return;
			say('ok', `تم إدراج التغيير في قائمة الانتظار — بانتظار الكاشير. الأمر ${cmd.id.slice(0, 8)}… / Change queued — waiting for Retail.`);
			void loadCommands();
		} catch (err) {
			if (actionEpoch !== epoch) return;
			say('err', apiErrorText(err, 'فشل إنشاء الأمر. / Command failed.'));
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
			const cmd = await dashboardApi.adminCreateCommand({
				store_id: store,
				type: 'catalog.product.online-policy.update.v1',
				entity_id: p.product_id,
				expected_revision: p.sales_policy_revision,
				payload: { product_id: p.product_id, sell_online: next, expected_sales_policy_revision: p.sales_policy_revision }
			});
			if (actionEpoch !== epoch) return;
			say('ok', `تم إدراج التغيير في قائمة الانتظار — بانتظار الكاشير. الأمر ${cmd.id.slice(0, 8)}… / Change queued — waiting for Retail.`);
			void loadCommands();
		} catch (err) {
			if (actionEpoch !== epoch) return;
			say('err', apiErrorText(err, 'فشل إنشاء الأمر. / Command failed.'));
		} finally {
			if (actionEpoch === epoch) saving = false;
		}
	}

	async function saveProductClassification() {
		if (!selectedProduct || !store || saving) return;
		const p = selectedProduct;
		const top = (document.getElementById('pc-top') as HTMLSelectElement)?.value ?? p.top_category_id;
		const subs = Array.from(document.querySelectorAll<HTMLInputElement>('.pc-sub-check:checked')).map((el) => el.value);
		const tagIds = Array.from(document.querySelectorAll<HTMLInputElement>('.pc-tag-check:checked')).map((el) => el.value);
		const actionEpoch = epoch;
		saving = true;
		try {
			const cmd = await dashboardApi.adminCreateCommand({
				store_id: store,
				type: 'catalog.product.classification.update.v1',
				entity_id: p.product_id,
				expected_revision: p.catalog_revision,
				payload: { product_id: p.product_id, top_category_id: top, subcategory_ids: subs, tag_ids: tagIds, expected_catalog_revision: p.catalog_revision }
			});
			if (actionEpoch !== epoch) return;
			say('ok', `تم إدراج التغيير في قائمة الانتظار — بانتظار الكاشير. الأمر ${cmd.id.slice(0, 8)}… / Change queued — waiting for Retail.`);
			void loadCommands();
		} catch (err) {
			if (actionEpoch !== epoch) return;
			say('err', apiErrorText(err, 'فشل إنشاء الأمر. / Command failed.'));
		} finally {
			if (actionEpoch === epoch) saving = false;
		}
	}

	interface ConfigRowState {
		key: number;
		id?: string;
		style_code: string;
		style_ar: string;
		color_code: string;
		color_ar: string;
		egp: string;
		usd: string;
		enabled: boolean;
	}
	let configRows: ConfigRowState[] = $state([]);
	let configKey = 0;

	function addConfigRow() {
		configKey++;
		configRows.push({ key: configKey, style_code: '', style_ar: '', color_code: '', color_ar: '', egp: '0', usd: '', enabled: true });
	}

	function removeConfigRow(key: number) {
		configRows = configRows.filter((r) => r.key !== key);
	}

	async function saveConfigurations() {
		if (!selectedProduct || !store || saving) return;
		const p = selectedProduct;
		if (configRows.length > 100) return say('err', 'الحد الأقصى 100 خيار. / At most 100 options.');
		const entries: Record<string, unknown>[] = [];
		for (const r of configRows) {
			if (!r.style_code.trim() || !r.style_ar.trim() || !r.color_code.trim() || !r.color_ar.trim()) {
				return say('err', 'أكمل الأكواد والأسماء لكل خيار. / Complete codes and names for every option.');
			}
			if (!/^\d+$/.test(r.egp.trim())) return say('err', 'EGP يجب أن يكون أرقامًا صحيحة. / EGP must be integer minor units.');
			const usdRaw = r.usd.trim();
			let usd: string | null = null;
			if (usdRaw !== '') {
				if (!/^\d+$/.test(usdRaw)) return say('err', 'USD يجب أن يكون أرقامًا أو فارغًا. / USD must be digits or blank.');
				usd = usdRaw;
			}
			entries.push({
				...(r.id ? { id: r.id } : {}),
				style_code: r.style_code.trim(),
				style_name_ar: r.style_ar.trim(),
				color_code: r.color_code.trim(),
				color_name_ar: r.color_ar.trim(),
				egp_delta_cents: r.egp.trim(),
				usd_delta_cents: usd,
				enabled: r.enabled
			});
		}
		const actionEpoch = epoch;
		saving = true;
		try {
			const cmd = await dashboardApi.adminCreateCommand({
				store_id: store,
				type: 'catalog.product.configurations.update.v1',
				entity_id: p.product_id,
				expected_revision: p.configuration_revision,
				payload: { product_id: p.product_id, configurations: entries, expected_configuration_revision: p.configuration_revision }
			});
			if (actionEpoch !== epoch) return;
			say('ok', `تم إدراج التغيير في قائمة الانتظار — بانتظار الكاشير. الأمر ${cmd.id.slice(0, 8)}… / Change queued — waiting for Retail.`);
			void loadCommands();
		} catch (err) {
			if (actionEpoch !== epoch) return;
			say('err', apiErrorText(err, 'فشل إنشاء الأمر. / Command failed.'));
		} finally {
			if (actionEpoch === epoch) saving = false;
		}
	}

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
			const ok = confirm('تعطيل هذه الفئة يزيل المنتجات المتأثرة من القنوات الإلكترونية دون تغيير قيمة sell_online لكل منتج. متابعة؟ / Disabling removes affected Products from online channels without changing each Product\u2019s own sell_online. Continue?');
			if (!ok) return;
		}
		const c = selectedCategory;
		const actionEpoch = epoch;
		saving = true;
		try {
			const cmd = await dashboardApi.adminCreateCommand({
				store_id: store,
				type: 'catalog.category.online-policy.update.v1',
				entity_id: c.category_id,
				expected_revision: c.catalog_revision,
				payload: { category_id: c.category_id, online_enabled: next, expected_catalog_revision: c.catalog_revision }
			});
			if (actionEpoch !== epoch) return;
			say('ok', `تم إدراج التغيير في قائمة الانتظار — بانتظار الكاشير. الأمر ${cmd.id.slice(0, 8)}… / Change queued — waiting for Retail.`);
			void loadCommands();
		} catch (err) {
			if (actionEpoch !== epoch) return;
			say('err', apiErrorText(err, 'فشل إنشاء الأمر. / Command failed.'));
		} finally {
			if (actionEpoch === epoch) saving = false;
		}
	}

	async function saveCategoryDetails() {
		if (!selectedCategory || !store || saving) return;
		const c = selectedCategory;
		const ar = (document.getElementById('cd-ar') as HTMLInputElement)?.value.trim() ?? '';
		const en = (document.getElementById('cd-en') as HTMLInputElement)?.value.trim() ?? '';
		const status = (document.getElementById('cd-status') as HTMLSelectElement)?.value ?? c.status;
		if (!ar) return say('err', 'الاسم العربي مطلوب. / Arabic name required.');
		const actionEpoch = epoch;
		saving = true;
		try {
			const cmd = await dashboardApi.adminCreateCommand({
				store_id: store,
				type: 'catalog.category.details.update.v1',
				entity_id: c.category_id,
				expected_revision: c.catalog_revision,
				payload: { category_id: c.category_id, name_ar: ar, name_en: en, status, expected_catalog_revision: c.catalog_revision }
			});
			if (actionEpoch !== epoch) return;
			say('ok', `تم إدراج التغيير في قائمة الانتظار — بانتظار الكاشير. الأمر ${cmd.id.slice(0, 8)}… / Change queued — waiting for Retail.`);
			void loadCommands();
		} catch (err) {
			if (actionEpoch !== epoch) return;
			say('err', apiErrorText(err, 'فشل إنشاء الأمر. / Command failed.'));
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
			const cmd = await dashboardApi.adminCreateCommand({
				store_id: store,
				type: 'catalog.category.parents.update.v1',
				entity_id: c.category_id,
				expected_revision: c.catalog_revision,
				payload: { category_id: c.category_id, parent_ids: checked, expected_catalog_revision: c.catalog_revision }
			});
			if (actionEpoch !== epoch) return;
			say('ok', `تم إدراج التغيير في قائمة الانتظار — يتحقق الكاشير من عدم وجود دورات. الأمر ${cmd.id.slice(0, 8)}… / Change queued — Retail validates the graph.`);
			void loadCommands();
		} catch (err) {
			if (actionEpoch !== epoch) return;
			say('err', apiErrorText(err, 'فشل إنشاء الأمر. / Command failed.'));
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
		if (!ar) return say('err', 'الاسم العربي مطلوب. / Arabic name required.');
		const actionEpoch = epoch;
		saving = true;
		try {
			const cmd = await dashboardApi.adminCreateCommand({
				store_id: store,
				type: 'catalog.tag.details.update.v1',
				entity_id: t.tag_id,
				expected_revision: t.catalog_revision,
				payload: { tag_id: t.tag_id, name_ar: ar, name_en: en, is_active: active, expected_catalog_revision: t.catalog_revision }
			});
			if (actionEpoch !== epoch) return;
			say('ok', `تم إدراج التغيير في قائمة الانتظار — بانتظار الكاشير. الأمر ${cmd.id.slice(0, 8)}… / Change queued — waiting for Retail.`);
			void loadCommands();
		} catch (err) {
			if (actionEpoch !== epoch) return;
			say('err', apiErrorText(err, 'فشل إنشاء الأمر. / Command failed.'));
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
			say('err', apiErrorText(err, 'فشل تحميل الأمر. / Load failed.'));
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
			say('ok', 'تم إلغاء الأمر المعلق. / Pending command cancelled.');
			void loadCommands();
		} catch (err) {
			if (actionEpoch !== epoch) return;
			say('err', apiErrorText(err, 'فشل الإلغاء. / Cancel failed.'));
		} finally {
			if (actionEpoch === epoch) saving = false;
		}
	}

	function aggregateLabel(a: string): string {
		switch (a) {
			case 'PENDING': return 'بانتظار الكاشير / Pending';
			case 'DELIVERED': return 'تم التسليم — بانتظار التطبيق / Delivered';
			case 'APPLIED': return 'مطبق في الكاشير — بانتظار المزامنة / Applied — syncing';
			case 'CONVERGED': return 'مكتمل ومتزامن / Converged';
			case 'PARTIAL': return 'جزئي — راجع الأجهزة / Partial';
			case 'CONFLICT': return 'تعارض — حدّث قبل إعادة المحاولة / Conflict';
			case 'BLOCKED_CAPABILITY': return 'يتطلب تحديث الكاشير / Retail update required';
			case 'SKIPPED_REVOKED': return 'تم تخطي الجهاز الملغى أو المنقول / Revoked or rebound device skipped';
			case 'CANCELLED': return 'ملغي / Cancelled';
			default: return a;
		}
	}

	// Store race: a slow Store A response must never overwrite Store B.
	$effect(() => {
		const current = store;
		void current;
		epoch++;
		saving = false;
		productState = 'idle';
		configRows = [];
		products = [];
		productCursor = null;
		selectedProduct = null;
		categories = [];
		selectedCategory = null;
		tags = [];
		selectedTag = null;
		commands = [];
		selectedCommand = null;
		notice = null;
		if (!store) return;
		untrack(() => {
			void loadProducts(true);
			void loadCategories();
			void loadTags();
			void loadCommands();
		});
	});
</script>

<div class="admin">
	{#if !store}
		<p class="muted">اختر المتجر أولًا من منتقي المتجر. / Select a Store first.</p>
	{:else}
		{#if notice}<p class="notice {notice.kind}" role="status">{notice.text}</p>{/if}
		<div class="tabs" role="tablist" aria-label="Catalog admin sections">
			{#each [['products', 'المنتجات / Products'], ['categories', 'الفئات / Categories'], ['tags', 'الوسوم / Tags'], ['commands', 'الأوامر / Commands']] as [id, label]}
				<button type="button" role="tab" aria-selected={tab === id} class:active={tab === id} onclick={() => (tab = id as typeof tab)}>{label}</button>
			{/each}
		</div>

		{#if tab === 'products'}
			<div class="row">
				<input id="admin-search" type="search" placeholder="SKU أو الاسم / SKU or name" bind:value={search} aria-label="Search products" />
				<button type="button" onclick={() => void loadProducts(true)}>بحث / Search</button>
			</div>
			{#if productsState === 'loading'}<p class="muted">جارٍ التحميل… / Loading…</p>
			{:else if productsState === 'error'}<p class="muted">فشل التحميل. <button type="button" onclick={() => void loadProducts(true)}>إعادة / Retry</button></p>
			{:else if productsState === 'empty'}<p class="muted">لا توجد منتجات. / No products.</p>
			{:else}
				<table class="data">
					<thead><tr><th>SKU</th><th>الاسم / Name</th><th>أونلاين / Online</th><th>الحالة / State</th></tr></thead>
					<tbody>
						{#each products as p (p.product_id)}
							<tr>
								<td class="num" dir="ltr">{p.sku}</td>
								<td><button type="button" class="link" onclick={() => void openProduct(p.product_id)}>{p.name_ar}</button></td>
								<td>{p.sell_online ? 'نعم / Yes' : 'لا / No'}</td>
								<td>{p.has_pending ? 'تغيير معلق / Pending change' : '—'}</td>
							</tr>
						{/each}
					</tbody>
				</table>
				{#if productCursor}<button type="button" onclick={() => void loadProducts(false)}>المزيد / More</button>{/if}
			{/if}
			{#if productState === 'loading'}
			<p role="status">جارٍ تحميل المنتج… / Loading Product…</p>
		{:else if productState === 'error'}
			<p role="alert">تعذر تحميل المنتج. / Product could not be loaded.</p>
		{/if}
		{#if selectedProduct}
				{@const p = selectedProduct}
				<section class="detail" aria-label="Product editor">
					<h3>{p.name_ar} <span class="muted num" dir="ltr">{p.sku}</span></h3>
					<p class="muted">مراجعة الكتالوج <span class="num" dir="ltr">{p.catalog_revision}</span> · سياسة البيع <span class="num" dir="ltr">{p.sales_policy_revision}</span> · التكوينات <span class="num" dir="ltr">{p.configuration_revision}</span> · المخزون (قراءة فقط) <span class="num" dir="ltr">{p.stock_quantity}</span></p>
					{#if p.has_pending}<p class="notice ok" role="status">يوجد تغيير معلق — القيم الحالية أدناه هي المعروضة من السحابة، وليست نجاحًا بعد. / Pending change exists — values below are current projections, not success.</p>{/if}
					<h4>البيانات والأسعار / Details &amp; prices</h4>
					<div class="grid">
						<label>الاسم العربي / Arabic name <input id="pd-ar" type="text" value={p.name_ar} /></label>
						<label>الاسم الإنجليزي / English name <input id="pd-en" type="text" value={p.name_en ?? ''} dir="ltr" /></label>
						<label>سعر الجنيه (وحدات صغرى) / EGP minor <input id="pd-egp" type="text" inputmode="numeric" value={p.egp_price_minor} dir="ltr" /></label>
						<label>سعر الدولار (وحدات صغرى) / USD minor <input id="pd-usd" type="text" inputmode="numeric" value={p.usd_price_minor ?? '0'} dir="ltr" /></label>
						<label>التكلفة (وحدات صغرى) / Cost minor <input id="pd-cost" type="text" inputmode="numeric" value={p.cost_minor} dir="ltr" /></label>
					</div>
					<button type="button" disabled={saving} onclick={() => void saveProductDetails()}>إدراج تعديل البيانات / Queue details change</button>
					<h4>التوفر أونلاين / Online availability</h4>
					<p class="muted">القيمة الحالية: {p.sell_online ? 'مفعّل / On' : 'معطّل / Off'} — التبديل لا يغيّر sell_offline.</p>
					<button type="button" disabled={saving} onclick={() => void saveProductOnline(!p.sell_online)}>{p.sell_online ? 'تعطيل البيع أونلاين / Disable online' : 'تفعيل البيع أونلاين / Enable online'}</button>
					<h4>التصنيف / Classification</h4>
					<p class="muted">الفئة العليا الحالية: <span dir="ltr">{p.top_category_id.slice(0, 8)}…</span> — يستخدم الكاشير نفس مدققات العمل. / Retail reuses canonical validators.</p>
					<label>الفئة العليا / Top category
						<select id="pc-top">
							{#each categories as c (c.category_id)}
								<option value={c.category_id} selected={c.category_id === p.top_category_id}>{c.name_ar}</option>
							{/each}
						</select>
					</label>
					<fieldset>
						<legend>الفئات الفرعية / Subcategories</legend>
						{#each categories.filter((x) => x.category_id !== p.top_category_id) as c (c.category_id)}
							<label class="check"><input type="checkbox" class="pc-sub-check" value={c.category_id} checked={p.subcategory_ids.includes(c.category_id)} /> {c.name_ar}</label>
						{/each}
					</fieldset>
					<fieldset>
						<legend>الوسوم / Tags</legend>
						{#each tags as t (t.tag_id)}
							<label class="check"><input type="checkbox" class="pc-tag-check" value={t.tag_id} checked={p.tag_ids.includes(t.tag_id)} /> {t.name_ar}</label>
						{/each}
					</fieldset>
					<div><button type="button" disabled={saving} onclick={() => void saveProductClassification()}>إدراج تعديل التصنيف / Queue classification change</button></div>
					<h4>خيارات الإطار / Frame options</h4>
					<p class="muted">القيم بالوحدات الصغرى كنصوص دقيقة — فارغ USD يعني غير متاح (NULL)، وصفر يعني صفرًا صريحًا. / Minor-unit strings; blank USD is NULL, zero is explicit zero.</p>
					{#each configRows as r (r.key)}
						<div class="grid cfgrow">
							<label>Style code <input type="text" bind:value={r.style_code} dir="ltr" /></label>
							<label>اسم الستايل / Style AR <input type="text" bind:value={r.style_ar} /></label>
							<label>Color code <input type="text" bind:value={r.color_code} dir="ltr" /></label>
							<label>اسم اللون / Color AR <input type="text" bind:value={r.color_ar} /></label>
							<label>EGP delta minor <input type="text" inputmode="numeric" bind:value={r.egp} dir="ltr" /></label>
							<label>USD delta (فارغ=NULL / blank=NULL) <input type="text" inputmode="numeric" bind:value={r.usd} dir="ltr" placeholder="" /></label>
							<label class="check"><input type="checkbox" bind:checked={r.enabled} /> مفعّل / Enabled</label>
							<button type="button" onclick={() => removeConfigRow(r.key)} aria-label="Remove option">✕</button>
						</div>
					{/each}
					<div class="row">
						<button type="button" onclick={addConfigRow}>+ إضافة خيار / Add option</button>
						<button type="button" disabled={saving} onclick={() => void saveConfigurations()}>إدراج تعديل الخيارات / Queue options change</button>
					</div>
				</section>
			{/if}
		{/if}

		{#if tab === 'categories'}
			{#if categoriesState === 'loading'}<p class="muted">جارٍ التحميل… / Loading…</p>
			{:else if categoriesState === 'error'}<p class="muted">فشل التحميل. <button type="button" onclick={() => void loadCategories()}>إعادة / Retry</button></p>
			{:else if categoriesState === 'empty'}<p class="muted">لا توجد فئات. / No categories.</p>
			{:else}
				<table class="data">
					<thead><tr><th>الاسم / Name</th><th>الحالة / Status</th><th>أونلاين / Online</th><th>الحالة / State</th></tr></thead>
					<tbody>
						{#each categories as c (c.category_id)}
							<tr>
								<td><button type="button" class="link" onclick={() => (selectedCategory = c)}>{c.name_ar}</button></td>
								<td>{c.status}</td>
								<td>{c.online_enabled ? 'نعم / Yes' : 'لا / No'}</td>
								<td>{c.has_pending ? 'تغيير معلق / Pending change' : '—'}</td>
							</tr>
						{/each}
					</tbody>
				</table>
			{/if}
			{#if selectedCategory}
				{@const c = selectedCategory}
				<section class="detail" aria-label="Category editor">
					<h3>{c.name_ar} <span class="muted num" dir="ltr">rev {c.catalog_revision}</span></h3>
					{#if c.has_pending}<p class="notice ok" role="status">يوجد تغيير معلق. / Pending change exists.</p>{/if}
					<div class="grid">
						<label>الاسم العربي / Arabic name <input id="cd-ar" type="text" value={c.name_ar} /></label>
						<label>الاسم الإنجليزي / English name <input id="cd-en" type="text" value={c.name_en ?? ''} dir="ltr" /></label>
						<label>الحالة / Status
							<select id="cd-status" value={c.status}>
								<option value="active">active</option>
								<option value="hidden">hidden</option>
							</select>
						</label>
					</div>
					<button type="button" disabled={saving} onclick={() => void saveCategoryDetails()}>إدراج تعديل البيانات / Queue details change</button>
					<h4>التوفر أونلاين للفئة / Category online availability</h4>
					<p class="muted">تعطيل الفئة يزيل المنتجات المتأثرة من القنوات الإلكترونية دون تغيير قيمة sell_online لكل منتج.</p>
					<button type="button" disabled={saving} onclick={() => void saveCategoryOnline(!c.online_enabled)}>{c.online_enabled ? 'تعطيل أونلاين / Disable online' : 'تفعيل أونلاين / Enable online'}</button>
					<h4>الآباء / Parents</h4>
					<p class="muted">الكاشير يتحقق نهائيًا من عدم وجود دورات وحدود العمق. / Retail is the final DAG validator.</p>
					{#each categories.filter((x) => x.category_id !== c.category_id) as other (other.category_id)}
						<label class="check"><input type="checkbox" class="cat-parent-check" value={other.category_id} checked={c.parent_ids.includes(other.category_id)} /> {other.name_ar}</label>
					{/each}
					<div><button type="button" disabled={saving} onclick={() => void saveCategoryParents()}>إدراج تعديل الآباء / Queue parents change</button></div>
				</section>
			{/if}
		{/if}

		{#if tab === 'tags'}
			{#if tagsState === 'loading'}<p class="muted">جارٍ التحميل… / Loading…</p>
			{:else if tagsState === 'error'}<p class="muted">فشل التحميل. <button type="button" onclick={() => void loadTags()}>إعادة / Retry</button></p>
			{:else if tagsState === 'empty'}<p class="muted">لا توجد وسوم. / No tags.</p>
			{:else}
				<table class="data">
					<thead><tr><th>الاسم / Name</th><th>slug</th><th>نشط / Active</th><th>الحالة / State</th></tr></thead>
					<tbody>
						{#each tags as t (t.tag_id)}
							<tr>
								<td><button type="button" class="link" onclick={() => (selectedTag = t)}>{t.name_ar}</button></td>
								<td class="num" dir="ltr">{t.slug}</td>
								<td>{t.is_active ? 'نعم / Yes' : 'لا / No'}</td>
								<td>{t.has_pending ? 'تغيير معلق / Pending change' : '—'}</td>
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
						<label>الاسم العربي / Arabic name <input id="td-ar" type="text" value={t.name_ar} /></label>
						<label>الاسم الإنجليزي / English name <input id="td-en" type="text" value={t.name_en ?? ''} dir="ltr" /></label>
						<label class="check"><input id="td-active" type="checkbox" checked={t.is_active} /> نشط / Active</label>
					</div>
					<button type="button" disabled={saving} onclick={() => void saveTagDetails()}>إدراج تعديل الوسم / Queue tag change</button>
				</section>
			{/if}
		{/if}

		{#if tab === 'commands'}
			{#if commandsState === 'loading'}<p class="muted">جارٍ التحميل… / Loading…</p>
			{:else if commandsState === 'error'}<p class="muted">فشل التحميل. <button type="button" onclick={() => void loadCommands()}>إعادة / Retry</button></p>
			{:else if commandsState === 'empty'}<p class="muted">لا توجد أوامر. / No commands.</p>
			{:else}
				<table class="data">
					<thead><tr><th>النوع / Type</th><th>الحالة / Aggregate</th><th>الفاعل / Actor</th></tr></thead>
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
					{#if cmd.targets}
						<table class="data">
							<thead><tr><th>الجهاز / Device</th><th>الحالة / Status</th><th>الكود / Code</th></tr></thead>
							<tbody>
								{#each cmd.targets as tgt (tgt.id)}
									<tr>
										<td class="num" dir="ltr">{tgt.device_name || tgt.device_id.slice(0, 8)}</td>
										<td>{aggregateLabel(tgt.status)}{#if tgt.capable === false} — يتطلب تحديث الكاشير / Retail update required{/if}</td>
										<td class="num" dir="ltr">{tgt.result_code ?? '—'}</td>
									</tr>
								{/each}
							</tbody>
						</table>
					{/if}
					{#if cmd.status === 'PENDING' && !cmd.targets?.some(t => t.status === 'DELIVERED' || t.status === 'APPLIED')}
						<button type="button" disabled={saving} onclick={() => void cancelCommand(cmd.id)}>إلغاء الأمر المعلق / Cancel pending command</button>
					{/if}
				</section>
			{/if}
		{/if}
	{/if}
</div>

<style>
	.admin { display: flex; flex-direction: column; gap: 0.75rem; }
	.tabs { display: flex; gap: 0.5rem; }
	.tabs button { padding: 0.4rem 0.8rem; }
	.tabs button.active { font-weight: bold; text-decoration: underline; }
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
