import type { AdminCategoryRow, AdminProductDetail, AdminTagRow, AdminProductType } from './apiTypes.js';

// These local builders preserve the wire intent. Retail still validates and
// applies business rules; command hashing and delivery remain in the API.
export function productClassificationCommand(
 store: string,
 product: Pick<AdminProductDetail, 'product_id' | 'catalog_revision'>,
 classification: { top_category_id: string; subcategory_ids: string[]; tag_ids: string[] },
) {
 return {
  store_id: store,
  type: 'catalog.product.classification.update.v1' as const,
  entity_id: product.product_id,
  expected_revision: product.catalog_revision,
  payload: {
   product_id: product.product_id,
   top_category_id: classification.top_category_id,
   subcategory_ids: classification.subcategory_ids,
   tag_ids: classification.tag_ids,
   expected_catalog_revision: product.catalog_revision,
  },
 };
}

export function productOnlinePolicyCommand(
 store: string,
 product: Pick<AdminProductDetail, 'product_id' | 'sales_policy_revision'>,
 online: boolean,
) {
 return {
  store_id: store,
  type: 'catalog.product.online-policy.update.v1' as const,
  entity_id: product.product_id,
  expected_revision: product.sales_policy_revision,
  payload: { product_id: product.product_id, sell_online: online, expected_sales_policy_revision: product.sales_policy_revision },
 };
}

export function categoryOnlinePolicyCommand(
 store: string,
 category: Pick<AdminCategoryRow, 'category_id' | 'catalog_revision'>,
 online: boolean,
) {
 return {
  store_id: store,
  type: 'catalog.category.online-policy.update.v1' as const,
  entity_id: category.category_id,
  expected_revision: category.catalog_revision,
  payload: { category_id: category.category_id, online_enabled: online, expected_catalog_revision: category.catalog_revision },
 };
}


export function productDetailsCommand(
 store: string,
 product: Pick<AdminProductDetail, 'product_id' | 'catalog_revision' | 'description_ar' | 'description_en' | 'width_cm' | 'height_cm' | 'top_category_id' | 'subcategory_ids' | 'tag_ids'>,
 details: { arabic_name: string; english_name: string; egp_price_cents: string; usd_price_cents: string; cost_cents: string },
) {
 return {
  store_id: store, type: 'catalog.product.details.update.v1' as const,
  entity_id: product.product_id, expected_revision: product.catalog_revision,
  payload: {
   product_id: product.product_id,
   arabic_name: details.arabic_name, arabic_description: product.description_ar,
   english_name: details.english_name, english_description: product.description_en,
   width_cm: product.width_cm ?? 1, height_cm: product.height_cm ?? 1,
   egp_price_cents: details.egp_price_cents, usd_price_cents: details.usd_price_cents, cost_cents: details.cost_cents,
   top_category_id: product.top_category_id, subcategory_ids: product.subcategory_ids, tag_ids: product.tag_ids,
   expected_catalog_revision: product.catalog_revision,
  },
 };
}

export function categoryDetailsCommand(store: string, category: Pick<AdminCategoryRow, 'category_id' | 'catalog_revision'>, details: { name_ar: string; name_en: string; status: string }) {
 return {store_id:store, type:'catalog.category.details.update.v1' as const, entity_id:category.category_id, expected_revision:category.catalog_revision,
  payload:{category_id:category.category_id, name_ar:details.name_ar, name_en:details.name_en, status:details.status, expected_catalog_revision:category.catalog_revision}};
}

export function categoryParentsCommand(store: string, category: Pick<AdminCategoryRow, 'category_id' | 'catalog_revision'>, parents: string[]) {
 return {store_id:store, type:'catalog.category.parents.update.v1' as const, entity_id:category.category_id, expected_revision:category.catalog_revision,
  payload:{category_id:category.category_id, parent_ids:parents, expected_catalog_revision:category.catalog_revision}};
}

export function tagDetailsCommand(store: string, tag: Pick<AdminTagRow, 'tag_id' | 'catalog_revision'>, details: { name_ar: string; name_en: string; is_active: boolean }) {
 return {store_id:store, type:'catalog.tag.details.update.v1' as const, entity_id:tag.tag_id, expected_revision:tag.catalog_revision,
  payload:{tag_id:tag.tag_id, name_ar:details.name_ar, name_en:details.name_en, is_active:details.is_active, expected_catalog_revision:tag.catalog_revision}};
}

export function productTypeDetailsCommand(store: string, type: Pick<AdminProductType, 'type_id' | 'type_revision' | 'description_ar' | 'description_en' | 'position' | 'dimensions' | 'capabilities'>, names: { name_ar: string; name_en: string }) {
 return {store_id:store, type:'catalog.product-type.details.update.v1' as const, entity_id:type.type_id, expected_revision:type.type_revision,
  payload:{product_type_id:type.type_id, name_ar:names.name_ar, name_en:names.name_en,
   description_ar:type.description_ar ?? '', description_en:type.description_en ?? '', position:type.position,
   dimensions:type.dimensions, capabilities:type.capabilities, expected_type_revision:type.type_revision}};
}

export function productTypeStatusCommand(store: string, type: Pick<AdminProductType, 'type_id' | 'type_revision'>, active: boolean) {
 return {store_id:store, type:'catalog.product-type.status.update.v1' as const, entity_id:type.type_id, expected_revision:type.type_revision,
  payload:{product_type_id:type.type_id, is_active:active, expected_type_revision:type.type_revision}};
}

export function productTypeCreateCommand(store: string, details: { code: string; name_ar: string; name_en: string; position: number }) {
 return {store_id:store, type:'catalog.product-type.create.v1' as const, entity_id:'', expected_revision:0,
  payload:{code:details.code, name_ar:details.name_ar, name_en:details.name_en, position:details.position, dimensions:[] as string[], capabilities:[] as string[], expected_type_revision:0}};
}

export function productTypeAssignmentCommand(store: string, product: Pick<AdminProductDetail, 'product_id' | 'catalog_revision'>, typeID: string) {
 return {store_id:store, type:'catalog.product.type.assign.v1' as const, entity_id:product.product_id, expected_revision:product.catalog_revision,
  payload:{product_id:product.product_id, product_type_id:typeID, expected_catalog_revision:product.catalog_revision}};
}

// Only the commands currently rendered by this dashboard. Backend vocabulary
// and validation are independent; this union neither enables nor removes routes.
export type CatalogCommandRequest = ReturnType<
 typeof productClassificationCommand | typeof productOnlinePolicyCommand | typeof categoryOnlinePolicyCommand |
 typeof productDetailsCommand | typeof categoryDetailsCommand | typeof categoryParentsCommand | typeof tagDetailsCommand |
 typeof productTypeDetailsCommand | typeof productTypeStatusCommand | typeof productTypeCreateCommand | typeof productTypeAssignmentCommand
>;
