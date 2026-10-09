#!/usr/bin/env bash
# Strict OpenAPI gate: rejects duplicate YAML mapping keys (which plain
# parsers silently overwrite), rejects unknown/junk keys inside Schema
# Objects (catches unquoted commas in flow mappings that silently truncate
# descriptions into stray keys), validates runtime-facing dashboard
# response codes are documented, and runs full OpenAPI semantic validation.
# Fails closed on any violation.
set -euo pipefail
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
python3 - "$REPO_ROOT/api/openapi.yaml" <<'PYEOF'
import sys, yaml

class StrictLoader(yaml.SafeLoader):
    pass

def no_dup(loader, node, deep=False):
    mapping = {}
    for k, v in node.value:
        key = loader.construct_object(k, deep=True)
        if key in mapping:
            raise ValueError(f"duplicate mapping key {key!r} at line {k.start_mark.line + 1}")
        mapping[key] = loader.construct_object(v, deep=True)
    return mapping

StrictLoader.add_constructor(yaml.resolver.BaseResolver.DEFAULT_MAPPING_TAG, no_dup)

path = sys.argv[1]
with open(path) as f:
    spec = yaml.load(f, Loader=StrictLoader)

# Every dashboard + reports route with a body must document the statuses
# its handlers can return (drives the shared error-envelope contract).
for route, methods in spec.get("paths", {}).items():
    if not route.startswith("/api/v1/dashboard/") and not route.startswith("/api/v1/reports/"):
        continue
    for method, op in methods.items():
        if not isinstance(op, dict) or "responses" not in op:
            continue
        codes = set(op["responses"].keys())
        # A create-only route (e.g. POST rollouts) documents 201 truthfully
        # instead of a 200 it can never return; a 2xx is still mandatory.
        if not codes & {"200", "201"}:
            raise SystemExit(f"FAIL: {method.upper()} {route} missing '200'/'201' response")
        # The public activation endpoint has no session to reject: invalid
        # tokens are 400 ACTIVATION_INVALID (documented); nothing returns 401.
        if route == "/api/v1/dashboard/auth/activate":
            continue
        if "401" not in codes:
            raise SystemExit(f"FAIL: {method.upper()} {route} missing '401' response")

# Unknown keys inside Schema Objects are almost always truncated scalars
# (e.g. an unquoted comma in a flow mapping turns the remainder into a
# stray key). Reject them; x- extensions stay allowed.
SCHEMA_KEYS = {
    "type", "format", "title", "description", "example", "examples",
    "properties", "required", "items", "enum", "nullable", "default",
    "allOf", "oneOf", "anyOf", "not", "additionalProperties", "$ref",
    "minimum", "maximum", "minLength", "maxLength", "pattern",
    "uniqueItems", "minItems", "maxItems", "multipleOf",
    "exclusiveMinimum", "exclusiveMaximum", "readOnly", "writeOnly",
    "xml", "externalDocs", "deprecated", "discriminator",
}

SCHEMA_TYPES = {"object", "array", "string", "integer", "number", "boolean"}

def is_schema(node):
    # A Schema Object's "type" is always a type-name string. A properties
    # mapping merely *contains* a field that may itself be named "type"
    # (e.g. our command's "type": {"type": "string", ...}), whose value is
    # a dict — never confuse the two.
    if "$ref" in node or "properties" in node:
        return True
    if isinstance(node.get("type"), str) and node["type"] in SCHEMA_TYPES:
        return True
    if "enum" in node and "type" not in node:
        return True
    return False

def check_schema(node, where):
    if isinstance(node, dict):
        if is_schema(node):
            for k in node:
                if k not in SCHEMA_KEYS and not str(k).startswith("x-"):
                    raise SystemExit(f"FAIL: unknown schema key {k!r} at {where}")
        for k, v in node.items():
            check_schema(v, f"{where}/{k}")
    elif isinstance(node, list):
        for i, v in enumerate(node):
            check_schema(v, f"{where}[{i}]")

check_schema(spec.get("components", {}).get("schemas", {}), "components/schemas")
for route, methods in spec.get("paths", {}).items():
    if not isinstance(methods, dict):
        continue
    for method, op in methods.items():
        if isinstance(op, dict):
            check_schema(op.get("responses", {}), f"paths {route} {method} responses")
print("openapi strict gate: PASS (no duplicate keys; no junk schema keys; core statuses documented)")
PYEOF

# Full standards-compliant semantic validation. Pinned, like govulncheck.
if ! python3 -c "import openapi_spec_validator" 2>/dev/null; then
  echo "openapi-spec-validator missing; installing pinned version..." >&2
  python3 -m pip install --quiet "openapi-spec-validator==0.9.0" || {
    echo "FAIL: cannot install openapi-spec-validator" >&2
    exit 1
  }
fi
python3 - "$REPO_ROOT/api/openapi.yaml" <<'PYEOF'
import sys, yaml
from openapi_spec_validator import validate
with open(sys.argv[1]) as f:
    validate(yaml.safe_load(f))
print("openapi semantic gate: PASS")
PYEOF
