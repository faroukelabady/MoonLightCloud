package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/faroukelabady/MoonLightCloud/internal/migrate"
	"strings"
	"testing"
)

func TestR3AsyncReceiptMigrationPreservation(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	root, sub, tag := catalogProductFixture(t, env, 8200, "review-preservation")
	p, event := "c001beef-0000-4000-8000-000000004444", "d001beef-0000-4000-8000-000000005555"
	ingestCatalog(t, env, event, catalog.EventProductSnapshotV1, "2026-10-05T10:00:00Z", productPayload(p, "ML-PRESERVE-001", "بردي", root, []string{sub}, []string{tag}, 1))
	projectCatalogOnce(t, env, event)
	conn, e := sql.Open("pgx", env.pool.Config().ConnConfig.ConnString())
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	if e = migrate.DownTo(ctx, conn, 30); e != nil {
		t.Fatal(e)
	}
	// Seed other durable domains in exact v30 before freezing old columns.
	// Their content must remain byte/value identical after the appended ALTER.
	seeds := []string{
		`INSERT INTO notification_template_mappings(provider_key,template_key,locale,external_template_name,external_language_code) VALUES ('telegram-main','daily_business_report_v1','ar','plain','ar')`,
		`INSERT INTO notification_messages(id,provider_key,idempotency_key,semantic_fingerprint,recipient,template_key,locale,ext_template_name,ext_language_code,parameters,dispatch_status) VALUES ('11111111-0000-7000-8000-000000000001','telegram-main','r3-preserve','\x0102','@preservedrecipient','daily_business_report_v1','ar','plain','ar','{"body":"تقرير محفوظ"}','accepted')`,
		`INSERT INTO business_report_recipients(id,label,provider_key,recipient,locale) VALUES ('11111111-0000-7000-8000-000000000002','preserved-report','telegram-main','@preservedreport','ar')`,
		`INSERT INTO operational_alert_recipients(id,label,provider_key,recipient,locale) VALUES ('11111111-0000-7000-8000-000000000003','preserved-operation','telegram-main','@preservedops','en')`,
		`INSERT INTO commerce_product_mappings(provider_key,product_id,external_product_id) VALUES ('r3-preserve','11111111-0000-4000-8000-000000000001','preserved')`,
		`INSERT INTO commerce_product_mutation_barriers(operation_id,provider_key,product_id,request_fingerprint,state) VALUES ('11111111-0000-7000-8000-000000000009','r3-preserve','11111111-0000-4000-8000-000000000001',repeat('a',64),'uncertain')`,
	}
	for _, q := range seeds {
		result, e := conn.ExecContext(ctx, q)
		if e != nil {
			t.Fatal(e)
		}
		count, e := result.RowsAffected()
		if e != nil || count != 1 {
			t.Fatal("migration seed affected rows", e)
		}
	}
	type frozen struct{ query, value string }
	all := []frozen{}
	// Phase 17-R0 00035 retires catalog_products.sku (ADR-0049): the
	// frozen v30 column set excludes the retired column so the 30→36→30
	// cycle compares every remaining business value byte-identically.
	rows, e := conn.QueryContext(ctx, `SELECT table_name,string_agg(quote_ident(column_name),',' ORDER BY ordinal_position) FROM information_schema.columns WHERE table_schema='public' AND table_name<>'goose_db_version' AND NOT (table_name='catalog_products' AND column_name='sku') GROUP BY table_name ORDER BY table_name`)
	if e != nil {
		t.Fatal(e)
	}
	for rows.Next() {
		var name, cols string
		if e = rows.Scan(&name, &cols); e != nil {
			t.Fatal(e)
		}
		q := fmt.Sprintf(`SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY to_jsonb(r)::text),'[]')::text FROM (SELECT %s FROM %s) r`, cols, name)
		var v string
		if e = conn.QueryRowContext(ctx, q).Scan(&v); e != nil {
			t.Fatal(e)
		}
		all = append(all, frozen{q, v})
	}
	rows.Close()
	for i := 0; i < 2; i++ {
		if e = migrate.Up(ctx, conn); e != nil {
			t.Fatal(e)
		}
		for _, a := range all {
			var v string
			if e = conn.QueryRowContext(ctx, a.query).Scan(&v); e != nil || v != a.value {
				t.Fatalf("preservation mismatch %s %v", a.query, e)
			}
		}
		if i == 0 {
			if e = migrate.DownTo(ctx, conn, 30); e != nil {
				t.Fatal(e)
			}
		}
	}
	t.Logf("PASS: schema30→31→30→31 preserves all old-column values in %d tables; seeded Product, Category, Tag, device, ingress and projection state", len(all))
	if _, e = conn.ExecContext(ctx, "SET enable_seqscan=off"); e != nil {
		t.Fatal(e)
	}
	for _, q := range []string{"SELECT * FROM catalog_product_configurations WHERE product_id='" + p + "' ORDER BY position,configuration_id", "SELECT * FROM commerce_product_configuration_mappings WHERE provider_key='website' AND external_product_id='500' AND external_configuration_id='501'", "SELECT * FROM commerce_online_order_lines WHERE provider_key='website' AND external_order_id='15001'"} {
		r, e := conn.QueryContext(ctx, "EXPLAIN "+q)
		if e != nil {
			t.Fatal(e)
		}
		var plan []string
		for r.Next() {
			var x string
			r.Scan(&x)
			plan = append(plan, x)
		}
		r.Close()
		t.Log("eligible plan:", strings.Join(plan, "; "))
	}
	// Receipt history is intentionally durable. Rollback must refuse even
	// completed receipts instead of discarding the adoption evidence.
	tagResult, e := conn.ExecContext(ctx, `INSERT INTO commerce_product_mutation_barriers(operation_id,provider_key,product_id,request_fingerprint,state,resolved_at,resolution,async_role,async_intent,provider_operation_id,async_state,async_product_id) VALUES('019c0000-0000-7000-8000-000000000031','website',$1,repeat('a',64),'resolved',now(),'remote_completed','bundle_create',repeat('b',64),'gid://shopify/ProductBundleOperation/31','completed','gid://shopify/Product/31')`, p)
	if e != nil {
		t.Fatal(e)
	}
	count, e := tagResult.RowsAffected()
	if e != nil || count != 1 {
		t.Fatal("receipt fixture insert", e)
	}
	if e = migrate.DownTo(ctx, conn, 30); e == nil {
		t.Fatal("rollback discarded receipt provenance")
	}
	var receipts int
	if e = conn.QueryRowContext(ctx, "SELECT count(*) FROM commerce_product_mutation_barriers WHERE async_role='bundle_create'").Scan(&receipts); e != nil || receipts != 1 {
		t.Fatal("refused rollback altered receipt", e)
	}
	t.Log("rollback refuses receipt history and preserves all provenance")

}
