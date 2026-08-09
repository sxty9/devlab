// Package model is the ONE typed wire contract of DevLab (REQ-040.4): the vocabulary and the
// JSON shapes the API returns, mirrored field by field by the frontend's src/types.ts and pinned
// by the golden fixtures under contract/fixtures/ (drift breaks both builds).
//
// Terminology mapping (German spec term → wire vocabulary; A2-9):
//
//	Verfassung            → axioms / rules / run rules
//	automatischer Lauf    → run kind "auto"
//	ToDo                  → run kind "todo"
//	Ausführung            → execution
//	Schritt               → step
//	Kette / Stufe         → chain / stage
//	Lieferung / Gegenbuchung → delivery / reversal
//	Arbeitsstand          → workbench branch "mercury-dev"
//	Vorprüfung            → preflight
//	Platz / einreihen / zurückstellen / überladen → slot / queue / defer / overload
//	Hinweis / Bericht     → notice / report
//	Abo-Limit-Pause       → usage-limit pause
package model

import (
	"encoding/json"
	"fmt"
	"time"
)

// ── Chain vocabulary — the ONE place for wire literals (REQ-040.4; W-A) ──────────────────

// Stage is one stage of the ONE delivery chain.
type Stage string

const (
	StagePreflight   Stage = "preflight"
	StageImplement   Stage = "implement"
	StageDeliverDev  Stage = "deliver-dev"
	StagePublish     Stage = "publish"
	StagePullRequest Stage = "pull-request"
)

// ChainStages returns the chain order — the single definition of the chain as a list.
func ChainStages() []Stage {
	return []Stage{StagePreflight, StageImplement, StageDeliverDev, StagePublish, StagePullRequest}
}

// StepState is a stage's state: two transient + four terminal. NORMATIVE: every stage ENDS in
// exactly one of the FOUR terminal states (REQ-030; W-B) — never in pending or running.
type StepState string

const (
	StepPending       StepState = "pending"
	StepRunning       StepState = "running"
	StepExecuted      StepState = "executed"
	StepFailed        StepState = "failed"
	StepNotApplicable StepState = "not-applicable"
	StepNotExecuted   StepState = "not-executed"
)

// Terminal reports whether s is one of the four terminal states.
func (s StepState) Terminal() bool {
	switch s {
	case StepExecuted, StepFailed, StepNotApplicable, StepNotExecuted:
		return true
	}
	return false
}

// TaskState is the preflight observation of a task at a repo. "unknown" means the source of
// truth was unreachable — never guessed (K-3).
type TaskState string

const (
	TaskNotImplemented         TaskState = "not-implemented"
	TaskImplementedUndelivered TaskState = "implemented-undelivered"
	TaskDelivered              TaskState = "delivered"
	TaskUnknown                TaskState = "unknown"
)

// ExecPhase is the phase of one execution's persisted state document.
type ExecPhase string

const (
	PhaseCreated     ExecPhase = "created"
	PhaseQueued      ExecPhase = "queued"
	PhaseRunning     ExecPhase = "running"
	PhasePaused      ExecPhase = "paused"
	PhaseBlocked     ExecPhase = "blocked"
	PhaseInterrupted ExecPhase = "interrupted"
	PhaseCompleted   ExecPhase = "completed"
	PhaseFailed      ExecPhase = "failed"
	PhaseDiscarded   ExecPhase = "discarded"
)

// PauseReason is the ONE pause concept (W-C, REQ-015.1).
type PauseReason string

const (
	PauseDeferredByUser PauseReason = "deferred-by-user"
	PauseUsageLimit     PauseReason = "usage-limit"
)

// RunKind discriminates the two things sharing the run machinery (A2-9).
type RunKind string

const (
	KindAuto RunKind = "auto"
	KindTodo RunKind = "todo"
)

// AutonomyLevel is HOW SELF-RELIANT an execution works — chosen when a run is defined. It fixes WHEN
// the run stops and asks the user instead of deciding for itself. The three levels are one ordered
// axis (from "asks the most" to "asks never"); the names carry their meaning, so no help text stands
// beside the picker (Intuitiv by Design). An empty value RESOLVES to Autonomous — today's behavior —
// so a run that names no level keeps working exactly as before.
type AutonomyLevel string

const (
	// AutonomyCollaborative stops before EVERY nontrivial decision: the run raises the choice as a
	// question and blocks its repository until the user answers.
	AutonomyCollaborative AutonomyLevel = "collaborative"
	// AutonomyBalanced stops only at a genuine architecture or design fork it cannot settle from the
	// task, the code, or the constitution (the Klärungspflicht core); routine choices it makes itself.
	AutonomyBalanced AutonomyLevel = "balanced"
	// AutonomyAutonomous never stops to ask: it decides everything itself and never blocks on a
	// question. This is the resolved default of an unset level.
	AutonomyAutonomous AutonomyLevel = "autonomous"
)

// Resolve maps an empty level onto the default (Autonomous) and leaves a named level unchanged, so
// the resolution lives in ONE place.
func (a AutonomyLevel) Resolve() AutonomyLevel {
	if a == "" {
		return AutonomyAutonomous
	}
	return a
}

// MayAsk reports whether a run at this level is permitted to stop and ask at all — false only for the
// autonomous level, which decides everything itself.
func (a AutonomyLevel) MayAsk() bool { return a.Resolve() != AutonomyAutonomous }

// Valid reports whether a is empty (⇒ default) or one of the three named levels.
func (a AutonomyLevel) Valid() bool {
	switch a {
	case "", AutonomyCollaborative, AutonomyBalanced, AutonomyAutonomous:
		return true
	}
	return false
}

// ── Authorship (REQ-041): a label, never a barrier ───────────────────────────────────────

// Actor names who acted: a person, the autonomous system, or the system on a person's behalf.
// Legacy records carry User "" — surfaced as "unknown", never back-filled.
type Actor struct {
	User       string `json:"user"`
	Autonomous bool   `json:"autonomous,omitempty"`
	OnBehalfOf string `json:"onBehalfOf,omitempty"`
}

// Authorship is who created and who last changed a record, kept separate so the creator stays
// visible after later edits.
type Authorship struct {
	Created   Actor     `json:"created"`
	CreatedAt time.Time `json:"createdAt"`
	Updated   Actor     `json:"updated"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ── The server stage array — the client ONLY renders it (B-17/B-35) ──────────────────────

// StageView is one stage's honest, server-derived state.
type StageView struct {
	Stage Stage     `json:"stage"`
	State StepState `json:"state"`
	// Reason is mandatory for failed | not-applicable | not-executed.
	Reason string `json:"reason,omitempty"`
	// Evidence is mandatory for not-applicable: the attested repo property (REQ-031.3).
	Evidence string `json:"evidence,omitempty"`
	Log      string `json:"log,omitempty"`
	// Session marks the stage whose work IS an open agent conversation. Its record is not the
	// Log text but the execution's session journal, which is followed while it runs and stays
	// readable afterwards. The SERVER names it (it comes off the chain's own stage table), so no
	// client ever has to recognise a stage by its name.
	Session   bool       `json:"session,omitempty"`
	Link      string     `json:"link,omitempty"`
	StartedAt *time.Time `json:"startedAt,omitempty"`
	EndedAt   *time.Time `json:"endedAt,omitempty"`
}

// ── The session journal — one open agent conversation, line by line ──────────────────────

// SessionLine is ONE recorded line of an execution's agent session: what the agent said or did,
// or what a person wrote INTO the running session. Both live in the same recording — a human's
// words are not kept in a second place — and the line says which of the two it is.
//
// From is empty for the agent's own output and carries the person's username for an
// intervention. Repo is empty on records written before the session carried its repository.
type SessionLine struct {
	At   time.Time `json:"at"`
	Repo string    `json:"repo,omitempty"`
	From string    `json:"from,omitempty"`
	Text string    `json:"text"`
}

// Intervention is the fact that a PERSON wrote into a running execution: who, when, and into
// which repository's session. Its presence is what makes "this run was not purely self-acting"
// visible without reading the whole journal.
type Intervention struct {
	By   Actor     `json:"by"`
	At   time.Time `json:"at"`
	Repo string    `json:"repo,omitempty"`
}

// Backoff is a persisted retry state (K-5): why, how it is classified, how often it was tried,
// and when the next attempt is due.
type Backoff struct {
	Reason   string    `json:"reason"`
	Class    string    `json:"class"`
	Attempts int       `json:"attempts"`
	FirstAt  time.Time `json:"firstAt"`
	LastAt   time.Time `json:"lastAt"`
	NextAt   time.Time `json:"nextAt"`
}

// RepoPipeline is one repo's chain progress inside an execution.
type RepoPipeline struct {
	Repo   string      `json:"repo"`
	Stages []StageView `json:"stages"`
	// TaskState is the preflight observation (its evidence sits in the preflight step).
	TaskState TaskState `json:"taskState,omitempty"`
	// Block is set while the repo is blocked (REQ-032) — nil otherwise.
	Block *Backoff `json:"block,omitempty"`
	// Done and Succeeded are NEVER stored; always computed via PipelineSucceeded.
	Done      bool `json:"done"`
	Succeeded bool `json:"succeeded"`
}

// PipelineSucceeded derives a pipeline's completion: done ⇔ every stage state is terminal;
// succeeded ⇔ done AND no stage failed AND no stage was skipped as not-executed (REQ-030.4).
// An empty stage array is neither done nor succeeded.
func PipelineSucceeded(stages []StageView) (done, succeeded bool) {
	if len(stages) == 0 {
		return false, false
	}
	succeeded = true
	for _, sv := range stages {
		if !sv.State.Terminal() {
			return false, false
		}
		if sv.State == StepFailed || sv.State == StepNotExecuted {
			succeeded = false
		}
	}
	return true, succeeded
}

// UsageView is consumed tokens and their monetary equivalent — informative, NEVER a cap
// (REQ-017, W1).
type UsageView struct {
	InputTokens  int64   `json:"inputTokens"`
	OutputTokens int64   `json:"outputTokens"`
	CostUSD      float64 `json:"costUsd"`
}

// ContinuationView is the exact spot a paused/interrupted execution continues at.
type ContinuationView struct {
	Repo  string `json:"repo"`
	Stage Stage  `json:"stage"`
}

// PauseView describes the ONE pause of an execution.
type PauseView struct {
	Reason         PauseReason `json:"reason"`
	Message        string      `json:"message,omitempty"`
	ResumeAttempts int         `json:"resumeAttempts"`
	NotBefore      *time.Time  `json:"notBefore,omitempty"`
}

// ExecutionView is the projection of one execution (state document + result) the active list,
// the slot panel and the detail header render.
type ExecutionView struct {
	ID           string            `json:"id"`
	RunID        string            `json:"runId"`
	RunTitle     string            `json:"runTitle"`
	Kind         RunKind           `json:"kind"`
	Phase        ExecPhase         `json:"phase"`
	Reason       string            `json:"reason,omitempty"`
	Pause        *PauseView        `json:"pause,omitempty"`
	Continuation *ContinuationView `json:"continuation,omitempty"`
	Repos        []RepoPipeline    `json:"repos"`
	Overload     bool              `json:"overload,omitempty"`
	Usage        UsageView         `json:"usage"`
	Requested    Authorship        `json:"requested"`
	CreatedAt    time.Time         `json:"createdAt"`
	StartedAt    time.Time         `json:"startedAt"`
	UpdatedAt    time.Time         `json:"updatedAt"`
	// DeliveredCommit names the delivered state (REQ-022.3).
	DeliveredCommit string `json:"deliveredCommit,omitempty"`
}

// DeferSuggestion proposes which execution to defer when all slots are taken (C F3 deferScore).
type DeferSuggestion struct {
	ExecutionID string `json:"executionId"`
	Reason      string `json:"reason"`
	Score       int    `json:"score"`
}

// QueuedStart is a start recorded while a restart was pending; it fires after the restart.
type QueuedStart struct {
	RunID string    `json:"runId"`
	Title string    `json:"title"`
	By    Actor     `json:"by"`
	At    time.Time `json:"at"`
}

// SlotOverview is the read-only projection of the execution slots.
type SlotOverview struct {
	Capacity       int             `json:"capacity"`
	Occupied       int             `json:"occupied"`
	OverloadActive bool            `json:"overloadActive"`
	RestartPending bool            `json:"restartPending"`
	Active         []ExecutionView `json:"active"`
	Deferred       []ExecutionView `json:"deferred"`
	QueuedStarts   []QueuedStart   `json:"queuedStarts"`
}

// StartOutcome answers POST …/{id}/run honestly.
type StartOutcome struct {
	ExecutionID string `json:"executionId,omitempty"`
	Started     bool   `json:"started"`
	Queued      bool   `json:"queued,omitempty"`
	Resumed     bool   `json:"resumed,omitempty"`
	Fresh       bool   `json:"fresh,omitempty"`
	// NotStarted is the named reason nothing was started (e.g. "already delivered"), with the
	// per-target state in TaskStates and the observation it rests on in TaskEvidence (B-4). A
	// rejection is nachpruefbar only WITH the evidence: "already delivered" must name which stand
	// counts as delivered, so a caller can check it against the todo's current text.
	NotStarted     string               `json:"notStarted,omitempty"`
	TaskStates     map[string]TaskState `json:"taskStates,omitempty"`
	TaskEvidence   map[string][]string  `json:"taskEvidence,omitempty"`
	Suggestion     *DeferSuggestion     `json:"suggestion,omitempty"`
	RestartPending bool                 `json:"restartPending,omitempty"`
}

// RestartState is the state of the ONE restart marker (B-13).
type RestartState struct {
	Pending      bool          `json:"pending"`
	RequestedBy  Actor         `json:"requestedBy"`
	RequestedAt  time.Time     `json:"requestedAt"`
	Deadline     time.Time     `json:"deadline"`
	QueuedStarts []QueuedStart `json:"queuedStarts,omitempty"`
}

// Delivery is the ledger view of one delivery (REQ-024).
type Delivery struct {
	ID         string     `json:"id"`
	Repo       string     `json:"repo"`
	Branch     string     `json:"branch"`
	FromCommit string     `json:"fromCommit"`
	ToCommit   string     `json:"toCommit"`
	PRNumber   int        `json:"prNumber,omitempty"`
	PRURL      string     `json:"prUrl,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	MergedAt   *time.Time `json:"mergedAt,omitempty"`
	// ReversalOf points at the delivery this one counter-books (a reversal), "" otherwise.
	ReversalOf string `json:"reversalOf,omitempty"`
	// Stage is the reached stage for the deliveries view (F12).
	Stage string `json:"stage,omitempty"`
	// FailedReason names WHY a failed delivery ("Lieferung gescheitert") did not ship, so the ledger
	// surface can say which layer at the tip is broken and on what — set only when Stage is "failed".
	FailedReason string `json:"failedReason,omitempty"`

	// ── The production step (WHAT-1) — what the PROD view of the deliveries surface renders ──────
	//
	// ProdStage is the production lifecycle of a MERGED delivery, distinct from the dev/PR Stage above:
	// "" (not merged, production not reached), "not-applicable" (the repo is no service),
	// "pending" (merged, production not yet done), "failed" (last production send failed, retrying),
	// or "live" (proven running in production). The PROD view shows only merged deliveries and reads
	// this; the DEV view keeps reading Stage.
	ProdStage string `json:"prodStage,omitempty"`
	// ProdDeployedAt is when the service was proven running in production — the "since when" the PROD
	// view shows for a live delivery.
	ProdDeployedAt *time.Time `json:"prodDeployedAt,omitempty"`
	// ProdFailedReason names why the last production send failed — shown while ProdStage is "failed".
	ProdFailedReason string `json:"prodFailedReason,omitempty"`
	// ProdRetryNextAt is when the production send is next attempted (a self-ending failure retries by
	// itself). Present only while retrying, so the surface can say the wait is timed, not stuck.
	ProdRetryNextAt *time.Time `json:"prodRetryNextAt,omitempty"`
	// ProdEvidence attests the property behind a not-applicable production step (the repo is no service).
	ProdEvidence string `json:"prodEvidence,omitempty"`
}

// PRRef references one pull request.
type PRRef struct {
	Number     int    `json:"number"`
	URL        string `json:"url"`
	HeadBranch string `json:"headBranch"`
}

// PortAllocation is one observed port (F9) — derived from routes + /proc/net/tcp, never stored.
type PortAllocation struct {
	Port     int    `json:"port"`
	Service  string `json:"service"`
	Routed   bool   `json:"routed"`
	Bound    bool   `json:"bound"`
	Conflict bool   `json:"conflict"`
}

// Notice is one persistent hint, coalesced by key (REQ-032.5).
type Notice struct {
	ID       string    `json:"id"`
	Kind     string    `json:"kind"`
	Repo     string    `json:"repo,omitempty"`
	Text     string    `json:"text"`
	NextStep string    `json:"nextStep,omitempty"`
	Count    int       `json:"count"`
	FirstAt  time.Time `json:"firstAt"`
	LastAt   time.Time `json:"lastAt"`
	Read     bool      `json:"read"`
}

// Duration marshals as a Go duration string ("3h", "45m") — the wire form of every tunable
// duration (service config).
type Duration time.Duration

// MarshalJSON renders the duration as its canonical string.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UnmarshalJSON accepts a duration string ("3h") or a number of nanoseconds (legacy tolerance).
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		v, perr := time.ParseDuration(s)
		if perr != nil {
			return fmt.Errorf("invalid duration %q: %w", s, perr)
		}
		*d = Duration(v)
		return nil
	}
	var n int64
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("invalid duration %s", string(b))
	}
	*d = Duration(n)
	return nil
}

// ServiceConfig is the service's runtime configuration (the config interface, cross-cutting 5).
type ServiceConfig struct {
	MaxConcurrency    int      `json:"maxConcurrency"`
	DefaultTimeBudget Duration `json:"defaultTimeBudget"`
	AutomergeWindow   Duration `json:"automergeWindow"`
}

// PoolUsage is one pool's portioned storage share.
type PoolUsage struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
	Files int    `json:"files"`
}

// StorageView reports which data the service holds, portioned per pool — reported, never judged.
type StorageView struct {
	Pools      []PoolUsage `json:"pools"`
	TotalBytes int64       `json:"totalBytes"`
}

// LoadView reports the process's own resource claim — reported, never judged.
type LoadView struct {
	CPUPercent float64 `json:"cpuPercent"`
	RSSBytes   int64   `json:"rssBytes"`
	Goroutines int     `json:"goroutines"`
}

// AiUsageView aggregates the AI-usage pool over a window, per source.
type AiUsageView struct {
	WindowHours int                  `json:"windowHours"`
	Samples     int                  `json:"samples"`
	Totals      UsageView            `json:"totals"`
	BySource    map[string]UsageView `json:"bySource"`
}

// Health is deliberately MINIMAL (A1-5): no operational internals on the public endpoint.
type Health struct {
	OK   bool   `json:"ok"`
	Mode string `json:"mode"`
}

// User is the SPA's bootstrap identity probe.
type User struct {
	Username     string `json:"username"`
	DisplayName  string `json:"displayName"`
	IsAdmin      bool   `json:"isAdmin"`
	CanUseDevlab bool   `json:"canUseDevlab"`
	// The two rights over the session a run works in, kept apart on the wire exactly as they are
	// kept apart in the rights system: watching is not writing.
	CanWatchSession bool   `json:"canWatchSession"`
	CanSpeakSession bool   `json:"canSpeakSession"`
	GithubLinked    bool   `json:"githubLinked"`
	GithubLogin     string `json:"githubLogin,omitempty"`
}

// ── Calendar + coverage (ported wire forms; REQ-012) ─────────────────────────────────────

// RunOccurrence is one calendar entry: a future firing (Schedule set) or a past execution
// (ResultID set — the calendar opens the same detail as the history).
type RunOccurrence struct {
	RunID     string  `json:"runId"`
	RunTitle  string  `json:"runTitle"`
	Kind      RunKind `json:"kind"`
	At        string  `json:"at"`
	Schedule  string  `json:"schedule,omitempty"`
	ResultID  string  `json:"resultId,omitempty"`
	Succeeded *bool   `json:"succeeded,omitempty"`
	Paused    bool    `json:"paused,omitempty"`
}

// RunCalendar is the occurrence window both calendars render.
type RunCalendar struct {
	From        string          `json:"from"`
	To          string          `json:"to"`
	Occurrences []RunOccurrence `json:"occurrences"`
}

// RunCoverage reports which axioms are backed by a run (honest; pending marks a transient gap).
type RunCoverage struct {
	Covered map[string][]string `json:"covered"`
	Index   map[string]string   `json:"index"`
	Axioms  map[string]string   `json:"axioms"`
	Pending bool                `json:"pending,omitempty"`
}
