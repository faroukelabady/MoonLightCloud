package shopify

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"github.com/faroukelabady/MoonLightCloud/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

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
				p, e := NewShopifyProvider(testConfig(), harnessClient(t, f.h), postgres.NewCommercePersistence(pool, 5*time.Second))
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
			p, e := NewShopifyProvider(testConfig(), harnessClient(t, f.h), postgres.NewCommercePersistence(pool, 5*time.Second))
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

// seedR3Product projects a minimal Store-owned catalog product so commerce
// mappings can resolve the product's Store (ADR-0055: no unscoped rows).
func seedR3Product(t *testing.T, pool *pgxpool.Pool, productID string) {
	t.Helper()
	if _, e := pool.Exec(context.Background(), `
		WITH s AS (INSERT INTO stores (id, display_name, timezone) VALUES (gen_random_uuid(), 'R3', 'Africa/Cairo') RETURNING id),
		d AS (INSERT INTO devices (id, name) VALUES (gen_random_uuid(), 'r3-device') RETURNING id),
		b AS (INSERT INTO device_store_bindings (device_id, store_id) SELECT d.id, s.id FROM d, s RETURNING device_id, store_id),
		e AS (INSERT INTO sync_events (event_id, device_id, event_type, occurred_at, payload, payload_hash, store_id)
			SELECT gen_random_uuid(), b.device_id, 'catalog.product.snapshot.v2', now(), '{}', '\x00', b.store_id FROM b
			RETURNING event_id, device_id, store_id),
		c AS (INSERT INTO catalog_categories (category_id, status, name_ar, source_revision, source_event_id, source_device_id, source_payload_hash, source_received_at, store_id)
			SELECT gen_random_uuid(), 'active', 'Root', 1, e.event_id, e.device_id, '\x00', now(), e.store_id FROM e
			RETURNING category_id)
		INSERT INTO catalog_products (product_id, name_ar, top_category_id, is_active, source_revision, source_event_id, source_device_id, source_payload_hash, source_received_at, store_id)
		SELECT $1, 'R3', c.category_id, true, 1, e.event_id, e.device_id, '\x00', now(), e.store_id FROM c, e`, productID); e != nil {
		t.Fatalf("seed product: %v", e)
	}
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
				d := postgres.NewCommercePersistence(pool, 5*time.Second)
				f := newBundleFixture(t)
				p, e := NewShopifyProvider(testConfig(), harnessClient(t, f.h), d)
				if e != nil {
					t.Fatal(e)
				}
				req := upsertRequest("019c0000-0000-7000-8000-000000000071", "R3-PRIVACY", true)
				seedR3Product(t, pool, req.ProductID)
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
	seedR3Product(t, pool, req.ProductID)
	req.Product.Configurations = []commerce.CommerceConfiguration{frameConfig("019c0000-0000-7000-8000-000000000082", "classic", "black", "Classic", "Black", 1000, nil, true)}
	makeService := func() *commerce.CommerceService {
		d := postgres.NewCommercePersistence(pool, 5*time.Second)
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
	d := postgres.NewCommercePersistence(pool, 5*time.Second)
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
	seedPool, e := pgxpool.New(context.Background(), database)
	if e != nil {
		t.Fatal(e)
	}
	seedR3Product(t, seedPool, "019c0000-0000-7000-8000-000000000091")
	seedPool.Close()
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
