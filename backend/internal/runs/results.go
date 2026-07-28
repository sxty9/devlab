// Execution results — ONE document per execution (Welle-0 contract, A1-7).
//
// New format: executions/<id>/result.json carries the per-repo server stage array
// ([]model.RepoPipeline); the live transcript is an append journal executions/<id>/transcript.jsonl.
// Legacy format: runs-results/<runId>/<resultId>.json (the pre-rebuild shape) is read TOLERANTLY
// and mapped onto the new shape so old executions stay viewable (REQ-027.3). Legacy states are
// display-only: they are never written again.
package runs

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"devlab/backend/internal/fsatomic"
	"devlab/backend/internal/model"
	"devlab/backend/internal/statepath"
)

// Result is one execution of a run — the ONE result document (new stage-array format).
type Result struct {
	ID        string        `json:"id"`
	RunID     string        `json:"runId"`
	RunTitle  string        `json:"runTitle,omitempty"` // snapshot; survives run deletion
	Kind      model.RunKind `json:"kind"`
	Model     string        `json:"model,omitempty"`
	StartedAt time.Time     `json:"startedAt"`
	EndedAt   *time.Time    `json:"endedAt,omitempty"`
	// MergedAt is set once ALL deliveries of this execution are merged/rolled back/closed with
	// reason — the time of the LAST one (B-8 rule, ARCHITEKTUR §6.5).
	MergedAt *time.Time `json:"mergedAt,omitempty"`
	// Repos is THE server stage array — the client only renders it (B-17).
	Repos []model.RepoPipeline `json:"repos"`
	// Report is the three-part execution report (done / running or outstanding / open, blocked
	// or needing a decision — D 41), Markdown.
	Report string          `json:"report,omitempty"`
	Usage  model.UsageView `json:"usage"`
	// Prompt is the exact prompt this execution was driven by (snapshot).
	Prompt    string           `json:"prompt,omitempty"`
	Requested model.Authorship `json:"requested"`
	// Synthetic marks a result synthesized by the startup reconciliation (B-5), never by a run.
	Synthetic bool `json:"synthetic,omitempty"`
	// Legacy is true for a document read from the pre-rebuild archive — display-only provenance.
	Legacy bool `json:"legacy,omitempty"`
}

// NewResultID mints a time-sortable execution id (a filename-safe path segment).
func NewResultID(t time.Time) string {
	return "exec_" + strings.ReplaceAll(t.UTC().Format("20060102-150405.000000000"), ".", "-")
}

// ResultStore reads and writes result documents. It writes ONLY the new format below
// executions/<id>/; it additionally reads the legacy archive runs-results/ tolerantly.
type ResultStore struct {
	execDir   string
	legacyDir string
}

// NewResultStore builds the store below the given state root. The env overrides
// DEVLAB_MERCURY_EXECUTIONS (new) and DEVLAB_MERCURY_RUNS_RESULTS (legacy archive) are honored
// first — ported test seams.
func NewResultStore(p *statepath.Paths) *ResultStore {
	execDir := os.Getenv("DEVLAB_MERCURY_EXECUTIONS")
	legacyDir := os.Getenv("DEVLAB_MERCURY_RUNS_RESULTS")
	if execDir == "" && p != nil {
		execDir = p.Executions()
	}
	if legacyDir == "" && p != nil {
		legacyDir = p.LegacyResults()
	}
	return &ResultStore{execDir: execDir, legacyDir: legacyDir}
}

// Put writes one result document atomically (written live; coalescing is the recorder's concern).
func (r *ResultStore) Put(res Result) error {
	if res.ID == "" {
		return errors.New("result without id")
	}
	if res.Repos == nil {
		// A nil slice marshals to JSON null, which every array consumer must otherwise guard.
		res.Repos = []model.RepoPipeline{}
	}
	return fsatomic.WriteJSON(filepath.Join(r.execDir, res.ID, "result.json"), res)
}

// Get returns one result by execution id — new format first, then the legacy archive.
func (r *ResultStore) Get(id string) (Result, bool, error) {
	b, err := os.ReadFile(filepath.Join(r.execDir, id, "result.json"))
	if err == nil {
		var res Result
		if uerr := json.Unmarshal(b, &res); uerr != nil {
			return Result{}, false, uerr
		}
		normalizeResult(&res)
		return res, true, nil
	}
	if !os.IsNotExist(err) {
		return Result{}, false, err
	}
	all, err := r.legacyAll()
	if err != nil {
		return Result{}, false, err
	}
	for _, res := range all {
		if res.ID == id {
			return res, true, nil
		}
	}
	return Result{}, false, nil
}

// ForRun returns all results of one run (new + legacy), newest first.
func (r *ResultStore) ForRun(runID string) ([]Result, error) {
	all, err := r.List()
	if err != nil {
		return nil, err
	}
	out := []Result{}
	for _, res := range all {
		if res.RunID == runID {
			out = append(out, res)
		}
	}
	return out, nil
}

// List returns every result (new + legacy), newest first. A torn or foreign file is skipped,
// never fatal — an old husk must not black out the history.
func (r *ResultStore) List() ([]Result, error) {
	out := []Result{}
	entries, err := os.ReadDir(r.execDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(r.execDir, e.Name(), "result.json"))
		if err != nil {
			continue
		}
		var res Result
		if err := json.Unmarshal(b, &res); err != nil {
			continue
		}
		normalizeResult(&res)
		out = append(out, res)
	}
	legacy, err := r.legacyAll()
	if err != nil {
		return nil, err
	}
	out = append(out, legacy...)
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out, nil
}

// AppendTranscript appends one compact transcript line to the execution's journal
// (fsatomic.AppendLine — the one append primitive; B-11).
func (r *ResultStore) AppendTranscript(execID string, line []byte) error {
	if execID == "" {
		return errors.New("transcript without execution id")
	}
	return fsatomic.AppendLine(filepath.Join(r.execDir, execID, "transcript.jsonl"), line)
}

// ReadTranscriptTail returns up to maxBytes from the end of the execution's transcript journal,
// starting at a line boundary (the torn first line of a mid-file cut is dropped).
func (r *ResultStore) ReadTranscriptTail(execID string, maxBytes int) (string, error) {
	f, err := os.Open(filepath.Join(r.execDir, execID, "transcript.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	var offset int64
	if maxBytes > 0 && fi.Size() > int64(maxBytes) {
		offset = fi.Size() - int64(maxBytes)
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return "", err
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	s := string(b)
	if offset > 0 {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
	}
	return s, nil
}

func normalizeResult(res *Result) {
	if res.Repos == nil {
		res.Repos = []model.RepoPipeline{}
	}
	for i := range res.Repos {
		res.Repos[i].Done, res.Repos[i].Succeeded = model.PipelineSucceeded(res.Repos[i].Stages)
	}
}

// ── Tolerant legacy reading (REQ-027.3) ─────────────────────────────────────────────────

// legacyResult is the pre-rebuild result shape — only the fields the mapping needs. Unknown
// fields are ignored; missing fields default. Derived from the archived format (fixture:
// testdata/legacy-result.json, taken from the old runs-results layout at ae5eed5).
type legacyResult struct {
	RunID      string    `json:"runId"`
	ResultID   string    `json:"resultId"`
	RunName    string    `json:"runName"`
	Type       string    `json:"type"` // "" reads as auto
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
	Prompt     string    `json:"prompt"`
	Model      string    `json:"model"`
	OK         bool      `json:"ok"`
	Trigger    struct {
		Auto bool   `json:"auto"`
		By   string `json:"by"`
	} `json:"trigger"`
	RequestedBy  string             `json:"requestedBy"`
	Repos        []legacyRepoResult `json:"repos"`
	Live         *legacyRepoResult  `json:"live"`
	InputTokens  int64              `json:"inputTokens"`
	OutputTokens int64              `json:"outputTokens"`
	CostUSD      float64            `json:"costUsd"`
}

type legacyRepoResult struct {
	Repo  string `json:"repo"`
	OK    bool   `json:"ok"`
	Error string `json:"error"`
	Steps []struct {
		Name    string    `json:"name"`
		Running bool      `json:"running"`
		Status  string    `json:"status"`
		OK      *bool     `json:"ok"` // the pre-status legacy of the legacy
		Log     string    `json:"log"`
		At      time.Time `json:"at"`
	} `json:"steps"`
}

// mapLegacy maps one archived result onto the new shape. Legacy step names (analyze, dev-deploy,
// push, pr, …) are carried VERBATIM as stage names — displayable, never produced anew.
func mapLegacy(lr legacyResult) Result {
	res := Result{
		ID:        lr.ResultID,
		RunID:     lr.RunID,
		RunTitle:  lr.RunName,
		Kind:      model.KindAuto,
		Model:     lr.Model,
		StartedAt: lr.StartedAt,
		Prompt:    lr.Prompt,
		Usage:     model.UsageView{InputTokens: lr.InputTokens, OutputTokens: lr.OutputTokens, CostUSD: lr.CostUSD},
		Legacy:    true,
	}
	if lr.Type == string(model.KindTodo) {
		res.Kind = model.KindTodo
	}
	if !lr.FinishedAt.IsZero() {
		t := lr.FinishedAt
		res.EndedAt = &t
	}
	if lr.Trigger.By != "" {
		res.Requested.Created = model.Actor{User: lr.Trigger.By}
	} else if lr.Trigger.Auto {
		res.Requested.Created = model.Actor{Autonomous: true, OnBehalfOf: lr.RequestedBy}
	}
	repos := lr.Repos
	if lr.Live != nil {
		repos = append(repos, *lr.Live)
	}
	res.Repos = make([]model.RepoPipeline, 0, len(repos))
	for _, rr := range repos {
		rp := model.RepoPipeline{Repo: rr.Repo}
		for _, st := range rr.Steps {
			sv := model.StageView{Stage: model.Stage(st.Name), Log: st.Log}
			switch {
			case st.Running:
				sv.State = model.StepRunning
			case st.Status == "ok" || (st.Status == "" && st.OK != nil && *st.OK):
				sv.State = model.StepExecuted
			case st.Status == "not-applicable":
				sv.State = model.StepNotApplicable
				sv.Reason = st.Log
			case st.Status == "not-executed":
				sv.State = model.StepNotExecuted
				sv.Reason = st.Log
			default:
				sv.State = model.StepFailed
				sv.Reason = st.Log
			}
			if !st.At.IsZero() {
				at := st.At
				sv.EndedAt = &at
			}
			rp.Stages = append(rp.Stages, sv)
		}
		if len(rp.Stages) == 0 && rr.Error != "" {
			// A repo that failed before any step ran still renders a defined state.
			rp.Stages = []model.StageView{{Stage: model.StagePreflight, State: model.StepFailed, Reason: rr.Error}}
		}
		rp.Done, rp.Succeeded = model.PipelineSucceeded(rp.Stages)
		res.Repos = append(res.Repos, rp)
	}
	return res
}

// legacyAll reads the whole legacy archive tolerantly: any file that does not parse is skipped,
// never fatal.
func (r *ResultStore) legacyAll() ([]Result, error) {
	out := []Result{}
	if r.legacyDir == "" {
		return out, nil
	}
	runDirs, err := os.ReadDir(r.legacyDir)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	for _, rd := range runDirs {
		if !rd.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(r.legacyDir, rd.Name()))
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
				continue
			}
			b, err := os.ReadFile(filepath.Join(r.legacyDir, rd.Name(), f.Name()))
			if err != nil {
				continue
			}
			var lr legacyResult
			if err := json.Unmarshal(b, &lr); err != nil {
				continue
			}
			if lr.ResultID == "" {
				lr.ResultID = strings.TrimSuffix(f.Name(), ".json")
			}
			if lr.RunID == "" {
				lr.RunID = rd.Name()
			}
			out = append(out, mapLegacy(lr))
		}
	}
	return out, nil
}
