# Report and money hardening — accepted scope

User request: fix F08, H01, H02, F09 and F10, use the same tax/rounding rules in Retail and Cloud, rerun the disposable demo sync and update the decision review.

Continue on the existing fix branches. Preserve the earlier uncommitted changes. No live database writes, schema changes, historical backfill, provider sends, commits or push.

## Shared report policy

- Keep historical sale/refund/cost/payment minor-unit snapshots unchanged.
- Currency conversion uses the existing exact rational half-away-from-zero rule at each sale/refund boundary and the same configured historical rates.
- Revenue = displayed net sales minus displayed net tax; net cost = original cost minus restocked cost; profit = revenue minus net cost. Do not independently reconvert a tax-exclusive subtotal for this reconciliation.
- Payment breakdowns aggregate each document's applied money by payment method. Use stable method order, not local private payment IDs or transport array order. Tax-on uses the contract’s applied payment amounts (change is separate metadata, never subtracted again); tax-off uses the persisted sale total less tax. Refund totals similarly exclude refunded tax when requested.
- Convert the document payment/refund target once, then allocate it across methods by cumulative exact integer floors. This keeps the method amounts equal to that converted target, including odd cents and split payments. Keep native receipt/tender facts intact and preserve payment row counts.

## Other acceptance

- F08: bulk and single-variant commands use the same nullable exact digit-string validation and int64 bounds; malformed nested values fail before queueing.
- H01: invalid/oversized/non-array option snapshots cannot stop a report. Unknown options are labelled Not recorded; known Original stays Original. No schema/backup compatibility change.
- H02: eliminate silent integer/formatting loss at the desktop boundary, including checked totals and multiplication. Preserve full stored int64 data. Choose and document either exact transport or an enforced safe desktop bound after checking the existing seams. Unsupported values must fail clearly before display, confirmation or mutation, rather than round.
- Run focused failure/regression tests, independent financial/security review and canonical gates. Rerun the fresh-demo HTTP sync and exact comparison with tax and split/rounding examples. Save evidence separately from the previous partial result and remove only the newly created disposable resources.
- Update all five issue statuses and generate a new HTML review only after proof. Keep any unavailable native/print checks explicit.

## Final hardening follow-up

F08/H01/H02/F09/F10 are now fixed and tested. Final fresh sync: 136 events, 37 sales, 38 lines, three refunds, 4,514 matching checks, zero differences. Receipt change is metadata; applied Amount is never reduced again. Numeric Retail screens have an explicit safe-integer limit; backend/storage int64 remains intact. Temporary resources were removed. The final review and verification are in `Guidlines/audit/decision-review-2026-11-10-hardening.html` and `hardening-verification-2026-11-10.md` from the workspace root. This supersedes earlier partial/blocker statuses. Changes remain uncommitted; current app data stayed untouched.
