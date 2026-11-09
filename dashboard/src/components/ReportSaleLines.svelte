<script lang="ts">
  import { lang, msg } from "../lib/i18n.svelte";
  import { formatMinor } from "../lib/money";
  import type { RecordedSaleLine } from "../lib/reportsApi";
  let {
    lines,
    saleID,
    saleNumber,
  }: { lines: RecordedSaleLine[]; saleID: string; saleNumber: string } =
    $props();
  let expanded = $state(false);
  const amount = (value: string | null | undefined, currency: "EGP" | "USD") =>
    value == null
      ? msg("salesReportView.lineContext.notRecorded")
      : formatMinor(value, currency);
</script>

<section class="sale-line-context">
  <button
    type="button"
    class="link screen-only"
    aria-expanded={expanded}
    aria-controls={`recorded-lines-${saleID}`}
    onclick={() => (expanded = !expanded)}
    >{msg("salesReportView.lineContext.title")} —
    <bdi dir="ltr">{saleNumber}</bdi></button
  >
  <div
    id={`recorded-lines-${saleID}`}
    class="recorded-lines"
    hidden={!expanded}
  >
    <p class="report-note">{msg("salesReportView.lineContext.hint")}</p>
    <div class="table-scroll">
      <table aria-label={msg("salesReportView.lineContext.title")}>
        <thead
          ><tr
            ><th scope="col">{msg("salesReportView.product")}</th><th
              scope="col">{msg("saleDetail.qty")}</th
            ><th scope="col">{msg("salesReportView.lineContext.unitCost")}</th
            ><th scope="col"
              >{msg("salesReportView.lineContext.catalogPrice")}</th
            ><th scope="col"
              >{msg("salesReportView.lineContext.agreedPrice")}</th
            ><th scope="col">{msg("salesReportView.sales")}</th></tr
          ></thead
        >
        <tbody
          >{#each lines as line (line.id)}<tr
              ><th scope="row"
                >{line.product_name}<small dir="ltr">{line.sku}</small
                >{#each line.attributes ?? [] as a}<small
                    >{lang() === "ar"
                      ? a.definition_name_ar
                      : a.definition_name_en || a.definition_name_ar}: {lang() ===
                    "ar"
                      ? a.name_ar
                      : a.name_en || a.name_ar}</small
                  >{/each}</th
              ><td>{line.quantity}</td><td
                ><bdi>{amount(line.unit_cost_minor, line.currency)}</bdi></td
              ><td
                ><bdi>{amount(line.catalog_price_minor, line.currency)}</bdi
                ></td
              ><td><bdi>{amount(line.unit_price_minor, line.currency)}</bdi></td
              ><td><bdi>{amount(line.line_total_minor, line.currency)}</bdi></td
              ></tr
            >{/each}</tbody
        >
      </table>
    </div>
  </div>
</section>

<style>
  .sale-line-context {
    text-align: start;
    min-width: 0;
  }
  small {
    display: block;
    font-weight: 400;
  }
  .recorded-lines {
    padding-block: 10px;
  }
  @media print {
    :global(.sale-line-context-cell) { width: 100% !important; }
    .recorded-lines[hidden] {
      display: block !important;
    }
    button {
      display: none;
    }
  }
</style>
