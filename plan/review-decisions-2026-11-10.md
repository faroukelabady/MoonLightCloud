# Selected review fixes — 10 November 2026

User decisions: /home/farouk/Downloads/moonlight-review-decisions-2026-11-09.json.
First verify, commit and fast-forward the existing work into each local main;
retain old branches and source checkpoints. Then branch for the selected fixes.
No force push, reset, live reseed or live schema change.

## Accepted work

- Retail F01/E01: preserve costs omitted by metadata UI, including unknown EGP
  versus zero and stored USD costs. For new USD sales convert effective EGP
  unit purchase cost at the same saved sale-time rate, with checked integer
  rounding; keep existing recorded sales/refunds immutable. Test product edit
  round-trips, variant inheritance/zero/custom costs, rate changes, refunds,
  reports and transaction rollback. Preserve declared USD-only cost as a
  fallback when no EGP cost exists; never replace it with invented zero.
- Retail F02/F03/F07/E05: exact variant image ownership at checkout, consistent
  active-stock totals, localized product-type labels and Original-default
  image targeting in view and edit. Disable/grey zero-stock checkout cards
  with an Out of stock note; barcode-label selection must remain available.
- Retail F06: expose media upload states and safe diagnostics separately from
  outbox ACKs; add authorized controlled retry after validating local bytes.
  Keep full resync and existing identity/hash fences.
- Cloud F04/F05: reset report page/scope filters and temporary scope costs
  correctly, with stale-read guards; distinguish failed frame reads from empty
  frame lists and support retry.
- Cloud E03/E04: effective variant prices/costs with raw nullable overrides and
  clear Original/inherited/custom labels; flat sale-item report with separate
  option value/SKU columns and four print-friendly sale-reference cell shades.
  Preserve store scope, exact money, immutable facts and one sale total.
- D01: update stale schema/flow descriptions against current executable source.
- Update fresh demo seeds to illustrate the accepted behaviour, using current
  SKU/reference standards, correct payments/stock/outbox and empty-only guards.
  Verify temporary demo/report/edge datasets, including synced Cloud reports.
- S01–S04: investigate only. Record evidence and current-data limits; no column,
  table or index removal. F08/H01/H02/E02/E07 stay deferred; E06 stays unchanged.

## Verification and status

Reproduce meaningful regressions before fixes, then run focused SQLite/Postgres
service and frontend tests. Apply UI/RTL/accessibility and inventory/financial/
security second-pass review. Run canonical gates in both repos and Retail
readiness against temporary data; final checks include no local data mutation
and main contains the committed starting branches. Mark a review item complete
only after its acceptance is proved. Keep selected fixes on their new branches
for review; no unrequested deployment or notification/provider sends.

## Result — 10 November 2026

Selected F01–F07, E01/E03/E04/E05, D01 and zero-stock checkout UX are implemented and regression-tested. Retail readiness passed, including temporary seeds/recovery and Linux desktop build. Cloud substantive checks passed; its final clean-tree guard correctly stopped at the deliberately uncommitted source changes. No current shop rows or schema were changed.

S01–S03 are investigated cleanup candidates, with customer-name values found; keep all columns for now. S04 still needs plan/usage measurements. A new temporary refreshed-demo → Cloud HTTP run was not executed because automatic approval review hit its usage limit. Native Windows/print/manual acceptance is not claimed. See `../../Guidlines/audit/decision-review-2026-11-09.html` and `../../Guidlines/audit/verification-2026-11-10.md` for detailed status.

## Fresh sync follow-up

The isolated demo → Cloud test completed: 134 events, 36 sales, 37 sale lines and two canonical refunds reached Cloud. Recorded facts matched. Six report views exposed F09 (payment tax handling) and F10 (two-cent converted USD rounding); these new findings are open, with no product fix applied during verification. Historical rate settings were explicitly aligned in the disposable Cloud. Test resources and temporary credentials were removed. See `Guidlines/audit/decision-review-2026-11-10-sync.html` from the workspace root. This supersedes the earlier approval-service blocker.
