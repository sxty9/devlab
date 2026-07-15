// Package mercury builds the axiom-management model on top of aigentic's scheme-backed graveyard.
// It owns no store: the scheme tree is the single source of truth, and everything here is a
// projection of it. The tree is derived purely from the record paths — because a category exists
// exactly when it holds a record (scheme has no empty folders), the set of file paths IS the tree,
// complete and exact.
package mercury

import (
	"sort"
	"strings"
)

// The three top-level namespaces of the store. Konsolidierungen are gone: changes merge directly.
const (
	NsAxiome = "axiome" // the axiom tree, arbitrarily deep
	NsRegeln = "regeln" // Implementierungsregeln — extra rules Claude respects when implementing
	NsLaeufe = "laeufe" // scheduled-run plans and results
)

// Node is one node of a namespace's tree: a category (folder) or an axiom (a .md leaf). Categories
// nest to any depth. Path is the node's scheme path — for a leaf it is the record's stable address.
type Node struct {
	Name     string  `json:"name"`    // the path segment (category label or leaf slug, ".md" trimmed)
	Path     string  `json:"path"`    // full scheme path
	IsAxiom  bool    `json:"isAxiom"` // true for a .md leaf, false for a category
	Children []*Node `json:"children,omitempty"`
}

// Tree is the whole model, grouped by namespace.
type Tree struct {
	Axiome []*Node `json:"axiome"`
	Regeln []*Node `json:"regeln"`
	Laeufe []*Node `json:"laeufe"`
}

// Build assembles the tree from the flat, sorted list of record paths the graveyard returns. order
// is the user's manual sibling arrangement (nil/empty ⇒ pure name sort).
func Build(paths []string, order Order) Tree {
	roots := map[string]*Node{
		NsAxiome: {Name: NsAxiome, Path: NsAxiome},
		NsRegeln: {Name: NsRegeln, Path: NsRegeln},
		NsLaeufe: {Name: NsLaeufe, Path: NsLaeufe},
	}
	for _, p := range paths {
		insert(roots, p)
	}
	for _, ns := range []string{NsAxiome, NsRegeln, NsLaeufe} {
		sortNode(roots[ns])          // deterministic name sort first
		applyOrder(roots[ns], order) // then the user's manual arrangement on top
	}
	// Always emit a list, never nil: an empty namespace must marshal to [] not null, or the client
	// reads .length off null and blanks. Every list this API returns is a list.
	return Tree{
		Axiome: orEmpty(roots[NsAxiome].Children),
		Regeln: orEmpty(roots[NsRegeln].Children),
		Laeufe: orEmpty(roots[NsLaeufe].Children),
	}
}

func orEmpty(ns []*Node) []*Node {
	if ns == nil {
		return []*Node{}
	}
	return ns
}

// insert threads one record path into its namespace tree, creating category nodes as needed.
func insert(roots map[string]*Node, path string) {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	if len(segs) < 2 {
		return // a bare namespace or empty path holds no record
	}
	root, ok := roots[segs[0]]
	if !ok {
		return // outside the known namespaces (e.g. eingang/) — not part of the model
	}
	cur := root
	prefix := segs[0]
	for i := 1; i < len(segs); i++ {
		prefix += "/" + segs[i]
		leaf := i == len(segs)-1
		child := find(cur.Children, segs[i])
		if child == nil {
			name := segs[i]
			if leaf {
				name = strings.TrimSuffix(name, ".md")
			}
			child = &Node{Name: name, Path: prefix, IsAxiom: leaf}
			cur.Children = append(cur.Children, child)
		}
		cur = child
	}
}

func find(children []*Node, name string) *Node {
	trimmed := strings.TrimSuffix(name, ".md")
	for _, c := range children {
		if c.Name == name || c.Name == trimmed {
			return c
		}
	}
	return nil
}

// sortNode orders each level: categories before axioms, then by name — a stable, readable tree.
func sortNode(n *Node) {
	sort.SliceStable(n.Children, func(i, j int) bool {
		a, b := n.Children[i], n.Children[j]
		if a.IsAxiom != b.IsAxiom {
			return !a.IsAxiom // categories first
		}
		return a.Name < b.Name
	})
	for _, c := range n.Children {
		sortNode(c)
	}
}
