# ADR-0056: Product media sync (Retail images in Cloud)

- Status: Accepted (2026-10-28); step 2 (Shopify media) implemented 2026-10-28
- Relates to: ADR-0016 (async projection), ADR-0025 / Retail ADR-025 (catalog
  snapshots and bootstrap), ADR-0044 (Shopify adapter), ADR-0049 (physical
  variants), ADR-0055 / Retail ADR-049 (pre-production baseline).

## Context

Retail stores Product and Variant gallery images on the Desktop disk
(`product_images`, `product_variant_images`, at most 10 per gallery, JPEG or
PNG, at most 5 MiB). Nothing leaves the Desktop: the catalog snapshots carry
no image data, and ADR-0044 deliberately leaves Shopify media alone. The owner
wants the same images in Cloud so the online channels (Shopify first) can use
them without re-uploading by hand.

The sync protocol is JSON with a 256 KiB payload cap and an 8 MiB batch body
cap. Image bytes do not belong in it: one gallery alone can exceed the batch
limit, and an all-or-nothing batch would make a single large file block every
Sale behind it.

## Decision

Images travel as two independent, idempotent parts: **what the gallery is**
(a catalog snapshot through the outbox) and **the bytes** (content-addressed
uploads).

1. **Gallery state is a catalog stream.** `catalog.product.media.snapshot.v1`
   carries the complete media state of one Product at one `media_revision`:
   the Product gallery and every Variant gallery, one entry per image with
   `image_id`, `variant_id` (null for the Product gallery), `position`,
   `is_primary`, `sha256`, `content_type`, `byte_size`, `width`, `height`.
   Retail bumps `products.media_revision` and enqueues the snapshot in the same
   transaction as every image mutation (add, delete, reorder, set primary,
   move between galleries), and the catalog bootstrap enqueues it for Products
   whose bookmark is behind. Bounds: at most 200 images per Product; image IDs
   unique; `(variant_id, position)` unique; at most one primary per gallery;
   `sha256` lower-case hex; `image/jpeg` or `image/png`; 1 byte to 5 MiB;
   width × height at most 20,000,000 pixels.
2. **Projection follows the catalog rules.** `catalog_product_media_projection.v1`
   uses the same revision gate as the configuration stream: an older revision
   is a processed no-op, an equal revision must match the stored state
   (otherwise blocked), a newer revision replaces the Product's rows in
   `catalog_product_media` and stores the revision on `catalog_products`. The
   Product must already be projected (dependency wait). A referenced Variant
   must exist and belong to the Product: missing waits, foreign blocks. An
   `image_id` never moves to another Product. The trusted ingress Store must
   own the Product.
3. **Bytes are content-addressed.** `PUT /api/v1/sync/media/{sha256}`
   (device auth, `Content-Type: image/jpeg|image/png`, body at most 5 MiB)
   stores the file in `media_blobs` after checking that the SHA-256 of the
   body equals the path, that the bytes decode as the declared format, and the
   pixel bounds. Repeating an upload is harmless (`200 already_stored`), the
   first one answers `201 stored`. Snapshots and blobs arrive in any order:
   `catalog_product_media.sha256` is not a foreign key, and readers join
   `media_blobs` to know whether the file has arrived.
   `GET /api/v1/sync/capabilities` advertises `media_upload: true` and
   `max_media_bytes`.
4. **Blobs live in PostgreSQL** (`bytea`). At the expected scale (500–1,000
   SKUs, a few images each) this is a few GB at most, it is covered by the
   existing database backups and restore, needs no shared volume between
   Cloud instances, and stays transactional. The blob table is only read by
   key, never scanned with its content. A future object store can replace it
   behind the same upload contract.
5. **Retail uploads after the outbox drain.** Each sync cycle that drained the
   outbox successfully uploads up to 200 not-yet-confirmed hashes (local
   ledger `cloud_media_uploads`). Retryable failures stop the stage and the
   next cycle continues; a file that is missing or no longer matches its hash,
   or a Cloud `422`, is marked failed with a code and is not retried
   automatically. A full re-sync (Admin PIN) also clears the ledger, so a
   restored Cloud gets every file again.

## Shopify media (step 2, 2026-10-28)

The images reach Shopify through an optional provider capability
(`commerce.MediaCommerceProvider`) that runs after product, variant and
stock convergence in `CommerceService.SyncProduct`, so stock never waits for
a file. Shopify Admin API 2026-10 deprecates `productCreateMedia`; the
adapter uses the file model instead:

- The media projector and every newly stored blob enqueue the commerce
  re-evaluation (reason `product_media`) in their own transaction.
- Desired order: the Product gallery (main image first, then position), then
  each Variant gallery in Variant order; a file shown twice is sent once;
  files that have not arrived are skipped until they do.
- A new file goes through `stagedUploadsCreate` (resource `IMAGE`, POST), a
  multipart upload to the returned HTTPS target without the access token,
  and `fileCreate`. It is recorded in
  `commerce_media_mappings(provider_key, product_id, sha256, store_id,
  external_product_id, external_media_id)` before anything else can fail,
  then attached to the sellable product with `fileUpdate(referencesToAdd)`
  once its status is `READY`. At most 10 uploads per sync; anything still
  processing returns retryable `MEDIA_PENDING`.
- Only recorded media is reordered (`productReorderMedia`, MoonLight images
  first), set on variants (`productVariantsBulkUpdate` `mediaId`, the main
  image of each Variant gallery when the product has several variants) or
  deleted (`fileDelete`, when no gallery shows the file or the sellable
  product changed). Media added by hand is never touched. A file Shopify
  fails to process stays recorded and unattached until the image changes,
  so it is not uploaded in a loop.
- The adapter re-checks the product's `moonlight` ownership metafields and
  the SHA-256 of the stored bytes before uploading.
- Every image mutation goes through the existing mutation barrier and
  settles under the same definitive-outcome rule.

## Consequences

- One more catalog stream and processor (`allProjectionProcessors`, the
  pending-projection count and the projection CLI list it).
- Both baselines change before the first install (Cloud sections
  `00047_product_media` and `00048_commerce_media_mappings` in
  `00112_baseline`; Retail `100001_baseline`, renamed per Retail ADR-049):
  every development database is recreated.
- Blobs that no snapshot references any more stay in `media_blobs` until a
  later clean-up job; they are never served to anyone but Cloud itself.
- Threat model: a device can store at most 5 MiB per request and only files
  that decode as images; uploads are authenticated, never public, and the
  bytes are never logged.
