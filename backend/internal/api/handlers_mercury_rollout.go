package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"devlab/backend/internal/github"
	"devlab/backend/internal/mercury"
	"devlab/backend/internal/runs"
	"devlab/backend/internal/workspace"
)

const rolloutTimeout = 12 * time.Minute

// rolloutBranchPrefix is the head-branch prefix that identifies a constitution-rollout PR. One such PR
// stays open per repo; a rollout updates it in place rather than opening a rival.
const rolloutBranchPrefix = "mercury-axioms/"

// rolloutRepo is one repo's outcome in the rollout report.
type rolloutRepo struct {
	Repo    string `json:"repo"`
	Branch  string `json:"branch,omitempty"`
	Changed bool   `json:"changed"`
	Commit  string `json:"commit,omitempty"`
	PRUrl   string `json:"prUrl,omitempty"`   // the pull request carrying this repo's update
	Skipped string `json:"skipped,omitempty"` // why this repo produced no change (error or "up-to-date")
}

// mercuryRollout writes the axioms + implementation rules into every holistic repo's CLAUDE.md, so
// Claude Code sees them wherever it works. It is a dry-run by default: only ?apply=true pushes.
//
// Each repo is handled by pure git plumbing over a fresh clone — no working tree is checked out, so
// a repo's local state, its current branch, and a CLAUDE.MD → CLAUDE.md rename are all irrelevant,
// and an unchanged block produces no commit (idempotent). Repos are independent: one failure is
// reported and skipped, the rest proceed.
func (s *Server) mercuryRollout(w http.ResponseWriter, r *http.Request) {
	if s.v.DevBypass() {
		writeErr(w, http.StatusBadRequest, "Rollout requires a linked GitHub account (not available under dev-bypass)")
		return
	}
	apply := r.URL.Query().Get("apply") == "true"
	u := userFrom(r)
	cookie := r.Header.Get("Cookie")

	token, err := s.userToken(u)
	if err != nil {
		writeErr(w, http.StatusForbidden, "Link your GitHub account first")
		return
	}

	// Build the generated block once from the live store.
	block, err := s.buildRolloutBlock(r.Context(), cookie)
	if err != nil {
		mercuryError(w, err)
		return
	}
	splice := func(old string) string { return mercury.SpliceMarker(old, block) }

	repos, ok := s.userRepos(r)
	if !ok {
		writeErr(w, http.StatusBadGateway, "Could not reach GitHub")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), rolloutTimeout)
	defer cancel()

	results := make([]rolloutRepo, 0, len(repos))
	for _, repo := range repos {
		results = append(results, s.rolloutOne(ctx, u.Username, repo.ID, repo.FullName, token, splice, apply))
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Repo < results[j].Repo })

	changed := 0
	for _, r := range results {
		if r.Changed {
			changed++
		}
	}
	out := map[string]any{
		"applied": apply,
		"changed": changed,
		"repos":   results,
	}
	// On a dry-run, return the exact block that would be injected, so the owner reviews the content
	// before pushing it into 18 constitutions.
	if !apply {
		out["block"] = block
	}
	writeJSON(w, http.StatusOK, out)
}

// mercuryRolloutStatus reports the last automatic rollout — when it ran, which repos changed, how many
// were already current, and which were skipped — portioned for the Mercury surface, not a raw log. It
// returns null until the first background rollout completes. Read-only: the worker owns the writing.
func (s *Server) mercuryRolloutStatus(w http.ResponseWriter, r *http.Request) {
	var last *RolloutReport
	if s.autoRollout != nil {
		last = s.autoRollout.report()
	}
	writeJSON(w, http.StatusOK, map[string]any{"last": last})
}

// rolloutOne clones/updates a repo and syncs its CLAUDE.md. Never fatal: any failure becomes a
// skipped entry so the rollout is per-repo independent.
func (s *Server) rolloutOne(ctx context.Context, user, id, fullName, token string, splice func(string) string, apply bool) rolloutRepo {
	res := rolloutRepo{Repo: id}

	if _, err := s.workspaces.Ensure(ctx, user, id, fullName, token, true); err != nil {
		res.Skipped = "clone: " + err.Error()
		return res
	}
	unlock, err := s.workspaces.Lock(user, id)
	if err != nil {
		res.Skipped = "lock: " + err.Error()
		return res
	}
	defer unlock()

	branch, err := github.DefaultBranch(ctx, token, fullName)
	if err != nil || branch == "" {
		res.Skipped = "default branch: " + errString(err)
		return res
	}
	res.Branch = branch

	wt, _ := s.workspaces.Path(user, id)
	exec := workspace.Executor{User: user, PerUser: true}
	// The constitution reaches a repo the SAME WAY every other change does: on a branch, through a pull
	// request. Pushing straight onto the default branch was the one path that bypassed review entirely —
	// and the one thing that made a protected default branch impossible.
	// One open rollout PR per repo (req 17): if one is already open, reuse ITS branch so SyncFile force-
	// updates the existing PR in place — same number, same deadline — instead of opening a rival. The older
	// rollout duplicates are collapsed onto the newest (req 18). A dry-run pushes and opens nothing, so it
	// spends no GitHub read discovering the branch.
	prBranch := rolloutBranchPrefix + rolloutStamp()
	var reuse *github.PullRequest
	if apply {
		if open, lerr := github.ListOpenPullRequests(ctx, token, fullName); lerr == nil {
			if keep, stale := rolloutPRSelection(open); keep != nil {
				reuse = keep
				prBranch = keep.Head.Ref
				s.closeStaleRolloutPRs(ctx, token, fullName, stale, *keep)
			}
		}
	}

	out, err := exec.SyncFile(ctx, wt, token, branch, "CLAUDE.md", "CLAUDE.MD", splice, !apply, prBranch)
	if err != nil {
		res.Skipped = err.Error()
		return res
	}
	res.Changed = out.Changed
	res.Commit = out.Commit
	if !out.Changed {
		res.Skipped = "up-to-date"
		return res
	}
	if !apply {
		return res // dry-run: nothing was pushed, so there is no PR to raise
	}

	// The existing rollout PR now carries the new constitution on its force-updated branch — keep its
	// number and deadline, open nothing new (req 16/17).
	if reuse != nil {
		res.PRUrl = reuse.HTMLURL
		s.trackRolloutPR(*reuse, fullName)
		return res
	}

	pr, err := github.CreatePullRequest(ctx, token, fullName, prBranch, branch,
		"Mercury: Axiome & Implementierungsregeln aktualisieren",
		"Automatisch erzeugt vom Mercury-Rollout. Aktualisiert den generierten Abschnitt der CLAUDE.md; "+
			"alles außerhalb der Marker bleibt unangetastet.\n\n🤖 Mercury")
	if err != nil {
		if found, ok := github.FindOpenPullRequest(ctx, token, fullName, prBranch); ok {
			pr = found
		} else {
			res.Skipped = "PR: " + err.Error()
			return res
		}
	}
	res.PRUrl = pr.HTMLURL
	s.trackRolloutPR(pr, fullName)
	return res
}

// rolloutPRSelection splits a repo's open PRs into the single rollout PR to KEEP — the newest, which is
// reused and lifted to the new constitution — and the older rollout duplicates to CLOSE. Non-rollout PRs
// are ignored. "Newest" is the highest PR number (monotonic). This is the whole dedup decision (req 17/18),
// kept pure so it is testable without a live GitHub.
func rolloutPRSelection(open []github.PullRequest) (keep *github.PullRequest, stale []github.PullRequest) {
	var rollout []github.PullRequest
	for _, pr := range open {
		if strings.HasPrefix(pr.Head.Ref, rolloutBranchPrefix) {
			rollout = append(rollout, pr)
		}
	}
	if len(rollout) == 0 {
		return nil, nil
	}
	sort.SliceStable(rollout, func(i, j int) bool { return rollout[i].Number > rollout[j].Number })
	k := rollout[0]
	return &k, rollout[1:]
}

// trackRolloutPR records a rollout PR in the pending-PR pool so the ordered auto-merge carries it onto the
// default branch. Idempotent (Add dedupes by repo+number): reusing an existing PR keeps its original
// deadline (req 17).
func (s *Server) trackRolloutPR(pr github.PullRequest, fullName string) {
	if s.runPRs == nil {
		return
	}
	_ = s.runPRs.Add(runs.PendingPR{
		Repo: fullName, Number: pr.Number, URL: pr.HTMLURL, RunID: "rollout",
		CreatedAt: time.Now().UTC(), MergeBy: time.Now().Add(rolloutMergeAfter()).UTC(),
	})
}

// closeStaleRolloutPRs collapses a repo's older rollout duplicates onto the newest (req 18): each is
// commented with a pointer to the one that supersedes it, closed, its branch pruned, and dropped from the
// service's pending-PR tracking so it no longer lingers in the merge queue. A close that fails leaves the
// PR tracked (retried next rollout) rather than silently forgotten.
func (s *Server) closeStaleRolloutPRs(ctx context.Context, token, fullName string, stale []github.PullRequest, keep github.PullRequest) {
	for _, pr := range stale {
		note := fmt.Sprintf("Ersetzt durch den aktuelleren Rollout-PR #%d — dieser wird geschlossen (ein offener Rollout-PR pro Repository).", keep.Number)
		_ = github.AddPullRequestComment(ctx, token, fullName, pr.Number, note)
		if err := github.ClosePullRequest(ctx, token, fullName, pr.Number); err != nil {
			log.Printf("devlabd: rollout dedup — could not close stale PR %s#%d: %v", fullName, pr.Number, err)
			continue // still open → keep tracking it; retry next rollout
		}
		_ = github.DeleteBranch(ctx, token, fullName, pr.Head.Ref)
		if s.runPRs != nil {
			_ = s.runPRs.Remove(fullName, pr.Number)
		}
		log.Printf("devlabd: rollout dedup — closed stale PR %s#%d (superseded by #%d)", fullName, pr.Number, keep.Number)
	}
}

// rolloutStamp is the per-rollout branch suffix: one branch per rollout, shared across repos, so a
// repeated rollout of the same content reuses nothing stale.
func rolloutStamp() string { return time.Now().UTC().Format("20060102-150405") }

// rolloutMergeAfter mirrors the run PRs' auto-merge window (DEVLAB_RUNS_AUTOMERGE), so constitution
// PRs and run PRs live under one rule rather than two.
func rolloutMergeAfter() time.Duration {
	if d := os.Getenv("DEVLAB_RUNS_AUTOMERGE"); d != "" {
		if pd, err := time.ParseDuration(d); err == nil && pd > 0 {
			return pd
		}
	}
	return 30 * 24 * time.Hour
}

// buildRolloutBlock reads every axiom and rule from the store, parses each, and renders the block.
func (s *Server) buildRolloutBlock(ctx context.Context, cookie string) (string, error) {
	paths, err := s.axioms.List(ctx, "")
	if err != nil {
		return "", err
	}
	var axiome, regeln []mercury.Record
	for _, p := range paths {
		switch {
		case strings.HasPrefix(p, mercury.NsAxiome+"/") && strings.HasSuffix(p, ".md"):
			if rec, ok := s.fetchRecord(ctx, cookie, p); ok {
				axiome = append(axiome, rec)
			}
		case strings.HasPrefix(p, mercury.NsRegeln+"/") && strings.HasSuffix(p, ".md"):
			if rec, ok := s.fetchRecord(ctx, cookie, p); ok {
				regeln = append(regeln, rec)
			}
		}
	}
	return mercury.RenderBlock(axiome, regeln), nil
}

func (s *Server) fetchRecord(ctx context.Context, cookie, path string) (mercury.Record, bool) {
	data, found, err := s.axioms.Get(ctx, path)
	if err != nil || !found {
		return mercury.Record{}, false
	}
	return mercury.Record{Path: path, Axiom: mercury.ParseAxiom(string(data))}, true
}

func errString(err error) string {
	if err == nil {
		return "empty"
	}
	return err.Error()
}
