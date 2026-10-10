# MoonLight Phase 20 Risk Register

Severity: **Critical / High / Medium / Low**. Origin distinguishes a
source-code defect from an external operational prerequisite.

| ID | Severity | Origin | Risk | Likelihood | Impact | Mitigation | Test evidence | Remaining dependency | Owner / action |
|---|---|---|---|---|---|---|---|---|---|
| R-01 | High | Operational prerequisite | No automated off-device Retail backup exists; a stolen workstation loses everything since the last manual backup | medium | catastrophic | Backup tooling + retention + keyring implemented; runbook 3 | backup/restore round-trip tests; live-backup test | **Off-device destination must be provisioned and scheduled by the shop** | Shop owner — provision external backup destination before pilot |
| R-02 | High | Operational prerequisite | `AUTH_MFA_ENCRYPTION_KEY` lost → all MFA ciphertext unreadable | low | major | Fail-closed behavior verified; recovery = new key + per-user MFA reset | config matrix tests; runbook 8 | **Key backup must exist outside the Cloud host** | Cloud operator — store key backup offline before pilot |
| R-03 | High | Operational prerequisite | Release-signing private key lost or compromised | low | catastrophic | Offline ceremony, overlapping key rotation, revocation procedure | runbook 10; Cloud never holds private keys | **Offline key ceremony must be performed; key custody named** | Release owner |
| R-04 | Medium | Source (fixed) | Wrong-Store backup accepted over an existing Store's database | low | catastrophic | Destination guard on immutable `store_identity` UUID (Phase 20) | `TestRestoreRejectsWrongStoreBackup` | none | closed |
| R-05 | Medium | Source (fixed) | Sealed archive restored without a key silently interpreted as plaintext | low | major | Envelope magic detection + fail-closed refusal | `TestSealedRestoreRefusedWithoutKeyring` | none | closed |
| R-05b | High | Source (fixed, R1) | **Cloud restored older than events Retail already acknowledged** — the worker only ever sent `pending` rows, so a Cloud database restored to an older checkpoint stayed silently missing acknowledged Sales/Returns with no detection and no recovery path | low | major | Phase 20 R1 `ReconcileCloud`: retained delivered events are re-queued in bounded batches and resent; Cloud ingest is idempotent so resending is always safe | `TestReconcileCloudRequeuesDelivered`, `TestReconcileCloudBounds`, `TestReconcileCloudRequiresRetainedHistory` | none | closed |
| R-06 | Medium | Operational prerequisite | PostgreSQL PITR unavailable → RPO equals backup interval | medium | major | Documented; scripts produce verified dumps | restore drill PASS | **Provider WAL archiving must be configured for tighter RPO** | Cloud operator |
| R-07 | Medium | Operational prerequisite | Physical hardware (scanner, label printer, Arabic print rendering) untested | high | moderate | — | none | **Physical certification still required** | Shop owner + IT |
| R-08 | Medium | Source (disclosed) | Retail staff permissions are a fixed three-role matrix (admin/manager/cashier); `backup.manage` and `settings.manage` sit with admin | low | major | Documented; no untrusted-staff path to backups | permission matrix in `internal/domain/user/permission.go` | **Shop must decide its staff access policy** | Shop owner |
| R-09 | Low | Source (disclosed) | No human-readable sales/finance export exists (catalog CSV only) | medium | moderate | Disaster-recovery backups restore the full DB; they are not an export | export capability audit | **Export requirement must be decided by owner** | Shop owner |
| R-10 | Low | Source (disclosed) | Single backup copy on the same disk is not disaster recovery | high | catastrophic | Runbook requires an independent copy; scripts warn loudly | `pg-backup.sh` warning; runbook 3.1 | **Second-copy discipline** | Shop owner |
| R-11 | Low | Source (fixed) | Backup encryption added without key management would strand backups | low | major | External keyring, key rotation keeps old backups readable, key ID recorded in manifest | `TestKeyRotationRestoresOldBackup`, `TestKeyringRoundTripAndPermissions` | none | closed |
| R-12 | Low | Operational prerequisite | Cloud TLS certificate expiry | medium | moderate | Renewal runbook (runbook 9) | none | **Certificate renewal automation** | Cloud operator |
| R-13 | Low | Source (disclosed) | Schema downgrade is not universally safe; rollback may require forward remediation | low | major | Documented policy (runbook §42 equivalent) | migration up/down/re-up tests | none | closed |

## Not classified as open risks

The following were verified closed in Phase 20 and are listed only so
the reviewer can confirm they were considered:

- Backup that claims success but cannot restore — backups are verified
  with `pg_restore --list` (Cloud) and full archive validation (Retail)
  before being reported as successful.
- Restore corrupting inventory/Sales/Returns — restore is atomic
  (rename swap) with pre-restore preservation and post-restore
  integrity + stock-vs-ledger reconciliation.
- Cloud restored state fabricating Retail business data — Cloud is a
  projection; restore never writes Retail authority.
- Lost command/result creating duplicate mutation — durable receipts +
  revision fencing (Phase 16/17 invariant, unchanged).
- Cloud silently considers itself synchronized while canonical Sales are
  missing — closed in R1 by the reconciliation resend (R-05b).
- Post-update backup restore erasing newer business writes — automatic
  restore after business use is forbidden; deliberate restore warns.
- Credentials in support bundle — bundle contains no secrets; negative
  tests guard the allowlist.
