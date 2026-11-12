# ADR-0060: TUF/Sigstore Retail release verification

Status: Implemented with the operator's reviewed public bootstrap on 12 November
2026. Cloud activation, first automated publication and native release acceptance
remain pending. Extends ADR-0051/0052; Retail ADR-051 owns the v2 protocol and
publishing workflow.

Cloud continues to verify/import and orchestrate; it never receives private
root or online release keys. RELEASE_TUF_BOOTSTRAP selects a reviewed public
file containing the initial TUF root, a fixed public HTTPS metadata directory,
and pinned public Sigstore material. RELEASE_TUF_CACHE_DIR is private persistent
state, separate from PostgreSQL and business backups. Each concurrent server
instance needs its own cache. Compose provides a read-only trust mount and a
separate non-root-owned named volume; it does not alter pgdata or media.

Explicit TUF mode replaces legacy public-key import trust completely. Missing
or invalid files fail closed. Empty configuration preserves the v1 transition
path. Both parsers retain the same canonical manifest, artifact, envelope-size
and sequence-conflict rules; no business schema or historical rows change.

The v2 adapter uses pinned go-tuf/v2 and sigstore-go libraries, matching Retail's
reviewed source/policy. Require online TUF refresh/version floors/freshness,
exact approved target bytes, Fulcio/SCT/log proof, exact workflow/issuer/source
repository/commit and GitHub-hosted runner. Import failures expose stable bounded
errors, never provider output. Metadata network calls are deadline/size bounded
and do not follow redirects. Cloud's trust is a filter: Retail verifies again
using its own embedded bootstrap and persistent cache. The helper can verify
cached metadata without network while preserving existing local recovery.

CI's public cloud-import.json can fill the existing authenticated import form;
the server verifies it, and starting a rollout remains a separate human action.
No CI login, new unauthenticated import endpoint, or automatic broad rollout.
The shared protocol marker key_id=7475667369677632 is not a signing-key identity.

Normal release approval is online in the same GitHub job as Sigstore. A compromised
release job can approve malicious software. Offline TUF root keys support trust
administration/recovery but do not prevent this or undo an installed compromise.
Public signing metadata exposes workflow identity/digests, not source contents.
TUF checks run when a release is offered; Cloud can still withhold rollouts.

See Retail docs/operations/tuf-sigstore-releases.md and the HTML guide under
../Guidlines/tuf-sigstore-releases.html for setup, renewal, rollback/transition
and the remaining real CI/native acceptance requirements.

Mixed-client safety: v2 clients report updater protocol 2. PostgreSQL claim
routing requires matching envelope and device protocols. Incompatible targets
require a manual bootstrap; they never receive an unverifiable envelope. The
command transport stays v1. Existing schema columns are reused; no DDL change.
