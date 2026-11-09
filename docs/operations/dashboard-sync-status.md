# Dashboard sync activity and image storage

Sync health shows received events over the rolling last seven days, including
today. Its collapsed Details section contains selected-period counts and lifetime retained-event counts.
These are accepted event counts, not unique Products or proof that Retail has
sent all local records. The financial report's completed-day range still
excludes today; receiving data today can legitimately leave that range at zero.
Time bounds come from the Cloud clock, and all counters retain Store scope.

Image storage keeps actual R2 bucket usage prominent. Its collapsed Details section contains Cloud-registered image metadata and upload/gallery diagnostics. The all-Stores view performs an authenticated ListObjectsV2 metadata
read: at most ten pages of 1,000 objects under a shared eight-second status
budget. It does not download objects. Larger buckets are explicitly partial
lower bounds. R2 errors or missing ListBucket permission show unavailable,
never a fabricated zero. A working listing can show bucket contents even if
Cloud has no registered image metadata.

Bucket-wide usage is not returned in Store-scoped requests: an R2 bucket may
contain multiple Stores' files. Per-Store gallery/registered-file counts and
the newest registered-image HEAD check keep their existing scope. Use the
authorized all-Stores view for total bucket usage. This is a live bucket read,
not an account-wide billing, quota, or Cloudflare analytics report.
