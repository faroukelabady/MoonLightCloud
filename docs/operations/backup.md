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
