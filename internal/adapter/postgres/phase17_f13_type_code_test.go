package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/sale"
	"github.com/google/uuid"
)

func f13TypeCodePayload(t *testing.T, length int, withVariant bool) string {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(v3Fixture(t, uuid.NewString(), "TYPE-CODE-BOUND")), &payload); err != nil {
		t.Fatal(err)
	}
	for _, entry := range payload["lines"].([]any) {
		line := entry.(map[string]any)
		if !withVariant {
			for _, key := range []string{"variant_id", "variant_sku", "variant_attributes", "variant_price_egp_cents", "variant_price_usd_cents"} {
				delete(line, key)
			}
		}
		line["product_type_id"] = "10000000-0000-4000-8000-000000000001"
		line["product_type_code"] = strings.Repeat("a", length)
		line["product_type_name_ar"], line["product_type_name_en"] = "نوع", "Type"
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestSaleV3ProductTypeCodeIngressBounds(t *testing.T) {
	for _, withVariant := range []bool{true, false} {
		for _, length := range []int{1, 32, 33, 64} {
			t.Run(fmt.Sprintf("variant=%v/length=%d", withVariant, length), func(t *testing.T) {
				env := openSaleEnv(t)
				ctx := context.Background()
				eventID := uuid.NewString()
				body := fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":"sale.finalized.v3","occurred_at":"2026-09-20T10:00:00Z","payload":%s}]}`, eventID, f13TypeCodePayload(t, length, withVariant))
				result, err := env.syncSvc.Ingest(ctx, env.devID, env.credID, []byte(body))
				if length > 32 {
					if err == nil || kindOf(err) != apperr.Unprocessable {
						t.Fatalf("oversized Type code not rejected by validation: %+v %v", result, err)
					}
					for _, table := range []string{"sync_events", "sale_event_ownership", "sales_projection", "sale_lines_projection", "sale_item_tag_snapshots"} {
						if count := saleCount(t, env.pool, table); count != 0 {
							t.Fatalf("%s has rejected-event state: %d", table, count)
						}
					}
					return
				}
				if err != nil || len(result.Events) != 1 || result.Events[0].Status != "accepted" {
					t.Fatalf("supported Type code rejected: %+v %v", result, err)
				}
				store := NewDevices(env.pool, 5*time.Second)
				record, found, err := store.LoadSaleEvent(ctx, eventID)
				if err != nil || !found {
					t.Fatalf("load valid event: %v %v", found, err)
				}
				projected, err := store.ProjectSaleV3(ctx, record, time.Now())
				if err != nil || projected.Outcome != sale.OutcomeProcessed {
					t.Fatalf("supported Type code cannot project: %+v %v", projected, err)
				}
				var code string
				if err := env.pool.QueryRow(ctx, `SELECT product_type_code FROM sale_lines_projection`).Scan(&code); err != nil {
					t.Fatal(err)
				}
				if code != strings.Repeat("a", length) {
					t.Fatal("historical code changed")
				}
			})
		}
	}
}

func TestSaleV3PreviouslyAcceptedOversizedTypeCodeBlocks(t *testing.T) {
	for _, withVariant := range []bool{true, false} {
		for _, length := range []int{33, 64} {
			t.Run(fmt.Sprintf("variant=%v/length=%d", withVariant, length), func(t *testing.T) {
				env := openSaleEnv(t)
				ctx := context.Background()
				eventID := uuid.NewString()
				payload := f13TypeCodePayload(t, length, withVariant)
				hash := sha256.Sum256([]byte(payload))
				if _, err := env.pool.Exec(ctx, `INSERT INTO sync_events (event_id,device_id,event_type,occurred_at,payload,payload_hash) VALUES ($1,$2,'sale.finalized.v3',now(),$3,$4)`, eventID, env.devID, payload, hash[:]); err != nil {
					t.Fatal(err)
				}
				var beforePayload string
				if err := env.pool.QueryRow(ctx, `SELECT payload::text FROM sync_events WHERE event_id=$1`, eventID).Scan(&beforePayload); err != nil {
					t.Fatal(err)
				}
				store := NewDevices(env.pool, 5*time.Second)
				record, found, err := store.LoadSaleEvent(ctx, eventID)
				if err != nil || !found {
					t.Fatalf("load persisted event: %v %v", found, err)
				}
				for attempt := 0; attempt < 2; attempt++ {
					result, err := store.ProjectSaleV3(ctx, record, time.Now())
					if err != nil || result.Outcome != sale.OutcomeBlocked || result.ErrorCode != ErrValidation {
						t.Fatalf("malformed history must block, not retry: %+v %v", result, err)
					}
				}
				var status, code, afterPayload string
				var afterHash []byte
				if err := env.pool.QueryRow(ctx, `SELECT status,last_error_code FROM sync_event_processing WHERE event_id=$1 AND processor=$2`, eventID, sale.ProcessorSaleProjectionV3).Scan(&status, &code); err != nil {
					t.Fatal(err)
				}
				if status != sale.ProcBlocked || code != ErrValidation {
					t.Fatalf("nonterminal validation state: %q %q", status, code)
				}
				if err := env.pool.QueryRow(ctx, `SELECT payload::text,payload_hash FROM sync_events WHERE event_id=$1`, eventID).Scan(&afterPayload, &afterHash); err != nil {
					t.Fatal(err)
				}
				if beforePayload != afterPayload || !bytes.Equal(hash[:], afterHash) {
					t.Fatal("immutable accepted payload/hash changed")
				}
				for _, table := range []string{"sale_event_ownership", "sales_projection", "sale_lines_projection", "sale_item_tag_snapshots"} {
					if count := saleCount(t, env.pool, table); count != 0 {
						t.Fatalf("%s has partial rejected-history state: %d", table, count)
					}
				}
			})
		}
	}
}
