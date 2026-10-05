package shopify

// Stateful TLS fixture for the production bundle lifecycle. Base Product,
// inventory, auth and ordinary mutations use the existing strict harness.
// This layer implements sparse component choices, asynchronous operations,
// relationships and pagination; it validates supplied IDs against stored state.
import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"github.com/faroukelabady/MoonLightCloud/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

type bundleFixture struct {
	h                                                           *shopifyHarness
	t                                                           *testing.T
	mu                                                          sync.Mutex
	resources                                                   map[string]map[string]any
	operations                                                  map[string]map[string]any
	variantIDs                                                  map[string]string
	prices                                                      map[string]string
	creates, updates, polls, pendingReads, priceWrites          int
	next                                                        int
	dropCreate, failPublication, truncateVariants, repeatCursor bool
	injectOperation, injectMessage                              string
	productUpdates                                              []map[string]any
	operationErrorWithProduct                                   bool
}

func newBundleFixture(t *testing.T) *bundleFixture {
	var f *bundleFixture
	newHarnessWithHandler(t, func(h *shopifyHarness) http.Handler {
		f = &bundleFixture{h: h, t: t, resources: map[string]map[string]any{}, operations: map[string]map[string]any{}, prices: map[string]string{}, variantIDs: map[string]string{}, next: 9000}
		return http.HandlerFunc(f.serve)
	})
	return f
}
func (f *bundleFixture) reject(w http.ResponseWriter, message string, status int) {
	f.t.Log("fixture rejected:", message)
	http.Error(w, message, status)
}
func (f *bundleFixture) reply(w http.ResponseWriter, data any) {
	raw, _ := json.Marshal(map[string]any{"data": data})
	writeGQL(w, 200, string(raw))
}
func (f *bundleFixture) serve(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		f.reject(w, "read", 400)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var request gqlRequest
	if json.Unmarshal(raw, &request) != nil {
		f.reject(w, "decode", 400)
		return
	}
	if r.Header.Get("X-Shopify-Access-Token") != f.h.token {
		f.reject(w, "auth", 401)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := validateWireContract(request.Query, request.Variables); err != nil {
		f.reject(w, "wire contract", 400)
		return
	}
	if operationName(request.Query) == f.injectOperation && f.injectOperation != "" {
		roots := map[string]string{"frame_component_set": "productSet", "bundle_create": "productBundleCreate", "bundle_update": "productBundleUpdate", "MoonlightVariantPrices": "productVariantsBulkUpdate", "MoonlightMetafieldsSet": "metafieldsSet", "bundle_operation": "productOperation"}
		if root := roots[f.injectOperation]; root != "" {
			payload := map[string]any{"userErrors": []any{map[string]any{"message": f.injectMessage}}}
			if f.injectOperation == "bundle_operation" {
				payload["status"] = "COMPLETE"
				payload["product"] = nil
			}
			f.reply(w, map[string]any{root: payload})
		} else {
			writeGQL(w, 200, `{"errors":[{"message":`+strconv.Quote(f.injectMessage)+`,"extensions":{"code":"INTERNAL_SERVER_ERROR"}}]}`)
		}
		return
	}
	switch {
	case strings.Contains(request.Query, "MoonlightBundleDiscover"):
		nodes := []any{}
		query := str(request.Variables["query"])
		for id, resource := range f.resources {
			fields := resource["metafields"].(map[string]any)["nodes"].([]any)
			product, role := "", ""
			for _, raw := range fields {
				m := raw.(map[string]any)
				if m["key"] == metafieldProductID {
					product = str(m["value"])
				}
				if m["key"] == keyRole {
					role = str(m["value"])
				}
			}
			if product != "" && role != "" && strings.Contains(query, product) && strings.Contains(query, role) {
				nodes = append(nodes, map[string]any{"id": id})
			}
		}
		f.reply(w, map[string]any{"products": map[string]any{"nodes": nodes}})
	case strings.Contains(request.Query, "MoonlightFrameComponentSet"):
		input := request.Variables["input"].(map[string]any)
		id := str(input["id"])
		if id == "" {
			f.next++
			id = fmt.Sprintf("gid://shopify/Product/%d", f.next)
		}
		fields := []any{}
		if old := f.resources[id]; old != nil {
			fields = old["metafields"].(map[string]any)["nodes"].([]any)
		}
		if raw, ok := input["metafields"].([]any); ok {
			fields = raw
		}
		options := []any{}
		allowed := map[string]map[string]bool{}
		for i, raw := range input["productOptions"].([]any) {
			o := raw.(map[string]any)
			name := str(o["name"])
			allowed[name] = map[string]bool{}
			for _, v := range o["values"].([]any) {
				value, ok := v.(map[string]any)
				if !ok {
					f.reject(w, "option value object", 400)
					return
				}
				allowed[name][str(value["name"])] = true
			}
			options = append(options, map[string]any{"id": fmt.Sprintf("gid://shopify/ProductOption/%d", 100+i), "name": name})
		}
		variants := []any{}
		for _, raw := range input["variants"].([]any) {
			v := raw.(map[string]any)
			if v["inventoryItem"].(map[string]any)["tracked"] != false {
				f.reject(w, "frame stock", 400)
				return
			}
			selected := []any{}
			for _, raw := range v["optionValues"].([]any) {
				o := raw.(map[string]any)
				name, value := str(o["optionName"]), str(o["name"])
				if !allowed[name][value] {
					f.reject(w, "undefined choice", 400)
					return
				}
				selected = append(selected, map[string]any{"name": name, "value": value})
			}
			metadata := v["metafields"].([]any)
			cid := ""
			for _, raw := range metadata {
				m := raw.(map[string]any)
				if m["key"] == keyConfigurationIDMeta {
					cid = str(m["value"])
				}
			}
			if cid == "" {
				f.reject(w, "missing identity", 400)
				return
			}
			// Stable variant identity follows immutable configuration metadata.
			variantID := f.variantIdentity(id, cid)
			variants = append(variants, map[string]any{"id": variantID, "metafields": map[string]any{"nodes": metadata}, "selectedOptions": selected})
		}
		resource := map[string]any{"id": id, "options": options, "metafields": map[string]any{"nodes": fields}, "variants": map[string]any{"nodes": variants}}
		f.resources[id] = resource
		f.reply(w, map[string]any{"productSet": map[string]any{"product": resource, "userErrors": []any{}}})
	case strings.Contains(request.Query, "MoonlightBundleCreate") || strings.Contains(request.Query, "MoonlightBundleUpdate"):
		input := request.Variables["input"].(map[string]any)
		components := input["components"].([]any)
		if len(components) != 2 {
			f.reject(w, "components", 400)
			return
		}
		base := components[0].(map[string]any)
		frame := components[1].(map[string]any)
		for _, c := range []map[string]any{base, frame} {
			opts, ok := c["optionSelections"].([]any)
			if !ok || len(opts) == 0 {
				f.reject(w, "required option selections", 400)
				return
			}
			for _, raw := range opts {
				o := raw.(map[string]any)
				if str(o["componentOptionId"]) == "" || o["values"] == nil {
					f.reject(w, "option identity", 400)
					return
				}
			}
		}
		baseID := str(base["productId"])
		frameID := str(frame["productId"])
		bID, e := ParseGID(baseID, ResourceProduct)
		if e != nil || f.h.products[bID] == nil || f.resources[frameID] == nil {
			f.reject(w, "unknown components", 400)
			return
		}
		// Selections must name actual owned option IDs and values; nonempty
		// placeholders cannot pass this stateful oracle.
		allowedBase := map[string]map[string]bool{"gid://shopify/ProductOption/" + bID: {"Default Title": true}}
		allowedFrame := map[string]map[string]bool{}
		frameResource := f.resources[frameID]
		for _, raw := range frameResource["options"].([]any) {
			o := raw.(map[string]any)
			values := map[string]bool{}
			for _, raw := range frameResource["variants"].(map[string]any)["nodes"].([]any) {
				for _, raw := range raw.(map[string]any)["selectedOptions"].([]any) {
					selected := raw.(map[string]any)
					if selected["name"] == o["name"] {
						values[str(selected["value"])] = true
					}
				}
			}
			allowedFrame[str(o["id"])] = values
		}
		for i, component := range []map[string]any{base, frame} {
			allowed := allowedBase
			if i == 1 {
				allowed = allowedFrame
			}
			selections := component["optionSelections"].([]any)
			if len(selections) != len(allowed) {
				f.reject(w, "selection coverage", 400)
				return
			}
			for _, raw := range selections {
				o := raw.(map[string]any)
				values := allowed[str(o["componentOptionId"])]
				for _, raw := range o["values"].([]any) {
					if !values[str(raw)] {
						f.reject(w, "foreign option selection", 400)
						return
					}
				}
			}
		}
		id := str(input["productId"])
		root := "productBundleUpdate"
		if id == "" {
			root = "productBundleCreate"
			f.creates++
			f.next++
			id = fmt.Sprintf("gid://shopify/Product/%d", f.next)
		} else {
			f.updates++
			if f.resources[id] == nil {
				f.reject(w, "unknown bundle", 400)
				return
			}
		}
		fields := []any{}
		if old := f.resources[id]; old != nil {
			fields = old["metafields"].(map[string]any)["nodes"].([]any)
		}
		choices := f.resources[frameID]["variants"].(map[string]any)["nodes"].([]any)
		variants := []any{}
		for _, raw := range choices {
			choice := raw.(map[string]any)
			variantID := f.variantIdentity(id, metafieldIdentity(choice))
			metadata := []any{}
			if old := f.resources[id]; old != nil {
				for _, v := range old["variants"].(map[string]any)["nodes"].([]any) {
					ov := v.(map[string]any)
					if ov["id"] == variantID {
						metadata = ov["metafields"].(map[string]any)["nodes"].([]any)
					}
				}
			}
			components := []any{map[string]any{"quantity": 1, "productVariant": map[string]any{"id": f.h.products[bID].variants[0].gid, "product": map[string]any{"id": baseID}}}, map[string]any{"quantity": 1, "productVariant": map[string]any{"id": choice["id"], "product": map[string]any{"id": frameID}}}}
			variants = append(variants, map[string]any{"id": variantID, "metafields": map[string]any{"nodes": metadata}, "productVariantComponents": map[string]any{"nodes": components, "pageInfo": map[string]any{"hasNextPage": false}}})
		}
		// Metadata-state oracle semantics: create takes the submitted
		// title (default ACTIVE, empty description); an update with an
		// omitted title/description/status retains the prior value
		// rather than inventing an implicit rename.
		title, description, status := str(input["title"]), "", "ACTIVE"
		if prior := f.resources[id]; prior != nil {
			if input["title"] == nil {
				title = str(prior["title"])
			}
			description = str(prior["descriptionHtml"])
			status = str(prior["status"])
		}
		resource := map[string]any{"id": id, "title": title, "descriptionHtml": description, "status": status, "metafields": map[string]any{"nodes": fields}, "variants": map[string]any{"nodes": variants}}
		op := fmt.Sprintf("gid://shopify/ProductBundleOperation/%d", len(f.operations)+1)
		f.operations[op] = map[string]any{"id": op, "status": "ACTIVE", "product": map[string]any{"id": id}, "userErrors": []any{}, "resource": resource}
		if root == "productBundleCreate" && f.dropCreate {
			f.dropCreate = false
			panic(http.ErrAbortHandler)
		}
		f.reply(w, map[string]any{root: map[string]any{"productBundleOperation": map[string]any{"id": op, "status": "CREATED"}, "userErrors": []any{}}})
	case strings.Contains(request.Query, "MoonlightBundleOperation"):
		f.polls++
		op := f.operations[str(request.Variables["id"])]
		if op == nil {
			f.reject(w, "unknown receipt", 400)
			return
		}
		if f.pendingReads > 0 {
			f.pendingReads--
		} else {
			op["status"] = "COMPLETE"
			if resource, ok := op["resource"].(map[string]any); ok {
				f.resources[str(resource["id"])] = resource
			}
			if f.operationErrorWithProduct {
				op["userErrors"] = []any{map[string]any{"message": "processing failed after Product creation", "code": "GENERIC_ERROR"}}
			}
		}
		f.reply(w, map[string]any{"productOperation": op})
	case strings.Contains(request.Query, "MoonlightProduct($id") && f.resources[str(request.Variables["id"])] != nil:
		original := f.resources[str(request.Variables["id"])]
		resource := map[string]any{}
		for k, v := range original {
			resource[k] = v
		}
		nodes := original["variants"].(map[string]any)["nodes"].([]any)
		start := 0
		if after := str(request.Variables["after"]); after != "" {
			start, _ = strconv.Atoi(after)
		}
		end := start + 100
		if end > len(nodes) {
			end = len(nodes)
		}
		hasNext, cursor := end < len(nodes), strconv.Itoa(end)
		if f.truncateVariants {
			hasNext = false
		}
		if f.repeatCursor {
			hasNext = true
			cursor = "0"
		}
		resource["variants"] = map[string]any{"nodes": nodes[start:end], "pageInfo": map[string]any{"hasNextPage": hasNext, "endCursor": cursor}}
		f.reply(w, map[string]any{"product": resource})
	case strings.Contains(request.Query, "MoonlightMetafieldsSet"):
		fields := request.Variables["metafields"].([]any)
		owned := false
		for _, raw := range fields {
			field := raw.(map[string]any)
			resource := f.resources[str(field["ownerId"])]
			if resource == nil {
				continue
			}
			owned = true
			nodes := resource["metafields"].(map[string]any)["nodes"].([]any)
			found := false
			for _, raw := range nodes {
				m := raw.(map[string]any)
				if m["key"] == field["key"] {
					m["value"] = field["value"]
					found = true
				}
			}
			if !found {
				nodes = append(nodes, field)
			}
			resource["metafields"].(map[string]any)["nodes"] = nodes
		}
		if !owned {
			f.h.serve(w, r)
			return
		}
		f.reply(w, map[string]any{"metafieldsSet": map[string]any{"metafields": []any{}, "userErrors": []any{}}})
	case strings.Contains(request.Query, "MoonlightVariantPrices"):
		resource := f.resources[str(request.Variables["productId"])]
		if resource == nil {
			f.reject(w, "unknown price owner", 400)
			return
		}
		nodes := resource["variants"].(map[string]any)["nodes"].([]any)
		for _, raw := range request.Variables["variants"].([]any) {
			input := raw.(map[string]any)
			found := false
			for _, raw := range nodes {
				v := raw.(map[string]any)
				if v["id"] == input["id"] {
					found = true
					if price, ok := input["price"]; ok {
						f.prices[str(v["id"])] = str(price)
						f.priceWrites++
					}
					if fields, ok := input["metafields"]; ok {
						v["metafields"] = map[string]any{"nodes": fields}
					}
				}
			}
			if !found {
				f.reject(w, "foreign price", 400)
				return
			}
		}
		f.reply(w, map[string]any{"productVariantsBulkUpdate": map[string]any{"productVariants": []any{}, "userErrors": []any{}}})
	case strings.Contains(request.Query, "MoonlightProductUpdate") && f.resources[str(request.Variables["input"].(map[string]any)["id"])] != nil:
		input := request.Variables["input"].(map[string]any)
		f.productUpdates = append(f.productUpdates, input)
		resource := f.resources[str(input["id"])]
		for _, key := range []string{"title", "descriptionHtml", "status"} {
			if v, ok := input[key]; ok {
				resource[key] = v
			}
		}
		f.reply(w, map[string]any{"productUpdate": map[string]any{"product": map[string]any{"id": input["id"]}, "userErrors": []any{}}})
	case (strings.Contains(request.Query, "MoonlightPublish") || strings.Contains(request.Query, "MoonlightUnpublish")) && f.resources[str(request.Variables["id"])] != nil:
		id := str(request.Variables["id"])
		external, _ := ParseGID(id, ResourceProduct)
		publish := strings.Contains(request.Query, "MoonlightPublish")
		if publish && f.failPublication {
			f.failPublication = false
			f.reply(w, map[string]any{"publishablePublish": map[string]any{"publishable": nil, "userErrors": []any{map[string]any{"message": "fixture publication refusal"}}}})
			return
		}
		f.h.published[external] = publish
		root := "publishablePublish"
		if !publish {
			root = "publishableUnpublish"
		}
		f.reply(w, map[string]any{root: map[string]any{"publishable": map[string]any{"id": id}, "userErrors": []any{}}})
	default:
		f.h.serve(w, r)
	}
}

func TestR3PublicBundleDurableLifecycle(t *testing.T) {
	database := testutil.Isolated(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { pool.Close() }()
	f := newBundleFixture(t)
	provider := func() *ShopifyProvider {
		p, e := NewShopifyProvider(testConfig(), harnessClient(t, f.h), postgres.NewDevices(pool, 5*time.Second))
		if e != nil {
			t.Fatal(e)
		}
		return p
	}
	p := provider()
	req := upsertRequest("019c0000-0000-7000-8000-000000000015", "FRAME-R3", true)
	req.Product.Configurations = []commerce.CommerceConfiguration{frameConfig("019c0000-0000-7000-8000-0000000000c1", "classic", "black", "Classic", "Black", 30000, nil, true), frameConfig("019c0000-0000-7000-8000-0000000000c2", "classic", "gold", "Classic", "Gold", 40000, nil, true)}
	configurations := req.Product.Configurations
	req.Product.Configurations = nil
	simple, err := p.UpsertProduct(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req.ExistingExternal = &commerce.ProviderProductRef{ExternalProductID: simple.ExternalProductID}
	req.Product.Configurations = configurations
	f.pendingReads = 18
	for i := 0; i < 3; i++ {
		_, e := p.UpsertProduct(ctx, req)
		if e == nil || !strings.Contains(e.Error(), "pending") {
			t.Fatalf("expected durable pending at %d: %v", i, e)
		}
		pool.Close()
		pool, err = pgxpool.New(ctx, database)
		if err != nil {
			t.Fatal(err)
		}
		p = provider()
	}
	if f.creates != 1 {
		t.Fatalf("create submissions=%d", f.creates)
	}
	result, err := p.UpsertProduct(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Configurations) != 3 || f.creates != 1 {
		t.Fatal("incomplete mapping or duplicate create")
	}
	req.ExistingExternal = &commerce.ProviderProductRef{ExternalProductID: result.ExternalProductID}
	req.Product.Configurations[0].Enabled = false
	result, err = p.UpsertProduct(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Configurations) != 2 {
		t.Fatal("disabled choice resurrected")
	}
	req.Product.Configurations[1].Enabled = false
	result, err = p.UpsertProduct(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Configurations) != 1 {
		t.Fatal("all disabled must retain only No Frame")
	}
	req.Product.Configurations[0].Enabled = true
	result, err = p.UpsertProduct(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Configurations) != 2 || f.creates != 1 {
		t.Fatal("reenable failed")
	}
	req.Product.Configurations = nil
	simpleAgain, err := p.UpsertProduct(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if simpleAgain.ExternalProductID != simple.ExternalProductID || !simpleAgain.SellableTransition {
		t.Fatal("framed to simple role transition failed")
	}
	req.ExistingExternal = &commerce.ProviderProductRef{ExternalProductID: simpleAgain.ExternalProductID}
	req.Product.Configurations = configurations
	if _, err = p.UpsertProduct(ctx, req); err != nil {
		t.Fatal(err)
	}
	if f.creates != 1 {
		t.Fatal("reframing created a replacement owned bundle")
	}
	t.Logf("one creation; %d settled updates; same receipt across reopened PostgreSQL pools", f.updates)
}

func (f *bundleFixture) variantIdentity(product, configuration string) string {
	key := product + "|" + configuration
	if id := f.variantIDs[key]; id != "" {
		return id
	}
	f.next++
	id := fmt.Sprintf("gid://shopify/ProductVariant/%d", f.next)
	f.variantIDs[key] = id
	return id
}
func metafieldIdentity(choice map[string]any) string {
	for _, raw := range choice["metafields"].(map[string]any)["nodes"].([]any) {
		field := raw.(map[string]any)
		if field["key"] == keyConfigurationIDMeta {
			return str(field["value"])
		}
	}
	return ""
}

func TestR3MaximumChoicesAndOwnedIdentity(t *testing.T) {
	database := testutil.Isolated(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	f := newBundleFixture(t)
	p, err := NewShopifyProvider(testConfig(), harnessClient(t, f.h), postgres.NewDevices(pool, 5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	req := upsertRequest("019c0000-0000-7000-8000-000000000016", "MAX-FRAME", true)
	req.Product.Prices[0].AmountMinor = 9007199254740993
	for i := 0; i < 100; i++ {
		req.Product.Configurations = append(req.Product.Configurations, frameConfig(fmt.Sprintf("019c0000-0000-7000-8000-%012d", i+1), "same", fmt.Sprintf("color-%d", i), "Shared", "Shared", int64(i), nil, true))
	}
	result, err := p.UpsertProduct(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Configurations) != 101 {
		t.Fatalf("maximum choices truncated: %d", len(result.Configurations))
	}
	for i, c := range req.Product.Configurations {
		if f.prices[result.Configurations[c.ConfigurationID]] != fmt.Sprintf("90071992547409.%02d", 93+i) {
			// Expected format uses integer money, including carry across cents.
			want, err := FormatMinorUnits(req.Product.Prices[0].AmountMinor + int64(i))
			if err != nil {
				t.Fatal(err)
			}
			if f.prices[result.Configurations[c.ConfigurationID]] != want {
				t.Fatal("configured price lost precision")
			}
		}
	}
	req.ExistingExternal = &commerce.ProviderProductRef{ExternalProductID: result.ExternalProductID}
	// Rename/reorder must retain every choice's provider identity.
	for i := range req.Product.Configurations {
		req.Product.Configurations[i].StyleNameAR = "جديد"
		req.Product.Configurations[i].StyleNameEN = nil
	}
	for i, j := 0, len(req.Product.Configurations)-1; i < j; i, j = i+1, j-1 {
		req.Product.Configurations[i], req.Product.Configurations[j] = req.Product.Configurations[j], req.Product.Configurations[i]
	}
	updated, err := p.UpsertProduct(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	for id, external := range result.Configurations {
		if updated.Configurations[id] != external {
			t.Fatal("rename/reorder changed stable identity")
		}
	}
	// Corrupt durable remote metadata: duplicate stamped IDs must stop all writes.
	bundleID := FormatGID(ResourceProduct, result.ExternalProductID)
	nodes := f.resources[bundleID]["variants"].(map[string]any)["nodes"].([]any)
	original := nodes[1].(map[string]any)["metafields"]
	nodes[1].(map[string]any)["metafields"] = nodes[0].(map[string]any)["metafields"]
	beforePrices, beforeUpdates := f.priceWrites, f.updates
	_, err = p.UpsertProduct(ctx, req)
	if err == nil || f.priceWrites != beforePrices || f.updates != beforeUpdates {
		t.Fatal("duplicate metadata permitted mutation")
	}
	nodes[1].(map[string]any)["metafields"] = original
	// Foreign relationship cannot be adopted even with matching labels.
	component := nodes[1].(map[string]any)["productVariantComponents"].(map[string]any)["nodes"].([]any)[1].(map[string]any)["productVariant"].(map[string]any)
	old := component["product"]
	component["product"] = map[string]any{"id": "gid://shopify/Product/999999"}
	var checkErr error
	err = postgres.NewDevices(pool, 5*time.Second).WithProductSync(ctx, p.Key(), req.ProductID, func(held context.Context) error {
		_, checkErr = p.establishBundleVariantIdentity(held, bundleID, req, chooseConfigurations(req.Product, "EGP"))
		return checkErr
	})
	if err == nil || f.priceWrites != beforePrices {
		t.Fatal("foreign relationship permitted pricing")
	}
	component["product"] = old
	t.Log("101 choices loaded through real pagination; stable identities after rename/reorder; duplicate metadata and foreign relationships refused")
}

func TestR3TwoWorkersAdoptPendingReceipt(t *testing.T) {
	database := testutil.Isolated(t)
	ctx := context.Background()
	a, err := pgxpool.New(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := pgxpool.New(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	f := newBundleFixture(t)
	f.pendingReads = 12
	req := upsertRequest("019c0000-0000-7000-8000-000000000017", "RACE-FRAME", true)
	req.Product.Configurations = []commerce.CommerceConfiguration{frameConfig("019c0000-0000-7000-8000-0000000000d1", "s", "c", "Style", "Color", 100, nil, true)}
	results := make(chan error, 2)
	start := make(chan struct{})
	for _, pool := range []*pgxpool.Pool{a, b} {
		p, e := NewShopifyProvider(testConfig(), harnessClient(t, f.h), postgres.NewDevices(pool, 5*time.Second))
		if e != nil {
			t.Fatal(e)
		}
		go func() { <-start; _, e := p.UpsertProduct(ctx, req); results <- e }()
	}
	close(start)
	for i := 0; i < 2; i++ {
		e := <-results
		if e == nil || !strings.Contains(e.Error(), "pending") {
			t.Fatal("both bounded attempts must remain pending")
		}
	}
	if f.creates != 1 {
		t.Fatalf("competing workers submitted %d creates", f.creates)
	}
	p, e := NewShopifyProvider(testConfig(), harnessClient(t, f.h), postgres.NewDevices(a, 5*time.Second))
	if e != nil {
		t.Fatal(e)
	}
	result, e := p.UpsertProduct(ctx, req)
	if e != nil || len(result.Configurations) != 2 {
		t.Fatalf("receipt did not settle: %v", e)
	}
	t.Log("two independent pools serialize one creation and adopt the same durable receipt")
}

func TestR3CurrencyEligibilityAndOverflow(t *testing.T) {
	database := testutil.Isolated(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	f := newBundleFixture(t)
	f.h.shopCurrency = "USD"
	cfg := testConfig()
	cfg.Currency = "USD"
	p, err := NewShopifyProvider(cfg, harnessClient(t, f.h), postgres.NewDevices(pool, 5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	req := upsertRequest("019c0000-0000-7000-8000-000000000019", "USD-FRAME", true)
	req.Product.Prices = []commerce.Money{{Currency: "EGP", AmountMinor: 100000}, {Currency: "USD", AmountMinor: 1000}}
	req.Product.Configurations = []commerce.CommerceConfiguration{frameConfig("019c0000-0000-7000-8000-0000000000e1", "s", "c", "Style", "Color", 30000, nil, true)}
	result, err := p.UpsertProduct(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Configurations) != 1 {
		t.Fatal("missing USD delta offered")
	}
	req.ExistingExternal = &commerce.ProviderProductRef{ExternalProductID: result.ExternalProductID}
	delta := int64(2500)
	req.Product.Configurations[0].PriceDeltaUSDMinor = &delta
	result, err = p.UpsertProduct(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Configurations) != 2 {
		t.Fatal("restored USD delta not offered")
	}
	if f.prices[result.Configurations[req.Product.Configurations[0].ConfigurationID]] != "35.00" {
		t.Fatal("USD price mixed with EGP")
	}
	delta = 9223372036854775807
	before := f.priceWrites
	if _, err = p.UpsertProduct(ctx, req); err == nil || f.priceWrites != before {
		t.Fatal("overflow must reject before price writes")
	}
}

func TestR3LostReceiptAndChangedIntent(t *testing.T) {
	for _, scenario := range []string{"lost-response", "changed-intent", "publication-refusal"} {
		t.Run(scenario, func(t *testing.T) {
			database := testutil.Isolated(t)
			ctx := context.Background()
			pool, err := pgxpool.New(ctx, database)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { pool.Close() }()
			f := newBundleFixture(t)
			provider := func() *ShopifyProvider {
				p, e := NewShopifyProvider(testConfig(), harnessClient(t, f.h), postgres.NewDevices(pool, 5*time.Second))
				if e != nil {
					t.Fatal(e)
				}
				return p
			}
			p := provider()
			req := upsertRequest("019c0000-0000-7000-8000-000000000041", "R3-RESUME", true)
			req.Product.Configurations = []commerce.CommerceConfiguration{frameConfig("019c0000-0000-7000-8000-000000000042", "same", "same", "Same", "Same", 1000, nil, true)}
			switch scenario {
			case "lost-response":
				f.dropCreate = true
			case "changed-intent":
				f.pendingReads = 6
			case "publication-refusal":
				f.failPublication = true
			}
			if _, e := p.UpsertProduct(ctx, req); e == nil {
				t.Fatal("fault/pending must not report convergence")
			}
			if f.creates != 1 {
				t.Fatal("creation never reached remote")
			}
			pool.Close()
			pool, err = pgxpool.New(ctx, database)
			if err != nil {
				t.Fatal(err)
			}
			p = provider()
			if scenario == "changed-intent" {
				req.Product.Configurations[0].PriceDeltaEGPMinor = 2500
			}
			result, e := p.UpsertProduct(ctx, req)
			if scenario == "lost-response" {
				if e == nil {
					t.Fatal("unknown side effect must retain barrier")
				}
				if f.creates != 1 {
					t.Fatal("lost acknowledgement caused resend")
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			if len(result.Configurations) != 2 || f.creates != 1 || len(f.h.products) != 1 || len(f.resources) != 2 {
				t.Fatal("resource graph or retry wrong")
			}
			if !f.h.published[result.ExternalProductID] {
				t.Fatal("sellable not published after retry")
			}
		})
	}
}

func TestR3IncompletePaginationRefusesPriceWrites(t *testing.T) {
	for _, mode := range []string{"truncated", "repeated-cursor"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			pool, e := pgxpool.New(ctx, testutil.Isolated(t))
			if e != nil {
				t.Fatal(e)
			}
			defer pool.Close()
			f := newBundleFixture(t)
			p, e := NewShopifyProvider(testConfig(), harnessClient(t, f.h), postgres.NewDevices(pool, 5*time.Second))
			if e != nil {
				t.Fatal(e)
			}
			req := upsertRequest("019c0000-0000-7000-8000-000000000051", "R3-PAGES", true)
			for i := 0; i < 100; i++ {
				req.Product.Configurations = append(req.Product.Configurations, frameConfig(fmt.Sprintf("019c0000-0000-7000-8000-%012d", 1000+i), "same", fmt.Sprintf("color-%d", i), "Same", "Same", int64(i), nil, true))
			}
			result, e := p.UpsertProduct(ctx, req)
			if e != nil {
				t.Fatal(e)
			}
			req.ExistingExternal = &commerce.ProviderProductRef{ExternalProductID: result.ExternalProductID}
			before := f.priceWrites
			if mode == "truncated" {
				f.truncateVariants = true
			} else {
				f.repeatCursor = true
			}
			if _, e = p.UpsertProduct(ctx, req); e == nil {
				t.Fatal("incomplete connection accepted")
			}
			if f.priceWrites != before {
				t.Fatal("price written without complete identity")
			}
		})
	}
}

type r3DesiredSource struct{ desired commerce.DesiredProduct }

func (s r3DesiredSource) GetDesiredCommerceProduct(context.Context, string) (commerce.DesiredProduct, error) {
	return s.desired, nil
}

func TestR3ProviderServiceErrorPrivacy(t *testing.T) {
	for _, boundary := range []string{"frame_component_set", "bundle_create", "bundle_update", "bundle_operation", "MoonlightVariantPrices", "MoonlightMetafieldsSet", "MoonlightProduct"} {
		for _, position := range []string{"beginning", "middle", "end"} {
			t.Run(boundary+"/"+position, func(t *testing.T) {
				ctx := context.Background()
				pool, e := pgxpool.New(ctx, testutil.Isolated(t))
				if e != nil {
					t.Fatal(e)
				}
				defer pool.Close()
				d := postgres.NewDevices(pool, 5*time.Second)
				f := newBundleFixture(t)
				p, e := NewShopifyProvider(testConfig(), harnessClient(t, f.h), d)
				if e != nil {
					t.Fatal(e)
				}
				req := upsertRequest("019c0000-0000-7000-8000-000000000071", "R3-PRIVACY", true)
				req.Product.Configurations = []commerce.CommerceConfiguration{frameConfig("019c0000-0000-7000-8000-000000000072", "classic", "black", "Classic", "Black", 1000, nil, true)}
				registry := commerce.NewRegistry()
				if e = registry.Register(p.Key(), p); e != nil {
					t.Fatal(e)
				}
				source := r3DesiredSource{commerce.DesiredProduct{Product: req.Product, Published: true}}
				var logs bytes.Buffer
				service := commerce.NewCommerceService(registry, d, source, slog.New(slog.NewTextHandler(&logs, nil)))
				if boundary == "bundle_update" || boundary == "MoonlightProduct" {
					if _, e = service.SyncProduct(ctx, string(p.Key()), req.ProductID); e != nil {
						t.Fatal(e)
					}
					changed := "Changed"
					source.desired.Product.Configurations[0].StyleNameEN = &changed
				}
				secret := f.h.token + " " + f.h.secret
				pad := strings.Repeat("界", 2000)
				message := secret + pad
				if position == "middle" {
					message = pad + secret + pad
				}
				if position == "end" {
					message = pad + secret
				}
				f.injectOperation = boundary
				f.injectMessage = "privacy-boundary: " + message
				_, e = service.SyncProduct(ctx, string(p.Key()), req.ProductID)
				if e == nil || !strings.Contains(e.Error(), "privacy-boundary") {
					t.Fatal("expected provider boundary not reached")
				}
				// This is the same safe error passed to CLI output; inspect the
				// formatted boundary and service logs without printing secrets.
				output := fmt.Sprintln(e) + logs.String()
				if strings.Contains(output, f.h.token) || strings.Contains(output, f.h.secret) || len(e.Error()) > 512 || !utf8.ValidString(e.Error()) {
					t.Fatal("provider secret/size/UTF8 contract failed")
				}
			})
		}
	}
}

func TestR3MappingFailureAdoptsReceipt(t *testing.T) {
	ctx := context.Background()
	database := testutil.Isolated(t)
	pool, e := pgxpool.New(ctx, database)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { pool.Close() }()
	f := newBundleFixture(t)
	req := upsertRequest("019c0000-0000-7000-8000-000000000081", "R3-MAP-FAULT", true)
	req.Product.Configurations = []commerce.CommerceConfiguration{frameConfig("019c0000-0000-7000-8000-000000000082", "classic", "black", "Classic", "Black", 1000, nil, true)}
	makeService := func() *commerce.CommerceService {
		d := postgres.NewDevices(pool, 5*time.Second)
		p, e := NewShopifyProvider(testConfig(), harnessClient(t, f.h), d)
		if e != nil {
			t.Fatal(e)
		}
		registry := commerce.NewRegistry()
		if e = registry.Register(p.Key(), p); e != nil {
			t.Fatal(e)
		}
		return commerce.NewCommerceService(registry, d, r3DesiredSource{commerce.DesiredProduct{Product: req.Product, Published: true}}, nil)
	}
	if _, e = pool.Exec(ctx, `CREATE FUNCTION r3_mapping_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture'; END $$; CREATE TRIGGER r3_mapping_fault BEFORE INSERT ON commerce_product_mappings FOR EACH ROW EXECUTE FUNCTION r3_mapping_fault()`); e != nil {
		t.Fatal(e)
	}
	if _, e = makeService().SyncProduct(ctx, string(testConfig().ProviderKey), req.ProductID); e == nil {
		t.Fatal("mapping fault not reached")
	}
	if f.creates != 1 {
		t.Fatal("bundle creation did not precede mapping fault")
	}
	if _, e = pool.Exec(ctx, `DROP TRIGGER r3_mapping_fault ON commerce_product_mappings; DROP FUNCTION r3_mapping_fault()`); e != nil {
		t.Fatal(e)
	}
	pool.Close()
	pool, e = pgxpool.New(ctx, database)
	if e != nil {
		t.Fatal(e)
	}
	result, e := makeService().SyncProduct(ctx, string(testConfig().ProviderKey), req.ProductID)
	if e != nil {
		t.Fatal(e)
	}
	if result.ExternalProductID == "" || f.creates != 1 || len(f.h.products) != 1 || len(f.resources) != 2 {
		t.Fatal("mapping restart duplicated or lost owned graph")
	}
}

type r3ProcessTransport struct {
	target string
	base   http.RoundTripper
}

func (r r3ProcessTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL = mustParseURL(r.target + req.URL.RequestURI())
	clone.Host = clone.URL.Host
	return r.base.RoundTrip(clone)
}
func TestR3ReceiptProcessHelper(t *testing.T) {
	if os.Getenv("R3_RECEIPT_CHILD") != "1" {
		t.Skip("separate-process helper only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, e := pgxpool.New(ctx, os.Getenv("R3_RECEIPT_DATABASE"))
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	certificate, e := base64.StdEncoding.DecodeString(os.Getenv("R3_RECEIPT_CERT"))
	if e != nil {
		t.Fatal(e)
	}
	cert, e := x509.ParseCertificate(certificate)
	if e != nil {
		t.Fatal(e)
	}
	trust := x509.NewCertPool()
	trust.AddCert(cert)
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: trust, MinVersion: tls.VersionTLS12}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: r3ProcessTransport{os.Getenv("R3_RECEIPT_URL"), transport}, Timeout: 10 * time.Second}
	d := postgres.NewDevices(pool, 5*time.Second)
	p, e := NewShopifyProvider(testConfig(), client, d)
	if e != nil {
		t.Fatal(e)
	}
	req := upsertRequest("019c0000-0000-7000-8000-000000000091", "R3-PROCESS", true)
	req.Product.Configurations = []commerce.CommerceConfiguration{frameConfig("019c0000-0000-7000-8000-000000000093", "classic", "black", "Classic", "Black", 1000, nil, true)}
	registry := commerce.NewRegistry()
	if e = registry.Register(p.Key(), p); e != nil {
		t.Fatal(e)
	}
	service := commerce.NewCommerceService(registry, d, r3DesiredSource{commerce.DesiredProduct{Product: req.Product, Published: true}}, nil)
	result, e := service.SyncProduct(ctx, string(p.Key()), req.ProductID)
	if os.Getenv("R3_RECEIPT_EXPECT") == "pending" {
		if e == nil || !strings.Contains(e.Error(), "pending") {
			t.Fatal("pending receipt not reached")
		}
		return
	}
	if e != nil || result.ExternalProductID == "" {
		t.Fatal("receipt did not converge after process restart", e)
	}
}
func TestR3ReceiptSurvivesSeparateProcesses(t *testing.T) {
	database := testutil.Isolated(t)
	f := newBundleFixture(t)
	f.mu.Lock()
	f.pendingReads = 6
	f.mu.Unlock()
	binary, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	for _, expect := range []string{"pending", "completed"} {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		command := exec.CommandContext(ctx, binary, "-test.run=^TestR3ReceiptProcessHelper$", "-test.v")
		command.Env = append(os.Environ(), "R3_RECEIPT_CHILD=1", "R3_RECEIPT_DATABASE="+database, "R3_RECEIPT_CERT="+base64.StdEncoding.EncodeToString(f.h.server.Certificate().Raw), "R3_RECEIPT_URL="+f.h.server.URL, "R3_RECEIPT_EXPECT="+expect)
		output, e := command.CombinedOutput()
		cancel()
		if e != nil {
			t.Fatalf("child %s failed: %s", expect, output)
		}
		f.mu.Lock()
		creates := f.creates
		f.mu.Unlock()
		if creates != 1 {
			t.Fatal("process restart resubmitted acknowledged operation")
		}
	}
	pool, e := pgxpool.New(context.Background(), database)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	var pending, completed, mappings int
	e = pool.QueryRow(context.Background(), `SELECT count(*) FILTER(WHERE async_state='pending'),count(*) FILTER(WHERE async_state='completed') FROM commerce_product_mutation_barriers WHERE async_role='bundle_create'`).Scan(&pending, &completed)
	if e != nil || pending != 0 || completed != 1 {
		t.Fatal("durable receipt verdict", e)
	}
	if e = pool.QueryRow(context.Background(), "SELECT count(*) FROM commerce_product_mappings").Scan(&mappings); e != nil || mappings != 1 {
		t.Fatal("process restart mapping", e)
	}
	t.Log("two real OS processes, one acknowledged operation, one completed receipt, one mapping, no duplicate resource creation")
}
