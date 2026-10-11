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
  verify_missing         bigint   -- 00045 (Phase 20 R6), server-recorded
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

Four endpoints, all gated by ordinary device authentication (`DeviceAuth`)
and scoped to the calling device:

```text
GET  /api/v1/device-control/recovery/status
POST /api/v1/device-control/recovery/verify/begin
POST /api/v1/device-control/recovery/verify
POST /api/v1/device-control/recovery/complete
```

A device can only read and clear **its own** recovery state. There is no
cross-device or cross-Store access. Human/dashboard authentication is a
separate identity (ADR-0053) and cannot substitute for device auth here.
For identity verification, Cloud derives scope from that authenticated
device's immutable `device_store_bindings` row; the request cannot select a
Store or another device. A bound device may match event rows whose
server-assigned `sync_events.store_id` is its Store, which preserves
same-Store replacement and re-enrollment continuity. A legacy event with
`store_id IS NULL` is matchable only by its original `device_id`. An
unbound device receives no identity matches, and a replacement device
cannot claim unowned legacy rows whose Store cannot be proven.

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

- the page cursor `verify_last_event_id` — identities in a page must be
  strictly ascending by `event_id` in **byte order** and the page must start
  above the cursor, so a replayed, repeated or out-of-order page is refused
  (`UPDATE … WHERE verify_last_event_id COLLATE "C" < first_event_id`
  affecting zero rows);
- `verify_pages` / `verify_events` — cumulative coverage;
- `verify_complete` — set **only** by a page submitted as
  `end_of_history`;
- `verify_declared_total` — the effective minimum coverage: Cloud's
  accepted-event count captured atomically when the generation opens, raised
  on Retail's first page to the count in its fixed local snapshot;
- `verify_missing` — identities the server did not hold (or held with a
  different hash) in this generation, recorded by the server itself.

`Complete` refuses unless **all** of:

```text
verify_missing == 0                     (server-recorded; the client's
                                         last_missing must also be 0)
progress exists and pages > 0           (never a zero-work shortcut)
verify_complete == true                 (history walked to the end)
verify_declared_total != 0              (a total was declared)
verify_events >= verify_declared_total  (coverage reached the finite boundary)
```

Retail continues to accept and create business events during reconciliation.
The current client first calls the authenticated `verify/begin` endpoint;
Cloud atomically opens a generation and captures the accepted-event count
for that device. Retail then captures the retained, non-blocked outbox count
and SQLite insertion `rowid` high-water mark in one read statement. This
`rowid` is a local insertion boundary, not an event-ID watermark. Retail
declares that count on the first page and enumerates only rows at or below
the captured `rowid`, in the byte order required by Cloud's event-ID cursor.
The first page raises the generation's effective minimum to the greater of
Cloud's captured count and the local snapshot count. All pages carry the
generation token; page and completion writes remain generation-fenced.

Events inserted after the local snapshot are outside that finite pass and
continue through ordinary synchronization and projection. Events accepted
by Cloud before generation initialization are covered by Cloud's lower
bound; local retained events present at snapshot capture are covered by the
snapshot count and bounded enumeration. Thus a growing history does not
move the current pass's end or require Sales and inventory activity to
pause. A restart safely starts a fresh generation and captures a new
snapshot; a partially recorded generation is not treated as complete.
There is no UUID maximum-ID snapshot boundary: event IDs include random
inventory IDs, so a maximum ID would not reliably describe creation time.

The completion write is conditional on the exact `verify_generation` read
and validated by the request. PostgreSQL repeats the generation, page,
end-of-history, lower-bound-coverage and zero-missing predicates
in the same statement that clears `required`. If a newer pass or restore
mark wins the row update first, the older completion affects zero rows and
returns a stale/incomplete result; the caller refreshes status and retries
only after the current pass is complete. Repeating completion for the same
already-cleared valid generation remains idempotent. Page recording is
also generation-qualified, so a delayed page cannot add coverage to a
newer pass. These row-level conditions are database-backed across Cloud
processes; no process-local lock is relied on.

**Proven:** a client cannot clear recovery by claiming `missing=0`
without a coverage walk, nor after a walk in which the server recorded a
missing identity, nor by padding a page with repeated identities; cannot verify one page of many and complete; a
  skipped page is refused; a replayed page is refused; after restart the
  supported client starts a fresh generation and completes only after a new
  full snapshot walk.

**Trust boundary — explicitly not proven:** Cloud stores the count of
accepted events at generation initialization and raises it on the first
page to Retail's declared finite snapshot count. Cloud cannot independently
know whether Retail's local database retained every event that was lost
during restoration. A compromised or defective trusted client can omit
history Cloud has never seen. These checks enforce the declared boundary and
server-known floor; they are not cryptographic proof that no undisclosed
history was lost.

The current client declares the count from its finite insertion-boundary
snapshot, not a live count captured before the walk. The boundary prevents
new IDs that sort behind the cursor from being silently omitted from a
moving walk: post-snapshot rows are deliberately left to ordinary sync.
The residual trust risk is unchanged: a compromised or defective device can
omit history that Cloud has never seen. The lower bound and terminal marker
do not cryptographically prove that undisclosed history was not lost.

### 8. Multiple-device recovery requirements

- recovery state is per-device;
- `devices_unresolved` counts **active** devices bound to the requesting
  device's Store that are still marked required (Phase 20 R6: never another
  Store's devices; a revoked device is excluded);
- a Store is **never** reported reconciled while `devices_unresolved > 0`.

Consequently a device completing its own verification does not mark the
Store reconciled. An offline or lost (still active) device keeps the Store
unreconciled. Revoking the device identity (`moonlight-cloud device revoke`)
is the explicit operator resolution for a retired or replaced workstation:
its retained history travels with the restored Retail database and is
verified by the replacement device.

**Not proven:** behaviour when a device identity existed before the
backup but is absent from the restored snapshot (the identity is lost with
the snapshot). In that case the device cannot be marked because it no
longer exists in `devices`. Re-enrolment or manual reconciliation is
required and is documented as a manual step; no automatic handling is
claimed.

### 9. Ingest acceptance is not projection completion

Receiving and acknowledging an event is **not** the same as projecting it.
Event processors record their own state in `sync_event_processing`
(`pending`, `processed`, `retry`, `blocked`). `projections_pending` counts,
for events of the requesting device's Store, every accepted event whose
**required processor** (an explicit event-type -> processor mapping) is not
`processed` - including an event no processor has claimed yet (no row at
all). An unrelated processor's success never satisfies another event type.
Event types with no projector are outside the mapping (Phase 20 R6).

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

**Phase 20 R6:** both counts are scoped to the requesting device's Store;
another Store's devices or projections never block (or leak into) this
Store's report.

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
| Restart starts a fresh generation and requires a full snapshot walk | **Supported-client contract** |
| Identity mismatch is reported missing | **Proven** |
| Canonical hash matches Retail | **Proven (live)** |
| Replay idempotent (`already_accepted`) | **Proven (live)** |
| Store not reconciled while a device is outstanding | **Proven** |
| Store not reconciled while projections pending | **Proven (live)** |
| Event history ≠ projection completion | **Proven (live)** |
| *Declared total is truthful* | **Assumption (trusted device)** |
| *Arbitrary (non-scripted) restores are detected* | **Not guaranteed — not detected** |
| *Lost pre-backup device identities self-heal* | **Not guaranteed — manual** |
| `projections_pending` is Store-scoped | **Proven** |

## Verification evidence

- `internal/syncrecovery` unit tests: durable marking, missing detection,
  identity mismatch, unbounded-page refusal, complete-only-after-verified,
  multi-device rule, incomplete-walk refusal, skipped-page refusal,
  replayed-page refusal, restart-fresh-generation, no-declared-total refusal,
  projection-pending qualification of `store_reconciled`.
- Live Phase 20 acceptance: divergent Cloud restore (3 events / 1 sale vs
  12 Retail-delivered) reconciled to exact equality (4 sales, 5000 minor
  units, stock 45, revision 5), with `accepted=9 already_accepted=19`,
  projections converging and `store_reconciled: true` after restart.

## Open remediation (source change required)

1. ~~`verify_declared_total` is client-supplied. Smallest fail-closed
   hardening: reject a pass whose `verify_declared_total` is lower than
   the number of `sync_events` Cloud already holds for that device.~~
   **Fixed in Phase 20 R5/R11** (the generation initializer atomically
   captures the Cloud-known count). Residual: a
   compromised device can still under-declare history Cloud has never seen
   — documented in the proven-vs-assumed table above.
2. ~~`projections_pending` should be scoped per-Store.~~ **Fixed in Phase
   20 R6** together with `devices_unresolved` (active devices only).
3. ~~The instructions string references a `sync reconcile` CLI that does
   not exist, and Retail has no reconciliation entry point.~~ **Fixed in
   Phase 20 R5:** `Instructions` names the real
   `Settings → Cloud Sync → Cloud recovery` operator path, and
   MoonLightRetail exposes `GetCloudRecoveryStatus` / `ReconcileCloud`
   bindings (admin-only).

## Phase 20 R6 amendments (independent review findings)

- **Keyset order is byte order.** Pages must be strictly ascending by
  `event_id` in byte order and start above the recorded cursor
  (`COLLATE "C"` in SQL, plain string comparison in Go, SQLite `BINARY` in
  Retail). The earlier cursor compared `event_id` while Retail paged by
  `(created_at, event_id)`, so any history longer than one page was
  refused (P20-F01). Duplicates within a page are refused, so coverage can
  no longer be padded.
- **Server-owned missing ledger** (`verify_missing`, migration 00045).
  `Complete` refuses unless the pass recorded zero missing identities; the
  client's `last_missing` is checked but never sufficient.
- **Marking starts a fresh generation.** `UpsertSyncRecoveryState` and the
  `pg-restore.sh` marking statement reset the ledger, so a completed ledger
  restored from a backup can never be reused.
- **Store-scoped counts** (see §8–§10).
- **Pre-schema-45 backups.** `pg-restore.sh` reads the archive's schema
  version before any destructive step and refuses (exit 10) unless
  `--cloud-binary` is supplied, in which case it migrates the restored
  database and then marks every device.
- **Verification is one query per page** (`SyncEventHashes`).

## Related documentation

### Phase 20 R12 amendment — finite recovery boundary

The previous live-outbox walk could miss a new event whose ID sorted below
the already-materialized page cursor, even though the event was included in
Cloud's later generation count. The supported client now opens a generation
before capturing its local retained-history snapshot. Snapshot count and
SQLite insertion high-water mark are read together; pages carry the
generation and enumerate that finite boundary in event-ID cursor order.
Cloud uses the greater of its accepted count at generation start and the
first-page snapshot count, while retaining terminal-page, zero-missing,
cursor, current-ledger and generation-qualified completion predicates.
Post-snapshot activity is delivered by ordinary sync. A restart safely
starts a fresh pass. The boundary is not based on UUID ordering and does
not alter the documented trusted-client disclosure limitation.

- `docs/operations/disaster-recovery.md` — restore and reconciliation
  runbook.
- `docs/operations/recovery-evidence.md` — recorded acceptance evidence.
- MoonLightRetail `docs/decisions/ADR-043-operational-disaster-recovery-backups.md`.
