#!/usr/bin/env bash
# Mechanical fixture/schema parity gate (Phase 4A R58/R60): validates
# representative sale and return fixtures against api/openapi.yaml using the
# same python+yaml+jsonschema toolchain style as check-openapi.sh — no mere
# string containment. Fails closed on any mismatch.
set -euo pipefail
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
python3 - "$REPO_ROOT/api/openapi.yaml" "$REPO_ROOT" <<'PYEOF'
import copy, json, re, sys
import yaml
from jsonschema import Draft7Validator, FormatChecker

spec_path, repo = sys.argv[1], sys.argv[2]
with open(spec_path) as f:
    spec = yaml.safe_load(f)
schemas = spec["components"]["schemas"]

def resolve(node):
    if isinstance(node, dict):
        if set(node.keys()) == {"$ref"}:
            name = node["$ref"].split("/")[-1]
            return resolve(schemas[name])
        if "allOf" in node:
            merged = {}
            for part in node["allOf"]:
                merged = deep_merge(merged, resolve(part))
            rest = {k: resolve(v) for k, v in node.items() if k != "allOf"}
            return deep_merge(merged, rest)
        return {k: resolve(v) for k, v in node.items()}
    if isinstance(node, list):
        return [resolve(v) for v in node]
    return node

def deep_merge(a, b):
    out = copy.deepcopy(a)
    for k, v in b.items():
        if k in out and isinstance(out[k], dict) and isinstance(v, dict):
            out[k] = deep_merge(out[k], v)
        elif k in out and isinstance(out[k], list) and isinstance(v, list):
            out[k] = out[k] + [x for x in v if x not in out[k]]
        else:
            out[k] = copy.deepcopy(v)
    return out

def denull(node):
    # OpenAPI 3.0 nullable -> JSON Schema type union.
    if isinstance(node, dict):
        node = dict(node)
        if node.pop("nullable", False) and "type" in node and node["type"] != "object":
            node["type"] = [node["type"], "null"]
        if node.get("type") == "object" and "properties" not in node:
            node["additionalProperties"] = True
        return {k: denull(v) for k, v in node.items()}
    if isinstance(node, list):
        return [denull(v) for v in node]
    return node

def load_fixture(path):
    with open(f"{repo}/{path}") as f:
        return json.load(f)

def check(name, schema_name, instance, expect_valid=True):
    schema = denull(resolve(schemas[schema_name]))
    errors = list(Draft7Validator(schema, format_checker=FormatChecker()).iter_errors(instance))
    if expect_valid and errors:
        for e in errors[:3]:
            print(f"  schema error: {e.message} at {list(e.path)}", file=sys.stderr)
        raise SystemExit(f"FAIL: {name} must validate against {schema_name}")
    if not expect_valid and not errors:
        raise SystemExit(f"FAIL: {name} must NOT validate against {schema_name}")
    print(f"ok: {name} {'valid' if expect_valid else 'rejected'} vs {schema_name}")

# Canonical fixtures match exactly one branch each.
check("return_partial.json", "ReturnRefundFinalizedV1", load_fixture("internal/returnrefund/testdata/return_partial.json"))
check("return_full.json", "ReturnRefundFinalizedV1", load_fixture("internal/returnrefund/testdata/return_full.json"))
check("sale_usd.json", "SaleFinalizedV1", load_fixture("internal/sale/testdata/sale_usd.json"))
check("sale_egp.json", "SaleFinalizedV1", load_fixture("internal/sale/testdata/sale_egp.json"))

# Minimal Retail-valid shop (Arabic name only; English name and phone blank)
# must validate for both events (R60 drift guard).
minimal_sale = load_fixture("internal/sale/testdata/sale_egp.json")
minimal_sale["shop"] = {"name_ar": "متجر", "name_en": "", "address_ar": "", "address_en": "",
                        "phone": "", "receipt_footer_ar": "", "receipt_footer_en": ""}
check("minimal-shop sale", "SaleFinalizedV1", minimal_sale)
minimal_ret = load_fixture("internal/returnrefund/testdata/return_full.json")
minimal_ret["shop"] = dict(minimal_sale["shop"])
check("minimal-shop return", "ReturnRefundFinalizedV1", minimal_ret)

# Invalid shapes are rejected by the modeled contract.
bad_return = load_fixture("internal/returnrefund/testdata/return_partial.json")
bad_return["lines"] = []
check("empty-lines return", "ReturnRefundFinalizedV1", bad_return, expect_valid=False)
# Phase 8C v2 (per-line tag snapshots) and Phase 17-R0 v3 (per-line
# frozen variant snapshots) keep every v1 invariant and validate through
# the published union. History law: v3 snapshots are event-sourced only.
sale_v2 = load_fixture("internal/sale/testdata/sale_usd.json")
for line in sale_v2["lines"]:
    line["tags"] = [{"tag_id": "aaaaaaaa-0000-4000-8000-000000000001",
                     "slug": "horse", "name_ar": "حصان", "name_en": "Horse"}]
check("sale_v2", "SaleFinalizedV2", sale_v2)
sale_v3 = copy.deepcopy(sale_v2)
for line in sale_v3["lines"]:
    line["variant_id"] = "bbbbbbbb-0000-4000-8000-000000000001"
    line["variant_sku"] = "NFT-BLU-TRD"
    line["variant_attributes"] = [{
        "definition_code": "color", "value_code": "blue",
        "name_ar": "أزرق", "name_en": "Blue",
        "definition_name_ar": "اللون", "definition_name_en": "Color"}]
    line["variant_price_egp_cents"] = 65000
    line["variant_price_usd_cents"] = None
check("sale_v3", "SaleFinalizedV3", sale_v3)
bad_override = copy.deepcopy(sale_v3)
bad_override["lines"][0]["variant_price_egp_cents"] = -1
check("sale_v3_negative_override", "SaleFinalizedV3", bad_override, expect_valid=False)
too_many = copy.deepcopy(sale_v3)
too_many["lines"][0]["variant_attributes"] = [
    {"definition_code": "d%d" % i, "value_code": "v", "name_ar": "أ", "definition_name_ar": "د"}
    for i in range(33)]
check("sale_v3_too_many_attributes", "SaleFinalizedV3", too_many, expect_valid=False)
sale_v3_envelope = {
    "event_id": "22222222-2222-4222-8222-222222222222",
    "device_id": "11111111-1111-4111-8111-111111111111",
    "event_type": "sale.finalized.v3",
    "occurred_at": "2026-10-05T00:00:00Z",
    "payload": sale_v3,
}
check("sync_event_union_sale_v3", "SyncEvent", sale_v3_envelope)
check("sync_batch_sale_v3", "SyncBatch", {"events": [sale_v3_envelope]})
bad_sale = load_fixture("internal/sale/testdata/sale_usd.json")
bad_sale["lines"] = []
check("empty-lines sale", "SaleFinalizedV1", bad_sale, expect_valid=False)
bad_shop = load_fixture("internal/returnrefund/testdata/return_partial.json")
bad_shop["shop"]["name_ar"] = ""
check("blank-name_ar return", "ReturnRefundFinalizedV1", bad_shop, expect_valid=False)

# C01: note asymmetry uses ASCII runs so Go runes, Python code points, and
# JSON Schema maxLength agree deterministically (no leading/trailing space).
note_base = load_fixture("internal/returnrefund/testdata/return_partial.json")
note_ok = dict(note_base)
note_ok["note"] = "x" * 500
check("note-500 return", "ReturnRefundFinalizedV1", note_ok)
note_bad = dict(note_base)
note_bad["note"] = "x" * 501
check("note-501 return", "ReturnRefundFinalizedV1", note_bad, expect_valid=False)

# C01: quantity boundaries (runtime: 1 <= q <= 2147483647).
qty_base = load_fixture("internal/returnrefund/testdata/return_partial.json")
qty_zero = copy.deepcopy(qty_base)
qty_zero["lines"][0]["quantity"] = 0
check("quantity-0 return", "ReturnRefundFinalizedV1", qty_zero, expect_valid=False)
qty_huge = copy.deepcopy(qty_base)
qty_huge["lines"][0]["quantity"] = 2147483648
check("quantity-2147483648 return", "ReturnRefundFinalizedV1", qty_huge, expect_valid=False)

# C02: whitespace-only name_ar is invalid; normal Arabic is valid. Optional
# fields stay blank-valid (R14 regression).
ws_return = copy.deepcopy(note_base)
ws_return["shop"] = {"name_ar": "   ", "name_en": "", "address_ar": "", "address_en": "",
                     "phone": "", "receipt_footer_ar": "", "receipt_footer_en": ""}
check("whitespace-name_ar return", "ReturnRefundFinalizedV1", ws_return, expect_valid=False)
ws_sale = load_fixture("internal/sale/testdata/sale_egp.json")
ws_sale["shop"] = dict(ws_return["shop"])
check("whitespace-name_ar sale", "SaleFinalizedV1", ws_sale, expect_valid=False)

# Phase 4B: return-aware report responses validate against the additive
# reporting schemas. The negative-net case is the load-bearing proof that
# net fields are signed (never the nonnegative intake Money schema).
period = {"kind": "custom", "timezone": "Africa/Cairo",
          "start_local": "2026-09-20T00:00:00+03:00",
          "end_local_exclusive": "2026-09-21T00:00:00+03:00",
          "start_utc": "2026-09-19T21:00:00Z", "end_utc": "2026-09-20T21:00:00Z"}
fresh = {"projection_backlog_count": 0, "blocked_sale_event_count": 0,
         "cloud_projection_complete": True, "return_backlog_count": 0,
         "return_blocked_count": 0, "return_projection_complete": True}
check("summary negative-net", "SalesSummary", {
    "generated_at": "2026-09-20T21:00:00Z", "timezone": "Africa/Cairo",
    "period": period, "transaction_count": 0, "units_sold": 0,
    "return_transaction_count": 1, "units_returned": 2,
    "currency_totals": [{"currency": "EGP", "subtotal_minor": 0,
                         "discount_minor": 0, "tax_minor": 0,
                         "sales_total_minor": 0, "line_cost_minor": 0,
                         "refund_total_minor": 200000, "net_sales_minor": -200000,
                         "returned_units": 2, "returned_cost_minor": 800,
                         "net_cost_minor": -800}],
    "payment_totals": [], "freshness": fresh})
check("daily row with returns", "DailyRow", {
    "date": "2026-09-20", "transactions": 3, "units": 5,
    "return_transactions": 4, "units_returned": 4,
    "currency_totals": [{"currency": "EGP", "subtotal_minor": 400000,
                         "discount_minor": 0, "tax_minor": 0,
                         "sales_total_minor": 400000, "line_cost_minor": 0,
                         "refund_total_minor": 300000, "net_sales_minor": 100000,
                         "returned_units": 3, "returned_cost_minor": 1200,
                         "net_cost_minor": -1200}]})
check("line breakdown with refund", "LineSaleTotal", {
    "currency": "EGP", "line_sales_minor": 400000, "line_cost_minor": 0,
    "line_refund_minor": 300000, "line_returned_cost_minor": 1200})
check("header breakdown with net", "SaleCurrencyTotal", {
    "currency": "EGP", "subtotal_minor": 400000, "discount_minor": 0,
    "tax_minor": 0, "sales_total_minor": 400000,
    "refund_total_minor": 300000, "net_sales_minor": 100000})

# Phase 4B-R1: Sync Health carries independent Sale and Return diagnostic
# channels; both errors present simultaneously must validate, proving the
# contract exposes them without one overwriting the other.
fresh4b = dict(fresh)
fresh4b.update({"latest_return_event_received_at": "2026-09-20T12:00:00Z",
                "latest_projected_return_occurred_at": "2026-09-20T12:00:00Z",
                "return_backlog_count": 0, "return_blocked_count": 1,
                "return_projection_complete": False})
check("sync health dual errors", "DashboardSyncHealth", {
    "freshness": fresh4b, "queue_count": 0, "pending_count": 0,
    "processed_count": 5, "blocked_count": 1, "retry_count": 0,
    "return_pending_count": 0, "return_processed_count": 5,
    "return_blocked_count": 1, "return_retry_count": 0,
    "last_error_event": "22222222-2222-7222-8222-222222222222",
    "last_error_code": "SALE_ID_CONFLICT",
    "last_error_label_ar": "تعارض", "last_error_label_en": "sale conflict",
    "return_last_error_event": "44444444-4444-7444-8444-444444444444",
    "return_last_error_code": "RETURN_REFUND_ID_CONFLICT",
    "return_last_error_label_ar": "تعارض مرتجعات",
    "return_last_error_label_en": "return conflict"})
# Phase 5A: catalog snapshot events validate against the additive catalog
# schemas. Shapes mirror the Retail builders (read-only contract parity,
# never a shared module).
check("category_valid.json", "CatalogCategorySnapshotV1", load_fixture("internal/catalog/testdata/category_valid.json"))
check("category_shared_parents.json", "CatalogCategorySnapshotV1", load_fixture("internal/catalog/testdata/category_shared_parents.json"))
check("tag_valid.json", "CatalogTagSnapshotV1", load_fixture("internal/catalog/testdata/tag_valid.json"))
check("product_valid.json", "CatalogProductSnapshotV1", load_fixture("internal/catalog/testdata/product_valid.json"))
check("product_multi_root_tags.json", "CatalogProductSnapshotV1", load_fixture("internal/catalog/testdata/product_multi_root_tags.json"))
check("product_minimal.json", "CatalogProductSnapshotV1", load_fixture("internal/catalog/testdata/product_minimal.json"))
# Phase 5B: sales-policy snapshots carry channel eligibility plus the
# allocation cap only (never stock, availability, or provider identity).
check("policy_offline_only.json", "CatalogProductSalesPolicySnapshotV1", load_fixture("internal/catalog/testdata/policy_offline_only.json"))
check("policy_online_capped.json", "CatalogProductSalesPolicySnapshotV1", load_fixture("internal/catalog/testdata/policy_online_capped.json"))
policy_bad = copy.deepcopy(load_fixture("internal/catalog/testdata/policy_online_capped.json"))
policy_bad["online_allocation_limit"] = -1
check("policy-negative-cap", "CatalogProductSalesPolicySnapshotV1", policy_bad, expect_valid=False)
# Canonical rule (cap requires sell_online) is runtime validation, not
# schema: covered by TestValidateProductSalesPolicySnapshot.
# Phase 5C: inventory snapshots carry authoritative stock only (never
# negative: the Retail ledger guarantees >= 0 and Cloud mirrors it).
check("inventory_valid.json", "InventoryProductSnapshotV1", load_fixture("internal/catalog/testdata/inventory_valid.json"))
check("inventory_zero.json", "InventoryProductSnapshotV1", load_fixture("internal/catalog/testdata/inventory_zero.json"))
check("inventory_max.json", "InventoryProductSnapshotV1", load_fixture("internal/catalog/testdata/inventory_max.json"))
inv_bad = copy.deepcopy(load_fixture("internal/catalog/testdata/inventory_valid.json"))
inv_bad["stock_quantity"] = -1
check("inventory-negative", "InventoryProductSnapshotV1", inv_bad, expect_valid=False)
inv_huge = copy.deepcopy(load_fixture("internal/catalog/testdata/inventory_valid.json"))
inv_huge["stock_quantity"] = 2147483648
check("inventory-overflow", "InventoryProductSnapshotV1", inv_huge, expect_valid=False)
inv_rev0 = copy.deepcopy(load_fixture("internal/catalog/testdata/inventory_valid.json"))
inv_rev0["inventory_revision"] = 0
check("inventory-rev0", "InventoryProductSnapshotV1", inv_rev0, expect_valid=False)
inv_noid = copy.deepcopy(load_fixture("internal/catalog/testdata/inventory_valid.json"))
del inv_noid["product_id"]
check("inventory-missing-id", "InventoryProductSnapshotV1", inv_noid, expect_valid=False)
# R09: SKU parity with Retail/Cloud runtime CR/LF/TAB rejection.
sku_base = load_fixture("internal/catalog/testdata/product_valid.json")
for label, char in [("LF", "\n"), ("CR", "\r"), ("TAB", "\t")]:
    bad_sku = copy.deepcopy(sku_base)
    bad_sku["sku"] = "PAP" + char + "001"
    check("sku-" + label + " product", "CatalogProductSnapshotV1", bad_sku, expect_valid=False)
check("product_bad_money.json", "CatalogProductSnapshotV1", load_fixture("internal/catalog/testdata/product_bad_money.json"), expect_valid=False)
check("product_bad_dims.json", "CatalogProductSnapshotV1", load_fixture("internal/catalog/testdata/product_bad_dims.json"), expect_valid=False)
check("category_bad_shape.json", "CatalogCategorySnapshotV1", load_fixture("internal/catalog/testdata/category_bad_shape.json"), expect_valid=False)
check("revision_zero.json", "CatalogTagSnapshotV1", load_fixture("internal/catalog/testdata/revision_zero.json"), expect_valid=False)

# Phase 6C: dashboard order list/detail carry exact string money beyond
# 2^53, canonical statuses, mapping completeness, and deletion flags.
order_summary = {
    "provider_key": "website", "external_order_id": "100", "order_number": "100",
    "provider_status": "processing", "canonical_status": "PROCESSING",
    "currency": "EGP", "total_minor": "9007199254740993",
    "created_at": "2026-09-27T10:00:00Z", "modified_at": "2026-09-27T11:00:00Z",
    "customer_name": "A B", "mapping_complete": False, "unmapped_line_count": 1,
    "provider_deleted": False, "revision": 2,
}
check("order list", "DashboardOrderList", {
    "orders": [order_summary],
    "next_cursor": "eyJ2IjoxLCJ0IjoiMjAyNi0wOS0yN1QxMTowMDowMFoiLCJwIjoid2Vic2l0ZSIsIm8iOiIxMDAiLCJmcCI6IiIsImZzIjoiIn0",
    "status_counts": [{"canonical_status": "PROCESSING", "total": 1}],
    "webhook_inbox": {"pending": 0, "retry": 1, "blocked": 0, "oldest_pending_at": None}})
check("order list final page", "DashboardOrderList", {
    "orders": [order_summary],
    "next_cursor": None,
    "status_counts": [{"canonical_status": "PROCESSING", "total": 1}],
    "webhook_inbox": {"pending": 0, "retry": 1, "blocked": 0, "oldest_pending_at": None}})
check("order detail", "DashboardOrderDetail", {
    "summary": order_summary,
    "discount_minor": "0", "shipping_minor": "3000", "cart_tax_minor": "0",
    "total_tax_minor": "0", "prices_include_tax": False,
    "paid_at": "2026-09-27T10:05:00Z", "completed_at": None,
    "payment_method": "cod", "payment_method_title": "Cash on delivery",
    "customer_first_name": "A", "customer_last_name": "B",
    "customer_email": "a@example.com", "customer_phone": "+201000000000",
    "lines": [{"external_line_id": 1, "external_product_id": "500", "variation_id": 0,
               "sku": "PAP-1", "name": "X", "quantity": 2,
               "total_minor": "9007199254740993",
               "moonlight_product_id": None, "mapped": False}],
    "addresses": [{"kind": "billing", "first_name": "A", "last_name": "B",
                   "company": "", "address_1": "Cairo", "address_2": "",
                   "city": "Cairo", "state": "", "postcode": "", "country": "EG",
                   "email": "a@example.com", "phone": "+201000000000"}],
    "status_history": [{"order_revision": 1, "provider_status": "pending",
                        "canonical_status": "PENDING", "observed_at": "2026-09-27T10:00:00Z"},
                       {"order_revision": 2, "provider_status": "processing",
                        "canonical_status": "PROCESSING", "observed_at": "2026-09-27T11:00:00Z"}]})
bad_order = copy.deepcopy(order_summary)
bad_order["total_minor"] = 9007199254740993
check("order unsafe money", "DashboardOrderSummary", bad_order, expect_valid=False)

# Phase 15-R1 F15: configuration snapshot + order selection contract.
config_entry = {
    "configuration_id": "11111111-0000-4000-8000-0000000000c1",
    "kind": "frame",
    "style_code": "classic", "style_name_ar": "كلاسيكي", "style_name_en": "Classic",
    "color_code": "black", "color_name_ar": "أسود", "color_name_en": "Black",
    "price_delta_egp_cents": 30000, "price_delta_usd_cents": 2500,
    "enabled": True, "position": 0, "configuration_revision": 1,
}
config_snapshot = {
    "product_id": "11111111-0000-4000-8000-0000000000aa",
    "configurations": [config_entry],
    "configuration_revision": 3,
}
check("configuration_snapshot", "CatalogProductConfigurationsSnapshotV1", config_snapshot)
bad_sentinel = dict(config_snapshot, configurations=[dict(config_entry,
    configuration_id="00000000-0000-0000-0000-000000000000")])
check("configuration_sentinel_identity", "CatalogProductConfigurationsSnapshotV1", bad_sentinel, expect_valid=False)
bad_delta = dict(config_snapshot, configurations=[dict(config_entry, price_delta_egp_cents=-1)])
check("configuration_negative_delta", "CatalogProductConfigurationsSnapshotV1", bad_delta, expect_valid=False)
# Cross-item style/color uniqueness is NOT JSON-Schema expressible; it is
# enforced by the authoritative configuration validator and DB constraint
# (Go tests: duplicate style+color and duplicate canonical IDs rejected).
bad_null_usd = dict(config_snapshot, configurations=[dict(config_entry, price_delta_usd_cents=None)])
check("configuration_null_usd_distinct", "CatalogProductConfigurationsSnapshotV1", bad_null_usd)
order_line_selection = {
    "external_line_id": 1, "external_product_id": "1000", "variation_id": 2000,
    "sku": "ML-1", "name": "Papyrus", "quantity": 1, "total_minor": "130000", "mapped": True,
    "configuration_id": "11111111-0000-4000-8000-0000000000c1",
    "frame_style_code": "classic", "frame_style_name_ar": "كلاسيكي", "frame_style_name_en": "Classic",
    "frame_color_code": "black", "frame_color_name_ar": "أسود", "frame_color_name_en": "Black",
    "configuration_price_delta_minor": "30000",
    "provider_configuration_id": "gid://shopify/ProductVariant/1",
    "configuration_unresolved": False,
}
check("order_line_selection", "DashboardOrderLine", order_line_selection)
order_line_noframe = {
    "external_line_id": 2, "external_product_id": "1000", "variation_id": 0,
    "sku": "ML-1", "name": "Papyrus", "quantity": 1, "total_minor": "100000", "mapped": True,
    "configuration_id": None,
    "configuration_unresolved": False,
}
check("order_line_noframe", "DashboardOrderLine", order_line_noframe)
order_line_unresolved = {
    "external_line_id": 3, "external_product_id": "1000", "variation_id": 2001,
    "sku": "ML-1", "name": "Papyrus", "quantity": 1, "total_minor": "100000", "mapped": True,
    "configuration_id": None,
    "provider_configuration_id": "9999",
    "configuration_unresolved": True,
}
check("order_line_unresolved", "DashboardOrderLine", order_line_unresolved)
# Phase 17 order-line variant snapshot (00034): frozen purchase-time
# variant identity and labels, served exactly as captured.
order_line_variant = dict(order_line_selection,
    variant_id="11111111-0000-4000-8000-0000000000b1",
    variant_sku="ML-1-A",
    variant_attributes=[{"definition_code": "size", "value_code": "a5",
                         "name_ar": "أ5", "name_en": "A5",
                         "definition_name_ar": "المقاس", "definition_name_en": "Size",
                         "position": 0}])
check("order_line_variant_snapshot", "DashboardOrderLine", order_line_variant)

# Phase 15-R2 F15: the configuration event must validate through the
# FULL published SyncEvent union (and SyncBatch), not only its leaf.
config_envelope = {
    "event_id": "22222222-2222-4222-8222-222222222222",
    "device_id": "11111111-1111-4111-8111-111111111111",
    "event_type": "catalog.product.configuration.snapshot.v1",
    "occurred_at": "2026-10-05T00:00:00Z",
    "payload": config_snapshot,
}
check("sync_event_union_configuration", "SyncEvent", config_envelope)
bad_envelope = dict(config_envelope)
bad_envelope["payload"] = dict(config_snapshot, configurations=[dict(config_entry, price_delta_egp_cents=-5)])
check("sync_event_union_configuration_rejects_bad_leaf", "SyncEvent", bad_envelope, expect_valid=False)
batch = {"events": [config_envelope]}
check("sync_batch_configuration", "SyncBatch", batch)
category_v2 = dict(load_fixture("internal/catalog/testdata/category_valid.json"), online_enabled=True)
check("category_v2", "CatalogCategorySnapshotV2", category_v2)
category_envelope = dict(config_envelope, event_type="catalog.category.snapshot.v2", payload=category_v2)
check("category_v2_event", "SyncEvent", category_envelope)
check("category_v2_missing_policy", "CatalogCategorySnapshotV2", load_fixture("internal/catalog/testdata/category_valid.json"), expect_valid=False)

# Phase 17: product v2 (SKU authority moved to the variant) and physical
# variants must validate through the published union, and reject bad leaf
# shapes. Phase 17-R0 (ADR-0049): v2 carries NO SKU-bearing field at all
# — both `sku` and the retired `primary_variant_sku` mirror are outside
# the contract (Cloud's decoder tolerates-and-ignores the mirror for one
# release; see TestProductSnapshotV2Contract).
product_v2 = load_fixture("internal/catalog/testdata/product_valid.json")
product_v2.pop("sku")
check("product_v2", "CatalogProductSnapshotV2", product_v2)
product_v2_bad = dict(product_v2, sku="PAP-001")
check("product_v2_rejects_sku", "CatalogProductSnapshotV2", product_v2_bad, expect_valid=False)
product_v2_mirror = dict(product_v2, primary_variant_sku="PAP-001")
check("product_v2_rejects_retired_mirror", "CatalogProductSnapshotV2", product_v2_mirror, expect_valid=False)
product_v2_envelope = dict(config_envelope, event_type="catalog.product.snapshot.v2", payload=product_v2)
check("sync_event_union_product_v2", "SyncEvent", product_v2_envelope)

variant_snapshot = {
    "variant_id": "11111111-0000-4000-8000-0000000000b1",
    "product_id": "11111111-0000-4000-8000-0000000000aa",
    "sku": "PAP-001-BLK",
    "is_active": True,
    "deleted": False,
    "price_egp_cents": 70000,
    "price_usd_cents": None,
    "stock_quantity": 4,
    "inventory_revision": 2,
    "variant_revision": 5,
    "position": 0,
    "combination_key": "colour\u001fblack\u001fsize\u001fa5",
    "attributes": [{
        "definition_code": "colour", "value_code": "black",
        "name_ar": "أسود", "name_en": "Black",
        "definition_name_ar": "اللون", "definition_name_en": "Colour",
        "position": 0,
    }],
    "catalog_revision": 3,
}
check("product_variant_snapshot", "CatalogProductVariantSnapshotV1", variant_snapshot)
check("product_variant_missing_sku", "CatalogProductVariantSnapshotV1",
      {k: v for k, v in variant_snapshot.items() if k != "sku"}, expect_valid=False)
check("product_variant_negative_sku_whitespace", "CatalogProductVariantSnapshotV1",
      dict(variant_snapshot, sku="PAP\n001"), expect_valid=False)
variant_envelope = dict(config_envelope, event_type="catalog.product_variant.snapshot.v1", payload=variant_snapshot)
check("sync_event_union_product_variant", "SyncEvent", variant_envelope)
check("sync_batch_product_variant", "SyncBatch", {"events": [variant_envelope]})

inventory_variant = {
    "variant_id": "11111111-0000-4000-8000-0000000000b1",
    "product_id": "11111111-0000-4000-8000-0000000000aa",
    "sku": "PAP-001-BLK",
    "stock_quantity": 4,
    "inventory_revision": 2,
    "ready": True,
    "sell_online": True,
    "online_allocation_limit": 10,
    "policy_revision": 1,
    "catalog_revision": 3,
}
check("inventory_product_variant_snapshot", "InventoryProductVariantSnapshotV1", inventory_variant)
check("inventory_product_variant_negative_stock", "InventoryProductVariantSnapshotV1",
      dict(inventory_variant, stock_quantity=-1), expect_valid=False)
inventory_variant_envelope = dict(config_envelope, event_type="inventory.product_variant.snapshot.v1", payload=inventory_variant)
check("sync_event_union_inventory_variant", "SyncEvent", inventory_variant_envelope)

print("openapi fixture parity: PASS")
PYEOF
