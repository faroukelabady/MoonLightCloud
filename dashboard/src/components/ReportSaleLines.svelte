<script lang="ts">
  import { lang, msg } from '../lib/i18n.svelte';
  import { compactTime } from '../lib/time';
  import { formatInt, formatMicrorate, formatMinor } from '../lib/money';
  import type { RecordedSaleLine, SalesReport } from '../lib/reportsApi';

  let { sales, offset = 0 }: { sales: SalesReport['sales']; offset?: number } = $props();
  let converted = $derived(sales.some(sale => sale.original_currency !== sale.currency));
  const amount = (value: string | null | undefined, currency: 'EGP' | 'USD') =>
    value == null ? msg('salesReportView.lineContext.notRecorded') : formatMinor(value, currency);
  function optionValue(line: RecordedSaleLine | null): string {
    if (line?.attributes == null) return msg('salesReportView.lineContext.notRecorded');
    return line.attributes.map(attribute => lang() === 'ar'
      ? attribute.name_ar || attribute.name_en || attribute.value_code
      : attribute.name_en || attribute.name_ar || attribute.value_code
    ).join(' / ') || msg('salesReportView.lineContext.originalItem');
  }
  function payment(value: string): string {
    return value === 'cash' ? msg('salesReportView.cash') : value === 'card' ? msg('salesReportView.card') : msg('salesReportView.other');
  }
  function classifications(ar: string, en: string): string {
    return (lang() === 'ar' ? ar || en : en || ar) || msg('salesReportView.classificationUnavailable');
  }
</script>

<p class="report-note">{msg('salesReportView.lineContext.flatHint')}</p>
<div class="table-scroll">
  <table class="flat-sales-table" aria-label={msg('salesReportView.salesDetails')}>
    <thead><tr>
      <th scope="col">{msg('salesReportView.reference')}</th>
      <th scope="col">{msg('salesReportView.product')}</th>
      <th scope="col">{msg('saleDetail.qty')}</th>
      <th scope="col">{msg('salesReportView.lineContext.unitCost')}</th>
      <th scope="col">{msg('salesReportView.lineContext.catalogPrice')}</th>
      <th scope="col">{msg('salesReportView.lineContext.agreedPrice')}</th>
      <th scope="col">{msg('salesReportView.lineContext.lineTotal')}</th>
      <th scope="col">{msg('salesReportView.lineContext.saleTotal')}</th>
      {#if converted}<th scope="col">{msg('salesReportView.recordedAs')}</th>{/if}
    </tr></thead>
    {#each sales as sale, saleIndex (sale.sale_id)}
      <tbody>
        {#each sale.lines?.length ? sale.lines : [null] as line, lineIndex}
          <tr data-sale-id={sale.sale_id}>
            <th scope="row" class="sale-reference" data-sale-tone={(offset + saleIndex) % 4}>
              <bdi dir="ltr" class="sale-ref">{sale.sale_number}</bdi>
              {#if lineIndex === 0}
                <small class="sale-time"><bdi dir="ltr">{compactTime(sale.occurred_at, lang())}</bdi></small>
                <small>{sale.payment_methods.map(payment).join(msg('salesReportView.text'))}</small>
                <small>{msg('salesReportView.category')}: {classifications(sale.root_names_ar, sale.root_names_en)}</small>
                {#if sale.subcategory_names_ar || sale.subcategory_names_en}<small>{msg('salesReportView.subcategory')}: {classifications(sale.subcategory_names_ar, sale.subcategory_names_en)}</small>{/if}
              {/if}
            </th>
            <!-- Variant and SKU are subtext of the product (as in the Retail
                 report) so printed reports need far fewer columns. -->
            <td class="product-cell" data-testid="flat-line-product">
              {line?.product_name ?? sale.products}
              <small class="line-variant" data-testid="flat-line-variant">{optionValue(line)}</small>
              {#if line?.sku ?? sale.skus}<small class="line-sku mono" data-testid="flat-line-sku"><bdi dir="ltr">{line?.sku ?? sale.skus}</bdi></small>{/if}
            </td>
            <td class="number">{formatInt(line?.quantity ?? sale.items_sold)}</td>
            <td class="number"><bdi>{amount(line?.unit_cost_minor, line?.currency ?? sale.original_currency)}</bdi></td>
            <td class="number"><bdi>{amount(line?.catalog_price_minor, line?.currency ?? sale.original_currency)}</bdi></td>
            <td class="number"><bdi>{amount(line?.unit_price_minor, line?.currency ?? sale.original_currency)}</bdi></td>
            <td class="number"><bdi>{amount(line?.line_total_minor, line?.currency ?? sale.original_currency)}</bdi></td>
            <td class="number sale-total">{#if lineIndex === 0}<bdi>{amount(sale.total_minor, sale.currency)}</bdi>{:else}—{/if}</td>
            {#if converted}<td class="number">
              {#if lineIndex === 0 && sale.original_currency !== sale.currency}
                <bdi>{amount(sale.original_total_minor, sale.original_currency)}</bdi>
                {#if sale.rate_microrate !== null}<small>{msg('salesReportView.at')} <bdi>{formatMicrorate(sale.rate_microrate)}</bdi></small>{/if}
              {:else}—{/if}
            </td>{/if}
          </tr>
        {/each}
      </tbody>
    {/each}
  </table>
</div>

<style>
  .flat-sales-table { min-width: 980px; }
  .line-variant, .line-sku { color: #4d5b6b; font-size: 0.86em; }
  .sale-time { white-space: nowrap; }
  .flat-sales-table tbody tr { background: transparent; }
  .flat-sales-table th, .flat-sales-table td { width: auto; text-align: start; }
  .flat-sales-table .number { text-align: end; }
  .flat-sales-table .sale-reference { width: 190px; font-weight: 400; color: #17212b; }
  .sale-reference[data-sale-tone='0'] { background: #edf4ff; }
  .sale-reference[data-sale-tone='1'] { background: #e2f1e8; }
  .sale-reference[data-sale-tone='2'] { background: #f5ead1; }
  .sale-reference[data-sale-tone='3'] { background: #ebe3f4; }
  .sale-ref { font-weight: 700; white-space: nowrap; }
  small { display: block; margin-block-start: 4px; font-weight: 400; }
  @media print {
    .flat-sales-table { min-width: 0 !important; }
    .flat-sales-table th, .flat-sales-table td, .flat-sales-table .sale-reference { width: auto !important; padding: 3px 5px !important; }
    .flat-sales-table small { margin-block-start: 1px; }
    .line-variant, .line-sku { color: #333 !important; }
    .flat-sales-table .sale-reference { color: #111 !important; print-color-adjust: exact; -webkit-print-color-adjust: exact; }
    .sale-reference[data-sale-tone='0'] { background: #f4f4f4 !important; }
    .sale-reference[data-sale-tone='1'] { background: #e0e0e0 !important; }
    .sale-reference[data-sale-tone='2'] { background: #ccc !important; }
    .sale-reference[data-sale-tone='3'] { background: #b8b8b8 !important; }
    .sale-ref { white-space: normal; }
    .flat-sales-table tbody tr { background: transparent !important; }
  }
</style>
