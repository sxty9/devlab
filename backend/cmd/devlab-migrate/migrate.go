// The migration itself: PLAN, then APPLY. Planning reads the export and the existing pools and
// decides what is missing; applying writes exactly that. Nothing is derived twice and nothing is
// guessed: a record that cannot be mapped faithfully is refused by name instead of being
// imported half-way (S15).
//
// Idempotence is structural, not a flag: every step is guarded by "is it already there?", so a
// second run writes no byte at all.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
	"devlab/backend/internal/statepath"
)

// ownRepo names the service's OWN repository. Records aiming at it were requirements ON the
// rebuild; their deduplicated substance is the acceptance matrix, so they are not fed back as
// tasks (S15 step 2). This is the product's own name, not an instance value.
const ownRepo = "devlab"

// idShape bounds every identifier the export hands us before it becomes a path segment. The
// execution documents live in a directory named by the id, so a foreign id must never be able to
// leave the tree — and "." / ".." pass a character class, which is why they are rejected by name.
var idShape = regexp.MustCompile(`^[0-9A-Za-z_.:-]{1,64}$`)

func safeID(id string) error {
	if !idShape.MatchString(id) {
		return fmt.Errorf("id %q is not of the permitted shape", id)
	}
	if id == "." || id == ".." || strings.Contains(id, "..") {
		return fmt.Errorf("id %q is not usable as a directory name", id)
	}
	return nil
}

// archive is what the read-only legacy execution archive contributes.
type archive struct {
	// dir is the archive directory; "" when the instance has none.
	dir string
	// files counts the *.json records found in it.
	files int
	// imports are the archived executions to copy into the execution tree.
	imports []runs.Result
	// unmatched names archive files that produced no record (they stay in the moved archive).
	unmatched []string
	// movedTo is where the archive is put once its records are imported, so the tolerant
	// legacy read stops listing the same execution twice.
	movedTo string
}

// plan is the migration protocol: everything the import would do, and everything it finds
// already done. It is printed as-is (dry run) or printed after being applied.
type plan struct {
	stateRoot string
	inputPath string
	records   int

	autoRuns  []runs.Run
	openTodos []runs.Run
	history   []runs.Result
	notices   []OneOff
	arch      archive

	skippedOwn     int
	presentRuns    []string
	presentHistory []string
	presentNotices int
	lapsedDue      []string
	refusals       []string
}

// empty reports whether applying the plan would write nothing.
func (p *plan) empty() bool {
	return len(p.autoRuns) == 0 && len(p.openTodos) == 0 && len(p.history) == 0 &&
		len(p.notices) == 0 && len(p.arch.imports) == 0 && p.arch.movedTo == ""
}

// migrator holds the pools the import touches. They are the SAME access points the daemon uses —
// the migration adds no second data path.
type migrator struct {
	paths   *statepath.Paths
	runs    *runs.Store
	results *runs.ResultStore
	notices *runs.NoticeStore
	own     string
	now     time.Time
}

func newMigrator(p *statepath.Paths, own string, now time.Time) *migrator {
	return &migrator{
		paths:   p,
		runs:    runs.NewStore(p),
		results: runs.NewResultStore(p),
		notices: runs.NewNoticeStore(p),
		own:     own,
		now:     now.UTC(),
	}
}

// execDir and archiveDir mirror the frozen result store's own resolution (its documented per-pool
// env seams first, the state root otherwise) so the import's "is it already there?" checks look
// exactly where the store reads and writes.
func (m *migrator) execDir() string {
	if v := os.Getenv("DEVLAB_MERCURY_EXECUTIONS"); v != "" {
		return v
	}
	return m.paths.Executions()
}

func (m *migrator) archiveDir() string {
	if v := os.Getenv("DEVLAB_MERCURY_RUNS_RESULTS"); v != "" {
		return v
	}
	return m.paths.LegacyResults()
}

// imported reports whether an execution document already lives in the execution tree.
func (m *migrator) imported(id string) bool {
	_, err := os.Stat(filepath.Join(m.execDir(), id, "result.json"))
	return err == nil
}

// plan reads the export and the pools and decides what is missing.
func (m *migrator) plan(inputPath string) (*plan, error) {
	exp, err := readExport(inputPath)
	if err != nil {
		return nil, err
	}
	p := &plan{stateRoot: m.paths.Root, inputPath: inputPath, records: len(exp.Runs)}

	existingRuns, err := m.runs.List()
	if err != nil {
		return nil, fmt.Errorf("run pool unreadable: %w", err)
	}
	haveRunID := map[string]bool{}
	haveAutoTitle := map[string]bool{}
	for _, r := range existingRuns {
		haveRunID[r.ID] = true
		if r.Kind == model.KindAuto {
			haveAutoTitle[strings.ToLower(strings.TrimSpace(r.Title))] = true
		}
	}

	// The result store lists the execution tree AND the tolerant legacy archive, so one read
	// answers both "is this execution known?" and "what does the archive hold?".
	existingResults, err := m.results.List()
	if err != nil {
		return nil, fmt.Errorf("execution history unreadable: %w", err)
	}
	haveResult := map[string]bool{}
	for _, res := range existingResults {
		haveResult[res.ID] = true
	}
	m.planArchive(p, existingResults)

	for _, e := range exp.Runs {
		if err := safeID(e.ID); err != nil {
			p.refusals = append(p.refusals, "record id: "+err.Error())
			continue
		}
		if e.kind() == model.KindAuto {
			m.planAutoRun(p, e, haveRunID, haveAutoTitle)
			continue
		}
		if e.targetsOwnRepo(m.own) {
			p.skippedOwn++
			continue
		}
		if e.Done {
			m.planHistoryEntry(p, e, haveResult)
			continue
		}
		m.planOpenTodo(p, e, haveRunID)
	}

	if err := m.planNotices(p); err != nil {
		return nil, err
	}
	sort.Strings(p.presentRuns)
	sort.Strings(p.presentHistory)
	return p, nil
}

// planAutoRun prepares one recurring run. It is created WITHOUT an axiom assignment: an
// uncovered run is visible as uncovered, and the one planning path assigns axioms on the first
// constitution write with a session behind it (B-10). Its authorship is the import itself at
// import time — the schedule anchors on it, so a back-dated creation would make all seven runs
// due at once on the first tick.
func (m *migrator) planAutoRun(p *plan, e exportRun, haveID, haveTitle map[string]bool) {
	title := strings.TrimSpace(e.Name)
	if haveID[e.ID] || haveTitle[strings.ToLower(title)] {
		p.presentRuns = append(p.presentRuns, e.ID+" "+title)
		return
	}
	spec, err := e.schedule()
	if err != nil {
		p.refusals = append(p.refusals, err.Error())
		return
	}
	by := model.Actor{Autonomous: true}
	p.autoRuns = append(p.autoRuns, runs.Run{
		ID:       e.ID,
		Kind:     model.KindAuto,
		Title:    title,
		Schedule: spec,
		Active:   e.Enabled,
		// AxiomIDs, PromptSnapshot and PromptInputHash stay empty on purpose: the ONE
		// composition path fills them when the assignment happens.
		Authorship: model.Authorship{Created: by, CreatedAt: m.now, Updated: by, UpdatedAt: m.now},
	})
	haveID[e.ID] = true
	haveTitle[strings.ToLower(title)] = true
}

// planOpenTodo prepares the one open foreign task as an executable task with its original
// metadata. A due date that has already lapsed is NOT carried: it would fire the task on the
// first tick, and this phase feeds the foreign tasks in without running them.
func (m *migrator) planOpenTodo(p *plan, e exportRun, haveID map[string]bool) {
	if haveID[e.ID] {
		p.presentRuns = append(p.presentRuns, e.ID+" "+e.Name)
		return
	}
	due := e.DueAt
	if due != nil && !due.After(m.now) {
		p.lapsedDue = append(p.lapsedDue, fmt.Sprintf("%s (%s) — due %s", e.ID, e.Name, due.UTC().Format(time.RFC3339)))
		due = nil
	}
	// The actors are unknown: the export records no author, and unknown is never back-filled.
	auth := model.Authorship{CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt}
	p.openTodos = append(p.openTodos, runs.Run{
		ID:          e.ID,
		Kind:        model.KindTodo,
		Title:       strings.TrimSpace(e.Name),
		Task:        e.Task,
		Targets:     e.targets(),
		DueAt:       due,
		Attachments: e.attachments(),
		Authorship:  auth,
	})
	haveID[e.ID] = true
}

// planHistoryEntry prepares one completed foreign task as a history entry: an execution document
// with the original metadata and NO run definition, so it shows up in the history and never as an
// open task again. The archive's richer record wins whenever it exists.
func (m *migrator) planHistoryEntry(p *plan, e exportRun, haveResult map[string]bool) {
	lr := e.LastResult
	if lr == nil {
		p.refusals = append(p.refusals, fmt.Sprintf("completed record %s (%s) carries no execution summary", e.ID, e.Name))
		return
	}
	id := lr.ResultID
	if id == "" {
		id = runs.NewResultID(lr.At)
	}
	if err := safeID(id); err != nil {
		p.refusals = append(p.refusals, "execution id: "+err.Error())
		return
	}
	if haveResult[id] {
		p.presentHistory = append(p.presentHistory, id+" "+e.Name)
		return
	}
	at := lr.At
	repos := make([]model.RepoPipeline, 0, len(e.repoNames()))
	for _, name := range e.repoNames() {
		// No stages: the export carries no per-stage detail, and a claimed stage state is
		// exactly the false green the rebuild exists to remove (K-4).
		repos = append(repos, model.RepoPipeline{Repo: name, Stages: []model.StageView{}})
	}
	p.history = append(p.history, runs.Result{
		ID:        id,
		RunID:     e.ID,
		RunTitle:  strings.TrimSpace(e.Name),
		Kind:      model.KindTodo,
		StartedAt: at,
		EndedAt:   &at,
		Repos:     repos,
		Report:    historyReport(e, lr),
		Usage: model.UsageView{
			InputTokens: lr.InputTokens, OutputTokens: lr.OutputTokens, CostUSD: lr.CostUSD,
		},
		Prompt:    e.Prompt,
		Requested: model.Authorship{CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt},
		Legacy:    true,
	})
	haveResult[id] = true
}

// historyReport states WHERE this entry comes from and WHAT the source did not carry, so the
// history never presents an imported summary as if it were a fully recorded execution.
func historyReport(e exportRun, lr *exportResultSummary) string {
	var b strings.Builder
	b.WriteString("## Imported history entry\n\n")
	b.WriteString("This execution predates the rebuild and was taken from the run export.\n\n")
	b.WriteString("- original record: `" + e.ID + "` (task)\n")
	b.WriteString("- recorded outcome: ")
	if lr.OK {
		b.WriteString("succeeded\n")
	} else {
		b.WriteString("failed\n")
	}
	b.WriteString("- recorded at: " + lr.At.UTC().Format(time.RFC3339) + " (the export carried a single timestamp)\n")
	if names := e.repoNames(); len(names) > 0 {
		b.WriteString("- repositories: " + strings.Join(names, ", ") + "\n")
	}
	if len(e.Attachments) > 0 {
		files := make([]string, 0, len(e.Attachments))
		for _, a := range e.Attachments {
			files = append(files, a.Filename)
		}
		b.WriteString("- attachments (metadata only; the files stay in the attachment pool): " +
			strings.Join(files, ", ") + "\n")
	}
	b.WriteString("\nThe export carried no per-stage detail, so this entry claims none.\n")
	return b.String()
}

// planArchive plans the read-only legacy archive: every archived execution the tolerant reader
// could map is copied into the execution tree, then the archive is moved aside — otherwise the
// same execution would be listed twice, once from each location.
func (m *migrator) planArchive(p *plan, existing []runs.Result) {
	dir := m.archiveDir()
	if dir == "" {
		return
	}
	files, err := archiveFiles(dir)
	if err != nil {
		return // no archive on this instance — nothing to import
	}
	p.arch.dir = dir
	p.arch.files = len(files)

	matched := map[string]bool{}
	for _, res := range existing {
		if !res.Legacy || m.imported(res.ID) {
			continue // written by an earlier import, or not from the archive at all
		}
		matched[res.ID] = true
		if err := safeID(res.ID); err != nil {
			p.refusals = append(p.refusals, "archived execution id: "+err.Error())
			continue
		}
		p.arch.imports = append(p.arch.imports, res)
	}
	sort.Slice(p.arch.imports, func(i, j int) bool { return p.arch.imports[i].ID < p.arch.imports[j].ID })
	for _, f := range files {
		// The old store wrote one file per execution, named by its id — so an unmatched file
		// is one the tolerant read could not turn into a record. It is named, never dropped:
		// the moved archive keeps it verbatim.
		if !matched[f] && !m.imported(f) {
			p.arch.unmatched = append(p.arch.unmatched, f)
		}
	}
	sort.Strings(p.arch.unmatched)
	p.arch.movedTo = dir + ".imported"
}

// archiveFiles lists the execution ids the archive holds (one file per execution, named by id).
func archiveFiles(dir string) ([]string, error) {
	runDirs, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, rd := range runDirs {
		if !rd.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(dir, rd.Name()))
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
				continue
			}
			out = append(out, strings.TrimSuffix(f.Name(), ".json"))
		}
	}
	return out, nil
}

// planNotices prepares the M1–M8 protocol, skipping the items already recorded.
func (m *migrator) planNotices(p *plan) error {
	have, err := m.notices.List()
	if err != nil {
		return fmt.Errorf("notice pool unreadable: %w", err)
	}
	known := map[string]bool{}
	for _, n := range have {
		known[n.ID] = true
	}
	for _, o := range oneOffs() {
		if known[noticeIDPrefix+o.Key] {
			p.presentNotices++
			continue
		}
		p.notices = append(p.notices, o)
	}
	return nil
}

// errRefused reports that the export holds records the import will not guess at.
var errRefused = errors.New("the export holds records that cannot be imported faithfully")

// apply writes the plan. Refusals abort BEFORE the first write, so a half-imported state cannot
// exist. Every step is guarded by its own "already there?" check, which is what makes a second
// run a no-op.
func (m *migrator) apply(p *plan) error {
	if len(p.refusals) > 0 {
		return errRefused
	}
	if err := m.paths.CheckWritable(); err != nil {
		return err
	}
	if err := m.applyRuns(p); err != nil {
		return err
	}
	// The archive first: its records carry per-stage detail, so they win over an export summary
	// of the same execution (the plan already dropped the duplicate summaries).
	for _, res := range p.arch.imports {
		if m.imported(res.ID) {
			continue
		}
		if err := m.results.Put(res); err != nil {
			return fmt.Errorf("archived execution %s: %w", res.ID, err)
		}
	}
	if p.arch.movedTo != "" {
		if err := moveAside(p.arch.dir, p.arch.movedTo); err != nil {
			return fmt.Errorf("moving the archive aside: %w", err)
		}
	}
	for _, res := range p.history {
		if m.imported(res.ID) {
			continue
		}
		if err := m.results.Put(res); err != nil {
			return fmt.Errorf("history entry %s: %w", res.ID, err)
		}
	}
	for _, o := range p.notices {
		if err := m.notices.Add(o.notice(m.now)); err != nil {
			return fmt.Errorf("migration protocol %s: %w", o.Key, err)
		}
	}
	return nil
}

// applyRuns folds the missing run definitions into the pool in ONE atomic mutation — no snapshot
// per record: the import is a single bulk write, not a series of user edits.
func (m *migrator) applyRuns(p *plan) error {
	add := append(append([]runs.Run{}, p.autoRuns...), p.openTodos...)
	if len(add) == 0 {
		return nil
	}
	_, err := m.runs.Patch(func(cur []runs.Run) ([]runs.Run, error) {
		have := map[string]bool{}
		titles := map[string]bool{}
		for _, r := range cur {
			have[r.ID] = true
			if r.Kind == model.KindAuto {
				titles[strings.ToLower(strings.TrimSpace(r.Title))] = true
			}
		}
		for _, r := range add {
			if have[r.ID] {
				continue
			}
			if r.Kind == model.KindAuto && titles[strings.ToLower(r.Title)] {
				continue
			}
			cur = append(cur, r)
			have[r.ID] = true
			if r.Kind == model.KindAuto {
				titles[strings.ToLower(r.Title)] = true
			}
		}
		return cur, nil
	})
	if err != nil {
		return fmt.Errorf("run pool: %w", err)
	}
	return nil
}

// moveAside renames src to dst. Nothing is deleted: the archive keeps existing, under a name the
// tolerant legacy read no longer looks at. A destination left by an interrupted earlier attempt is
// filled up entry by entry, so repeating the move is safe.
func moveAside(src, dst string) error {
	if _, err := os.Stat(dst); err != nil {
		if os.IsNotExist(err) {
			return os.Rename(src, dst)
		}
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		from, to := filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())
		if _, err := os.Stat(to); err == nil {
			continue // already moved by the earlier attempt
		}
		if err := os.Rename(from, to); err != nil {
			return err
		}
	}
	// Only an EMPTY leftover is removed; anything still in it stays where the operator sees it.
	_ = os.Remove(src)
	return nil
}
