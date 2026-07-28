// Prompt assembly (B-7): the SNAPSHOT is the one composed part — it already carries the
// constitution in its full current wording (REQ-002.1, composed by the one composition path).
// The runtime addenda below NEVER compose constitution text: the division-of-labor preamble
// (REQ-021.1), the preflight finding (REQ-020.2) and the attachment manifest (REQ-007.3).
package executor

import (
	"strings"

	"devlab/backend/internal/preflight"
)

// promptPreamble is the division-of-labor preamble (REQ-021.1): the agent implements and
// commits; the service delivers, publishes, opens the pull request and merges. An execution
// never ends with a question (D 40), the agent never shrinks the task on its own, and the
// report is three-part (D 41).
const promptPreamble = `## Division of labor (read first)

You are the autonomous Holistic runner, executing unattended on the server.

- YOU implement the task and commit your work onto the workbench branch (` + workbenchBranch + `).
- THE SERVICE takes over everything after that: publishing the workbench, the dev delivery,
  the delivery branch, the pull request and the merge. Do NOT push, do NOT open pull
  requests, do NOT merge.
- There is no human to ask: never end with a question. For unresolved operational gaps follow
  the run rules — record a non-blocking skip with its reason and continue.
- Do not shrink the task on your own; anything you deliberately leave undone must be named.
- End with a three-part report: implemented / explicitly not implemented with reason /
  conclusions.`

// AssemblePrompt builds the execution prompt: preamble (REQ-021.1) + the composed snapshot
// (constitution included, REQ-002.1) + the preflight finding (REQ-020.2) + the attachment
// manifest (REQ-007.3, "" when there are none). The addenda never compose constitution text.
func AssemblePrompt(snapshot string, f preflight.Finding, attManifest string) string {
	var b strings.Builder
	b.WriteString(promptPreamble)
	b.WriteString("\n\n")
	b.WriteString(strings.TrimRight(snapshot, "\n"))
	b.WriteString("\n")

	b.WriteString("\n## Preflight — observed state of this repository\n\n")
	b.WriteString("State: " + string(f.State) + "\n")
	for _, ev := range f.Evidence {
		b.WriteString("- " + ev + "\n")
	}
	if f.OpenPR != nil {
		b.WriteString("- open pull request: " + f.OpenPR.URL + "\n")
	}
	b.WriteString("What already exists is INPUT, not something to recreate: only close the missing part of the path.\n")

	if strings.TrimSpace(attManifest) != "" {
		b.WriteString("\n## Attached media\n\n")
		b.WriteString(strings.TrimRight(attManifest, "\n"))
		b.WriteString("\n")
	}
	return b.String()
}
