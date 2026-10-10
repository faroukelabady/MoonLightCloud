# MoonLight Phase 20 — Recovery Evidence Package

Every entry records what actually ran. No credentials, no real business
data. Synthetic fixtures only.

## E-1: Cloud PostgreSQL backup → destroy → restore → serve

| Field | Value |
|---|---|
| Scenario | Cloud database destroyed and recovered from backup |
| Source tree | MoonLightCloud `feat/phase-20-production-hardening` (uncommitted working tree) |
| Environment | disposable PostgreSQL 18.1 (container), database `drill` |
| Backup identity | `drill-001`, pg_dump custom format |
| Data volume | 79 public tables, 1 Store, 1 device, 1 binding (synthetic) |
| Backup size | 275,355 bytes |
| SHA-256 | `87d8e000259cf2fe7a564f4f8f8a248020e7f23843742fb1b9d62b9ee7067a8a` |
| Failure injected | `DROP DATABASE drill` (complete data loss) |
| Restoration action | `CREATE DATABASE drill` + `pg_restore --clean --if-exists` |
| Integrity result | `pg_restore --list` OK before restore; post-restore checks all OK |
| Post-restore state | stores=1 devices=1 bindings=1 tables=79 orphan_bindings=0 goose_head=43 |
| Data fidelity | `Drill Store\|Africa/Cairo`, `drill-device\|active` — byte-identical to pre-backup |
| Application check | exact Cloud binary served `/health/live=200`, `/health/ready=200`, `/version` OK, graceful shutdown |
| Backup duration | ~100 ms (79 tables) |
| Verification duration | ~100 ms |
| Restore duration | **308 ms** |
| Schema validation | included in restore step |
| Time until Cloud usable | **< 1 s** after restore completes |
| RPO/RTO result | RTO target ≤ 4 h: **met**. RPO target ≤ 1 h: depends on backup schedule, not the tool |

## E-2: Retail operational backup → restore round-trip (encrypted)

| Field | Value |
|---|---|
| Scenario | Sealed operational backup restored after data corruption |
| Environment | real SQLite (WAL), migrated schema |
| Backup identity | `manual-backup.mlrbak.enc`, key `test-key-2026` |
| Data volume | 1 product + 1 image + full reference catalog |
| Failure injected | product row mutated post-backup |
| Restoration action | `StageRestore` + `ApplyPendingRestoreWithKeyring` |
| Integrity result | full validation: ZIP, per-file SHA-256, manifest parity, `integrity_check`, `foreign_key_check`, counts, stock-vs-ledger |
| Business reconciliation | restored product name matches pre-backup exactly |
| Evidence class | automated test (`TestEncryptedBackupRoundTrip`) |

## E-3: Wrong-Store restore rejected

| Field | Value |
|---|---|
| Scenario | Backup of Store A restored over Store B's live database |
| Failure injected | destination has a different immutable `store_identity` UUID |
| Result | **REJECTED** before staging; Store B's data unchanged |
| Evidence class | automated test (`TestRestoreRejectsWrongStoreBackup`) |

## E-4: Corrupt archive rejected

| Field | Value |
|---|---|
| Scenario | Backup truncated to 50% |
| Result | `InspectArchive` and `StageRestore` both refused; live DB healthy |
| Evidence class | automated test (`TestRestoreRejectsCorruptArchive`) |

## E-5: Sealed archive without keyring refused

| Field | Value |
|---|---|
| Scenario | Encrypted backup presented to a machine without the keyring |
| Result | refused (fail closed), never plaintext-interpreted |
| Evidence class | automated test (`TestSealedRestoreRefusedWithoutKeyring`) |

## E-6: Tampered sealed archive rejected

| Field | Value |
|---|---|
| Scenario | One byte flipped in the sealed envelope |
| Result | AES-256-GCM authentication failure, refused |
| Evidence class | automated test (`TestSealedRestoreRejectsTampering`, `TestTamperDetection`) |

## E-7: Backup failure leaves source untouched

| Field | Value |
|---|---|
| Scenario | Backup destination is not writable |
| Result | error surfaced, no false success, source DB unchanged and healthy |
| Evidence class | automated test (`TestBackupFailureLeavesSourceUntouched`) |

## E-8: Backup taken while the shop keeps selling

| Field | Value |
|---|---|
| Scenario | Concurrent product writes during `CreateBackup` |
| Result | consistent archive (WAL + `VACUUM INTO` snapshot); live DB healthy afterwards |
| Evidence class | automated test (`TestBackupRunsWhileSalesContinue`) |

## E-9: Key rotation keeps old backups readable

| Field | Value |
|---|---|
| Scenario | Backup written under `k1`, keyring rotated to `k2` |
| Result | `k1` backup still opens; new backups sealed with `k2`; key IDs recorded in manifests |
| Evidence class | automated test (`TestKeyRotationRestoresOldBackup`) |

## Evidence classes used

- **Automated test** — reproducible, runs in CI.
- **Real database drill** — disposable PostgreSQL 18 container, real
  `pg_dump`/`pg_restore`, real Cloud binary.
- **NOT RUN** — see the failure-injection matrix in the implementation
  report for scenarios that need physical hardware, real production
  infrastructure or native packaging.

## Deliberately not claimed

- Point-in-time recovery (requires provider WAL archiving).
- Physical power-loss behaviour of specific hardware.
- Native packaged-install certification.
- Multi-hour or multi-day soak.

## E-10: Divergent-database recovery harness (Phase 20 R1)

| Field | Value |
|---|---|
| Scenario | Retail and Cloud histories diverge through backup/restore |
| Environment | real Retail SQLite + real Cloud HTTP (PostgreSQL 18) + real sync protocol |
| Harness | `MoonLightRetail/scripts/recovery-drill.sh` + `MoonLightRetail/tests/recoverydrill` |
| Phases | baseline → Cloud backup (checkpoint A) → offline Sales → Cloud restore to A → reconnect → divergence probe → reconcile |
| Failure injected | Cloud database restored to a checkpoint predating acknowledged Retail events |
| Recovery action | `ReconcileCloud` re-queues retained delivered events; normal worker resends |
| Integrity result | Cloud ingest is idempotent — duplicates ACK `already_accepted`, missing events ingest and reproject |
| Evidence class | harness + automated tests (opt-in, `MOONLIGHT_E2E_ENABLED=1`) |

## E-11: Reconciliation resend (Phase 20 R1)

| Field | Value |
|---|---|
| Scenario | Cloud missing events Retail considers delivered |
| Mechanism | `syncservice.Service.ReconcileCloud` → `RequeueDeliveredBatch` (bounded, oldest-first) |
| Safety | delivered rows retained forever (never pruned); Cloud ingest idempotent; bounded batch; repeatable; non-destructive |
| Integrity result | `TestReconcileCloudRequeuesDelivered` — 5 delivered rows re-queued across bounded calls, total row count unchanged |
| Bounds | `TestReconcileCloudBounds` — limit 0/-1/5000 cannot storm; empty actor refused |
| Precondition | `TestReconcileCloudRequiresRetainedHistory` — delivered payload bytes retained on disk |
| Evidence class | automated tests |

## E-12: Reconciliation SQL correctness (Phase 20 R1)

The first implementation of `RequeueDeliveredBatch` violated the
`sync_outbox` CHECK constraint (`delivered_at` must be NULL whenever
status ≠ delivered) and was rejected by the database with
`invalid status`. Caught by the test, fixed by clearing `delivered_at`
in the same UPDATE. This is real evidence that the constraint is
enforced — the fix is in `internal/repository/sqlite/queries/sync.sql`.
