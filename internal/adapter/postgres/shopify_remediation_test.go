package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce/orders"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce/shopify"
	"github.com/faroukelabady/MoonLightCloud/internal/config"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type r1ShopifyTransport struct {
	base   http.RoundTripper
	target *url.URL
}

func (tr r1ShopifyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	q := r.Clone(r.Context())
	u := *q.URL
	u.Scheme = tr.target.Scheme
	u.Host = tr.target.Host
	q.URL = &u
	q.Host = u.Host
	return tr.base.RoundTrip(q)
}
func r1ShopifyMoney(s string) map[string]any {
	return map[string]any{"shopMoney": map[string]any{"amount": s, "currencyCode": "EGP"}}
}
func r1ShopifyOrder(id string, products ...string) map[string]any {
	lines := []any{}
	for i, p := range products {
		lines = append(lines, map[string]any{"id": fmt.Sprintf("gid://shopify/LineItem/%d", 100+i), "sku": "SAME-SKU", "title": "review item", "quantity": 1, "originalTotalSet": r1ShopifyMoney("1.00"), "discountedTotalSet": r1ShopifyMoney("1.00"), "taxLines": []any{}, "product": map[string]any{"id": "gid://shopify/Product/" + p}})
	}
	return map[string]any{"id": "gid://shopify/Order/" + id, "legacyResourceId": id, "name": "review", "createdAt": "2026-09-20T10:00:00Z", "updatedAt": "2026-09-20T11:00:00Z", "displayFinancialStatus": "PAID", "displayFulfillmentStatus": "UNFULFILLED", "currencyCode": "EGP", "totalPriceSet": r1ShopifyMoney("90071992547409.93"), "totalShippingPriceSet": r1ShopifyMoney("0.00"), "totalTaxSet": r1ShopifyMoney("0.00"), "totalDiscountsSet": r1ShopifyMoney("0.00"), "lineItems": map[string]any{"nodes": lines, "pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil}}}
}
func TestR1ShopifyRealPostgresPipeline(t *testing.T) {
	f := openScopeFixture(t)
	prodA, prodB := scopeProducts(t, f, 1880, "shopreview", "SAME-SKU")
	createMapping(t, f, "shopify-main", prodA, "700")
	createMapping(t, f, "shopify-main", prodB, "701")
	createMapping(t, f, "woo-main", prodA, "700")
	fixture := map[string]any{"800": r1ShopifyOrder("800", "700"), "801": r1ShopifyOrder("801", "700", "701"), "802": r1ShopifyOrder("802", "999")}
	remote := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Shopify-API-Version", "2026-10")
		if r.Header.Get("X-Shopify-Access-Token") != "review-token" {
			t.Error("missing access-token authority")
		}
		var in struct{ Variables map[string]string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		id := strings.TrimPrefix(in.Variables["id"], "gid://shopify/Order/")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"order": fixture[id]}})
	}))
	defer remote.Close()
	u, _ := url.Parse(remote.URL)
	client := remote.Client()
	client.Transport = r1ShopifyTransport{client.Transport, u}
	p, err := shopify.NewShopifyProvider(config.ShopifyConfig{Enabled: true, ProviderKey: "shopify-main", ShopDomain: "test.myshopify.com", APIVersion: "2026-10", AccessToken: "review-token", ClientSecret: "review-hmac-secret", Currency: "EGP", LocationID: "gid://shopify/Location/10", PublicationID: "gid://shopify/Publication/11", HTTPTimeout: 5 * time.Second, OrdersEnabled: true}, client)
	if err != nil {
		t.Fatal(err)
	}
	reg := commerce.NewRegistry()
	if err := reg.Register(p.Key(), p); err != nil {
		t.Fatal(err)
	}
	store := NewDevices(f.pool, 5*time.Second)
	svc := orders.NewOrderService(reg, store, nilLogger())
	ctx := context.Background()
	if _, err := svc.ReconcileOrder(ctx, "shopify-main", "800"); err != nil {
		t.Fatal(err)
	}
	if got := orderStore(t, f, "shopify-main", "800"); got == nil || *got != scopeStoreA {
		t.Fatal("incorrect store derivation")
	}
	var minor int64
	if err := f.pool.QueryRow(ctx, `SELECT total_minor FROM commerce_online_orders WHERE provider_key='shopify-main' AND external_order_id='800'`).Scan(&minor); err != nil || minor != 9007199254740993 {
		t.Fatalf("money %d %v", minor, err)
	}
	if _, err := svc.ReconcileOrder(ctx, "shopify-main", "801"); err == nil {
		t.Fatal("mixed store accepted")
	}
	if _, err := svc.ReconcileOrder(ctx, "shopify-main", "802"); err != nil {
		t.Fatal(err)
	}
	if orderStore(t, f, "shopify-main", "802") != nil {
		t.Fatal("unresolved store guessed")
	}
	// Real durable inbox adopts only metadata/hash; actual HTTP is covered separately.
	raw := `{"id":800,"email":"private-review-customer@example.com"}`
	hash := sha256.Sum256([]byte(raw))
	outcome, err := store.InsertOrderWebhookEvent(ctx, "shopify-main", "review-durable-delivery", orders.TopicOrderUpdated, "800", hash[:], nil)
	if err != nil || outcome != orders.WebhookInserted {
		t.Fatal(err)
	}
	outcome, err = store.InsertOrderWebhookEvent(ctx, "shopify-main", "review-durable-delivery", orders.TopicOrderUpdated, "800", hash[:], nil)
	if err != nil || outcome != orders.WebhookDuplicateIdentical {
		t.Fatal("bad replay")
	}
	changed := sha256.Sum256([]byte("changed"))
	if _, err := store.InsertOrderWebhookEvent(ctx, "shopify-main", "review-durable-delivery", orders.TopicOrderUpdated, "800", changed[:], nil); err == nil {
		t.Fatal("contradiction accepted")
	}
	processor := orders.NewProcessor(store, svc, orders.SystemClock{}, "review-pg-worker", nilLogger())
	runctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); processor.Run(runctx) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var status string
		_ = f.pool.QueryRow(ctx, `SELECT status FROM commerce_online_order_webhook_events WHERE delivery_id='review-durable-delivery'`).Scan(&status)
		if status == "processed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("inbox stuck=%s", status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM commerce_online_order_webhook_events WHERE delivery_id='review-durable-delivery'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate inbox")
	}
	outcome, err = store.InsertOrderWebhookEvent(ctx, "shopify-main", "review-durable-delivery", orders.TopicOrderUpdated, "800", hash[:], nil)
	if err != nil || outcome != orders.WebhookDuplicateIdentical {
		t.Fatal("bad processed replay")
	}
	t.Logf("real PostgreSQL: mappings separated; exact money=%d; Store A resolved; mixed blocked; unresolved NULL; durable inbox/dedupe/contradiction/processor/replay passed", minor)
}

func TestR1ShopifyPagedProjectionAtomicity(t *testing.T) {
	f := openScopeFixture(t)
	prodA, prodB := scopeProducts(t, f, 1881, "shoppaged", "SAME-SKU")
	createMapping(t, f, "shopify-main", prodA, "700")
	createMapping(t, f, "shopify-main", prodB, "701")
	ctx := context.Background()
	var mu sync.Mutex
	mode := "valid"
	remote := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Shopify-API-Version", "2026-10")
		var input struct{ Variables map[string]any }
		_ = json.NewDecoder(r.Body).Decode(&input)
		mu.Lock()
		current := mode
		mu.Unlock()
		id := strings.TrimPrefix(input.Variables["id"].(string), "gid://shopify/Order/")
		cursor, _ := input.Variables["after"].(string)
		if cursor != "" && current == "network" {
			w.WriteHeader(503)
			return
		}
		products := make([]string, 50)
		for i := range products {
			products[i] = "700"
		}
		start := 0
		if cursor != "" {
			products = []string{"700"}
			start = 50
			if current == "mixed" {
				products[0] = "701"
			}
			if current == "deleted" {
				products[0] = "999"
			}
		}
		order := r1ShopifyOrder(id, products...)
		connection := order["lineItems"].(map[string]any)
		for i, raw := range connection["nodes"].([]any) {
			line := raw.(map[string]any)
			line["id"] = fmt.Sprintf("gid://shopify/LineItem/%d", 100+i+start)
			if start == 50 {
				line["discountedTotalSet"] = r1ShopifyMoney("90071992547409.93")
			}
		}
		connection["pageInfo"] = map[string]any{"hasNextPage": cursor == "", "endCursor": "50"}
		if cursor != "" && current == "repeat" {
			connection["pageInfo"] = map[string]any{"hasNextPage": true, "endCursor": "50"}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"order": order}})
	}))
	defer remote.Close()
	target, _ := url.Parse(remote.URL)
	client := remote.Client()
	client.Transport = r1ShopifyTransport{client.Transport, target}
	provider, err := shopify.NewShopifyProvider(config.ShopifyConfig{Enabled: true, ProviderKey: "shopify-main", ShopDomain: "test.myshopify.com", APIVersion: "2026-10", AccessToken: "local-r1-token", ClientSecret: "local-r1-hmac-secret", Currency: "EGP", LocationID: "gid://shopify/Location/10", PublicationID: "gid://shopify/Publication/11", HTTPTimeout: 5 * time.Second, OrdersEnabled: true}, client)
	if err != nil {
		t.Fatal(err)
	}
	registry := commerce.NewRegistry()
	if err := registry.Register(provider.Key(), provider); err != nil {
		t.Fatal(err)
	}
	store := NewDevices(f.pool, 5*time.Second)
	service := orders.NewOrderService(registry, store, nilLogger())
	if _, err := service.ReconcileOrder(ctx, "shopify-main", "800"); err != nil {
		t.Fatal(err)
	}
	var count int
	var amount int64
	if err := f.pool.QueryRow(ctx, `SELECT count(*),max(total_minor) FROM commerce_online_order_lines WHERE provider_key='shopify-main' AND external_order_id='800'`).Scan(&count, &amount); err != nil || count != 51 || amount != 9007199254740993 {
		t.Fatalf("full projection: count=%d amount=%d %v", count, amount, err)
	}
	state := func() string {
		var body string
		err := f.pool.QueryRow(ctx, `SELECT jsonb_build_object('order',to_jsonb(o),'lines',(SELECT jsonb_agg(to_jsonb(l) ORDER BY external_line_id) FROM commerce_online_order_lines l WHERE l.provider_key=o.provider_key AND l.external_order_id=o.external_order_id))::text FROM commerce_online_orders o WHERE provider_key='shopify-main' AND external_order_id='800'`).Scan(&body)
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	before := state()
	for _, fault := range []string{"network", "mixed", "repeat"} {
		mu.Lock()
		mode = fault
		mu.Unlock()
		if _, err := service.ReconcileOrder(ctx, "shopify-main", "800"); err == nil {
			t.Fatal("incomplete/mixed pages projected", fault)
		}
		if state() != before {
			t.Fatal("failed pagination changed authoritative snapshot", fault)
		}
	}
	mu.Lock()
	mode = "deleted"
	mu.Unlock()
	if _, err := service.ReconcileOrder(ctx, "shopify-main", "801"); err != nil {
		t.Fatal(err)
	}
	var unresolved int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM commerce_online_order_lines WHERE provider_key='shopify-main' AND external_order_id='801' AND moonlight_product_id IS NULL`).Scan(&unresolved); err != nil || unresolved != 1 {
		t.Fatalf("late deleted line lost: %d %v", unresolved, err)
	}
	// Durable ingress and processor recreation; signed HTTP is verified by
	// the external lifecycle test and the actual OCI runtime.
	body := []byte(`{"id":802,"email":"private-r1-customer@example.com"}`)
	hash := sha256.Sum256(body)
	if _, err := store.InsertOrderWebhookEvent(ctx, "shopify-main", "r1-signed-paged", orders.TopicOrderUpdated, "802", hash[:], nil); err != nil {
		t.Fatal(err)
	}
	processor := orders.NewProcessor(store, service, orders.SystemClock{}, "r1-after-restart", nilLogger())
	run, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); processor.Run(run) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var status string
		err := f.pool.QueryRow(ctx, `SELECT status FROM commerce_online_order_webhook_events WHERE delivery_id='r1-signed-paged'`).Scan(&status)
		if err != nil {
			t.Fatal(err)
		}
		if status == "processed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("signed durable webhook failed to converge", status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM commerce_online_order_lines WHERE provider_key='shopify-main' AND external_order_id='802'`).Scan(&count); err != nil || count != 51 {
		t.Fatal("webhook lost late page")
	}
}
