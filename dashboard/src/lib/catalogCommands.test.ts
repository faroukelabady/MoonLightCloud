import { expect, it } from 'vitest';
import { categoryOnlinePolicyCommand, productClassificationCommand, productOnlinePolicyCommand, productDetailsCommand, categoryDetailsCommand, categoryParentsCommand, tagDetailsCommand, productTypeDetailsCommand, productTypeStatusCommand, productTypeCreateCommand, productTypeAssignmentCommand } from './catalogCommands.js';

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


it('preserves Product detail nulls, defaults, exact money and serialized key order', () => {
 const p={product_id:'p',catalog_revision:17,description_ar:null,description_en:undefined,width_cm:null,height_cm:0,top_category_id:'root',subcategory_ids:['b','a'],tag_ids:[]};
 const input={arabic_name:'كتاب',english_name:'Book',egp_price_cents:'9007199254740993',usd_price_cents:'0',cost_cents:'000'};
 const before=JSON.stringify({p,input});// Deliberately untyped transport metadata: the builder must not repair it.
 const command=productDetailsCommand('store',p as unknown as Parameters<typeof productDetailsCommand>[1],input);
 expect(JSON.stringify(command)).toBe(JSON.stringify({store_id:'store',type:'catalog.product.details.update.v1',entity_id:'p',expected_revision:17,payload:{product_id:'p',arabic_name:'كتاب',arabic_description:null,english_name:'Book',english_description:undefined,width_cm:1,height_cm:0,egp_price_cents:'9007199254740993',usd_price_cents:'0',cost_cents:'000',top_category_id:'root',subcategory_ids:['b','a'],tag_ids:[],expected_catalog_revision:17}}));
 expect(command.payload.subcategory_ids).toBe(p.subcategory_ids);expect(command.payload.tag_ids).toBe(p.tag_ids);expect(JSON.stringify({p,input})).toBe(before);
});

it('keeps Category and Tag replacements, inactive false and catalog revisions', () => {
 const c={category_id:'c',catalog_revision:31};const parents=['z','a','z'];const details={name_ar:'فئة',name_en:'Category',status:'inactive'};
 expect(JSON.stringify(categoryDetailsCommand('store',c,details))).toBe(JSON.stringify({store_id:'store',type:'catalog.category.details.update.v1',entity_id:'c',expected_revision:31,payload:{category_id:'c',name_ar:'فئة',name_en:'Category',status:'inactive',expected_catalog_revision:31}}));
 expect(JSON.stringify(categoryParentsCommand('store',c,parents))).toBe(JSON.stringify({store_id:'store',type:'catalog.category.parents.update.v1',entity_id:'c',expected_revision:31,payload:{category_id:'c',parent_ids:parents,expected_catalog_revision:31}}));
 expect(categoryParentsCommand('store',c,parents).payload.parent_ids).toBe(parents);
 expect(categoryParentsCommand('store',c,[]).payload.parent_ids).toEqual([]);
 expect(JSON.stringify(tagDetailsCommand('store',{tag_id:'tag',catalog_revision:7},{name_ar:'وسم',name_en:'Tag',is_active:false}))).toBe(JSON.stringify({store_id:'store',type:'catalog.tag.details.update.v1',entity_id:'tag',expected_revision:7,payload:{tag_id:'tag',name_ar:'وسم',name_en:'Tag',is_active:false,expected_catalog_revision:7}}));
});

it('keeps ProductType metadata, ordered structural sets and type revision', () => {
 const type={type_id:'type',type_revision:9,description_ar:null,description_en:undefined,position:0,dimensions:['size','color'],capabilities:['frame_configuration']};
 const command=productTypeDetailsCommand('store',type,{name_ar:'كتاب',name_en:'Book'});
 expect(JSON.stringify(command)).toBe(JSON.stringify({store_id:'store',type:'catalog.product-type.details.update.v1',entity_id:'type',expected_revision:9,payload:{product_type_id:'type',name_ar:'كتاب',name_en:'Book',description_ar:'',description_en:'',position:0,dimensions:type.dimensions,capabilities:type.capabilities,expected_type_revision:9}}));
 expect(command.payload.dimensions).toBe(type.dimensions);expect(command.payload.capabilities).toBe(type.capabilities);
 expect(JSON.stringify(productTypeStatusCommand('store',type,false))).toBe(JSON.stringify({store_id:'store',type:'catalog.product-type.status.update.v1',entity_id:'type',expected_revision:9,payload:{product_type_id:'type',is_active:false,expected_type_revision:9}}));
});

it('distinguishes ProductType create identity from Product assignment revision', () => {
 expect(JSON.stringify(productTypeCreateCommand('store',{code:'book',name_ar:'كتاب',name_en:'Book',position:0}))).toBe(JSON.stringify({store_id:'store',type:'catalog.product-type.create.v1',entity_id:'',expected_revision:0,payload:{code:'book',name_ar:'كتاب',name_en:'Book',position:0,dimensions:[],capabilities:[],expected_type_revision:0}}));
 expect(JSON.stringify(productTypeAssignmentCommand('store',{product_id:'p',catalog_revision:17},'type'))).toBe(JSON.stringify({store_id:'store',type:'catalog.product.type.assign.v1',entity_id:'p',expected_revision:17,payload:{product_id:'p',product_type_id:'type',expected_catalog_revision:17}}));
});

function invalidRenderedCommandTypes() {
 // @ts-expect-error Exact money remains text, never a floating-point conversion.
 productDetailsCommand('s',{product_id:'p',catalog_revision:1,description_ar:null,description_en:null,width_cm:null,height_cm:null,top_category_id:'c',subcategory_ids:[],tag_ids:[]},{arabic_name:'a',english_name:'e',egp_price_cents:9007199254740993,usd_price_cents:'0',cost_cents:'0'});
 // @ts-expect-error Product assignment uses catalog revision, not Type revision.
 productTypeAssignmentCommand('s',{product_id:'p',type_revision:1},'t');
 // @ts-expect-error Type status uses Type revision, not catalog revision.
 productTypeStatusCommand('s',{type_id:'t',catalog_revision:1},true);
}
void invalidRenderedCommandTypes;
