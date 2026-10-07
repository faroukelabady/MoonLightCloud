package postgres

// Phase 17-R3 §13 single-authoritative-target proof (real PostgreSQL): a
// create-kind command snapshots exactly ONE device target (deterministic),
// while entity-kind commands keep multi-device fan-out. Duplicate delivery
// of one create intent can therefore never mint divergent entities across
// a Store's devices; the code-uniqueness fence stays the last defense for
// genuinely distinct commands racing on one code.

import (
	"context"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/catalogadmin"
	"github.com/google/uuid"
)

func TestCreateCommandSingleAuthoritativeTarget(t *testing.T) {
	pool, authSvc := openTestRepo(t)
	ctx := context.Background()
	d := NewDevices(pool, 5*time.Second)

	storeID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO stores(id,display_name,timezone) VALUES($1,'Single','Africa/Cairo')`, storeID); err != nil {
		t.Fatal(err)
	}
	devIDs := []string{}
	for i := 0; i < 2; i++ {
		v, err := authSvc.Create(ctx, "single-dev")
		if err != nil {
			t.Fatal(err)
		}
		devIDs = append(devIDs, v.Device.ID)
		if _, err := pool.Exec(ctx, `INSERT INTO device_store_bindings(device_id,store_id) VALUES($1,$2)`, v.Device.ID, storeID); err != nil {
			t.Fatal(err)
		}
	}

	newCmd := func(kind, entity, key string) catalogadmin.NewCommand {
		return catalogadmin.NewCommand{
			ID: uuid.NewString(), StoreID: storeID, Type: catalogadmin.TypeProductTypeCreateV1,
			Version: 1, EntityID: entity, TargetKind: kind, RequestedKey: key,
			Payload: []byte(`{"code":"solo"}`), PayloadHash: "0000000000000000000000000000000000000000000000000000000000000000",
			ExpectedRevision: 0, Actor: "op",
		}
	}

	created, err := d.CreateCatalogAdminCommandWithTargets(ctx, newCmd(catalogadmin.TargetKindCreate, "", "solo"))
	if err != nil {
		t.Fatal(err)
	}
	targets, err := d.ListCatalogAdminTargets(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 {
		t.Fatalf("create must snapshot exactly one authoritative target, got %d", len(targets))
	}

	// Entity-kind keeps fan-out (idempotent updates need every device).
	updated, err := d.CreateCatalogAdminCommandWithTargets(ctx, catalogadmin.NewCommand{
		ID: uuid.NewString(), StoreID: storeID, Type: catalogadmin.TypeProductDetailsUpdateV1,
		Version: 1, EntityID: uuid.NewString(), TargetKind: catalogadmin.TargetKindEntity,
		Payload: []byte(`{}`), PayloadHash: "0000000000000000000000000000000000000000000000000000000000000000",
		ExpectedRevision: 1, Actor: "op",
	})
	if err != nil {
		t.Fatal(err)
	}
	targets, err = d.ListCatalogAdminTargets(ctx, updated.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 {
		t.Fatalf("entity commands must fan out to all bound devices, got %d", len(targets))
	}
}
