package projection

import (
	"testing"
	"time"
)

func TestBackoff(t *testing.T) {
	if Backoff(0) != 5*time.Second {
		t.Fatalf("attempt 0: %v", Backoff(0))
	}
	if Backoff(10) != time.Hour {
		t.Fatalf("cap: %v", Backoff(10))
	}
	if Backoff(2) != 20*time.Second {
		t.Fatalf("attempt 2: %v", Backoff(2))
	}
}
