# Historical sale context in reports

Mirror the Retail report improvements without joining current catalog state
to historical sales. Show the full filtered total after conversion using the
existing checked report summaries; never sum mixed currencies or only a page.
Distinguish purchase cost from effective variant selling price and agreed sale
price. Surface captured frame details in sale details and report output.

Preserve prior uncommitted Store/device management work and all existing IDs,
business data, refunds and historical FX. Additive migrations and optional
contract fields only if required. Cloud reader deploys before a Retail writer
that emits new fields; unknown old history must not be inferred from today's
prices. No dependency, live data mutation, commit or push.

Test report math/filters/paging, captured variant/frame context and immutable
history with PostgreSQL, API and component tests. Update OpenAPI/operations
docs, perform financial/security/UI review and run canonical checks.

## Implementation and review

Full-filter summary totals appear in the Currencies card after conversion,
independent of the detail page. New recorded-line DTOs expose saved unit cost,
effective variant selling price, agreed price and variant labels from existing
immutable sale projections. The line context retains the original sale currency
even when the outer report row is converted. SaleDetail uses the same snapshot
read. Missing legacy reference prices remain null/Not recorded. No current
catalog joins, business total changes, schema migrations or event changes.

User confirmed frames should be shown only where already captured. Online order
lines already store style/color/price delta/unresolved snapshots; their API now
has matching dashboard types and UI. No lookup against current frame options,
no false zero for an unknown USD delta, and no mixing online orders into Retail
sales reports. Recorded variant option labels are exposed in reports and sale
details; Retail does not currently record an offline frame selection.

Second-pass financial/security/UI/RTL/accessibility source and component review:
no objective unresolved findings after context print-cell widths were corrected.
Existing report permissions, Store scope, exact minor-unit strings, checked
backend arithmetic and frozen historical FX remain authoritative. Prior
Store/device provisioning work is intact.

Validation so far: strict/semantic OpenAPI + fixture parity, sqlc regeneration
and freshness, disposable PostgreSQL migrations and full Go race suite, vet,
govulncheck, dashboard typecheck/tests/build/audit and container build passed.
Canonical check stopped at its final clean-tree guard for the expected pending
internal/generated changes; no automatic commit was requested. Focused real
PostgreSQL tests prove captured prices/labels/costs in the original currency,
legacy behavior, frame snapshots and Store scope. Dashboard tests: 259 passed.

No live data was changed. Native authenticated visual/print smoke testing is
manual. API additions are optional for old dashboard clients; new UI tolerates
missing context from older servers. Deploy API/dashboard together.
