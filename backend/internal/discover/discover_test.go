// The resolution tests of B-10. They run against a FIXTURE repo list instead of GitHub (the
// listRepos seam), so the two things this package decides are observable: which repositories may
// become a target at all (owner + topic + id shape), and that no repository of the instance falls
// out of the offered set on the way (REQ-006.3).
//
// The package's caches are process-global, so every test starts from a clean slate (reset) and none
// of them runs in parallel.
package discover

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/github"
	"devlab/backend/internal/model"
)

const (
	testOwner = "an-org"
	testUser  = "u1"
	// The contract this package is held to, spelled out here rather than read from the
	// implementation: the owner variable, the marker variable, and the marker's default.
	envOwner     = "DEVLAB_GH_OWNER"
	envTopic     = "DEVLAB_GH_TOPIC"
	defaultTopic = "holistic"
)

// reset clears both caches and pins the seam back to the real GitHub read afterwards, so one test
// can never inherit another's cached set or fixture.
func reset(t *testing.T) {
	t.Helper()
	clear := func() {
		userMu.Lock()
		userCache = map[string]userEntry{}
		userMu.Unlock()
		mu.Lock()
		cached, cachedAt = nil, time.Time{}
		mu.Unlock()
	}
	clear()
	old := listRepos
	t.Cleanup(func() {
		listRepos = old
		clear()
	})
}

// fixtureSet is the repo list GitHub would report for a linked user: the FIRST group is the
// instance's holistic set (all of it must be offered — REQ-006.3), the second group must be
// filtered out. Each member of the first group carries one property that a careless filter drops:
// no description, no language, a dotted / underscored / dashed name, a mixed-case owner, a
// mixed-case topic, further topics beside the marker, and every permission level including none.
func fixtureSet() []github.Repo {
	return []github.Repo{
		// ── the holistic set ───────────────────────────────────────────────────
		{Name: "atlas", FullName: testOwner + "/atlas", Owner: testOwner,
			Description: "The map.", Language: "Go", Topics: []string{defaultTopic}, Permission: "admin"},
		{Name: "web-ui", FullName: testOwner + "/web-ui", Owner: testOwner,
			Language: "TypeScript", Topics: []string{"frontend", defaultTopic}, Permission: "push"},
		{Name: "docs.site", FullName: testOwner + "/docs.site", Owner: testOwner,
			Description: "Prose only.", Topics: []string{defaultTopic}, Permission: "pull"},
		{Name: "tool_kit", FullName: testOwner + "/tool_kit", Owner: testOwner,
			Description: "Shared bits.", Language: "Rust", Topics: []string{strings.ToUpper(defaultTopic)}, Permission: ""},
		{Name: "service-template", FullName: testOwner + "/service-template", Owner: strings.ToUpper(testOwner),
			Description: "Scaffold.", Language: "Shell", Topics: []string{defaultTopic}, Permission: "push"},

		// ── everything that must NOT be offered ────────────────────────────────
		{Name: "no-topic", FullName: testOwner + "/no-topic", Owner: testOwner,
			Description: "Same owner, marker missing.", Language: "Go", Topics: nil, Permission: "admin"},
		{Name: "other-topic", FullName: testOwner + "/other-topic", Owner: testOwner,
			Language: "Go", Topics: []string{"holistic-adjacent", "internal"}, Permission: "admin"},
		{Name: "foreign", FullName: "another-org/foreign", Owner: "another-org",
			Description: "Marker set, foreign owner.", Language: "Go", Topics: []string{defaultTopic}, Permission: "admin"},
		{Name: "bad name", FullName: testOwner + "/bad name", Owner: testOwner,
			Description: "Id shape violated.", Language: "Go", Topics: []string{defaultTopic}, Permission: "admin"},
		{Name: "../escape", FullName: testOwner + "/../escape", Owner: testOwner,
			Description: "Traversal in the id.", Topics: []string{defaultTopic}, Permission: "admin"},
		{Name: "", FullName: testOwner + "/", Owner: testOwner,
			Description: "No name at all.", Topics: []string{defaultTopic}, Permission: "admin"},
		{Name: "ownerless", FullName: "/ownerless", Owner: "",
			Description: "GitHub reported no owner.", Topics: []string{defaultTopic}, Permission: "admin"},
	}
}

// wantedIDs is the offered set the fixture must produce, in the order the resolution promises
// (case-insensitive by name).
var wantedIDs = []string{"atlas", "docs.site", "service-template", "tool_kit", "web-ui"}

// serve points the seam at a fixture list and counts the reads, so a cache hit is provable.
func serve(repos []github.Repo, err error) *int {
	calls := 0
	listRepos = func(context.Context, string) ([]github.Repo, error) {
		calls++
		if err != nil {
			return nil, err
		}
		return repos, nil
	}
	return &calls
}

func ids(repos []model.Repo) []string {
	out := make([]string, 0, len(repos))
	for _, r := range repos {
		out = append(out, r.ID)
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestReposForUserResolvesTheFixtureSet is the REQ-006.3 assertion: every repository of the
// instance is offered — none of the five drops out because of a missing description, a missing
// language, a dotted/underscored name, a differently-cased owner or topic, or an absent permission.
func TestReposForUserResolvesTheFixtureSet(t *testing.T) {
	reset(t)
	t.Setenv(envOwner, testOwner)
	serve(fixtureSet(), nil)

	repos, err := ReposForUser(context.Background(), testUser, "tok")
	if err != nil {
		t.Fatalf("ReposForUser: %v", err)
	}
	if got := ids(repos); !equal(got, wantedIDs) {
		t.Fatalf("offered set = %v, want %v (no repository of the instance may be missing)", got, wantedIDs)
	}

	// Every holistic-set member of the fixture is reachable by id, with its GitHub full name.
	for _, want := range wantedIDs {
		full, ok := FullName(testUser, want)
		if !ok {
			t.Errorf("FullName(%q) not resolvable — the repo is offered but cannot be targeted", want)
			continue
		}
		if !strings.EqualFold(full, testOwner+"/"+want) {
			t.Errorf("FullName(%q) = %q, want the configured owner's namespace", want, full)
		}
	}
}

// TestProjectReposFilter pins each individual reason a repo is not a legitimate target, so a
// loosened filter names the case it broke.
func TestProjectReposFilter(t *testing.T) {
	reset(t)
	repos := projectRepos(fixtureSet(), testOwner, defaultTopic)
	got := ids(repos)
	if !equal(got, wantedIDs) {
		t.Fatalf("filtered set = %v, want %v", got, wantedIDs)
	}
	for _, denied := range []string{"no-topic", "other-topic", "foreign", "bad name", "../escape", "", "ownerless"} {
		for _, r := range repos {
			if r.ID == denied {
				t.Errorf("%q must not be offered as a target", denied)
			}
		}
	}
}

// TestProjectReposDefaults pins the two substitutions the projection makes: a repo without a
// description gets the generic label, and a repo GitHub reports no permission for is read-only.
// A missing permission must never widen into push.
func TestProjectReposDefaults(t *testing.T) {
	reset(t)
	repos := projectRepos(fixtureSet(), testOwner, defaultTopic)
	byID := map[string]model.Repo{}
	for _, r := range repos {
		byID[r.ID] = r
	}

	if got := byID["web-ui"].Description; got != "Holistic repository" {
		t.Errorf("description of a repo without one = %q, want the generic label", got)
	}
	if got := byID["tool_kit"].Permission; got != "pull" {
		t.Errorf("permission default = %q, want pull (an unknown right is read-only)", got)
	}
	if got := byID["atlas"].Permission; got != "admin" {
		t.Errorf("permission of atlas = %q, want the reported admin", got)
	}
	if got := byID["docs.site"].Language; got != "—" {
		t.Errorf("language of a repo without one = %q, want the placeholder", got)
	}
	if got := byID["docs.site"].Icon; got != "service" {
		t.Errorf("icon without a language = %q, want the kind's mark", got)
	}
	if got := byID["service-template"].Kind; got != "library" {
		t.Errorf("kind of a template repo = %q, want library", got)
	}
}

// TestInHolisticSetFailsClosedOnUnsetScope is the guard behind BEFUND A: with no owner configured
// the membership test must match NOTHING. Without it a repo whose owner GitHub left blank would
// compare equal to the empty owner and enter the set.
func TestInHolisticSetFailsClosedOnUnsetScope(t *testing.T) {
	reset(t)
	ownerless := github.Repo{Name: "ownerless", Owner: "", Topics: []string{defaultTopic}}
	member := github.Repo{Name: "atlas", Owner: testOwner, Topics: []string{defaultTopic}}

	if inHolisticSet(ownerless, "", defaultTopic) {
		t.Error("an unset owner must match nothing, not every owner-less repo")
	}
	if inHolisticSet(member, testOwner, "") {
		t.Error("an unset topic must match nothing")
	}
	if !inHolisticSet(member, testOwner, defaultTopic) {
		t.Error("a configured scope must still match its own set")
	}
	if got := projectRepos(fixtureSet(), "", defaultTopic); len(got) != 0 {
		t.Errorf("projection without an owner = %v, want the empty set", ids(got))
	}
}

// TestOwnerIsMandatory is BEFUND A itself: an unconfigured owner must never resolve to a
// hard-coded organisation. It fails closed with the named error, and the exported accessor reports
// no namespace at all.
func TestOwnerIsMandatory(t *testing.T) {
	reset(t)
	t.Setenv(envOwner, "")
	calls := serve(fixtureSet(), nil)

	own, err := Owner()
	if !errors.Is(err, ErrOwnerUnset) {
		t.Errorf("Owner() error = %v, want ErrOwnerUnset", err)
	}
	if own != "" {
		t.Errorf("Owner() = %q — an unconfigured instance must not fall back to a namespace", own)
	}
	repos, rerr := ReposForUser(context.Background(), testUser, "tok")
	if !errors.Is(rerr, ErrOwnerUnset) {
		t.Errorf("ReposForUser error = %v, want ErrOwnerUnset", rerr)
	}
	if repos != nil {
		t.Errorf("ReposForUser repos = %v, want none", ids(repos))
	}
	if *calls != 0 {
		t.Errorf("GitHub was read %d times — the config failure must be detected first", *calls)
	}
	if !strings.Contains(ErrOwnerUnset.Error(), envOwner) {
		t.Errorf("the error must name the variable to set, got %q", ErrOwnerUnset.Error())
	}

	// The legacy sandbox path has no error channel; it must fail closed to the empty set instead
	// of minting "/<name>" full names or searching GitHub without an owner scope.
	noGH(t)
	t.Setenv("DEVLAB_REPOS_ALLOWLIST", "atlas,web-ui")
	if got := Repos(workingCopies(t, "atlas")); len(got) != 0 {
		t.Errorf("sandbox set without an owner = %v, want the empty set", ids(got))
	}
}

// TestSandboxSetUsesTheConfiguredNamespace is the other half of BEFUND A: the full name the sandbox
// mints — the namespace a newly created repository would land in — comes from the configuration, and
// only repositories with a working copy are actionable. It also pins the local 30s cache.
func TestSandboxSetUsesTheConfiguredNamespace(t *testing.T) {
	reset(t)
	noGH(t)
	t.Setenv(envOwner, testOwner)
	t.Setenv("DEVLAB_REPOS_ALLOWLIST", "atlas,not-cloned,bad name")
	base := workingCopies(t, "atlas", "unlisted")

	repos := Repos(base)
	if got := ids(repos); !equal(got, []string{"atlas"}) {
		t.Fatalf("sandbox set = %v, want [atlas] (listed AND cloned)", got)
	}
	if got := repos[0].FullName; got != testOwner+"/atlas" {
		t.Errorf("full name = %q, want the configured namespace", got)
	}
	if got := repos[0].Permission; got != "admin" {
		t.Errorf("sandbox permission = %q, want admin (the operator owns the working copies)", got)
	}

	// The 30s cache answers the second call: a changed allowlist is not picked up mid-window.
	t.Setenv("DEVLAB_REPOS_ALLOWLIST", "atlas,unlisted")
	if got := ids(Repos(base)); !equal(got, []string{"atlas"}) {
		t.Errorf("second call = %v, want the cached [atlas]", got)
	}
}

// noGH removes the gh CLI from PATH, so the legacy path takes its documented gh-free fallback and no
// test of this package ever reaches the network.
func noGH(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

// workingCopies creates a repos base holding a .git working copy per name.
func workingCopies(t *testing.T, names ...string) string {
	t.Helper()
	base := t.TempDir()
	for _, n := range names {
		if err := os.MkdirAll(filepath.Join(base, n, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return base
}

// TestOwnerAndTopicConfiguration pins the accessors the delivery chain places new repositories
// with: the owner comes from the runtime configuration and is trimmed; the topic has a default
// because the marker is a landscape constant, not an instance value.
func TestOwnerAndTopicConfiguration(t *testing.T) {
	reset(t)
	t.Setenv(envOwner, "  "+testOwner+" ")
	if got, err := Owner(); err != nil || got != testOwner {
		t.Errorf("Owner() = %q,%v, want the trimmed value and no error", got, err)
	}

	t.Setenv(envTopic, "")
	if got := Topic(); got != defaultTopic {
		t.Errorf("Topic() = %q, want the %q default", got, defaultTopic)
	}
	t.Setenv(envTopic, " own-marker ")
	if got := Topic(); got != "own-marker" {
		t.Errorf("Topic() = %q, want the configured marker", got)
	}

	// The marker really is what membership is read against, not a hard-coded word.
	reset(t)
	t.Setenv(envOwner, testOwner)
	t.Setenv(envTopic, "own-marker")
	serve([]github.Repo{
		{Name: "marked", Owner: testOwner, FullName: testOwner + "/marked", Topics: []string{"own-marker"}},
		{Name: "unmarked", Owner: testOwner, FullName: testOwner + "/unmarked", Topics: []string{defaultTopic}},
	}, nil)
	repos, err := ReposForUser(context.Background(), testUser, "tok")
	if err != nil {
		t.Fatalf("ReposForUser: %v", err)
	}
	if got := ids(repos); !equal(got, []string{"marked"}) {
		t.Errorf("set under a configured marker = %v, want [marked]", got)
	}
}

// TestReposForUserCache pins the TTL cache and its stale path: inside the TTL GitHub is not read
// again; a GitHub failure is answered from a stale entry rather than blanking the set; with no
// entry at all the failure surfaces.
func TestReposForUserCache(t *testing.T) {
	reset(t)
	t.Setenv(envOwner, testOwner)

	calls := serve(fixtureSet(), nil)
	if _, err := ReposForUser(context.Background(), testUser, "tok"); err != nil {
		t.Fatalf("first read: %v", err)
	}
	if _, err := ReposForUser(context.Background(), testUser, "tok"); err != nil {
		t.Fatalf("second read: %v", err)
	}
	if *calls != 1 {
		t.Errorf("GitHub reads = %d, want 1 — the second read must come from the cache", *calls)
	}

	// A cache entry older than the TTL is refreshed, not served.
	age(t, testUser, 2*userTTL)
	if _, err := ReposForUser(context.Background(), testUser, "tok"); err != nil {
		t.Fatalf("read after expiry: %v", err)
	}
	if *calls != 2 {
		t.Errorf("GitHub reads = %d, want 2 — an expired entry must be refreshed", *calls)
	}

	// Stale-serve: GitHub is down, the entry has expired → the last known set, no error. The UI
	// keeps its repositories instead of going blank on a transient blip.
	age(t, testUser, 2*userTTL)
	down := errors.New("github unreachable")
	serve(nil, down)
	repos, err := ReposForUser(context.Background(), testUser, "tok")
	if err != nil {
		t.Fatalf("stale serve must not error, got %v", err)
	}
	if got := ids(repos); !equal(got, wantedIDs) {
		t.Errorf("stale set = %v, want the last known %v", got, wantedIDs)
	}

	// No entry at all → the failure is the answer (an empty set would read as "no repositories").
	InvalidateUser(testUser)
	if _, err := ReposForUser(context.Background(), testUser, "tok"); !errors.Is(err, down) {
		t.Errorf("error without a cached set = %v, want the GitHub failure", err)
	}
}

// TestInvalidateUserAndLookupExpiry pins the two safety properties of the per-user cache: a
// link/unlink drops the set immediately, and an expired entry is treated as absent on the write
// hot path so revoked GitHub access cannot survive in FullName/PermissionFor.
func TestInvalidateUserAndLookupExpiry(t *testing.T) {
	reset(t)
	t.Setenv(envOwner, testOwner)
	serve(fixtureSet(), nil)
	if _, err := ReposForUser(context.Background(), testUser, "tok"); err != nil {
		t.Fatalf("ReposForUser: %v", err)
	}

	if perm, ok := PermissionFor(testUser, "web-ui"); !ok || perm != "push" {
		t.Errorf("PermissionFor(web-ui) = %q,%v, want push,true", perm, ok)
	}
	if _, ok := FullName(testUser, "foreign"); ok {
		t.Error("a repo outside the set must not resolve — write ops could target it")
	}
	if _, ok := FullName("other-user", "atlas"); ok {
		t.Error("another user's id must not resolve from this user's cache")
	}

	age(t, testUser, 2*userTTL)
	if _, ok := FullName(testUser, "atlas"); ok {
		t.Error("an expired entry must read as absent — revoked access must not survive")
	}
	if _, ok := PermissionFor(testUser, "atlas"); ok {
		t.Error("an expired entry must not hand out a permission")
	}

	InvalidateUser(testUser)
	userMu.Lock()
	_, present := userCache[testUser]
	userMu.Unlock()
	if present {
		t.Error("InvalidateUser must drop the entry")
	}
}

// TestPathRejectsForeignIDs pins the id gate of the local path: only the validated shape resolves,
// and only with a real working copy behind it.
func TestPathRejectsForeignIDs(t *testing.T) {
	reset(t)
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "atlas", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	if got, ok := Path(base, "atlas"); !ok || got != filepath.Join(base, "atlas") {
		t.Errorf("Path(atlas) = %q,%v, want the working copy", got, ok)
	}
	for _, bad := range []string{"", "../escape", "a/b", "bad name", "atlas/../../etc"} {
		if _, ok := Path(base, bad); ok {
			t.Errorf("Path(%q) resolved — the id shape must reject it", bad)
		}
	}
	if _, ok := Path(base, "not-cloned"); ok {
		t.Error("a repo without a working copy must not resolve")
	}
}

// ownerCallSite matches the ONE admissible shape of a call to Owner: both results are bound to
// names. The error name may not be the blank identifier — and once it is a real name, Go's own
// unused-variable rule forces the caller to read it, which is why binding is the whole assertion.
var ownerCallSite = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.\[\]]*, *([A-Za-z_][A-Za-z0-9_]*) *:?= *discover\.Owner\(\)$`)

// TestEveryOwnerCallSiteHandlesTheNamedError reads the SHIPPED backend sources and holds every
// caller of Owner to the contract of the accessor, not to its own good intentions.
//
// The defect this test exists for: the accessor used to answer `string` alone, with a doc comment
// asking callers to reject "" and report ErrOwnerUnset. Of its three callers two never looked —
// one placed a repository under the empty owner (GitHub creates it under whatever account the
// token holds) and one composed "/axioms", which the constitution store reads as a FILESYSTEM
// remote. A comment cannot enforce a contract; the signature can, and this test proves that no
// call site slipped back past it (a discarded error, or the call used inline as an operand).
func TestEveryOwnerCallSiteHandlesTheNamedError(t *testing.T) {
	backend := filepath.Join("..", "..")
	sites := 0
	err := filepath.Walk(backend, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(src), "\n") {
			stmt := strings.TrimSpace(line)
			if !strings.Contains(stmt, "discover.Owner(") || strings.HasPrefix(stmt, "//") {
				continue // prose about the accessor is not a call to it
			}
			sites++
			// An `if own, err := discover.Owner(); err != nil {` head is the same statement with
			// decoration: drop the keyword and the condition, then judge the assignment itself.
			stmt = strings.TrimPrefix(stmt, "if ")
			if k := strings.Index(stmt, ";"); k >= 0 {
				stmt = strings.TrimSpace(stmt[:k])
			}
			m := ownerCallSite.FindStringSubmatch(stmt)
			if m == nil {
				t.Errorf("%s:%d: %s\n\tOwner must be called as `x, err := discover.Owner()` — an unset namespace has to be REJECTED here, not turned into an empty owner", path, i+1, strings.TrimSpace(line))
				continue
			}
			if m[1] == "_" {
				t.Errorf("%s:%d: %s\n\tthe error is discarded — this is exactly how a repository ends up in a foreign namespace", path, i+1, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("read the shipped sources: %v", err)
	}
	// The audit must have READ something: no call site found means the tree moved or the accessor
	// was renamed, and the check would pass on nothing at all.
	if sites < 3 {
		t.Fatalf("found %d call sites of discover.Owner in the shipped backend, expected at least 3 — the audit read nothing meaningful", sites)
	}
}

// age backdates a user's cache entry so TTL behaviour is testable without waiting.
func age(t *testing.T, user string, by time.Duration) {
	t.Helper()
	userMu.Lock()
	defer userMu.Unlock()
	e, ok := userCache[user]
	if !ok {
		t.Fatalf("no cache entry for %q to age", user)
	}
	e.at = e.at.Add(-by)
	userCache[user] = e
}
