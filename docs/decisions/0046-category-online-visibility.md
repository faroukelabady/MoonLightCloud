# ADR-0046: Hierarchical Online Catalog Visibility (Phase 13)

Date: 2026-10-04
Status: accepted (Phase 13 implementation; freeze review pending)

## Context

Retail owns Categories and Products (max-depth-3 DAG: root → sub →
leaf, multi-parent allowed) and authorises catalog state through
revisioned `catalog.category.snapshot.v1` events. Cloud projects current
state and drives provider publication through `CommerceService.SyncProduct`
(`Published = is_active AND sell_online`) and Phase 5C availability
(`OnlineAvailable`). Phase 12 added Catalog Health on those predicates.

The roadmap asks for an explicit, provider-neutral ONLINE channel policy
on Category/Subcategory nodes: disabling a node must suppress every
Product classified beneath it — through any DAG path, including shared
nodes and disabled ancestors — from all providers, while preserving each
Product's own `sell_online`, authoritative inventory, history, orders and
provider mappings.

## Decision

1. **Retail-authored policy.** `categories.online_enabled INTEGER NOT
   NULL DEFAULT 1 CHECK (0,1)` (Retail migration 000010). Channel policy
   is a distinct switch from Category lifecycle (`active|hidden|archived`)
   and never rewrites Product rows: one Category mutation + revision bump
   + outbox event in one transaction; no-op updates emit nothing.

2. **Contract v2.** `catalog.category.snapshot.v2` carries
   `online_enabled` as required semantic state (presence-checked; v1
   stays semantically immutable). Cloud accepts both; v1 normalises to
   `online_enabled = true` (exact pre-Phase13 behaviour). Revision
   monotonicity and the frozen equal-revision conflict rule are canonical
   across both versions.

3. **One canonical eligibility calculation.** Migration 00028 adds the
   `catalog_product_online_state` view: a Product is category-eligible
   iff EVERY node in its classification paths (top category,
   subcategories, and all DAG ancestors) exists and has
   `online_enabled = true` (conservative: any disabled relevant node
   suppresses globally; missing state fails safe). Identity comes from
   IDs and edges only. Commerce publication and Catalog Health both read
   this view; Woo/Shopify adapters never traverse the DAG. Blocker
   selection is deterministic (depth, then category_id).

4. **Identity is two-part (§94–§96 vs §100).** `policy_fingerprint` =
   eligibility state (id, depth, online_enabled) — invariant to
   renames/translations. `policy_version` = revision digest of the same
   nodes — the generation marker that makes an
   enabled→disabled→enabled cycle produce fresh provider operation keys,
   so stale idempotency semantics can never be reused. Both feed
   `ProductOperationKey`/`InventoryOperationKey`. The version may rotate
   on renames; that is truthful because commerce payloads already carry
   Category display names (metadata upsert was already required), and a
   rename alone triggers no fan-out and no suppression.

5. **Durable, generic re-evaluation (§83).** Discovery proved NO
   commerce scheduling existed (publication was manual-CLI only), so the
   smallest generic mechanism was built: `commerce_product_reevaluations`
   (Product-keyed primary key = coalescing; Store-proven rows; leased
   SKIP-LOCKED claims). The category projector enqueues every Product
   whose classification reaches the changed node or any DAG descendant —
   set-based, in the SAME transaction as the projection (crash-safe);
   the product projector enqueues on classification change only. A
   worker drains rows through the EXISTING `CommerceService.SyncProduct`
   for every registered provider (independent per-provider progress;
   failure or an unresolved mutation barrier schedules a bounded retry —
   barriers are never bypassed). No provider I/O inside projection
   transactions.

6. **Catalog Health** joins the same view: mapping-missing and
   availability-not-ready now use EFFECTIVE eligibility, and
   `CATEGORY_ONLINE_DISABLED` is exposed as an informational reason that
   is never counted as a failure. R1-L03 (provider selector hidden on
   empty results) was fixed while touching the widget.

## Consequences

- Rebuild replays converge through the same queue: one coalesced
  re-evaluation per affected Product (bounded, deduplicated). Phase 15
  may add pacing; correctness does not depend on category size.
- Consumer impact: Retail must emit v2 (Phase 13 Retail). Old Cloud
  cannot consume v2 — minimum compatible pair is Phase 13 Retail +
  Phase 13 Cloud.
- Inventory authority, Sales/Returns, orders, Store ownership and the
  provider adapters are untouched; suppression is publication-only.
