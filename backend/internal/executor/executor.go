// Package executor is the chain motor (S8): it walks the ONE stage list over each target repo,
// records honest stage states, streams the transcript, counts consumption live, enforces the
// time budget honestly, and honors continuations. ONE Deps seam (B 1.3b) instead of nine
// function fields; sched is never imported — the restart hook and the limit pause arrive as
// injected functions (B-3).
package executor

import (
	"context"
	"io"
	"time"

	"devlab/backend/internal/execstate"
	"devlab/backend/internal/live"
	"devlab/backend/internal/model"
	"devlab/backend/internal/preflight"
	"devlab/backend/internal/runs"
	"devlab/backend/internal/telemetry"
)

// WorkbenchOps is the slice of workbench the motor drives (S9).
type WorkbenchOps interface {
	Prepare(ctx context.Context) error
	FoldInDefault(ctx context.Context) error
	Publish(ctx context.Context) error
	Head(ctx context.Context) (string, error)
}

// AgentStream is a running agent invocation: its stream-json stdout plus its completion.
type AgentStream interface {
	Output() io.Reader
	Wait() error
	Kill() error
}

// GitHubOps is the typed-error GitHub slice (classification input for faultclass).
type GitHubOps interface {
	DefaultBranch(ctx context.Context, token, repo string) (string, error)
}

// DeliverOps is the delivery slice (the ONE PR path + protection on repo creation).
type DeliverOps interface {
	OpenOrAdoptPR(ctx context.Context, in DeliverPRIn) (model.PRRef, bool, error)
	NextPRBase(ctx context.Context, repo string) (string, error)
	EnsureProtection(ctx context.Context, repo string) error
}

// DeliverPRIn mirrors deliver.PRIn across the seam (kept local so the seam stays mockable
// without importing deliver in tests).
type DeliverPRIn struct {
	Repo, Head, Base, Title, Body string
	DeliveryID                    string
}

// DeployOps is the deploy slice (detection, dev delivery, self handover).
type DeployOps interface {
	Detect(ctx context.Context, repo string) (kind string, evidence string, err error)
	DeliverDev(ctx context.Context, repo string) (running bool, port int, detail string, err error)
	SelfInstallAndHandover(ctx context.Context, artifactDir string) error
}

// Deps is the ONE test seam — implemented by the domain packages, injected in cmd/devlabd.
type Deps interface {
	Workbench(repo string) WorkbenchOps
	Agent(ctx context.Context, repo, prompt string, t runs.ResolvedTuning, resumeID string) (AgentStream, error)
	StageAttachments(ctx context.Context, repo string, atts []runs.AttachmentRef) (manifest string, cleanup func() error, err error) // B-6
	GitHub() GitHubOps
	Deliver() DeliverOps
	Deploy() DeployOps
	Preflight(ctx context.Context, repo string, run runs.Run) (preflight.Finding, error)
	RequestRestart(by model.Actor) error // B-3 — implemented by sched, injected
	RecordAiUsage(u telemetry.UsageSample)
	Publish(t live.Topic)
	Now() time.Time
}

// Sink receives the motor's progress — implemented by sched (document progress) and by the
// result recorder (live result); composed in cmd/devlabd.
type Sink interface {
	StageUpdate(repo string, sv model.StageView)
	Transcript(repo string, line []byte)
	Usage(u model.UsageView)
	RepoDone(rp model.RepoPipeline)
	Continuation(c model.ContinuationView)
}

// RepoCtx is the motor's per-repo context handed to the stage functions.
type RepoCtx struct {
	Repo    string
	Run     runs.Run
	Doc     execstate.Doc
	Tuning  runs.ResolvedTuning
	Finding preflight.Finding
	Deps    Deps
	Sink    Sink
}

// Request is one execution request.
type Request struct {
	Run    runs.Run
	Doc    execstate.Doc
	Budget runs.ResolvedTuning
}

// Execute walks the chain over every target repo. Motor rules: after a failed stage the
// following stages become not-executed; not-applicable (only WITH evidence) lets the chain
// continue; skips are never success; repo success = model.PipelineSucceeded; "implemented
// without deliver-dev" ⇒ failed + notice (K-4/REQ-031); honors Continuation (resume at the
// same spot), the time budget (honest overrun with value + achieved, F6), the usage-limit
// detection (mercury.DetectUsageLimit ⇒ the injected shared-pause hook), and publish after
// commit (K-1).
func Execute(ctx context.Context, deps Deps, req Request, sink Sink) error {
	panic("TODO(B3)")
}

// AssemblePrompt builds the execution prompt (B-7): the SNAPSHOT is the one composed part
// (constitution included); the runtime addenda (division-of-labor preamble REQ-021.1,
// preflight finding, attachment manifest) NEVER compose constitution text.
func AssemblePrompt(snapshot string, f preflight.Finding, attManifest string) string {
	panic("TODO(B3)")
}

// CompactStream compacts the agent's stream-json into transcript lines and live usage
// (F7/F11): every line reaches the sink immediately, so the transcript survives a kill.
func CompactStream(r io.Reader, sink Sink, repo string) (model.UsageView, error) {
	panic("TODO(B3)")
}

// Stage implementations referenced by the frozen chain list (chain.go) — B3 fills them.

func preflightApplies(ctx context.Context, rc *RepoCtx) (bool, string) { panic("TODO(B3)") }
func preflightRun(ctx context.Context, rc *RepoCtx) error              { panic("TODO(B3)") }
func implementApplies(ctx context.Context, rc *RepoCtx) (bool, string) { panic("TODO(B3)") }
func implementRun(ctx context.Context, rc *RepoCtx) error              { panic("TODO(B3)") }
func deliverDevApplies(ctx context.Context, rc *RepoCtx) (bool, string) {
	panic("TODO(B3)")
}
func deliverDevRun(ctx context.Context, rc *RepoCtx) error               { panic("TODO(B3)") }
func publishApplies(ctx context.Context, rc *RepoCtx) (bool, string)     { panic("TODO(B3)") }
func publishRun(ctx context.Context, rc *RepoCtx) error                  { panic("TODO(B3)") }
func pullRequestApplies(ctx context.Context, rc *RepoCtx) (bool, string) { panic("TODO(B3)") }
func pullRequestRun(ctx context.Context, rc *RepoCtx) error              { panic("TODO(B3)") }
