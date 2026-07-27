package runs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Result is one execution of a run: the per-repo pipeline outcomes. Stored under the run id and kept
// even after the run is deleted — logs are historized and persistent, viewable via the orphan-logs
// listing. The scheduler/executor (Phase 2) writes these; the management layer only reads them.
type Result struct {
	RunID    string `json:"runId"`
	ResultID string `json:"resultId"`
	RunName  string `json:"runName,omitempty"` // snapshot of the run's name (survives run deletion)
	// Type snapshots the run's kind (auto|todo) at execution time, like RunName — so the Lauf- and
	// ToDo-History can each show ONLY their own executions even after the run is deleted. "" (results
	// written before this field) reads as auto, and the reader falls back to the live run's kind.
	Type      Type      `json:"type,omitempty"`
	StartedAt time.Time `json:"startedAt"`
	// UpdatedAt is refreshed on every Save (i.e. after every repo and every carry-over), so it tracks
	// when the execution was last worked — not just when it began. FindStranded bounds resume by this,
	// so a multi-night carry-over that is touched each night never ages out, while a genuinely abandoned
	// husk does. Zero on results written before this field existed (FindStranded falls back to StartedAt).
	UpdatedAt  time.Time `json:"updatedAt,omitempty"`
	FinishedAt time.Time `json:"finishedAt,omitempty"`
	PromptHash string    `json:"promptHash,omitempty"`
	// Prompt is the run's Promptstellung snapshotted at execution start — the exact prompt the agent was
	// driven by (auto: axioms + Laufregeln; todo: the task). Snapshotted like RunName/Type/PromptHash so
	// the History shows THIS execution's prompt even after the run — or its axioms — later change, or the
	// run is deleted. Empty on results written before this field existed.
	Prompt string `json:"prompt,omitempty"`
	// Mode is the safety-ladder mode (report | pr | full) this execution was started under. A resume
	// must not continue a husk from a DIFFERENT mode: a report husk marks repos "done" after mere
	// analysis, so resuming it in pr mode would skip implementing them. resumeOrNew reaps a
	// mode-mismatched husk instead of continuing it. Empty on results predating this field.
	Mode string `json:"mode,omitempty"`
	// Model + Effort record which Claude engine drove THIS execution: the resolved model and the selected
	// effort tier ("" = the runner default max, "ultracode" = the maximal tier). Stamped when the execution
	// is minted and re-stamped on resume to track the run's current tuning, so the report can label the
	// answer with its model — a Holistic requirement now that the model is chosen per run — and the History
	// shows how each run was tuned. Empty on older results.
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
	// TimeBudget is the per-repo agent time budget that governed THIS execution, stamped when the
	// execution is minted and re-stamped on resume — like Model/Effort — so the Ausführungsansicht can
	// name the budget that actually applied (and a timed-out run can be read as a budget overrun, not a
	// bare failure). It is the RESOLVED label: "0" = no cap, else a compact duration ("3h"). Empty on
	// results predating this field.
	TimeBudget string `json:"timeBudget,omitempty"`
	OK         bool   `json:"ok"`
	// Suspended marks an execution paused on the Claude usage limit; ResumeAt is when the scheduler
	// will continue it (with only the repos NOT already in Repos). Cleared once it finishes.
	Suspended bool         `json:"suspended,omitempty"`
	ResumeAt  *time.Time   `json:"resumeAt,omitempty"`
	Repos     []RepoResult `json:"repos"`
	// Live is the repo CURRENTLY being worked, published as the execution progresses so the UI can
	// follow a run step by step (and the agent's output as it streams). It is deliberately SEPARATE from
	// Repos: Repos holds only COMPLETED repos (DoneRepos/resume skip exactly those), so a half-done repo
	// must never leak into it — a crash would otherwise make a resume treat it as finished and never
	// re-implement it. Cleared the instant a repo completes (its finished result moves into Repos) and on
	// finish. Purely additive for observation — no scheduling or resume logic reads it.
	Live *RepoResult `json:"live,omitempty"`
	// Token/cost totals across all repos of this execution.
	InputTokens  int     `json:"inputTokens"`
	OutputTokens int     `json:"outputTokens"`
	CostUSD      float64 `json:"costUsd"`
	NumTurns     int     `json:"numTurns"`
}

// Ref projects an execution to the lightweight reference stored on the run — including how far the
// work got (dev-deploy, PR), so the surface can name the stage reached instead of a bare tick. One
// definition for every return path of an execution; Maintain later adds the merge/prod rungs.
func (r Result) Ref() ResultRef {
	ref := ResultRef{
		ResultID: r.ResultID, At: r.StartedAt, OK: r.OK, RepoCount: len(r.Repos),
		InputTokens: r.InputTokens, OutputTokens: r.OutputTokens, CostUSD: r.CostUSD,
		Suspended: r.Suspended, ResumeAt: r.ResumeAt,
	}
	for _, rr := range r.Repos {
		if rr.Deployed {
			ref.Deployed = true
		}
		if ref.PRUrl == "" && rr.PRUrl != "" {
			ref.PRUrl = rr.PRUrl
		}
	}
	return ref
}

// RepoResult is the outcome of a run against one repository.
type RepoResult struct {
	Repo string `json:"repo"`
	// Base is the commit this repo stood at when the agent examined it — the stand recorded per axiom so
	// the NEXT run only has to look at what came after it.
	Base string `json:"base,omitempty"`
	// Running marks the repo still in flight — set only on Result.Live, cleared once it moves into Repos.
	Running      bool    `json:"running,omitempty"`
	OK           bool    `json:"ok"`
	Deployed     bool    `json:"deployed"`
	PRUrl        string  `json:"prUrl,omitempty"`
	Steps        []Step  `json:"steps"`
	Error        string  `json:"error,omitempty"`
	InputTokens  int     `json:"inputTokens,omitempty"`
	OutputTokens int     `json:"outputTokens,omitempty"`
	CostUSD      float64 `json:"costUsd,omitempty"`
	NumTurns     int     `json:"numTurns,omitempty"`
}

// Step is one stage of the per-repo pipeline (analyze | implement | push | pr | deploy).
type Step struct {
	Name string `json:"name"`
	// Running marks a step still in progress — the agent step carries its streaming transcript in Log
	// while Running is true, and the finished report once it clears. Set only on a Live repo's steps.
	Running bool      `json:"running,omitempty"`
	OK      bool      `json:"ok"`
	Log     string    `json:"log,omitempty"`
	At      time.Time `json:"at"`
}

// Results stores execution results at runs-results/<runId>/<resultId>.json.
type Results struct {
	dir string
}

// NewResults builds the result store from the environment.
func NewResults() *Results {
	return &Results{dir: resultsDir()}
}

func resultsDir() string {
	if p := os.Getenv("DEVLAB_MERCURY_RUNS_RESULTS"); p != "" {
		return p
	}
	return filepath.Join("/var/lib/devlab/mercury", "runs-results")
}

// NewResultID mints a time-sortable result id (RFC3339Nano, colon-escaped) so filenames sort by time.
func NewResultID(t time.Time) string {
	return stem(t.UTC().Format(time.RFC3339Nano))
}

// Save writes a result atomically under its run's directory. It stamps UpdatedAt so the file records
// when the execution was last worked (used by FindStranded to bound resume by recency of activity).
func (r *Results) Save(res Result) error {
	res.UpdatedAt = time.Now().UTC()
	// A nil slice marshals to JSON null, which the UI iterates directly and crashes on (a black screen on
	// a failed/husk execution that completed no repo). Persist an empty slice instead — same guarantee the
	// aggregate listings already make (see All/ListForRun).
	if res.Repos == nil {
		res.Repos = []RepoResult{}
	}
	dir := filepath.Join(r.dir, res.RunID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, res.ResultID+".json.tmp")
	final := filepath.Join(dir, res.ResultID+".json")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}

// ListForRun returns result summaries for a run, newest first.
func (r *Results) ListForRun(runID string) ([]ResultRef, error) {
	dir := filepath.Join(r.dir, runID)
	refs := []ResultRef{} // never nil (see All)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return refs, nil
		}
		return refs, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		res, err := r.readFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		refs = append(refs, ResultRef{
			ResultID: res.ResultID, At: res.StartedAt, OK: res.OK, RepoCount: len(res.Repos),
			InputTokens: res.InputTokens, OutputTokens: res.OutputTokens, CostUSD: res.CostUSD,
			Suspended: res.Suspended, ResumeAt: res.ResumeAt,
		})
	}
	// Newest first, by the real time (not the string id — trimmed RFC3339Nano fractions mis-sort).
	sort.Slice(refs, func(i, j int) bool { return refs[i].At.After(refs[j].At) })
	return refs, nil
}

// DoneRepos is the set of repo ids already recorded in this execution — the ones a resume must skip.
func (res Result) DoneRepos() map[string]bool {
	done := make(map[string]bool, len(res.Repos))
	for _, rr := range res.Repos {
		done[rr.Repo] = true
	}
	return done
}

// newestHusk returns the newest unfinished (FinishedAt zero) execution of a run worked at or after
// notBefore. includeSuspended decides whether a usage-limit-suspended husk counts. Recency is bounded
// by last activity (UpdatedAt), not the frozen StartedAt, so a carry-over touched every night stays
// eligible however many nights it spans while a truly abandoned husk ages out (older files predating
// UpdatedAt fall back to StartedAt). Shared by FindStranded and FindResumable.
func (r *Results) newestHusk(runID string, notBefore time.Time, includeSuspended bool) (Result, bool) {
	dir := filepath.Join(r.dir, runID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Result{}, false
	}
	var best Result
	found := false
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		res, err := r.readFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		if !res.FinishedAt.IsZero() {
			continue // already finished
		}
		if res.Suspended && !includeSuspended {
			continue // a live suspension owned by the run.Suspended pointer path
		}
		touched := res.UpdatedAt
		if touched.IsZero() {
			touched = res.StartedAt
		}
		if touched.Before(notBefore) {
			continue // not worked within the window — treat as abandoned
		}
		if !found || res.StartedAt.After(best.StartedAt) {
			best, found = res, true
		}
	}
	return best, found
}

// FindStranded returns the newest execution of a run that was interrupted mid-flight — never finished
// (FinishedAt zero) and not paused on the usage limit (Suspended false) — provided it was worked at or
// after notBefore. That is the signature of a run killed by a devlabd restart / crash: the scheduler
// advanced NextFireAt before executing, so the next fire would otherwise start a FRESH execution and
// re-implement every repo into a duplicate PR set. Resuming this result instead skips the repos it had
// already completed. notBefore bounds it so a long-dead result after extended downtime is not
// resurrected. ok=false when there is nothing to resume.
func (r *Results) FindStranded(runID string, notBefore time.Time) (Result, bool) {
	return r.newestHusk(runID, notBefore, false)
}

// FindResumable is FindStranded PLUS orphaned usage-limit suspensions: an execution that suspended on
// the limit but whose run lost its Suspended pointer (e.g. a devlabd restart landed between writing the
// result and patching the run) would otherwise NEVER resume — it is skipped by FindStranded and no
// pointer references it, so it freezes forever, stranding its spend. Including suspended husks here lets
// the next fire pick it up and continue the not-yet-done repos, self-healing the lost pointer. A healthy
// suspension (pointer intact) is resumed by resumeOrNew's pointer check first and never reaches here.
func (r *Results) FindResumable(runID string, notBefore time.Time) (Result, bool) {
	return r.newestHusk(runID, notBefore, true)
}

// Get returns one result document.
func (r *Results) Get(runID, resultID string) (Result, bool, error) {
	res, err := r.readFile(filepath.Join(r.dir, runID, resultID+".json"))
	if err != nil {
		if os.IsNotExist(err) {
			return Result{}, false, nil
		}
		return Result{}, false, err
	}
	return res, true, nil
}

// ExecutionSummary is one completed execution across all runs — the Lauf-History row (with token/cost
// and the run name snapshot so it survives the run's deletion).
type ExecutionSummary struct {
	RunID        string     `json:"runId"`
	RunName      string     `json:"runName"`
	Type         Type       `json:"type,omitempty"` // auto|todo — the run's kind, so history is split per surface
	ResultID     string     `json:"resultId"`
	At           time.Time  `json:"at"`
	FinishedAt   time.Time  `json:"finishedAt,omitempty"`
	OK           bool       `json:"ok"`
	Suspended    bool       `json:"suspended,omitempty"`
	ResumeAt     *time.Time `json:"resumeAt,omitempty"`
	RepoCount    int        `json:"repoCount"`
	InputTokens  int        `json:"inputTokens"`
	OutputTokens int        `json:"outputTokens"`
	CostUSD      float64    `json:"costUsd"`
	NumTurns     int        `json:"numTurns"`
}

// All returns every stored execution across all runs (including runs since deleted), newest first —
// the Lauf-History.
func (r *Results) All() ([]ExecutionSummary, error) {
	runIDs, err := r.Runs()
	if err != nil {
		return nil, err
	}
	out := []ExecutionSummary{} // never nil: a nil slice marshals to JSON null and hangs the UI
	for _, id := range runIDs {
		dir := filepath.Join(r.dir, id)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			res, err := r.readFile(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			out = append(out, ExecutionSummary{
				RunID: res.RunID, RunName: res.RunName, Type: res.Type, ResultID: res.ResultID,
				At: res.StartedAt, FinishedAt: res.FinishedAt, OK: res.OK, RepoCount: len(res.Repos),
				Suspended: res.Suspended, ResumeAt: res.ResumeAt,
				InputTokens: res.InputTokens, OutputTokens: res.OutputTokens, CostUSD: res.CostUSD, NumTurns: res.NumTurns,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out, nil
}

// Runs returns the run ids that have stored results (including runs since deleted — orphan logs).
func (r *Results) Runs() ([]string, error) {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func (r *Results) readFile(path string) (Result, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Result{}, err
	}
	var res Result
	if err := json.Unmarshal(b, &res); err != nil {
		return Result{}, err
	}
	return res, nil
}
