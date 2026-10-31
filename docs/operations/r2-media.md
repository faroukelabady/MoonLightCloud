# Product images in Cloudflare R2

Cloud keeps product image bytes in a private R2 bucket
([ADR-0057](../decisions/0057-r2-media-uploads.md)). PostgreSQL only records
which files arrived. Retail keeps using its device credential and the
existing upload route; R2 keys exist only on Cloud. No public URL, CORS rule
or Worker is needed.

## 1. Create the bucket and token

1. In Cloudflare open **R2** and create a bucket (for example
   `moonlight-media` for production, `moonlight-media-dev` for development).
   Leave public access off.
2. Under **Manage R2 API tokens** create a token with **Object Read & Write**
   scoped to that bucket only. Copy the **Access Key ID** and **Secret Access
   Key** (the S3 credentials, not the Cloudflare API token string).
3. Note the account endpoint `https://<account-id>.r2.cloudflarestorage.com`
   (use the `eu`/`fedramp` form if the bucket has a jurisdiction).

## 2. Configure Cloud

Hosted (staging/production): set these on the **Cloud service's secret
environment** (runtime variables, not build arguments). Cloud refuses to
start without them, including `migrate up`.

```dotenv
MEDIA_STORAGE=r2
R2_ENDPOINT=https://<account-id>.r2.cloudflarestorage.com
R2_BUCKET=moonlight-media
R2_ACCESS_KEY_ID=<access key id>
R2_SECRET_ACCESS_KEY=<secret access key>
```

Local development defaults to `MEDIA_STORAGE=local`: files go to
`MEDIA_LOCAL_DIR` (`.media` for `go run`, the `moonlightcloud_media` volume
under Compose). To use the dev bucket instead, put the five lines above in
the Git-ignored **`.env.local`** (`chmod 600 .env.local`) and run
`./scripts/dev-up.sh --no-setup`, which exports it into the Cloud container.
Never put these credentials in Retail, dashboard code or chat.

## 3. First switch-over (this change)

The database baseline changed (no image bytes column), so:

1. Development: `./scripts/dev-reset.sh`, then `./scripts/dev-up.sh`. This
   recreates the development database; existing dev data is lost.
2. In Retail open the Cloud recovery settings and run **Resync all data**
   (Admin PIN). It clears Retail's media upload ledger, so every retained
   image is uploaded again, now into the configured store. It also replays
   event history; do it once, deliberately.
3. Check: R2 → bucket → Objects shows keys like
   `product-images/sha256/<first two hash characters>/<sha256>`, and the
   product images appear in the Cloud dashboard catalog admin.

## How an upload and a read work

Cloud validates JPEG/PNG format, decoded dimensions, SHA-256 and the 5 MiB
cap, writes the R2 object, then commits the PostgreSQL record and only then
answers `201 stored` / `200 already_stored`. An R2 failure answers a safe
retryable `503 UNAVAILABLE`; Retail retries on its next sync cycle.

Dashboard and Shopify reads fetch the record, then the object, and refuse
bytes whose size or SHA-256 differ from the record. A missing object shows as
a failed image (503) until a resync uploads it again.

## Backups and failure cases

- **Database backups no longer include images.** The bucket is the only
  copy. Keep the token scoped, public access off, and add a retention or
  replication rule if your Cloudflare plan offers one.
- Restoring an older database leaves extra objects in the bucket: harmless.
- An emptied or replaced bucket: run Retail's Resync all data; every upload
  rewrites its object, even for files PostgreSQL already records.
- A failed database commit after the object write leaves an unreferenced
  object; the retry reuses the same key. Cloud never deletes objects.

## Verification limits

Automated tests use synthetic credentials, a local TLS S3 fixture and
isolated PostgreSQL databases. Real R2 permissions are proven only by
uploading a new image with the real credentials; `/health/ready` does not
contact R2.
