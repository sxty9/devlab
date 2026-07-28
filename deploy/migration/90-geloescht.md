# 90 — Deleted in Welle 0 (ARCHITEKTUR §10) — every line names its replacement

## Backend machinery

| Deleted | Replacement |
|---|---|
| `internal/preview/` + `api/handlers_preview.go` + 3 routes | removed (previews retired; B-18) |
| `api/handlers_mercury_rollout.go`, `api/rollout_auto.go` (+test), `mercury/rollout.go` (+test), `mercury/decompose.go` (+test), `deploy/holistic-sync-local`, root `axioms/` | rollout abolished (REQ-002); marker rendering → `mercury/claudemd.go` (B6); constitution reaches repos via the prompt |
| `api/handlers_mercury_migrate.go` | one-time decompose migration done; new data migration = `cmd/devlab-migrate` (B13) |
| `workspace/plumb.go` (+test), `workspace/mergeref_test.go` + `MergeRef` | removed with the PR-reconstruction path; delivery branches ride the ledger (B4) |
| dev-branch/reset machinery in `gitops.go` (EnsureDevBranch, ResetToRemote, CleanWorktree hard-reset, ResetDevToDefault) + `workspace/devbranch_test.go`, `workspace/reset_test.go` | `internal/workbench/` (B1; K-1 — fold-in only, no reset primitive) with its own tests per F8 |
| `runs/scheduler.go` (+`scheduler_test.go`, `scheduler_restart_test.go`, `stranded_test.go`), `runs/active_marker.go` | `internal/execstate/` + `internal/sched/` (B2); marker = only `mercury/restart.json` (B-13) |
| `api/handlers_mercury_runs_exec.go`, `_wave.go` (+test) | chain motor → `internal/executor/` (B3) |
| `api/handlers_mercury_runs_stream.go` (+test) | → `executor.CompactStream` (B3 ports from `git show ae5eed5:…_stream.go`, F7/F11) |
| `api/handlers_mercury_runs_rollback.go` (+test) | logic → `deliver/rollback.go`, handler → `handlers_mercury_deliveries.go` (B4 ports the test from `git show ae5eed5:…_rollback_test.go`) |

## Old tests that tested ONLY deleted machinery (checked individually)

| Deleted test | Replacement test lives at |
|---|---|
| `api/runs_deploy_test.go` | B5 (deploy) + B3 (chain deliver-dev stage) |
| `api/runs_infra_test.go` (isInfraError) | B3 `faultclass` classification tests (K-5) |
| `api/handlers_mercury_runs_steps_test.go` (deliveryChain modes) | B3 chain tests — no modes exist anymore (REQ-027) |
| `api/handlers_mercury_runs_test.go` (type filter over old summaries) | B8 handler tests over the new ResultStore |
| `api/handlers_mercury_runs_live_test.go` (resumeOrNew prompt snapshot) | B2 `runs/resume_test.go` (PlanResume) |
| `api/handlers_mercury_runs_inflight_test.go` (assembleInFlight) | B2 slot/active projections (`handlers_slots` tests) |
| `api/handlers_mercury_runs_stream_test.go` | B3 `executor` stream tests (F7) |
| `api/run_tuning_test.go` | B8 tuning validation tests (three tunables incl. time budget) |
| `api/mercury_branch_test.go` (PR marker/isMercuryPR) | B4 origin-status tests — recognition is the LEDGER now, not a body marker (REQ-033) |
| `api/handlers_mercury_runs_assign_test.go` | B6 re-ports it from `git show ae5eed5:…_assign_test.go` against `runs/plan.go` |
| `runs/abortcause_test.go` (ErrRunAborted) | B2 cancel-path tests (REQ-013.5) |
| `runs/scheduler_test.go` (~886 lines) | NOT ported (B 1.3c); B2 uses the rescue-branch copy as a test TEMPLATE only |

## Deploy artifacts

| Deleted | Replacement |
|---|---|
| `deploy/devlab-restart-idle` | `deploy/devlab-restart-when-free` (ready-socket probe; W2 — one restart path) |
| `deploy/devlab-deploy` + `deploy/deploy.d.goservice` + `deploy/deploy.d.example-devlab` | `deploy/devlab-install` (generic install-only; B-44 — per-repo script mechanics retired) |
| `deploy/devlab-preview`, `deploy/PREVIEW-peruser-plan.md` | removed (previews retired) |

## Frontend

| Deleted | Replacement |
|---|---|
| `src/views/mercuryPipeline.ts` (+test) | none — the client renders ONLY the server stage array (B-17/B-35) |
| `src/lib/mercury.ts`, `src/lib/axioms.ts` | server-side single sources (axiom store, model vocabulary) |
| `src/data/mockSource.ts`, `src/mock/workspace.ts` | `src/data/stubSource.ts` (defined empty states, NO behavior clone; B-24) |
| old `views/MercuryView.tsx` monolith, `views/RunsView.tsx`, `views/TodosView.tsx`, `views/MercuryExecutions.tsx` | `views/mercury/{MercuryView,tasks/*,exec/*}` (B6/B8/B9; reference = `git show ae5eed5:<path>`) |
