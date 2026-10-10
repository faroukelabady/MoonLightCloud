# MoonLight Production Threat & Failure Model (Phase 20)

Scope: MoonLightRetail (shop workstation, authoritative business data)
and MoonLightCloud (projection, control, analytics, fleet). This is the
analysis Phase 20 recovery work is ranked against. A scenario is
"covered" only where a tested mechanism exists — exception handling
alone is not coverage.

## Severity scale

- **Impact**: catastrophic (business cannot trade or has lost financial
  truth) / major (significant recovery effort) / moderate / minor.
- **Likelihood**: high / medium / low over a multi-year shop life.
- **Recoverable** means: tested procedure exists AND prerequisites are
  provisioned. Anything else is an open dependency.

## Threat and failure register

| ID | Scenario | Impact | Likelihood | Recovery owner | Required resources | Status |
|---|---|---|---|---|---|---|
| T-01 | Retail workstation lost/stolen/damaged/replaced | catastrophic | medium | Shop owner + IT | verified Retail install, operational backup, external backup copy, Cloud enrollment | Recovery procedure documented + rehearsed (see runbook 6). Depends on an off-device backup existing at loss time. |
| T-02 | Power failure during SQLite writes | major | high | Shop owner | none (self-recover) | Covered: WAL + `synchronous(FULL)` + `BEGIN IMMEDIATE` + atomic outbox. No partial committed mutation. |
| T-03 | Local disk corruption | catastrophic | low | Shop owner + IT | operational backup, integrity check | Covered: `PRAGMA integrity_check` diagnostics; restore refuses corrupt archives. |
| T-04 | Disk full | major | medium | Shop owner | disk headroom | Covered: controlled refusal (`ErrStorageFull`), no partial write. Low-space pre-flight is an operational prerequisite. |
| T-05 | Failed backup | major | medium | Shop owner | working destination | Covered: backup fails loudly, source untouched (tested). |
| T-06 | Corrupt/tampered backup | catastrophic | low | Shop owner | a good backup | Covered: SHA-256 manifest + GCM authentication + ZIP validation; restore refuses (tested). |
| T-07 | Missing backup | catastrophic | medium | Shop owner | discipline | **OPEN**: no automated off-device backup exists. Owner must adopt the schedule. |
| T-08 | Cloud PostgreSQL corruption/loss | major | low | Cloud operator | pg backup, restore runbook | Restore drill PASS. PITR depends on provider WAL archiving (see §17). |
| T-09 | Cloud container/server failure | moderate | medium | Cloud operator | OCI image, config, secrets | Covered: stateless container + external PostgreSQL; restart policy documented. |
| T-10 | Prolonged Internet disconnection | moderate | high | Shop owner | none | Covered: Retail is offline-first; POS, inventory, returns all work without Cloud. |
| T-11 | Expired TLS certificate | moderate | medium | Cloud operator | cert renewal runbook | Runbook 9 covers renewal. Not exercised here (external infra). |
| T-12 | Compromised device credentials | major | low | Cloud operator | CLI revoke + re-enroll | Covered: revoke is immediate; re-enrollment issues a new credential. |
| T-13 | Compromised OWNER account | catastrophic | low | Cloud operator | CLI `auth reset-password`, audit | Covered: server-side recovery revokes all sessions. No public reset bypass exists. |
| T-14 | Lost MFA device / recovery codes | major | medium | Cloud operator | CLI recovery | Covered: `auth reset-password --reset-mfa`. |
| T-15 | Lost `AUTH_MFA_ENCRYPTION_KEY` | major | low | Cloud operator | key backup | **OPEN dependency**: key must be backed up externally. Recovery = new key + per-user MFA reset (fail-closed, never silent disable). |
| T-16 | Compromised/lost release-signing key | catastrophic | low | Release owner | offline key ceremony, key rotation | Runbook 10. Private key must never enter any build. Cloud cannot redefine Retail trust. |
| T-17 | Failed application update | major | medium | none (self) | pre-update backup | Covered: safe-boundary rollback (Phase 18). Automatic restore after business use is forbidden. |
| T-18 | Failed database migration | major | low | Cloud/Retail operator | pre-migration backup | Covered: pre-migration backup kept; forward remediation preferred over down migrations. |
| T-19 | Operator mistake / accidental deletion | major | medium | Shop owner | operational backup | Covered by restore, subject to backup existing (T-07). |
| T-20 | Cloud restored older than Retail devices | major | low | Cloud operator | reconciliation runbook | Retail authority preserved; sync replays durable outbox. Cases needing human review surface as recovery-blocked (runbook 5). |

## What this model deliberately does NOT claim

- Physical power-loss resilience of the specific workstation/storage
  hardware (never tested).
- Multi-day peak load or unlimited Store scale (Phase 19 envelope only).
- All operating systems (see supported-platform section of the
  installation runbook).
- That a backup exists just because the software can make one.

## Recovery ownership summary

| Function | Owner |
|---|---|
| Retail backup schedule + off-device copy | Shop owner |
| Retail restore / workstation replacement | Shop owner + IT |
| Cloud database backup + restore | Cloud operator |
| OWNER/MFA recovery | Cloud operator (CLI, audited) |
| Device credential revocation | Cloud operator |
| Release signing key | Release owner (offline, air-gapped) |
| Incident diagnosis | Named incident owner (Phase 21 prerequisite) |
