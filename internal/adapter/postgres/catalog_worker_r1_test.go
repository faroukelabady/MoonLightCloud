package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestCatalogRetryContractMatrix(t *testing.T) {
	exact := catalog.ProjectResult{Outcome: catalog.OutcomeRetryable, ErrorCode: ErrProjection}
	for _, tc := range []struct {
		name   string
		result catalog.ProjectResult
		err    error
		want   bool
	}{
		{"raw serialization", catalog.ProjectResult{}, &pgconn.PgError{Code: "40001"}, true},
		{"wrapped serialization", catalog.ProjectResult{}, fmt.Errorf("outer: %w", &pgconn.PgError{Code: "40001"}), true},
		{"raw deadlock", catalog.ProjectResult{}, &pgconn.PgError{Code: "40P01"}, true},
		{"wrapped deadlock", catalog.ProjectResult{}, fmt.Errorf("outer: %w", &pgconn.PgError{Code: "40P01"}), true},
		{"zero arbitrary", catalog.ProjectResult{}, errors.New("genuine"), false},
		{"retry arbitrary", exact, errors.New("genuine"), false},
		{"retry unique", exact, &pgconn.PgError{Code: "23505"}, false},
		{"loader undefined column", exact, &pgconn.PgError{Code: "42703"}, false},
		{"other transaction state", exact, &pgconn.PgError{Code: "40002"}, false},
		{"broad unavailable", exact, apperr.New(apperr.Unavailable, "transaction unavailable"), false},
		{"exact durable pair", exact, transient(errors.New("catalog projection transient failure")), true},
		{"wrong code", catalog.ProjectResult{Outcome: catalog.OutcomeRetryable, ErrorCode: "OTHER"}, transient(errors.New("catalog projection transient failure")), false},
		{"no-error retry", exact, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := catalogAttemptRetryable(tc.result, tc.err); got != tc.want {
				t.Fatalf("retry=%v want=%v", got, tc.want)
			}
		})
	}
}
func TestCatalogWorkerFiniteBudgetAndFailFast(t *testing.T) {
	for _, state := range []string{"42703", "40001", "40P01"} {
		count := 0
		result := runCatalogAttempts("event-proof", "tag", catalogRetryBudget, func() catalogWorkerResult { count++; return catalogWorkerResult{Err: &pgconn.PgError{Code: state}} })
		want := 1
		if state != "42703" {
			want = 30
		}
		if count != want || result.Attempts != want || result.Err == nil {
			t.Fatalf("%s count=%d result=%+v", state, count, result)
		}
		for _, expected := range []string{"event-proof", "tag", fmt.Sprint(want), state} {
			if !strings.Contains(result.Err.Error(), expected) {
				t.Fatalf("missing diagnostic %q: %v", expected, result.Err)
			}
		}
	}
	count := 0
	result := runCatalogAttempts("waiting", "category", 30, func() catalogWorkerResult {
		count++
		if count == 1 {
			return catalogWorkerResult{Result: catalog.ProjectResult{Outcome: catalog.OutcomeNotDue}}
		}
		return catalogWorkerResult{Result: catalog.ProjectResult{Outcome: catalog.OutcomeProcessed}}
	})
	if result.Err != nil || result.Attempts != 2 {
		t.Fatal("legitimate no-error backoff wait", result)
	}
}
func TestCatalogWorkerGenuineLoaderFailureOneAttempt(t *testing.T) {
	f := openScopeFixture(t)
	if _, err := f.pool.Exec(context.Background(), `ALTER TABLE sync_events RENAME COLUMN payload TO deliberately_unavailable_payload`); err != nil {
		t.Fatal(err)
	}
	done := make(chan catalogWorkerResult, 1)
	go func() {
		done <- runCatalogWorker(NewDevices(f.pool, 5*time.Second), r2EventID(12345), catalog.EventTagSnapshotV1, 30)
	}()
	result := <-done
	if result.Err == nil || result.Attempts != 1 || !strings.Contains(result.Err.Error(), "42703") {
		t.Fatalf("genuine loader failure did not reach parent immediately: %+v", result)
	}
}

func TestCatalogWorkerRepeatedSerializationBudget(t *testing.T) {
	f := openScopeFixture(t)
	ctx := context.Background()
	id := r2EventID(12345)
	f.ingest(t, f.devA, f.credA, id, catalog.EventTagSnapshotV1, tagPayload(sharedTagGold, "gold", true, map[string]string{"ar": "ذهب", "en": "Gold"}, 1))
	before := r4Payloads(t, f)
	if _, err := f.pool.Exec(ctx, `CREATE SEQUENCE r1_attempts; CREATE FUNCTION r1_abort() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM nextval('r1_attempts'); RAISE EXCEPTION 'test serialization abort' USING ERRCODE='40001'; END $$; CREATE TRIGGER r1_abort BEFORE INSERT ON catalog_tags FOR EACH ROW EXECUTE FUNCTION r1_abort()`); err != nil {
		t.Fatal(err)
	}
	result := runCatalogWorker(NewDevices(f.pool, 5*time.Second), id, catalog.EventTagSnapshotV1, catalogRetryBudget)
	var count int64
	if err := f.pool.QueryRow(ctx, "SELECT last_value FROM r1_attempts").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if result.Err == nil || result.Attempts != 30 || count != 30 {
		t.Fatalf("serialization budget result=%+v actual DB attempts=%d", result, count)
	}
	if !strings.Contains(result.Err.Error(), id) || !strings.Contains(result.Err.Error(), catalog.EventTagSnapshotV1) || !strings.Contains(result.Err.Error(), "30") {
		t.Fatal("missing exhaustion context", result.Err)
	}
	if before != r4Payloads(t, f) {
		t.Fatal("retry changed durable payload/hash")
	}
}
