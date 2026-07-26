package api

import (
	"context"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"devlab/backend/internal/discover"
	"devlab/backend/internal/mercury"
)

// A change to the axioms or implementation rules must reach every Holistic repo's CLAUDE.md on its own,
// without a manual rollout call. The scheme-writing request renders the desired block right there (the
// only place the axiom-store session exists) and hands it to this worker as a target state — the same
// snapshot discipline the run prompts use. The worker then pushes that state in the background: it never
// blocks the write, it collapses a burst of edits into ONE rollout after a short quiet period, and it
// authenticates to GitHub with the autonomous runner's linked token (from the link store), never with
// the triggering request's token — a background job holds no request session.
//
// The push itself reuses the very same per-repo primitive the manual rollout uses (rolloutOne): each
// repo is independent and idempotent — a failing repo skips only itself, an unchanged block yields no
// commit.

// rolloutDefaultDebounce is the quiet period a burst of writes must settle into before one rollout runs.
const rolloutDefaultDebounce = 15 * time.Second

// RolloutSkip is one repo the rollout could not update, with a short reason (not a raw log).
type RolloutSkip struct {
	Repo   string `json:"repo"`
	Reason string `json:"reason"`
}

// RolloutReport is the portioned outcome of the last automatic rollout, shown in Mercury: when it ran,
// which repos got a new commit, how many were already current, and which were skipped and why.
type RolloutReport struct {
	At        time.Time     `json:"at"`
	Repos     int           `json:"repos"`             // repos considered
	Changed   []string      `json:"changed"`           // repo ids that received a new commit
	Unchanged int           `json:"unchanged"`         // already current (idempotent, no commit)
	Skipped   []RolloutSkip `json:"skipped,omitempty"` // repos that failed, each with a short reason
	Error     string        `json:"error,omitempty"`   // whole-rollout failure (no runner token, discovery down)
}

// autoRollout debounces axiom/rule writes into a single background CLAUDE.md rollout. It holds the most
// recent desired block and a timer; each enqueue re-arms the timer, so writes in quick succession result
// in exactly one rollout of the newest block.
type autoRollout struct {
	s        *Server
	debounce time.Duration
	// doFn is the push seam: nil in production (the real per-repo rollout runs), set by tests to a fake
	// so the debounce/bundling behaviour is exercised without a GitHub token or network.
	doFn func(ctx context.Context, block string) RolloutReport

	mu      sync.Mutex
	baseCtx context.Context // cancelled on shutdown; drains an in-flight rollout
	desired string          // the latest rendered block awaiting rollout (snapshot)
	pending bool            // a block is waiting to be rolled out
	running bool            // a rollout is in flight (serialize; never overlap)
	timer   *time.Timer

	reportMu sync.Mutex
	last     *RolloutReport
}

// newAutoRollout builds the worker with a default (env-overridable) debounce and a background base
// context — StartRolloutWorker later swaps in a cancellable one so shutdown can drain an in-flight push.
func newAutoRollout(s *Server) *autoRollout {
	d := rolloutDefaultDebounce
	if v := strings.TrimSpace(os.Getenv("DEVLAB_MERCURY_ROLLOUT_DEBOUNCE")); v != "" {
		if pd, err := time.ParseDuration(v); err == nil && pd >= 0 {
			d = pd
		} else {
			log.Printf("devlabd: ignoring DEVLAB_MERCURY_ROLLOUT_DEBOUNCE=%q (want a non-negative duration), keeping %s", v, d)
		}
	}
	return &autoRollout{s: s, debounce: d, baseCtx: context.Background()}
}

// StartRolloutWorker binds the worker's lifetime to ctx so an in-flight rollout drains on shutdown, and
// logs whether the background rollout can actually push (it needs the runner's linked token, exactly as
// the scheduler does). Called from main alongside StartScheduler.
func (s *Server) StartRolloutWorker(ctx context.Context) {
	if s.autoRollout == nil {
		return
	}
	s.autoRollout.mu.Lock()
	s.autoRollout.baseCtx = ctx
	s.autoRollout.mu.Unlock()
	if reason := s.rolloutReadiness(); reason != "" {
		log.Printf("devlabd: mercury auto-rollout will NOT push (%s) — axiom edits still recompose runs", reason)
		return
	}
	log.Printf("devlabd: mercury auto-rollout ENABLED — debounce=%s tokenUser=%s", s.autoRollout.debounce, runnerTokenUser())
}

// rolloutReadiness returns "" when the background rollout can push, else a human reason it cannot. It
// mirrors the runner's requirements: not under dev-bypass, a link store, and a configured runner OS
// user + token user.
func (s *Server) rolloutReadiness() string {
	if s.v.DevBypass() {
		return "dev-bypass: no linked GitHub account"
	}
	if s.links == nil {
		return "link store unavailable"
	}
	if strings.TrimSpace(os.Getenv("DEVLAB_RUNS_USER")) == "" || runnerTokenUser() == "" {
		return "runner not configured (DEVLAB_RUNS_USER / DEVLAB_RUNS_TOKEN_USER)"
	}
	return ""
}

// enqueue records the freshly rendered block as the target state and (re)arms the debounce timer. It
// returns at once — the writing request never waits for the rollout.
func (a *autoRollout) enqueue(block string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.desired = block
	a.pending = true
	if a.timer != nil {
		a.timer.Stop()
	}
	a.timer = time.AfterFunc(a.debounce, a.flush)
}

// flush runs (from the debounce timer's own goroutine) at most one rollout at a time. If one is already
// in flight it leaves `pending` set so the run re-arms when it finishes; otherwise it takes the newest
// block and rolls it out, then re-arms if another write landed meanwhile.
func (a *autoRollout) flush() {
	a.mu.Lock()
	if a.running || !a.pending {
		a.mu.Unlock()
		return
	}
	block := a.desired
	a.pending = false
	a.running = true
	ctx := a.baseCtx
	a.mu.Unlock()

	report := a.do(ctx, block)

	a.reportMu.Lock()
	a.last = &report
	a.reportMu.Unlock()

	a.mu.Lock()
	a.running = false
	rearm := a.pending // a write arrived during the rollout → push the newest block next
	if rearm {
		if a.timer != nil {
			a.timer.Stop()
		}
		a.timer = time.AfterFunc(a.debounce, a.flush)
	}
	a.mu.Unlock()
}

// do dispatches to the test seam if set, else to the real per-repo rollout.
func (a *autoRollout) do(ctx context.Context, block string) RolloutReport {
	if a.doFn != nil {
		return a.doFn(ctx, block)
	}
	return a.rollout(ctx, block)
}

// rollout pushes the block into every Holistic repo, authenticating with the runner's linked token (the
// same account the autonomous runner commits as), per-repo independent and idempotent.
func (a *autoRollout) rollout(ctx context.Context, block string) RolloutReport {
	s := a.s
	rep := RolloutReport{At: time.Now().UTC()}
	if reason := s.rolloutReadiness(); reason != "" {
		rep.Error = reason
		return rep
	}
	osUser := strings.TrimSpace(os.Getenv("DEVLAB_RUNS_USER"))
	tokenUser := runnerTokenUser()

	token, err := s.links.Token(tokenUser)
	if err != nil {
		rep.Error = "Runner-Token nicht verfügbar (verknüpftes GitHub-Konto " + tokenUser + "): " + err.Error()
		return rep
	}
	repos, err := discover.ReposForUser(ctx, tokenUser, token)
	if err != nil {
		rep.Error = "Repo-Ermittlung fehlgeschlagen: " + err.Error()
		return rep
	}

	cctx, cancel := context.WithTimeout(ctx, rolloutTimeout)
	defer cancel()
	splice := func(old string) string { return mercury.SpliceMarker(old, block) }

	rep.Repos = len(repos)
	for _, repo := range repos {
		res := s.rolloutOne(cctx, osUser, repo.ID, repo.FullName, token, splice, true)
		switch {
		case res.Changed:
			rep.Changed = append(rep.Changed, res.Repo)
		case res.Skipped == "up-to-date":
			rep.Unchanged++ // idempotent: no commit — the silent, expected majority
		default:
			rep.Skipped = append(rep.Skipped, RolloutSkip{Repo: res.Repo, Reason: res.Skipped})
		}
	}
	sort.Strings(rep.Changed)
	log.Printf("devlabd: mercury auto-rollout done — %d repos, %d changed, %d unchanged, %d skipped",
		rep.Repos, len(rep.Changed), rep.Unchanged, len(rep.Skipped))
	return rep
}

// report returns a copy of the last completed rollout, or nil if none has run yet.
func (a *autoRollout) report() *RolloutReport {
	a.reportMu.Lock()
	defer a.reportMu.Unlock()
	if a.last == nil {
		return nil
	}
	r := *a.last
	return &r
}

// runnerTokenUser is the link-store key whose GitHub token the autonomous runner (and the background
// rollout) authenticate with: DEVLAB_RUNS_TOKEN_USER, falling back to DEVLAB_RUNS_USER. The single
// source of truth so the rollout and the scheduler never drift on which account pushes.
func runnerTokenUser() string {
	if u := strings.TrimSpace(os.Getenv("DEVLAB_RUNS_TOKEN_USER")); u != "" {
		return u
	}
	return strings.TrimSpace(os.Getenv("DEVLAB_RUNS_USER"))
}
