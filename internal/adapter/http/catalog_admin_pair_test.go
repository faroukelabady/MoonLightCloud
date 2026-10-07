package http

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres"
	"github.com/faroukelabady/MoonLightCloud/internal/auth"
	"github.com/faroukelabady/MoonLightCloud/internal/catalogadmin"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/ids"
	"github.com/faroukelabady/MoonLightCloud/internal/testutil"
)

type pairDirectory struct {
	repo           postgres.Devices
	authentication auth.Service
}

func (d pairDirectory) BindingStore(ctx context.Context, id string) (string, error) {
	return d.repo.CatalogAdminBindingStore(ctx, id)
}
func (d pairDirectory) DeviceActive(ctx context.Context, id string) (bool, error) {
	device, e := d.authentication.Get(ctx, id)
	return device.Active(), e
}
func (d pairDirectory) DeviceName(context.Context, string) string { return "pair fixture" }

func TestRemediationExactPair(t *testing.T) {
	binary := os.Getenv("P16_PAIR_RETAIL_BINARY")
	if binary == "" {
		t.Skip("Retail test binary not supplied")
	}
	ctx := context.Background()
	p, e := pgxpool.New(ctx, testutil.Isolated(t))
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	repo := postgres.NewDevices(p, 5*time.Second)
	hasher, e := auth.NewHasher(make([]byte, 32))
	if e != nil {
		t.Fatal(e)
	}
	authentication := auth.NewService(repo, hasher, "", 1, clock.System{}, ids.System{})
	device, e := authentication.Create(ctx, "pair fixture")
	if e != nil {
		t.Fatal(e)
	}
	svc := catalogadmin.NewService(repo, pairDirectory{repo, authentication})
	h := CatalogAdminHandlers{Svc: svc, Log: slog.Default()}
	commands := make(chan catalogadmin.CommandView, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /remediation-fixture", func(w http.ResponseWriter, r *http.Request) {
		var f struct {
			Store    string          `json:"store"`
			Product  string          `json:"product"`
			Category string          `json:"category"`
			Revision int64           `json:"revision"`
			Payload  json.RawMessage `json:"payload"`
		}
		if e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&f); e != nil {
			http.Error(w, "fixture", 400)
			return
		}
		event := uuid.NewString()
		stmts := []struct {
			query string
			args  []any
		}{
			{`INSERT INTO stores(id,display_name,timezone) VALUES($1,'Pair','Africa/Cairo')`, []any{f.Store}},
			{`INSERT INTO device_store_bindings(device_id,store_id) VALUES($1,$2)`, []any{device.Device.ID, f.Store}},
			{`INSERT INTO sync_events(event_id,device_id,event_type,occurred_at,payload,payload_hash,store_id) VALUES($1,$2,'catalog.product.snapshot.v1',now(),'{}','\x00',$3)`, []any{event, device.Device.ID, f.Store}},
			{`INSERT INTO catalog_categories(category_id,status,name_ar,source_revision,source_event_id,source_device_id,source_payload_hash,source_received_at,store_id) VALUES($1,'active','Root',1,$2,$3,'\x00',now(),$4)`, []any{f.Category, event, device.Device.ID, f.Store}},
			{`INSERT INTO catalog_products(product_id,name,top_category_id,is_active,source_revision,source_event_id,source_device_id,source_payload_hash,source_received_at,store_id) VALUES($1,'Original',$2,true,$3,$4,$5,'\x00',now(),$6)`, []any{f.Product, f.Category, f.Revision, event, device.Device.ID, f.Store}},
		}
		for _, st := range stmts {
			if _, e := p.Exec(r.Context(), st.query, st.args...); e != nil {
				t.Error("pair fixture persistence", e)
				http.Error(w, "fixture", 500)
				return
			}
		}
		command, e := svc.Create(r.Context(), "pair fixture", f.Store, catalogadmin.TypeProductDetailsUpdateV1, f.Product, f.Revision, f.Payload)
		if e != nil {
			t.Error(e)
			http.Error(w, "fixture", 500)
			return
		}
		commands <- command
		w.WriteHeader(200)
	})
	secure := DeviceAuth(authentication)
	mux.Handle("GET /api/v1/device-control/catalog-commands", secure(http.HandlerFunc(h.PollCatalogCommands)))
	mux.Handle("POST /api/v1/device-control/capabilities", secure(http.HandlerFunc(h.ReportCapabilities)))
	var reports atomic.Int32
	mux.Handle("POST /api/v1/device-control/catalog-commands/{target_id}/result", secure(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorded := httptest.NewRecorder()
		h.ReportCatalogResult(recorded, r)
		if recorded.Code == 200 && reports.Add(1) == 1 {
			http.Error(w, "injected response failure", 503)
			return
		}
		for key, values := range recorded.Header() {
			w.Header()[key] = values
		}
		w.WriteHeader(recorded.Code)
		_, _ = w.Write(recorded.Body.Bytes())
	})))
	server := httptest.NewServer(mux)
	defer server.Close()
	child := exec.Command(binary, "-test.run=^TestRemediationHTTPPair$", "-test.v")
	child.Env = append(os.Environ(), "P16_PAIR_URL="+server.URL, "P16_PAIR_CREDENTIAL="+device.Device.ID+"."+device.Credential.ID+"."+device.RawSecret)
	output, e := child.CombinedOutput()
	if e != nil {
		t.Fatalf("Retail pair process failed: %s", output)
	}
	command := <-commands
	got, e := svc.Get(ctx, command.StoreID, command.ID)
	if e != nil || got.Aggregate != catalogadmin.AggregateApplied || got.Converged || reports.Load() != 2 || len(got.Targets) != 1 {
		t.Fatal(got, e, reports.Load())
	}
	t.Log("Authenticated HTTP pair: one command/target, one SQLite mutation, failed terminal response, exact receipt/ACK replay, APPLIED awaiting ordinary projection sync")
}
