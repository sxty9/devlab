package executor

// The pull request must state what the CHAIN did, not only what the agent believed at the time it
// wrote its report. The agent writes at the end of implement, from a sandbox with neither sudo nor
// push rights, so it truthfully reports "built, but not live". Two stages later the pipeline
// deploys. Without the chain's own line, the pull request preserves the agent's snapshot as if it
// were the outcome — measured 2026-07-31 on presentr #7, whose service was installed and running
// while its own pull request read "NOT live".

import (
	"strings"
	"testing"

	"devlab/backend/internal/execstate"
	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
)

func bodyCtx(report, deliveredCommit string) *RepoCtx {
	return &RepoCtx{
		Repo:            "org/app",
		Run:             runs.Run{Kind: model.KindTodo, Title: "upload files"},
		Doc:             execstate.Doc{ID: "exec_1"},
		deliveryBase:    "aaaaaaaabbbb",
		head:            "ccccccccdddd",
		report:          report,
		deliveredCommit: deliveredCommit,
	}
}

func TestPRBodyStatesTheChainsOwnDeliveryNextToTheAgentsReport(t *testing.T) {
	const agentReport = "Gebaut und getestet, aber NICHT live — der Runner hat kein sudo und keine Push-Rechte."

	t.Run("delivered", func(t *testing.T) {
		got := prBody(bodyCtx(agentReport, "ccccccccdddd"))
		if !strings.Contains(got, "**Dev delivery:** installed and running at cccccccc") {
			t.Fatalf("the chain does not state its own delivery:\n%s", got)
		}
		if !strings.Contains(got, "AFTER the report below was written") {
			t.Fatalf("the report is not placed in time against the delivery:\n%s", got)
		}
		if !strings.Contains(got, agentReport) {
			t.Fatal("the agent's report was dropped — it is evidence, not noise")
		}
		if strings.Index(got, "**Dev delivery:**") > strings.Index(got, agentReport) {
			t.Fatal("the outcome must stand ABOVE the snapshot, or the snapshot is read as the outcome")
		}
	})

	t.Run("not delivered", func(t *testing.T) {
		got := prBody(bodyCtx(agentReport, ""))
		if !strings.Contains(got, "**Dev delivery:** not performed in this run") {
			t.Fatalf("a delivery that did NOT happen must be stated too:\n%s", got)
		}
		if strings.Contains(got, "installed and running") {
			t.Fatal("a delivery was claimed that never happened")
		}
	})
}
