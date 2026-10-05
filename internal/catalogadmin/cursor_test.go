package catalogadmin

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestHistoryCursorRoundTrip proves the opaque history cursor carries
// the exact row position and rejects everything else fail-closed.
func TestHistoryCursorRoundTrip(t *testing.T) {
	ts := time.Date(2026, 9, 1, 12, 0, 0, 123456789, time.UTC)
	id := uuid.NewString()
	encoded := encodeHistoryCursor(ts, id)
	if strings.Contains(encoded, id) || strings.Contains(encoded, "2026") {
		t.Fatal("cursor must be opaque")
	}
	gotTS, gotID, err := decodeHistoryCursor(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !gotTS.Equal(ts) || gotID != id {
		t.Fatalf("round trip: %v %q", gotTS, gotID)
	}
	if _, _, err := decodeHistoryCursor(""); err != nil {
		t.Fatalf("empty means start: %v", err)
	}
	for _, bad := range []string{"!!!", "bm90LWFjdXJzb3I", "e30=", "dGVzdA==", encodeHistoryCursor(time.Time{}, id)} {
		if _, _, err := decodeHistoryCursor(bad); err == nil {
			t.Fatalf("malformed cursor accepted: %q", bad)
		}
	}
	// Zero timestamp never decodes to a usable position.
	if _, _, err := decodeHistoryCursor(encodeHistoryCursor(time.Time{}, uuid.NewString())); err == nil {
		t.Fatal("zero-time cursor must be rejected")
	}
}

// cursorListStore is a minimal Store stub serving canned history rows.
type cursorListStore struct {
	Store
	rows []CommandView
}

func (s *cursorListStore) ListCatalogAdminCommands(_ context.Context, _ string, _, _, _ string, _ int, _ time.Time, _ string) ([]CommandView, error) {
	return s.rows, nil
}

func (s *cursorListStore) ListCatalogAdminTargetsBatch(_ context.Context, _ []string) (map[string][]TargetView, error) {
	return map[string][]TargetView{}, nil
}

// TestServiceListReturnsNextCursor proves next_cursor appears exactly
// when a full page suggests more history, and decodes to the last row.
func TestServiceListReturnsNextCursor(t *testing.T) {
	mk := func(id string, ts time.Time) CommandView {
		return CommandView{ID: id, StoreID: uuid.NewString(), CreatedAt: ts, UpdatedAt: ts}
	}
	ts := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	full := []CommandView{mk(uuid.NewString(), ts), mk(uuid.NewString(), ts.Add(-time.Second))}
	svc := NewService(&cursorListStore{rows: full}, nil)
	views, next, err := svc.List(context.Background(), uuid.NewString(), "", "", "", 2, "")
	if err != nil || len(views) != 2 || next == "" {
		t.Fatalf("full page must carry next: %+v %q %v", views, next, err)
	}
	gotTS, gotID, err := decodeHistoryCursor(next)
	if err != nil || !gotTS.Equal(full[1].CreatedAt) || gotID != full[1].ID {
		t.Fatalf("next points at last row: %v %q %v", gotTS, gotID, err)
	}
	short := []CommandView{mk(uuid.NewString(), ts)}
	svc = NewService(&cursorListStore{rows: short}, nil)
	if _, next, err := svc.List(context.Background(), uuid.NewString(), "", "", "", 2, ""); err != nil || next != "" {
		t.Fatalf("short page must end history: %q %v", next, err)
	}
	if _, _, err := svc.List(context.Background(), uuid.NewString(), "", "", "", 2, "bogus"); err == nil {
		t.Fatal("malformed cursor must fail")
	}
}

// cursorProductStore serves canned Product rows for scope tests.
type cursorProductStore struct {
	Store
	rows []AdminProductRow
	got  string
}

func (s *cursorProductStore) AdminProductList(_ context.Context, _, _, cursor string, _ int) ([]AdminProductRow, error) {
	s.got = cursor
	return s.rows, nil
}

// TestProductCursorScopeBinding proves cursors are bound to the exact
// Store+search scope: cross-scope reuse fails closed instead of
// silently continuing another scope's list.
func TestProductCursorScopeBinding(t *testing.T) {
	storeA, storeB := uuid.NewString(), uuid.NewString()
	row := AdminProductRow{ProductID: uuid.NewString()}
	stub := &cursorProductStore{rows: []AdminProductRow{row}}
	svc := NewService(stub, nil)
	_, next, err := svc.AdminProducts(context.Background(), storeA, "pap", "", 1)
	if err != nil || next == "" {
		t.Fatalf("first page: %q %v", next, err)
	}
	if strings.Contains(next, row.ProductID) || strings.Contains(next, storeA) {
		t.Fatal("cursor must be opaque")
	}
	// Same scope continues: bare Product ID reaches the store.
	if _, _, err := svc.AdminProducts(context.Background(), storeA, "pap", next, 1); err != nil {
		t.Fatalf("same scope: %v", err)
	}
	if stub.got != row.ProductID {
		t.Fatalf("store received %q, want bare Product ID", stub.got)
	}
	// Cross-Store reuse rejected.
	if _, _, err := svc.AdminProducts(context.Background(), storeB, "pap", next, 1); err == nil {
		t.Fatal("cross-Store cursor reuse must fail")
	}
	// Different search rejected.
	if _, _, err := svc.AdminProducts(context.Background(), storeA, "other", next, 1); err == nil {
		t.Fatal("cross-search cursor reuse must fail")
	}
	// Malformed rejected.
	for _, bad := range []string{"!!!", row.ProductID, "e30="} {
		if _, _, err := svc.AdminProducts(context.Background(), storeA, "pap", bad, 1); err == nil {
			t.Fatalf("malformed cursor accepted: %q", bad)
		}
	}
}
