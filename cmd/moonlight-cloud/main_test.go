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
