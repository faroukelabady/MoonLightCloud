# S03/S04 — pre-production schema cleanup

User explicitly requests both cleanups and confirms there is no production history to migrate. Preserve all earlier uncommitted fixes and current local files. Work stays on the existing fix branches; no commit/push/deployment/live reseed is requested.

- S03: remove only Retail product_images.url and product_variant_images.url, their unused domain fields, handwritten/generated mappings and query parameters. Keep both image tables, ownership, storage keys, metadata, ordering, primary flags, Cloud hashes and R2 behavior. Rename the pre-production SQLite baseline to 100003, update embedded version and schema fixture. Existing older development databases fail the compatibility check and can be recreated explicitly; do not automatically alter or replace them.
- S04: measure representative PostgreSQL plans/index sizes with and without the three prefix indexes in a disposable environment. Remove candidates supported by the measurements, retaining every primary/unique key. Rename the changed Cloud baseline above the previous target (00117); retain the existing report-configuration migration as 00118 and target 118 so its behavior remains unchanged. This avoids unrelated report-configuration rewrites. Old local schemas are not silently accepted.
- Regenerate SQL and reviewed schema fixtures using repository tools. Update current schema docs/ERD guide and the decision review. No schema compatibility bridge or backfill is required during this accepted pre-production window.
- Verify image insert/read/move/delete, local files, catalog bootstrap/media sync, fresh seeds, sales/refunds/reports and recovery on temporary data. Run canonical gates, Retail readiness and an independent schema review. Reuse the final isolated demo/report harness for a fresh-baseline parity proof if required by integration scope.
- Keep the current local DBs/backups unchanged. Report the new fresh-database requirement clearly. Seed data changes are limited to what the new schemas actually require.

## Completed verification

S03 and S04 are implemented. Exact reviewed schema deltas are two Retail URL columns and three Cloud ordinary indexes; all keys and constraints remain. Retail canonical/readiness gates pass. Cloud substantive canonical gates pass through OCI build, then its expected uncommitted-tree guard stops. Fresh cross-app proof passes: 148 events, 37 sales, 38 lines, three refunds, 12 uploaded images and 4,518 matching checks with zero differences. Temporary resources were removed and current app data stayed untouched.

Current report: `../../Guidlines/audit/decision-review-2026-11-10-schema-cleanup.html`; full evidence: `../../Guidlines/audit/schema-cleanup-verification-2026-11-10.md`. Older development databases need explicit recreation for the new baselines. Changes remain uncommitted.
