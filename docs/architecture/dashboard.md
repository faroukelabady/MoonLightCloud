# Dashboard Development Guide

## Approved visual principles

- Arabic primary, English muted sublabel directly below (sidebar, cards,
  charts, filters, metrics, buttons).
- `dir="rtl" lang="ar"` shell; numbers, currency codes, dates, chart axes,
  and order IDs stay LTR (`.num`, tabular numerals).
- Right full-height navy sidebar (`#101c38`), sticky desktop, stacked mobile.
- White flat cards, simple 1px borders, 8px radius (`--radius-card`);
  no pill containers, no decorative border complexity.
- Blue primary actions (`#1d4ed8`), green healthy accents, muted helpers.
- Combined Total Sales card with All/EGP/USD tabs; All normalizes USD via
  each sale's historical FX (server-side, exact integer math).
- Single Top Products component (table/chart toggle); Donut-default
  category chart (Pie/Bar toggle) with the subcategory facet note.
- Detailed sync-health card with honest Cloud-only wording; recent
  activity from real inbox/processing rows with render-time relative ages;
  latest finalized sales (never fabricated order lifecycle).

## Prerequisites

- Go toolchain (repo-pinned), Podman/Docker, Node 22 + npm (pinned lockfile).
- PostgreSQL 18 via `deploy/compose.yaml` (`./scripts/dev-up.sh`).

## Frontend workflows

```bash
npm --prefix dashboard ci      # locked install
npm --prefix dashboard run dev # Vite dev server :5173 (proxies /api to :8080)
npm --prefix dashboard run typecheck
npm --prefix dashboard test    # vitest unit + component
npm --prefix dashboard run build        # dist/ for Go serving
npm --prefix dashboard run test:e2e     # needs dev stack (see below)
```

Full-stack local loop: `./scripts/dev-up.sh` (builds dashboard `dist/`?
No — dev serves Go from source; build `dashboard/dist` once with
`npm --prefix dashboard run build`, then `go run ./cmd/moonlight-cloud`
serves it at `/dashboard`). For hot frontend iteration use Vite dev
(`:5173`, API proxied to Go on `:8080`).

## Dashboard accounts (development)

There is no default account (ADR-0053). Create the first OWNER once per
database, then sign in and enroll TOTP MFA in the browser:

```bash
go run ./cmd/moonlight-cloud auth bootstrap-owner --login dev@example.test --display-name "Dev Owner"
```

The password is read from the terminal without echo (or `--password-stdin`).
Local scripts generate `AUTH_MFA_ENCRYPTION_KEY` into `.env.local`. Never
commit passwords, TOTP secrets or recovery codes; never log them.

## E2E

```bash
npx --prefix dashboard playwright install chromium
./scripts/e2e-dashboard.sh
```

E2E runs against a throwaway production-mode Cloud behind local HTTPS with
explicit OWNER/ADMIN accounts and test-owned data; see
`docs/operations/dashboard.md` (Browser E2E test environment).

## Conventions

- Arabic primary, English muted sublabel; `dir="rtl" lang="ar"` shell;
  numbers/currency/codes stay LTR (`.num` class).
- Money: server strings (`amount_minor`), formatted with BigInt only
  (`src/lib/money.ts`); charts use `toChartNumber` safe-range conversion.
- One typed client (`src/lib/api.ts`); AbortController per filter change;
  period/currency in URL query.
- Component-scoped styles + CSS tokens in `src/app.css`; radius 8px;
  no CSS framework, no CDN, no analytics.
