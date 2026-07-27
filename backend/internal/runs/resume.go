package runs

// ResumeAction is what the next fire of a run will do with any interrupted execution it finds: continue
// the SAME execution, or start a fresh one. It is the honest, surfaced answer to "resume or restart?" —
// decided in ONE place (the executor's resume classifier) and reported back to whoever triggered the run,
// so a re-trigger never silently redoes a whole night's work into a second, duplicate pull request.
type ResumeAction string

const (
	// ResumeContinue continues the same execution (same id), skipping the repositories it already finished.
	ResumeContinue ResumeAction = "resume"
	// ResumeFresh starts a new execution — either because no resumable one exists, or because an existing
	// one was deliberately discarded (an explicit fresh start), or because it belonged to a different mode.
	ResumeFresh ResumeAction = "fresh"
)

// ResumePlan describes what a trigger is about to do and why: whether it continues an interrupted
// execution or begins a new one, which execution it continues (ResultID) and how far that one already got
// (ReposDone), and — on an explicit fresh start — which incomplete execution it discarded. It is purely
// descriptive: the executor enacts it. It travels from the scheduler's FireNow back to the HTTP trigger so
// the difference between "fortgesetzt" and "neu begonnen" is visible to whoever pressed the button.
type ResumePlan struct {
	Action ResumeAction `json:"action"`
	// ResultID is the execution being continued (set only when Action == resume).
	ResultID string `json:"resultId,omitempty"`
	// ReposDone is how many repositories that execution had already completed — the ones a resume skips.
	ReposDone int `json:"reposDone"`
	// Reason is a human-readable explanation of the decision (why it resumed, started fresh, or discarded).
	Reason string `json:"reason"`
	// Discarded is the resultId of an incomplete execution that an explicit fresh start marked as discarded,
	// so the caller can name it (and any pull request it left open) instead of it lingering unnoticed.
	Discarded string `json:"discarded,omitempty"`
	// DiscardedPRUrl names an open pull request the discarded execution left behind, so a deliberate fresh
	// start surfaces it rather than silently placing a second one beside it.
	DiscardedPRUrl string `json:"discardedPrUrl,omitempty"`
}
