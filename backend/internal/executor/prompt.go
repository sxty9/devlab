// Prompt assembly (B-7): the SNAPSHOT is the one composed part — it already carries the
// constitution in its full current wording (REQ-002.1, composed by the one composition path).
// The runtime addenda below NEVER compose constitution text: the division-of-labor preamble
// (REQ-021.1), the autonomy policy, the preflight finding (REQ-020.2) and the attachment manifest
// (REQ-007.3).
package executor

import (
	"strings"

	"devlab/backend/internal/model"
	"devlab/backend/internal/preflight"
)

// promptPreamble is the division-of-labor preamble (REQ-021.1): the agent implements and commits;
// the service delivers, publishes, opens the pull request and merges. WHETHER the agent may stop
// and ask is NOT decided here — it follows the run's autonomy level (autonomyPolicy), appended by
// AssemblePrompt — because the Klärungspflicht is now a per-run choice, not a fixed "never ask".
const promptPreamble = `## Division of labor (read first)

You are the autonomous Holistic runner, executing unattended on the server.

- YOU implement the task and commit your work onto the workbench branch (` + workbenchBranch + `).
- THE SERVICE takes over everything after that: publishing the workbench, the dev delivery,
  the delivery branch, the pull request and the merge. Do NOT push, do NOT open pull
  requests, do NOT merge.
- Do not shrink the task on your own; anything you deliberately leave undone must be named.
- End with a three-part report: implemented / explicitly not implemented with reason /
  conclusions.`

// questionMarker delimits the machine-read question block an asking run ends its final message with.
// It is a fixed sentinel (not markdown) so parseAgentQuestion recognises it verbatim; the closing
// marker is optional for the parser but instructed for the agent.
const (
	questionOpen  = "=== MERCURY-QUESTION ==="
	questionClose = "=== END MERCURY-QUESTION ==="
	questionQ     = "QUESTION:"
	questionR     = "RECOMMENDATION:"
)

// autonomyPolicy is the per-run ask policy the agent obeys (REQ: three autonomy levels). It fixes
// WHEN the run stops and asks instead of deciding for itself, and — for the two asking levels — HOW
// to ask: end the final message with the question block and stop, so the repository blocks and the
// user's answer continues the same run.
func autonomyPolicy(level model.AutonomyLevel) string {
	switch level.Resolve() {
	case model.AutonomyAutonomous:
		return "- AUTONOMY — autonomous: there is no human to ask. Decide every question yourself and " +
			"never end with a question. For unresolved operational gaps follow the run rules — record a " +
			"non-blocking skip with its reason and continue."
	case model.AutonomyCollaborative:
		return "- AUTONOMY — collaborative: stop and ask before EVERY nontrivial decision instead of " +
			"deciding it yourself. When a choice is more than routine, ask.\n" + askInstruction
	default: // balanced
		return "- AUTONOMY — balanced: decide routine matters yourself. If, and only if, you reach a " +
			"genuine architecture or design decision you cannot resolve from the task, the code, or the " +
			"constitution, STOP and ask instead of guessing. Do not shrink the task to avoid asking.\n" + askInstruction
	}
}

// askInstruction tells an asking run HOW to raise a blocking question. Kept as one block so both
// asking levels share the exact wording the parser expects.
const askInstruction = "  When you must ask, do NOT keep implementing past the decision. Commit whatever you have " +
	"already finished, then end your final message with EXACTLY this block and nothing after it:\n" +
	"  " + questionOpen + "\n" +
	"  " + questionQ + "\n" +
	"  <your one clear question>\n" +
	"  " + questionR + "\n" +
	"  <the answer you would choose, and why>\n" +
	"  " + questionClose + "\n" +
	"  The repository is then blocked until the user answers; the run continues from here with their " +
	"answer — you are not restarted. Ask exactly one question at a time."

// AssemblePrompt builds the execution prompt: preamble (REQ-021.1) + the autonomy policy + the
// composed snapshot (constitution included, REQ-002.1) + the examined stand of THIS repository + the
// preflight finding (REQ-020.2) + the attachment manifest (REQ-007.3, "" when there are none). The
// addenda never compose constitution text.
//
// scope is the per-repository examined stand as the ONE renderer wrote it
// (mercury.RepoScopeSection, handed in by Deps.AxiomScope) — "" when the run names no axiom.
func AssemblePrompt(snapshot string, autonomy model.AutonomyLevel, f preflight.Finding, scope, attManifest string) string {
	var b strings.Builder
	b.WriteString(promptPreamble)
	b.WriteString("\n")
	b.WriteString(autonomyPolicy(autonomy))
	b.WriteString("\n\n")
	b.WriteString(strings.TrimRight(snapshot, "\n"))
	b.WriteString("\n")

	if strings.TrimSpace(scope) != "" {
		b.WriteString(strings.TrimRight(scope, "\n"))
		b.WriteString("\n")
	}

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

// parseAgentQuestion extracts a blocking question from the agent's final message: the text between
// the MERCURY-QUESTION markers, split into the question and the run's own recommendation. ok is false
// when no well-formed block with a non-empty question is present, so ordinary output never blocks a
// run. The closing marker is optional (the block may run to the end of the message).
func parseAgentQuestion(text string) (question, recommendation string, ok bool) {
	i := strings.Index(text, questionOpen)
	if i < 0 {
		return "", "", false
	}
	body := text[i+len(questionOpen):]
	if j := strings.Index(body, questionClose); j >= 0 {
		body = body[:j]
	}
	qi := strings.Index(body, questionQ)
	if qi < 0 {
		return "", "", false
	}
	body = body[qi+len(questionQ):]
	if ri := strings.Index(body, questionR); ri >= 0 {
		question = strings.TrimSpace(body[:ri])
		recommendation = strings.TrimSpace(body[ri+len(questionR):])
	} else {
		question = strings.TrimSpace(body)
	}
	if question == "" {
		return "", "", false
	}
	return question, recommendation, true
}
