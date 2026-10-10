# ADR-0054: Cloud Restore Divergence, Durable Recovery State and Reconciliation Protocol (Phase 20)

- Status: Accepted (Phase 20).
- Relates to: ADR-0014 (sync ingestion protocol), ADR-0017 (canonical
  JSON + payload-hash compatibility, compat rule superseded by 0019),
  ADR-0019 (fail-closed legacy payload-hash policy), ADR-0039 (Cloud
  store registry & ingress store context), ADR-0040 (Store-scoped
  projections and ownership isolation). None is rewritten.
- Scope: what a PostgreSQL restore does to Cloud's business-projection
  state, the durable per-device recovery marker, the device-authenticated
  verification/completion protocol, and the exact conditions under which a
  Store may be reported reconciled. It does **not** change ingest,
  projection or Store-ownership semantics.

## Context

Cloud is projection state. Its `sync_events` and projection tables are
derived from events that Retail (the sole authority for Sales, Returns and
inventory) delivered. Ingest is acknowledged durably and Retail then marks
its outbox row `delivered`.

A PostgreSQL restore can place Cloud in a state that is **structurally
healthy but historically incomplete**:

- `/health/ready` returns 200 — the process and database are fine;
- every projection is internally consistent;
- yet events Retail was told were accepted are **absent**.

Before Phase 20 nothing detected this. The Phase 19 reconciliation
primitive existed but had no trigger and no reachable call path, so a
restore that lost acknowledged events lost them silently and permanently.

## Decision

### 1. A Cloud restore is a potentially divergent business-projection state

A restored database is never assumed to be business-complete. The
operational fact is split in two and reported separately:

```text
DATABASE_RESTORED   — infrastructure restored; Cloud serves traffic.
STORE_RECONCILED    — business history verified and projections converged.
```

`DATABASE_RESTORED` is never a synonym for `STORE_RECONCILED`. The
supported restore workflow prints both concepts and refuses to conflate
them.

### 2. Migration `00044_sync_recovery_state` (additive)

One new table, no alteration of any existing table (frozen migrations
`≤ 43` are untouched):

```text
sync_recovery_state
  device_id              PK → devices(id) ON DELETE CASCADE
  required               bool        durable recovery flag
  reason                 text ≤ 200
  started_at, cleared_at
  verified_events        bigint
  updated_at
  -- verification coverage ledger (see §7)
  verify_generation      bigint
  verify_last_event_id   text ≤ 64
  verify_pages           int
  verify_events          bigint
  verify_complete        bool
  verify_declared_total  bigint
```

Per-device, not per-Store: one Store with several devices cannot be
reported reconciled while one device is still outstanding. Being a table
rather than memory, the state survives Cloud restarts.

### 3. Recovery-required marking through the supported restore procedure

`scripts/pg-restore.sh` marks every device `required = TRUE` with
`reason = 'cloud database restored from backup'`, using
`INSERT … ON CONFLICT (device_id) DO UPDATE`. The marking step **fails
loudly** (`die`) if the device list cannot be read, if any per-device
write fails, or if the number marked is lower than the number processed.
A restore that cannot mark devices does not announce success.

**Proven:** marking is durable and survives Cloud restart; a failed
marking step aborts the script.

**Not proven / explicitly out of scope — do not assume:** *arbitrary*
PostgreSQL restores are **not** automatically detected. Detection depends
on running the supported restore workflow. A restore performed by other
means (manual `pg_restore`, `pg_basebackup`, a managed-service PITR,
replica promotion, a vendor snapshot restore) will **not** set the
marker. Such a restore is indistinguishable from a healthy Cloud to this
mechanism. The runbook states this limitation explicitly; no claim of
general restore detection is made.

### 4. Device-authenticated recovery status, verification and completion

Three endpoints, all gated by ordinary device authentication (`DeviceAuth`)
and scoped to the calling device:

```text
GET  /api/v1/device-control/recovery/status
POST /api/v1/device-control/recovery/verify
POST /api/v1/device-control/recovery/complete
```

A device can only read and clear **its own** recovery state. There is no
cross-device or cross-Store access. Human/dashboard authentication is a
separate identity (ADR-0053) and cannot substitute for device auth here.

### 5. Identity comparison by `(event_id, payload_hash)`

Continuity is never compared by count. Two histories are equal only when
every identity is present:

```text
event_id      — the canonical immutable event identity
payload_hash  — sha256(canonical JSON payload bytes)
```

An `event_id` present in Cloud with a **different** hash is a content
conflict and is reported as missing. It is never treated as a match, and
the stored row is never rewritten. Same-ID-different-content is therefore
fail-closed.

### 6. Canonical JSON hashing compatibility with Retail

`sync_events.payload_hash` is `sha256(canonicalJSON(payload))` where the
canonical form is `encoding/json` output of a `UseNumber`-decoded value:
object keys sorted, insignificant whitespace removed, numbers normalised
exactly in decimal (no `float64` round-trip; hostile exponents rejected as
malformed before ACK).

Retail must hash the **same** canonical bytes. Hashing the raw stored
`payload_json` would never match. This compatibility is a live contract
(ADR-0017), and hash-version policy for legacy rows is fail-closed
(ADR-0019).

> **Proven:** an independently recomputed Retail-side canonical hash
> equals Cloud's stored `payload_hash` for the same event. Verified
> against a live Cloud database during Phase 20 acceptance.

### 7. Server-side verification progress and completion constraints

Cloud owns the completeness proof. The coverage ledger records, per
verification generation:

- the page cursor `verify_last_event_id` — a page must **advance** it, so
  a skipped or replayed page is refused (`UPDATE … WHERE
  verify_last_event_id < $2` affecting zero rows);
- `verify_pages` / `verify_events` — cumulative coverage;
- `verify_complete` — set **only** by a page submitted as
  `end_of_history`;
- `verify_declared_total` — the retained-history size the device commits
  to walking, supplied when the pass opens.

`Complete` refuses unless **all** of:

```text
last_missing == 0                       (no identities still missing)
progress exists and pages > 0           (never a zero-work shortcut)
verify_complete == true                 (history walked to the end)
verify_declared_total != 0              (a total was declared)
verify_events >= verify_declared_total  (coverage reached the total)
```

**Proven:** a client cannot clear recovery by claiming `missing=0`
without a coverage walk; cannot verify one page of many and complete; a
skipped page is refused; a replayed page is refused; a restart resumes
and completes only after a full walk.

**Trust boundary — explicitly not proven:** `verify_declared_total` is
**client-supplied**. Cloud cannot independently know how many events the
device retains, because retained history lives in Retail's own database.
An authenticated device that declares a total **smaller** than its true
retained history, walks only that many events and submits
`end_of_history`, satisfies every constraint above and completes. The
constraints prove *consistency with the declaration*; they do not prove
the declaration is truthful.

The honest Retail client sends `CountRetained()` (its true retained row
count), so the supported path is sound. The residual risk is a
compromised or defective device deliberately under-declaring. See
"Rejected alternatives" for the smallest server-side hardening that
remains open.

### 8. Multiple-device recovery requirements

- recovery state is per-device;
- `devices_unresolved` counts devices of the Store still marked required;
- a Store is **never** reported reconciled while `devices_unresolved > 0`.

Consequently a device completing its own verification does not mark the
Store reconciled. An offline, revoked, or lost device keeps the Store
unreconciled until an operator resolves it.

**Not proven:** behaviour when a device identity existed before the
backup but is absent from the restored snapshot (the identity is lost with
the snapshot). In that case the device cannot be marked because it no
longer exists in `devices`. Re-enrolment or manual reconciliation is
required and is documented as a manual step; no automatic handling is
claimed.

### 9. Ingest acceptance is not projection completion

Receiving and acknowledging an event is **not** the same as projecting it.
Event processors record their own state in `sync_event_processing`
(`processed`, `retry`, `blocked`). `projections_pending` counts rows still
`retry` or `blocked`.

Recovery verification therefore proves **event history**, not business
projection convergence. The two are reported separately:

```text
EVENT_HISTORY_VERIFIED  — every retained identity present in Cloud
REPLAY_COMPLETE         — missing events requeued and accepted
PROJECTION_PENDING      — processors still retrying/blocked
STORE_RECONCILED        — see §10
```

**Proven:** reconciliation can report event-history completion while
projections are still pending, and the report says so honestly rather than
claiming business success.

### 10. Conditions for reporting a Store as reconciled

```text
store_reconciled = (devices_unresolved == 0) && (projections_pending == 0)
```

Only when no device is outstanding **and** no projection is pending.
Never inferred from event-history verification alone, never from
`/health/ready`, never from a non-zero delivered count.

**Limitation:** `projections_pending` is currently computed globally, not
per-Store. In a multi-Store deployment a pending projection for one Store
will suppress `store_reconciled` for all Stores. This is conservative
(never falsely positive) but imprecise.

### 11. Replay idempotency and Store/device authorization

- Ingest is idempotent: a re-sent event already held returns
  `already_accepted`, never a second business effect. Replay is therefore
  safe and repeatable, and bounded over-requeue is harmless.
- `InsertSyncEvent` uses `ON CONFLICT (event_id) DO NOTHING`; the stored
  immutable payload is the duplicate proof (ADR-0019).
- Ingress Store context is server-derived from the authenticated device's
  binding (ADR-0039). It is never taken from the payload. Replays leave
  the originally snapshotted row untouched.
- Store and device authorization is unchanged: a device can only ingest
  for its own Store and only manage its own recovery state.

### 12. Restore, interruption and recovery-failure behaviour

- Recovery state is a table, so it survives Cloud restarts and process
  interruptions.
- Verification progress is durable per generation; an interrupted pass
  simply leaves `required` set and a later pass can resume or restart.
- `Complete` never clears the flag on partial evidence; a failed or
  partial verification leaves the device unresolved.
- If recovery **marking** fails during restore, the supported restore
  workflow aborts rather than reporting success.
- A restore performed outside the supported workflow is neither detected
  nor marked (see §3).

### 13. Cloud authority limitations

Cloud **never**:

- writes authoritative Retail Sales or Returns;
- fabricates business events;
- replaces inventory;
- reissues business mutations;
- becomes authoritative for business history through projection.

Reconciliation replays Retail's own canonical events. It never imports
Cloud state into Retail as business truth. Cloud's projections are derived
read-only state for reporting and commerce, and the canonical event id,
Store attribution, revision and immutable payload identity are preserved
throughout.

## Rejected alternatives

- **A server-derived "high watermark" of event ids.** Rejected: event ids
  carry no total ordering (they are UUIDs), so a watermark would be a
  false contract.
- **Comparing counts of delivered vs received events.** Rejected: equal
  counts do not imply equal identity; this would silently accept a
  divergence.
- **Clearing recovery on a client-reported "verified N events".** Rejected
  outright — that is the defect Phase 20 removed. Completion depends on
  the server-side ledger, not a claim.
- **Automatic detection of any PostgreSQL restore.** Rejected as not
  implementable with confidence here; claiming it would create false
  assurance. The supported-workflow limitation is stated instead.
- **Per-Store projection-pending scoping.** Not rejected on merit —
  deferred. Global scoping is the conservative V1 choice (§10).
- **Server-side enforcement of a retained-history floor.** Considered and
  left open; see below. It is hardening, not a substitute for a trusted
  device.

## Consequences

**Easier / gained**

- A divergent restore is detectable and repairable instead of silent.
- Completion is provable to a documented boundary rather than asserted.
- Operators can distinguish "infrastructure healthy" from
  "business reconciled".
- Replay safety is structural (idempotent ingest), not procedural.

**Harder / costs**

- One more durable table and a per-device lifecycle to operate.
- Canonical-JSON compatibility with Retail is a live cross-repo contract.
- Reconciliation adds an operator step after every restore.
- Multi-device Stores require resolving every device before the Store is
  reconciled.

## Security considerations

- Device-authenticated only; no human credential can complete recovery for
  a device.
- Per-device scoping prevents one device from clearing another's state or
  from marking a Store reconciled unilaterally.
- The ledger is monotonic within a generation and refuses non-advancing
  pages, so coverage cannot be inflated by re-submitting pages.
- Same event id with different content is fail-closed (never accepted).
- No payload is stored in recovery state — only identity metadata.
- Status/instructions text is bounded and contains no credentials,
  payloads or SQL.

## Proven guarantees vs assumptions

| | Status |
|---|---|
| Recovery marking is durable across restart | **Proven** |
| Failed marking aborts the supported restore | **Proven** |
| `missing=0` alone cannot clear recovery | **Proven** |
| Partial walk cannot complete | **Proven** |
| Skipped / replayed verification page refused | **Proven** |
| Restart resumes and completes only on full walk | **Proven** |
| Identity mismatch is reported missing | **Proven** |
| Canonical hash matches Retail | **Proven (live)** |
| Replay idempotent (`already_accepted`) | **Proven (live)** |
| Store not reconciled while a device is outstanding | **Proven** |
| Store not reconciled while projections pending | **Proven (live)** |
| Event history ≠ projection completion | **Proven (live)** |
| *Declared total is truthful* | **Assumption (trusted device)** |
| *Arbitrary (non-scripted) restores are detected* | **Not guaranteed — not detected** |
| *Lost pre-backup device identities self-heal* | **Not guaranteed — manual** |
| *`projections_pending` is Store-scoped* | **Not guaranteed — global** |

## Verification evidence

- `internal/syncrecovery` unit tests: durable marking, missing detection,
  identity mismatch, unbounded-page refusal, complete-only-after-verified,
  multi-device rule, incomplete-walk refusal, skipped-page refusal,
  replayed-page refusal, restart-resume, no-declared-total refusal,
  projection-pending qualification of `store_reconciled`.
- Live Phase 20 acceptance: divergent Cloud restore (3 events / 1 sale vs
  12 Retail-delivered) reconciled to exact equality (4 sales, 5000 minor
  units, stock 45, revision 5), with `accepted=9 already_accepted=19`,
  projections converging and `store_reconciled: true` after restart.

## Open remediation (source change required)

1. ~~`verify_declared_total` is client-supplied. Smallest fail-closed
   hardening: reject a pass whose `verify_declared_total` is lower than
   the number of `sync_events` Cloud already holds for that device.~~
   **Fixed in Phase 20 R5** (`CountDeviceSyncEvents` +
   `BeginVerifyPass` floor check). Residual: a compromised device can
   still under-declare down to the Cloud-known bound for events Cloud has
   never seen — documented in the proven-vs-assumed table above.
2. `projections_pending` should be scoped per-Store for multi-Store
   deployments. Still open: currently global (conservative, never falsely
   positive).
3. ~~The instructions string references a `sync reconcile` CLI that does
   not exist, and Retail has no reconciliation entry point.~~ **Fixed in
   Phase 20 R5:** `Instructions` names the real
   `Settings → Cloud Sync → Cloud recovery` operator path, and
   MoonLightRetail exposes `GetCloudRecoveryStatus` / `ReconcileCloud`
   bindings (admin-only).

## Related documentation

- `docs/operations/disaster-recovery.md` — restore and reconciliation
  runbook.
- `docs/operations/recovery-evidence.md` — recorded acceptance evidence.
- MoonLightRetail `docs/decisions/ADR-043-operational-disaster-recovery-backups.md`.
