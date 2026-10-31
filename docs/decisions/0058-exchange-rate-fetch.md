# ADR-0058: Cloud fetches the USD→EGP rate

Status: Accepted (31 October 2026). Relates to ADR-0055 (pre-production
baseline). Mirrors MoonLightRetail's `internal/service/exchangerate`.

## Context

Reports show EGP Sales in USD with the Cloud rate in effect when each Sale
was made (`exchange_rates`). Until now a person had to type every rate in
Settings. MoonLightRetail already fetches the rate from open.er-api.com every
12 hours, so the two apps disagreed on how current a rate is.

## Decision

1. **Provider behind a seam.** `settings.RateSource` is the domain contract;
   `internal/adapter/exchangerate` is the only code that knows the provider's
   JSON (`{"result":"success","base_code":"USD","rates":{"EGP":…}}`). The
   rate is read as decimal text and rounded half up to millionths with
   `math/big`, never through a float. Exponents and text over 32 characters
   are refused. A rate outside 1..10,000 EGP per USD is a provider fault.
2. **Configuration.** `EXCHANGE_RATE_URL` defaults to
   `https://open.er-api.com/v6/latest/USD`; an empty value turns fetching
   off and the Settings page says so. Outside development the URL must be
   https. The client has a 10-second timeout, reads at most 1 MiB and does
   not follow redirects.
3. **When.** Each Cloud instance fetches at startup and every 12 hours
   (`settings.Fetcher.Run`). People with `settings.manage` on all Stores can
   press "Fetch latest rate" (`POST /api/v1/dashboard/settings/exchange-rates/fetch`);
   a second press within a minute returns the last answer without calling
   the provider.
4. **What is stored.** A fetched rate is a normal `exchange_rates` row with
   `source` = the provider host (manual rows have `source = 'manual'`) and no
   note. It starts at the database clock read after a transaction advisory
   lock, and is stored only when the rate in effect at that moment differs.
   So instances that fetch together store one row, a fetch never changes
   the rate of Sales already made, and an unchanged rate adds no history.
5. **Manual rates stay.** A person can still add or delete rates. A manual
   rate stays in use until a later fetch brings a different value, as in
   Retail. A manual rate dated in the future is not "in effect" yet and does
   not stop a fetch.
6. **Failures never block.** A provider or network fault keeps the stored
   rate (`503 RATE_PROVIDER_FAILED` for a click; a warning log with the error
   class only, never the response body, for the background fetch).
7. **Attribution.** The Settings page names the provider and links
   exchangerate-api.com, as the free endpoint's terms ask and as Retail does.

## Consequences

- One new outbound HTTPS dependency, listed in the threat model.
- `exchange_rates.source` is added to the baseline (renumbered `00115`).
- Tests and the E2E server set `EXCHANGE_RATE_URL=` so no test calls the
  internet; the HTTP tests point it at a local server.
