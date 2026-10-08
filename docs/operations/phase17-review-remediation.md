# Phase 17 review compatibility remediation

This correction keeps Retail schema 000021 and Cloud schema 40. No shipped
migration, Retail business identity, durable event payload, historical snapshot
or financial calculation changes.

## Shared installation seed versus projection key

Retail migration 000019 assigns the fixed ProductType identity
`10000000-0000-4000-8000-000000000001` independently in every installation.
Its mutable current state belongs to the authenticated ingress Store. The
fixed source identity does not grant one Store ownership of other installations.

Cloud follows its established shared Category/Tag compatibility pattern for
this one enumerated identity. A new scoped Type projection receives a
deterministic internal UUID derived from the existing default namespace and
`<store>:product-type:<source-id>`. An already projected, source-proven,
Store-owned raw seed retains its original key and Product references. A NULL
legacy projection remains unowned and separate from scoped projections.
Custom Type identities retain the normal strict ownership rules.

The internal key is storage identity only. Type and Product Admin responses,
command payloads, command receipts/results, Retail outbox bytes and historical
Sale/Return snapshots retain the actual Retail identity. Admin ownership and
convergence lookups take the requested Store explicitly. They never substitute
an alias for a Retail-created result entity.

Before using an occupied seed key, projection checks its source event's original
Type identity and ingress Store. Public inverse translation has the same proof,
using joined reads rather than additional per-row queries. An unrelated custom
Type that already occupies a derived key is preserved; the seed event blocks
with a bounded identity conflict. New raw alias intents are refused. Unbound
legacy work cannot overwrite either an owned raw seed or a proven scoped alias.

This affects storage scoping, not ProductType capabilities. No behavior depends
on a ProductType name, translated label or user-entered code.

## Existing blocked installations

After deploying the correction, run the existing bounded operation:

```sh
moonlight-cloud projection recover-catalog
```

The operation now includes seed Type and Product-v2 events previously blocked
with the exact Type ownership reason. Eligibility requires the original
non-NULL ingress Store and a foreign-owned raw seed projection. A Product
owned by another Store is excluded. Recovery never resets custom identity
collisions, same-Store revision contradictions or arbitrary validation errors.

At most 100 source events are rearmed per invocation, across all supported
recovery families. Selection and reset share the existing serializable
transaction. Allow ordinary workers to converge, then repeat until the count
is zero. A serialization abort is safely retryable. Payloads, hashes, histories
and already valid projections are not rewritten. Recovery is not a historical
Store backfill.

## Migration 40 development rollback

See [migration operations](migrations.md) for the strictly validated constraint
rename used when replaying shipped migration 40 after a supported development
rollback. The migration files remain immutable. The correction does not change
migration 40's intentionally restricted Down policy or make production
rollback safe.

## Sale-v3 historical Type code bounds

Historical ProductType codes are limited to 32 characters, matching the
persisted schema. Generic Variant snapshot codes retain their separate
64-character contract. Invalid new events are rejected before acceptance.
Already accepted malformed Sale-v3 events become terminal validation blocks
without partial Sale/ownership rows or payload rewriting. V1/V2 are unchanged.
