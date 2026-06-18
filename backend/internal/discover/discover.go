// Package discover resolves DevLab's managed repository set. The single source of truth is
// GitHub: repos carrying the topic "holistic" (queried via the gh CLI, which already respects
// the operator's GitHub auth/permissions). Each is mapped to its local working copy under
// DEVLAB_REPOS_PATH. A short TTL cache avoids a gh round-trip on every request.
package discover

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"devlab/backend/internal/model"
)

var idRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// owner scoping for the topic search (override with DEVLAB_GH_OWNER).
func owner() string {
	if o := os.Getenv("DEVLAB_GH_OWNER"); o != "" {
		return o
	}
	return "sxty9"
}

type ghRepo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Language    string `json:"language"`
}

var (
	mu       sync.Mutex
	cached   []model.Repo
	cachedAt time.Time
)

// Repos returns the locally-cloned repos tagged `holistic` (30s cached).
func Repos(base string) []model.Repo {
	mu.Lock()
	defer mu.Unlock()
	if time.Since(cachedAt) < 30*time.Second && cached != nil {
		return cached
	}
	repos := query(base)
	cached, cachedAt = repos, time.Now()
	return repos
}

func query(base string) []model.Repo {
	ghs := ghSearch()
	if len(ghs) == 0 {
		// gh unavailable (e.g. no keyring in the sandboxed preview) → fall back to an explicit
		// allowlist of repo names. Keeps the holistic set visible without a live gh round-trip.
		ghs = allowlist()
	}
	var repos []model.Repo
	for _, g := range ghs {
		if !idRe.MatchString(g.Name) {
			continue
		}
		if _, ok := localPath(base, g.Name); !ok {
			continue // only repos with a local working copy are actionable in phase 2
		}
		desc := g.Description
		if desc == "" {
			desc = "Holistic service"
		}
		repos = append(repos, model.Repo{
			ID:          g.Name,
			Name:        g.Name,
			Kind:        kind(g.Name),
			Description: desc,
			Language:    languageLabel(g.Language),
			Tint:        tint(g.Language),
		})
	}
	sort.Slice(repos, func(i, j int) bool { return strings.ToLower(repos[i].Name) < strings.ToLower(repos[j].Name) })
	if repos == nil {
		repos = []model.Repo{}
	}
	return repos
}

func ghSearch() []ghRepo {
	cmd := exec.Command("gh", "search", "repos", "--owner", owner(), "--topic", "holistic",
		"--limit", "100", "--json", "name,description,language")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var ghs []ghRepo
	if json.Unmarshal(out, &ghs) != nil {
		return nil
	}
	return ghs
}

// allowlist reads DEVLAB_REPOS_ALLOWLIST (comma-separated repo names) as a gh-free fallback.
func allowlist() []ghRepo {
	raw := os.Getenv("DEVLAB_REPOS_ALLOWLIST")
	if raw == "" {
		return nil
	}
	var out []ghRepo
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		if name != "" {
			out = append(out, ghRepo{Name: name})
		}
	}
	return out
}

// Path resolves a repo id to its local working-copy path (validated, must contain .git).
func Path(base, id string) (string, bool) {
	if !idRe.MatchString(id) {
		return "", false
	}
	return localPath(base, id)
}

func localPath(base, name string) (string, bool) {
	p := filepath.Join(base, name)
	if fi, err := os.Stat(filepath.Join(p, ".git")); err == nil && (fi.IsDir() || fi.Mode().IsRegular()) {
		return p, true
	}
	return "", false
}

func kind(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "template"):
		return "library"
	case n == "sxgate" || n == "remshel" || n == "privleg":
		return "repo"
	default:
		return "service"
	}
}

func languageLabel(l string) string {
	if l == "" {
		return "—"
	}
	return l
}

func tint(language string) string {
	switch strings.ToLower(language) {
	case "typescript":
		return "accent"
	case "go":
		return "ssd"
	case "shell":
		return "net"
	case "python":
		return "success"
	case "rust", "c", "c++":
		return "warning"
	case "javascript":
		return "warning"
	default:
		return "gpu"
	}
}
