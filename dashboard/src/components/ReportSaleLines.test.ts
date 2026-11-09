import { cleanup, render, screen, fireEvent } from "@testing-library/svelte";
import { afterEach, it, expect } from "vitest";
import { setLang } from "../lib/i18n.svelte";
import ReportSaleLines from "./ReportSaleLines.svelte";
afterEach(cleanup);
it("shows recorded options, distinct prices, and unknown costs without losing money precision", async () => {
  setLang("en");
  render(ReportSaleLines, {
    saleID: "s",
    saleNumber: "R-1",
    lines: [
      {
        id: "l",
        sku: "SKU",
        product_name: "Papyrus",
        currency: "EGP",
        quantity: 1,
        unit_cost_minor: "700",
        catalog_price_minor: "1250",
        unit_price_minor: "1100",
        line_total_minor: "1100",
        attributes: [
          {
            definition_code: "frame",
            value_code: "gold",
            definition_name_ar: "الإطار",
            definition_name_en: "Frame",
            name_ar: "ذهبي",
            name_en: "Gold",
          },
        ],
      },
      {
        id: "big",
        sku: "BIG",
        product_name: "Large",
        currency: "EGP",
        quantity: 1,
        unit_cost_minor: null,
        catalog_price_minor: null,
        unit_price_minor: "9007199254740993",
        line_total_minor: "9007199254740993",
      },
    ],
  });
  expect(screen.queryByRole("table")).toBeNull();
  await fireEvent.click(screen.getByRole("button"));
  const table = screen.getByRole("table");
  expect(table.textContent).toContain("Frame: Gold");
  expect(table.textContent).toContain("7 EGP");
  expect(table.textContent).toContain("12.50 EGP");
  expect(table.textContent).toContain("11 EGP");
  expect(table.textContent).toContain("90,071,992,547,409.93 EGP");
  expect(screen.getAllByText("Not recorded")).toHaveLength(2);
});
