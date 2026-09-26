package postgres

import (
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
)

// Pure catalog-projection helper tests (no DB): DAG defense, reachability,
// and revision disposition must fail closed.

func TestCheckCategoryGraphValid(t *testing.T) {
	edges := []catalog.Edge{
		{ParentID: "root-a", ChildID: "sub"},
		{ParentID: "root-b", ChildID: "sub"},
		{ParentID: "sub", ChildID: "leaf"},
	}
	// Shared subcategory keeps both parents; depth root→sub→leaf is 3 nodes.
	if err := checkCategoryGraph(edges, "sub", []string{"root-a", "root-b"}); err != nil {
		t.Fatalf("shared parents: %v", err)
	}
	// Parent replacement converges exactly.
	if err := checkCategoryGraph(edges, "sub", []string{"root-b"}); err != nil {
		t.Fatalf("parent removal: %v", err)
	}
	// New root child is fine.
	if err := checkCategoryGraph(edges, "new", []string{"root-a"}); err != nil {
		t.Fatalf("new child: %v", err)
	}
}

func TestCheckCategoryGraphRejects(t *testing.T) {
	edges := []catalog.Edge{{ParentID: "root", ChildID: "sub"}}
	if err := checkCategoryGraph(edges, "sub", []string{"sub"}); err == nil {
		t.Fatal("self edge must fail")
	}
	// Cycle: sub becomes its own ancestor.
	if err := checkCategoryGraph(edges, "root", []string{"sub"}); err == nil {
		t.Fatal("cycle must fail")
	}
	// Depth: root→a→b exists; adding b→c makes a 4-node path.
	deep := []catalog.Edge{
		{ParentID: "root", ChildID: "a"},
		{ParentID: "a", ChildID: "b"},
	}
	if err := checkCategoryGraph(deep, "c", []string{"b"}); err == nil {
		t.Fatal("4-node path must fail")
	}
	// Exactly 3 nodes stays valid.
	if err := checkCategoryGraph(deep[:1], "c", []string{"a"}); err != nil {
		t.Fatalf("3-node path valid: %v", err)
	}
}

func TestReachableFromTop(t *testing.T) {
	edges := []catalog.Edge{
		{ParentID: "root-a", ChildID: "sub"},
		{ParentID: "root-b", ChildID: "sub"},
		{ParentID: "sub", ChildID: "leaf"},
	}
	reached := reachableFromTop(edges, "root-a")
	if !reached["sub"] || !reached["leaf"] {
		t.Fatalf("descendants: %v", reached)
	}
	if reached["root-a"] || reached["root-b"] {
		t.Fatalf("top and siblings excluded: %v", reached)
	}
	if hasParents(edges, "root-a") || !hasParents(edges, "sub") {
		t.Fatal("parent detection")
	}
}

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
