# Report tax and rounding rules

Retail and Cloud use the same projection policy. Stored sale, cost, tender, change and refund facts are immutable. Reports use integer minor units and half-away-from-zero FX rounding, with matching historical rate settings for cross-app comparisons.

- Displayed net sales = displayed sales − displayed refunds.
- Displayed net tax = displayed tax − displayed refunded tax.
- Revenue = displayed net sales − displayed net tax. Net cost = original cost − restocked cost. Profit = revenue − net cost. Checked arithmetic rejects overflow.
- Payment weights are applied amounts, grouped per document and method. Tax-on target = total applied payments; tax-off target = max(saved sale total − tax, 0). Refund target = saved refund total, less refunded tax when tax is off.
- Convert the document target once, then allocate it across alphabetical method names by cumulative integer floors. The allocated methods sum to the converted target. Counts retain the number of original tender rows. Local IDs and array ordering never decide odd cents.
- Refunds use the original sale's conversion. Refund-only periods and non-restocked returned cost remain explicit.

Money travels as exact digit strings at Cloud's API. Single and bulk variant commands validate each nullable monetary field as non-negative int64 strings before queueing. Retail repeats canonical validation. No schema or historical data migration is needed.
