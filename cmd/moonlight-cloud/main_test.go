package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// TestRunServerBindFailure occupies a port, then asserts orchestration
// returns the error promptly: no os.Exit, no deadlock, cleanup runs.
func TestRunServerBindFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	addr := ln.Addr().String()

	cleaned := atomic.Bool{}
	srv := &http.Server{Addr: addr, Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {})}
	done := make(chan error, 1)
	go func() {
		defer func() { cleaned.Store(true) }()
		done <- runServer(context.Background(), srv, 2*time.Second, slog.Default())
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("bind failure must return an error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runServer deadlocked on bind failure")
	}
	if !cleaned.Load() {
		t.Fatal("deferred cleanup must run on bind failure")
	}
}

// TestRunServerCleanShutdown cancels context and expects nil: graceful path.
func TestRunServerCleanShutdown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	srv := &http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", port)}
	done := make(chan error, 1)
	go func() { done <- runServer(ctx, srv, 2*time.Second, slog.Default()) }()
	time.Sleep(300 * time.Millisecond) // let ListenAndServe start, no sleep in assertions
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("clean shutdown must return nil: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runServer deadlocked on shutdown")
	}
}

// TestProjectionProcessorRegistry proves the canonical registry covers
// every registered projector (three frozen sale versions, return, 5
// catalog, inventory) and that retry validation accepts exactly those:
// the Phase 5B Low where the policy processor was visible in status but
// rejected by retry can never recur for policy, inventory, configuration,
// or future processors. (Phase 17-R0 closes the same gap for the v2/v3
// sale processors, which were missing from the registry.)
func TestProjectionProcessorRegistry(t *testing.T) {
	processors := allProjectionProcessors()
	if len(processors) != 13 {
		t.Fatalf("want 13 processors, got %d: %v", len(processors), processors)
	}
	seen := map[string]bool{}
	for _, processor := range processors {
		if seen[processor] {
			t.Fatalf("duplicate processor %q", processor)
		}
		seen[processor] = true
		if !validProjectionProcessor(processor) {
			t.Fatalf("registered processor %q rejected by retry validation", processor)
		}
	}
	for _, want := range []string{
		"sale_projection.v2",
		"sale_projection.v3",
		"catalog_product_sales_policy_projection.v1",
		"inventory_product_projection.v1",
		"catalog_product_configuration_projection.v1",
		"catalog_product_variant_projection.v1",
		"inventory_product_variant_projection.v1",
	} {
		if !seen[want] {
			t.Fatalf("registry missing %q", want)
		}
	}
	for _, unknown := range []string{"", "sale_projection.v4", "catalog_product_projection.v1 "} {
		if validProjectionProcessor(unknown) {
			t.Fatalf("unknown processor %q accepted", unknown)
		}
	}
}
