package catalog

import (
	"errors"
	"sort"
)

// CategoryDescendants returns the changed child plus its transitive
// descendants in the current graph. Product validity can change for
// products referencing any of these (direct reference, or reachability
// through them), so all of them scope locks, orphan checks, and resets.
func CategoryDescendants(allEdges []Edge, childID string) []string {
	children := map[string][]string{}
	for _, edge := range allEdges {
		children[edge.ParentID] = append(children[edge.ParentID], edge.ChildID)
	}
	seen := map[string]bool{childID: true}
	out := []string{childID}
	queue := []string{childID}
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		for _, child := range children[node] {
			if !seen[child] {
				seen[child] = true
				out = append(out, child)
				queue = append(queue, child)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Graph violation sentinels: cycles and depth excess are distinct safe
// diagnostics (R07), both terminal.
var (
	ErrGraphCycle = errors.New("category cycle")
	ErrGraphDepth = errors.New("category depth exceeded")
)

// CheckCategoryGraph validates the replaced edge set for one child: no
// self edge (already enforced at ingestion, rechecked defensively),
// acyclic, and every root→leaf path at most maxCatalogDepthNodes nodes
// (mirrors the Retail max-3-node DAG invariant as a global property).
func CheckCategoryGraph(allEdges []Edge, childID string, newParents []string) error {
	const maxCatalogDepthNodes = 3
	children := map[string]map[string]bool{}
	edgeSet := map[[2]string]bool{}
	add := func(parent, child string) {
		if parent == child {
			return
		}
		if children[parent] == nil {
			children[parent] = map[string]bool{}
		}
		if !children[parent][child] {
			children[parent][child] = true
			edgeSet[[2]string{parent, child}] = true
		}
	}
	for _, edge := range allEdges {
		if edge.ChildID == childID {
			continue
		}
		add(edge.ParentID, edge.ChildID)
	}
	for _, parent := range newParents {
		if parent == childID {
			return ErrGraphCycle
		}
		add(parent, childID)
	}
	// DFS from every node: cycle detection + longest path.
	const visiting = 1
	const done = 2
	state := map[string]int{}
	depth := map[string]int{}
	var visit func(node string, stack int) error
	visit = func(node string, stack int) error {
		if stack > 10000 {
			return ErrGraphDepth
		}
		switch state[node] {
		case done:
			return nil
		case visiting:
			return ErrGraphCycle
		}
		state[node] = visiting
		longest := 1
		for child := range children[node] {
			if err := visit(child, stack+1); err != nil {
				return err
			}
			if depth[child]+1 > longest {
				longest = depth[child] + 1
			}
		}
		if longest > maxCatalogDepthNodes {
			return ErrGraphDepth
		}
		depth[node] = longest
		state[node] = done
		return nil
	}
	nodes := map[string]bool{childID: true}
	for pair := range edgeSet {
		nodes[pair[0]] = true
		nodes[pair[1]] = true
	}
	for node := range nodes {
		if state[node] == done {
			continue
		}
		if err := visit(node, 0); err != nil {
			return err
		}
	}
	return nil
}

// ReachableFromTop reports the descendant set of top over current edges
// (top itself excluded, mirroring the Retail reachableFrom helper).
func ReachableFromTop(allEdges []Edge, topID string) map[string]bool {
	children := map[string][]string{}
	for _, edge := range allEdges {
		children[edge.ParentID] = append(children[edge.ParentID], edge.ChildID)
	}
	reached := map[string]bool{}
	queue := append([]string{}, children[topID]...)
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		if reached[node] {
			continue
		}
		reached[node] = true
		queue = append(queue, children[node]...)
	}
	return reached
}

// HasParents reports whether a category currently has any parent edge.
func HasParents(allEdges []Edge, categoryID string) bool {
	for _, edge := range allEdges {
		if edge.ChildID == categoryID {
			return true
		}
	}
	return false
}
