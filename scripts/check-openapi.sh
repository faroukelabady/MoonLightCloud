#!/usr/bin/env bash
# Strict OpenAPI gate: rejects duplicate YAML mapping keys (which plain
# parsers silently overwrite) and validates runtime-facing dashboard
# response codes are documented. Fails closed on any violation.
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
        for want in ("200", "401"):
            if want not in codes:
                raise SystemExit(f"FAIL: {method.upper()} {route} missing '{want}' response")
print("openapi strict gate: PASS (no duplicate keys; core statuses documented)")
PYEOF
