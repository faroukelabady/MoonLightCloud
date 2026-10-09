#!/usr/bin/env bash
# Canonical dashboard browser E2E (Playwright) against a throwaway
# PRODUCTION-mode Cloud. Nothing here touches a real deployment.
#
#   ./scripts/e2e-dashboard.sh [playwright args...]
#
# What it does:
#   1. builds the Cloud image from this tree (or uses E2E_IMAGE);
#   2. starts postgres:18.1 + Cloud with ENVIRONMENT=production and fresh
#      random secrets (no default account; clean-slate catalog);
#   3. serves it over local HTTPS (dashboard/e2e/support/tls-proxy.mjs) with
#      a per-run CA/leaf certificate, so the real __Host-mlc_session cookie
#      (Secure, HttpOnly, SameSite=Strict) is exercised; Chromium trusts
#      exactly that leaf (SPKI pin) and Node trusts the run CA;
#   4. bootstraps an OWNER through the CLI and registers Store A and Store B
#      through the real device API (two provisioned device credentials);
#   5. runs Playwright with E2E_PROVISION=1: global setup enrolls the
#      OWNER's MFA and creates + enrolls a Store-A-only ADMIN via the API.
#
# Test data stays test-owned: specs that need report rows ingest them
# through the device API themselves (e.g. phase12-r1); nothing is seeded
# globally. Requires podman, node/npx (dashboard deps installed, Chromium via
# `npx playwright install chromium`), openssl and curl.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
cd "$REPO_ROOT"

for c in podman node npx openssl curl; do
  command -v "$c" >/dev/null 2>&1 || { echo "error: $c is required" >&2; exit 1; }
done

STORE_A="aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
STORE_B="bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
HTTP_PORT="${E2E_HTTP_PORT:-19700}"
HTTPS_PORT="${E2E_HTTPS_PORT:-19743}"
IMG="${E2E_IMAGE:-}"
tag="mle2e-$$"
NET="$tag-net" PG="$tag-pg" APP="$tag-app"
WORK="$(mktemp -d)"
PROXY_PID=""

cleanup() {
  [[ -n "$PROXY_PID" ]] && kill "$PROXY_PID" >/dev/null 2>&1 || true
  podman rm -f "$APP" "$PG" >/dev/null 2>&1 || true
  podman network rm "$NET" >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT

rand() { head -c "$1" /dev/urandom | base64 | tr -d '/+=\n'; }

if [[ -z "$IMG" ]]; then
  IMG="moonlight-cloud:e2e"
  echo "== building $IMG from this tree =="
  podman build -q -f deploy/Containerfile -t "$IMG" . >/dev/null
fi

echo "== production-mode Cloud ($IMG) =="
PGPW="$(rand 24)"
podman network create "$NET" >/dev/null
podman run -d --name "$PG" --network "$NET" -e POSTGRES_USER=cloud -e "POSTGRES_PASSWORD=$PGPW" -e POSTGRES_DB=cloud \
  docker.io/library/postgres:18.1-bookworm >/dev/null
for _ in $(seq 60); do podman exec "$PG" pg_isready -U cloud >/dev/null 2>&1 && break; sleep 1; done
sleep 2
ENVS=(-e ENVIRONMENT=production -e "DATABASE_URL=postgres://cloud:$PGPW@$PG:5432/cloud?sslmode=disable"
  -e "DEVICE_SECRET_PEPPER=$(head -c 32 /dev/urandom | base64 -w0)" -e STORE_TIMEZONE=Africa/Cairo
  -e "REPORTING_API_TOKEN=$(rand 32)" -e "AUTH_MFA_ENCRYPTION_KEY=$(head -c 32 /dev/urandom | base64 -w0)")
podman run --rm --network "$NET" "${ENVS[@]}" "$IMG" migrate up >/dev/null
podman run -d --name "$APP" --network "$NET" -p "127.0.0.1:$HTTP_PORT:8080" "${ENVS[@]}" "$IMG" serve >/dev/null
HTTP="http://127.0.0.1:$HTTP_PORT"
for _ in $(seq 60); do [[ "$(curl -s -o /dev/null -w '%{http_code}' "$HTTP/health/ready")" == 200 ]] && break; sleep 1; done
[[ "$(curl -s -o /dev/null -w '%{http_code}' "$HTTP/health/ready")" == 200 ]] || { echo "error: Cloud not ready" >&2; podman logs "$APP" >&2; exit 1; }

echo "== local HTTPS (per-run CA, pinned leaf) =="
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj "/CN=MoonLight E2E local CA" \
  -keyout "$WORK/ca.key" -out "$WORK/ca.pem" -addext "basicConstraints=critical,CA:TRUE" >/dev/null 2>&1
openssl req -newkey rsa:2048 -nodes -subj "/CN=127.0.0.1" -keyout "$WORK/leaf.key" -out "$WORK/leaf.csr" >/dev/null 2>&1
printf 'subjectAltName=IP:127.0.0.1,DNS:localhost\nextendedKeyUsage=serverAuth\n' >"$WORK/leaf.ext"
openssl x509 -req -in "$WORK/leaf.csr" -CA "$WORK/ca.pem" -CAkey "$WORK/ca.key" -CAcreateserial -days 1 \
  -extfile "$WORK/leaf.ext" -out "$WORK/leaf.pem" >/dev/null 2>&1
SPKI="$(openssl x509 -in "$WORK/leaf.pem" -pubkey -noout | openssl pkey -pubin -outform der | openssl dgst -sha256 -binary | base64)"
node dashboard/e2e/support/tls-proxy.mjs "$HTTPS_PORT" "$HTTP" "$WORK/leaf.pem" "$WORK/leaf.key" >"$WORK/proxy.log" 2>&1 &
PROXY_PID=$!
BASE="https://127.0.0.1:$HTTPS_PORT"
for _ in $(seq 30); do [[ "$(curl -s --cacert "$WORK/ca.pem" -o /dev/null -w '%{http_code}' "$BASE/health/ready")" == 200 ]] && break; sleep 0.5; done
[[ "$(curl -s --cacert "$WORK/ca.pem" -o /dev/null -w '%{http_code}' "$BASE/health/ready")" == 200 ]] || { echo "error: TLS endpoint not ready" >&2; cat "$WORK/proxy.log" >&2; exit 1; }

echo "== OWNER bootstrap (CLI) and Stores via the device API =="
OWNER_USER="owner@e2e.test" OWNER_PW="$(rand 24)"
printf '%s\n' "$OWNER_PW" | podman run --rm -i --network "$NET" "${ENVS[@]}" "$IMG" \
  auth bootstrap-owner --login "$OWNER_USER" --display-name "E2E Owner" --password-stdin >/dev/null
device() {
  podman exec "$APP" moonlight-cloud device create --name "$1" 2>&1 |
    grep -oE '[0-9a-f-]{36}\.[0-9a-f-]{36}\.[0-9a-fA-F]+' | head -1
}
register() {
  local code
  code="$(curl -s -o "$WORK/reg.json" -w '%{http_code}' -H "Authorization: Bearer $1" -H 'Content-Type: application/json' \
    -d "{\"store_id\":\"$2\",\"display_name\":\"$3\",\"timezone\":\"Africa/Cairo\"}" "$HTTP/api/v1/sync/store-registration")"
  [[ "$code" == 200 ]] || { echo "error: store registration $3 -> $code" >&2; cat "$WORK/reg.json" >&2; exit 1; }
}
DEV_A="$(device e2e-store-a)"; DEV_B="$(device e2e-store-b)"
[[ -n "$DEV_A" && -n "$DEV_B" ]] || { echo "error: device provisioning failed" >&2; exit 1; }
register "$DEV_A" "$STORE_A" "Store A"
register "$DEV_B" "$STORE_B" "Store B"

echo "== Playwright (production auth over HTTPS) =="
mkdir -p "$WORK/totp"
rc=0
(
  cd dashboard
  E2E_BASE_URL="$BASE/dashboard/" E2E_TLS_SPKI="$SPKI" NODE_EXTRA_CA_CERTS="$WORK/ca.pem" \
  E2E_PROVISION=1 E2E_TOTP_STATE_DIR="$WORK/totp" \
  E2E_DASHBOARD_USER="$OWNER_USER" E2E_DASHBOARD_PASSWORD="$OWNER_PW" \
  E2E_ADMIN_USER="admin@e2e.test" E2E_ADMIN_PASSWORD="$(rand 24)" E2E_ADMIN_STORE="$STORE_A" \
  E2E_DEVICE_CREDENTIAL_A="$DEV_A" \
    npx playwright test "$@"
) || rc=$?

if podman logs "$APP" 2>&1 | grep -qF "$OWNER_PW"; then
  echo "error: OWNER password appeared in server logs" >&2
  rc=1
fi
[[ $rc == 0 ]] && echo "e2e-dashboard: PASS" || echo "e2e-dashboard: FAIL ($rc)"
exit "$rc"
