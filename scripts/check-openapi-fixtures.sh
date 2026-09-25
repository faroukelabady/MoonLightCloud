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
print("openapi fixture parity: PASS")
PYEOF
