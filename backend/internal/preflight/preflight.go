// Package preflight derives a task's state at a repo (K-3): collecting is impure (git, ledger,
// GitHub), deriving is pure. The state has NO storage location — it is observed fresh each time
// (TaskState "unknown" when a source is unreachable; never guessed).
package preflight

import (
	"context"
	"fmt"
	"time"

	"devlab/backend/internal/live"
	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
)

// Sources are the observation points preflight collects from — fixture-testable. The adapter
// in cmd/devlabd composes them from the workbench (git), the delivery ledger, the result pool
// and the GitHub client; fixtures substitute the whole interface.
type Sources interface {
	// WorkbenchState reports whether ONE named branch is ahead of the default branch, and its
	// head commit. The branch asked about is the task's own — a branch that does not exist yet
	// is not an error, it is "nothing here".
	WorkbenchState(ctx context.Context, repo, branch string) (aheadOfDefault bool, headCommit string, err error)
	// RunDeliveries returns every ledger delivery this run produced at repo (joined via the
	// run's executions), oldest first. Empty when the run never delivered there.
	RunDeliveries(runID, repo string) ([]runs.Delivery, error)
	// OpenPRByHead returns the open PR with the given head branch, nil when none.
	OpenPRByHead(ctx context.Context, repo, head string) (*model.PRRef, error)
	// ContainedInDefault reports whether commit is contained in the default branch.
	ContainedInDefault(ctx context.Context, repo, commit string) (bool, error)
}

// Finding is the derived observation: the task state WITH its evidence (REQ-031.3). Err names
// an unreachable source (state unknown then). OpenDelivery carries the open ledger record of
// the rest path (implemented-undelivered), so the chain adopts it instead of duplicating.
type Finding struct {
	State        model.TaskState `json:"state"`
	Evidence     []string        `json:"evidence"`
	ObservedAt   time.Time       `json:"observedAt"`
	OpenPR       *model.PRRef    `json:"openPr,omitempty"`
	OpenDelivery *runs.Delivery  `json:"openDelivery,omitempty"`
	Err          string          `json:"err,omitempty"`
}

// Derive observes one repo for one run: not-implemented | implemented-undelivered | delivered |
// unknown, each with evidence — NEVER from a stored flag (K-3). Pure over what Sources report;
// an unreachable source yields unknown plus the error (never a guess).
//
// Rules (REQ-020, D 50). Every rule reads THIS task's own branch — one branch per run, named after
// it — so commits on it are this task's by construction and no rule has to ask whose they are.
//   - an OPEN ledger delivery of this run at repo   ⇒ implemented-undelivered
//   - this task's branch ahead of the default one   ⇒ implemented-undelivered
//   - ≥ 1 delivery of this run merged, none open    ⇒ delivered
//   - otherwise                                     ⇒ not-implemented
func Derive(ctx context.Context, src Sources, repo string, run runs.Run) (Finding, error) {
	f := Finding{State: model.TaskUnknown, ObservedAt: time.Now().UTC()}

	// The branch this task owns in this repository — derived, never looked up, so the observation
	// and the chain can never disagree about which branch is being talked about.
	create := false
	for _, t := range run.Targets {
		if t.Repo == repo {
			create = t.Create
			break
		}
	}
	ahead, head, err := src.WorkbenchState(ctx, repo, runs.TaskBranch(create, run.Title, run.ID))
	if err != nil {
		f.Err = "workbench state unreachable: " + err.Error()
		f.Evidence = append(f.Evidence, f.Err)
		return f, fmt.Errorf("preflight %s: %w", repo, err)
	}
	dels, err := src.RunDeliveries(run.ID, repo)
	if err != nil {
		f.Err = "delivery ledger unreachable: " + err.Error()
		f.Evidence = append(f.Evidence, f.Err)
		return f, fmt.Errorf("preflight %s: %w", repo, err)
	}

	var open *runs.Delivery
	var merged *runs.Delivery
	for i := range dels {
		d := dels[i]
		if d.OpenState() {
			open = &d
			continue
		}
		if d.MergedAt != nil {
			merged = &d
		}
	}

	switch {
	case open != nil:
		f.State = model.TaskImplementedUndelivered
		f.OpenDelivery = open
		f.Evidence = append(f.Evidence, fmt.Sprintf("open delivery %s (branch %s, %s..%s) recorded in the ledger",
			open.ID, open.Branch, short(open.FromCommit), short(open.ToCommit)))
		// The PR probe is auxiliary evidence: its failure never flips the state (the ledger
		// already attests the work) — it is named instead of guessed about.
		if pr, perr := src.OpenPRByHead(ctx, repo, open.Branch); perr != nil {
			f.Evidence = append(f.Evidence, "PR probe unavailable: "+perr.Error())
		} else if pr != nil {
			f.OpenPR = pr
			f.Evidence = append(f.Evidence, fmt.Sprintf("open PR #%d (%s)", pr.Number, pr.URL))
		} else {
			f.Evidence = append(f.Evidence, "no open PR for the delivery branch yet")
		}
	case ahead:
		// This task's OWN branch carries commits the default branch does not. Whose they are is
		// not a question any more: the branch belongs to this run and to no other.
		f.State = model.TaskImplementedUndelivered
		f.Evidence = append(f.Evidence,
			fmt.Sprintf("this task's branch is ahead of the default branch @%s; no delivery of it is recorded", short(head)))
	case merged != nil:
		f.State = model.TaskDelivered
		f.Evidence = append(f.Evidence, fmt.Sprintf("delivery %s merged at %s",
			merged.ID, merged.MergedAt.UTC().Format(time.RFC3339)))
	default:
		f.State = model.TaskNotImplemented
		f.Evidence = append(f.Evidence,
			fmt.Sprintf("this task's branch carries nothing beyond the default branch @%s; no delivery recorded", short(head)))
	}
	return f, nil
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	if sha == "" {
		return "unknown"
	}
	return sha
}

// startupNoticeKind labels the startup-reconcile notices in the notice pool.
const startupNoticeKind = "startup-reconcile"

// SyncStartupTodos is the startup reconciliation (REQ-039.2, B-5): every open todo whose work
// verifiably arrived in the default branch is checked off via a SYNTHETIC completed result
// (MergedAt set, authorship autonomous, one preflight stage "arrived in default branch
// @<commit>" with the evidence commit) plus a notice. GitHub unreachable ⇒ named deferral,
// never a guess. Returns how many todos it reconciled.
//
// A todo counts as arrived when every target repo has a merged ledger delivery of this run —
// confirmed against the default branch where the probe is reachable. Idempotent: a run that
// already carries a completed (MergedAt) result is never touched again.
func SyncStartupTodos(ctx context.Context, src Sources, rs *runs.Store, res *runs.ResultStore, n *runs.NoticeStore, pub live.Publisher) (int, error) {
	all, err := rs.List()
	if err != nil {
		return 0, err
	}
	reconciled := 0
	deferred := []string{}
	for _, r := range all {
		if r.Kind != model.KindTodo || len(r.Targets) == 0 {
			continue
		}
		prior, err := res.ForRun(r.ID)
		if err != nil {
			return reconciled, err
		}
		done := false
		for _, pr := range prior {
			if pr.MergedAt != nil {
				done = true // already checked off — idempotency
				break
			}
		}
		if done {
			continue
		}

		type arrival struct {
			repo   string
			commit string
			at     time.Time
		}
		arrivals := make([]arrival, 0, len(r.Targets))
		arrived := true
		deferredThis := false
		for _, tgt := range r.Targets {
			dels, err := src.RunDeliveries(r.ID, tgt.Repo)
			if err != nil {
				return reconciled, err
			}
			var merged *runs.Delivery
			for i := range dels {
				if dels[i].MergedAt != nil {
					merged = &dels[i]
				}
			}
			if merged == nil {
				arrived = false
				break
			}
			// Confirm against the default branch. An unreachable probe is a NAMED deferral
			// for this todo — never a guess (REQ-039.2).
			if merged.ToCommit != "" {
				in, perr := src.ContainedInDefault(ctx, tgt.Repo, merged.ToCommit)
				if perr != nil {
					deferredThis = true
					arrived = false
					break
				}
				if !in {
					arrived = false
					break
				}
			}
			arrivals = append(arrivals, arrival{repo: tgt.Repo, commit: merged.ToCommit, at: merged.MergedAt.UTC()})
		}
		if deferredThis {
			deferred = append(deferred, r.Title)
			continue
		}
		if !arrived || len(arrivals) == 0 {
			continue
		}

		now := time.Now().UTC()
		mergedAt := arrivals[0].at
		repos := make([]model.RepoPipeline, 0, len(arrivals))
		for _, a := range arrivals {
			if a.at.After(mergedAt) {
				mergedAt = a.at
			}
			at := a.at
			stage := model.StageView{
				Stage:    model.StagePreflight,
				State:    model.StepExecuted,
				Log:      "arrived in default branch @" + short(a.commit),
				Evidence: "commit " + a.commit + " contained in the default branch",
				EndedAt:  &at,
			}
			rp := model.RepoPipeline{Repo: a.repo, Stages: []model.StageView{stage}, TaskState: model.TaskDelivered}
			rp.Done, rp.Succeeded = model.PipelineSucceeded(rp.Stages)
			repos = append(repos, rp)
		}
		synth := runs.Result{
			ID:        runs.NewResultID(now),
			RunID:     r.ID,
			RunTitle:  r.Title,
			Kind:      r.Kind,
			StartedAt: now,
			EndedAt:   &now,
			MergedAt:  &mergedAt,
			Repos:     repos,
			Report:    "Checked off by the startup reconciliation: the work of this todo verifiably arrived in the default branch.",
			Requested: model.Authorship{
				Created: model.Actor{Autonomous: true}, CreatedAt: now,
				Updated: model.Actor{Autonomous: true}, UpdatedAt: now,
			},
			Synthetic: true,
		}
		if err := res.Put(synth); err != nil {
			return reconciled, err
		}
		_ = n.Add(runs.Notice{
			Kind:    startupNoticeKind,
			RunID:   r.ID,
			RunName: r.Title,
			Reason:  "checked off at startup: work arrived in the default branch",
		})
		reconciled++
	}
	if len(deferred) > 0 {
		_ = n.Add(runs.Notice{
			Kind:   startupNoticeKind,
			Reason: fmt.Sprintf("startup reconciliation deferred for %d todo(s) — source of truth unreachable, no guess made", len(deferred)),
		})
	}
	if pub != nil && (reconciled > 0 || len(deferred) > 0) {
		pub.Publish(live.TopicRuns)
		pub.Publish(live.TopicNotices)
	}
	return reconciled, nil
}
