package postgres

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
)

// Explicit opt-in: actual OCI CLI/serve processes, PostgreSQL and local TLS.
// The CONNECT proxy changes routing only; the production HTTPS authority and
// certificate verification stay intact. No request can reach a live shop.
func TestR1ShopifyOCIRuntime(t *testing.T) {
	image := os.Getenv("MOONLIGHT_SHOPIFY_RUNTIME_IMAGE")
	if image == "" {
		t.Skip("MOONLIGHT_SHOPIFY_RUNTIME_IMAGE unset: actual OCI runtime is opt-in")
	}
	env := openSaleEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	productID := commerceFixture(t, env, 0x1191, "r1oci", "PAP-R1OCI", 10)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	cert, key := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if out, err := exec.CommandContext(ctx, "openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", key, "-out", cert, "-days", "1", "-subj", "/CN=test.myshopify.com", "-addext", "subjectAltName=DNS:test.myshopify.com").CombinedOutput(); err != nil {
		t.Fatalf("certificate: %s %v", out, err)
	}
	if err := os.Chmod(cert, 0644); err != nil {
		t.Fatal(err)
	}
	port := func() string {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		ln.Close()
		return strings.Split(addr, ":")[1]
	}
	remotePort := port()
	remoteAddr := "127.0.0.1:" + remotePort
	fixtureLog, err := os.Create(filepath.Join(dir, "fixture.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer fixtureLog.Close()
	fixture := exec.CommandContext(ctx, "python3", "testdata/shopify_runtime.py", cert, key, remotePort)
	fixture.Stdout = fixtureLog
	fixture.Stderr = fixtureLog
	if err := fixture.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fixture.Process.Kill(); _ = fixture.Wait() }()
	localClient := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} // local fixture control only
	state := func() map[string]any {
		r, err := localClient.Get("https://" + remoteAddr + "/state")
		if err != nil {
			return nil
		}
		defer r.Body.Close()
		var s map[string]any
		_ = json.NewDecoder(r.Body).Decode(&s)
		return s
	}
	waitFor(t, 5*time.Second, func() bool { return state() != nil }, "TLS fixture")
	control := func(body string) {
		r, err := localClient.Post("https://"+remoteAddr+"/control", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
	}
	var proxyHits atomic.Int64
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		if r.Method != "CONNECT" || r.Host != "test.myshopify.com:443" {
			http.Error(w, "test authority only", 403)
			return
		}
		upstream, err := net.DialTimeout("tcp", remoteAddr, time.Second)
		if err != nil {
			http.Error(w, "local target unavailable", 502)
			return
		}
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			upstream.Close()
			return
		}
		_, _ = rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = rw.Flush()
		go func() { defer upstream.Close(); _, _ = io.Copy(upstream, rw) }()
		go func() { defer conn.Close(); _, _ = io.Copy(conn, upstream) }()
	}))
	defer proxy.Close()
	addr := "127.0.0.1:" + port()
	settings := map[string]string{
		"DATABASE_URL": env.pool.Config().ConnConfig.ConnString(), "ENVIRONMENT": "development", "ALLOW_UNAUTHENTICATED_REPORTING": "true", "HTTP_ADDR": addr,
		"COMMERCE_SHOPIFY_ENABLED": "true", "COMMERCE_SHOPIFY_PROVIDER_KEY": "shopify-main", "COMMERCE_SHOPIFY_SHOP_DOMAIN": "test.myshopify.com", "COMMERCE_SHOPIFY_API_VERSION": "2026-10", "COMMERCE_SHOPIFY_ACCESS_TOKEN": "local-runtime-token", "COMMERCE_SHOPIFY_CLIENT_SECRET": "local-runtime-hmac-secret", "COMMERCE_SHOPIFY_CURRENCY": "EGP", "COMMERCE_SHOPIFY_LOCATION_ID": "gid://shopify/Location/10", "COMMERCE_SHOPIFY_PUBLICATION_ID": "gid://shopify/Publication/11", "COMMERCE_SHOPIFY_ORDERS_ENABLED": "true", "COMMERCE_SHOPIFY_HTTP_TIMEOUT": "2s", "HTTPS_PROXY": proxy.URL, "NO_PROXY": "", "SSL_CERT_FILE": "/local-cert.pem",
	}
	// Test-only trust configuration. Bind-mounted files are inaccessible to
	// the rootless image's nonroot UID in this environment, so copy the CA into
	// a disposable derivative image. The candidate binary remains byte-identical.
	baseImage := image
	image = fmt.Sprintf("localhost/moonlight-11r1-trust-%d", time.Now().UnixNano())
	containerfile := filepath.Join(dir, "Containerfile")
	if err := os.WriteFile(containerfile, []byte("FROM "+baseImage+"\nCOPY --chmod=0644 cert.pem /local-cert.pem\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.CommandContext(ctx, "podman", "build", "-f", containerfile, "-t", image, dir).CombinedOutput(); err != nil {
		t.Fatalf("test CA image: %s %v", out, err)
	}
	defer func() { _ = exec.Command("podman", "rmi", image).Run() }()
	args := []string{"run", "--network=host"}
	for k, v := range settings {
		args = append(args, "--env", k+"="+v)
	}
	run := func(success bool, command ...string) {
		a := append([]string{}, args...)
		a = append(a, "--rm", image)
		a = append(a, command...)
		out, err := exec.CommandContext(ctx, "podman", a...).CombinedOutput()
		if (err == nil) != success {

			t.Fatalf("image %v: %s %v; fixture=%s proxy=%d state=%+v", command, out, err, func() string { b, _ := os.ReadFile(filepath.Join(dir, "fixture.log")); return string(b) }(), proxyHits.Load(), state())
		}
	}
	syncProduct := func(success bool) {
		run(success, "commerce", "sync-product", "--provider", "shopify-main", "--product", productID)
	}
	control(`{"drop_create":true}`)
	syncProduct(false)
	syncProduct(true)
	check := func(qty float64, published bool) {
		s := state()
		if s["quantity"] != qty || s["published"] != published || s["creations"] != float64(1) || s["unsafe_updates"] != float64(0) {
			t.Fatalf("physical state: %+v", s)
		}
	}
	check(10, true)
	var mapping string
	if err := env.pool.QueryRow(ctx, `SELECT external_product_id FROM commerce_product_mappings WHERE provider_key='shopify-main' AND product_id=$1`, productID).Scan(&mapping); err != nil || mapping != "500" {
		t.Fatalf("mapping %q %v", mapping, err)
	}
	tag, err := env.pool.Exec(ctx, `DELETE FROM commerce_product_mappings WHERE provider_key='shopify-main' AND product_id=$1`, productID)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatal("mapping loss fixture", err)
	}
	syncProduct(true)
	check(10, true)
	control(`{"quantity":20}`)
	syncProduct(true)
	check(10, true)
	control(`{"quantity":20,"fail_update":true}`)
	syncProduct(false)
	check(0, true)
	control(`{"quantity":30,"fail_update":false}`)
	syncProduct(true)
	check(10, true)
	for _, v := range []struct {
		revision int64
		online   bool
	}{{2, false}, {3, true}} {
		event := commerceIDs(t, int(v.revision)+0x11900, "policy")["policy"]
		ingestCatalog(t, env, event, catalog.EventProductSalesPolicySnapshotV1, "2026-09-20T12:03:00Z", policyPayload(productID, v.revision, true, v.online, nil))
		projectCatalogOnce(t, env, event)
		syncProduct(true)
		if v.online {
			check(10, true)
		} else {
			check(0, false)
		}
	}
	name := fmt.Sprintf("moonlight-11r1-runtime-%d", time.Now().UnixNano())
	stop := func() { _ = exec.Command("podman", "rm", "-f", name).Run() }
	defer stop()
	start := func() {
		a := append([]string{}, args...)
		a = append(a, "-d", "--name", name, image, "serve")
		if out, err := exec.CommandContext(ctx, "podman", a...).CombinedOutput(); err != nil {
			t.Fatalf("serve: %s %v", out, err)
		}
		waitFor(t, 15*time.Second, func() bool {
			r, e := http.Get("http://" + addr + "/health/ready")
			if e != nil {
				return false
			}
			defer r.Body.Close()
			return r.StatusCode == 200
		}, "actual Cloud ready")
	}
	control(`{"fail_order":true}`)
	start()
	body := []byte(`{"id":802,"email":"private-r1-runtime@example.com"}`)
	webhook := func() int {
		mac := hmac.New(sha256.New, []byte(settings["COMMERCE_SHOPIFY_CLIENT_SECRET"]))
		mac.Write(body)
		r, _ := http.NewRequestWithContext(ctx, "POST", "http://"+addr+"/api/v1/commerce/webhooks/shopify/shopify-main", bytes.NewReader(body))
		r.Header.Set("X-Shopify-Hmac-Sha256", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
		r.Header.Set("X-Shopify-Shop-Domain", "test.myshopify.com")
		r.Header.Set("X-Shopify-Topic", "orders/updated")
		r.Header.Set("X-Shopify-Webhook-Id", "r1-runtime-delivery")
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if got := webhook(); got != 202 {
		t.Fatal("signed webhook", got)
	}
	waitFor(t, 15*time.Second, func() bool {
		var status string
		err := env.pool.QueryRow(ctx, `SELECT status FROM commerce_online_order_webhook_events WHERE delivery_id='r1-runtime-delivery'`).Scan(&status)
		return err == nil && status == "retry"
	}, "durable retry")
	logs, _ := exec.Command("podman", "logs", name).CombinedOutput()
	stop()
	control(`{"fail_order":false}`)
	tag, err = env.pool.Exec(ctx, `UPDATE commerce_online_order_webhook_events SET next_attempt_at=now()-interval '1 minute' WHERE delivery_id='r1-runtime-delivery' AND status='retry'`)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatal("retry eligibility fixture", err)
	}
	start()
	waitFor(t, 20*time.Second, func() bool {
		var status string
		err := env.pool.QueryRow(ctx, `SELECT status FROM commerce_online_order_webhook_events WHERE delivery_id='r1-runtime-delivery'`).Scan(&status)
		return err == nil && status == "processed"
	}, "restart terminal processing")
	if got := webhook(); got != 202 {
		t.Fatal("restart replay", got)
	}
	var count int
	var minor int64
	if err := env.pool.QueryRow(ctx, `SELECT count(*),max(total_minor) FROM commerce_online_order_lines WHERE provider_key='shopify-main' AND external_order_id='802'`).Scan(&count, &minor); err != nil || count != 51 || minor != 9007199254740993 {
		t.Fatalf("runtime full snapshot %d %d %v", count, minor, err)
	}
	if err := env.pool.QueryRow(ctx, `SELECT count(*) FROM commerce_online_order_webhook_events WHERE delivery_id='r1-runtime-delivery'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate durable inbox", count, err)
	}
	more, _ := exec.Command("podman", "logs", name).CombinedOutput()
	logs = append(logs, more...)
	for _, secret := range []string{"local-runtime-token", "local-runtime-hmac-secret", "private-r1-runtime@example.com"} {
		if bytes.Contains(logs, []byte(secret)) {
			t.Fatal("runtime privacy leak")
		}
	}
	t.Log("OCI CLI: create response loss, paginated recovery, mapping loss, safe-zero failure, physical drift, publish/unpublish PASS; actual serve restart: signed inbox retry -> 51-line processed snapshot, exact money, replay one row, privacy PASS")
}
