# Catalog Administration (Phase 16)

Authenticated Store-scoped remote control of existing Retail catalog
state. Cloud is the command surface; Retail SQLite is the business
authority. Cloud projections are never written by the admin panel:
they converge through normal Retail catalog sync after Retail applies
a command.

## Authority

```text
Cloud Admin (operator intent)
    ↓ durable typed command + per-device targets
Retail pulls over authenticated outbound device control
    ↓ revision check + canonical Retail service
SQLite authoritative mutation + outbox event
    ↓ normal Retail→Cloud sync
Cloud projection → existing commerce reevaluation
```

## Command types (all v1, immutable)

```text
catalog.product.details.update.v1        names, description, dimensions, prices, cost
catalog.product.online-policy.update.v1  sell_online only (sell_offline never remote)
catalog.product.classification.update.v1 top category, subcategories, tags
catalog.category.details.update.v1       names, status
catalog.category.parents.update.v1       complete parent set (Retail validates DAG)
catalog.category.online-policy.update.v1 online_enabled
catalog.tag.details.update.v1            names, active
catalog.product.configurations.update.v1 full frame-option set (Phase 15 semantics)
```

No remote Product creation, no hard delete, no stock edits, no Sales
or Returns mutation, no historical snapshot rewrites. SKU and entity
UUIDs are immutable and display-only (SKU is LTR-isolated in RTL UI).

## Revisions

Every command carries the expected authoritative revision:

```text
details/classification → expected_catalog_revision
online policy          → expected_sales_policy_revision
configurations         → expected_configuration_revision
category/tag           → expected_catalog_revision
```

Stale expectations conflict with zero business writes. Conflicts are
terminal: refresh the projection and resubmit explicitly. Cloud never
preassigns the next revision; Retail post-increments and returns
`post_revision` in the receipt.

## Command lifecycle (dashboard wording)

```text
PENDING            Change queued — waiting for Retail
DELIVERED          Delivered — waiting for Retail to apply
APPLIED            Applied on Retail — waiting for synchronization
CONVERGED          Converged (projection reached post_revision)
PARTIAL            Partial — some devices diverged, see targets
CONFLICT           Conflict — refresh before retrying
BLOCKED_CAPABILITY Retail update required (old Retail, no capability)
CANCELLED          Cancelled (only while nothing applied)
```

Creation shows **Change queued**, never Saved. APPLIED shows
**Applied on Retail — waiting for synchronization**, never converged.

History pages use opaque keyset cursors (`next_cursor`): concurrent
inserts never shift already-returned pages, so readers observe
neither duplicates nor skips. Product list cursors are additionally
bound to the exact Store+search scope that produced them — reuse
under a different Store or search is rejected instead of silently
continuing another scope's list.

## Offline Retail

Commands stay durable PENDING while Retail is offline; the panel
shows **Command queued — waiting for Retail to reconnect**, never
Save failed. Cloud restart preserves pending commands; Retail
restart preserves receipts and redelivers safely.

## Multi-device Stores

One command snapshots the currently bound devices as immutable
targets; each eligible Retail applies independently and reports its
own outcome. Aggregate success requires every target applied plus
projection convergence. A conflicted device yields PARTIAL, never
hidden success. Devices that rebind to another Store skip old
targets (`SKIPPED_REVOKED`); revoked devices cannot fetch or ACK;
devices joining after creation are not retroactive targets. Old
Retail without `catalog_admin_commands_v1` never receives unknown
payloads; the panel shows **Retail update required before remote
catalog administration**.

## Capability

Retail announces `catalog_admin_commands_v1` via
`POST /api/v1/device-control/capabilities` on every poll tick
(best effort). Absence means incapable. Phase 17 will provide update
controls; Phase 16 never force-updates.

## Cancellation

Pending commands may be cancelled. Once any device applied,
cancellation is refused: issue a compensating command instead.
Cancellation never implies rollback.

## Money

All admin money editing uses int64 minor-unit strings end to end
(>2^53 safe, no JSON Number authority). Frame USD delta preserves
blank-vs-zero: blank means NULL/unavailable, `0` means explicit zero.

## Category online toggle

Disabling a Category/Subcategory removes affected Products from
online channels through Phase 13 reevaluation **without** changing
each Product's own `sell_online`. The UI requires explicit
confirmation with this explanation (Arabic + English) before
queueing. Provider effects happen only after Retail apply → sync →
projection → durable reevaluation; provider outage never rolls back
an applied catalog change.

## Audit

Every command records actor (server-derived from the session, never
client-supplied), Store, type, entity, timestamp, expected revision,
payload hash and per-device results. History is immutable and
retained (no Phase 16 expiry). Payloads carry no secrets and no
customer PII.

## Troubleshooting

```text
REVISION_CONFLICT        Refresh the projection; resubmit explicitly.
VALIDATION_FAILED        Fix values (dimensions, names, prices, DAG, frame bounds).
ENTITY_NOT_FOUND         Entity left the Store scope or was removed.
STORE_SCOPE_CONFLICT     Command Store and entity Store disagree; rejected at both ends.
UNSUPPORTED_COMMAND      Old Retail or unknown type; update Retail.
COMMAND_ID_PAYLOAD_MISMATCH Same ID, changed payload; rejected, original stands.
RETRYABLE_FAILURE        Transient (database busy); redelivery retries automatically.
```
