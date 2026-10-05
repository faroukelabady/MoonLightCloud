package catalogadmin

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
)

// NewCommand is the validated creation intent.
type NewCommand struct {
	ID               string
	StoreID          string
	Type             string
	Version          int
	EntityID         string
	Payload          []byte
	PayloadHash      string
	ExpectedRevision int64
	Actor            string
}

// CommandView is one command row for API/UI.
type CommandView struct {
	ID               string       `json:"id"`
	StoreID          string       `json:"store_id"`
	Type             string       `json:"type"`
	Version          int          `json:"version"`
	EntityID         string       `json:"entity_id"`
	Payload          []byte       `json:"payload,omitempty"`
	PayloadHash      string       `json:"payload_hash"`
	ExpectedRevision int64        `json:"expected_revision"`
	Actor            string       `json:"actor"`
	Status           string       `json:"status"`
	Aggregate        string       `json:"aggregate"`
	Converged        bool         `json:"converged"`
	Targets          []TargetView `json:"targets,omitempty"`
	CreatedAt        time.Time    `json:"created_at"`
	UpdatedAt        time.Time    `json:"updated_at"`
}

// TargetView is one per-device target row.
type TargetView struct {
	ID           string `json:"id"`
	CommandID    string `json:"command_id"`
	DeviceID     string `json:"device_id"`
	DeviceName   string `json:"device_name,omitempty"`
	Status       string `json:"status"`
	ResultCode   string `json:"result_code,omitempty"`
	EntityID     string `json:"entity_id,omitempty"`
	PreRevision  int64  `json:"pre_revision"`
	PostRevision int64  `json:"post_revision"`
	Capable      *bool  `json:"capable,omitempty"`
}

// DueTarget is one deliverable target for a polling device.
type DueTarget struct {
	TargetID    string
	CommandID   string
	DeviceID    string
	Status      string
	Type        string
	Version     int
	StoreID     string
	EntityID    string
	Payload     []byte
	PayloadHash string
}

// BoundDevice is one currently Store-bound device.
type BoundDevice struct {
	DeviceID string
	Name     string
}

// AdminProductRow is one Store-scoped Product for the operator list.
type AdminProductRow struct {
	ProductID             string `json:"product_id"`
	SKU                   string `json:"sku"`
	NameAR                string `json:"name_ar"`
	NameEN                string `json:"name_en"`
	IsActive              bool   `json:"is_active"`
	CatalogRevision       int64  `json:"catalog_revision"`
	SellOnline            bool   `json:"sell_online"`
	StockQuantity         int64  `json:"stock_quantity"`
	ConfigurationRevision int64  `json:"configuration_revision"`
	HasPending            bool   `json:"has_pending"`
}

// AdminProductDetail is the full projection snapshot an editor needs:
// current values, all three revision streams, read-only stock/SKU.
type AdminProductDetail struct {
	ProductID             string   `json:"product_id"`
	SKU                   string   `json:"sku"`
	NameAR                string   `json:"name_ar"`
	NameEN                string   `json:"name_en"`
	DescriptionAR         string   `json:"description_ar"`
	DescriptionEN         string   `json:"description_en"`
	WidthCM               *int     `json:"width_cm"`
	HeightCM              *int     `json:"height_cm"`
	TopCategoryID         string   `json:"top_category_id"`
	SubcategoryIDs        []string `json:"subcategory_ids"`
	TagIDs                []string `json:"tag_ids"`
	EGPPriceMinor         string   `json:"egp_price_minor"`
	USDPriceMinor         *string  `json:"usd_price_minor"`
	CostMinor             string   `json:"cost_minor"`
	IsActive              bool     `json:"is_active"`
	SellOnline            bool     `json:"sell_online"`
	SellOffline           bool     `json:"sell_offline"`
	StockQuantity         int64    `json:"stock_quantity"`
	CatalogRevision       int64    `json:"catalog_revision"`
	SalesPolicyRevision   int64    `json:"sales_policy_revision"`
	ConfigurationRevision int64    `json:"configuration_revision"`
	HasPending            bool     `json:"has_pending"`
}

// AdminCategoryRow is one Store-scoped Category with DAG position.
type AdminCategoryRow struct {
	CategoryID      string   `json:"category_id"`
	NameAR          string   `json:"name_ar"`
	NameEN          string   `json:"name_en"`
	Status          string   `json:"status"`
	OnlineEnabled   bool     `json:"online_enabled"`
	ParentIDs       []string `json:"parent_ids"`
	CatalogRevision int64    `json:"catalog_revision"`
	HasPending      bool     `json:"has_pending"`
}

// AdminTagRow is one Store-scoped Tag.
type AdminTagRow struct {
	TagID           string `json:"tag_id"`
	Slug            string `json:"slug"`
	NameAR          string `json:"name_ar"`
	NameEN          string `json:"name_en"`
	IsActive        bool   `json:"is_active"`
	CatalogRevision int64  `json:"catalog_revision"`
	HasPending      bool   `json:"has_pending"`
}

// AdminConfigurationRow is one projected frame option for the editor.
type AdminConfigurationRow struct {
	ID            string  `json:"id"`
	StyleCode     string  `json:"style_code"`
	StyleNameAR   string  `json:"style_name_ar"`
	StyleNameEN   *string `json:"style_name_en"`
	ColorCode     string  `json:"color_code"`
	ColorNameAR   string  `json:"color_name_ar"`
	ColorNameEN   *string `json:"color_name_en"`
	EGPDeltaMinor string  `json:"egp_delta_minor"`
	USDDeltaMinor *string `json:"usd_delta_minor"`
	Enabled       bool    `json:"enabled"`
	Position      int     `json:"position"`
}

// Store persists commands, targets and capabilities plus projection
// ownership/convergence reads. Implemented in adapter/postgres.
type Store interface {
	CreateCatalogAdminCommand(ctx context.Context, cmd NewCommand) (CommandView, error)
	CreateCatalogAdminTarget(ctx context.Context, commandID, targetID, deviceID string) (TargetView, error)
	GetCatalogAdminCommand(ctx context.Context, id string) (CommandView, error)
	ListCatalogAdminCommands(ctx context.Context, storeID, commandType, entityID, status string, limit int, cursorTS time.Time, cursorID string) ([]CommandView, error)
	ListCatalogAdminTargets(ctx context.Context, commandID string) ([]TargetView, error)
	ListCatalogAdminTargetsBatch(ctx context.Context, commandIDs []string) (map[string][]TargetView, error)
	DueCatalogAdminTargets(ctx context.Context, deviceID string, limit int) ([]DueTarget, error)
	FinishCatalogAdminTarget(ctx context.Context, targetID, deviceID, status, code, entityID string, pre, post int64) (bool, error)
	MarkCatalogAdminTargetDelivered(ctx context.Context, targetID, deviceID string) error
	CancelCatalogAdminCommand(ctx context.Context, commandID string) (bool, error)
	UpsertCatalogAdminCapability(ctx context.Context, deviceID string, capable bool) error
	GetCatalogAdminCapability(ctx context.Context, deviceID string) (bool, error)
	ListCatalogAdminBoundDevices(ctx context.Context, storeID string) ([]BoundDevice, error)
	// CatalogAdminOwnership verifies projection ownership and reads
	// the stream convergence revision. found=false means unknown
	// entity; storeID "" means legacy NULL (never mutable by Store).
	CatalogAdminOwnership(ctx context.Context, commandType, entityID string) (storeID string, revision int64, found bool, err error)
	// Admin reads serve the operator UI from projections only.
	AdminProductList(ctx context.Context, storeID, search, cursor string, limit int) ([]AdminProductRow, error)
	AdminProductDetail(ctx context.Context, storeID, productID string) (AdminProductDetail, error)
	AdminProductConfigurations(ctx context.Context, storeID, productID string) ([]AdminConfigurationRow, error)
	AdminCategoryList(ctx context.Context, storeID string) ([]AdminCategoryRow, error)
	AdminTagList(ctx context.Context, storeID string) ([]AdminTagRow, error)
}

// DeviceDirectory resolves device bindings and lifecycle.
type DeviceDirectory interface {
	// BindingStore returns the Store the device is currently bound to,
	// or "" when unbound.
	BindingStore(ctx context.Context, deviceID string) (string, error)
	// DeviceActive reports the existing device lifecycle.
	DeviceActive(ctx context.Context, deviceID string) (bool, error)
	// DeviceName returns display metadata (best effort).
	DeviceName(ctx context.Context, deviceID string) string
}

// Service orchestrates admin commands: creation validation, target
// snapshots, delivery gating, outcome recording, aggregate + convergence.
type Service struct {
	store   Store
	devices DeviceDirectory
}

// NewService wires the admin command service.
func NewService(store Store, devices DeviceDirectory) *Service {
	return &Service{store: store, devices: devices}
}

// Create validates operator intent, verifies the entity projection
// belongs to the selected Store, persists the immutable command and
// snapshots the current eligible device set. No projection writes, no
// provider calls: only durable intent.
func (s *Service) Create(ctx context.Context, actor, storeID, typ, entityID string, expectedRevision int64, payload []byte) (CommandView, error) {
	actor = strings.TrimSpace(actor)
	if actor == "" || len(actor) > 128 {
		return CommandView{}, apperr.New(apperr.InvalidInput, "invalid actor")
	}
	decoded, err := ValidateNewCommand(typ, storeID, entityID, expectedRevision, payload)
	if err != nil {
		return CommandView{}, apperr.New(apperr.InvalidInput, err.Error())
	}
	_ = decoded
	ownerStore, _, found, err := s.store.CatalogAdminOwnership(ctx, typ, entityID)
	if err != nil {
		return CommandView{}, err
	}
	if !found {
		return CommandView{}, apperr.New(apperr.NotFound, CodeEntityNotFound)
	}
	if ownerStore == "" || !strings.EqualFold(ownerStore, strings.TrimSpace(storeID)) {
		// Legacy NULL rows and cross-Store entities are never
		// mutable under a Store scope: no global fallback.
		return CommandView{}, apperr.New(apperr.InvalidInput, CodeStoreScopeConflict)
	}
	hash, err := HashPayload(payload)
	if err != nil {
		return CommandView{}, apperr.New(apperr.InvalidInput, err.Error())
	}
	cmd := NewCommand{
		ID: uuid.NewString(), StoreID: strings.TrimSpace(storeID),
		Type: typ, Version: 1, EntityID: strings.TrimSpace(entityID),
		Payload: payload, PayloadHash: hash,
		ExpectedRevision: expectedRevision, Actor: actor,
	}
	view, err := s.store.CreateCatalogAdminCommand(ctx, cmd)
	if err != nil {
		return CommandView{}, err
	}
	bound, err := s.store.ListCatalogAdminBoundDevices(ctx, storeID)
	if err != nil {
		return CommandView{}, err
	}
	for _, dev := range bound {
		if _, err := s.store.CreateCatalogAdminTarget(ctx, view.ID, uuid.NewString(), dev.DeviceID); err != nil {
			return CommandView{}, err
		}
	}
	return s.Get(ctx, storeID, view.ID)
}

// Get returns one Store-scoped command with targets, aggregate and
// convergence evidence.
func (s *Service) Get(ctx context.Context, storeID, commandID string) (CommandView, error) {
	view, err := s.store.GetCatalogAdminCommand(ctx, commandID)
	if err != nil {
		return CommandView{}, err
	}
	if !strings.EqualFold(view.StoreID, strings.TrimSpace(storeID)) {
		return CommandView{}, apperr.New(apperr.NotFound, CodeEntityNotFound)
	}
	targets, err := s.store.ListCatalogAdminTargets(ctx, commandID)
	if err != nil {
		return CommandView{}, err
	}
	s.annotateCapabilities(ctx, targets)
	view.Targets = targets
	view.Aggregate, view.Converged = s.aggregate(ctx, view, targets)
	view.Payload = nil // history lists never ship payloads by default
	return view, nil
}

// GetWithPayload is Get plus the immutable payload (detail view).
func (s *Service) GetWithPayload(ctx context.Context, storeID, commandID string) (CommandView, error) {
	view, err := s.store.GetCatalogAdminCommand(ctx, commandID)
	if err != nil {
		return CommandView{}, err
	}
	if !strings.EqualFold(view.StoreID, strings.TrimSpace(storeID)) {
		return CommandView{}, apperr.New(apperr.NotFound, CodeEntityNotFound)
	}
	targets, err := s.store.ListCatalogAdminTargets(ctx, commandID)
	if err != nil {
		return CommandView{}, err
	}
	s.annotateCapabilities(ctx, targets)
	view.Targets = targets
	view.Aggregate, view.Converged = s.aggregate(ctx, view, targets)
	return view, nil
}

// List pages Store-scoped command history (newest first).
func (s *Service) List(ctx context.Context, storeID, commandType, entityID, status string, limit int, cursor string) ([]CommandView, string, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	if commandType != "" && !IsKnownType(commandType) {
		return nil, "", apperr.New(apperr.InvalidInput, "unknown command type")
	}
	cursorTS, cursorID, err := decodeHistoryCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	views, err := s.store.ListCatalogAdminCommands(ctx, storeID, commandType, entityID, status, limit, cursorTS, cursorID)
	if err != nil {
		return nil, "", err
	}
	ids := make([]string, 0, len(views))
	for _, view := range views {
		ids = append(ids, view.ID)
	}
	batched, err := s.store.ListCatalogAdminTargetsBatch(ctx, ids)
	if err != nil {
		return nil, "", err
	}
	for i := range views {
		targets := batched[views[i].ID]
		views[i].Targets = targets
		views[i].Aggregate, views[i].Converged = s.aggregate(ctx, views[i], targets)
	}
	var next string
	if len(views) == limit {
		next = encodeHistoryCursor(views[len(views)-1].CreatedAt, views[len(views)-1].ID)
	}
	return views, next, nil
}

// historyCursorSeparator splits the opaque history cursor payload. The
// cursor is opaque to callers: timestamp (RFC3339Nano UTC) of the last
// returned row plus its command ID, base64url-encoded.
func encodeHistoryCursor(ts time.Time, id string) string {
	raw := ts.UTC().Format(time.RFC3339Nano) + "|" + id
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeHistoryCursor parses an opaque history cursor. Empty means start
// from the newest row. Malformed cursors fail closed with InvalidInput:
// they never widen scope or skip ordering.
func decodeHistoryCursor(cursor string) (time.Time, string, error) {
	if strings.TrimSpace(cursor) == "" {
		return time.Time{}, "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(cursor))
	if err != nil {
		return time.Time{}, "", apperr.New(apperr.InvalidInput, "invalid history cursor")
	}
	ts, id, ok := strings.Cut(string(raw), "|")
	if !ok {
		return time.Time{}, "", apperr.New(apperr.InvalidInput, "invalid history cursor")
	}
	parsed, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil || parsed.IsZero() {
		return time.Time{}, "", apperr.New(apperr.InvalidInput, "invalid history cursor")
	}
	if _, err := uuid.Parse(id); err != nil {
		return time.Time{}, "", apperr.New(apperr.InvalidInput, "invalid history cursor")
	}
	return parsed, id, nil
}

// Cancel marks a PENDING command CANCELLED. Commands with any APPLIED
// target cannot be cancelled: cancellation never pretends to roll
// back applied work (a compensating command is required).
func (s *Service) Cancel(ctx context.Context, storeID, commandID string) (CommandView, error) {
	view, err := s.store.GetCatalogAdminCommand(ctx, commandID)
	if err != nil {
		return CommandView{}, err
	}
	if !strings.EqualFold(view.StoreID, strings.TrimSpace(storeID)) {
		return CommandView{}, apperr.New(apperr.NotFound, CodeEntityNotFound)
	}
	targets, err := s.store.ListCatalogAdminTargets(ctx, commandID)
	if err != nil {
		return CommandView{}, err
	}
	for _, t := range targets {
		if t.Status == TargetApplied {
			return CommandView{}, apperr.New(apperr.Conflict, "command already applied; issue a compensating command")
		}
	}
	ok, err := s.store.CancelCatalogAdminCommand(ctx, commandID)
	if err != nil {
		return CommandView{}, err
	}
	if !ok {
		return s.Get(ctx, storeID, commandID)
	}
	return s.Get(ctx, storeID, commandID)
}

// Poll returns due targets for the calling device: active, currently
// bound to the command Store, and capability-gated. Unknown command
// types never deliver (defense in depth: Retail also rejects them).
func (s *Service) Poll(ctx context.Context, deviceID string, limit int) ([]DueTarget, error) {
	active, err := s.devices.DeviceActive(ctx, deviceID)
	if err != nil {
		return nil, err
	}
	if !active {
		return nil, apperr.New(apperr.InvalidInput, "device not active")
	}
	boundStore, err := s.devices.BindingStore(ctx, deviceID)
	if err != nil {
		return nil, err
	}
	capable, err := s.store.GetCatalogAdminCapability(ctx, deviceID)
	if err != nil {
		return nil, err
	}
	if !capable {
		return nil, nil
	}
	if limit < 1 || limit > 50 {
		limit = 10
	}
	due, err := s.store.DueCatalogAdminTargets(ctx, deviceID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]DueTarget, 0, len(due))
	for _, t := range due {
		if !IsKnownType(t.Type) {
			// Never deliver unknown commands to any device.
			_, _ = s.store.FinishCatalogAdminTarget(ctx, t.TargetID, deviceID,
				TargetBlockedCapability, CodeUnsupportedCommand, t.EntityID, 0, 0)
			continue
		}
		if boundStore == "" || !strings.EqualFold(boundStore, t.StoreID) {
			// Rebound since snapshot: must not apply the old
			// Store's command here. Leave for the bound Store's
			// devices; mark skipped so history stays truthful.
			_, _ = s.store.FinishCatalogAdminTarget(ctx, t.TargetID, deviceID,
				TargetSkippedRevoked, CodeStoreScopeConflict, t.EntityID, 0, 0)
			continue
		}
		_ = s.store.MarkCatalogAdminTargetDelivered(ctx, t.TargetID, deviceID)
		t.Status = TargetDelivered
		out = append(out, t)
	}
	return out, nil
}

// ReportOutcome records one device outcome. The target is bound
// server-side to the calling device: wrong-device ACKs are rejected
// and change nothing.
func (s *Service) ReportOutcome(ctx context.Context, deviceID, targetID, status, code, entityID string, pre, post int64) error {
	switch status {
	case TargetApplied, TargetConflict, TargetRejected:
	default:
		return apperr.New(apperr.InvalidInput, "invalid outcome status")
	}
	if len(code) == 0 || len(code) > 64 {
		return apperr.New(apperr.InvalidInput, "invalid result code")
	}
	active, err := s.devices.DeviceActive(ctx, deviceID)
	if err != nil {
		return err
	}
	if !active {
		return apperr.New(apperr.InvalidInput, "device not active")
	}
	ok, err := s.store.FinishCatalogAdminTarget(ctx, targetID, deviceID, status, code, entityID, pre, post)
	if err != nil {
		return err
	}
	if !ok {
		return apperr.New(apperr.NotFound, "unknown command target")
	}
	return nil
}

// ReportCapabilities records Retail capability advertisement.
func (s *Service) ReportCapabilities(ctx context.Context, deviceID string, capabilities []string) (bool, error) {
	active, err := s.devices.DeviceActive(ctx, deviceID)
	if err != nil {
		return false, err
	}
	if !active {
		return false, apperr.New(apperr.InvalidInput, "device not active")
	}
	capable := false
	for _, c := range capabilities {
		if c == CapabilityV1 {
			capable = true
		}
	}
	if err := s.store.UpsertCatalogAdminCapability(ctx, deviceID, capable); err != nil {
		return false, err
	}
	return capable, nil
}

// annotateCapabilities marks PENDING targets on incapable devices so
// the dashboard can show update-required instead of silent waiting.
func (s *Service) annotateCapabilities(ctx context.Context, targets []TargetView) {
	for i := range targets {
		if targets[i].Status != TargetPending && targets[i].Status != TargetDelivered {
			continue
		}
		capable, err := s.store.GetCatalogAdminCapability(ctx, targets[i].DeviceID)
		if err != nil || !capable {
			incapable := false
			targets[i].Capable = &incapable
			continue
		}
		ok := true
		targets[i].Capable = &ok
		name := s.devices.DeviceName(ctx, targets[i].DeviceID)
		if name != "" {
			targets[i].DeviceName = name
		}
	}
}

// aggregate derives the Store-level state plus convergence: CONVERGED
// requires every applied target's post_revision to be visible in the
// Cloud projection for its stream.
func (s *Service) aggregate(ctx context.Context, view CommandView, targets []TargetView) (string, bool) {
	states := make([]string, 0, len(targets))
	for _, t := range targets {
		states = append(states, t.Status)
	}
	agg := Aggregate(view.Status, states, false)
	if agg != AggregateApplied {
		return agg, false
	}
	converged := true
	for _, t := range targets {
		if t.Status != TargetApplied {
			converged = false
			break
		}
		_, revision, found, err := s.store.CatalogAdminOwnership(ctx, view.Type, view.EntityID)
		if err != nil || !found || revision < t.PostRevision {
			converged = false
			break
		}
	}
	if converged {
		return AggregateConverged, true
	}
	return AggregateApplied, false
}

var _ = fmt.Sprintf

// AdminProducts lists Store-scoped Products for the operator UI.
// Malformed Store IDs fail; unknown Stores yield empty results
// (never a global fallback).
func (s *Service) AdminProducts(ctx context.Context, storeID, search, cursor string, limit int) ([]AdminProductRow, string, error) {
	if _, err := uuid.Parse(strings.TrimSpace(storeID)); err != nil {
		return nil, "", apperr.New(apperr.InvalidInput, "invalid store_id")
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	if len(search) > 64 {
		return nil, "", apperr.New(apperr.InvalidInput, "search too long")
	}
	productID, err := decodeProductCursor(cursor, strings.TrimSpace(storeID), strings.TrimSpace(search))
	if err != nil {
		return nil, "", err
	}
	rows, err := s.store.AdminProductList(ctx, strings.TrimSpace(storeID), strings.TrimSpace(search), productID, limit)
	if err != nil {
		return nil, "", err
	}
	var next string
	if len(rows) == limit {
		next = encodeProductCursor(strings.TrimSpace(storeID), strings.TrimSpace(search), rows[len(rows)-1].ProductID)
	}
	return rows, next, nil
}

// productCursorSeparator splits the opaque scope-bound Product cursor.
// The cursor binds the Store ID and search string that produced the
// page to the last returned Product ID (base64url-encoded). Reusing a
// cursor under a different Store or search fails closed with
// InvalidInput instead of silently continuing another scope's list
// from an unrelated position.
func encodeProductCursor(storeID, search, productID string) string {
	raw := storeID + "\n" + search + "\n" + productID
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeProductCursor resolves an opaque Product cursor to the bare
// Product ID after verifying it was issued for this exact Store+search
// scope. Empty means start from the newest row.
func decodeProductCursor(cursor, storeID, search string) (string, error) {
	if strings.TrimSpace(cursor) == "" {
		return "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(cursor))
	if err != nil {
		return "", apperr.New(apperr.InvalidInput, "invalid Product cursor")
	}
	scopeStore, scopeSearch, productID, ok := stringsCut2(string(raw))
	if !ok {
		return "", apperr.New(apperr.InvalidInput, "invalid Product cursor")
	}
	if scopeStore != storeID || scopeSearch != search {
		return "", apperr.New(apperr.InvalidInput, "Product cursor does not match request scope")
	}
	if _, err := uuid.Parse(productID); err != nil {
		return "", apperr.New(apperr.InvalidInput, "invalid Product cursor")
	}
	return productID, nil
}

// stringsCut2 splits s on the first two newlines.
func stringsCut2(s string) (string, string, string, bool) {
	first, rest, ok := strings.Cut(s, "\n")
	if !ok {
		return "", "", "", false
	}
	second, third, ok := strings.Cut(rest, "\n")
	if !ok {
		return "", "", "", false
	}
	return first, second, third, true
}

// AdminProduct returns one Store-scoped Product detail snapshot.
func (s *Service) AdminProduct(ctx context.Context, storeID, productID string) (AdminProductDetail, error) {
	if _, err := uuid.Parse(strings.TrimSpace(storeID)); err != nil {
		return AdminProductDetail{}, apperr.New(apperr.InvalidInput, "invalid store_id")
	}
	if _, err := uuid.Parse(strings.TrimSpace(productID)); err != nil {
		return AdminProductDetail{}, apperr.New(apperr.InvalidInput, "invalid product id")
	}
	detail, err := s.store.AdminProductDetail(ctx, strings.TrimSpace(storeID), strings.TrimSpace(productID))
	if err != nil {
		return AdminProductDetail{}, err
	}
	if detail.ProductID == "" {
		return AdminProductDetail{}, apperr.New(apperr.NotFound, CodeEntityNotFound)
	}
	return detail, nil
}

// AdminCategories lists the Store-scoped Category tree rows.
func (s *Service) AdminCategories(ctx context.Context, storeID string) ([]AdminCategoryRow, error) {
	if _, err := uuid.Parse(strings.TrimSpace(storeID)); err != nil {
		return nil, apperr.New(apperr.InvalidInput, "invalid store_id")
	}
	return s.store.AdminCategoryList(ctx, strings.TrimSpace(storeID))
}

// AdminTags lists the Store-scoped Tag rows.
func (s *Service) AdminTags(ctx context.Context, storeID string) ([]AdminTagRow, error) {
	if _, err := uuid.Parse(strings.TrimSpace(storeID)); err != nil {
		return nil, apperr.New(apperr.InvalidInput, "invalid store_id")
	}
	return s.store.AdminTagList(ctx, strings.TrimSpace(storeID))
}

// AdminConfigurations lists current frame options for one Product.
func (s *Service) AdminConfigurations(ctx context.Context, storeID, productID string) ([]AdminConfigurationRow, error) {
	if _, err := uuid.Parse(strings.TrimSpace(storeID)); err != nil {
		return nil, apperr.New(apperr.InvalidInput, "invalid store_id")
	}
	if _, err := uuid.Parse(strings.TrimSpace(productID)); err != nil {
		return nil, apperr.New(apperr.InvalidInput, "invalid product id")
	}
	owner, _, found, err := s.store.CatalogAdminOwnership(ctx, TypeProductConfigurationsUpdateV1, strings.TrimSpace(productID))
	if err != nil {
		return nil, err
	}
	if !found || owner == "" || !strings.EqualFold(owner, strings.TrimSpace(storeID)) {
		return nil, apperr.New(apperr.NotFound, CodeEntityNotFound)
	}
	return s.store.AdminProductConfigurations(ctx, strings.TrimSpace(storeID), strings.TrimSpace(productID))
}
