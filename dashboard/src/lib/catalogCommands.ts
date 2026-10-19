import type { AdminCategoryRow, AdminProductDetail } from './apiTypes.js';

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
