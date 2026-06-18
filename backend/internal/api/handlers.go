package api

import (
	"net/http"
	"os"
	"path/filepath"

	"devlab/backend/internal/discover"
	"devlab/backend/internal/git"
	"devlab/backend/internal/model"
)

func (s *Server) repos(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, discover.Repos(s.reposBase))
}

func (s *Server) branches(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoPath(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, git.Branches(p))
}

func (s *Server) tree(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoPath(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, git.Tree(p))
}

func (s *Server) changes(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoPath(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, git.Changes(p))
}

func (s *Server) worktrees(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoPath(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, git.Worktrees(p))
}

func (s *Server) commits(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoPath(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, git.Commits(p, r.URL.Query().Get("branch"), 100))
}

func (s *Server) file(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoPath(w, r)
	if !ok {
		return
	}
	rel := r.URL.Query().Get("path")
	if rel == "" {
		writeErr(w, http.StatusBadRequest, "Missing path")
		return
	}
	writeJSON(w, http.StatusOK, git.FileAt(p, rel))
}

func (s *Server) diff(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoPath(w, r)
	if !ok {
		return
	}
	rel := r.URL.Query().Get("path")
	if rel == "" {
		writeErr(w, http.StatusBadRequest, "Missing path")
		return
	}
	writeJSON(w, http.StatusOK, git.DiffFor(p, rel))
}

// repoData assembles the aggregate the workspace loads on repo switch. files/diffBefore are
// lazy (fetched per-path); claude/terminal are live via WebSocket (phase 2c/2d), empty here.
func (s *Server) repoData(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoPath(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	branch := r.URL.Query().Get("branch")
	if branch == "" {
		branch = git.DefaultBranch(p)
	}

	tree := git.Tree(p)
	changes := git.Changes(p)
	structureID := "structure:" + id

	data := model.RepoData{
		Branches:    git.Branches(p),
		Tree:        tree,
		Files:       map[string]model.FileContent{},
		Changes:     changes,
		Commits:     git.Commits(p, branch, 100),
		Worktrees:   git.Worktrees(p),
		Vision:      []model.VisionDoc{},
		Claude:      []model.ClaudeMsg{},
		Terminal:    []model.TermLine{},
		Stages:      stages(len(changes) > 0),
		DefaultTabs: []model.Tab{{ID: structureID, Title: id + " — structure", Kind: "structure"}},
		ActiveTabID: structureID,
		Structure:   structure(p, tree),
	}
	writeJSON(w, http.StatusOK, data)
}

// stages derives the delivery pipeline state (Code active while there are changes).
func stages(hasChanges bool) []model.Stage {
	code := "done"
	if hasChanges {
		code = "active"
	}
	return []model.Stage{
		{ID: "vision", Label: "Vision", State: "done", Hint: "Captured"},
		{ID: "code", Label: "Code", State: code, Hint: "Working tree"},
		{ID: "preview", Label: "Preview", State: "pending", Hint: "sxgate"},
		{ID: "delivery", Label: "Delivery", State: "pending", Hint: "Prod test"},
		{ID: "merge", Label: "main", State: "pending", Hint: "Awaiting merge"},
	}
}

// structure derives the repo skeleton overview from the top of the file tree.
func structure(repoPath string, tree []model.FileNode) []model.StructureSection {
	var dirs, files []model.StructureEntry
	for _, n := range tree {
		if n.Kind == "dir" {
			dirs = append(dirs, model.StructureEntry{Name: n.Name + "/", Kind: "dir", Note: "module"})
		}
	}
	for _, name := range []string{"README.md", "package.json", "go.mod", "CLAUDE.md", ".sxgate/preview.conf"} {
		if _, err := os.Stat(filepath.Join(repoPath, name)); err == nil {
			files = append(files, model.StructureEntry{Name: name, Kind: "file", Note: "key file"})
		}
	}
	var sections []model.StructureSection
	if len(dirs) > 0 {
		sections = append(sections, model.StructureSection{Title: "Top-level modules", Hint: "Directories in the working tree", Entries: dirs})
	}
	if len(files) > 0 {
		sections = append(sections, model.StructureSection{Title: "Key files", Hint: "Open to inspect", Entries: files})
	}
	if sections == nil {
		sections = []model.StructureSection{}
	}
	return sections
}
