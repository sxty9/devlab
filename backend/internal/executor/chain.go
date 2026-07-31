// The chain as DATA (S8, principle 3): exactly one typed stage list; the motor walks it. No
// operating modes, no switch, no second form, no step-name string coupling (K-4/K-6, B 1.3b/d).
//
// This stage list is a FROZEN Welle-0 contract: implementations of the stage functions are
// B3's; the LIST itself (names, order) changes only through the orchestrator.
//
// Stage details wired elsewhere:
//   - preflight: the admission gate already ran BEFORE the document existed (B-4); this stage
//     re-derives for prompt + protocol.
//   - implement: agent on the workbench; publish after every commit (K-1); attachments staged
//     via the workspace manifest (B-6); the constitution block of the repo's CLAUDE.md is kept
//     current via mercury.ReplaceConstitutionBlock as part of implement/publish (B6 renders,
//     B3 calls).
//   - deliver-dev: build as user, install-only as root; self-repo ⇒ --handover + RequestRestart
//     (B-2/B-3).
//   - publish: push the workbench (rejected ⇒ fold-in + retry).
//   - pull-request: delivery branch + ledger intent + deliver.OpenOrAdoptPR (the ONE PR path).
package executor

import (
	"context"

	"devlab/backend/internal/model"
)

// StageSpec is one stage: its name, its applicability probe (not-applicable ONLY with
// evidence, REQ-031.3) and its effect.
type StageSpec struct {
	Name    model.Stage
	Applies func(ctx context.Context, rc *RepoCtx) (ok bool, evidence string)
	Run     func(ctx context.Context, rc *RepoCtx) error
}

// Chain returns THE chain: preflight → implement → deliver-dev → publish → pull-request
// (REQ-027). The five entries reference the motor's stage implementations (B3).
func Chain() []StageSpec {
	return []StageSpec{
		{Name: model.StagePreflight, Applies: preflightApplies, Run: preflightRun},
		{Name: model.StageImplement, Applies: implementApplies, Run: implementRun},
		{Name: model.StageDeliverDev, Applies: deliverDevApplies, Run: deliverDevRun},
		{Name: model.StagePublish, Applies: publishApplies, Run: publishRun},
		{Name: model.StagePullRequest, Applies: pullRequestApplies, Run: pullRequestRun},
	}
}
