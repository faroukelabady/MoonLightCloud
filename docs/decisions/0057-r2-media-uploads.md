# ADR-0057: Product image bytes in object storage (R2)

Status: Accepted (31 October 2026). Supersedes ADR-0056 decision 4 ("Blobs
live in PostgreSQL"). Relates to ADR-0055 (pre-production baseline).

## Context

ADR-0056 kept image bytes in PostgreSQL (`media_blobs.content`). That made
every image part of the database, its backups and restores, and every
dashboard or Shopify read a database read of up to 5 MiB. The first R2 step
(earlier on 31 October) only copied each upload to R2 while every reader kept
using PostgreSQL, so R2 held a second copy nobody read.

## Decision

1. **The bytes live only in object storage.** `media_blobs` keeps the record
   of a stored file (`sha256`, `content_type`, `byte_size`, `width`,
   `height`, `uploaded_by_device`) and no longer has a `content` column. A row
   means the object is stored; the commerce re-evaluation and gallery
   `available` flags keep joining it as before.
2. **One object store per Cloud, chosen by `MEDIA_STORAGE`:**
   - `r2`: a private Cloudflare R2 bucket over the S3 API (official AWS SDK,
     `internal/adapter/r2` only). Required in staging and production.
   - `local`: files in `MEDIA_LOCAL_DIR` (`internal/adapter/filemedia`).
     Development only, and the development default; configuration refuses it
     elsewhere. Compose keeps it in the `moonlightcloud_media` volume.
   Both use the key `product-images/sha256/<first two hex>/<sha256>`, derived
   from the verified hash only. There is no PostgreSQL fallback.
3. **Upload order is object first, record second.** Retail's
   `PUT /api/v1/sync/media/{sha256}` is unchanged. Cloud validates hash,
   format, dimensions and size, writes the object, then commits the
   `media_blobs` row and commerce re-evaluation, then answers 201/200. An
   object-store failure answers a fixed retryable 503 and records nothing. A
   database failure after the object write leaves an unreferenced object; the
   retry writes the same key and completes. Objects are never deleted as
   compensation. Every upload rewrites the object, so a full Retail resync can
   repopulate an emptied bucket.
4. **Reads are verified.** The dashboard image route and Shopify read through
   one service: record from PostgreSQL, bytes from the object store, then the
   size and SHA-256 must match the record (else 503). The dashboard route
   keeps its Store check and headers (ADR-0056); the bucket is never public
   and the browser never sees an R2 key or URL.
5. **Bounded calls.** R2 calls have an eight-second deadline, at most two SDK
   attempts, no redirects and static credentials (no AWS default chain).
   Reads stop at 5 MiB + 1 byte. SDK errors never reach logs or responses.

## Consequences

- Baseline renumbered to `00114_baseline` (ADR-0055): every development
  database is recreated (`./scripts/dev-reset.sh`). Images that existed only
  in the old `content` column are gone; a Retail **Resync all data** (Admin
  PIN) clears Retail's upload ledger and sends every retained image again.
- Database backups no longer contain images. The bucket is the only copy:
  protect it (scoped Object Read & Write token, no public access, and a
  retention or replication rule if Cloudflare offers one for the account).
  Restoring an older database against a newer bucket is safe (extra objects
  are ignored); a bucket missing objects shows 503 until a resync re-uploads.
- Staging and production refuse to start without R2 settings, including the
  `migrate` and other CLI commands that load the full configuration.
- Unreferenced objects (failed commits, images no gallery uses any more) stay
  until a later clean-up job.
- Thumbnails are still the original files; resized copies would be new keys
  under the same contract.

## Rollout

Set the R2 variables on the Cloud service (`docs/operations/r2-media.md`),
deploy, then run Retail's Resync all data once. Rollback to the previous
build needs the previous baseline: there is no in-place downgrade.
