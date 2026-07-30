// Package discover resolves DevLab's managed repository set. The single source of truth is
// GitHub: the holistic set is every repository of the CONFIGURED owner that carries the holistic
// topic, resolved PER USER with that user's own OAuth token (ReposForUser) so visibility and
// permissions come straight from GitHub. A short per-user TTL cache avoids an API round-trip on
// every request.
//
// The owner is mandatory runtime configuration (DEVLAB_GH_OWNER). It deliberately has NO default: a
// default would scope both the managed set and every repository the delivery chain creates to
// another organisation's namespace. Without it every resolution fails closed with ErrOwnerUnset —
// an empty set is served, never a foreign one.
//
// The legacy local path (Repos/Path) — discovery of local working copies via the gh CLI — is
// retained for the dev-bypass/preview sandbox, which has no per-user GitHub link.
package discover

import (
	"context"
	"encoding/json"
	"errors"
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

// ErrOwnerUnset reports that the instance has not configured the GitHub owner of the holistic set.
// Resolution fails closed with this error rather than falling back to a hard-coded namespace.
var ErrOwnerUnset = errors.New("discover: no GitHub owner configured for the holistic set (DEVLAB_GH_OWNER)")

// Owner reads the MANDATORY owner scoping of the holistic set from the runtime configuration — one
// definition for the whole daemon, so a newly created service repository lands in the same namespace
// this package scans and no drifting sibling can name a second one. The variable is named at the
// lookup, not behind a constant, so the instance-neutrality audit (B-45c) keeps watching this exact
// spot for a re-introduced fallback.
//
// There are exactly TWO answers: a configured owner, or ErrOwnerUnset. There is no third answer and
// no usable default, and the SIGNATURE is what enforces that a caller notices — the accessor used to
// hand out "" beside a comment asking callers to reject it, and two of its three callers did not.
// "" is never a namespace: it makes `Owner() + "/axioms"` read "/axioms" (a filesystem remote, not a
// repository) and would place a created repository in whatever account the token happens to hold.
func Owner() (string, error) {
	if o := strings.TrimSpace(os.Getenv("DEVLAB_GH_OWNER")); o != "" {
		return o, nil
	}
	return "", ErrOwnerUnset
}

// Topic is the marker that makes a repo part of the holistic set (override with DEVLAB_GH_TOPIC).
// Unlike the owner this DOES default: "holistic" is the landscape-wide membership marker, not an
// instance value, so it needs no error channel.
func Topic() string {
	if t := strings.TrimSpace(os.Getenv("DEVLAB_GH_TOPIC")); t != "" {
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

// listRepos is the GitHub read the set is resolved from — a package variable so the resolution can
// be driven from a fixture repo list in tests, mirroring the fixture seam the github package uses
// for its own client tests. Production never reassigns it.
var listRepos = github.ListRepos

// ReposForUser returns the holistic-set repos the user can see on GitHub, with that user's
// effective per-repo permission. Cached per user for userTTL. A GitHub error returns the error
// (and any still-fresh cached value is preferred over erroring). An unconfigured owner fails
// closed with ErrOwnerUnset — before any GitHub call and without serving a cached set, because
// there is no namespace the set could legitimately be scoped to.
func ReposForUser(ctx context.Context, user, token string) ([]model.Repo, error) {
	own, err := Owner()
	if err != nil {
		return nil, err
	}
	userMu.Lock()
	if e, ok := userCache[user]; ok && time.Since(e.at) < userTTL {
		repos := e.repos
		userMu.Unlock()
		return repos, nil
	}
	userMu.Unlock()

	ghs, err := listRepos(ctx, token)
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

	repos := projectRepos(ghs, own, Topic())
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

// projectRepos filters GitHub repos to the holistic set (own + want, both resolved by the caller)
// and maps them onto model.Repo.
func projectRepos(ghs []github.Repo, own, want string) []model.Repo {
	var repos []model.Repo
	for _, g := range ghs {
		if !inHolisticSet(g, own, want) {
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
		k := kind(g.Name)
		repos = append(repos, model.Repo{
			ID:          g.Name,
			Name:        g.Name,
			FullName:    g.FullName,
			Kind:        k,
			Description: desc,
			Language:    languageLabel(g.Language),
			Icon:        icon(g.Language, k),
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

// inHolisticSet matches the `gh search --owner <owner> --topic <topic>` semantics: the repo must be
// owned by the configured owner AND carry the set marker. An unset scope matches NOTHING — without
// that guard an empty owner would admit every repo whose owner GitHub left blank.
func inHolisticSet(g github.Repo, own, want string) bool {
	if own == "" || want == "" {
		return false
	}
	if !strings.EqualFold(g.Owner, own) {
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
	// Treat an entry older than userTTL as absent so the caller repopulates via ReposForUser,
	// which re-checks GitHub and drops repos the user can no longer see. Without this, a cache
	// hit would let revoked GitHub access survive on the read hot path indefinitely.
	if !present || time.Since(e.at) >= userTTL {
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
// dev-bypass/preview sandbox, which has no per-user GitHub token. Without a configured owner the
// set is empty (query fails closed); this path has no error channel of its own.
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
	own, err := Owner()
	if err != nil {
		// Fail closed like the per-user path: without an owner there is no namespace to search
		// and no full name to mint, so the sandbox offers nothing rather than something foreign.
		return []model.Repo{}
	}
	ghs := ghSearch(own)
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
		k := kind(g.Name)
		repos = append(repos, model.Repo{
			ID:          g.Name,
			Name:        g.Name,
			FullName:    own + "/" + g.Name,
			Kind:        k,
			Description: desc,
			Language:    languageLabel(g.Language),
			Icon:        icon(g.Language, k),
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

func ghSearch(own string) []ghRepo {
	cmd := exec.Command("gh", "search", "repos", "--owner", own, "--topic", Topic(),
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

// icon derives the repo's card glyph from its primary language, falling back to its kind when the
// language has no mark of its own (a docs-only repo reports no language at all). The frontend maps
// the name to an SVG in src/ui/repoIcon.ts; an unknown name there renders the service mark.
func icon(language, kind string) string {
	switch strings.ToLower(language) {
	case "go":
		return "go"
	case "typescript", "javascript":
		return "ts"
	case "rust":
		return "rust"
	case "python":
		return "python"
	case "shell":
		return "shell"
	}
	switch kind {
	case "library":
		return "library"
	case "repo":
		return "repo"
	}
	return "service"
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
