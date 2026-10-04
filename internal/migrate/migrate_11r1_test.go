package migrate_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/migrate"
)

func TestV26To27MutationBarrierPreservation(t *testing.T) {
	conn, ctx := openRaw(t)
	if err := migrate.UpTo(ctx, conn, 26); err != nil {
		t.Fatal(err)
	}
	seeds := []string{
		`INSERT INTO devices(id,name,status,created_at,updated_at) VALUES ('11111111-1111-4111-8111-111111111111','preserved-device','active',now(),now())`,
		`INSERT INTO commerce_product_mappings(provider_key,product_id,external_product_id) VALUES ('shopify-main','aaaaaaaa-0000-4000-8000-000000000001','500')`,
		`INSERT INTO commerce_online_orders(provider_key,external_order_id,canonical_status,currency,discount_minor,shipping_minor,cart_tax_minor,total_tax_minor,total_minor,created_at,modified_at,revision,fingerprint,mapping_complete,unmapped_lines) VALUES ('shopify-main','800','PROCESSING','EGP',0,0,0,0,9007199254740993,now(),now(),1,'\x01',true,0)`,
		`INSERT INTO notification_template_mappings(provider_key,template_key,locale,external_template_name,external_language_code) VALUES ('telegram-main','preserve_v1','ar','plain','ar')`,
		`INSERT INTO notification_messages(id,provider_key,idempotency_key,semantic_fingerprint,recipient,template_key,locale,ext_template_name,ext_language_code,parameters,dispatch_status) VALUES ('11111111-0000-7000-8000-000000000001','telegram-main','preserve','\x0102','@preservedrecipient','preserve_v1','ar','plain','ar','{"body":"تقرير محفوظ"}','accepted')`,
		`INSERT INTO business_report_recipients(id,label,provider_key,recipient,locale) VALUES ('11111111-0000-7000-8000-000000000002','preserved-report','telegram-main','@preservedreport','ar')`,
		`INSERT INTO operational_alert_recipients(id,label,provider_key,recipient,locale) VALUES ('11111111-0000-7000-8000-000000000003','preserved-operation','telegram-main','@preservedops','en')`,
	}
	for _, query := range seeds {
		tag, err := conn.ExecContext(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		n, err := tag.RowsAffected()
		if err != nil || n != 1 {
			t.Fatal("seed row count", n, err)
		}
	}
	rows, err := conn.QueryContext(ctx, `SELECT table_name FROM information_schema.tables WHERE table_schema='public' AND table_type='BASE TABLE' AND table_name<>'goose_db_version' ORDER BY table_name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	snapshot := func(table string) string {
		var data string
		quoted := `"` + strings.ReplaceAll(table, `"`, `""`) + `"`
		if err := conn.QueryRowContext(ctx, fmt.Sprintf(`SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM %s t`, quoted)).Scan(&data); err != nil {
			t.Fatal(err)
		}
		return data
	}
	before := map[string]string{}
	for _, table := range tables {
		before[table] = snapshot(table)
	}
	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if got := version(t, conn, ctx); got != migrate.TargetVersion {
		t.Fatal("schema", got)
	}
	for _, table := range tables {
		if snapshot(table) != before[table] {
			t.Fatal("existing state changed", table)
		}
	}
	var n int
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM commerce_product_mutation_barriers").Scan(&n); err != nil || n != 0 {
		t.Fatal("fabricated barriers", n, err)
	}
	if err := migrate.DownTo(ctx, conn, 26); err != nil {
		t.Fatal("empty downgrade", err)
	}
	for _, table := range tables {
		if snapshot(table) != before[table] {
			t.Fatal("downgrade altered state", table)
		}
	}
	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
}

func TestFreshTo27MutationBarrierConstraintsAndRollback(t *testing.T) {
	conn, ctx := openRaw(t)
	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO commerce_product_mutation_barriers(operation_id,provider_key,product_id,request_fingerprint,state) VALUES ($1,'shopify-main','aaaaaaaa-0000-4000-8000-000000000001',$2,'in_flight')`
	first := "019c0000-0000-7000-8000-000000000001"
	second := "019c0000-0000-7000-8000-000000000002"
	if _, err := conn.ExecContext(ctx, insert, first, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, insert, second, strings.Repeat("b", 64)); err == nil {
		t.Fatal("two active barriers accepted")
	}
	if err := migrate.DownTo(ctx, conn, 26); err == nil {
		t.Fatal("rollback erased active uncertainty")
	}
	if got := version(t, conn, ctx); got != migrate.TargetVersion {
		t.Fatal("failed rollback changed schema", got)
	}
	tag, err := conn.ExecContext(ctx, `UPDATE commerce_product_mutation_barriers SET state='resolved',resolved_at=now(),resolution='remote_completed' WHERE operation_id=$1`, first)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := tag.RowsAffected(); n != 1 {
		t.Fatal("resolution fixture", n)
	}
	if _, err := conn.ExecContext(ctx, insert, second, strings.Repeat("b", 64)); err != nil {
		t.Fatal("resolved history blocked new barrier", err)
	}
	if err := migrate.DownTo(ctx, conn, 26); err == nil {
		t.Fatal("rollback erased resolution history")
	}
	var n int
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM commerce_product_mutation_barriers").Scan(&n); err != nil || n != 2 {
		t.Fatal("rollback lost barrier history", n, err)
	}
	var plan string
	if _, err := conn.ExecContext(ctx, "SET enable_seqscan=off"); err != nil {
		t.Fatal(err)
	}
	rows, err := conn.QueryContext(ctx, `EXPLAIN SELECT operation_id FROM commerce_product_mutation_barriers WHERE provider_key='shopify-main' AND product_id='aaaaaaaa-0000-4000-8000-000000000001' AND state IN ('in_flight','uncertain')`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var row string
		rows.Scan(&row)
		plan += row
	}
	rows.Close()
	if !strings.Contains(plan, "idx_commerce_mutation_barrier_active") {
		t.Fatal("active index ineligible", plan)
	}
}
