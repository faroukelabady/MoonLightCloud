package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestAdminVariantMoneyRetainsNullZeroAndExactStrings(t *testing.T) {
	present := func(value int64) pgtype.Int8 { return pgtype.Int8{Int64: value, Valid: true} }
	for _, tc := range []struct {
		name                   string
		value, base            pgtype.Int8
		wantRaw, wantEffective *string
	}{
		{name: "unknown"},
		{name: "inherit", base: present(100), wantEffective: adminCostText("100")},
		{name: "zero overrides base", value: present(0), base: present(100), wantRaw: adminCostText("0"), wantEffective: adminCostText("0")},
		{name: "large exact override", value: present(9007199254740993), base: present(100), wantRaw: adminCostText("9007199254740993"), wantEffective: adminCostText("9007199254740993")},
		{name: "large exact inherited", base: present(9007199254740993), wantEffective: adminCostText("9007199254740993")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := adminVariantToDomain(adminVariantRow{PriceEgpCents: tc.value, PriceUsdCents: tc.value, CostEgpCents: tc.value, BasePriceEgpCents: tc.base, BasePriceUsdCents: tc.base, BaseCostEgpCents: tc.base})
			actual := []*string{got.PriceEGPMinor, got.PriceUSDMinor, got.CostEGPMinor, got.EffectivePriceEGPMinor, got.EffectivePriceUSDMinor, got.EffectiveCostEGPMinor}
			want := []*string{tc.wantRaw, tc.wantRaw, tc.wantRaw, tc.wantEffective, tc.wantEffective, tc.wantEffective}
			if !reflect.DeepEqual(actual, want) {
				a, _ := json.Marshal(actual)
				b, _ := json.Marshal(want)
				t.Fatalf("money %s want %s", a, b)
			}
			raw, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			expected := `"effective_cost_egp_minor":null`
			if tc.wantEffective != nil {
				expected = `"effective_cost_egp_minor":"` + *tc.wantEffective + `"`
			}
			if !strings.Contains(string(raw), expected) {
				t.Fatalf("money wire value changed: %s", raw)
			}
		})
	}
}

func TestAdminVariantEffectiveMoneyUsesStoreScopedBaseAndPreservesOverrides(t *testing.T) {
	r := remediationCloudSetup(t)
	ctx := context.Background()
	var baseID string
	if err := r.p.QueryRow(ctx, `SELECT variant_id FROM catalog_product_variants WHERE product_id=$1`, r.pid).Scan(&baseID); err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := r.p.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO catalog_product_prices(product_id,currency,price_minor,cost_minor) VALUES($1,'EGP',60000,30000),($1,'USD',3000,NULL)`, r.pid)
	customID, zeroID := uuid.NewString(), uuid.NewString()
	for index, row := range []struct{ id, sku, key string }{{customID, "CUSTOM-COST", "color=gold"}, {zeroID, "ZERO-COST", "color=silver"}} {
		exec(`INSERT INTO catalog_product_variants(variant_id,product_id,sku,is_active,deleted,position,combination_key,variant_revision,catalog_revision,source_event_id,source_device_id,source_payload_hash,source_received_at,store_id)
   SELECT $1,product_id,$2,true,false,$3,$4,variant_revision,catalog_revision,source_event_id,source_device_id,source_payload_hash,source_received_at,store_id FROM catalog_product_variants WHERE variant_id=$5`, row.id, row.sku, index+1, row.key, baseID)
	}
	exec(`INSERT INTO catalog_product_variant_prices(variant_id,currency,price_minor,cost_minor) VALUES($1,'EGP',75000,40000),($1,'USD',3500,NULL),($2,'EGP',0,0),($2,'USD',0,NULL)`, customID, zeroID)
	assertRead := func(id string, want []*string) {
		t.Helper()
		one, err := r.d.AdminProductVariant(ctx, r.sid, id)
		if err != nil {
			t.Fatal(err)
		}
		got := []*string{one.PriceEGPMinor, one.PriceUSDMinor, one.CostEGPMinor, one.EffectivePriceEGPMinor, one.EffectivePriceUSDMinor, one.EffectiveCostEGPMinor}
		if !reflect.DeepEqual(got, want) {
			a, _ := json.Marshal(got)
			b, _ := json.Marshal(want)
			t.Fatalf("variant %s: got=%s want=%s", id, a, b)
		}
		list, err := r.d.AdminProductVariants(ctx, r.sid, r.pid)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range list {
			if v.VariantID == id {
				if !reflect.DeepEqual(v, one) {
					t.Fatalf("list/detail differ: %+v / %+v", v, one)
				}
				return
			}
		}
		t.Fatalf("variant %s missing from list", id)
	}
	assertRead(baseID, []*string{nil, nil, nil, adminCostText("60000"), adminCostText("3000"), adminCostText("30000")})
	assertRead(customID, []*string{adminCostText("75000"), adminCostText("3500"), adminCostText("40000"), adminCostText("75000"), adminCostText("3500"), adminCostText("40000")})
	assertRead(zeroID, []*string{adminCostText("0"), adminCostText("0"), adminCostText("0"), adminCostText("0"), adminCostText("0"), adminCostText("0")})
	// Missing base facts stay unknown and never become fabricated zero.
	exec(`DELETE FROM catalog_product_prices WHERE product_id=$1`, r.pid)
	assertRead(baseID, []*string{nil, nil, nil, nil, nil, nil})
	assertRead(zeroID, []*string{adminCostText("0"), adminCostText("0"), adminCostText("0"), adminCostText("0"), adminCostText("0"), adminCostText("0")})
	foreignStore := uuid.NewString()
	list, err := r.d.AdminProductVariants(ctx, foreignStore, r.pid)
	if err != nil || len(list) != 0 {
		t.Fatalf("foreign list exposed rows: %+v %v", list, err)
	}
	_, err = r.d.AdminProductVariant(ctx, foreignStore, baseID)
	var classified *apperr.Error
	if !errors.As(err, &classified) || classified.Kind != apperr.NotFound {
		t.Fatalf("foreign detail must be not found: %v", err)
	}
	// Values cannot inherit from a parent projected under another Store.
	exec(`INSERT INTO catalog_product_prices(product_id,currency,price_minor,cost_minor) VALUES($1,'EGP',60000,30000)`, r.pid)
	exec(`INSERT INTO stores(id,display_name,timezone) VALUES($1,'Foreign','Africa/Cairo')`, foreignStore)
	exec(`UPDATE catalog_products SET store_id=$1 WHERE product_id=$2`, foreignStore, r.pid)
	assertRead(baseID, []*string{nil, nil, nil, nil, nil, nil})
}
func adminCostText(value string) *string { return &value }
