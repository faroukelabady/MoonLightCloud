# Recorded prices in reports

In Reports, Currencies and USD rate now includes all filtered sales after
conversion and the full sale count. It uses the selected report currency and
historical conversion rules; Separate currencies keeps EGP and USD apart.

Open Sales details and expand Line prices and options under a sale. Compare
Purchase cost per unit, Variant selling price per unit at sale time and Agreed
selling price per unit. All line amounts use the sale's recorded currency; the
outer row uses the report currency. The variant price includes its selling-price
override when recorded. A price override does not change purchase cost. Missing
historical prices stay Not recorded. Printing includes the line context.

Frame choices are shown from recorded snapshots only. In Online Orders, open
an order to see its saved frame style, color and charge per unit, or an
unresolved warning. Unknown charges stay Not recorded. Retail checkout has no
frame selection, so no offline frame charge is invented. Online orders remain
separate from Retail sales and are not added into Retail sales report totals.

These are read/UI changes with no database migration or backfill. Deploy the
Cloud API and dashboard together. Retail reads its optional historical selling
price reference from its original durable v3 outbox event. Missing or ambiguous
events never cause today's price to be substituted. Existing cost/FX history,
stock, refunds, credentials and image files are unchanged.
