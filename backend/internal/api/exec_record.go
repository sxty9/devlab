// The recorder half of the executor's Sink seam (ARCHITEKTUR §3.8), plus the two boot compositions
// the daemon drives: the admission preflight gate (B-4) and the startup reconciliation (REQ-039.2).
//
// The recorder is what makes a running execution VISIBLE: it writes the ONE result document while
// the chain works and appends the transcript line by line, so the live follow and the history read
// the same file and a killed process keeps whatever the agent already said (F7/F11). Without it,
// AppendTranscript/ReadTranscriptTail would have no caller and the live transcript would stay empty.
package api

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"devlab/backend/internal/deliver"
	"devlab/backend/internal/execstate"
	"devlab/backend/internal/executor"
	"devlab/backend/internal/live"
	"devlab/backend/internal/model"
	"devlab/backend/internal/preflight"
	"devlab/backend/internal/runs"
	"devlab/backend/internal/sched"
)

// PreflightGate hands out the B-4 admission gate: preflight runs per target repo BEFORE any
// execution document exists, so a fully delivered todo is refused with a reason instead of started
// (the K-3 start ban). Observation is lock-free — a caller's start request never waits out a running
// execution — and a repo without a local checkout answers "no workbench yet" rather than failing.
func (s *Server) PreflightGate() sched.PreflightGate {
	return func(ctx context.Context, repo string, run runs.Run) (preflight.Finding, error) {
		deps := s.ChainDeps(ChainHooks{}).WithRun(run)
		defer deps.Close()
		return preflight.Derive(ctx, deps, repo, run)
	}
}

// SyncStartupTodos is boot step 6 (REQ-039.2): work that verifiably arrived in a default branch is
// checked off with a synthetic, completed result; an unreachable source is a NAMED deferral, never a
// guess. Returns how many todos it reconciled.
func (s *Server) SyncStartupTodos(ctx context.Context) (int, error) {
	if s.runs == nil || s.results == nil || s.runNotices == nil {
		return 0, errors.New("startup reconciliation needs the run, result and notice pools")
	}
	deps := s.ChainDeps(ChainHooks{})
	defer deps.Close()
	return preflight.SyncStartupTodos(ctx, deps, s.runs, s.results, s.runNotices, s.publisher())
}

// ResultRecorder mirrors one execution's progress into its result document and its transcript
// journal — written LIVE, so the running view and the archive are one file. It implements the
// recorder half of executor.Sink; the document-progress half is sched's (composed in cmd/devlabd).
type ResultRecorder struct {
	s   *Server
	mu  sync.Mutex
	res runs.Result
}

// NewResultRecorder opens the result document of one execution (id = the execution id, so the
// document, the transcript journal and the state document share one name) and returns the recorder.
// The document exists from the first moment: an execution is visible while it runs, not only after.
func (s *Server) NewResultRecorder(doc execstate.Doc, run runs.Run) *ResultRecorder {
	now := time.Now().UTC()
	r := &ResultRecorder{s: s, res: runs.Result{
		ID:        doc.ID,
		RunID:     run.ID,
		RunTitle:  run.Title,
		Kind:      run.Kind,
		Model:     run.Tuning.Model,
		StartedAt: now,
		Prompt:    run.PromptSnapshot,
		Requested: doc.Requested,
		Repos:     []model.RepoPipeline{},
	}}
	// A resumed execution keeps its document (REQ-019.1): re-open the stored result instead of
	// overwriting the stages and consumption the earlier attempt already recorded.
	if s.results != nil {
		if prev, ok, err := s.results.Get(doc.ID); err == nil && ok {
			prev.EndedAt = nil
			prev.Report = ""
			r.res = prev
		}
	}
	r.flush()
	return r
}

func (r *ResultRecorder) flush() {
	if r.s.results == nil {
		return
	}
	if err := r.s.results.Put(r.res); err != nil {
		log.Printf("devlabd: record execution %s: %v", r.res.ID, err)
		return
	}
	r.s.publish(live.TopicProgress)
}

// repo returns (appending on demand) the pipeline row of one repo.
func (r *ResultRecorder) repo(repo string) *model.RepoPipeline {
	for i := range r.res.Repos {
		if r.res.Repos[i].Repo == repo {
			return &r.res.Repos[i]
		}
	}
	r.res.Repos = append(r.res.Repos, model.RepoPipeline{Repo: repo})
	return &r.res.Repos[len(r.res.Repos)-1]
}

// StageUpdate replaces (never appends twice) the recorded state of one stage — the stage array IS
// the server's truth, so a running stage becomes its own terminal state in place.
func (r *ResultRecorder) StageUpdate(repo string, sv model.StageView) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rp := r.repo(repo)
	for i := range rp.Stages {
		if rp.Stages[i].Stage == sv.Stage {
			rp.Stages[i] = sv
			r.flush()
			return
		}
	}
	rp.Stages = append(rp.Stages, sv)
	r.flush()
}

// Transcript appends one compact line to the execution's journal as it happens, so the transcript
// survives a kill (F11). It deliberately does NOT rewrite the result document: the journal is an
// append-only file, and coalescing the document per line would be a write per token.
func (r *ResultRecorder) Transcript(_ string, line []byte) {
	if r.s.results == nil {
		return
	}
	if err := r.s.results.AppendTranscript(r.res.ID, line); err != nil {
		log.Printf("devlabd: transcript of execution %s: %v", r.res.ID, err)
	}
}

// Usage records the execution-cumulative consumption — climbing while the agent works (F7).
// Informative only: there is no cap (REQ-017).
func (r *ResultRecorder) Usage(u model.UsageView) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.res.Usage = u
	r.flush()
}

// RepoDone stores one repo's finished pipeline exactly as the motor assembled it.
func (r *ResultRecorder) RepoDone(rp model.RepoPipeline) {
	r.mu.Lock()
	defer r.mu.Unlock()
	*r.repo(rp.Repo) = rp
	r.flush()
}

// Continuation is the document's concern — the result carries no resume point.
func (r *ResultRecorder) Continuation(model.ContinuationView) {}

// Notice lifts a motor finding into the notice pool (coalesced, so a repeating alarm is one entry
// with a count) and ticks the notices topic.
func (r *ResultRecorder) Notice(n executor.NoticeEvent) {
	if r.s.runNotices == nil {
		return
	}
	if _, err := r.s.runNotices.Coalesce(runs.Notice{
		Kind: n.Kind, Repo: n.Repo, Text: n.Text, NextStep: n.NextStep,
	}); err != nil {
		log.Printf("devlabd: notice [%s] not recorded: %v", n.Kind, err)
		return
	}
	r.s.publish(live.TopicNotices)
}

// Finish stamps the end of the execution and its honest report. A pause or an interruption is NOT
// an end: the document stays open so the resume continues in the same record (REQ-019.1).
func (r *ResultRecorder) Finish(execErr error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if execErr != nil && !executionEnded(execErr) {
		r.res.Report = executionReport(execErr)
		r.flush()
		return
	}
	now := time.Now().UTC()
	r.res.EndedAt = &now
	r.res.Report = executionReport(execErr)
	r.flush()
	// A settled execution may have completed its deliveries earlier (B-8): re-derive its merge
	// stamp so the history admits it the moment the last delivery is closed.
	if r.s.deliveries != nil && r.s.results != nil {
		if err := deliver.SettleExecution(r.s.deliveries, r.s.results, r.res.ID); err != nil {
			log.Printf("devlabd: settle execution %s: %v", r.res.ID, err)
		}
	}
}

// executionEnded reports whether an execution-level outcome is an END (as opposed to a pause or an
// interruption, which keep the record open for the resume).
func executionEnded(err error) bool {
	if errors.Is(err, executor.ErrPausedUsageLimit) {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return true
}

// executionReport is the honest three-part report line the surface reads: what happened, named.
func executionReport(execErr error) string {
	switch {
	case execErr == nil:
		return "## implemented\n\nEvery target repository completed."
	case errors.Is(execErr, executor.ErrPausedUsageLimit):
		return "## paused\n\nThe shared usage limit was reached; the execution resumes at the same spot."
	case errors.Is(execErr, context.Canceled), errors.Is(execErr, context.DeadlineExceeded):
		return "## interrupted\n\n" + execErr.Error() + "\n\nThe continuation is recorded; the execution resumes at the same spot."
	default:
		return "## not implemented\n\n" + execErr.Error()
	}
}

// ── the composed sink (ARCHITEKTUR §3.8 "Motor/Recorder") ────────────────────────────────

// ExecutionSink composes the motor's two sink halves into the ONE sink an execution writes
// through:
//
//   - the document-progress half (sched.DocSink): coarse per-repo progress and the continuation
//     point on the persisted state document, so admission, the slot overview and the resume read
//     the truth;
//   - the recorder half (ResultRecorder): the one result document plus the transcript journal,
//     written live, so the running view and the history read the same file.
//
// The composition exists because neither half may know the other: sched never imports the executor
// (B-3), and the recorder never reaches into the scheduler's documents. Notices belong to the
// recorder half alone — a state document holds no operator findings.
func ExecutionSink(doc *sched.DocSink, rec *ResultRecorder) executor.Sink {
	return execSink{doc: doc, rec: rec}
}

type execSink struct {
	doc *sched.DocSink
	rec *ResultRecorder
}

func (t execSink) StageUpdate(repo string, sv model.StageView) {
	t.doc.StageUpdate(repo, sv)
	t.rec.StageUpdate(repo, sv)
}

func (t execSink) Transcript(repo string, line []byte) {
	t.doc.Transcript(repo, line)
	t.rec.Transcript(repo, line)
}

func (t execSink) Usage(u model.UsageView) {
	t.doc.Usage(u)
	t.rec.Usage(u)
}

func (t execSink) RepoDone(rp model.RepoPipeline) {
	t.doc.RepoDone(rp)
	t.rec.RepoDone(rp)
}

func (t execSink) Continuation(c model.ContinuationView) {
	t.doc.Continuation(c)
	t.rec.Continuation(c)
}

func (t execSink) Notice(n executor.NoticeEvent) { t.rec.Notice(n) }
