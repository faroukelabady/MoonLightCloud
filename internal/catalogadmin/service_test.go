package catalogadmin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
)

func errFakeNotFoundValue() error {
	return apperr.New(apperr.NotFound, "not found")
}

type fakeStore struct {
	commands  map[string]CommandView
	targets   map[string][]TargetView
	owner     map[string]string
	rev       map[string]int64
	bound     map[string][]BoundDevice
	capable   map[string]bool
	resultSet [][2]string
}

func (s *fakeStore) CreateCatalogAdminCommand(_ context.Context, cmd NewCommand) (CommandView, error) {
	view := CommandView{ID: cmd.ID, StoreID: cmd.StoreID, Type: cmd.Type, Version: cmd.Version,
		EntityID: cmd.EntityID, TargetKind: cmd.TargetKind, RequestedKey: cmd.RequestedKey,
		PayloadHash: cmd.PayloadHash, ExpectedRevision: cmd.ExpectedRevision,
		Actor: cmd.Actor, Status: CommandPending}
	s.commands[cmd.ID] = view
	return view, nil
}

func (s *fakeStore) CreateCatalogAdminTarget(_ context.Context, commandID, targetID, deviceID string) (TargetView, error) {
	t := TargetView{ID: targetID, CommandID: commandID, DeviceID: deviceID, Status: TargetPending}
	s.targets[commandID] = append(s.targets[commandID], t)
	return t, nil
}

func (s *fakeStore) GetCatalogAdminCommand(_ context.Context, id string) (CommandView, error) {
	view, ok := s.commands[id]
	if !ok {
		return CommandView{}, errFakeNotFound()
	}
	return view, nil
}

func (s *fakeStore) ListCatalogAdminCommands(_ context.Context, storeID, _, _, _ string, _ int, _ time.Time, _ string) ([]CommandView, error) {
	var out []CommandView
	for _, view := range s.commands {
		if view.StoreID == storeID {
			out = append(out, view)
		}
	}
	return out, nil
}

func (s *fakeStore) ListCatalogAdminTargets(_ context.Context, commandID string) ([]TargetView, error) {
	return s.targets[commandID], nil
}

func (s *fakeStore) ListCatalogAdminTargetsBatch(_ context.Context, commandIDs []string) (map[string][]TargetView, error) {
	out := map[string][]TargetView{}
	for _, id := range commandIDs {
		out[id] = s.targets[id]
	}
	return out, nil
}

func (s *fakeStore) DueCatalogAdminTargets(_ context.Context, deviceID string, _ int) ([]DueTarget, error) {
	var out []DueTarget
	for _, list := range s.targets {
		for _, t := range list {
			if t.DeviceID == deviceID && (t.Status == TargetPending || t.Status == TargetDelivered) {
				out = append(out, DueTarget{TargetID: t.ID, CommandID: t.CommandID, DeviceID: t.DeviceID,
					Status: t.Status, Type: "catalog.product.details.update.v1", Version: 1,
					StoreID: s.commands[t.CommandID].StoreID, EntityID: "e",
					Payload: []byte(`{}`), PayloadHash: "h"})
			}
		}
	}
	return out, nil
}

func (s *fakeStore) FinishCatalogAdminTarget(_ context.Context, targetID, deviceID, status, code, entityID string, pre, post int64) (bool, error) {
	for cmdID, list := range s.targets {
		for i, t := range list {
			if t.ID == targetID {
				if t.DeviceID != deviceID {
					return false, nil
				}
				list[i].Status = status
				list[i].ResultCode = code
				list[i].EntityID = entityID
				list[i].PreRevision = pre
				list[i].PostRevision = post
				s.targets[cmdID] = list
				if status == TargetApplied {
					if err := s.SetCommandResultEntity(context.Background(), targetID, entityID); err != nil {
						return false, err
					}
				}
				return true, nil
			}
		}
	}
	return false, nil
}

func (s *fakeStore) MarkCatalogAdminTargetDelivered(_ context.Context, _, _ string) error {
	return nil
}

func (s *fakeStore) CancelCatalogAdminCommand(_ context.Context, commandID string) (bool, error) {
	view, ok := s.commands[commandID]
	if !ok || view.Status != CommandPending {
		return false, nil
	}
	view.Status = CommandCancelled
	s.commands[commandID] = view
	for cmdID, list := range s.targets {
		if cmdID != commandID {
			continue
		}
		for i := range list {
			if list[i].Status == TargetPending || list[i].Status == TargetDelivered {
				list[i].Status = TargetCancelled
			}
		}
		s.targets[cmdID] = list
	}
	return true, nil
}

func (s *fakeStore) UpsertCatalogAdminCapability(_ context.Context, deviceID string, capable bool) error {
	s.capable[deviceID] = capable
	return nil
}

func (s *fakeStore) GetCatalogAdminCapability(_ context.Context, deviceID string) (bool, error) {
	return s.capable[deviceID], nil
}

func (s *fakeStore) ListCatalogAdminBoundDevices(_ context.Context, storeID string) ([]BoundDevice, error) {
	return s.bound[storeID], nil
}

func (s *fakeStore) CatalogAdminOwnership(_ context.Context, _, entityID, _ string) (string, int64, bool, error) {
	owner, ok := s.owner[entityID]
	if !ok {
		return "", 0, false, nil
	}
	return owner, s.rev[entityID], true, nil
}

func (s *fakeStore) AdminProductList(_ context.Context, _, _, _ string, _ int) ([]AdminProductRow, error) {
	return nil, nil
}

func (s *fakeStore) AdminProductDetail(_ context.Context, _, _ string) (AdminProductDetail, error) {
	return AdminProductDetail{}, errFakeNotFound()
}

func (s *fakeStore) AdminProductConfigurations(_ context.Context, _, _ string) ([]AdminConfigurationRow, error) {
	return nil, nil
}

func (s *fakeStore) AdminCategoryList(_ context.Context, _ string) ([]AdminCategoryRow, error) {
	return nil, nil
}

func (s *fakeStore) AdminTagList(_ context.Context, _ string) ([]AdminTagRow, error) {
	return nil, nil
}

func (s *fakeStore) AdminProductVariants(_ context.Context, _, _ string) ([]AdminProductVariant, error) {
	return nil, nil
}

func (s *fakeStore) AdminProductVariant(_ context.Context, _, _ string) (AdminProductVariant, error) {
	return AdminProductVariant{Attributes: []AdminProductVariantAttribute{}}, nil
}

func (s *fakeStore) AdminProductTypeList(_ context.Context, _ string) ([]AdminProductType, error) {
	return nil, nil
}

func (s *fakeStore) AdminProductType(_ context.Context, _, _ string) (AdminProductType, error) {
	return AdminProductType{}, errFakeNotFound()
}

func (s *fakeStore) SetCommandResultEntity(_ context.Context, targetID, entityID string) error {
	s.resultSet = append(s.resultSet, [2]string{targetID, entityID})
	for cmdID, list := range s.targets {
		for _, t := range list {
			if t.ID == targetID {
				if view, ok := s.commands[cmdID]; ok && view.ResultEntityID == "" {
					view.ResultEntityID = entityID
					s.commands[cmdID] = view
				}
				return nil
			}
		}
	}
	return nil
}

type fakeDevices struct {
	binding map[string]string
	active  map[string]bool
}

func (s *fakeDevices) BindingStore(_ context.Context, deviceID string) (string, error) {
	return s.binding[deviceID], nil
}

func (s *fakeDevices) DeviceActive(_ context.Context, deviceID string) (bool, error) {
	return s.active[deviceID], nil
}

func (s *fakeDevices) DeviceName(_ context.Context, _ string) string { return "dev" }

func testService() (*Service, *fakeStore, *fakeDevices, string, string) {
	storeID := uuid.NewString()
	entityID := uuid.NewString()
	store := &fakeStore{
		commands: map[string]CommandView{},
		targets:  map[string][]TargetView{},
		owner:    map[string]string{entityID: storeID},
		rev:      map[string]int64{entityID: 5},
		bound:    map[string][]BoundDevice{},
		capable:  map[string]bool{},
	}
	devices := &fakeDevices{binding: map[string]string{}, active: map[string]bool{}}
	return NewService(store, devices), store, devices, storeID, entityID
}

func validPayload(entityID string) []byte {
	raw, _ := json.Marshal(map[string]any{"product_id": entityID})
	return raw
}

// TestServiceCreateSnapshotsTargets ensures creation snapshots the
// current eligible device set and reports PENDING (never success).
func TestServiceCreateSnapshotsTargets(t *testing.T) {
	svc, store, devices, storeID, entityID := testService()
	ctx := context.Background()
	devA, devB := uuid.NewString(), uuid.NewString()
	for _, dev := range []string{devA, devB} {
		devices.active[dev] = true
		devices.binding[dev] = storeID
		store.capable[dev] = true
	}
	svc.store.(*fakeStore).bound[storeID] = []BoundDevice{{DeviceID: devA}, {DeviceID: devB}}

	view, err := svc.Create(ctx, "op", storeID, TypeProductDetailsUpdateV1, entityID, 5, validPayload(entityID))
	if err != nil {
		t.Fatal(err)
	}
	if view.Aggregate != AggregatePending {
		t.Fatalf("aggregate: %s", view.Aggregate)
	}
	if len(view.Targets) != 2 {
		t.Fatalf("targets: %d", len(view.Targets))
	}
	seen := map[string]bool{}
	for _, target := range view.Targets {
		seen[target.DeviceID] = true
	}
	if !seen[devA] || !seen[devB] {
		t.Fatalf("targets must snapshot bound devices: %+v", view.Targets)
	}
}

// TestServiceCancelAfterAppliedFails ensures cancellation never
// pretends to roll back applied work.
func TestServiceCancelAfterAppliedFails(t *testing.T) {
	svc, store, _, storeID, entityID := testService()
	ctx := context.Background()
	dev := uuid.NewString()
	store.bound[storeID] = []BoundDevice{{DeviceID: dev}}

	view, err := svc.Create(ctx, "op", storeID, TypeProductDetailsUpdateV1, entityID, 5, validPayload(entityID))
	if err != nil {
		t.Fatal(err)
	}
	targetID := view.Targets[0].ID
	if _, err := store.FinishCatalogAdminTarget(ctx, targetID, dev, TargetApplied, CodeApplied, entityID, 5, 6); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Cancel(ctx, storeID, view.ID); err == nil {
		t.Fatal("cancel after apply must fail")
	}
	got, err := svc.Get(ctx, storeID, view.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Aggregate != AggregateApplied {
		t.Fatalf("aggregate: %s", got.Aggregate)
	}
}

// TestServicePollGatesCapabilityBindingRevocation ensures delivery
// gating: incapable devices get nothing, rebound devices skip old
// Store targets, revoked devices get nothing.
func TestServicePollGatesCapabilityBindingRevocation(t *testing.T) {
	svc, store, devices, storeID, entityID := testService()
	ctx := context.Background()
	dev := uuid.NewString()
	devices.binding[dev] = storeID
	devices.active[dev] = true
	store.bound[storeID] = []BoundDevice{{DeviceID: dev}}

	view, err := svc.Create(ctx, "op", storeID, TypeProductDetailsUpdateV1, entityID, 5, validPayload(entityID))
	if err != nil {
		t.Fatal(err)
	}
	_ = view
	// Incapable: nothing due.
	due, err := svc.Poll(ctx, dev, 10)
	if err != nil || len(due) != 0 {
		t.Fatalf("incapable poll: %+v %v", due, err)
	}
	// Capable: one due target.
	if _, err := svc.ReportCapabilities(ctx, dev, []string{CapabilityV1}); err != nil {
		t.Fatal(err)
	}
	due, err = svc.Poll(ctx, dev, 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("capable poll: %+v %v", due, err)
	}
	// Rebound to another Store: target skipped, nothing due.
	devices.binding[dev] = uuid.NewString()
	due, err = svc.Poll(ctx, dev, 10)
	if err != nil || len(due) != 0 {
		t.Fatalf("rebound poll: %+v %v", due, err)
	}
	targets, _ := store.ListCatalogAdminTargets(ctx, view.ID)
	if targets[0].Status != TargetSkippedRevoked {
		t.Fatalf("rebound target: %s", targets[0].Status)
	}
	// Revoked device: rejected (middleware rejects revoked credentials
	// first; this is defense in depth).
	devices.binding[dev] = storeID
	devices.active[dev] = false
	if _, err := svc.Poll(ctx, dev, 10); err == nil {
		t.Fatal("revoked poll must fail")
	}
}

// TestServiceConvergenceRequiresProjection ensures APPLIED is not
// CONVERGED until the projection reaches the resulting revision.
func TestServiceConvergenceRequiresProjection(t *testing.T) {
	svc, store, devices, storeID, entityID := testService()
	ctx := context.Background()
	dev := uuid.NewString()
	devices.binding[dev] = storeID
	devices.active[dev] = true
	store.capable[dev] = true
	store.bound[storeID] = []BoundDevice{{DeviceID: dev}}

	view, err := svc.Create(ctx, "op", storeID, TypeProductDetailsUpdateV1, entityID, 5, validPayload(entityID))
	if err != nil {
		t.Fatal(err)
	}
	targetID := view.Targets[0].ID
	// Wrong-device outcome must not apply.
	if err := svc.ReportOutcome(ctx, uuid.NewString(), targetID, TargetApplied, CodeApplied, entityID, 5, 6); err == nil {
		t.Fatal("wrong-device outcome must fail")
	}
	if err := svc.ReportOutcome(ctx, dev, targetID, TargetApplied, CodeApplied, entityID, 5, 6); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Get(ctx, storeID, view.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Aggregate != AggregateApplied || got.Converged {
		t.Fatalf("applied but projection still rev 5: %+v", got.Aggregate)
	}
	// Projection converges to post_revision 6.
	store.rev[entityID] = 6
	got, err = svc.Get(ctx, storeID, view.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Aggregate != AggregateConverged || !got.Converged {
		t.Fatalf("converged: %+v %v", got.Aggregate, got.Converged)
	}
}

func errFakeNotFound() error {
	return errFakeNotFoundValue()
}

func (s *fakeStore) CreateCatalogAdminCommandWithTargets(ctx context.Context, cmd NewCommand) (CommandView, error) {
	v, err := s.CreateCatalogAdminCommand(ctx, cmd)
	if err != nil {
		return v, err
	}
	bound, err := s.ListCatalogAdminBoundDevices(ctx, cmd.StoreID)
	if err != nil {
		return v, err
	}
	for _, dev := range bound {
		if _, err = s.CreateCatalogAdminTarget(ctx, v.ID, uuid.NewString(), dev.DeviceID); err != nil {
			return v, err
		}
	}
	return v, nil
}

type noopConfigurationStore struct {
	*fakeStore
	rows []AdminConfigurationRow
}

func (s *noopConfigurationStore) AdminProductConfigurations(context.Context, string, string) ([]AdminConfigurationRow, error) {
	return s.rows, nil
}
func TestNoopConfigurationsRequiresCompleteProjectedIntent(t *testing.T) {
	_, base, devices, _, _ := testService()
	store := &noopConfigurationStore{fakeStore: base, rows: []AdminConfigurationRow{{ID: "existing", Enabled: true}}}
	svc := NewService(store, devices)
	view := CommandView{Type: TypeProductConfigurationsUpdateV1, Payload: []byte(`{"configurations":[]}`)}
	if svc.noopProjected(context.Background(), view) {
		t.Fatal("empty desired set cannot converge while omitted configuration remains enabled")
	}
	store.rows[0].Enabled = false
	if !svc.noopProjected(context.Background(), view) {
		t.Fatal("already disabled omitted rows are a genuine no-op")
	}
}

func TestNoopConfigurationsPreservesExactMoneyAndLabels(t *testing.T) {
	_, base, devices, _, _ := testService()
	id := uuid.NewString()
	english := "English"
	usd := "0"
	store := &noopConfigurationStore{fakeStore: base, rows: []AdminConfigurationRow{{ID: id, StyleCode: "gold", ColorCode: "white", StyleNameAR: "ذهبي", ColorNameAR: "أبيض", StyleNameEN: &english, EGPDeltaMinor: "9007199254740993", USDDeltaMinor: &usd, Enabled: true, Position: 0}}}
	svc := NewService(store, devices)
	payload, _ := json.Marshal(map[string]any{"configurations": []map[string]any{{"id": strings.ToUpper(id), "style_code": " gold ", "color_code": "white", "style_name_ar": "ذهبي", "color_name_ar": "أبيض", "style_name_en": " English ", "egp_delta_cents": "09007199254740993", "usd_delta_cents": "000", "enabled": true}}})
	view := CommandView{Type: TypeProductConfigurationsUpdateV1, Payload: payload}
	if !svc.noopProjected(context.Background(), view) {
		t.Fatal("canonical equivalent no-op should converge without float money")
	}
	wrong := "Changed"
	store.rows[0].StyleNameEN = &wrong
	if svc.noopProjected(context.Background(), view) {
		t.Fatal("different English label cannot converge")
	}
	store.rows[0].StyleNameEN = &english
	store.rows[0].Position = 1
	if svc.noopProjected(context.Background(), view) {
		t.Fatal("different position cannot converge")
	}
}

// TestServiceCreateCarriesNoFakeEntity proves the R3 closure at the
// service boundary: a create stores target_kind=create with the absent
// entity marker (""), the requested code separately, and never a
// placeholder business UUID.
func TestServiceCreateCarriesNoFakeEntity(t *testing.T) {
	svc, store, devices, storeID, _ := testService()
	ctx := context.Background()
	dev := uuid.NewString()
	devices.binding[dev] = storeID
	devices.active[dev] = true
	store.capable[dev] = true
	store.bound[storeID] = []BoundDevice{{DeviceID: dev}}

	payload, _ := json.Marshal(map[string]any{
		"code": "ledger", "name_ar": "دفتر", "name_en": "Ledger",
		"dimensions": []any{}, "capabilities": []any{},
		"expected_type_revision": float64(0),
	})
	view, err := svc.Create(ctx, "op", storeID, TypeProductTypeCreateV1, "", 0, payload)
	if err != nil {
		t.Fatal(err)
	}
	if view.TargetKind != TargetKindCreate {
		t.Fatalf("target kind: %q", view.TargetKind)
	}
	if view.EntityID != "" {
		t.Fatalf("create must carry no business entity, got %q", view.EntityID)
	}
	if view.RequestedKey != "ledger" {
		t.Fatalf("requested key: %q", view.RequestedKey)
	}
	if view.ResultEntityID != "" {
		t.Fatalf("unapplied create must carry no result identity, got %q", view.ResultEntityID)
	}
	if _, err := uuid.Parse(view.EntityID); view.EntityID != "" && err != nil {
		t.Fatalf("entity marker must never look like a faked UUID: %q", view.EntityID)
	}

	// A placeholder UUID is rejected for creates (C01).
	if _, err := svc.Create(ctx, "op", storeID, TypeProductTypeCreateV1, uuid.NewString(), 0, payload); err == nil {
		t.Fatal("placeholder entity UUID must be rejected for creates")
	}
	// Bad codes are rejected at creation (C06).
	bad, _ := json.Marshal(map[string]any{"code": "BAD CODE", "expected_type_revision": float64(0)})
	if _, err := svc.Create(ctx, "op", storeID, TypeProductTypeCreateV1, "", 0, bad); err == nil {
		t.Fatal("bad code must be rejected at creation")
	}
}

// TestServiceCreateConvergesOnResultID proves C04: APPLIED alone never
// converges a create; convergence evaluates the Retail-minted result
// identity (ownership + revision on the ACTUAL type, never the marker).
func TestServiceCreateConvergesOnResultID(t *testing.T) {
	svc, store, devices, storeID, _ := testService()
	ctx := context.Background()
	dev := uuid.NewString()
	devices.binding[dev] = storeID
	devices.active[dev] = true
	store.capable[dev] = true
	store.bound[storeID] = []BoundDevice{{DeviceID: dev}}

	payload, _ := json.Marshal(map[string]any{
		"code": "ledger", "name_ar": "دفتر", "name_en": "Ledger",
		"dimensions": []any{}, "capabilities": []any{},
		"expected_type_revision": float64(0),
	})
	view, err := svc.Create(ctx, "op", storeID, TypeProductTypeCreateV1, "", 0, payload)
	if err != nil {
		t.Fatal(err)
	}
	realID := uuid.NewString()
	targetID := view.Targets[0].ID
	if err := svc.ReportOutcome(ctx, dev, targetID, TargetApplied, CodeApplied, realID, 0, 1); err != nil {
		t.Fatal(err)
	}
	if len(store.resultSet) != 1 || store.resultSet[0][1] != realID {
		t.Fatalf("result identity must persist on the command: %+v", store.resultSet)
	}
	got, err := svc.Get(ctx, storeID, view.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ResultEntityID != realID {
		t.Fatalf("history must expose the actual result identity: %+v", got)
	}
	if got.Aggregate != AggregateApplied || got.Converged {
		t.Fatalf("applied without projection must not converge: %+v", got.Aggregate)
	}
	// Projection of the ACTUAL type converges (ownership + revision).
	store.owner[realID] = storeID
	store.rev[realID] = 1
	got, err = svc.Get(ctx, storeID, view.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Aggregate != AggregateConverged || !got.Converged {
		t.Fatalf("must converge on the result identity: %+v %v", got.Aggregate, got.Converged)
	}
}
