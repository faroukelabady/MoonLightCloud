package catalog

import (
	"errors"
	"reflect"
	"testing"
)

// Pure catalog-projection helper tests (no DB): DAG defense, reachability,
// and revision disposition must fail closed.

func TestCheckCategoryGraphValid(t *testing.T) {
	edges := []Edge{
		{ParentID: "root-a", ChildID: "sub"},
		{ParentID: "root-b", ChildID: "sub"},
		{ParentID: "sub", ChildID: "leaf"},
	}
	// Shared subcategory keeps both parents; depth root→sub→leaf is 3 nodes.
	if err := CheckCategoryGraph(edges, "sub", []string{"root-a", "root-b"}); err != nil {
		t.Fatalf("shared parents: %v", err)
	}
	// Parent replacement converges exactly.
	if err := CheckCategoryGraph(edges, "sub", []string{"root-b"}); err != nil {
		t.Fatalf("parent removal: %v", err)
	}
	// New root child is fine.
	if err := CheckCategoryGraph(edges, "new", []string{"root-a"}); err != nil {
		t.Fatalf("new child: %v", err)
	}
}

func TestReachableFromTop(t *testing.T) {
	edges := []Edge{
		{ParentID: "root-a", ChildID: "sub"},
		{ParentID: "root-b", ChildID: "sub"},
		{ParentID: "sub", ChildID: "leaf"},
	}
	reached := ReachableFromTop(edges, "root-a")
	if !reached["sub"] || !reached["leaf"] {
		t.Fatalf("descendants: %v", reached)
	}
	if reached["root-a"] || reached["root-b"] {
		t.Fatalf("top and siblings excluded: %v", reached)
	}
	if HasParents(edges, "root-a") || !HasParents(edges, "sub") {
		t.Fatal("parent detection")
	}
}

func TestCategoryGraphViolationIdentity(t *testing.T) {
	edges := []Edge{{ParentID: "root", ChildID: "sub"}, {ParentID: "sub", ChildID: "leaf"}}
	for _, tc := range []struct {
		name    string
		child   string
		parents []string
		want    error
	}{
		{"self", "sub", []string{"sub"}, ErrGraphCycle},
		{"ancestor cycle", "root", []string{"leaf"}, ErrGraphCycle},
		{"four nodes", "deep", []string{"leaf"}, ErrGraphDepth},
		{"replacement removes old edge", "leaf", []string{"root"}, nil},
		{"duplicate parent is one edge", "leaf", []string{"sub", "sub"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := CheckCategoryGraph(edges, tc.child, tc.parents); !errors.Is(got, tc.want) {
				t.Fatalf("graph error = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCategoryGraphTraversalPreservesInputs(t *testing.T) {
	// Shared descendants appear once; the other root is not a descendant.
	edges := []Edge{
		{ParentID: "root", ChildID: "b"},
		{ParentID: "root", ChildID: "a"},
		{ParentID: "a", ChildID: "leaf"},
		{ParentID: "b", ChildID: "leaf"},
		{ParentID: "other", ChildID: "leaf"},
	}
	original := append([]Edge(nil), edges...)
	parents := []string{"root", "other"}
	wantParents := append([]string(nil), parents...)
	if err := CheckCategoryGraph(edges, "a", parents); err != nil {
		t.Fatal(err)
	}
	if got, want := CategoryDescendants(edges, "root"), []string{"a", "b", "leaf", "root"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("descendants = %v, want %v", got, want)
	}
	if got, want := ReachableFromTop(edges, "root"), map[string]bool{"a": true, "b": true, "leaf": true}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reachability = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(edges, original) || !reflect.DeepEqual(parents, wantParents) {
		t.Fatal("graph policy mutated caller-owned input")
	}
	if got := CategoryDescendants(nil, "isolated"); !reflect.DeepEqual(got, []string{"isolated"}) {
		t.Fatalf("isolated descendants = %v", got)
	}
}
