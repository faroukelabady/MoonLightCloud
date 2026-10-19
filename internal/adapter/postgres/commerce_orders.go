package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce/orders"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Commerce order webhook inbox, current-order projection, and read
// models on CommerceOrders (shared pool + timeouts). Webhook rows and order
// rows are durable integration state: no catalog/policy/inventory
// rebuild path touches these tables.

// InsertOrderWebhookEvent persists one delivery or classifies the
// duplicate. An identical redelivery (same hash, topic, and order
// identity) is idempotent; any contradiction — different hash, topic,
// or external order ID — is a conflict that never overwrites.
func (d CommerceOrders) InsertOrderWebhookEvent(ctx context.Context, providerKey commerce.ProviderKey, deliveryID string, topic orders.WebhookTopic, externalOrderID string, payloadHash []byte, webhookID *string) (orders.WebhookInsertOutcome, error) {
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
func (d CommerceOrders) ClaimOrderWebhookEvent(ctx context.Context, owner string, lease time.Duration, now time.Time) (orders.ClaimedWebhookEvent, bool, error) {
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
func (d CommerceOrders) BeginOrderReconcile(ctx context.Context, providerKey, externalOrderID string) (orders.ReconcileGeneration, error) {
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
func (d CommerceOrders) FinishOrderWebhookEvent(ctx context.Context, claim orders.ClaimedWebhookEvent, status string, nextAttemptAt *time.Time, processed bool, errorCode string) (orders.FinishResult, error) {
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
func (d CommerceOrders) OrderWebhookStats(ctx context.Context) (pending, retry, blocked int64, oldestPending *time.Time, err error) {
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
func (d CommerceOrders) ReconcileProjectedOrder(ctx context.Context, snapshot orders.OrderSnapshot, fingerprint [32]byte, generation orders.ReconcileGeneration) (orders.ReconcileOutcome, error) {
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

	resolved, lineStores, err := resolveOrderLines(ctx, q, snapshot)
	if err != nil {
		return none, err
	}
	fingerprint = orders.Fingerprint(resolved)

	// Phase 9C ownership gate: the single proven Store derives from
	// unanimous mapped-line evidence read above (no extra queries, no
	// binding lookups, never provider payloads). Mixed ownership blocks
	// the attempt before any order/history write. Tombstones preserve
	// the established Store and never block on mixed evidence: deletion
	// changes lifecycle, not attribution.
	derived, mixed := orders.DeriveOrderStore(resolved, lineStores)
	tombstone := resolved.ProviderDeleted
	existing, err := q.GetCommerceOrder(ctx, sqlcgen.GetCommerceOrderParams{
		ProviderKey: snapshot.ProviderKey, ExternalOrderID: snapshot.ExternalOrderID,
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return none, apperr.Wrap(apperr.Internal, "order reconcile", redact(err))
	}
	if err == nil {
		existingStore := storeString(existing.StoreID)
		writeStore, storeChanged, scopeErr := orders.DecideOrderStore(existingStore, derived, mixed, tombstone)
		if scopeErr != nil {
			_ = tx.Rollback(ctx)
			return none, scopeErr
		}
		if bytes.Equal(existing.Fingerprint, fingerprint[:]) && !storeChanged {
			if err := tx.Commit(ctx); err != nil {
				return none, apperr.Wrap(apperr.Internal, "order reconcile", redact(err))
			}
			return orders.ReconcileOutcome{Revision: existing.Revision}, nil
		}
		resolvedRevision := existing.Revision + 1
		if err := writeProjectedOrder(ctx, q, resolved, fingerprint, resolvedRevision, storeUUID(writeStore)); err != nil {
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
	newStore, scopeErr := orders.DecideNewOrderStore(derived, mixed, tombstone)
	if scopeErr != nil {
		_ = tx.Rollback(ctx)
		return none, scopeErr
	}
	if err := writeProjectedOrder(ctx, q, resolved, fingerprint, 1, storeUUID(newStore)); err != nil {
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
func (d CommerceOrders) LoadProjectedOrder(ctx context.Context, providerKey, externalOrderID string) (orders.OrderSnapshot, int64, bool, error) {
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
		if line.VariantID.Valid {
			variantID := uuidString(line.VariantID)
			resolved.VariantID = &variantID
		}
		resolved.VariantSKU = optionalTextPtr(line.VariantSku)
		if len(line.VariantAttributeSnapshot) > 0 {
			var attributes []orders.OrderLineVariantAttribute
			if err := json.Unmarshal(line.VariantAttributeSnapshot, &attributes); err != nil {
				return orders.OrderSnapshot{}, 0, false, apperr.Wrap(apperr.Internal, "order reconcile", redact(err))
			}
			resolved.VariantAttributeSnapshot = attributes
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
// It also returns per-line proven Store evidence (productID to Store,
// nil for legacy mappings) for Phase 9C order ownership derivation, read
// from the same mapping rows in the same pass (no extra queries).
func resolveOrderLines(ctx context.Context, q *sqlcgen.Queries, snapshot orders.OrderSnapshot) (orders.OrderSnapshot, map[string]*string, error) {
	unmapped := 0
	// Phase 15-R2 F13: an order whose provider selection is unresolved
	// is NEVER "fully mapped". Base-Product mapping completeness keeps
	// its frozen meaning (unmapped_lines); the aggregate
	// mapping_complete claim additionally requires resolved selections.
	unresolvedSelections := 0
	lineStores := map[string]*string{}
	for i := range snapshot.Lines {
		line := &snapshot.Lines[i]
		line.MoonlightProduct = nil
		line.Mapped = false
		line.UnsupportedReason = ""
		line.VariantID = nil
		line.VariantSKU = nil
		line.VariantAttributeSnapshot = nil
		if line.ProviderConfigurationID != "" || line.VariationID != 0 {
			// Phase 15 §117/§118: a provider configuration identity is
			// resolved through the durable configuration mapping — never
			// by displayed labels. The BASE product still resolves below;
			// an unknown configuration stays truthful (raw identity +
			// unresolved flag, §60) instead of guessing.
			configurationID := line.ProviderConfigurationID
			if configurationID == "" {
				configurationID = strconv.FormatInt(line.VariationID, 10)
				line.ProviderConfigurationID = configurationID
			}
			mapping, err := q.FindCommerceProductConfigurationMappingByExternal(ctx, sqlcgen.FindCommerceProductConfigurationMappingByExternalParams{
				ProviderKey: snapshot.ProviderKey, ExternalProductID: line.ExternalProductID,
				ExternalConfigurationID: configurationID,
			})
			if err != nil {
				if !errors.Is(err, pgx.ErrNoRows) {
					return orders.OrderSnapshot{}, nil, apperr.Wrap(apperr.Internal, "order reconcile", redact(err))
				}
				line.ConfigurationUnresolved = true
			} else {
				// Snapshot the selection AT INGESTION (§56/§129): the
				// projected configuration is copied into the order line,
				// never joined later.
				resolved := uuidString(mapping.ConfigurationID)
				// Phase 15-R1 F14: the zero sentinel is an integration
				// mapping key only — a NO-FRAME selection normalizes to
				// "no business configuration" while the raw provider
				// identity is preserved above.
				if resolved != catalog.NoFrameSentinelID {
					line.ConfigurationID = &resolved
					if row, err := q.CatalogProductConfigurationByID(ctx, mapping.ConfigurationID); err == nil {
						snapshotConfiguration(line, row, snapshot.Currency)
					} else if !errors.Is(err, pgx.ErrNoRows) {
						return orders.OrderSnapshot{}, nil, apperr.Wrap(apperr.Internal, "order reconcile", redact(err))
					}
				}
			}
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
			return orders.OrderSnapshot{}, nil, apperr.Wrap(apperr.Internal, "order reconcile", redact(err))
		}
		productID := uuidString(mapping.ProductID)
		line.MoonlightProduct = &productID
		line.Mapped = true
		lineStores[productID] = storeString(mapping.StoreID)
		// Phase 17 §33-§37: provider variation → durable variant mapping
		// → MoonLight variant identity snapshot (00034). Never label
		// matching; an unresolved variation stays truthful NULL.
		if line.ProviderVariantID != "" {
			if err := snapshotOrderLineVariant(ctx, q, snapshot.ProviderKey, line, mapping.ProductID); err != nil {
				return orders.OrderSnapshot{}, nil, err
			}
		}
	}
	for _, line := range snapshot.Lines {
		if line.ConfigurationUnresolved {
			unresolvedSelections++
		}
	}
	snapshot.UnmappedLines = unmapped
	snapshot.MappingComplete = unmapped == 0 && unresolvedSelections == 0
	return snapshot, lineStores, nil
}

// snapshotOrderLineVariant resolves one provider variation to its
// durable MoonLight ProductVariant mapping (Phase 17 §33-§37) and
// captures the immutable variant snapshot at ingestion (00034):
// identity, SKU, and option attributes are copied — later rename, price
// change, disable or tombstone can never rewrite history. A product
// with exactly ONE mapped variant owns every frame-layer selection of
// that variant (all frame choices consume that one variant's stock
// pool), so an unresolved variation on a single-variant product adopts
// that variant's identity; products with several variants require the
// exact provider variation mapping and NEVER guess.
func snapshotOrderLineVariant(ctx context.Context, q *sqlcgen.Queries, providerKey string, line *orders.OrderLine, productID pgtype.UUID) error {
	var variantMapping *sqlcgen.CommerceProductVariantMapping
	row, err := q.FindCommerceProductVariantMappingByExternal(ctx, sqlcgen.FindCommerceProductVariantMappingByExternalParams{
		ProviderKey: providerKey, ExternalProductID: line.ExternalProductID, ExternalVariantID: line.ProviderVariantID,
	})
	switch {
	case err == nil:
		variantMapping = &row
	case errors.Is(err, pgx.ErrNoRows):
		rows, listErr := q.ListCommerceProductVariantMappings(ctx, sqlcgen.ListCommerceProductVariantMappingsParams{
			ProviderKey: providerKey, ProductID: productID,
		})
		if listErr != nil {
			return apperr.Wrap(apperr.Internal, "order reconcile", redact(listErr))
		}
		if len(rows) == 1 {
			variantMapping = &rows[0]
		}
	default:
		return apperr.Wrap(apperr.Internal, "order reconcile", redact(err))
	}
	if variantMapping == nil {
		return nil
	}
	variantID := uuidString(variantMapping.VariantID)
	line.VariantID = &variantID
	captured := ""
	if variantRow, err := q.CatalogProductVariantByID(ctx, variantMapping.VariantID); err == nil {
		captured = variantRow.Sku
		attrs, attrErr := q.CatalogProductVariantAttributes(ctx, variantMapping.VariantID)
		if attrErr != nil {
			return apperr.Wrap(apperr.Internal, "order reconcile", redact(attrErr))
		}
		for _, attr := range attrs {
			entry := orders.OrderLineVariantAttribute{
				DefinitionCode: attr.DefinitionCode, ValueCode: attr.ValueCode,
				NameAR: attr.NameAr, DefinitionNameAR: attr.DefinitionNameAr,
				Position: int(attr.Position),
			}
			if attr.NameEn.Valid {
				value := attr.NameEn.String
				entry.NameEN = &value
			}
			if attr.DefinitionNameEn.Valid {
				value := attr.DefinitionNameEn.String
				entry.DefinitionNameEN = &value
			}
			line.VariantAttributeSnapshot = append(line.VariantAttributeSnapshot, entry)
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return apperr.Wrap(apperr.Internal, "order reconcile", redact(err))
	}
	if captured == "" {
		captured = line.SKU
	}
	line.VariantSKU = &captured
	return nil
}

// snapshotConfiguration copies the selection into the order line at
// ingestion (§56/§57/§58): historical display and pricing survive later
// rename, delta changes and disabling.
func snapshotConfiguration(line *orders.OrderLine, row sqlcgen.CatalogProductConfigurationByIDRow, currency string) {
	styleCode := row.StyleCode
	line.FrameStyleCode = &styleCode
	styleAR := row.StyleNameAr
	line.FrameStyleNameAR = &styleAR
	if row.StyleNameEn.Valid {
		value := row.StyleNameEn.String
		line.FrameStyleNameEN = &value
	}
	colorCode := row.ColorCode
	line.FrameColorCode = &colorCode
	colorAR := row.ColorNameAr
	line.FrameColorNameAR = &colorAR
	if row.ColorNameEn.Valid {
		value := row.ColorNameEn.String
		line.FrameColorNameEN = &value
	}
	// Phase 15-R1 F12: the delta is captured in the ORDER's currency.
	// A currency without a defined delta stays NULL — never zero, never
	// the other currency's value, never FX.
	if strings.EqualFold(currency, "USD") {
		if row.PriceDeltaUsdMinor.Valid {
			delta := row.PriceDeltaUsdMinor.Int64
			line.ConfigurationPriceDeltaMinor = &delta
		}
	} else {
		delta := row.PriceDeltaEgpMinor
		line.ConfigurationPriceDeltaMinor = &delta
	}
}

func writeProjectedOrder(ctx context.Context, q *sqlcgen.Queries, snapshot orders.OrderSnapshot, fingerprint [32]byte, revision int64, storeID pgtype.UUID) error {
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
		StoreID: storeID,
	}); err != nil {
		return apperr.Wrap(apperr.Internal, "order reconcile", redact(err))
	}
	// Phase 15-R2 F11: never delete-then-reinsert — that bypassed the
	// snapshot-preserving ON CONFLICT entirely. Lines synchronize by
	// stable provider line identity: surviving lines keep their first
	// captured selection (labels, codes, delta, currency); only lines the
	// provider no longer sends are removed. Same transaction as the
	// header/line writes.
	lineIDs := make([]int64, 0, len(snapshot.Lines))
	for _, line := range snapshot.Lines {
		lineIDs = append(lineIDs, line.ExternalLineID)
	}
	if len(lineIDs) > 0 {
		if err := q.DeleteCommerceOrderLinesNotIn(ctx, sqlcgen.DeleteCommerceOrderLinesNotInParams{
			ProviderKey: snapshot.ProviderKey, ExternalOrderID: snapshot.ExternalOrderID,
			Column3: lineIDs,
		}); err != nil {
			return apperr.Wrap(apperr.Internal, "order reconcile", redact(err))
		}
	} else {
		if err := q.DeleteCommerceOrderLines(ctx, sqlcgen.DeleteCommerceOrderLinesParams{
			ProviderKey: snapshot.ProviderKey, ExternalOrderID: snapshot.ExternalOrderID,
		}); err != nil {
			return apperr.Wrap(apperr.Internal, "order reconcile", redact(err))
		}
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
		params := sqlcgen.InsertCommerceOrderLineParams{
			ProviderKey: snapshot.ProviderKey, ExternalOrderID: snapshot.ExternalOrderID,
			ExternalLineID: line.ExternalLineID, ExternalProductID: line.ExternalProductID,
			VariationID: line.VariationID, Sku: line.SKU, Name: line.Name,
			Quantity:      line.Quantity,
			SubtotalMinor: line.SubtotalMinor, SubtotalTaxMinor: line.SubtotalTaxMinor,
			TotalMinor: line.TotalMinor, TotalTaxMinor: line.TotalTaxMinor,
			MoonlightProductID: productUID, Mapped: line.Mapped,
			UnsupportedReason:            line.UnsupportedReason,
			ProviderConfigurationID:      textFromPtr(optionalString(line.ProviderConfigurationID)),
			ConfigurationUnresolved:      line.ConfigurationUnresolved,
			FrameStyleCode:               textFromPtr(line.FrameStyleCode),
			FrameStyleNameAr:             textFromPtr(line.FrameStyleNameAR),
			FrameStyleNameEn:             textFromPtr(line.FrameStyleNameEN),
			FrameColorCode:               textFromPtr(line.FrameColorCode),
			FrameColorNameAr:             textFromPtr(line.FrameColorNameAR),
			FrameColorNameEn:             textFromPtr(line.FrameColorNameEN),
			ConfigurationPriceDeltaMinor: int8FromPtr(line.ConfigurationPriceDeltaMinor),
			VariantSku:                   textFromPtr(line.VariantSKU),
		}
		if line.VariantID != nil {
			vuid, err := parseUUID(*line.VariantID)
			if err != nil {
				return apperr.Wrap(apperr.Internal, "order reconcile", redact(err))
			}
			params.VariantID = vuid
		}
		if len(line.VariantAttributeSnapshot) > 0 {
			encoded, err := json.Marshal(line.VariantAttributeSnapshot)
			if err != nil {
				return apperr.Wrap(apperr.Internal, "order reconcile", redact(err))
			}
			params.VariantAttributeSnapshot = encoded
		}
		if line.ConfigurationID != nil {
			cuid, err := parseUUID(*line.ConfigurationID)
			if err == nil {
				params.ConfigurationID = cuid
			}
		}
		if err := q.InsertCommerceOrderLine(ctx, params); err != nil {
			return apperr.Wrap(apperr.Internal, "order reconcile", redact(err))
		}
	}
	// Re-read the durable verdict after immutable selection fields have survived
	// the line upserts. A newly discovered mapping cannot repair history.
	if err := q.RefreshCommerceOrderMappingCompleteness(ctx, sqlcgen.RefreshCommerceOrderMappingCompletenessParams{
		ProviderKey: snapshot.ProviderKey, ExternalOrderID: snapshot.ExternalOrderID,
	}); err != nil {
		return apperr.Wrap(apperr.Internal, "order reconcile", redact(err))
	}
	for _, address := range []orders.Address{snapshot.Billing, snapshot.Shipping} {
		// Provider orders may omit billing/shipping addresses; empty
		// kinds are never rows (the kind CHECK is authoritative).
		if address.Kind == "" {
			continue
		}
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

// Phase 9C Store-scoped order reads. Root ownership controls the entire
// graph: list, detail, and counts predicate on the order root, so lines,
// addresses, history, and customer PII can never cross a Store boundary.
// Legacy NULL rows never match a Store scope. Global reads above keep
// documented ALL+legacy behavior for administration. No HTTP change: the
// Store filter stays internal until 9D.
