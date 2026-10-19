# MoonLightCloud — architecture overview

Go modular monolith with PostgreSQL 18 as its durable state. Retail and Cloud
have separate domain models and exchange authenticated HTTPS contracts.

```text
Retail: ProductType → Product → physical Variant (SKU and stock)
  canonical Sales/Returns/history + retained transactional outbox
                  │ Retail-initiated HTTPS sync and polling
                  ▼
Cloud: ingestion → durable inbox → projections → reports/dashboard
       human auth → Store-scoped Admin intent → Retail receipts/convergence
       commerce adapters → Shopify/WooCommerce
       notification adapters, device control, signed releases/fleet recovery
                  │
                  ▼
              PostgreSQL 18
```

## Ownership and boundaries

Retail owns ProductTypes, Products, Variants, exact SKU/inventory, Sales,
Returns and frozen business snapshots. Cloud projects those events and never
repairs history by inventing Retail transactions. Product stock is derived from complete active Variant inventory;
FrameConfigurations have no SKU or stock. Cloud owns provider mappings,
online-order integration, human authentication, commands and operational state.

Core packages define contracts and policy; HTTP and PostgreSQL adapters own
I/O. Provider-specific HTTP belongs in adapters, not core business services.
`internal/app` composes the process; CLI entry points must preserve the same
Store, mutation-barrier and timeout contracts. Known boundary exceptions and
incremental extraction work are tracked in the architecture refactoring plan.
There is no common Retail/Cloud business-model module.

PostgreSQL persistence is being separated into bounded adapters sharing the same
application-owned pool and query timeout. `SyncInbox` owns ingestion,
`SyncRecovery` the recovery ledger, `Reporting` financial/dashboard reads,
`Notifications` mappings/outbox/delivery state, `DeviceControl` presence and
remote-sync commands, and `BusinessReports` schedules/runs/delivery snapshots.
`Operations` owns incident/recovery policies and state, `StoreRegistry` proven
Store registration/bindings, `CatalogAdmin` durable intent/targets, and
`CommerceOrders` webhook fencing and order projection/history. `SaleProjection`
and `ReturnProjection` own their financial projection transactions and permanent
ownership arbitration; `ProjectionState` owns shared diagnostics/manual retry SQL.
`CatalogProjection` owns current catalog reads and structural projections,
including their transaction-local commerce queue writes. Commerce orchestration
receives catalog reads and provider mappings as separate collaborators.
The dashboard depends only on the diagnostic reader, not projection mutation.
Each type satisfies its domain store contracts directly. The remaining
`Devices` families are still being extracted; these types do not embed that
universal repository. Cross-table atomic operations remain in one transaction.

Reporting and dashboard services select a scope-bound Reader once per
operation, after HTTP permissions and Store membership checks. Global reads
aggregate across Stores; they do not depend on legacy/unowned rows existing.
A selected Store never falls back to global. The existing global/ForStore SQL
methods remain explicit adapter/tooling compatibility surfaces; readers do not
construct dynamic accounting queries. Global processing freshness and already
scope-aware operational analytics remain separate.

## Ingestion, projection and recovery

`internal/sync` validates envelopes and exact canonical JSON, commits immutable
`sync_events` and ACKs durable acceptance. Projection is separate: processing
state records pending/retry/blocked/processed outcomes. Durable Sale ownership
survives projection rebuilds; current catalog revisions and historical Sale
snapshots have different lifecycles. ACK is never proof of projection completion.

The current Sale stream is `sale.finalized.v3`; Category and Product snapshots
use v2. Other supported streams and their limits are defined by
`api/openapi.yaml` and `docs/sync/protocol.md`. During the accepted ADR-0055
pre-production window, paired contracts change in place and obsolete versions
are removed. After the first production install, normal compatibility and
append-only migration rules resume.

Recovery verifies retained Retail event identity and missing-event replay,
then requires projection convergence before reporting reconciliation. Cloud
backup artifacts require the documented external confidentiality controls.
Operational DR and Retail updater rollback remain separate mechanisms.

## Security and HTTP

Public health/version endpoints do not grant business access. The Dashboard
uses OWNER/ADMIN human sessions, mandatory MFA, Secure/HttpOnly/Strict cookies,
CSRF/Origin checks, named permissions and Store membership. Device endpoints
use existing device credentials and server-derived Store binding; human and
device credentials are not interchangeable. Release-signing private keys stay
outside Cloud. Logs and bounded public errors must exclude secrets and SQL.

Exact minor units use checked integers; currencies remain separate and FX
reporting uses the frozen integer microrate. Current labels never supply
historical Sale attribution. Domain IDs are UUIDs and timestamps are UTC.

## Runtime and schema

Local development uses the committed compose/scripts. Deployment uses the OCI
image with PostgreSQL, stdout logs and bounded shutdown. No Redis, broker or
microservices are required. Startup verifies the schema; migrations are an
explicit operational action.

ADR-0055 replaces the old development migration chain with a guarded baseline.
Each model checkpoint raises its file/version and refuses an older development
database. `internal/migrate.TargetVersion` is authoritative for the current
checkpoint. The paired Retail `plan/database-baseline-plan.md` records the
completed data-model checkpoints and their verification, separately from manual
packaged-desktop acceptance. Cloud derives Product stock from the complete
active Variant inventory set and has no Product-level inventory writer.

See `docs/operations/disaster-recovery.md`, `docs/security/threat-model.md`,
`docs/operations/railway.md`, and the accepted ADRs for deployment requirements.
