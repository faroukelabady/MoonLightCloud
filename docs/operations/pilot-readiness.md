# MoonLight Pilot Readiness (Phase 20 → Phase 21)

A completed Phase 20 code freeze does **not** mean these are satisfied.
Every item needs evidence, not intention. `NOT RUN` means it has not
been proven, not that it is unimportant.

## Checklist

| # | Prerequisite | State | Evidence / what is missing |
|---|---|---|---|
| 1 | Verified backup destination (off-device) | **NOT SATISFIED** | Tooling exists (`pg-backup.sh`, Retail `recoverytool backup`) and a supervised schedule is provided (`deploy/systemd/moonlight-retail-backup.{service,timer}`). No real off-device destination has been provisioned. |
| 2 | Completed Retail restore drill | SATISFIED (code) | `TestEncryptedBackupRoundTrip`, `TestRestoreRejectsWrongStoreBackup`, `TestBackupRunsWhileSalesContinue`, live-workstation drill still recommended. |
| 2b | Divergent-DB recovery drill (Cloud older than Retail) | SATISFIED (harness) | `scripts/recovery-drill.sh` + `tests/recoverydrill` drive real SQLite + real Cloud HTTP + real backup/restore. Scenarios A–D. |
| 2c | Reconciliation after Cloud restore | SATISFIED (code) | `ReconcileCloud` re-queues retained delivered events; `TestReconcileCloud*` prove bounded, repeatable, non-destructive resend. |
| 3 | Completed PostgreSQL restore drill | SATISFIED | Real drill run in Phase 20: backup → destroy → restore → validate → exact Cloud binary served. |
| 4 | OWNER MFA recovery tested | SATISFIED (procedure) | CLI `auth reset-password --reset-mfa` verified by tests; runbook 8. Live OWNER exercise recommended. |
| 5 | Production auth secrets provisioned | **NOT SATISFIED** | `DEVICE_SECRET_PEPPER`, `AUTH_MFA_ENCRYPTION_KEY`, DB credentials, `REPORTING_API_TOKEN` must be generated and stored offline. |
| 6 | Release signing trust provisioned | **NOT SATISFIED** | Offline Ed25519 ceremony not performed; `RELEASE_TRUSTED_PUBLIC_KEYS` not populated. |
| 7 | Supported Retail package verified | **NOT SATISFIED** | Native packaged install not certified in this environment. |
| 8 | Store/device enrollment ready | PARTIAL | Procedure documented (runbook 6); first live enrollment not performed. |
| 9 | Actual scanner/printer identified | **NOT RUN** | Physical hardware never tested. |
| 10 | First catalog import ready | PARTIAL | Bulk catalog CSV import exists and round-trips; the shop's actual opening catalog not prepared. |
| 11 | Stock-opening procedure ready | PARTIAL | Inventory receive/adjust/damage exist; the shop's opening count procedure not agreed. |
| 12 | Staff access policy decided | **NOT SATISFIED** | Roles are admin/manager/cashier. Shop must decide who holds `backup.manage`/`settings.manage`/`users.manage`. |
| 13 | Cloud TLS/domain ready | **NOT RUN** | External infrastructure. |
| 14 | Alert recipient decided | **NOT SATISFIED** | Operational alerting surfaces exist (Phase 7D); who receives them is undecided. |
| 15 | Incident recovery owner named | **NOT SATISFIED** | Threat model names functions, not people. |
| 16 | RPO/RTO accepted | PARTIAL | Targets proposed; achieved values measured for the drills. Owner must accept that RPO equals the backup interval without PITR. |

## Decision rule

Phase 21 (pilot) may begin only when every row is SATISFIED **or** the
owner explicitly accepts the residual risk in writing. Items 1, 5, 6,
12, 15 are blockers in practice: a pilot without an off-device backup
destination or production secrets cannot be operated safely.

## Rollback decision

Rolling back the pilot means: stop new Sales in Retail (or run in
paper-fallback), keep the database, and decide whether to restore a
backup or continue from current state. It does **not** mean reverting
schema — prefer forward remediation. Automatic pre-update database
restore is forbidden after business operations have been enabled.
