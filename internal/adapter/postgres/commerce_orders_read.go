package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce/orders"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// keyset pagination (created_at DESC, provider_key, external_order_id).
// Empty provider/status means no filter; limit is bounded.
func (d CommerceOrders) ListOrderSummaries(ctx context.Context, provider, status string, limit int, cursor *orders.OrderCursor) ([]orders.OrderSummary, error) {
	if limit <= 0 || limit > 100 {
		return nil, apperr.New(apperr.InvalidInput, "limit must be 1..100")
	}
	rows, err := d.listOrderRows(ctx, provider, status, limit, cursor)
	if err != nil {
		return nil, err
	}
	summaries := make([]orders.OrderSummary, 0, len(rows))
	for _, row := range rows {
		summaries = append(summaries, orderSummaryRow(row))
	}
	return summaries, nil
}

// orderSummaryRow maps one list row to its dashboard summary with exact
// string money and UTC RFC3339 timestamps.
func orderSummaryRow(row sqlcgen.ListCommerceOrdersRow) orders.OrderSummary {
	name := strings.TrimSpace(row.CustomerFirstName + " " + row.CustomerLastName)
	return orders.OrderSummary{
		ProviderKey: row.ProviderKey, ExternalOrderID: row.ExternalOrderID,
		OrderNumber: row.OrderNumber, ProviderStatus: row.ProviderStatus,
		CanonicalStatus: row.CanonicalStatus, Currency: row.Currency,
		TotalMinor:   orders.MinorString(row.TotalMinor),
		CreatedAt:    row.CreatedAt.Time.UTC().Format(time.RFC3339),
		ModifiedAt:   row.ModifiedAt.Time.UTC().Format(time.RFC3339),
		CustomerName: name, MappingComplete: row.MappingComplete,
		UnmappedLineCount: int(row.UnmappedLines),
		ProviderDeleted:   row.ProviderDeleted, Revision: row.Revision,
	}
}

// ListOrderPage returns one dashboard page plus its continuation cursor
// using the same deterministic keyset ordering as ListOrderSummaries.
// It fetches one lookahead row to decide continuation without a full
// count; Next is nil on the final page and otherwise points after the
// last returned row (never the lookahead row).
func (d CommerceOrders) ListOrderPage(ctx context.Context, provider, status string, limit int, cursor *orders.OrderCursor) (orders.OrderPage, error) {
	if limit <= 0 || limit > 100 {
		return orders.OrderPage{}, apperr.New(apperr.InvalidInput, "limit must be 1..100")
	}
	// One unbounded page past the configured maximum: the lookahead
	// row only decides continuation and is never returned.
	rows, err := d.listOrderRows(ctx, provider, status, limit+1, cursor)
	if err != nil {
		return orders.OrderPage{}, err
	}
	page := orders.OrderPage{Items: make([]orders.OrderSummary, 0, len(rows))}
	for _, row := range rows {
		if len(page.Items) == limit {
			break
		}
		page.Items = append(page.Items, orderSummaryRow(row))
	}
	if len(rows) > limit {
		// Full-precision row time: summaries render seconds, but the
		// fence must keep sub-second precision so same-second rows
		// can neither repeat nor skip.
		last := rows[limit-1]
		page.Next = &orders.OrderCursor{
			CreatedAt: last.CreatedAt.Time, ProviderKey: last.ProviderKey, ExternalOrderID: last.ExternalOrderID,
		}
	}
	return page, nil
}

// listOrderRows runs the shared keyset list query for both list entry
// points so ordering can never diverge between them.
func (d CommerceOrders) listOrderRows(ctx context.Context, provider, status string, limit int, cursor *orders.OrderCursor) ([]sqlcgen.ListCommerceOrdersRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	var cursorAt pgtype.Timestamptz
	var cursorProvider, cursorOrder string
	if cursor != nil {
		cursorAt = pgTime(cursor.CreatedAt)
		cursorProvider, cursorOrder = cursor.ProviderKey, cursor.ExternalOrderID
	}
	rows, err := sqlcgen.New(d.pool).ListCommerceOrders(ctx, sqlcgen.ListCommerceOrdersParams{
		Column1: provider, Column2: status, Column3: cursorAt,
		Column4: cursorProvider, Column5: cursorOrder, Limit: int32(limit),
	})
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "order list", redact(err))
	}
	return rows, nil
}

// GetOrderDetail assembles the full operator-visible order.
func (d CommerceOrders) GetOrderDetail(ctx context.Context, providerKey, externalOrderID string) (orders.OrderDetail, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	if _, err := commerce.ValidateProviderKey(providerKey); err != nil {
		return orders.OrderDetail{}, apperr.New(apperr.InvalidInput, err.Error())
	}
	if _, err := orders.CanonicalExternalOrderID(externalOrderID); err != nil {
		return orders.OrderDetail{}, apperr.New(apperr.InvalidInput, "invalid external order id")
	}
	q := sqlcgen.New(d.pool)
	header, err := q.GetCommerceOrder(ctx, sqlcgen.GetCommerceOrderParams{
		ProviderKey: providerKey, ExternalOrderID: externalOrderID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return orders.OrderDetail{}, apperr.New(apperr.NotFound, "order not found")
		}
		return orders.OrderDetail{}, apperr.Wrap(apperr.Internal, "order detail", redact(err))
	}
	lines, err := q.ListCommerceOrderLines(ctx, sqlcgen.ListCommerceOrderLinesParams{
		ProviderKey: providerKey, ExternalOrderID: externalOrderID,
	})
	if err != nil {
		return orders.OrderDetail{}, apperr.Wrap(apperr.Internal, "order detail", redact(err))
	}
	addresses, err := q.ListCommerceOrderAddresses(ctx, sqlcgen.ListCommerceOrderAddressesParams{
		ProviderKey: providerKey, ExternalOrderID: externalOrderID,
	})
	if err != nil {
		return orders.OrderDetail{}, apperr.Wrap(apperr.Internal, "order detail", redact(err))
	}
	history, err := q.ListCommerceOrderStatusHistory(ctx, sqlcgen.ListCommerceOrderStatusHistoryParams{
		ProviderKey: providerKey, ExternalOrderID: externalOrderID,
	})
	if err != nil {
		return orders.OrderDetail{}, apperr.Wrap(apperr.Internal, "order detail", redact(err))
	}
	name := strings.TrimSpace(header.CustomerFirstName + " " + header.CustomerLastName)
	detail := orders.OrderDetail{
		Summary: orders.OrderSummary{
			ProviderKey: header.ProviderKey, ExternalOrderID: header.ExternalOrderID,
			OrderNumber: header.OrderNumber, ProviderStatus: header.ProviderStatus,
			CanonicalStatus: header.CanonicalStatus, Currency: header.Currency,
			TotalMinor:   orders.MinorString(header.TotalMinor),
			CreatedAt:    header.CreatedAt.Time.UTC().Format(time.RFC3339),
			ModifiedAt:   header.ModifiedAt.Time.UTC().Format(time.RFC3339),
			CustomerName: name, MappingComplete: header.MappingComplete,
			UnmappedLineCount: int(header.UnmappedLines),
			ProviderDeleted:   header.ProviderDeleted, Revision: header.Revision,
		},
		DiscountMinor:    orders.MinorString(header.DiscountMinor),
		ShippingMinor:    orders.MinorString(header.ShippingMinor),
		CartTaxMinor:     orders.MinorString(header.CartTaxMinor),
		TotalTaxMinor:    orders.MinorString(header.TotalTaxMinor),
		PricesIncludeTax: header.PricesIncludeTax,
		PaymentMethod:    header.PaymentMethod, PaymentMethodTitle: header.PaymentMethodTitle,
		CustomerFirstName: header.CustomerFirstName, CustomerLastName: header.CustomerLastName,
		CustomerEmail: header.CustomerEmail, CustomerPhone: header.CustomerPhone,
	}
	if header.PaidAt.Valid {
		paid := header.PaidAt.Time.UTC().Format(time.RFC3339)
		detail.PaidAt = &paid
	}
	if header.CompletedAt.Valid {
		completed := header.CompletedAt.Time.UTC().Format(time.RFC3339)
		detail.CompletedAt = &completed
	}
	for _, line := range lines {
		view := orderLineViewFromRow(line)
		detail.Lines = append(detail.Lines, view)
	}
	for _, address := range addresses {
		detail.Addresses = append(detail.Addresses, orders.OrderAddressView{
			Kind:      address.Kind,
			FirstName: address.FirstName, LastName: address.LastName, Company: address.Company,
			Address1: address.Address1, Address2: address.Address2,
			City: address.City, State: address.State, Postcode: address.Postcode,
			Country: address.Country, Email: address.Email, Phone: address.Phone,
		})
	}
	for _, event := range history {
		detail.StatusHistory = append(detail.StatusHistory, orders.OrderStatusEvent{
			OrderRevision: event.OrderRevision, ProviderStatus: event.ProviderStatus,
			CanonicalStatus: event.CanonicalStatus,
			ObservedAt:      event.ObservedAt.Time.UTC().Format(time.RFC3339),
		})
	}
	return detail, nil
}

// CountOrdersByStatus returns order counts per canonical status.
func (d CommerceOrders) CountOrdersByStatus(ctx context.Context, provider string) ([]orders.OrderStatusCount, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).CountCommerceOrdersByStatus(ctx, provider)
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "order counts", redact(err))
	}
	counts := make([]orders.OrderStatusCount, 0, len(rows))
	for _, row := range rows {
		counts = append(counts, orders.OrderStatusCount{CanonicalStatus: row.CanonicalStatus, Total: row.Total})
	}
	return counts, nil
}

// OrderInboxStats adapts the webhook queue stats to the orders read model.
func (d CommerceOrders) OrderInboxStats(ctx context.Context) (orders.WebhookQueueStats, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	pending, retry, blocked, oldest, err := d.OrderWebhookStats(ctx)
	if err != nil {
		return orders.WebhookQueueStats{}, err
	}
	stats := orders.WebhookQueueStats{Pending: pending, Retry: retry, Blocked: blocked}
	if oldest != nil {
		formatted := oldest.UTC().Format(time.RFC3339)
		stats.OldestPending = &formatted
	}
	return stats, nil
}

func scopedStoreUUID(storeID string) (pgtype.UUID, error) {
	suid, err := parseUUID(storeID)
	if err != nil {
		return pgtype.UUID{}, apperr.New(apperr.InvalidInput, "store_id must be a UUID")
	}
	return suid, nil
}

// ListOrderSummariesForStore returns one Store-scoped dashboard page.
func (d CommerceOrders) ListOrderSummariesForStore(ctx context.Context, storeID, provider, status string, limit int, cursor *orders.OrderCursor) ([]orders.OrderSummary, error) {
	if limit <= 0 || limit > 100 {
		return nil, apperr.New(apperr.InvalidInput, "limit must be 1..100")
	}
	rows, err := d.listOrderRowsForStore(ctx, storeID, provider, status, limit, cursor)
	if err != nil {
		return nil, err
	}
	summaries := make([]orders.OrderSummary, 0, len(rows))
	for _, row := range rows {
		summaries = append(summaries, scopedOrderSummaryRow(row))
	}
	return summaries, nil
}

// ListOrderPageForStore returns one Store-scoped page plus its
// continuation cursor, mirroring ListOrderPage semantics.
func (d CommerceOrders) ListOrderPageForStore(ctx context.Context, storeID, provider, status string, limit int, cursor *orders.OrderCursor) (orders.OrderPage, error) {
	if limit <= 0 || limit > 100 {
		return orders.OrderPage{}, apperr.New(apperr.InvalidInput, "limit must be 1..100")
	}
	rows, err := d.listOrderRowsForStore(ctx, storeID, provider, status, limit+1, cursor)
	if err != nil {
		return orders.OrderPage{}, err
	}
	page := orders.OrderPage{Items: make([]orders.OrderSummary, 0, len(rows))}
	for _, row := range rows {
		if len(page.Items) == limit {
			break
		}
		page.Items = append(page.Items, scopedOrderSummaryRow(row))
	}
	if len(rows) > limit {
		last := rows[limit-1]
		page.Next = &orders.OrderCursor{
			CreatedAt: last.CreatedAt.Time, ProviderKey: last.ProviderKey, ExternalOrderID: last.ExternalOrderID,
		}
	}
	return page, nil
}

func scopedOrderSummaryRow(row sqlcgen.ListCommerceOrdersForStoreRow) orders.OrderSummary {
	name := strings.TrimSpace(row.CustomerFirstName + " " + row.CustomerLastName)
	return orders.OrderSummary{
		ProviderKey: row.ProviderKey, ExternalOrderID: row.ExternalOrderID,
		OrderNumber: row.OrderNumber, ProviderStatus: row.ProviderStatus,
		CanonicalStatus: row.CanonicalStatus, Currency: row.Currency,
		TotalMinor:   orders.MinorString(row.TotalMinor),
		CreatedAt:    row.CreatedAt.Time.UTC().Format(time.RFC3339),
		ModifiedAt:   row.ModifiedAt.Time.UTC().Format(time.RFC3339),
		CustomerName: name, MappingComplete: row.MappingComplete,
		UnmappedLineCount: int(row.UnmappedLines),
		ProviderDeleted:   row.ProviderDeleted, Revision: row.Revision,
	}
}

func (d CommerceOrders) listOrderRowsForStore(ctx context.Context, storeID, provider, status string, limit int, cursor *orders.OrderCursor) ([]sqlcgen.ListCommerceOrdersForStoreRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	suid, err := scopedStoreUUID(storeID)
	if err != nil {
		return nil, err
	}
	var cursorAt pgtype.Timestamptz
	var cursorProvider, cursorOrder string
	if cursor != nil {
		cursorAt = pgTime(cursor.CreatedAt)
		cursorProvider, cursorOrder = cursor.ProviderKey, cursor.ExternalOrderID
	}
	rows, err := sqlcgen.New(d.pool).ListCommerceOrdersForStore(ctx, sqlcgen.ListCommerceOrdersForStoreParams{
		StoreID: suid, Column2: provider, Column3: status, Column4: cursorAt,
		Column5: cursorProvider, Column6: cursorOrder, Limit: int32(limit),
	})
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "order list", redact(err))
	}
	return rows, nil
}

// GetOrderDetailForStore assembles the full order graph for one Store.
// Wrong-Store and missing rows are indistinguishable
// (repository-standard not-found): Store A can never observe Store B
// order, customer, address, line, or history data through this surface.
func (d CommerceOrders) GetOrderDetailForStore(ctx context.Context, storeID, providerKey, externalOrderID string) (orders.OrderDetail, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	if _, err := commerce.ValidateProviderKey(providerKey); err != nil {
		return orders.OrderDetail{}, apperr.New(apperr.InvalidInput, err.Error())
	}
	if _, err := orders.CanonicalExternalOrderID(externalOrderID); err != nil {
		return orders.OrderDetail{}, apperr.New(apperr.InvalidInput, "invalid external order id")
	}
	suid, err := scopedStoreUUID(storeID)
	if err != nil {
		return orders.OrderDetail{}, err
	}
	q := sqlcgen.New(d.pool)
	header, err := q.GetCommerceOrderForStore(ctx, sqlcgen.GetCommerceOrderForStoreParams{
		ProviderKey: providerKey, ExternalOrderID: externalOrderID, StoreID: suid,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return orders.OrderDetail{}, apperr.New(apperr.NotFound, "order not found")
		}
		return orders.OrderDetail{}, apperr.Wrap(apperr.Internal, "order detail", redact(err))
	}
	lines, err := q.ListCommerceOrderLines(ctx, sqlcgen.ListCommerceOrderLinesParams{
		ProviderKey: providerKey, ExternalOrderID: externalOrderID,
	})
	if err != nil {
		return orders.OrderDetail{}, apperr.Wrap(apperr.Internal, "order detail", redact(err))
	}
	addresses, err := q.ListCommerceOrderAddresses(ctx, sqlcgen.ListCommerceOrderAddressesParams{
		ProviderKey: providerKey, ExternalOrderID: externalOrderID,
	})
	if err != nil {
		return orders.OrderDetail{}, apperr.Wrap(apperr.Internal, "order detail", redact(err))
	}
	history, err := q.ListCommerceOrderStatusHistory(ctx, sqlcgen.ListCommerceOrderStatusHistoryParams{
		ProviderKey: providerKey, ExternalOrderID: externalOrderID,
	})
	if err != nil {
		return orders.OrderDetail{}, apperr.Wrap(apperr.Internal, "order detail", redact(err))
	}
	name := strings.TrimSpace(header.CustomerFirstName + " " + header.CustomerLastName)
	detail := orders.OrderDetail{
		StoreID: storeString(header.StoreID),
		Summary: orders.OrderSummary{
			ProviderKey: header.ProviderKey, ExternalOrderID: header.ExternalOrderID,
			OrderNumber: header.OrderNumber, ProviderStatus: header.ProviderStatus,
			CanonicalStatus: header.CanonicalStatus, Currency: header.Currency,
			TotalMinor:   orders.MinorString(header.TotalMinor),
			CreatedAt:    header.CreatedAt.Time.UTC().Format(time.RFC3339),
			ModifiedAt:   header.ModifiedAt.Time.UTC().Format(time.RFC3339),
			CustomerName: name, MappingComplete: header.MappingComplete,
			UnmappedLineCount: int(header.UnmappedLines),
			ProviderDeleted:   header.ProviderDeleted, Revision: header.Revision,
		},
		DiscountMinor:    orders.MinorString(header.DiscountMinor),
		ShippingMinor:    orders.MinorString(header.ShippingMinor),
		CartTaxMinor:     orders.MinorString(header.CartTaxMinor),
		TotalTaxMinor:    orders.MinorString(header.TotalTaxMinor),
		PricesIncludeTax: header.PricesIncludeTax,
		PaymentMethod:    header.PaymentMethod, PaymentMethodTitle: header.PaymentMethodTitle,
		CustomerFirstName: header.CustomerFirstName, CustomerLastName: header.CustomerLastName,
		CustomerEmail: header.CustomerEmail, CustomerPhone: header.CustomerPhone,
	}
	if header.PaidAt.Valid {
		paid := header.PaidAt.Time.UTC().Format(time.RFC3339)
		detail.PaidAt = &paid
	}
	if header.CompletedAt.Valid {
		completed := header.CompletedAt.Time.UTC().Format(time.RFC3339)
		detail.CompletedAt = &completed
	}
	for _, line := range lines {
		view := orderLineViewFromRow(line)
		detail.Lines = append(detail.Lines, view)
	}
	for _, address := range addresses {
		detail.Addresses = append(detail.Addresses, orders.OrderAddressView{
			Kind:      address.Kind,
			FirstName: address.FirstName, LastName: address.LastName, Company: address.Company,
			Address1: address.Address1, Address2: address.Address2,
			City: address.City, State: address.State, Postcode: address.Postcode,
			Country: address.Country, Email: address.Email, Phone: address.Phone,
		})
	}
	for _, event := range history {
		detail.StatusHistory = append(detail.StatusHistory, orders.OrderStatusEvent{
			OrderRevision: event.OrderRevision, ProviderStatus: event.ProviderStatus,
			CanonicalStatus: event.CanonicalStatus,
			ObservedAt:      event.ObservedAt.Time.UTC().Format(time.RFC3339),
		})
	}
	return detail, nil
}

// CountOrdersByStatusForStore returns Store-scoped counts per canonical status.
func (d CommerceOrders) CountOrdersByStatusForStore(ctx context.Context, storeID, provider string) ([]orders.OrderStatusCount, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	suid, err := scopedStoreUUID(storeID)
	if err != nil {
		return nil, err
	}
	rows, err := sqlcgen.New(d.pool).CountCommerceOrdersByStatusForStore(ctx, sqlcgen.CountCommerceOrdersByStatusForStoreParams{
		StoreID: suid, Column2: provider,
	})
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "order counts", redact(err))
	}
	counts := make([]orders.OrderStatusCount, 0, len(rows))
	for _, row := range rows {
		counts = append(counts, orders.OrderStatusCount{CanonicalStatus: row.CanonicalStatus, Total: row.Total})
	}
	return counts, nil
}

// optionalString converts an empty identity to nil (NULL persistence).
func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// textFromPtr renders an optional snapshot label for persistence.
func textFromPtr(value *string) pgtype.Text {
	if value == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *value, Valid: true}
}

// int8FromPtr renders an optional snapshot minor-unit value.
func int8FromPtr(value *int64) pgtype.Int8 {
	if value == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *value, Valid: true}
}

// orderLineViewFromRow exposes the stored immutable selection snapshot
// (Phase 15-R1 F13): persisted values only — no current-catalog join —
// including the truthful unresolved flag for unknown provider
// selections (never presented as resolved, never hidden).
func orderLineViewFromRow(line sqlcgen.CommerceOnlineOrderLine) orders.OrderLineView {
	view := orders.OrderLineView{
		ExternalLineID: line.ExternalLineID, ExternalProductID: line.ExternalProductID,
		VariationID: line.VariationID, SKU: line.Sku, Name: line.Name,
		Quantity: line.Quantity, TotalMinor: orders.MinorString(line.TotalMinor),
		Mapped:                  line.Mapped,
		ProviderConfigurationID: line.ProviderConfigurationID.String,
		ConfigurationUnresolved: line.ConfigurationUnresolved,
	}
	if line.MoonlightProductID.Valid {
		productID := uuidString(line.MoonlightProductID)
		view.MoonlightProduct = &productID
	}
	if line.ConfigurationID.Valid {
		value := uuidString(line.ConfigurationID)
		view.ConfigurationID = &value
	}
	view.FrameStyleCode = optionalTextPtr(line.FrameStyleCode)
	view.FrameStyleNameAR = optionalTextPtr(line.FrameStyleNameAr)
	view.FrameStyleNameEN = optionalTextPtr(line.FrameStyleNameEn)
	view.FrameColorCode = optionalTextPtr(line.FrameColorCode)
	view.FrameColorNameAR = optionalTextPtr(line.FrameColorNameAr)
	view.FrameColorNameEN = optionalTextPtr(line.FrameColorNameEn)
	if line.ConfigurationPriceDeltaMinor.Valid {
		value := orders.MinorString(line.ConfigurationPriceDeltaMinor.Int64)
		view.ConfigurationPriceDeltaMinor = &value
	}
	if line.VariantID.Valid {
		value := uuidString(line.VariantID)
		view.VariantID = &value
	}
	view.VariantSKU = optionalTextPtr(line.VariantSku)
	if len(line.VariantAttributeSnapshot) > 0 {
		var attributes []orders.OrderLineVariantAttribute
		if err := json.Unmarshal(line.VariantAttributeSnapshot, &attributes); err != nil {
			// Corrupt snapshot bytes are never presented as data.
			attributes = nil
		}
		view.VariantAttributes = attributes
	}
	return view
}

func optionalTextPtr(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	out := value.String
	return &out
}
