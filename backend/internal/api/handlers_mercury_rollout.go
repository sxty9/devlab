package api

import (
	"context"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"devlab/backend/internal/aigentic"
	"devlab/backend/internal/github"
	"devlab/backend/internal/mercury"
	"devlab/backend/internal/runs"
	"devlab/backend/internal/workspace"
)

const rolloutTimeout = 12 * time.Minute

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
		mercuryError(w, http.StatusBadGateway, err)
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
	prBranch := "mercury-axioms/" + rolloutStamp()
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
	// Tracked like a run PR, so the same ordered auto-merge carries it onto the default branch.
	if s.runPRs != nil {
		_ = s.runPRs.Add(runs.PendingPR{
			Repo: fullName, Number: pr.Number, URL: pr.HTMLURL, RunID: "rollout",
			CreatedAt: time.Now().UTC(), MergeBy: time.Now().Add(rolloutMergeAfter()).UTC(),
		})
	}
	return res
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
	paths, _, err := aigentic.GraveList(ctx, cookie, "")
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
	data, found, _, err := aigentic.GraveGet(ctx, cookie, path)
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
