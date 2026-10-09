package catalogadmin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/ids"
)

// NewCommand is the validated creation intent.
type NewCommand struct {
	ID       string
	StoreID  string
	Type     string
	Version  int
	EntityID string
	// TargetKind is "entity" (default: the command addresses a
	// pre-existing business entity) or "create" (creation intent: the
	// entity does not exist yet; EntityID must be the absent marker "").
	TargetKind string
	// RequestedKey is the stable requested key for creates (ProductType
	// code); empty for entity commands.
	RequestedKey     string
	Payload          []byte
	PayloadHash      string
	ExpectedRevision int64
	Actor            string
}

// Command target kinds.
const (
	// TargetKindEntity addresses a pre-existing business entity.
	TargetKindEntity = "entity"
	// TargetKindCreate is a creation intent: no business identity exists
	// yet. EntityID is the explicit absent marker ""; the requested
	// stable key travels in RequestedKey and the Retail-minted identity
	// returns in ResultEntityID. Never a placeholder UUID.
	TargetKindCreate = "create"
)

// CommandView is one command row for API/UI.
type CommandView struct {
	ID               string       `json:"id"`
	StoreID          string       `json:"store_id"`
	Type             string       `json:"type"`
	Version          int          `json:"version"`
	EntityID         string       `json:"entity_id"`
	TargetKind       string       `json:"target_kind"`
	RequestedKey     string       `json:"requested_key,omitempty"`
	ResultEntityID   string       `json:"result_entity_id,omitempty"`
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

// DeviceState is the batched device read used when annotating command
// targets (Phase 19): one row per device, no per-device queries.
type DeviceState struct {
	DeviceID string
	Name     string
	Active   bool
	StoreID  string
	Capable  bool
}

// AdminProductRow is one Store-scoped Product for the operator list.
// Phase 17-R0 (ADR-0049): products carry NO sku/stock — they carry
// variant_count and derived_stock (SUM of active, non-tombstoned variant
// stock; product stock is derived only). Variant rows own the SKU.
type AdminProductRow struct {
	ProductID string `json:"product_id"`
	// Phase 17-R2: structural type identity (never inferred).
	ProductTypeID         string `json:"product_type_id"`
	NameAR                string `json:"name_ar"`
	NameEN                string `json:"name_en"`
	IsActive              bool   `json:"is_active"`
	CatalogRevision       int64  `json:"catalog_revision"`
	SellOnline            bool   `json:"sell_online"`
	VariantCount          int64  `json:"variant_count"`
	DerivedStock          int64  `json:"derived_stock"`
	ConfigurationRevision int64  `json:"configuration_revision"`
	HasPending            bool   `json:"has_pending"`
}

// AdminProductDetail is the full projection snapshot an editor needs:
// current values, all three revision streams, and the derived variant
// rollup (variant_count/derived_stock; no product SKU — 17-R0).
type AdminProductDetail struct {
	ProductID string `json:"product_id"`
	// Phase 17-R2: structural type identity (never inferred).
	ProductTypeID         string   `json:"product_type_id"`
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
	VariantCount          int64    `json:"variant_count"`
	DerivedStock          int64    `json:"derived_stock"`
	CatalogRevision       int64    `json:"catalog_revision"`
	SalesPolicyRevision   int64    `json:"sales_policy_revision"`
	ConfigurationRevision int64    `json:"configuration_revision"`
	HasPending            bool     `json:"has_pending"`
	// Variants are the product's Phase 17 SKU/inventory-owning rows with
	// attributes, prices and last-known stock.
	Variants []AdminProductVariant `json:"variants"`
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

// AdminProductVariantAttribute is one projected physical option of a
// variant with its bilingual display labels (projection-only display).
type AdminProductVariantAttribute struct {
	DefinitionCode   string `json:"definition_code"`
	ValueCode        string `json:"value_code"`
	NameAR           string `json:"name_ar"`
	NameEN           string `json:"name_en"`
	DefinitionNameAR string `json:"definition_name_ar"`
	DefinitionNameEN string `json:"definition_name_en"`
	Position         int    `json:"position"`
}

// AdminProductVariant is one projected product variant for the editor:
// identity, exact minor-unit prices, last-known stock, and options.
// Tombstones keep their rows (deleted=true) for historical safety.
type AdminProductVariant struct {
	VariantID         string                         `json:"variant_id"`
	ProductID         string                         `json:"product_id"`
	SKU               string                         `json:"sku"`
	IsActive          bool                           `json:"is_active"`
	Deleted           bool                           `json:"deleted"`
	PriceEGPMinor     string                         `json:"price_egp_minor"`
	PriceUSDMinor     *string                        `json:"price_usd_minor"`
	StockQuantity     int64                          `json:"stock_quantity"`
	Position          int                            `json:"position"`
	CombinationKey    string                         `json:"combination_key"`
	VariantRevision   int64                          `json:"variant_revision"`
	CatalogRevision   int64                          `json:"catalog_revision"`
	InventoryRevision int64                          `json:"inventory_revision"`
	HasPending        bool                           `json:"has_pending"`
	Attributes        []AdminProductVariantAttribute `json:"attributes"`
}

// Store persists commands, targets and capabilities plus projection
// ownership/convergence reads. Implemented in adapter/postgres.
type Store interface {
	CreateCatalogAdminCommandWithTargets(ctx context.Context, cmd NewCommand) (CommandView, error)
	CreateCatalogAdminCommand(ctx context.Context, cmd NewCommand) (CommandView, error)
	CreateCatalogAdminTarget(ctx context.Context, commandID, targetID, deviceID string) (TargetView, error)
	GetCatalogAdminCommand(ctx context.Context, id string) (CommandView, error)
	ListCatalogAdminCommands(ctx context.Context, storeID, commandType, entityID, status string, limit int, cursorTS time.Time, cursorID string) ([]CommandView, error)
	ListCatalogAdminTargets(ctx context.Context, commandID string) ([]TargetView, error)
	ListCatalogAdminTargetsBatch(ctx context.Context, commandIDs []string) (map[string][]TargetView, error)
	DueCatalogAdminTargets(ctx context.Context, deviceID string, limit int) ([]DueTarget, error)
	FinishCatalogAdminTarget(ctx context.Context, targetID, deviceID, status, code, entityID string, pre, post int64) (bool, error)
	// SetCommandResultEntity records a command's actual resulting entity
	// (Retail-minted on APPLIED). First writer wins; intent never moves.
	SetCommandResultEntity(ctx context.Context, targetID, entityID string) error
	MarkCatalogAdminTargetDelivered(ctx context.Context, targetID, deviceID string) error
	CancelCatalogAdminCommand(ctx context.Context, commandID string) (bool, error)
	UpsertCatalogAdminCapability(ctx context.Context, deviceID string, capable bool) error
	GetCatalogAdminCapability(ctx context.Context, deviceID string) (bool, error)
	ListCatalogAdminBoundDevices(ctx context.Context, storeID string) ([]BoundDevice, error)
	// CatalogAdminOwnership verifies projection ownership and reads
	// the stream convergence revision. found=false means unknown
	// entity; storeID "" means legacy NULL (never mutable by Store).
	CatalogAdminOwnership(ctx context.Context, commandType, entityID, requestedStoreID string) (storeID string, revision int64, found bool, err error)
	// CatalogAdminDeviceStates is the Phase 19 batched device read.
	CatalogAdminDeviceStates(ctx context.Context, deviceIDs []string) (map[string]DeviceState, error)
	// Admin reads serve the operator UI from projections only.
	AdminProductList(ctx context.Context, storeID, search, cursor string, limit int) ([]AdminProductRow, error)
	AdminProductDetail(ctx context.Context, storeID, productID string) (AdminProductDetail, error)
	AdminProductConfigurations(ctx context.Context, storeID, productID string) ([]AdminConfigurationRow, error)
	AdminCategoryList(ctx context.Context, storeID string) ([]AdminCategoryRow, error)
	AdminTagList(ctx context.Context, storeID string) ([]AdminTagRow, error)
	// Phase 17 variant reads (Store-scoped, projection only).
	AdminProductVariants(ctx context.Context, storeID, productID string) ([]AdminProductVariant, error)
	AdminProductVariant(ctx context.Context, storeID, variantID string) (AdminProductVariant, error)
	// Phase 17-R2 type reads (Store-scoped, projection only).
	AdminProductTypeList(ctx context.Context, storeID string) ([]AdminProductType, error)
	AdminProductType(ctx context.Context, storeID, typeID string) (AdminProductType, error)
}

// AdminProductType is one projected structural type for the editor:
// identity, labels, lifecycle, allowed dimensions and capabilities.
type AdminProductType struct {
	TypeID        string   `json:"type_id"`
	Code          string   `json:"code"`
	NameAR        string   `json:"name_ar"`
	NameEN        string   `json:"name_en"`
	DescriptionAR *string  `json:"description_ar,omitempty"`
	DescriptionEN *string  `json:"description_en,omitempty"`
	IsActive      bool     `json:"is_active"`
	Position      int      `json:"position"`
	TypeRevision  int64    `json:"type_revision"`
	Dimensions    []string `json:"dimensions"`
	Capabilities  []string `json:"capabilities"`
	HasPending    bool     `json:"has_pending"`
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
// provider calls: only durable intent. Create-kind commands carry NO
// business entity (entity_id is the explicit absent marker ""); the
// requested stable key is stored separately and the Retail-minted
// identity returns later in ResultEntityID.
func (s *Service) Create(ctx context.Context, actor, storeID, typ, entityID string, expectedRevision int64, payload []byte) (CommandView, error) {
	actor = strings.TrimSpace(actor)
	if actor == "" || len(actor) > 128 {
		return CommandView{}, apperr.New(apperr.InvalidInput, "invalid actor")
	}
	decoded, err := ValidateNewCommand(typ, storeID, entityID, expectedRevision, payload)
	if err != nil {
		return CommandView{}, apperr.New(apperr.InvalidInput, err.Error())
	}
	payload, err = json.Marshal(decoded)
	if err != nil || len(payload) > 64*1024 {
		return CommandView{}, apperr.New(apperr.InvalidInput, "invalid command payload")
	}
	storeUUID, _ := uuid.Parse(strings.TrimSpace(storeID))
	storeID = storeUUID.String()
	targetKind := TargetKindEntity
	requestedKey := ""
	if typ == TypeProductTypeCreateV1 {
		// No entity to own yet: skip the ownership lookup entirely.
		// Retail validates code uniqueness and owns acceptance (and the
		// canonical ID) at apply time. requested_key is the normalized
		// code from the validated payload.
		targetKind = TargetKindCreate
		requestedKey, _ = decoded["code"].(string)
		entityID = ""
	} else {
		entityUUID, _ := uuid.Parse(strings.TrimSpace(entityID))
		entityID = entityUUID.String()
	}
	ownerStore := strings.TrimSpace(storeID)
	if targetKind == TargetKindEntity {
		var found bool
		ownerStore, _, found, err = s.store.CatalogAdminOwnership(ctx, typ, entityID, storeID)
		if err != nil {
			return CommandView{}, err
		}
		if !found {
			return CommandView{}, apperr.New(apperr.NotFound, CodeEntityNotFound)
		}
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
		ID: (ids.System{}).New(), StoreID: strings.TrimSpace(storeID),
		Type: typ, Version: 1, EntityID: strings.TrimSpace(entityID),
		TargetKind: targetKind, RequestedKey: requestedKey,
		Payload: payload, PayloadHash: hash,
		ExpectedRevision: expectedRevision, Actor: actor,
	}
	view, err := s.store.CreateCatalogAdminCommandWithTargets(ctx, cmd)
	if err != nil {
		return CommandView{}, err
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
	if err := s.annotateCapabilities(ctx, targets); err != nil {
		return CommandView{}, err
	}
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
	if err := s.annotateCapabilities(ctx, targets); err != nil {
		return CommandView{}, err
	}
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
		if err := s.annotateCapabilities(ctx, targets); err != nil {
			return nil, "", err
		}
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
		if !IsKnownType(t.Type) || t.Version != 1 {
			// Never deliver unknown commands to any device.
			if _, err := s.store.FinishCatalogAdminTarget(ctx, t.TargetID, deviceID,
				TargetBlockedCapability, CodeUnsupportedCommand, t.EntityID, 0, 0); err != nil {
				return nil, err
			}
			continue
		}
		if boundStore == "" || !strings.EqualFold(boundStore, t.StoreID) {
			// Rebound since snapshot: must not apply the old
			// Store's command here. Leave for the bound Store's
			// devices; mark skipped so history stays truthful.
			if _, err := s.store.FinishCatalogAdminTarget(ctx, t.TargetID, deviceID,
				TargetSkippedRevoked, CodeStoreScopeConflict, t.EntityID, 0, 0); err != nil {
				return nil, err
			}
			continue
		}
		candidate := append(append([]DueTarget(nil), out...), t)
		if CatalogPollBytes(candidate) > MaxCatalogPollResponseBytes {
			break
		}
		if err := s.store.MarkCatalogAdminTargetDelivered(ctx, t.TargetID, deviceID); err != nil {
			return nil, err
		}
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
	// The repository commits the target outcome and actual result identity
	// together. An acknowledged create must never lose its result identity.
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
//
// Phase 19: this is batched — one device-state read for all targets
// plus one command read per distinct command replaces the previous
// per-target DeviceActive/BindingStore/Capability/DeviceName round
// trips (up to 5 statements per target).
func (s *Service) annotateCapabilities(ctx context.Context, targets []TargetView) error {
	pending := make([]int, 0, len(targets))
	deviceIDs := make([]string, 0, len(targets))
	commandIDs := map[string]struct{}{}
	for i := range targets {
		if targets[i].Status != TargetPending && targets[i].Status != TargetDelivered {
			continue
		}
		pending = append(pending, i)
		deviceIDs = append(deviceIDs, targets[i].DeviceID)
		commandIDs[targets[i].CommandID] = struct{}{}
	}
	if len(pending) == 0 {
		return nil
	}
	states, err := s.store.CatalogAdminDeviceStates(ctx, deviceIDs)
	if err != nil {
		return err
	}
	commandStores := make(map[string]string, len(commandIDs))
	commandEntities := make(map[string]string, len(commandIDs))
	for id := range commandIDs {
		cmd, err := s.store.GetCatalogAdminCommand(ctx, id)
		if err != nil {
			return err
		}
		commandStores[id] = cmd.StoreID
		commandEntities[id] = cmd.EntityID
	}
	for _, i := range pending {
		state, known := states[targets[i].DeviceID]
		if !known || !state.Active || !strings.EqualFold(state.StoreID, commandStores[targets[i].CommandID]) {
			changed, err := s.store.FinishCatalogAdminTarget(ctx, targets[i].ID, targets[i].DeviceID, TargetSkippedRevoked, CodeStoreScopeConflict, commandEntities[targets[i].CommandID], 0, 0)
			if err != nil {
				return err
			}
			if changed {
				targets[i].Status = TargetSkippedRevoked
				targets[i].ResultCode = CodeStoreScopeConflict
			}
			continue
		}
		if !state.Capable {
			incapable := false
			targets[i].Capable = &incapable
			continue
		}
		ok := true
		targets[i].Capable = &ok
		if state.Name != "" {
			targets[i].DeviceName = state.Name
		}
	}
	return nil
}

// aggregate derives the Store-level state plus convergence: CONVERGED
// requires every applied target's post_revision to be visible in the
// Cloud projection for its stream. For create-kind commands the effective
// entity is the Retail-minted ResultEntityID (the command carries no
// business identity); APPLIED without a result yet cannot converge.
func (s *Service) aggregate(ctx context.Context, view CommandView, targets []TargetView) (string, bool) {
	states := make([]string, 0, len(targets))
	for _, t := range targets {
		states = append(states, t.Status)
	}
	agg := Aggregate(view.Status, states, false)
	if agg != AggregateApplied {
		return agg, false
	}
	effEntity := view.EntityID
	if view.TargetKind == TargetKindCreate {
		if strings.TrimSpace(view.ResultEntityID) == "" {
			return AggregateApplied, false
		}
		effEntity = view.ResultEntityID
	}
	converged := true
	for _, t := range targets {
		if t.Status != TargetApplied {
			converged = false
			break
		}
		owner, revision, found, err := s.store.CatalogAdminOwnership(ctx, view.Type, effEntity, view.StoreID)
		if err != nil || !found || !strings.EqualFold(owner, view.StoreID) || t.EntityID != effEntity || t.PostRevision < view.ExpectedRevision || revision < t.PostRevision || (t.PostRevision == view.ExpectedRevision && !s.noopProjected(ctx, view)) {
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

// AdminProductTypes lists Store-scoped ProductTypes for the operator UI.
// Malformed Store IDs fail; unknown Stores yield empty results (never a
// global fallback).
func (s *Service) AdminProductTypes(ctx context.Context, storeID string) ([]AdminProductType, error) {
	if _, err := uuid.Parse(strings.TrimSpace(storeID)); err != nil {
		return nil, apperr.New(apperr.InvalidInput, "invalid store_id")
	}
	return s.store.AdminProductTypeList(ctx, strings.TrimSpace(storeID))
}

// AdminProductType serves one Store-scoped ProductType for the editor.
func (s *Service) AdminProductType(ctx context.Context, storeID, typeID string) (AdminProductType, error) {
	if _, err := uuid.Parse(strings.TrimSpace(storeID)); err != nil {
		return AdminProductType{}, apperr.New(apperr.InvalidInput, "invalid store_id")
	}
	if _, err := uuid.Parse(strings.TrimSpace(typeID)); err != nil {
		return AdminProductType{}, apperr.New(apperr.InvalidInput, "invalid product_type_id")
	}
	return s.store.AdminProductType(ctx, strings.TrimSpace(storeID), strings.TrimSpace(typeID))
}

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
	variants, err := s.store.AdminProductVariants(ctx, strings.TrimSpace(storeID), strings.TrimSpace(productID))
	if err != nil {
		return AdminProductDetail{}, err
	}
	detail.Variants = variants
	return detail, nil
}

// AdminProductVariants lists one Product's projected variants for the
// operator UI (identity, prices, stock, option attributes). Unknown
// Stores and foreign Products yield empty results (never a global
// fallback).
func (s *Service) AdminProductVariants(ctx context.Context, storeID, productID string) ([]AdminProductVariant, error) {
	if _, err := uuid.Parse(strings.TrimSpace(storeID)); err != nil {
		return nil, apperr.New(apperr.InvalidInput, "invalid store_id")
	}
	if _, err := uuid.Parse(strings.TrimSpace(productID)); err != nil {
		return nil, apperr.New(apperr.InvalidInput, "invalid product id")
	}
	return s.store.AdminProductVariants(ctx, strings.TrimSpace(storeID), strings.TrimSpace(productID))
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
	owner, _, found, err := s.store.CatalogAdminOwnership(ctx, TypeProductConfigurationsUpdateV1, strings.TrimSpace(productID), storeID)
	if err != nil {
		return nil, err
	}
	if !found || owner == "" || !strings.EqualFold(owner, strings.TrimSpace(storeID)) {
		return nil, apperr.New(apperr.NotFound, CodeEntityNotFound)
	}
	return s.store.AdminProductConfigurations(ctx, strings.TrimSpace(storeID), strings.TrimSpace(productID))
}

// Wire shape is shared by byte budgeting and the HTTP encoder.
const MaxCatalogPollResponseBytes = 128 * 1024

type CommandWire struct {
	TargetID    string `json:"target_id"`
	CommandID   string `json:"command_id"`
	CommandType string `json:"command_type"`
	Version     int    `json:"version"`
	StoreID     string `json:"store_id"`
	Payload     []byte `json:"payload"`
	PayloadHash string `json:"payload_hash"`
}

func PollWire(due []DueTarget) []CommandWire {
	out := make([]CommandWire, 0, len(due))
	for _, t := range due {
		out = append(out, CommandWire{t.TargetID, t.CommandID, t.Type, t.Version, t.StoreID, t.Payload, t.PayloadHash})
	}
	return out
}
func CatalogPollBytes(due []DueTarget) int {
	b, _ := json.Marshal(map[string]any{"commands": PollWire(due)})
	return len(b) + 1
}

// A no-op may retain its revision. Require the requested values in projection,
// rather than treating a reported unchanged revision as proof of a change.
func (s *Service) noopProjected(ctx context.Context, view CommandView) bool {
	if len(view.Payload) == 0 {
		stored, err := s.store.GetCatalogAdminCommand(ctx, view.ID)
		if err != nil {
			return false
		}
		view.Payload = stored.Payload
	}
	var payload map[string]any
	if json.Unmarshal(view.Payload, &payload) != nil {
		return false
	}
	switch view.Type {
	case TypeProductOnlinePolicyUpdateV1:
		p, err := s.store.AdminProductDetail(ctx, view.StoreID, view.EntityID)
		wanted, ok := payload["sell_online"].(bool)
		return err == nil && ok && p.SellOnline == wanted
	case TypeCategoryOnlinePolicyUpdateV1:
		rows, err := s.store.AdminCategoryList(ctx, view.StoreID)
		if err != nil {
			return false
		}
		wanted, ok := payload["online_enabled"].(bool)
		if !ok {
			return false
		}
		for _, row := range rows {
			if row.CategoryID == view.EntityID {
				return row.OnlineEnabled == wanted
			}
		}
	case TypeProductConfigurationsUpdateV1:
		rows, err := s.store.AdminProductConfigurations(ctx, view.StoreID, view.EntityID)
		if err != nil {
			return false
		}
		desired, ok := payload["configurations"].([]any)
		if !ok {
			return false
		}
		requestedIDs := make(map[string]bool, len(desired))
		for position, item := range desired {
			m, ok := item.(map[string]any)
			if !ok {
				return false
			}
			id := payloadText(m, "id")
			if parsed, err := uuid.Parse(id); err == nil {
				id = parsed.String()
			}
			requestedIDs[id] = true
			matched := false
			for _, row := range rows {
				if row.ID != id {
					continue
				}
				enabled, valid := m["enabled"].(bool)
				matched = valid && row.Position == position && optionalTextEqual(row.StyleNameEN, m["style_name_en"]) && optionalTextEqual(row.ColorNameEN, m["color_name_en"]) && row.StyleCode == payloadText(m, "style_code") && row.ColorCode == payloadText(m, "color_code") && row.StyleNameAR == payloadText(m, "style_name_ar") && row.ColorNameAR == payloadText(m, "color_name_ar") && minorTextEqual(row.EGPDeltaMinor, m["egp_delta_cents"]) && row.Enabled == enabled && ((row.USDDeltaMinor == nil && m["usd_delta_cents"] == nil) || (row.USDDeltaMinor != nil && minorTextEqual(*row.USDDeltaMinor, m["usd_delta_cents"])))
				break
			}
			if !matched {
				return false
			}
		}
		// Omitted options must already be disabled for a genuine no-op.
		for _, row := range rows {
			if row.Enabled && !requestedIDs[row.ID] {
				return false
			}
		}
		return true
	case TypeProductVariantUpdateV1:
		row, err := s.store.AdminProductVariant(ctx, view.StoreID, view.EntityID)
		return err == nil && variantStateMatches(row, payload)
	case TypeVariantAttributesUpdateV1:
		row, err := s.store.AdminProductVariant(ctx, view.StoreID, view.EntityID)
		if err != nil {
			return false
		}
		desired, ok := payload["attributes"].([]any)
		if !ok {
			return false
		}
		return attributesMatch(row.Attributes, desired)
	case TypeProductVariantsUpdateV1:
		rows, err := s.store.AdminProductVariants(ctx, view.StoreID, view.EntityID)
		if err != nil {
			return false
		}
		desired, ok := payload["variants"].([]any)
		if !ok {
			return false
		}
		matched := make(map[string]bool, len(rows))
		for _, item := range desired {
			entry, ok := item.(map[string]any)
			if !ok {
				return false
			}
			found := false
			for _, row := range rows {
				if matched[row.VariantID] {
					continue
				}
				if sameVariantIdentity(row, entry) && variantStateMatches(row, entry) {
					matched[row.VariantID] = true
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		// Replace-set semantics: every projected variant must already be
		// requested for a genuine no-op.
		for _, row := range rows {
			if !matched[row.VariantID] {
				return false
			}
		}
		return true
	}
	return false
}

// sameVariantIdentity reports whether a bulk variants entry targets a
// projected variant by explicit ID or by normalized combination key.
func sameVariantIdentity(row AdminProductVariant, entry map[string]any) bool {
	if id := payloadText(entry, "id"); id != "" {
		parsed := id
		if canonical, err := uuid.Parse(id); err == nil {
			parsed = canonical.String()
		}
		return parsed == row.VariantID
	}
	if key := payloadText(entry, "combination_key"); key != "" {
		return key == row.CombinationKey
	}
	return false
}

// variantStateMatches verifies every requested variant field against the
// projection (fields absent from the payload are unconstrained). Money
// compares as exact decimal strings, never floats.
func variantStateMatches(row AdminProductVariant, entry map[string]any) bool {
	if raw, present := entry["sku"]; present {
		value, ok := raw.(string)
		if !ok || strings.TrimSpace(value) != row.SKU {
			return false
		}
	}
	if raw, present := entry["combination_key"]; present {
		value, ok := raw.(string)
		if !ok || strings.TrimSpace(value) != row.CombinationKey {
			return false
		}
	}
	if raw, present := entry["is_active"]; present {
		value, ok := raw.(bool)
		if !ok || value != row.IsActive {
			return false
		}
	}
	if raw, present := entry["deleted"]; present {
		value, ok := raw.(bool)
		if !ok || value != row.Deleted {
			return false
		}
	}
	if raw, present := entry["position"]; present {
		value, ok := raw.(float64)
		if !ok || value != float64(row.Position) {
			return false
		}
	}
	if raw, present := entry["price_egp_cents"]; present {
		if raw == nil || !minorTextEqual(row.PriceEGPMinor, raw) {
			return false
		}
	}
	if raw, present := entry["price_usd_cents"]; present {
		if raw == nil {
			if row.PriceUSDMinor != nil {
				return false
			}
		} else if row.PriceUSDMinor == nil || !minorTextEqual(*row.PriceUSDMinor, raw) {
			return false
		}
	}
	return attributesOptionalMatch(row.Attributes, entry)
}

// attributesOptionalMatch verifies the requested attribute list when
// present (absent = unconstrained).
func attributesOptionalMatch(projected []AdminProductVariantAttribute, entry map[string]any) bool {
	raw, present := entry["attributes"]
	if !present {
		return true
	}
	desired, ok := raw.([]any)
	if !ok {
		return false
	}
	return attributesMatch(projected, desired)
}

// attributesMatch verifies a full requested attribute list against the
// projection: same definition codes with identical value codes, labels
// and positions.
func attributesMatch(projected []AdminProductVariantAttribute, desired []any) bool {
	if len(desired) != len(projected) {
		return false
	}
	byCode := make(map[string]AdminProductVariantAttribute, len(projected))
	for _, attr := range projected {
		byCode[attr.DefinitionCode] = attr
	}
	for _, item := range desired {
		entry, ok := item.(map[string]any)
		if !ok {
			return false
		}
		row, ok := byCode[payloadText(entry, "definition_code")]
		if !ok {
			return false
		}
		if payloadText(entry, "value_code") != row.ValueCode ||
			!optionalTextEqual(&row.NameAR, entry["name_ar"]) ||
			!optionalTextEqual(&row.NameEN, entry["name_en"]) ||
			!optionalTextEqual(&row.DefinitionNameAR, entry["definition_name_ar"]) ||
			!optionalTextEqual(&row.DefinitionNameEN, entry["definition_name_en"]) {
			return false
		}
		if raw, present := entry["position"]; present {
			value, ok := raw.(float64)
			if !ok || value != float64(row.Position) {
				return false
			}
		}
	}
	return true
}

func optionalTextEqual(stored *string, requested any) bool {
	value, _ := requested.(string)
	value = strings.TrimSpace(value)
	if stored == nil {
		return value == ""
	}
	return *stored == value
}

func payloadText(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return strings.TrimSpace(value)
}

// Compare exact decimal strings after the same whitespace/leading-zero
// normalization as Retail's integer parser; never use floating-point money.
func minorTextEqual(stored string, requested any) bool {
	value, ok := requested.(string)
	if !ok {
		return false
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	value = strings.TrimLeft(value, "0")
	if value == "" {
		value = "0"
	}
	return stored == value
}
