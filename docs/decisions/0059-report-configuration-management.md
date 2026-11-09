# ADR-0059 — Editable report configuration with retained history

Status: accepted. Extends ADR-0035's originally immutable schedule configuration.

The Settings page separates provider templates, recipients, schedule editing,
configured schedules, and paginated history. Two provider dialogs show sample
content. Credentials remain server configuration and never enter these dialogs.

Recipients retain their UUID and provider when their label, address, or language
changes. Schedule edits retain the UUID, use the existing revision as an optimistic
concurrency check, and replace recipient links in one locked transaction. Name
and recipient-only edits retain the locked materialization cursor. A cadence
change computes the next future slot; it does not backfill the edited interval.
Existing slot uniqueness still prevents sending a scheduled slot twice.

Deleting a configuration sets `deleted_at` and disables future use. It does not
delete report runs, delivery snapshots, or notifications. Already queued work
continues with its original snapshots. A recipient's deletion removes it from
future recipient lists; schedules that need another recipient must be edited.

Migration 00116 is additive over the installed 00115 baseline. It adds deletion
state and immutable report kind/name fields on runs, backfilled from the original
schedule. A BEFORE INSERT trigger snapshots both values for new and previous
writers. The report worker reads the run's kind rather than editable configuration.
This preserves a pending daily report even if its schedule becomes ten-day.

Rollout: back up, quiesce Cloud writers, migrate explicitly, then start the new
binary. Do not use the edit/delete feature while previous-version workers still
run. The trigger supports old insert shapes during migration, but an old reader
does not understand deleted configurations or edited schedule snapshots.

Downgrade to 115 is allowed only before deletion or historical kind/name divergence.
The Down guard refuses to revive removed configurations or discard historical
meaning. Otherwise restore a compatible application/database backup pair; never
delete history to force a downgrade.

History uses bounded, descending `(created_at, id)` keyset pagination. New sends
cannot shift older pages into duplicates. Schedule names come from the run snapshot
so renamed/deleted configurations remain readable. The legacy Settings response
keeps its recent-run array for compatibility.

Full recipient addresses are returned only by an all-Stores `settings.manage`
editor endpoint with `Cache-Control: no-store`. Lists remain masked. All new
mutations use the existing session, MFA stage, Store scope, CSRF and body limits.

Report arithmetic, notification idempotency, provider credentials, and send/retry
policies are unchanged. A completed report run means queued notifications, not
confirmed delivery.
