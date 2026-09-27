package postgres

import (
	"bytes"
	"context"
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

// Commerce order webhook inbox, current-order projection, and read
// models on Devices (shared pool + timeouts). Webhook rows and order
// rows are durable integration state: no catalog/policy/inventory
// rebuild path touches these tables.

// InsertOrderWebhookEvent persists one delivery or classifies the
// duplicate. An identical redelivery (same hash, topic, and order
// identity) is idempotent; any contradiction — different hash, topic,
// or external order ID — is a conflict that never overwrites.
func (d Devices) InsertOrderWebhookEvent(ctx context.Context, providerKey commerce.ProviderKey, deliveryID string, topic orders.WebhookTopic, externalOrderID string, payloadHash []byte, webhookID *string) (orders.WebhookInsertOutcome, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	if _, err := commerce.ValidateProviderKey(string(providerKey)); err != nil {
		return orders.WebhookInserted, apperr.New(apperr.InvalidInput, err.Error())
	}
	if err := orders.ValidateDeliveryID(deliveryID); err != nil {
		return orders.WebhookInserted, apperr.New(apperr.InvalidInput, err.Error())
	}
	var webhook pgtype.Text
	if webhookID != nil {
		webhook = pgText(*webhookID)
	}
	q := sqlcgen.New(d.pool)
	row, err := q.InsertCommerceWebhookEvent(ctx, sqlcgen.InsertCommerceWebhookEventParams{
		ProviderKey: string(providerKey), DeliveryID: deliveryID,
		Topic: string(topic), ExternalOrderID: externalOrderID,
		PayloadHash: payloadHash, WebhookID: webhook,
	})
	if err == nil {
		_ = row
		return orders.WebhookInserted, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return orders.WebhookInserted, apperr.Wrap(apperr.Internal, "order webhook", redact(err))
	}
	existing, err := q.GetCommerceWebhookEvent(ctx, sqlcgen.GetCommerceWebhookEventParams{
		ProviderKey: string(providerKey), DeliveryID: deliveryID,
	})
	if err != nil {
		return orders.WebhookInserted, apperr.Wrap(apperr.Internal, "order webhook", redact(err))
	}
	if !bytes.Equal(existing.PayloadHash, payloadHash) ||
		existing.Topic != string(topic) || existing.ExternalOrderID != externalOrderID {
		return orders.WebhookInserted, apperr.New(apperr.Conflict, "webhook delivery already recorded with different content")
	}
	return orders.WebhookDuplicateIdentical, nil
}

// ClaimOrderWebhookEvent leases one due event to this worker. Row-level
// SKIP LOCKED keeps multi-instance workers apart; the bounded lease
// (not a permanent status) makes crashed claims eligible again.
func (d Devices) ClaimOrderWebhookEvent(ctx context.Context, owner string, lease time.Duration, now time.Time) (orders.ClaimedWebhookEvent, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	leaseUntil := pgtype.Timestamptz{Time: now.Add(lease), Valid: true}
	row, err := sqlcgen.New(d.pool).ClaimCommerceWebhookEvent(ctx, sqlcgen.ClaimCommerceWebhookEventParams{
		LeaseOwner: pgText(owner), LeaseUntil: leaseUntil,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return orders.ClaimedWebhookEvent{}, false, nil
		}
		return orders.ClaimedWebhookEvent{}, false, apperr.Wrap(apperr.Internal, "order webhook", redact(err))
	}
	topic, err := orders.ParseWebhookTopic(row.Topic)
	if err != nil {
		return orders.ClaimedWebhookEvent{}, false, apperr.Wrap(apperr.Internal, "order webhook", redact(err))
	}
	// The claim just wrote this owner, so it is always present.
	claimedOwner := row.LeaseOwner.String
	return orders.ClaimedWebhookEvent{
		ProviderKey: row.ProviderKey, DeliveryID: row.DeliveryID,
		Topic: topic, ExternalOrderID: row.ExternalOrderID,
		AttemptCount: row.AttemptCount,
		LeaseOwner:   claimedOwner, LeaseGeneration: orders.LeaseGeneration(row.LeaseGeneration),
	}, true, nil
}

// BeginOrderReconcile atomically creates or increments the per-order
// reconciliation fence and returns the caller's generation token. Must
// run before provider HTTP; never inside the projection transaction.
func (d Devices) BeginOrderReconcile(ctx context.Context, providerKey, externalOrderID string) (orders.ReconcileGeneration, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	generation, err := sqlcgen.New(d.pool).BeginOrderReconcile(ctx, sqlcgen.BeginOrderReconcileParams{
		ProviderKey: providerKey, ExternalOrderID: externalOrderID,
	})
	if err != nil {
		return 0, apperr.Wrap(apperr.Internal, "order reconcile", redact(err))
	}
	return orders.ReconcileGeneration(generation), nil
}

// FinishOrderWebhookEvent records the outcome under the claim token and
// releases the lease. A superseded claim affects zero rows and reports
// stale: the current owner controls the event. Only machine-readable
// codes persist: no prose, PII, or bodies.
func (d Devices) FinishOrderWebhookEvent(ctx context.Context, claim orders.ClaimedWebhookEvent, status string, nextAttemptAt *time.Time, processed bool, errorCode string) (orders.FinishResult, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	var next pgtype.Timestamptz
	if nextAttemptAt != nil {
		next = pgtype.Timestamptz{Time: *nextAttemptAt, Valid: true}
	}
	var processedAt pgtype.Timestamptz
	if processed {
		processedAt = pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	}
	affected, err := sqlcgen.New(d.pool).FinishCommerceWebhookEvent(ctx, sqlcgen.FinishCommerceWebhookEventParams{
		ProviderKey: claim.ProviderKey, DeliveryID: claim.DeliveryID,
		LeaseOwner: pgText(claim.LeaseOwner), LeaseGeneration: int64(claim.LeaseGeneration),
		Status: status, NextAttemptAt: next, ProcessedAt: processedAt,
		LastErrorCode: pgText(errorCode),
	})
	if err != nil {
		return orders.FinishStale, apperr.Wrap(apperr.Internal, "order webhook", redact(err))
	}
	if affected == 0 {
		return orders.FinishStale, nil
	}
	return orders.FinishApplied, nil
}

// OrderWebhookStats returns pending/retry/blocked counts plus the oldest
// pending age for operational visibility.
func (d Devices) OrderWebhookStats(ctx context.Context) (pending, retry, blocked int64, oldestPending *time.Time, err error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	row, err := sqlcgen.New(d.pool).CommerceWebhookStats(ctx)
	if err != nil {
		return 0, 0, 0, nil, apperr.Wrap(apperr.Internal, "order webhook", redact(err))
	}
	if row.OldestPending.Valid {
		at := row.OldestPending.Time
		oldestPending = &at
	}
	return row.Pending, row.Retry, row.Blocked, oldestPending, nil
}

// ReconcileProjectedOrder atomically converges one current order to a
// resolved snapshot under the caller's generation token. Lock order is
// fixed: reconciliation fence first, then order rows — no path locks in
// the opposite order. A token older than the fence's current generation
// is superseded and mutates nothing (not header, lines, history, or
// revision). A fault anywhere leaves the previous full revision
// visible. A missing fence row fails closed: every production path
// begins a generation before projecting.
func (d Devices) ReconcileProjectedOrder(ctx context.Context, snapshot orders.OrderSnapshot, fingerprint [32]byte, generation orders.ReconcileGeneration) (orders.ReconcileOutcome, error) {
	none := orders.ReconcileOutcome{}
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return none, apperr.Wrap(apperr.Internal, "order reconcile", redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)

	// Fence first, order rows second: the locked generation read is
	// the single ordering point. A concurrent Begin either commits
	// before this lock (we observe its generation) or blocks behind
	// our projection commit. No DB lock ever spans provider HTTP: the
	// fence was obtained before the GET and released long ago.
	fence, err := q.LockOrderReconcileFence(ctx, sqlcgen.LockOrderReconcileFenceParams{
		ProviderKey: snapshot.ProviderKey, ExternalOrderID: snapshot.ExternalOrderID,
	})
	if err != nil {
		_ = tx.Rollback(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			return none, apperr.Wrap(apperr.Internal, "order reconcile", redact(errors.New("no reconcile fence")))
		}
		return none, apperr.Wrap(apperr.Internal, "order reconcile", redact(err))
	}
	if fence != int64(generation) {
		_ = tx.Rollback(ctx)
		return orders.ReconcileOutcome{Superseded: true}, nil
	}

	resolved, err := resolveOrderLines(ctx, q, snapshot)
	if err != nil {
		return none, err
	}
	fingerprint = orders.Fingerprint(resolved)

	existing, err := q.GetCommerceOrder(ctx, sqlcgen.GetCommerceOrderParams{
		ProviderKey: snapshot.ProviderKey, ExternalOrderID: snapshot.ExternalOrderID,
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return none, apperr.Wrap(apperr.Internal, "order reconcile", redact(err))
	}
	if err == nil {
		if bytes.Equal(existing.Fingerprint, fingerprint[:]) {
			if err := tx.Commit(ctx); err != nil {
				return none, apperr.Wrap(apperr.Internal, "order reconcile", redact(err))
			}
			return orders.ReconcileOutcome{Revision: existing.Revision}, nil
		}
		resolvedRevision := existing.Revision + 1
		if err := writeProjectedOrder(ctx, q, resolved, fingerprint, resolvedRevision); err != nil {
			return none, err
		}
		if existing.ProviderStatus != resolved.ProviderStatus || existing.CanonicalStatus != string(resolved.Canonical) {
			if err := q.InsertCommerceOrderStatusHistory(ctx, sqlcgen.InsertCommerceOrderStatusHistoryParams{
				ProviderKey: resolved.ProviderKey, ExternalOrderID: resolved.ExternalOrderID,
				OrderRevision: resolvedRevision, ProviderStatus: resolved.ProviderStatus,
				CanonicalStatus:    string(resolved.Canonical),
				ProviderModifiedAt: pgTime(resolved.ModifiedAt),
			}); err != nil {
				return none, apperr.Wrap(apperr.Internal, "order reconcile", redact(err))
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return none, apperr.Wrap(apperr.Internal, "order reconcile", redact(err))
		}
		return orders.ReconcileOutcome{Revision: resolvedRevision, Changed: true}, nil
	}
	if err := writeProjectedOrder(ctx, q, resolved, fingerprint, 1); err != nil {
		return none, err
	}
	if err := q.InsertCommerceOrderStatusHistory(ctx, sqlcgen.InsertCommerceOrderStatusHistoryParams{
		ProviderKey: resolved.ProviderKey, ExternalOrderID: resolved.ExternalOrderID,
		OrderRevision: 1, ProviderStatus: resolved.ProviderStatus,
		CanonicalStatus:    string(resolved.Canonical),
		ProviderModifiedAt: pgTime(resolved.ModifiedAt),
	}); err != nil {
		return none, apperr.Wrap(apperr.Internal, "order reconcile", redact(err))
	}
	if err := tx.Commit(ctx); err != nil {
		return none, apperr.Wrap(apperr.Internal, "order reconcile", redact(err))
	}
	return orders.ReconcileOutcome{Revision: 1, Changed: true}, nil
}

// LoadProjectedOrder reconstructs the current normalized snapshot for
// fingerprint continuity (tombstones, deletion checks). Missing orders
// report found=false.
func (d Devices) LoadProjectedOrder(ctx context.Context, providerKey, externalOrderID string) (orders.OrderSnapshot, int64, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	q := sqlcgen.New(d.pool)
	header, err := q.GetCommerceOrder(ctx, sqlcgen.GetCommerceOrderParams{
		ProviderKey: providerKey, ExternalOrderID: externalOrderID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return orders.OrderSnapshot{}, 0, false, nil
		}
		return orders.OrderSnapshot{}, 0, false, apperr.Wrap(apperr.Internal, "order reconcile", redact(err))
	}
	lines, err := q.ListCommerceOrderLines(ctx, sqlcgen.ListCommerceOrderLinesParams{
		ProviderKey: providerKey, ExternalOrderID: externalOrderID,
	})
	if err != nil {
		return orders.OrderSnapshot{}, 0, false, apperr.Wrap(apperr.Internal, "order reconcile", redact(err))
	}
	addresses, err := q.ListCommerceOrderAddresses(ctx, sqlcgen.ListCommerceOrderAddressesParams{
		ProviderKey: providerKey, ExternalOrderID: externalOrderID,
	})
	if err != nil {
		return orders.OrderSnapshot{}, 0, false, apperr.Wrap(apperr.Internal, "order reconcile", redact(err))
	}
	snapshot := orders.OrderSnapshot{
		ProviderKey: header.ProviderKey, ExternalOrderID: header.ExternalOrderID,
		OrderNumber:    header.OrderNumber,
		ProviderStatus: header.ProviderStatus, Canonical: orders.CanonicalStatus(header.CanonicalStatus),
		Currency:      header.Currency,
		DiscountMinor: header.DiscountMinor, ShippingMinor: header.ShippingMinor,
		CartTaxMinor: header.CartTaxMinor, TotalTaxMinor: header.TotalTaxMinor,
		TotalMinor: header.TotalMinor, PricesIncludeTax: header.PricesIncludeTax,
		CreatedAt: header.CreatedAt.Time, ModifiedAt: header.ModifiedAt.Time,
		PaymentMethod: header.PaymentMethod, PaymentMethodTitle: header.PaymentMethodTitle,
		Customer: orders.Customer{
			FirstName: header.CustomerFirstName, LastName: header.CustomerLastName,
			Email: header.CustomerEmail, Phone: header.CustomerPhone,
		},
		ProviderDeleted: header.ProviderDeleted,
		MappingComplete: header.MappingComplete, UnmappedLines: int(header.UnmappedLines),
	}
	if header.PaidAt.Valid {
		paid := header.PaidAt.Time
		snapshot.PaidAt = &paid
	}
	if header.CompletedAt.Valid {
		completed := header.CompletedAt.Time
		snapshot.CompletedAt = &completed
	}
	for _, line := range lines {
		resolved := orders.OrderLine{
			ExternalLineID: line.ExternalLineID, ExternalProductID: line.ExternalProductID,
			VariationID: line.VariationID, SKU: line.Sku, Name: line.Name,
			Quantity:      line.Quantity,
			SubtotalMinor: line.SubtotalMinor, SubtotalTaxMinor: line.SubtotalTaxMinor,
			TotalMinor: line.TotalMinor, TotalTaxMinor: line.TotalTaxMinor,
			Mapped: line.Mapped, UnsupportedReason: line.UnsupportedReason,
		}
		if line.MoonlightProductID.Valid {
			productID := uuidString(line.MoonlightProductID)
			resolved.MoonlightProduct = &productID
		}
		snapshot.Lines = append(snapshot.Lines, resolved)
	}
	for _, address := range addresses {
		target := &snapshot.Billing
		if address.Kind == "shipping" {
			target = &snapshot.Shipping
		}
		*target = orders.Address{
			Kind:      address.Kind,
			FirstName: address.FirstName, LastName: address.LastName, Company: address.Company,
			Address1: address.Address1, Address2: address.Address2,
			City: address.City, State: address.State, Postcode: address.Postcode,
			Country: address.Country, Email: address.Email, Phone: address.Phone,
		}
	}
	return snapshot, header.Revision, true, nil
}

// resolveOrderLines fills MoonlightProduct/Mapped/completeness from the
// frozen generic mapping. Variation lines never resolve: variation IDs
// live in a different identity space and must not fabricate mappings.
func resolveOrderLines(ctx context.Context, q *sqlcgen.Queries, snapshot orders.OrderSnapshot) (orders.OrderSnapshot, error) {
	unmapped := 0
	for i := range snapshot.Lines {
		line := &snapshot.Lines[i]
		line.MoonlightProduct = nil
		line.Mapped = false
		line.UnsupportedReason = ""
		if line.VariationID != 0 {
			line.UnsupportedReason = "variation"
			unmapped++
			continue
		}
		if line.ExternalProductID == "" {
			unmapped++
			continue
		}
		mapping, err := q.FindCommerceProductMappingByExternal(ctx, sqlcgen.FindCommerceProductMappingByExternalParams{
			ProviderKey: snapshot.ProviderKey, ExternalProductID: line.ExternalProductID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				unmapped++
				continue
			}
			return orders.OrderSnapshot{}, apperr.Wrap(apperr.Internal, "order reconcile", redact(err))
		}
		productID := uuidString(mapping.ProductID)
		line.MoonlightProduct = &productID
		line.Mapped = true
	}
	snapshot.UnmappedLines = unmapped
	snapshot.MappingComplete = unmapped == 0
	return snapshot, nil
}

func writeProjectedOrder(ctx context.Context, q *sqlcgen.Queries, snapshot orders.OrderSnapshot, fingerprint [32]byte, revision int64) error {
	if err := q.UpsertCommerceOrder(ctx, sqlcgen.UpsertCommerceOrderParams{
		ProviderKey: snapshot.ProviderKey, ExternalOrderID: snapshot.ExternalOrderID,
		OrderNumber:    snapshot.OrderNumber,
		ProviderStatus: snapshot.ProviderStatus, CanonicalStatus: string(snapshot.Canonical),
		Currency:      snapshot.Currency,
		DiscountMinor: snapshot.DiscountMinor, ShippingMinor: snapshot.ShippingMinor,
		CartTaxMinor: snapshot.CartTaxMinor, TotalTaxMinor: snapshot.TotalTaxMinor,
		TotalMinor: snapshot.TotalMinor, PricesIncludeTax: snapshot.PricesIncludeTax,
		CreatedAt: pgTime(snapshot.CreatedAt), ModifiedAt: pgTime(snapshot.ModifiedAt),
		PaidAt: pgTimePtr(snapshot.PaidAt), CompletedAt: pgTimePtr(snapshot.CompletedAt),
		PaymentMethod: snapshot.PaymentMethod, PaymentMethodTitle: snapshot.PaymentMethodTitle,
		CustomerFirstName: snapshot.Customer.FirstName, CustomerLastName: snapshot.Customer.LastName,
		CustomerEmail: snapshot.Customer.Email, CustomerPhone: snapshot.Customer.Phone,
		Revision: revision, Fingerprint: fingerprint[:],
		ProviderDeleted: snapshot.ProviderDeleted,
		MappingComplete: snapshot.MappingComplete, UnmappedLines: int32(snapshot.UnmappedLines),
	}); err != nil {
		return apperr.Wrap(apperr.Internal, "order reconcile", redact(err))
	}
	if err := q.DeleteCommerceOrderLines(ctx, sqlcgen.DeleteCommerceOrderLinesParams{
		ProviderKey: snapshot.ProviderKey, ExternalOrderID: snapshot.ExternalOrderID,
	}); err != nil {
		return apperr.Wrap(apperr.Internal, "order reconcile", redact(err))
	}
	if err := q.DeleteCommerceOrderAddresses(ctx, sqlcgen.DeleteCommerceOrderAddressesParams{
		ProviderKey: snapshot.ProviderKey, ExternalOrderID: snapshot.ExternalOrderID,
	}); err != nil {
		return apperr.Wrap(apperr.Internal, "order reconcile", redact(err))
	}
	for _, line := range snapshot.Lines {
		var productUID pgtype.UUID
		if line.MoonlightProduct != nil {
			parsed, err := parseUUID(*line.MoonlightProduct)
			if err != nil {
				return apperr.Wrap(apperr.Internal, "order reconcile", redact(err))
			}
			productUID = parsed
		}
		if err := q.InsertCommerceOrderLine(ctx, sqlcgen.InsertCommerceOrderLineParams{
			ProviderKey: snapshot.ProviderKey, ExternalOrderID: snapshot.ExternalOrderID,
			ExternalLineID: line.ExternalLineID, ExternalProductID: line.ExternalProductID,
			VariationID: line.VariationID, Sku: line.SKU, Name: line.Name,
			Quantity:      line.Quantity,
			SubtotalMinor: line.SubtotalMinor, SubtotalTaxMinor: line.SubtotalTaxMinor,
			TotalMinor: line.TotalMinor, TotalTaxMinor: line.TotalTaxMinor,
			MoonlightProductID: productUID, Mapped: line.Mapped,
			UnsupportedReason: line.UnsupportedReason,
		}); err != nil {
			return apperr.Wrap(apperr.Internal, "order reconcile", redact(err))
		}
	}
	for _, address := range []orders.Address{snapshot.Billing, snapshot.Shipping} {
		if err := q.InsertCommerceOrderAddress(ctx, sqlcgen.InsertCommerceOrderAddressParams{
			ProviderKey: snapshot.ProviderKey, ExternalOrderID: snapshot.ExternalOrderID,
			Kind:      address.Kind,
			FirstName: address.FirstName, LastName: address.LastName, Company: address.Company,
			Address1: address.Address1, Address2: address.Address2,
			City: address.City, State: address.State, Postcode: address.Postcode,
			Country: address.Country, Email: address.Email, Phone: address.Phone,
		}); err != nil {
			return apperr.Wrap(apperr.Internal, "order reconcile", redact(err))
		}
	}
	return nil
}

// ListOrderSummaries returns one dashboard page with deterministic
// keyset pagination (created_at DESC, provider_key, external_order_id).
// Empty provider/status means no filter; limit is bounded.
func (d Devices) ListOrderSummaries(ctx context.Context, provider, status string, limit int, cursor *orders.OrderCursor) ([]orders.OrderSummary, error) {
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
func (d Devices) ListOrderPage(ctx context.Context, provider, status string, limit int, cursor *orders.OrderCursor) (orders.OrderPage, error) {
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
func (d Devices) listOrderRows(ctx context.Context, provider, status string, limit int, cursor *orders.OrderCursor) ([]sqlcgen.ListCommerceOrdersRow, error) {
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
func (d Devices) GetOrderDetail(ctx context.Context, providerKey, externalOrderID string) (orders.OrderDetail, error) {
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
		view := orders.OrderLineView{
			ExternalLineID: line.ExternalLineID, ExternalProductID: line.ExternalProductID,
			VariationID: line.VariationID, SKU: line.Sku, Name: line.Name,
			Quantity: line.Quantity, TotalMinor: orders.MinorString(line.TotalMinor),
			Mapped: line.Mapped,
		}
		if line.MoonlightProductID.Valid {
			productID := uuidString(line.MoonlightProductID)
			view.MoonlightProduct = &productID
		}
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
func (d Devices) CountOrdersByStatus(ctx context.Context, provider string) ([]orders.OrderStatusCount, error) {
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
func (d Devices) OrderInboxStats(ctx context.Context) (orders.WebhookQueueStats, error) {
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

var (
	_ orders.OrderReader = Devices{}
	_ orders.OrderStore  = Devices{}
)
