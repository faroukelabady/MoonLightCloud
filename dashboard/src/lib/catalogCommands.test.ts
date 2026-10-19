import { expect, it } from 'vitest';
import { categoryOnlinePolicyCommand, productClassificationCommand, productOnlinePolicyCommand } from './catalogCommands.js';

it('keeps classification wire keys, replacement order and source identity', () => {
 const product = { product_id: 'product-1', catalog_revision: 17 };
 const classification = { top_category_id: 'root', subcategory_ids: ['z', 'a', 'z'], tag_ids: ['tag-b', 'tag-a'] };
 const before = JSON.stringify({ product, classification });
 expect(JSON.stringify(productClassificationCommand('store-1', product, classification))).toBe('{"store_id":"store-1","type":"catalog.product.classification.update.v1","entity_id":"product-1","expected_revision":17,"payload":{"product_id":"product-1","top_category_id":"root","subcategory_ids":["z","a","z"],"tag_ids":["tag-b","tag-a"],"expected_catalog_revision":17}}');
 expect(JSON.stringify({ product, classification })).toBe(before);
 expect(productClassificationCommand('store-2', product, { top_category_id: '', subcategory_ids: [], tag_ids: [] }).payload).toEqual({ product_id: 'product-1', top_category_id: '', subcategory_ids: [], tag_ids: [], expected_catalog_revision: 17 });
});

it('preserves online false and the correct distinct revision streams', () => {
 for (const online of [true, false]) {
  expect(productOnlinePolicyCommand('store-1', { product_id: 'product-1', sales_policy_revision: 29 }, online)).toEqual({store_id:'store-1',type:'catalog.product.online-policy.update.v1',entity_id:'product-1',expected_revision:29,payload:{product_id:'product-1',sell_online:online,expected_sales_policy_revision:29}});
  expect(categoryOnlinePolicyCommand('store-2', { category_id: 'category-1', catalog_revision: 31 }, online)).toEqual({store_id:'store-2',type:'catalog.category.online-policy.update.v1',entity_id:'category-1',expected_revision:31,payload:{category_id:'category-1',online_enabled:online,expected_catalog_revision:31}});
 }
});

function invalidCommandBuilders() {
 // @ts-expect-error Classification requires an explicit replacement tag set.
 productClassificationCommand('store', {product_id:'p',catalog_revision:1}, {top_category_id:'root',subcategory_ids:[]});
 // @ts-expect-error Product policy uses the sales-policy stream, not catalog.
 productOnlinePolicyCommand('store', {product_id:'p',catalog_revision:1}, true);
 // @ts-expect-error Category policy uses the catalog stream.
 categoryOnlinePolicyCommand('store', {category_id:'c',sales_policy_revision:1}, false);
}
void invalidCommandBuilders;
