# Production Backup Model (PostgreSQL)

Phase 1A needs no full DR solution, but production will run all four layers.
Development volumes are not backups.

1. **Provider snapshot / PITR** where supported (e.g. Railway/Neon/Render/RDS
   automated backups). First line of recovery.
2. **Logical `pg_dump`** on a schedule (custom format + schema-only copy),
   so restores do not depend on one provider's snapshot format.
3. **Off-provider copy.** Dump artifacts replicated to independent storage
   (different account/region) with retention and encryption at rest.
4. **Periodic restore test.** A restore drill against an isolated database
   that runs migrations verification + `/health/ready` before sign-off.
   An untested backup is not a backup.

## Non-rebuildable state

Most Cloud tables are derived projections rebuildable from the durable
`sync_events` inbox (catalog, policy, inventory, sale/return
projections). `commerce_product_mappings` (Phase 6A) is the exception:
provider↔MoonLight external identities cannot be reconstructed from
MoonLight events and must survive every projection rebuild. Backup and
restore verification must cover this table explicitly. Phase 6C adds
the same durability class: `commerce_online_orders*` tables, the
webhook inbox, and the reconciliation fence table are provider-derived
or operational integration state, never cleared by projection
rebuilds, and covered by normal database backup.

## Local development

Named volume `moonlightcloud_pgdata` persists across `dev-down`/`dev-up`.
`dev-reset.sh` destroys it deliberately (development only, hard-gated).
For a quick logical snapshot during development:

```bash
podman exec moonlightcloud-postgres-1 pg_dump -U moonlight moonlight_dev > /tmp/dev.dump
```

The Compose volume mounts the image-recommended parent path
(`/var/lib/postgresql`); the postgres:18 image keeps versioned PGDATA
below it. Never mount `.../data` directly.

## Commerce mutation barriers (schema 27)

`commerce_product_mutation_barriers` stores durable uncertain Shopify request
evidence and confirmed operator-resolution history. Include it in database
backups and restores. It is operational state, not a rebuildable projection.
Do not purge, reset, or restore it independently of the associated commerce
state: erasing an active barrier can authorize a newer write while an older
remote request is still pending. Successful acknowledged requests remove their
active row; resolved uncertainty retains audit history. No retention job is added.
