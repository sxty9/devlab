// The golden-fixture contract test (W-K): canonical wire values marshal EXACTLY to
// contract/fixtures/*.json; src/types.contract.test.ts parses the same files against
// types.ts. Drift breaks both builds. Regenerate deliberately with UPDATE_FIXTURES=1
// (a contract change — only the orchestrator/integration agent may commit it).
package model_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"devlab/backend/internal/model"
	"devlab/backend/internal/preflight"
	"devlab/backend/internal/runs"
)

func fixturesDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("..", "..", "..", "contract", "fixtures")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func check(t *testing.T, name string, v any) {
	t.Helper()
	got, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("%s: marshal: %v", name, err)
	}
	got = append(got, '\n')
	path := filepath.Join(fixturesDir(t), name+".json")
	if os.Getenv("UPDATE_FIXTURES") == "1" {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: fixture missing (run with UPDATE_FIXTURES=1 to create): %v", name, err)
	}
	if string(want) != string(got) {
		t.Errorf("%s: wire drift against the golden fixture.\n-- fixture --\n%s\n-- marshal --\n%s", name, want, got)
	}
}

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

func TestContractFixtures(t *testing.T) {
	t0 := ts("2026-07-28T03:00:00Z")
	t1 := ts("2026-07-28T03:10:00Z")
	t2 := ts("2026-07-28T04:00:00Z")
	alice := model.Actor{User: "alice"}
	auto := model.Actor{Autonomous: true, OnBehalfOf: "alice"}
	authorship := model.Authorship{Created: alice, CreatedAt: t0, Updated: auto, UpdatedAt: t1}

	check(t, "health", model.Health{OK: true, Mode: "devlab/0.2.0"})

	// The canonical user WATCHES sessions but may not speak into them — the split is pinned in the
	// contract itself, so a build that collapses the two rights into one fails on both sides.
	check(t, "user", model.User{
		Username: "alice", DisplayName: "Alice", IsAdmin: false, CanUseDevlab: true,
		CanWatchSession: true, CanSpeakSession: false,
		GithubLinked: true, GithubLogin: "alice-gh",
	})

	check(t, "session_line", model.SessionLine{
		At: t0, Repo: "svc-a", From: "alice", Text: "stop and check the migration first",
	})

	check(t, "repo", model.Repo{
		ID: "svc-a", Name: "svc-a", FullName: "org/svc-a", Kind: "service",
		Description: "A service.", Language: "Go", Icon: "go", Tint: "accent", Permission: "push",
	})

	check(t, "repo_data", model.RepoData{
		Branches:  []model.Branch{{Name: "main", IsDefault: true, Ahead: 0, Behind: 0, Updated: "2h ago"}},
		Tree:      []model.FileNode{{ID: "cmd", Name: "cmd", Kind: "dir"}},
		Files:     map[string]model.FileContent{},
		Changes:   []model.Change{{Path: "a.go", Status: "modified", Additions: 1, Deletions: 2, Staged: false}},
		Commits:   []model.Commit{{Hash: "abc1234", Message: "init", Author: "alice", Time: "1d", DotLane: 0, Lines: []model.CommitLine{}}},
		Worktrees: []model.Worktree{}, Vision: []model.VisionDoc{}, Claude: []model.ClaudeMsg{},
		Terminal:    []model.TermLine{},
		Stages:      []model.RepoStage{{ID: "code", Label: "Code", State: "active", Hint: "Uncommitted changes"}},
		DefaultTabs: []model.Tab{{ID: "structure:svc-a", Title: "svc-a — structure", Kind: "structure"}},
		ActiveTabID: "structure:svc-a",
		Structure:   []model.StructureSection{},
	})

	budget := model.Duration(3 * time.Hour)
	check(t, "run", runs.Run{
		ID: "run_x", Kind: model.KindTodo, Title: "Ship it", Task: "Do the thing.",
		Targets:        []runs.Target{{Repo: "svc-a"}, {Repo: "new-svc", Create: true}},
		DueAt:          &t2,
		Tuning:         runs.Tuning{Model: "opus", Effort: "max", TimeBudget: &budget},
		PromptSnapshot: "# Task\n\nDo the thing.", Attachments: []runs.AttachmentRef{{
			ID: "att_1", Filename: "sketch.png", MIME: "image/png", Size: 123, SHA256: "ab12", UploadedAt: t0, UploadedBy: "alice",
		}},
		Authorship: authorship,
	})

	active := true
	check(t, "run_input", runs.RunInput{
		Kind: model.KindAuto, Title: "Nightly sweep",
		AxiomIDs: []string{"ax_1"}, Schedule: &runs.ScheduleSpec{Kind: runs.Daily, TimeOfDay: "03:00"},
		Active: &active, Tuning: &runs.Tuning{Effort: "max"},
	})

	sv := model.StageView{
		Stage: model.StageDeliverDev, State: model.StepNotApplicable,
		Reason: "library — nothing to deploy", Evidence: "no service CLI, no cmd/<id>d",
		StartedAt: &t0, EndedAt: &t1,
	}
	check(t, "stage_view", sv)

	pipeline := model.RepoPipeline{
		Repo: "svc-a",
		Stages: []model.StageView{
			{Stage: model.StagePreflight, State: model.StepExecuted, StartedAt: &t0, EndedAt: &t0},
			{Stage: model.StageImplement, State: model.StepRunning, StartedAt: &t0},
			{Stage: model.StageDeliverDev, State: model.StepPending},
			{Stage: model.StagePublish, State: model.StepPending},
			{Stage: model.StagePullRequest, State: model.StepPending},
		},
		TaskState: model.TaskNotImplemented,
	}
	execView := model.ExecutionView{
		ID: "exec_1", RunID: "run_x", RunTitle: "Ship it", Kind: model.KindTodo,
		Phase:        model.PhaseRunning,
		Continuation: &model.ContinuationView{Repo: "svc-a", Stage: model.StageImplement},
		Repos:        []model.RepoPipeline{pipeline},
		Usage:        model.UsageView{InputTokens: 1000, OutputTokens: 200, CostUSD: 0.05},
		Requested:    authorship, CreatedAt: t0, StartedAt: t0, UpdatedAt: t1,
		DeliveredCommit: "abc1234",
	}
	check(t, "execution_view", execView)

	check(t, "slot_overview", model.SlotOverview{
		Capacity: 3, Occupied: 1, OverloadActive: false, RestartPending: false,
		Active:   []model.ExecutionView{execView},
		Deferred: []model.ExecutionView{},
		QueuedStarts: []model.QueuedStart{{
			RunID: "run_y", Title: "Later run", By: alice, At: t1,
		}},
	})

	check(t, "start_outcome", model.StartOutcome{
		Started: false, NotStarted: "already delivered",
		TaskStates:   map[string]model.TaskState{"svc-a": model.TaskDelivered},
		TaskEvidence: map[string][]string{"svc-a": {"delivery dlv_1 merged at 2026-07-28T05:00:00Z; the todo text still asks for exactly this work (editorial edits aside)"}},
		Suggestion:   &model.DeferSuggestion{ExecutionID: "exec_2", Reason: "longest idle", Score: 7},
	})

	check(t, "restart_state", model.RestartState{
		Pending: true, RequestedBy: auto, RequestedAt: t1, Deadline: t2,
		QueuedStarts: []model.QueuedStart{{RunID: "run_x", Title: "Ship it", By: alice, At: t1}},
	})

	check(t, "delivery", model.Delivery{
		ID: "dlv_1", Repo: "svc-a", Branch: "fix/login-flow", FromCommit: "a1", ToCommit: "a2",
		PRNumber: 41, PRURL: "https://example.invalid/pr/41", CreatedAt: t0, MergedAt: &t2,
		Stage: "merged",
	})

	check(t, "notice", model.Notice{
		ID: "not_1", Kind: "delivery-gap", Repo: "svc-a",
		Text: "delivery not yet set up", NextStep: "run the service setup once",
		Count: 3, FirstAt: t0, LastAt: t1, Read: false,
	})

	check(t, "ports", []model.PortAllocation{
		{Port: 8781, Service: "devlab", Routed: true, Bound: true},
		{Port: 8542, Service: "prizm", Routed: true, Bound: true, Conflict: true},
	})

	succeeded := true
	check(t, "calendar", model.RunCalendar{
		From: "2026-07-20T02:00:00Z", To: "2026-08-27T03:00:00Z",
		Occurrences: []model.RunOccurrence{
			{RunID: "run_x", RunTitle: "Ship it", Kind: model.KindTodo, At: "2026-07-28T04:00:00Z", Schedule: "once"},
			{RunID: "run_a", RunTitle: "Nightly sweep", Kind: model.KindAuto, At: "2026-07-20T02:00:00Z", ResultID: "exec_0", Succeeded: &succeeded},
		},
	})

	check(t, "coverage", model.RunCoverage{
		Covered: map[string][]string{"ax_1": {"run_a"}},
		Index:   map[string]string{"ax_1": "axiome/architecture/minimalism.md"},
		Axioms:  map[string]string{"ax_1": "Minimalism"},
		Pending: true,
	})

	check(t, "service_config", model.ServiceConfig{
		MaxConcurrency: 3, DefaultTimeBudget: model.Duration(3 * time.Hour), AutomergeWindow: model.Duration(720 * time.Hour),
	})

	check(t, "usage", model.UsageView{InputTokens: 123456, OutputTokens: 6543, CostUSD: 12.34})

	check(t, "finding", preflight.Finding{
		State:      model.TaskImplementedUndelivered,
		Evidence:   []string{"mercury-dev ahead of main @abc1234", "no open delivery"},
		ObservedAt: t0,
		OpenPR:     &model.PRRef{Number: 41, URL: "https://example.invalid/pr/41", HeadBranch: "fix/login-flow"},
	})
}

// The chain list and the terminal-state rule are part of the frozen vocabulary.
func TestChainVocabulary(t *testing.T) {
	want := []model.Stage{model.StagePreflight, model.StageImplement, model.StageDeliverDev, model.StagePublish, model.StagePullRequest}
	got := model.ChainStages()
	if len(got) != len(want) {
		t.Fatalf("chain = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("chain[%d] = %s, want %s", i, got[i], want[i])
		}
	}
	terminal := map[model.StepState]bool{
		model.StepPending: false, model.StepRunning: false,
		model.StepExecuted: true, model.StepFailed: true, model.StepNotApplicable: true, model.StepNotExecuted: true,
	}
	for st, want := range terminal {
		if st.Terminal() != want {
			t.Errorf("%s.Terminal() = %v, want %v", st, st.Terminal(), want)
		}
	}
}
