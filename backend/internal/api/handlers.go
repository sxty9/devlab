package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"devlab/backend/internal/discover"
	"devlab/backend/internal/git"
	"devlab/backend/internal/model"
)

// userRepos resolves the caller's holistic repo set. ok is false only when GitHub could not be
// reached — an unlinked account is an empty set, not a failure. Callers decide what a failure means:
// /api/repos turns it into a 502, Atlas degrades around it.
func (s *Server) userRepos(r *http.Request) (repos []model.Repo, ok bool) {
	// Dev-bypass/preview sandbox: no per-user token → the local working-copy set.
	if s.v.DevBypass() {
		return discover.Repos(s.reposBase), true
	}
	u := userFrom(r)
	token, err := s.userToken(u)
	if err != nil {
		// Not linked (the link gate should have prevented this) → an empty set, not an error.
		return []model.Repo{}, true
	}
	repos, err = discover.ReposForUser(r.Context(), u.Username, token)
	if err != nil {
		return nil, false
	}
	return repos, true
}

func (s *Server) repos(w http.ResponseWriter, r *http.Request) {
	repos, ok := s.userRepos(r)
	if !ok {
		writeErr(w, http.StatusBadGateway, "Could not reach GitHub")
		return
	}
	writeJSON(w, http.StatusOK, repos)
}

func (s *Server) file(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoPath(w, r)
	if !ok {
		return
	}
	rel := r.URL.Query().Get("path")
	if rel == "" {
		writeErr(w, http.StatusBadRequest, errMissingPath)
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
		writeErr(w, http.StatusBadRequest, errMissingPath)
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
	branches := git.Branches(p)
	structureID := "structure:" + id

	ahead := 0
	for _, b := range branches {
		if b.Name == branch {
			ahead = b.Ahead
		}
	}

	data := model.RepoData{
		Branches:    branches,
		Tree:        tree,
		Files:       map[string]model.FileContent{},
		Changes:     changes,
		Commits:     git.Commits(p, branch, 100),
		Worktrees:   git.Worktrees(p),
		Vision:      []model.VisionDoc{},
		Claude:      []model.ClaudeMsg{},
		Terminal:    []model.TermLine{},
		Stages:      stages(len(changes) > 0, ahead),
		DefaultTabs: []model.Tab{{ID: structureID, Title: id + " — structure", Kind: "structure"}},
		ActiveTabID: structureID,
		Structure:   structure(p, tree),
	}
	writeJSON(w, http.StatusOK, data)
}

// stages derives the repo overview rows HONESTLY from real git state and nothing else
// (B 1.4): Code is active while the working tree is dirty, Delivery while the branch has
// unpushed commits. Rows this server cannot attest (vision, preview, merge) are NOT invented.
func stages(hasChanges bool, ahead int) []model.RepoStage {
	code, codeHint := "done", "Committed"
	if hasChanges {
		code, codeHint = "active", "Uncommitted changes"
	}
	delivery, deliveryHint := "pending", "Push to deliver"
	if ahead > 0 {
		delivery = "active"
		deliveryHint = plural(ahead, "commit") + " to push"
	} else if !hasChanges {
		deliveryHint = "Up to date"
	}
	return []model.RepoStage{
		{ID: "code", Label: "Code", State: code, Hint: codeHint},
		{ID: "delivery", Label: "Delivery", State: delivery, Hint: deliveryHint},
	}
}

func plural(n int, noun string) string {
	s := noun
	if n != 1 {
		s += "s"
	}
	return strconv.Itoa(n) + " " + s
}

// structure derives the repo skeleton overview from the top of the file tree.
func structure(repoPath string, tree []model.FileNode) []model.StructureSection {
	var dirs, files []model.StructureEntry
	for _, n := range tree {
		if n.Kind == "dir" {
			dirs = append(dirs, model.StructureEntry{Name: n.Name + "/", Kind: "dir", Note: "module"})
		}
	}
	for _, name := range []string{"README.md", "package.json", "go.mod", "CLAUDE.md", "service", "holistic-service.json"} {
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
