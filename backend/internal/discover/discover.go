// Package discover resolves DevLab's managed repository set. The single source of truth is
// GitHub: the holistic set is "owner sxty9, topic holistic", resolved PER USER with that user's
// own OAuth token (ReposForUser) so visibility and permissions come straight from GitHub. A short
// per-user TTL cache avoids an API round-trip on every request.
//
// The legacy local path (Repos/Path) — discovery of local working copies via the gh CLI — is
// retained for the dev-bypass/preview sandbox, which has no per-user GitHub link.
package discover

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"devlab/backend/internal/github"
	"devlab/backend/internal/model"
)

var idRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// owner scoping for the holistic set (override with DEVLAB_GH_OWNER).
func owner() string {
	if o := os.Getenv("DEVLAB_GH_OWNER"); o != "" {
		return o
	}
	return "sxty9"
}

// topic marks a repo as part of the holistic set (override with DEVLAB_GH_TOPIC).
func topic() string {
	if t := os.Getenv("DEVLAB_GH_TOPIC"); t != "" {
		return t
	}
	return "holistic"
}

// ─── Per-user GitHub visibility (production) ────────────────────────────────

type userEntry struct {
	repos []model.Repo
	at    time.Time
}

var (
	userMu    sync.Mutex
	userCache = map[string]userEntry{}
)

const userTTL = 45 * time.Second

// ReposForUser returns the holistic-set repos the user can see on GitHub, with that user's
// effective per-repo permission. Cached per user for userTTL. A GitHub error returns the error
// (and any still-fresh cached value is preferred over erroring).
func ReposForUser(ctx context.Context, user, token string) ([]model.Repo, error) {
	userMu.Lock()
	if e, ok := userCache[user]; ok && time.Since(e.at) < userTTL {
		repos := e.repos
		userMu.Unlock()
		return repos, nil
	}
	userMu.Unlock()

	ghs, err := github.ListRepos(ctx, token)
	if err != nil {
		// Serve a stale cache entry rather than blanking the UI on a transient GitHub blip.
		userMu.Lock()
		if e, ok := userCache[user]; ok {
			repos := e.repos
			userMu.Unlock()
			return repos, nil
		}
		userMu.Unlock()
		return nil, err
	}

	repos := projectRepos(ghs)
	userMu.Lock()
	userCache[user] = userEntry{repos: repos, at: time.Now()}
	userMu.Unlock()
	return repos, nil
}

// InvalidateUser drops a user's cached repo set (call after link/unlink so the next read
// reflects the new token immediately).
func InvalidateUser(user string) {
	userMu.Lock()
	delete(userCache, user)
	userMu.Unlock()
}

// projectRepos filters GitHub repos to the holistic set and maps them onto model.Repo.
func projectRepos(ghs []github.Repo) []model.Repo {
	want := topic()
	var repos []model.Repo
	for _, g := range ghs {
		if !inHolisticSet(g, want) {
			continue
		}
		if !idRe.MatchString(g.Name) {
			continue
		}
		desc := g.Description
		if desc == "" {
			desc = "Holistic repository"
		}
		perm := g.Permission
		if perm == "" {
			perm = "pull"
		}
		repos = append(repos, model.Repo{
			ID:          g.Name,
			Name:        g.Name,
			FullName:    g.FullName,
			Kind:        kind(g.Name),
			Description: desc,
			Language:    languageLabel(g.Language),
			Tint:        tint(g.Language),
			Permission:  perm,
		})
	}
	sort.Slice(repos, func(i, j int) bool { return strings.ToLower(repos[i].Name) < strings.ToLower(repos[j].Name) })
	if repos == nil {
		repos = []model.Repo{}
	}
	return repos
}

// inHolisticSet matches the existing `gh search --owner sxty9 --topic holistic` semantics:
// the repo must be owned by the configured owner AND carry the holistic topic.
func inHolisticSet(g github.Repo, want string) bool {
	if !strings.EqualFold(g.Owner, owner()) {
		return false
	}
	for _, t := range g.Topics {
		if strings.EqualFold(t, want) {
			return true
		}
	}
	return false
}

// FullName resolves a repo id to its GitHub owner/repo for the given user (from the cached set).
// Returns false if the user's set doesn't contain the id (so write ops can't target arbitrary
// repos). PermissionFor returns the cached effective permission alongside.
func FullName(user, id string) (string, bool) {
	full, _, ok := lookup(user, id)
	return full, ok
}

// PermissionFor returns the user's cached effective permission on a repo id (pull|push|admin).
func PermissionFor(user, id string) (string, bool) {
	_, perm, ok := lookup(user, id)
	return perm, ok
}

func lookup(user, id string) (full, perm string, ok bool) {
	userMu.Lock()
	defer userMu.Unlock()
	e, present := userCache[user]
	if !present {
		return "", "", false
	}
	for _, r := range e.repos {
		if r.ID == id {
			return r.FullName, r.Permission, true
		}
	}
	return "", "", false
}

// ─── Legacy local discovery (dev-bypass / preview sandbox) ──────────────────

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

// Repos returns the locally-cloned repos tagged `holistic` (30s cached). Used only in the
// dev-bypass/preview sandbox, which has no per-user GitHub token.
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
		ghs = allowlist()
	}
	var repos []model.Repo
	for _, g := range ghs {
		if !idRe.MatchString(g.Name) {
			continue
		}
		if _, ok := localPath(base, g.Name); !ok {
			continue // only repos with a local working copy are actionable in the sandbox
		}
		desc := g.Description
		if desc == "" {
			desc = "Holistic service"
		}
		repos = append(repos, model.Repo{
			ID:          g.Name,
			Name:        g.Name,
			FullName:    owner() + "/" + g.Name,
			Kind:        kind(g.Name),
			Description: desc,
			Language:    languageLabel(g.Language),
			Tint:        tint(g.Language),
			Permission:  "admin", // sandbox operator has full local control
		})
	}
	sort.Slice(repos, func(i, j int) bool { return strings.ToLower(repos[i].Name) < strings.ToLower(repos[j].Name) })
	if repos == nil {
		repos = []model.Repo{}
	}
	return repos
}

func ghSearch() []ghRepo {
	cmd := exec.Command("gh", "search", "repos", "--owner", owner(), "--topic", topic(),
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

// ─── Shared presentation helpers ────────────────────────────────────────────

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
