package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/auth"
	"github.com/faroukelabady/MoonLightCloud/internal/devicecontrol"
)

type memCtlStore struct {
	mu       sync.Mutex
	cmds     map[string]devicecontrol.Command
	byKey    map[string]string
	presence map[string]devicecontrol.Presence
}

func newMemCtlStore() *memCtlStore {
	return &memCtlStore{cmds: map[string]devicecontrol.Command{}, byKey: map[string]string{}, presence: map[string]devicecontrol.Presence{}}
}

func (m *memCtlStore) TouchSeen(_ context.Context, deviceID string, at time.Time, isPoll bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.presence[deviceID]
	t := at.UTC()
	p.DeviceID = deviceID
	p.LastSeenAt = &t
	if isPoll {
		p.LastPollAt = &t
	}
	m.presence[deviceID] = p
	return nil
}

func (m *memCtlStore) TouchAccepted(_ context.Context, deviceID string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.presence[deviceID]
	t := at.UTC()
	p.DeviceID = deviceID
	p.LastAcceptedAt = &t
	m.presence[deviceID] = p
	return nil
}

func (m *memCtlStore) TouchFinished(_ context.Context, deviceID string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.presence[deviceID]
	t := at.UTC()
	p.DeviceID = deviceID
	p.LastFinishedAt = &t
	m.presence[deviceID] = p
	return nil
}

func (m *memCtlStore) GetPresence(_ context.Context, deviceID string) (devicecontrol.Presence, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.presence[deviceID]
	return p, ok, nil
}

func (m *memCtlStore) ListPresence(_ context.Context) ([]devicecontrol.Presence, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]devicecontrol.Presence, 0, len(m.presence))
	for _, p := range m.presence {
		out = append(out, p)
	}
	return out, nil
}

func (m *memCtlStore) CreateCommand(_ context.Context, id, deviceID, key string, at time.Time) (devicecontrol.Command, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cmd := devicecontrol.Command{ID: id, DeviceID: deviceID, Type: "sync_now", Version: 1, IdempotencyKey: key, Status: "pending", RequestedAt: at}
	m.cmds[id] = cmd
	m.byKey[deviceID+"\x00"+key] = id
	return cmd, nil
}

func (m *memCtlStore) GetCommand(_ context.Context, id string) (devicecontrol.Command, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.cmds[id]
	return c, ok, nil
}

func (m *memCtlStore) GetCommandForDevice(_ context.Context, id, deviceID string) (devicecontrol.Command, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.cmds[id]
	if !ok || c.DeviceID != deviceID {
		return devicecontrol.Command{}, false, nil
	}
	return c, true, nil
}

func (m *memCtlStore) GetByIdempotency(_ context.Context, deviceID, key string) (devicecontrol.Command, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.byKey[deviceID+"\x00"+key]
	if !ok {
		return devicecontrol.Command{}, false, nil
	}
	return m.cmds[id], true, nil
}

func (m *memCtlStore) GetActive(_ context.Context, deviceID string) (devicecontrol.Command, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.cmds {
		if c.DeviceID == deviceID && devicecontrol.IsActive(c.Status) {
			return c, true, nil
		}
	}
	return devicecontrol.Command{}, false, nil
}

func (m *memCtlStore) PollClaim(_ context.Context, deviceID string, now time.Time, lease time.Duration) (devicecontrol.Command, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, c := range m.cmds {
		if c.DeviceID != deviceID || c.Status != "pending" {
			continue
		}
		c.Status = "leased"
		c.LeaseGeneration++
		m.cmds[id] = c
		return c, true, nil
	}
	return devicecontrol.Command{}, false, nil
}

func (m *memCtlStore) Accept(_ context.Context, id, deviceID string, at time.Time) (devicecontrol.Command, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.cmds[id]
	if !ok || c.DeviceID != deviceID {
		return devicecontrol.Command{}, false, nil
	}
	if devicecontrol.IsTerminal(c.Status) {
		return c, false, nil
	}
	if c.Status != "pending" && c.Status != "leased" && c.Status != "accepted" {
		return c, false, nil
	}
	c.Status = "accepted"
	m.cmds[id] = c
	return c, true, nil
}

func (m *memCtlStore) MarkRunning(_ context.Context, id, deviceID string, at time.Time) (devicecontrol.Command, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.cmds[id]
	if !ok || c.DeviceID != deviceID {
		return devicecontrol.Command{}, false, nil
	}
	if devicecontrol.IsTerminal(c.Status) {
		return c, false, nil
	}
	c.Status = "running"
	m.cmds[id] = c
	return c, true, nil
}

func (m *memCtlStore) Finish(_ context.Context, id, deviceID, status, code string, at time.Time) (devicecontrol.Command, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.cmds[id]
	if !ok || c.DeviceID != deviceID {
		return devicecontrol.Command{}, false, nil
	}
	if devicecontrol.IsTerminal(c.Status) {
		return c, false, nil
	}
	c.Status = status
	c.ResultCode = &code
	m.cmds[id] = c
	return c, true, nil
}

func (m *memCtlStore) Recent(_ context.Context, deviceID string, limit int) ([]devicecontrol.Command, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []devicecontrol.Command
	for _, c := range m.cmds {
		if c.DeviceID == deviceID {
			out = append(out, c)
		}
	}
	return out, nil
}

type activeStub struct{ active map[string]bool }

func (a activeStub) IsActive(_ context.Context, id string) (bool, error) { return a.active[id], nil }

func ctlTestSetup() (*DeviceControlHandlers, *devicecontrol.Service, *memCtlStore) {
	store := newMemCtlStore()
	n := 0
	svc := devicecontrol.NewService(store, activeStub{map[string]bool{"dev-A": true}}, func() string {
		n++
		return "cmd-" + string(rune('0'+n))
	}, time.Minute, time.Now)
	return &DeviceControlHandlers{Svc: svc}, svc, store
}

func withDevice(r *http.Request, id string) *http.Request {
	dev := auth.Device{ID: id, Name: "d", Status: auth.StatusActive}
	ctx := context.WithValue(r.Context(), deviceKey, dev)
	return r.WithContext(ctx)
}

func TestControlPollEmpty(t *testing.T) {
	h, _, _ := ctlTestSetup()
	req := withDevice(httptest.NewRequest("POST", "/api/v1/device-control/poll", nil), "dev-A")
	rec := httptest.NewRecorder()
	h.Poll(rec, req)
	if rec.Code != 200 {
		t.Fatalf("poll: %d %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if _, has := body["command"]; has {
		t.Fatalf("no command expected: %s", rec.Body.String())
	}
	if body["server_time"] == nil {
		t.Fatalf("server time required: %s", rec.Body.String())
	}
}

func TestControlPollDeliversMinimal(t *testing.T) {
	h, svc, _ := ctlTestSetup()
	ctx := context.Background()
	if _, _, err := svc.CreateSyncRequest(ctx, "dev-A", "k1"); err != nil {
		t.Fatal(err)
	}
	req := withDevice(httptest.NewRequest("POST", "/api/v1/device-control/poll", nil), "dev-A")
	rec := httptest.NewRecorder()
	h.Poll(rec, req)
	var body struct {
		Command *struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			Version int    `json:"version"`
		} `json:"command"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Command == nil || body.Command.Type != "sync_now" || body.Command.Version != 1 {
		t.Fatalf("minimal command: %s", rec.Body.String())
	}
	var raw map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &raw)
	if s, _ := json.Marshal(raw["command"]); bytes.Contains(s, []byte("device")) || bytes.Contains(s, []byte("credential")) {
		t.Fatalf("no internals: %s", s)
	}
}

func TestControlWrongDevice404(t *testing.T) {
	h, svc, _ := ctlTestSetup()
	ctx := context.Background()
	created, _, err := svc.CreateSyncRequest(ctx, "dev-A", "k1")
	if err != nil {
		t.Fatal(err)
	}
	req := withDevice(httptest.NewRequest("POST", "/api/v1/device-control/commands/"+created.ID+"/accepted", nil), "dev-B")
	rec := httptest.NewRecorder()
	h.Accepted(rec, req)
	if rec.Code != 404 {
		t.Fatalf("cross-device must be 404: %d %s", rec.Code, rec.Body.String())
	}
}

func TestControlTerminalConflict409(t *testing.T) {
	h, svc, _ := ctlTestSetup()
	ctx := context.Background()
	created, _, _ := svc.CreateSyncRequest(ctx, "dev-A", "k1")
	if _, err := svc.ReportTerminal(ctx, "dev-A", created.ID, "completed", "SYNC_COMPLETED"); err != nil {
		t.Fatal(err)
	}
	body := bytes.NewReader([]byte(`{"status":"failed","result_code":"SYNC_FAILED_NETWORK"}`))
	req := withDevice(httptest.NewRequest("POST", "/api/v1/device-control/commands/"+created.ID+"/status", body), "dev-A")
	rec := httptest.NewRecorder()
	h.Status(rec, req)
	if rec.Code != 409 {
		t.Fatalf("contradictory terminal must be 409: %d %s", rec.Code, rec.Body.String())
	}
}

func TestControlInvalidTransition400(t *testing.T) {
	h, svc, _ := ctlTestSetup()
	ctx := context.Background()
	created, _, _ := svc.CreateSyncRequest(ctx, "dev-A", "k1")
	body := bytes.NewReader([]byte(`{"status":"teleport","result_code":"X"}`))
	req := withDevice(httptest.NewRequest("POST", "/api/v1/device-control/commands/"+created.ID+"/status", body), "dev-A")
	rec := httptest.NewRecorder()
	h.Status(rec, req)
	if rec.Code != 400 {
		t.Fatalf("unknown state must be 400: %d %s", rec.Code, rec.Body.String())
	}
}

func TestControlResultPrivacy(t *testing.T) {
	_, svc, _ := ctlTestSetup()
	ctx := context.Background()
	created, _, _ := svc.CreateSyncRequest(ctx, "dev-A", "k1")
	_, err := svc.ReportTerminal(ctx, "dev-A", created.ID, "completed", "customer 2010... secret hunter2 "+string(make([]byte, 5000)))
	if err == nil {
		t.Fatal("oversize/free-form result must be rejected")
	}
}
