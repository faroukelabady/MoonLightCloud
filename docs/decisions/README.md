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
