# ADRs

Concise, irreversible-or-important choices only.

- [0001](0001-separate-cloud-repo.md) — separate MoonLightCloud repository
- [0002](0002-modular-monolith.md) — modular monolith over microservices
- [0003](0003-postgresql.md) — PostgreSQL as the only state
- [0004](0004-oci-dev.md) — Podman/Docker-compatible OCI development
- [0005](0005-pgx-sqlc-no-orm.md) — pgx + sqlc, no ORM
- [0006](0006-versioned-api-boundary.md) — versioned API boundary with MoonLightRetail
- [0007](0007-commerce-adapter.md) — external commerce behind an adapter
- [0008](0008-notification-adapter.md) — notification provider behind an adapter
- [0009](0009-no-shared-domain-package.md) — no shared desktop/cloud domain package
- [0010](0010-railway-is-target.md) — Railway is a deployment target, not a dependency
- [0011](0011-device-secret-hashing.md) — device secret hashing design (superseded by 0015)
- [0012](0012-explicit-migrations.md) — explicit migrations, verified schema at startup
- [0013](0013-device-credential-lifecycle.md) — device credential lifecycle
- [0014](0014-sync-ingestion-protocol.md) — sync ingestion protocol
- [0015](0015-device-secret-hmac.md) — final HMAC construction (supersedes 0011)
- [0016](0016-async-sale-projection.md) — durable inbox + asynchronous projection
- [0017](0017-canonical-hash-compat.md) — exact canonical JSON + payload-hash compatibility (compat rule superseded by 0019)
- [0018](0018-durable-sale-ownership.md) — durable Sale ownership arbitration
- [0019](0019-fail-closed-legacy-hash.md) — fail-closed legacy payload-hash policy
- [0020](0020-reporting-time-semantics.md) — sales reporting and store-timezone semantics
- [0021](0021-reporting-audit-remediation.md) — sales reporting audit remediation (H1/H2/H3/H4/M1/L1)
- [0022](0022-breakdown-dto-split.md) — final breakdown DTO split (M2) and category ordering (L2)
- [0023](0023-cloud-dashboard.md) — cloud reporting dashboard architecture
- [0024](0024-dashboard-review-remediation.md) — dashboard review remediation (3B-R)
- [0025](0025-dashboard-freeze-remediation.md) — dashboard freeze remediation (3B-R2)
- [0026](0026-return-refund-sync.md) — returns & refunds sync contract (4A)
- [0027](0027-return-projection-reporting.md) — returns/refunds projection & reporting integration (4B)
- [0028](0028-catalog-sync-projection.md) — product catalog sync projection (5A)
- [0029](0029-sales-policy-projection.md) — product sales-policy projection (5B)
- [0030](0030-inventory-availability-projection.md) — product inventory projection and availability (5C)
- [0031](0031-commerce-provider-abstraction.md) — commerce provider abstraction (6A)
- [0032](0032-woocommerce-adapter.md) — WooCommerce adapter (6B)
- [0033](0033-online-order-ingestion.md) — Online order ingestion (6C)
- [0034](0034-notification-provider-whatsapp.md) — notification provider & WhatsApp delivery (7A)
- [0035](0035-scheduled-business-reports.md) — scheduled business reports (7B)
- [0036](0036-device-connectivity-remote-sync.md) — device connectivity & remote sync (7C)
- [0037](0037-operational-alerts-self-healing.md) — operational alerts & bounded self-healing (7D)
- [0038](0038-historical-sale-tag-snapshots.md) — historical sale tag snapshots, v2 projection (8C)
- [0039](0039-cloud-store-registry.md) — Cloud store registry & ingress store context (9A)
- [0040](0040-store-scoped-projections.md) — Store-scoped projections & ownership isolation (9B)
- [0041](0041-store-scoped-commerce.md) — Store-scoped commerce ownership & order isolation (9C)
- [0042](0042-shared-default-catalog-identity.md) — shared default catalog identity & adoption dependency integrity (9-R1)
- [0043](0043-telegram-notification-adapter.md) — Telegram notification adapter as second NotificationProvider (10)
- [0044](0044-shopify-commerce-adapter.md) — Shopify commerce adapter & online order integration (11)
- [0045](0045-analytics-catalog-health.md) — analytics, catalog health & reporting closure (12)
- [0046](0046-category-online-visibility.md) — hierarchical online catalog visibility (13)
- [0047](0047-product-options-frame-configurations.md) — product options (frame configurations) projection & publication (15)
- [0048](0048-cloud-admin-catalog-control.md) — Cloud admin catalog control plane (16)
- [0049](0049-product-physical-variant-architecture.md) — product & physical variant architecture (17)
- [0050](0050-product-type-projection-admin.md) — ProductType projection & admin control (17-R2)
- [0051](0051-signed-release-registry.md) — signed release registry (18)
- [0052](0052-fleet-rollout-device-update-control.md) — fleet rollout & device update control (18)
- [0053](0053-human-authentication-authorization.md) — human authentication & authorization: OWNER/ADMIN, MFA, Store memberships (18 R1)

## Phase numbering gaps

The ADR sequence is not one-per-phase. These are intentional, not missing files:

- **Phase 8A / 8B** — Retail-authoritative (MoonLightRetail `ADR-029`,
  `ADR-030`). Cloud absorbs the projected state through the Phase 5A/5B
  projections instead of owning an ADR.
- **Phase 14** — Retail-only. Barcode labels and HID scanner checkout input
  have no Cloud surface (MoonLightRetail `ADR-034`, `ADR-035`).
- **Phase 9D** — dashboard scope selection; shipped without its own ADR
  (commit `408f388`), covered by ADR-0039 store registry intent.
