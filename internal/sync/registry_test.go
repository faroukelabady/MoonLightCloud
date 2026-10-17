package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
)

type registryRepo struct{ writes atomic.Int64 }

func (r *registryRepo) IngestBatch(_ context.Context, _, _ string, events []Event, _ time.Time) ([]EventResult, error) {
	r.writes.Add(1)
	return []EventResult{{EventID: events[0].EventID, Status: StatusAccepted}}, nil
}

func registryBody(eventType string) []byte {
	return []byte(batchJSON(eventJSON("22222222-2222-7222-8222-222222222222", eventType, "2026-09-19T10:20:30Z", `{}`)))
}

func TestRegistryCopiesDefinitionsAndKeepsValidatorsLocal(t *testing.T) {
	rejected := errors.New("validator rejected")
	definitions := map[string]Validator{"local.test.v1": func(json.RawMessage) error { return rejected }}
	firstRepo, secondRepo := &registryRepo{}, &registryRepo{}
	first := NewService(firstRepo, clock.Fixed{T: testNow}, NewRegistry(definitions))
	definitions["local.test.v1"] = func(json.RawMessage) error { return nil }
	definitions["another.test.v1"] = func(json.RawMessage) error { return nil }
	second := NewService(secondRepo, clock.Fixed{T: testNow}, NewRegistry(definitions))
	if _, err := first.Ingest(context.Background(), testDevice, "", registryBody("local.test.v1")); !errors.Is(err, rejected) {
		t.Fatalf("caller changed the first validator: %v", err)
	}
	if _, err := first.Ingest(context.Background(), testDevice, "", registryBody("another.test.v1")); err == nil {
		t.Fatal("new definition contaminated an existing service")
	}
	if _, err := second.Ingest(context.Background(), testDevice, "", registryBody("local.test.v1")); err != nil {
		t.Fatal(err)
	}
	if firstRepo.writes.Load() != 0 || secondRepo.writes.Load() != 1 {
		t.Fatal("validation must precede durable acceptance")
	}
	caps := second.Capabilities()
	caps.SupportedEvents[0] = "changed"
	if second.Capabilities().SupportedEvents[0] != "another.test.v1" {
		t.Fatal("capability response exposes registry storage")
	}
}

func TestRegistryConcurrentServiceConstructionAndUse(t *testing.T) {
	var workers sync.WaitGroup
	failures := make(chan error, 32)
	for n := range 32 {
		workers.Add(1)
		go func(n int) {
			defer workers.Done()
			name := fmt.Sprintf("worker%d.test.v1", n)
			repo := &registryRepo{}
			service := NewService(repo, clock.Fixed{T: testNow}, NewRegistry(map[string]Validator{name: func(json.RawMessage) error { return nil }}))
			for range 20 {
				if names := service.Capabilities().SupportedEvents; len(names) != 1 || names[0] != name {
					failures <- fmt.Errorf("capabilities contaminated: %v", names)
					return
				}
				if _, err := service.Ingest(context.Background(), testDevice, "", registryBody(name)); err != nil {
					failures <- err
					return
				}
			}
		}(n)
	}
	workers.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
}

func TestRegistryRefusesMissingBusinessValidator(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("an unvalidated business capability must fail at construction")
		}
	}()
	NewRegistry(map[string]Validator{"business.event.v1": nil})
}
