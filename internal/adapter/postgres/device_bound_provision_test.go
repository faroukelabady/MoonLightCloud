package postgres

import (
	"context"
	"github.com/google/uuid"
	"testing"
)

func TestBoundProvisionRefusesMissingOrDisabledStoreWithoutOrphan(t *testing.T) {
	pool, svc := openTestRepo(t)
	ctx := context.Background()
	for _, disabled := range []bool{false, true} {
		id := uuid.NewString()
		if disabled {
			if _, err := pool.Exec(ctx, `INSERT INTO stores(id,display_name,timezone,status) VALUES($1,'Disabled','Africa/Cairo','disabled')`, id); err != nil {
				t.Fatal(err)
			}
		}
		_, err := svc.CreateForStore(ctx, "Test device", id)
		if err == nil {
			t.Fatal("invalid target store accepted")
		}
		var devices, credentials, bindings int
		if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM devices),(SELECT count(*) FROM device_credentials),(SELECT count(*) FROM device_store_bindings)`).Scan(&devices, &credentials, &bindings); err != nil {
			t.Fatal(err)
		}
		if devices != 0 || credentials != 0 || bindings != 0 {
			t.Fatalf("partial provisioning: %d %d %d", devices, credentials, bindings)
		}
	}
}
