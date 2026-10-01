package http

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres"
	"github.com/faroukelabady/MoonLightCloud/internal/auth"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/ids"
	isync "github.com/faroukelabady/MoonLightCloud/internal/sync"
	"github.com/faroukelabady/MoonLightCloud/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Exercise the actual authenticated routes and durable enrollment on PostgreSQL.
func TestStoreEnrollmentAuthenticatedPostgres(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testutil.Isolated(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	hasher, err := auth.NewHasher([]byte(strings.Repeat("t", 32)))
	if err != nil {
		t.Fatal(err)
	}
	repo := postgres.NewDevices(pool, 5*time.Second)
	authSvc := auth.NewService(repo, hasher, "", 1, clock.System{}, ids.System{})
	tokens := make([]string, 4)
	deviceIDs := make([]string, 4)
	for i := range tokens {
		p, err := authSvc.Create(ctx, fmt.Sprintf("store-device-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		deviceIDs[i] = p.Device.ID
		tokens[i] = p.Device.ID + "." + p.Credential.ID + "." + p.RawSecret
	}
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/sync/store-registration", DeviceAuth(authSvc)(StoreRegistration(repo)))
	mux.Handle("POST /api/v1/sync/batch", DeviceAuth(authSvc)(SyncBatch(isync.NewService(repo, clock.System{}), nil)))
	server := httptest.NewServer(mux)
	defer server.Close()
	post := func(token, path, body string) int {
		req, err := http.NewRequest("POST", server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Error(err)
			return 0
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		response, err := server.Client().Do(req)
		if err != nil {
			t.Error(err)
			return 0
		}
		defer response.Body.Close()
		return response.StatusCode
	}
	storeA := "aaaaaaaa-0000-4000-8000-000000000001"
	storeB := "bbbbbbbb-0000-4000-8000-000000000002"
	request := func(id, name string) string {
		return fmt.Sprintf(`{"store_id":%q,"display_name":%q,"timezone":"Africa/Cairo"}`, id, name)
	}
	var bootstrap sync.WaitGroup
	bootstrapCodes := make([]int, 2)
	for i, storeID := range []string{storeA, storeB} {
		bootstrap.Add(1)
		go func(i int, storeID string) {
			defer bootstrap.Done()
			bootstrapCodes[i] = post(tokens[i], "/api/v1/sync/store-registration", request(storeID, string(rune('A'+i))))
		}(i, storeID)
	}
	bootstrap.Wait()
	for _, code := range bootstrapCodes {
		if code != 200 {
			t.Fatalf("concurrent A/B bootstrap: %d", code)
		}
	}
	var wg sync.WaitGroup
	codes := make([]int, 8)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = post(tokens[2], "/api/v1/sync/store-registration", request(storeA, "hijack"))
		}(i)
	}
	wg.Wait()
	for _, code := range codes {
		if code != 409 {
			t.Fatalf("unauthorized existing Store join: %d", code)
		}
	}
	var count int
	var name string
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM device_store_bindings WHERE device_id=$1`, deviceIDs[2]).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unauthorized binding: %d %v", count, err)
	}
	if err := pool.QueryRow(ctx, `SELECT display_name FROM stores WHERE id=$1`, storeA).Scan(&name); err != nil || name != "A" {
		t.Fatalf("metadata mutation: %q %v", name, err)
	}
	if err := repo.EnrollStore(ctx, deviceIDs[3], strings.ToUpper(storeA)); err != nil {
		t.Fatal(err)
	}
	if err := repo.EnrollStore(ctx, deviceIDs[3], storeA); err != nil {
		t.Fatal(err)
	}
	if err := repo.EnrollStore(ctx, deviceIDs[3], storeB); err == nil {
		t.Fatal("operator rebind accepted")
	}
	if code := post(tokens[3], "/api/v1/sync/store-registration", request(strings.ToUpper(storeA), "متجر A")); code != 200 {
		t.Fatalf("enrolled registration: %d", code)
	}
	for _, i := range []int{0, 1, 2, 3} {
		eventID := fmt.Sprintf("99999999-9999-7999-8999-%012d", i)
		body := fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":"system.test.v1","occurred_at":"2026-09-19T10:20:30Z","payload":{"message":"test"}}]}`, eventID)
		if code := post(tokens[i], "/api/v1/sync/batch", body); code != 200 {
			t.Fatalf("ingress: %d", code)
		}
		var actual *string
		if err := pool.QueryRow(ctx, `SELECT store_id::text FROM sync_events WHERE event_id=$1`, eventID).Scan(&actual); err != nil {
			t.Fatal(err)
		}
		if i == 2 {
			if actual != nil {
				t.Fatal("unauthorized device acquired Store context")
			}
		} else {
			expected := storeA
			if i == 1 {
				expected = storeB
			}
			if actual == nil || *actual != expected {
				t.Fatal("device ingress context crossed Store boundary")
			}
		}
	}
	if err := authSvc.RevokeDevice(ctx, deviceIDs[3]); err != nil {
		t.Fatal(err)
	}
	if code := post(tokens[3], "/api/v1/sync/store-registration", request(storeA, "revoked")); code != 401 {
		t.Fatalf("revoked registration: %d", code)
	}
	if err := repo.EnrollStore(ctx, deviceIDs[3], storeA); err == nil {
		t.Fatal("revoked operator enrollment accepted")
	}
	if err := repo.EnrollStore(ctx, "11111111-1111-4111-8111-111111111111", storeA); err == nil {
		t.Fatal("unknown device enrolled")
	}
}
