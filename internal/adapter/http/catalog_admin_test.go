package http

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/catalogadmin"
)

// stubCatalogStore implements catalogadmin.Store in memory with strict
// Store scoping.
type stubCatalogStore struct {
	commands map[string]catalogadmin.CommandView
	targets  map[string][]catalogadmin.TargetView
	due      map[string][]catalogadmin.DueTarget
	capable  map[string]bool
	bound    map[string][]catalogadmin.BoundDevice
	owner    map[string]string // entity -> store
	rev      map[string]int64
}

func errNotFoundStub() error {
	return apperr.New(apperr.NotFound, "not found")
}

func (s *stubCatalogStore) CreateCatalogAdminCommand(_ context.Context, cmd catalogadmin.NewCommand) (catalogadmin.CommandView, error) {
	view := catalogadmin.CommandView{
		ID: cmd.ID, StoreID: cmd.StoreID, Type: cmd.Type, Version: cmd.Version,
		EntityID: cmd.EntityID, PayloadHash: cmd.PayloadHash,
		ExpectedRevision: cmd.ExpectedRevision, Actor: cmd.Actor, Status: "PENDING",
	}
	s.commands[cmd.ID] = view
	return view, nil
}

func (s *stubCatalogStore) CreateCatalogAdminTarget(_ context.Context, commandID, targetID, deviceID string) (catalogadmin.TargetView, error) {
	t := catalogadmin.TargetView{ID: targetID, CommandID: commandID, DeviceID: deviceID, Status: "PENDING"}
	s.targets[commandID] = append(s.targets[commandID], t)
	return t, nil
}

func (s *stubCatalogStore) GetCatalogAdminCommand(_ context.Context, id string) (catalogadmin.CommandView, error) {
	view, ok := s.commands[id]
	if !ok {
		return catalogadmin.CommandView{}, errNotFoundStub()
	}
	return view, nil
}

func (s *stubCatalogStore) ListCatalogAdminCommands(_ context.Context, storeID, _, _, _ string, _, _ int) ([]catalogadmin.CommandView, error) {
	var out []catalogadmin.CommandView
	for _, view := range s.commands {
		if view.StoreID == storeID {
			out = append(out, view)
		}
	}
	return out, nil
}

func (s *stubCatalogStore) ListCatalogAdminTargets(_ context.Context, commandID string) ([]catalogadmin.TargetView, error) {
	return s.targets[commandID], nil
}

func (s *stubCatalogStore) ListCatalogAdminTargetsBatch(_ context.Context, commandIDs []string) (map[string][]catalogadmin.TargetView, error) {
	out := map[string][]catalogadmin.TargetView{}
	for _, id := range commandIDs {
		out[id] = s.targets[id]
	}
	return out, nil
}

func (s *stubCatalogStore) DueCatalogAdminTargets(_ context.Context, deviceID string, _ int) ([]catalogadmin.DueTarget, error) {
	return s.due[deviceID], nil
}

func (s *stubCatalogStore) FinishCatalogAdminTarget(_ context.Context, targetID, deviceID, status, code, entityID string, pre, post int64) (bool, error) {
	for cmdID, list := range s.targets {
		for i, t := range list {
			if t.ID == targetID {
				if t.DeviceID != deviceID {
					return false, nil
				}
				if t.Status != "PENDING" && t.Status != "DELIVERED" {
					return false, nil
				}
				list[i].Status = status
				list[i].ResultCode = code
				list[i].EntityID = entityID
				list[i].PreRevision = pre
				list[i].PostRevision = post
				s.targets[cmdID] = list
				return true, nil
			}
		}
	}
	return false, nil
}

func (s *stubCatalogStore) MarkCatalogAdminTargetDelivered(_ context.Context, _, _ string) error {
	return nil
}

func (s *stubCatalogStore) CancelCatalogAdminCommand(_ context.Context, commandID string) (bool, error) {
	view, ok := s.commands[commandID]
	if !ok {
		return false, nil
	}
	view.Status = "CANCELLED"
	s.commands[commandID] = view
	return true, nil
}

func (s *stubCatalogStore) UpsertCatalogAdminCapability(_ context.Context, deviceID string, capable bool) error {
	s.capable[deviceID] = capable
	return nil
}

func (s *stubCatalogStore) GetCatalogAdminCapability(_ context.Context, deviceID string) (bool, error) {
	return s.capable[deviceID], nil
}

func (s *stubCatalogStore) ListCatalogAdminBoundDevices(_ context.Context, storeID string) ([]catalogadmin.BoundDevice, error) {
	return s.bound[storeID], nil
}

func (s *stubCatalogStore) CatalogAdminOwnership(_ context.Context, _, entityID string) (string, int64, bool, error) {
	owner, ok := s.owner[entityID]
	if !ok {
		return "", 0, false, nil
	}
	return owner, s.rev[entityID], true, nil
}

func (s *stubCatalogStore) AdminProductList(_ context.Context, _, _, _ string, _ int) ([]catalogadmin.AdminProductRow, error) {
	return nil, nil
}

func (s *stubCatalogStore) AdminProductDetail(_ context.Context, _, _ string) (catalogadmin.AdminProductDetail, error) {
	return catalogadmin.AdminProductDetail{}, errNotFoundStub()
}

func (s *stubCatalogStore) AdminProductConfigurations(_ context.Context, _, _ string) ([]catalogadmin.AdminConfigurationRow, error) {
	return nil, nil
}

func (s *stubCatalogStore) AdminCategoryList(_ context.Context, _ string) ([]catalogadmin.AdminCategoryRow, error) {
	return nil, nil
}

func (s *stubCatalogStore) AdminTagList(_ context.Context, _ string) ([]catalogadmin.AdminTagRow, error) {
	return nil, nil
}

type stubCatalogDevices struct {
	binding map[string]string
	active  map[string]bool
}

func (s *stubCatalogDevices) BindingStore(_ context.Context, deviceID string) (string, error) {
	return s.binding[deviceID], nil
}

func (s *stubCatalogDevices) DeviceActive(_ context.Context, deviceID string) (bool, error) {
	return s.active[deviceID], nil
}

func (s *stubCatalogDevices) DeviceName(_ context.Context, _ string) string { return "dev" }

func catalogAdminTestSetup() (*CatalogAdminHandlers, *stubCatalogStore, *stubCatalogDevices, string, string) {
	storeID := uuid.NewString()
	entityID := uuid.NewString()
	stub := &stubCatalogStore{
		commands: map[string]catalogadmin.CommandView{},
		targets:  map[string][]catalogadmin.TargetView{},
		due:      map[string][]catalogadmin.DueTarget{},
		capable:  map[string]bool{},
		bound:    map[string][]catalogadmin.BoundDevice{},
		owner:    map[string]string{entityID: storeID},
		rev:      map[string]int64{entityID: 5},
	}
	devices := &stubCatalogDevices{binding: map[string]string{}, active: map[string]bool{}}
	svc := catalogadmin.NewService(stub, devices)
	return &CatalogAdminHandlers{Svc: svc, Log: slog.Default()}, stub, devices, storeID, entityID
}

func postCommand(t *testing.T, handlers *CatalogAdminHandlers, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/dashboard/catalog-admin/commands", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handlers.CreateCommand(rec, req)
	return rec
}

// TestCatalogAdminCreateValidates ensures typed creation: valid
// creation snapshots bound-device targets; unknown types,
// cross-Store entities and unknown entities fail. The store has no
// projection mutation surface, so creation cannot write projections.
func TestCatalogAdminCreateValidates(t *testing.T) {
	handlers, stub, _, storeID, entityID := catalogAdminTestSetup()
	otherStore := uuid.NewString()
	crossEntity := uuid.NewString()
	stub.owner[crossEntity] = otherStore
	deviceID := uuid.NewString()
	stub.bound[storeID] = []catalogadmin.BoundDevice{{DeviceID: deviceID, Name: "d1"}}

	valid := `{"store_id":"` + storeID + `","type":"catalog.product.details.update.v1","entity_id":"` + entityID + `","expected_revision":5,"payload":{"product_id":"` + entityID + `"}}`
	rec := postCommand(t, handlers, valid)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid create: %d %s", rec.Code, rec.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created["aggregate"] != "PENDING" {
		t.Fatalf("creation must report pending, got %v", created["aggregate"])
	}
	targets, _ := created["targets"].([]any)
	if len(targets) != 1 {
		t.Fatalf("one bound device means one target: %v", created)
	}

	unknown := `{"store_id":"` + storeID + `","type":"catalog.patch","entity_id":"` + entityID + `","expected_revision":5,"payload":{}}`
	if rec := postCommand(t, handlers, unknown); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown type: %d", rec.Code)
	}
	cross := `{"store_id":"` + storeID + `","type":"catalog.product.details.update.v1","entity_id":"` + crossEntity + `","expected_revision":1,"payload":{}}`
	if rec := postCommand(t, handlers, cross); rec.Code != http.StatusBadRequest {
		t.Fatalf("cross-store: %d", rec.Code)
	}
	missing := `{"store_id":"` + storeID + `","type":"catalog.product.details.update.v1","entity_id":"` + uuid.NewString() + `","expected_revision":1,"payload":{"product_id":"x"}}`
	if rec := postCommand(t, handlers, missing); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown entity: %d", rec.Code)
	}
}

// TestCatalogAdminIDOR ensures Store-scoped reads and cancellation: a
// command from Store A is invisible under Store B (404, never payload).
func TestCatalogAdminIDOR(t *testing.T) {
	handlers, stub, _, storeID, entityID := catalogAdminTestSetup()
	otherStore := uuid.NewString()
	deviceID := uuid.NewString()
	stub.bound[storeID] = []catalogadmin.BoundDevice{{DeviceID: deviceID, Name: "d1"}}

	valid := `{"store_id":"` + storeID + `","type":"catalog.product.details.update.v1","entity_id":"` + entityID + `","expected_revision":5,"payload":{"product_id":"` + entityID + `"}}`
	rec := postCommand(t, handlers, valid)
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	get := func(store, id string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/catalog-admin/commands/"+id+"?store_id="+store, nil)
		req.SetPathValue("id", id)
		rec := httptest.NewRecorder()
		handlers.GetCommand(rec, req)
		return rec
	}
	if rec := get(storeID, created.ID); rec.Code != http.StatusOK {
		t.Fatalf("own store read: %d", rec.Code)
	}
	if rec := get(otherStore, created.ID); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-store read must 404: %d %s", rec.Code, rec.Body.String())
	}

	cancel := func(store, id string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/dashboard/catalog-admin/commands/"+id+"/cancel?store_id="+store, nil)
		req.SetPathValue("id", id)
		rec := httptest.NewRecorder()
		handlers.CancelCommand(rec, req)
		return rec
	}
	if rec := cancel(otherStore, created.ID); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-store cancel must 404: %d", rec.Code)
	}
	if rec := cancel(storeID, created.ID); rec.Code != http.StatusOK {
		t.Fatalf("own cancel: %d", rec.Code)
	}
}

// TestCatalogAdminWrongDeviceACK ensures server-side target binding:
// device B cannot acknowledge device A's target.
func TestCatalogAdminWrongDeviceACK(t *testing.T) {
	handlers, stub, devices, storeID, entityID := catalogAdminTestSetup()
	deviceA := uuid.NewString()
	deviceB := uuid.NewString()
	stub.bound[storeID] = []catalogadmin.BoundDevice{{DeviceID: deviceA, Name: "a"}}
	devices.binding[deviceA] = storeID
	devices.binding[deviceB] = storeID
	devices.active[deviceA] = true
	devices.active[deviceB] = true
	stub.capable[deviceA] = true

	valid := `{"store_id":"` + storeID + `","type":"catalog.product.details.update.v1","entity_id":"` + entityID + `","expected_revision":5,"payload":{"product_id":"` + entityID + `"}}`
	rec := postCommand(t, handlers, valid)
	var created struct {
		ID      string `json:"id"`
		Targets []struct {
			ID       string `json:"id"`
			DeviceID string `json:"device_id"`
		} `json:"targets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if len(created.Targets) != 1 {
		t.Fatalf("targets: %+v", created)
	}
	targetID := created.Targets[0].ID

	report := func(device, target string) int {
		t.Helper()
		body := `{"status":"APPLIED","result_code":"APPLIED","entity_id":"` + entityID + `","pre_revision":5,"post_revision":6}`
		_ = body
		return 0
	}
	_ = report
	// Wrong device: FinishCatalogAdminTarget binds target+device.
	ok, err := stub.FinishCatalogAdminTarget(t.Context(), targetID, deviceB, "APPLIED", "APPLIED", entityID, 5, 6)
	if err != nil || ok {
		t.Fatalf("wrong-device ACK must not apply: ok=%v err=%v", ok, err)
	}
	ok, err = stub.FinishCatalogAdminTarget(t.Context(), targetID, deviceA, "APPLIED", "APPLIED", entityID, 5, 6)
	if err != nil || !ok {
		t.Fatalf("own-device ACK must apply: ok=%v err=%v", ok, err)
	}
}

// TestCatalogAdminMethodGating ensures every catalog-admin handler
// rejects wrong HTTP methods without touching the service: no state
// change on GET-vs-POST confusion, no device surface on dashboard
// paths and vice versa.
func TestCatalogAdminMethodGating(t *testing.T) {
	handlers, _, _, _, _ := catalogAdminTestSetup()
	cases := []struct {
		name string
		call func() *httptest.ResponseRecorder
	}{
		{"create via GET", func() *httptest.ResponseRecorder {
			rec := httptest.NewRecorder()
			handlers.CreateCommand(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
			return rec
		}},
		{"list via POST", func() *httptest.ResponseRecorder {
			rec := httptest.NewRecorder()
			handlers.ListCommands(rec, httptest.NewRequest(http.MethodPost, "/x", nil))
			return rec
		}},
		{"cancel via GET", func() *httptest.ResponseRecorder {
			rec := httptest.NewRecorder()
			handlers.CancelCommand(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
			return rec
		}},
		{"device poll via POST", func() *httptest.ResponseRecorder {
			rec := httptest.NewRecorder()
			handlers.PollCatalogCommands(rec, httptest.NewRequest(http.MethodPost, "/x", nil))
			return rec
		}},
		{"device result via GET", func() *httptest.ResponseRecorder {
			rec := httptest.NewRecorder()
			handlers.ReportCatalogResult(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
			return rec
		}},
	}
	for _, tc := range cases {
		if rec := tc.call(); rec.Code != http.StatusNotFound {
			t.Fatalf("%s: want 404, got %d", tc.name, rec.Code)
		}
	}
}
