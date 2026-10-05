# ADR-0047: Product Options — Frame Configurations (Phase 15)

Date: 2026-10-05
Status: accepted (Phase 15 implementation; freeze review pending)

## Context

Phase 5 projected Product lifecycle and sales policy, Phase 9 added Store
ownership, Phase 13 added hierarchical ONLINE category visibility, and
Phases 6A–6C/11 established the provider-neutral commerce boundary
(`CommerceProvider`/`CommerceOrderProvider`) with WooCommerce and Shopify
adapters. Products were sellable as a single representation, but Retail
offers an ONLINE product option: a frame chosen by style and colour with a
per-currency price delta, either explicitly ("No Frame") or via a valid
style/colour combination.

Cloud must publish those options without creating a second stock
authority, without inventing a Product per option, and without breaking
the frozen publication, ownership, inventory, reporting or order
contracts.

## Decision

### Retail-authored contract, current state only

- New event `catalog.product.configuration.snapshot.v1` carries the
  complete configuration state of one Product at one independent monotonic
  `configuration_revision`: a Product ID plus a bounded list of valid
  combination rows (`configuration_id`, `kind = frame`, style/colour
  codes and AR/EN labels, per-currency price deltas in exact integer minor
  units, `enabled`, `position`).
- Rows are explicit valid combinations, never a Cartesian generation;
  duplicate style+colour, duplicate `configuration_id` (after UUID
  canonicalization), out-of-bounds codes/labels, negative money, floats
  and unsupported kinds are rejected at ingestion and re-checked at
  projection.
- The reserved `00000000-0000-0000-0000-000000000000` sentinel is an
  integration-only mapping key for the implicit NO-FRAME choice; it is
  never a business configuration identity.

### Projection reuses the frozen catalog machinery

- `catalog_product_configuration_projection.v1` shares
  `sync_event_processing`, claim/lock/mark, backoff, CLI, lazy backfill,
  and the Store-scope authorization rules. A missing core Product waits
  retryably (`CATALOG_DEPENDENCY_WAIT`); stale revision is a terminal
  no-op; equal revision with differing semantic state blocks
  (`CATALOG_REVISION_CONFLICT`); a `configuration_id` already owned by
  another Product blocks.
- Each accepted revision replaces the Product's configuration set inside
  the projection transaction and bumps the Product's
  `configuration_revision`, then enqueues a coalesced commerce
  re-evaluation (Phase 13) so publication converges — the worker reads
  current state at run time.
- The canonical Product remains the only stock and identity authority.
  `catalog_product_configurations` carries **no SKU and no inventory**;
  configurations are options, not Products.

### Deterministic provider identity

Configured price = base Product price + delta per currency (exact int64
minor units, no FX). Commerce assembles the complete option set (enabled
and disabled) and derives two identities:

- `ConfigurationsFingerprint` — published option semantics (ids, codes,
  published labels, deltas, enabled, position); rename-invariant.
- `ConfigurationsVersion` — per-configuration revision digest; rotates on
  any revision (including a pure label rename) so provider idempotency
  keys can never be reused after a change.

Both feed `ProductOperationKey`/`InventoryOperationKey`. Publication
remains gated by the Product's effective eligibility (active, sell_online,
category-allowed).

### Adapters stay provider-neutral

- The seam returns the provider-side identity of each published choice
  keyed by MoonLight configuration ID (NO-FRAME sentinel for the implicit
  choice). `CommerceService` persists those as durable, Store-scoped
  ownership-keyed `commerce_product_configuration_mappings` **before**
  `SetInventory`; ownership is proven by
  `(provider_key, product_id, configuration_id)` — never by labels/SKU —
  and mappings survive disable/re-enable.
- WooCommerce: one **variable** Product whose variations never carry
  quantities, so NO-FRAME and every frame choice consume the single
  physical parent stock pool. Owned variations are matched by
  `_moonlight_configuration_id` metadata; foreign/manual variations are
  left untouched.
- Shopify: the sellable remote Product is a **bundle** whose only
  tracked-inventory component is the base Product (its managed variant
  carries the canonical `OnlineAvailable`); the untracked frame component
  carries the valid combinations plus NO-FRAME. Configuration identity is
  the bundle-parent variant GID stored in durable mappings. A shop that
  refuses the bundle surface reports the stable
  `SHOPIFY_FRAME_OPTIONS_CAPABILITY_UNAVAILABLE` state rather than
  approximating with unsafe native variants.
- Bundle mutations are asynchronous: a 2xx only acknowledges the request.
  Durable receipts (schema 31, on the existing mutation-evidence row)
  record role, request fingerprint and provider operation id; pending work
  is polled before any new Product mutation, and a returned Product ID is
  retained even on failure so it fences replacement creation. This reuses
  the Phase 11 durable-uncertainty/coordination machinery; no new
  subsystem is introduced.

### Order selections are immutable snapshots

Online order lines capture the selected configuration immutably at
ingest (configuration id, style/colour labels, delta, provider
configuration id). Current configuration rows are never authority for a
past purchase, unknown provider selections are preserved raw and marked
unresolved, an order with an unresolved selection is never reported fully
mapped, and line reconciliation by stable provider line identity keeps
each line's first captured selection.

### Explicit non-goals

Options are publication-only. Inventory authority, sales/returns,
reporting, Store ownership, order status semantics, and provider auth are
untouched. Configuration rows create no Sale/Return, no inventory, and no
financial reporting effect. Phase 16+ may generalize beyond the `frame`
kind.

## Consequences

- Products with options publish correctly to Woo and Shopify without
  duplicating physical stock or inventing Products.
- Schema additions are append-only (00030, 00031); frozen migrations and
  historical snapshots are unchanged.
- Operators gain no new CLI; convergence rides the existing
  `commerce sync-product` path and the Phase 13 re-evaluation worker.
- Residual: bundle adoption depends on Shopify's bundle surface; unsupported
  shops block with a stable capability code instead of degrading silently.
