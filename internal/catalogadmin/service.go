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
	owner, _, found, err := s.store.CatalogAdminOwnership(ctx, TypeProductDetailsUpdateV1, strings.TrimSpace(productID), storeID)
	if err != nil {
		return nil, err
	}
	if !found || owner == "" || !strings.EqualFold(owner, strings.TrimSpace(storeID)) {
		return nil, apperr.New(apperr.NotFound, CodeEntityNotFound)
	}
	return s.store.AdminProductConfigurations(ctx, strings.TrimSpace(storeID), strings.TrimSpace(productID))
}
