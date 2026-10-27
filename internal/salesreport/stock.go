package salesreport

import "time"

// StockRow is one Variant on the count sheet. Values are quantity times
// the EGP unit cost or price; a missing unit value leaves its value nil.
type StockRow struct {
	VariantID     string `json:"variant_id"`
	SKU           string `json:"sku"`
	ProductNameAr string `json:"product_name_ar"`
	ProductNameEn string `json:"product_name_en"`
	VariantNameAr string `json:"variant_name_ar"`
	VariantNameEn string `json:"variant_name_en"`
	CategoryID    string `json:"category_id"`
	Active        bool   `json:"active"`
	Quantity      int64  `json:"quantity"`
	UnitCost      *Money `json:"unit_cost_minor"`
	Cost          *Money `json:"cost_minor"`
	UnitPrice     *Money `json:"unit_price_minor"`
	Retail        *Money `json:"retail_minor"`
}

// StockCategory totals the stock under one top category.
type StockCategory struct {
	ID              string `json:"id"`
	NameAr          string `json:"name_ar"`
	NameEn          string `json:"name_en"`
	ProductsInStock int64  `json:"products_in_stock"`
	Units           int64  `json:"units"`
	Cost            Money  `json:"cost_minor"`
	Retail          Money  `json:"retail_minor"`
}

// StockTotals is the whole valuation. Units without a cost or price are
// counted separately so an incomplete valuation is visible.
type StockTotals struct {
	ProductsInStock   int64 `json:"products_in_stock"`
	VariantsInStock   int64 `json:"variants_in_stock"`
	Units             int64 `json:"units"`
	Cost              Money `json:"cost_minor"`
	Retail            Money `json:"retail_minor"`
	UnitsWithoutCost  int64 `json:"units_without_cost"`
	UnitsWithoutPrice int64 `json:"units_without_price"`
	InactiveUnits     int64 `json:"inactive_units"`
}

// StockReport is the stocktaking response body.
type StockReport struct {
	GeneratedAt time.Time       `json:"generated_at"`
	StoreID     *string         `json:"store_id"`
	Shop        *Shop           `json:"shop"`
	Totals      StockTotals     `json:"totals"`
	Categories  []StockCategory `json:"categories"`
	Items       []StockRow      `json:"items"`
}

// BuildStock values the stock rows (already in category/product order).
func BuildStock(items []StockItem) (StockTotals, []StockCategory, []StockRow, error) {
	b := &builder{}
	var totals StockTotals
	categories := []StockCategory{}
	categoryIndex := map[string]int{}
	productSeen := map[string]bool{}
	categoryProductSeen := map[[2]string]bool{}
	rows := make([]StockRow, 0, len(items))
	for _, it := range items {
		row := StockRow{
			VariantID: it.VariantID, SKU: it.SKU, CategoryID: it.CategoryID, Active: it.Active, Quantity: it.Quantity,
			ProductNameAr: it.ProductNameAr, ProductNameEn: it.ProductNameEn,
			VariantNameAr: it.VariantNameAr, VariantNameEn: it.VariantNameEn,
		}
		i, ok := categoryIndex[it.CategoryID]
		if !ok {
			i = len(categories)
			categoryIndex[it.CategoryID] = i
			categories = append(categories, StockCategory{ID: it.CategoryID, NameAr: it.CategoryNameAr, NameEn: it.CategoryNameEn})
		}
		category := &categories[i]
		if it.UnitCost != nil {
			unit, value := Money(*it.UnitCost), Money(b.mul(*it.UnitCost, it.Quantity))
			row.UnitCost, row.Cost = &unit, &value
			b.add(&totals.Cost, int64(value))
			b.add(&category.Cost, int64(value))
		} else if it.Quantity > 0 {
			totals.UnitsWithoutCost += it.Quantity
		}
		if it.UnitPrice != nil {
			unit, value := Money(*it.UnitPrice), Money(b.mul(*it.UnitPrice, it.Quantity))
			row.UnitPrice, row.Retail = &unit, &value
			b.add(&totals.Retail, int64(value))
			b.add(&category.Retail, int64(value))
		} else if it.Quantity > 0 {
			totals.UnitsWithoutPrice += it.Quantity
		}
		if it.Quantity > 0 {
			totals.VariantsInStock++
			totals.Units += it.Quantity
			category.Units += it.Quantity
			if !it.Active {
				totals.InactiveUnits += it.Quantity
			}
			if !productSeen[it.ProductID] {
				productSeen[it.ProductID] = true
				totals.ProductsInStock++
			}
			if key := [2]string{it.CategoryID, it.ProductID}; !categoryProductSeen[key] {
				categoryProductSeen[key] = true
				category.ProductsInStock++
			}
		}
		rows = append(rows, row)
	}
	if b.overflow {
		return StockTotals{}, nil, nil, errOverflow()
	}
	return totals, categories, rows, nil
}
