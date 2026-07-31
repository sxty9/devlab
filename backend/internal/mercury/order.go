package mercury

import (
	"encoding/json"
	"os"
	"sort"

	"devlab/backend/internal/fsatomic"
)

// A path-addressed store sorts each level by name, so Mercury has no inherent manual order. To let
// the user arrange siblings by hand (like the mail service's folders), an explicit per-category
// order is kept alongside — a name→[child names] map, exactly the shape the mail service persists in
// its folders.json. It is a pure display preference: the axioms themselves live in scheme untouched,
// and a category with no saved order falls back to the name sort.

// Order maps a category path (e.g. "axiome/architektur") to the desired order of its immediate
// children (subcategory names and axiom leaf names, the leaf carrying its ".md").
type Order map[string][]string

// LoadOrder reads the order map from path. A missing file is an empty order, not an error.
func LoadOrder(path string) Order {
	b, err := os.ReadFile(path)
	if err != nil {
		return Order{}
	}
	var o Order
	if json.Unmarshal(b, &o) != nil || o == nil {
		return Order{}
	}
	return o
}

// SaveOrder writes the order map to path atomically (tmp + rename), 0600.
func SaveOrder(path string, o Order) error {
	return fsatomic.WriteJSON(path, o)
}

// applyOrder sorts one node's children by the saved order for its path: named children first in the
// saved sequence, everything else after in the existing (name) order. Mirrors the mail service's
// applyOrder. The child key is the leaf name with ".md" for an axiom, the segment for a category.
func applyOrder(n *Node, order Order) {
	want := order[n.Path]
	if len(want) > 0 {
		rank := make(map[string]int, len(want))
		for i, name := range want {
			rank[name] = i
		}
		sort.SliceStable(n.Children, func(i, j int) bool {
			ri, iok := rank[childKey(n.Children[i])]
			rj, jok := rank[childKey(n.Children[j])]
			if iok && jok {
				return ri < rj
			}
			if iok != jok {
				return iok // ranked children first
			}
			return false // keep the incoming (name-sorted) order for the rest
		})
	}
	for _, c := range n.Children {
		applyOrder(c, order)
	}
}

// childKey is a child's identity within its parent's order list: an axiom keeps its ".md", a
// category is its segment name.
func childKey(n *Node) string {
	if n.IsAxiom {
		return n.Name + ".md"
	}
	return n.Name
}
