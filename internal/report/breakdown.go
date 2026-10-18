package report

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
)

// Breakdown assembles one fixed dimension via its static query.
func (s Service) Breakdown(ctx context.Context, req Request, dimension string) (Breakdown, error) {
	fresh, err := s.freshness(ctx)
	if err != nil {
		return Breakdown{}, err
	}
	out := Breakdown{
		GeneratedAt: req.now, Timezone: req.Period.Timezone,
		Period: metaOf(req.Period, s.loc), Dimension: dimension,
		Rows: []BreakdownRow{}, Freshness: fresh,
		StoreID: req.StoreIDOrNil(),
	}
	switch dimension {
	case DimensionProduct:
		rows, err := s.scopedSalesByProduct(ctx, req)
		if err != nil {
			return Breakdown{}, err
		}
		refundRows, err := s.scopedRefundsByProduct(ctx, req)
		if err != nil {
			return Breakdown{}, err
		}
		// Rows are heap-allocated (pointers stay valid); per-currency
		// entry indexes avoid dangling across LineSales appends.
		byKey := map[string]*BreakdownRow{}
		order := []string{}
		lineEntry := map[string]map[string]int{}
		getRow := func(key string, fill func(*BreakdownRow)) *BreakdownRow {
			row, ok := byKey[key]
			if !ok {
				row = &BreakdownRow{Dimension: dimension, LineSales: []LineSaleTotal{}}
				fill(row)
				byKey[key] = row
				order = append(order, key)
				lineEntry[key] = map[string]int{}
			}
			return row
		}
		entryOf := func(key, currency string) *LineSaleTotal {
			row := byKey[key]
			if ei, ok := lineEntry[key][currency]; ok {
				return &row.LineSales[ei]
			}
			row.LineSales = append(row.LineSales, LineSaleTotal{Currency: currency})
			ei := len(row.LineSales) - 1
			lineEntry[key][currency] = ei
			return &row.LineSales[ei]
		}
		for _, r := range rows {
			key := ptrStr(r.ProductID) + "\x00" + r.SKU + "\x00" + r.ProductName
			row := getRow(key, func(row *BreakdownRow) {
				row.ProductID, row.SKU, row.ProductName = r.ProductID, strPtr(r.SKU), strPtr(r.ProductName)
			})
			row.Units += r.Units
			entry := entryOf(key, r.Currency)
			entry.LineSalesMinor = r.LineSales
			entry.LineCostMinor = r.LineCost
		}
		for _, r := range refundRows {
			key := ptrStr(r.ProductID) + "\x00" + r.SKU + "\x00" + r.ProductName
			row := getRow(key, func(row *BreakdownRow) {
				row.ProductID, row.SKU, row.ProductName = r.ProductID, strPtr(r.SKU), strPtr(r.ProductName)
			})
			row.UnitsReturned += r.Units
			entry := entryOf(key, r.Currency)
			entry.LineRefundMinor = r.Refund
			entry.LineReturnedCostMinor = r.ReturnedCost
		}
		for _, row := range byKey {
			sort.Slice(row.LineSales, func(i, j int) bool { return row.LineSales[i].Currency < row.LineSales[j].Currency })
		}
		out.Rows = applyLimit(sortProductRows(byKey, order, req.Currency), req.Limit)
	case DimensionProductType:
		rows, err := s.scopedSalesByProductType(ctx, req)
		if err != nil {
			return Breakdown{}, err
		}
		refundRows, err := s.scopedRefundsByProductType(ctx, req)
		if err != nil {
			return Breakdown{}, err
		}
		byKey := map[string]*BreakdownRow{}
		order := []string{}
		lineEntry := map[string]map[string]int{}
		getRow := func(key string, fill func(*BreakdownRow)) *BreakdownRow {
			row, ok := byKey[key]
			if !ok {
				row = &BreakdownRow{Dimension: dimension, LineSales: []LineSaleTotal{}}
				fill(row)
				byKey[key] = row
				order = append(order, key)
				lineEntry[key] = map[string]int{}
			}
			return row
		}
		entryOf := func(key, currency string) *LineSaleTotal {
			row := byKey[key]
			if ei, ok := lineEntry[key][currency]; ok {
				return &row.LineSales[ei]
			}
			row.LineSales = append(row.LineSales, LineSaleTotal{Currency: currency})
			ei := len(row.LineSales) - 1
			lineEntry[key][currency] = ei
			return &row.LineSales[ei]
		}
		for _, r := range rows {
			key := productTypeSnapshotKey(r.ProductTypeID, r.ProductTypeCode, r.ProductTypeNameAR, r.ProductTypeNameEN)
			row := getRow(key, func(row *BreakdownRow) {
				row.ProductTypeID, row.ProductTypeCode = r.ProductTypeID, r.ProductTypeCode
				row.ProductTypeNameAR, row.ProductTypeNameEN = r.ProductTypeNameAR, r.ProductTypeNameEN
			})
			row.Units, err = addChecked(row.Units, r.Units)
			if err != nil {
				return Breakdown{}, err
			}
			entry := entryOf(key, r.Currency)
			entry.LineSalesMinor = r.LineSales
			entry.LineCostMinor = r.LineCost
		}
		for _, r := range refundRows {
			key := productTypeSnapshotKey(r.ProductTypeID, r.ProductTypeCode, r.ProductTypeNameAR, r.ProductTypeNameEN)
			row := getRow(key, func(row *BreakdownRow) {
				row.ProductTypeID, row.ProductTypeCode = r.ProductTypeID, r.ProductTypeCode
				row.ProductTypeNameAR, row.ProductTypeNameEN = r.ProductTypeNameAR, r.ProductTypeNameEN
			})
			row.UnitsReturned, err = addChecked(row.UnitsReturned, r.Units)
			if err != nil {
				return Breakdown{}, err
			}
			entry := entryOf(key, r.Currency)
			entry.LineRefundMinor = r.Refund
			entry.LineReturnedCostMinor = r.ReturnedCost
		}
		out.Rows = applyLimit(sortProductTypeRows(byKey, order, req.Currency), req.Limit)
	case DimensionRootCategory, DimensionSubcategory:
		var rows []CategoryRow
		var refundRows []RefundCategoryRow
		if root := dimension == DimensionRootCategory; true {
			rows, err = s.scopedSalesByCategory(ctx, req, root)
			if err != nil {
				return Breakdown{}, err
			}
			refundRows, err = s.scopedRefundsByCategory(ctx, req, root)
		}
		if err != nil {
			return Breakdown{}, err
		}
		byKey := map[string]*BreakdownRow{}
		order := []string{}
		lineEntry := map[string]map[string]int{}
		getRow := func(key string, fill func(*BreakdownRow)) *BreakdownRow {
			row, ok := byKey[key]
			if !ok {
				row = &BreakdownRow{Dimension: dimension, LineSales: []LineSaleTotal{}}
				fill(row)
				byKey[key] = row
				order = append(order, key)
				lineEntry[key] = map[string]int{}
			}
			return row
		}
		for _, r := range rows {
			// Snapshot identity: kind + id + both historical names.
			// Renamed snapshots of one category stay separate rows;
			// buckets merge only within exact snapshot identity.
			key := r.Kind + "\x00" + r.ID + "\x00" + r.NameAR + "\x00" + r.NameEN
			kind, id, ar, en := r.Kind, r.ID, r.NameAR, r.NameEN
			row := getRow(key, func(row *BreakdownRow) {
				row.ClassificationKind, row.ClassificationID = &kind, &id
				row.NameAR, row.NameEN = &ar, &en
			})
			row.Units += r.Units
			row.LineSales = append(row.LineSales, LineSaleTotal{
				Currency: r.Currency, LineSalesMinor: r.Sales, LineCostMinor: r.Cost})
			lineEntry[key][r.Currency] = len(row.LineSales) - 1
		}
		for _, r := range refundRows {
			key := r.Kind + "\x00" + r.ID + "\x00" + r.NameAR + "\x00" + r.NameEN
			kind, id, ar, en := r.Kind, r.ID, r.NameAR, r.NameEN
			row := getRow(key, func(row *BreakdownRow) {
				row.ClassificationKind, row.ClassificationID = &kind, &id
				row.NameAR, row.NameEN = &ar, &en
			})
			row.UnitsReturned += r.Units
			ei, ok := lineEntry[key][r.Currency]
			if !ok {
				row.LineSales = append(row.LineSales, LineSaleTotal{Currency: r.Currency})
				ei = len(row.LineSales) - 1
				lineEntry[key][r.Currency] = ei
			}
			row.LineSales[ei].LineRefundMinor = r.Refund
			row.LineSales[ei].LineReturnedCostMinor = r.ReturnedCost
		}
		for _, row := range byKey {
			sort.Slice(row.LineSales, func(i, j int) bool { return row.LineSales[i].Currency < row.LineSales[j].Currency })
		}
		out.Rows = applyLimit(sortCategoryRows(byKey, order, req.Currency), req.Limit)
	case DimensionTag:
		rows, err := s.scopedSalesByTag(ctx, req)
		if err != nil {
			return Breakdown{}, err
		}
		refundRows, err := s.scopedRefundsByTag(ctx, req)
		if err != nil {
			return Breakdown{}, err
		}
		byKey := map[string]*BreakdownRow{}
		order := []string{}
		lineEntry := map[string]map[string]int{}
		getRow := func(key string, fill func(*BreakdownRow)) *BreakdownRow {
			row, ok := byKey[key]
			if !ok {
				row = &BreakdownRow{Dimension: dimension, LineSales: []LineSaleTotal{}}
				fill(row)
				byKey[key] = row
				order = append(order, key)
				lineEntry[key] = map[string]int{}
			}
			return row
		}
		for _, r := range rows {
			// Snapshot identity: id + slug + both historical names.
			// Renamed snapshots of one tag stay separate rows; buckets
			// merge only within exact snapshot identity. Tag groups
			// overlap by design (one line may carry many tags), so group
			// totals are not additive to the overall total.
			key := r.ID + "\x00" + r.Slug + "\x00" + r.NameAR + "\x00" + r.NameEN
			id, slug, ar, en := r.ID, r.Slug, r.NameAR, r.NameEN
			row := getRow(key, func(row *BreakdownRow) {
				row.TagID, row.TagSlug = &id, &slug
				row.NameAR, row.NameEN = &ar, &en
			})
			row.Units += r.Units
			row.LineSales = append(row.LineSales, LineSaleTotal{
				Currency: r.Currency, LineSalesMinor: r.Sales, LineCostMinor: r.Cost})
			lineEntry[key][r.Currency] = len(row.LineSales) - 1
		}
		for _, r := range refundRows {
			key := r.ID + "\x00" + r.Slug + "\x00" + r.NameAR + "\x00" + r.NameEN
			id, slug, ar, en := r.ID, r.Slug, r.NameAR, r.NameEN
			row := getRow(key, func(row *BreakdownRow) {
				row.TagID, row.TagSlug = &id, &slug
				row.NameAR, row.NameEN = &ar, &en
			})
			row.UnitsReturned += r.Units
			ei, ok := lineEntry[key][r.Currency]
			if !ok {
				row.LineSales = append(row.LineSales, LineSaleTotal{Currency: r.Currency})
				ei = len(row.LineSales) - 1
				lineEntry[key][r.Currency] = ei
			}
			row.LineSales[ei].LineRefundMinor = r.Refund
			row.LineSales[ei].LineReturnedCostMinor = r.ReturnedCost
		}
		for _, row := range byKey {
			sort.Slice(row.LineSales, func(i, j int) bool { return row.LineSales[i].Currency < row.LineSales[j].Currency })
		}
		out.Rows = applyLimit(sortCategoryRows(byKey, order, req.Currency), req.Limit)
	case DimensionCashier:
		rows, err := s.scopedSalesByCashier(ctx, req)
		if err != nil {
			return Breakdown{}, err
		}
		refundRows, err := s.scopedRefundsByCashier(ctx, req)
		if err != nil {
			return Breakdown{}, err
		}
		byKey := map[string]*BreakdownRow{}
		order := []string{}
		bucketOf := map[string]map[string]int{}
		getRow := func(key string, fill func(*BreakdownRow)) *BreakdownRow {
			row, ok := byKey[key]
			if !ok {
				row = &BreakdownRow{Dimension: dimension}
				fill(row)
				byKey[key] = row
				order = append(order, key)
				bucketOf[key] = map[string]int{}
			}
			return row
		}
		for _, r := range rows {
			// Explicit null bucket: unattributed sales group under "".
			// Refunds attribute to the ORIGINAL sale cashier (net
			// performance), never subtracted from another cashier; the
			// Return actor is retained in projection for audit/future
			// surfaces and is not currently exposed in dashboard activity.
			key := ptrStr(r.CashierID) + "\x00" + ptrStr(r.CashierName)
			row := getRow(key, func(row *BreakdownRow) {
				row.CashierID, row.CashierName = r.CashierID, r.CashierName
			})
			row.Units += r.Units
			row.Transactions += r.Transactions
			row.CurrencyTotals = append(row.CurrencyTotals, SaleCurrencyTotal{
				Currency: r.Currency, SubtotalMinor: r.Subtotal, DiscountMinor: r.Discount,
				TaxMinor: r.Tax, SalesTotalMinor: r.SalesTotal})
			bucketOf[key][r.Currency] = len(row.CurrencyTotals) - 1
		}
		for _, r := range refundRows {
			key := ptrStr(r.CashierID) + "\x00" + ptrStr(r.CashierName)
			row := getRow(key, func(row *BreakdownRow) {
				row.CashierID, row.CashierName = r.CashierID, r.CashierName
			})
			row.UnitsReturned += r.Units
			row.ReturnTransactions += r.Transactions
			ei, ok := bucketOf[key][r.Currency]
			if !ok {
				row.CurrencyTotals = append(row.CurrencyTotals, SaleCurrencyTotal{Currency: r.Currency})
				ei = len(row.CurrencyTotals) - 1
				bucketOf[key][r.Currency] = ei
			}
			row.CurrencyTotals[ei].RefundTotalMinor = r.RefundTotal
		}
		for _, row := range byKey {
			for i := range row.CurrencyTotals {
				net, err := subChecked(row.CurrencyTotals[i].SalesTotalMinor, row.CurrencyTotals[i].RefundTotalMinor)
				if err != nil {
					return Breakdown{}, err
				}
				row.CurrencyTotals[i].NetSalesMinor = net
			}
			sortSaleCurrencyTotals(row.CurrencyTotals)
		}
		out.Rows = applyLimit(sortCashierRows(byKey, order), req.Limit)
	case DimensionChannel:
		rows, err := s.scopedSalesByChannel(ctx, req)
		if err != nil {
			return Breakdown{}, err
		}
		byKey := map[string]*BreakdownRow{}
		order := []string{}
		refundRows, err := s.scopedRefundsByChannel(ctx, req)
		if err != nil {
			return Breakdown{}, err
		}
		bucketOf := map[string]map[string]int{}
		for _, r := range rows {
			row, ok := byKey[r.Channel]
			if !ok {
				ch := r.Channel
				row = &BreakdownRow{Dimension: dimension, Channel: &ch}
				byKey[r.Channel] = row
				order = append(order, r.Channel)
				bucketOf[r.Channel] = map[string]int{}
			}
			row.Units += r.Units
			row.Transactions += r.Transactions
			row.CurrencyTotals = append(row.CurrencyTotals, SaleCurrencyTotal{
				Currency: r.Currency, SubtotalMinor: r.Subtotal, DiscountMinor: r.Discount,
				TaxMinor: r.Tax, SalesTotalMinor: r.SalesTotal})
			bucketOf[r.Channel][r.Currency] = len(row.CurrencyTotals) - 1
		}
		for _, r := range refundRows {
			// Channel attribution follows the original sale channel.
			row, ok := byKey[r.Channel]
			if !ok {
				ch := r.Channel
				row = &BreakdownRow{Dimension: dimension, Channel: &ch}
				byKey[r.Channel] = row
				order = append(order, r.Channel)
				bucketOf[r.Channel] = map[string]int{}
			}
			row.UnitsReturned += r.Units
			row.ReturnTransactions += r.Transactions
			ei, ok := bucketOf[r.Channel][r.Currency]
			if !ok {
				row.CurrencyTotals = append(row.CurrencyTotals, SaleCurrencyTotal{Currency: r.Currency})
				ei = len(row.CurrencyTotals) - 1
				bucketOf[r.Channel][r.Currency] = ei
			}
			row.CurrencyTotals[ei].RefundTotalMinor = r.RefundTotal
		}
		for _, row := range byKey {
			for i := range row.CurrencyTotals {
				net, err := subChecked(row.CurrencyTotals[i].SalesTotalMinor, row.CurrencyTotals[i].RefundTotalMinor)
				if err != nil {
					return Breakdown{}, err
				}
				row.CurrencyTotals[i].NetSalesMinor = net
			}
			sortSaleCurrencyTotals(row.CurrencyTotals)
		}
		for _, k := range order {
			out.Rows = append(out.Rows, *byKey[k])
		}
		// Stable channel-code order.
		sort.Slice(out.Rows, func(i, j int) bool {
			return ptrStr(out.Rows[i].Channel) < ptrStr(out.Rows[j].Channel)
		})
		out.Rows = applyLimit(out.Rows, req.Limit)
	default:
		return Breakdown{}, apperr.New(apperr.InvalidInput, "unsupported dimension")
	}
	return out, nil
}

func sortCurrencyTotals(t []CurrencyTotal) {
	sort.Slice(t, func(i, j int) bool { return t[i].Currency < t[j].Currency })
}

// sortSaleCurrencyTotals orders header buckets alphabetically by currency.
func sortSaleCurrencyTotals(t []SaleCurrencyTotal) {
	sort.Slice(t, func(i, j int) bool { return t[i].Currency < t[j].Currency })
}

// sortLineSales orders nested line buckets alphabetically by currency.
// SQL GROUP BY order is never relied upon for response order.
func sortLineSales(t []LineSaleTotal) {
	sort.Slice(t, func(i, j int) bool { return t[i].Currency < t[j].Currency })
}

// Deterministic ordering: units descending, then identity tie-breakers.
// Channel rows arrive one per (channel, currency) and sort by channel code.
func sortProductRows(byKey map[string]*BreakdownRow, order []string, currencyScope string) []BreakdownRow {
	out := rowsInOrder(byKey, order)
	for i := range out {
		sortLineSales(out[i].LineSales)
	}
	// Net-desc ranking in a native currency scope (net = gross − refund for
	// that bucket; negatives sort naturally, never clamped). In the
	// all-currency scope cross-currency net is undefined (EGP and USD are
	// never summed), so the frozen units-desc order is preserved; dashboard
	// All mode ranks by normalized net instead.
	sort.SliceStable(out, func(i, j int) bool {
		if currencyScope != "" {
			ni, nj := bucketNet(out[i].LineSales, currencyScope), bucketNet(out[j].LineSales, currencyScope)
			if ni != nj {
				return ni > nj
			}
		}
		if out[i].Units != out[j].Units {
			return out[i].Units > out[j].Units
		}
		if ptrStr(out[i].ProductID) != ptrStr(out[j].ProductID) {
			return ptrStr(out[i].ProductID) < ptrStr(out[j].ProductID)
		}
		if ptrStr(out[i].SKU) != ptrStr(out[j].SKU) {
			return ptrStr(out[i].SKU) < ptrStr(out[j].SKU)
		}
		return ptrStr(out[i].ProductName) < ptrStr(out[j].ProductName)
	})
	return out
}

// productTypeSnapshotKey mirrors the complete historical SQL grouping.
// Length prefixes preserve arbitrary labels; NULL remains distinct from
// an explicitly empty value, including the legacy unknown-type bucket.
func productTypeSnapshotKey(id, code, nameAR, nameEN *string) string {
	var key strings.Builder
	for _, field := range []*string{id, code, nameAR, nameEN} {
		if field == nil {
			key.WriteString("N;")
			continue
		}
		key.WriteString("S")
		key.WriteString(strconv.Itoa(len(*field)))
		key.WriteString(":")
		key.WriteString(*field)
		key.WriteString(";")
	}
	return key.String()
}

func sortProductTypeRows(byKey map[string]*BreakdownRow, order []string, currencyScope string) []BreakdownRow {
	out := rowsInOrder(byKey, order)
	for i := range out {
		sortLineSales(out[i].LineSales)
	}
	sort.Slice(out, func(i, j int) bool {
		if currencyScope != "" {
			ni, nj := bucketNet(out[i].LineSales, currencyScope), bucketNet(out[j].LineSales, currencyScope)
			if ni != nj {
				return ni > nj
			}
		}
		if out[i].Units != out[j].Units {
			return out[i].Units > out[j].Units
		}
		left := [4]*string{out[i].ProductTypeID, out[i].ProductTypeCode, out[i].ProductTypeNameAR, out[i].ProductTypeNameEN}
		right := [4]*string{out[j].ProductTypeID, out[j].ProductTypeCode, out[j].ProductTypeNameAR, out[j].ProductTypeNameEN}
		for k := range left {
			if left[k] == nil || right[k] == nil {
				if (left[k] == nil) != (right[k] == nil) {
					return left[k] == nil
				}
				continue
			}
			if *left[k] != *right[k] {
				return *left[k] < *right[k]
			}
		}
		return false
	})
	return out
}

func sortCategoryRows(byKey map[string]*BreakdownRow, order []string, currencyScope string) []BreakdownRow {
	out := rowsInOrder(byKey, order)
	for i := range out {
		sortLineSales(out[i].LineSales)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if currencyScope != "" {
			ni, nj := bucketNet(out[i].LineSales, currencyScope), bucketNet(out[j].LineSales, currencyScope)
			if ni != nj {
				return ni > nj
			}
		}
		if out[i].Units != out[j].Units {
			return out[i].Units > out[j].Units
		}
		// Tags have their own complete historical identity, distinct from Category IDs.
		if ptrStr(out[i].TagID) != ptrStr(out[j].TagID) {
			return ptrStr(out[i].TagID) < ptrStr(out[j].TagID)
		}
		if ptrStr(out[i].TagSlug) != ptrStr(out[j].TagSlug) {
			return ptrStr(out[i].TagSlug) < ptrStr(out[j].TagSlug)
		}
		// Complete historical-identity tie-breaker: kind, ID, both names.
		// Two distinct rows never compare equal, so response order cannot
		// depend on SQL arrival order.
		if ptrStr(out[i].ClassificationKind) != ptrStr(out[j].ClassificationKind) {
			return ptrStr(out[i].ClassificationKind) < ptrStr(out[j].ClassificationKind)
		}
		if ptrStr(out[i].ClassificationID) != ptrStr(out[j].ClassificationID) {
			return ptrStr(out[i].ClassificationID) < ptrStr(out[j].ClassificationID)
		}
		if ptrStr(out[i].NameAR) != ptrStr(out[j].NameAR) {
			return ptrStr(out[i].NameAR) < ptrStr(out[j].NameAR)
		}
		return ptrStr(out[i].NameEN) < ptrStr(out[j].NameEN)
	})
	return out
}

func sortCashierRows(byKey map[string]*BreakdownRow, order []string) []BreakdownRow {
	out := rowsInOrder(byKey, order)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Units != out[j].Units {
			return out[i].Units > out[j].Units
		}
		if ptrStr(out[i].CashierID) != ptrStr(out[j].CashierID) {
			return ptrStr(out[i].CashierID) < ptrStr(out[j].CashierID)
		}
		return ptrStr(out[i].CashierName) < ptrStr(out[j].CashierName)
	})
	return out
}

func rowsInOrder(byKey map[string]*BreakdownRow, order []string) []BreakdownRow {
	// Deterministic: callers pass SQL-ordered keys (units desc, identity
	// tie-break); map assembly preserves that order here.
	out := make([]BreakdownRow, 0, len(order))
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	return out
}

// bucketNet returns gross − refund for one currency bucket (missing bucket
// counts as zero). Both operands are nonnegative by schema, so the
// difference cannot overflow int64; negatives are valid and preserved.
func bucketNet(buckets []LineSaleTotal, currency string) int64 {
	for _, b := range buckets {
		if b.Currency == currency {
			return b.LineSalesMinor - b.LineRefundMinor
		}
	}
	return 0
}
