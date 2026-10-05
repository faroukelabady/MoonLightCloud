package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/faroukelabady/MoonLightCloud/internal/catalogadmin"
)

// TestCatalogAdminHistoryKeysetStable proves keyset pagination is
// stable under concurrent inserts: page through history, insert new
// commands between pages, and observe neither duplicates nor skips.
// Skips without TEST_DATABASE_URL.
func TestCatalogAdminHistoryKeysetStable(t *testing.T) {
	pool, _ := openTestRepo(t)
	ctx := context.Background()
	store := NewDevices(pool, 5*time.Second)
	storeID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO stores (id, display_name, timezone) VALUES ($1, 'K', 'Africa/Cairo')`, storeID); err != nil {
		t.Fatal(err)
	}
	mkCommand := func() string {
		id := uuid.NewString()
		_, err := store.CreateCatalogAdminCommand(ctx, catalogadmin.NewCommand{
			ID: id, StoreID: storeID, Type: catalogadmin.TypeProductDetailsUpdateV1,
			Version: 1, EntityID: uuid.NewString(), Payload: []byte(`{}`),
			PayloadHash:      "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			ExpectedRevision: 0, Actor: "keyset-test",
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	var zero time.Time
	// Seed 5 commands.
	for i := 0; i < 5; i++ {
		mkCommand()
	}
	// Page 1: newest 2.
	page1, err := store.ListCatalogAdminCommands(ctx, storeID, "", "", "", 2, zero, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page1) != 2 {
		t.Fatalf("page1: %d", len(page1))
	}
	// Concurrent inserts land newer than everything returned so far.
	for i := 0; i < 2; i++ {
		mkCommand()
	}
	// Page 2 continues strictly after page 1's last row.
	last := page1[len(page1)-1]
	page2, err := store.ListCatalogAdminCommands(ctx, storeID, "", "", "", 2, last.CreatedAt, last.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 2 {
		t.Fatalf("page2: %d", len(page2))
	}
	// Page 3: remainder (5 seed - 4 returned = 1, plus nothing else:
	// the 2 concurrent inserts are NEWER than page 1, correctly absent).
	last = page2[len(page2)-1]
	page3, err := store.ListCatalogAdminCommands(ctx, storeID, "", "", "", 2, last.CreatedAt, last.ID)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, view := range append(append(page1, page2...), page3...) {
		if seen[view.ID] {
			t.Fatalf("duplicate %s across keyset pages", view.ID)
		}
		seen[view.ID] = true
	}
	if len(seen) != 5 {
		t.Fatalf("keyset coverage: %d, want exactly the 5 seed rows", len(seen))
	}
	// Full newest-first ordering across pages.
	all := append(append(page1, page2...), page3...)
	for i := 1; i < len(all); i++ {
		a, b := all[i-1].CreatedAt, all[i].CreatedAt
		if a.Before(b) || (a.Equal(b) && all[i-1].ID < all[i].ID) {
			t.Fatalf("ordering violated at %d", i)
		}
	}
}
