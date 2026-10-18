package postgres

import "testing"

func TestRevisionGate(t *testing.T) {
	if proceed, stale := revisionGate(5, 4); !proceed || stale {
		t.Fatal("higher revision proceeds")
	}
	if proceed, stale := revisionGate(4, 5); proceed || !stale {
		t.Fatal("stale revision is a terminal no-op")
	}
	if proceed, stale := revisionGate(5, 5); proceed || stale {
		t.Fatal("equal revision needs reconstruction comparison")
	}
}
