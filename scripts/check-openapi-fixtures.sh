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
print("openapi fixture parity: PASS")
PYEOF
