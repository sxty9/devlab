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

	// The takeover of the pre-rebuild stock (takeover.go): the run pool split by form, the
	// delivery ledger's conversion, the config snapshots that hold pre-rebuild records, and the
	// pools the rebuild has no reader for. They are the reason the import CARRIES the state over
	// instead of writing beside it.
	pool    *runPool
	ledger  *ledgerTakeover
	snaps   *snapshotTakeover
	orphans []orphan
	// backups are the operator's own hand-made copies of the run pool. They are reported and
	// deliberately not touched (leftoverBackups), so they never make the plan non-empty.
	backups *leftoverBackups

	skippedOwn     int
	presentRuns    []string
	presentHistory []string
	presentNotices int
	lapsedDue      []string
	// heldInactive names the recurring runs the export had switched ON and the import creates
	// switched OFF, because they carry no axioms yet and therefore no prompt. It is the
	// protocol's counterpart to the activation gate item.
	heldInactive []string
	refusals     []string
}

// empty reports whether applying the plan would write nothing. The takeover counts: a state
// directory whose pools are already in the rebuilt form is what makes a second run a no-op — not
// the absence of records to import.
func (p *plan) empty() bool {
	return len(p.autoRuns) == 0 && len(p.openTodos) == 0 && len(p.history) == 0 &&
		len(p.notices) == 0 && len(p.arch.imports) == 0 && p.arch.movedTo == "" &&
		!p.pool.takenOver() && p.ledger.count() == 0 && len(p.snaps.moved) == 0 &&
		len(p.orphans) == 0
}

// poolAfter is the run pool the takeover writes: the records already in the rebuilt form, then the
// ones this import adds. The deduplication happened ONCE, while planning (haveRunID/haveAutoTitle
// are seeded from exactly these existing records), so there is no second dedup here that could
// disagree with it.
func (p *plan) poolAfter() []runs.Run {
	out := make([]runs.Run, 0, len(p.pool.newForm)+len(p.autoRuns)+len(p.openTodos))
	out = append(out, p.pool.newForm...)
	out = append(out, p.autoRuns...)
	return append(out, p.openTodos...)
}

// migrator holds the pools the import touches. They are the SAME access points the daemon uses —
// the migration adds no second data path.
type migrator struct {
	paths      *statepath.Paths
	runs       *runs.Store
	results    *runs.ResultStore
	notices    *runs.NoticeStore
	deliveries *runs.DeliveryStore
	own        string
	now        time.Time
}

func newMigrator(p *statepath.Paths, own string, now time.Time) *migrator {
	return &migrator{
		paths:      p,
		runs:       runs.NewStore(p),
		results:    runs.NewResultStore(p),
		notices:    runs.NewNoticeStore(p),
		deliveries: runs.NewDeliveryStore(p),
		own:        own,
		now:        now.UTC(),
	}
}

// poolPath mirrors a store's OWN path resolution — its documented per-pool env seam first, the
// state root otherwise — so every read and every write of the import lands exactly where the
// daemon's store reads and writes. One helper, one rule, for all of them.
func (m *migrator) poolPath(env string, fromRoot func() string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	return fromRoot()
}

func (m *migrator) execDir() string {
	return m.poolPath("DEVLAB_MERCURY_EXECUTIONS", m.paths.Executions)
}

func (m *migrator) archiveDir() string {
	return m.poolPath("DEVLAB_MERCURY_RUNS_RESULTS", m.paths.LegacyResults)
}

func (m *migrator) runsPath() string {
	return m.poolPath("DEVLAB_MERCURY_RUNS", m.paths.Runs)
}

func (m *migrator) ledgerPath() string {
	return m.poolPath("DEVLAB_MERCURY_RUNS_DELIVERIES", m.paths.Deliveries)
}

func (m *migrator) historyDir() string {
	return m.poolPath("DEVLAB_MERCURY_RUNS_HISTORY", m.paths.HistoryDir)
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

	// The run pool is read BY FORM, not through the store's typed decode. This is the whole
	// difference between an import that carries the state over and one that lies down beside it:
	// the pre-rebuild record and the rebuilt record share their `id`, so the typed decode yields a
	// blank run per pre-rebuild record and every id then answers "already imported" — the import
	// writes nothing and the pool keeps a set of runs the surface shows as nameless. Only records
	// in the REBUILT form count as present; the rest is set aside by the takeover below.
	pool, err := readRunPool(m.runsPath())
	if err != nil {
		return nil, fmt.Errorf("run pool unreadable: %w", err)
	}
	p.pool = pool
	haveRunID := map[string]bool{}
	haveAutoTitle := map[string]bool{}
	for _, r := range pool.newForm {
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
	if err := m.planTakeover(p); err != nil {
		return nil, err
	}
	// The bar comes LAST, over everything the import would create: nothing may enter the pool
	// able to fire without a prompt that names its subject.
	barUnsubstantiated(p)
	sort.Strings(p.presentRuns)
	sort.Strings(p.presentHistory)
	return p, nil
}

// planTakeover plans everything the import carries over rather than adds: the delivery ledger's
// conversion, the config snapshots that would re-inject pre-rebuild records, the pools the rebuild
// has no reader for, and the operator's own copies of the run pool (reported, never touched). The
// run pool itself was already classified in plan().
func (m *migrator) planTakeover(p *plan) error {
	ledger, err := readLedgerTakeover(m.ledgerPath())
	if err != nil {
		return err
	}
	p.ledger = ledger
	p.refusals = append(p.refusals, ledger.refusals...)

	snaps, err := readSnapshotTakeover(m.historyDir())
	if err != nil {
		return err
	}
	p.snaps = snaps

	for _, o := range orphanPools() {
		from := filepath.Join(m.paths.Mercury(), o.name)
		b, err := os.ReadFile(from)
		if err != nil {
			continue // not on this instance
		}
		to, err := freeAsidePath(from)
		if err != nil {
			return err
		}
		held := ""
		if o.held != nil {
			held = o.held(b)
		}
		p.orphans = append(p.orphans, orphan{from: from, to: to, why: o.why, held: held})
	}

	backups, err := readLeftoverBackups(m.paths.Mercury())
	if err != nil {
		return err
	}
	p.backups = backups
	return nil
}

// wouldFire answers "would the scheduler start this freshly imported record?" by the SAME two
// conditions sched.IsDue applies — the one place "due" is decided — for a record with no
// execution behind it yet: a recurring run fires while it is active and carries a recurrence, a
// task fires once it carries a due date. The conditions are mirrored deliberately: the import has
// to judge admission by the rule that will actually admit the record.
func wouldFire(r runs.Run) bool {
	if r.Kind == model.KindTodo {
		return r.DueAt != nil
	}
	return r.Active && r.Schedule != nil
}

// unsubstantiated names why a record must not be imported ready to fire, or "" when it is sound.
//
// An execution takes the run's prompt snapshot VERBATIM and prepends only the division-of-labor
// preamble. A record that can fire without a prompt that names its subject therefore sends an
// unattended agent into every one of its target repositories with "you implement the task", no
// task named, and the standing order never to end with a question and never to shrink the task.
// That is not a half-finished import — it is an autonomous sweep without a subject, and an empty
// axiom list produces exactly the same thing one step later (the composer writes its heading and
// lists nothing beneath it). Both are refused by name.
func unsubstantiated(r runs.Run) string {
	if !wouldFire(r) {
		return ""
	}
	if strings.TrimSpace(r.PromptSnapshot) == "" {
		return "carries no prompt snapshot"
	}
	if r.Kind == model.KindAuto && len(r.AxiomIDs) == 0 {
		return "carries no axioms, so its prompt names no subject"
	}
	if r.Kind == model.KindTodo && strings.TrimSpace(r.Task) == "" {
		return "carries no task text, so its prompt names no subject"
	}
	return ""
}

// barUnsubstantiated is the import's own bar: it turns every unsound record into a named refusal,
// which aborts the whole import before the first write. It is a structural tripwire, not a
// repair — the planning above is what keeps records sound, and this states out loud what "sound"
// means so a later change cannot quietly drop it.
func barUnsubstantiated(p *plan) {
	for _, r := range append(append([]runs.Run{}, p.autoRuns...), p.openTodos...) {
		why := unsubstantiated(r)
		if why == "" {
			continue
		}
		p.refusals = append(p.refusals, fmt.Sprintf(
			"record %s (%s) would be imported ready to fire and %s — its execution would run the bare preamble",
			r.ID, r.Title, why))
	}
}

// planAutoRun prepares one recurring run. Two properties belong together and are both deliberate:
//
//   - WITHOUT an axiom assignment: an uncovered run is visible as uncovered, and the one planning
//     path assigns axioms on the first constitution write with a session behind it (B-10). No
//     prompt is composed here either — an empty snapshot is the honest "not composed yet", while a
//     composition over zero axioms would look complete and name nothing.
//   - INACTIVE, whatever the export's switch said: an active run with no axioms and no prompt is
//     due on its next window and would execute the bare preamble across every repository of the
//     instance. The export's switch is not lost — it is protocolled per run and the activation
//     gate item states what has to happen first (deploy/migration/10-daten.md). Activation is the
//     operator's step AFTER the assignment, and the bar above refuses any record that would slip
//     in ready to fire without a subject.
//
// Its authorship is the import itself at import time — the schedule anchors on it, so a
// back-dated creation would make all seven runs due at once on the first tick.
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
		// Active is false for every imported run, see above.
		Active: false,
		// AxiomIDs, PromptSnapshot and PromptInputHash stay empty on purpose: the ONE
		// composition path fills them when the assignment happens.
		Authorship: model.Authorship{Created: by, CreatedAt: m.now, Updated: by, UpdatedAt: m.now},
	})
	if e.Enabled {
		p.heldInactive = append(p.heldInactive, e.ID+" "+title)
	}
	haveID[e.ID] = true
	haveTitle[strings.ToLower(title)] = true
}

// planOpenTodo prepares the one open foreign task as an executable task with its original
// metadata. A due date that has already lapsed is NOT carried: it would fire the task on the
// first tick, and this phase feeds the foreign tasks in without running them.
//
// The prompt snapshot is COMPOSED here, through runs.ComposeInto — the one composition path
// (REQ-003). It is not copied from the export: the old composition is a foreign string of unknown
// age, while the snapshot is what an execution hands the agent verbatim. Composing it at import
// time is what makes the task executable at all; leaving it empty would make the task startable
// and subjectless at once.
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
	todo := runs.Run{
		ID:          e.ID,
		Kind:        model.KindTodo,
		Title:       strings.TrimSpace(e.Name),
		Task:        e.Task,
		Targets:     e.targets(),
		DueAt:       due,
		Attachments: e.attachments(),
		Authorship:  auth,
	}
	// Composed through the ONE path with an UNSCANNED catalog: this import runs offline, without the
	// constitution store (no session, no token, possibly no clone). A prompt carries the constitution
	// in full wording (REQ-002.1), so the snapshot composed here NAMES the missing wording instead of
	// pretending the constitution is empty — and because its input fingerprint records "corpus not
	// read", the first constitution write recomposes the task in full (runs.RecomposeDrifted).
	runs.ComposeInto(&todo, runs.Catalog{})
	p.openTodos = append(p.openTodos, todo)
	haveID[e.ID] = true
}

// stageArchivedOutcome is the ONE stage an imported history entry carries, and it is deliberately
// NOT a chain stage. The export recorded a single outcome flag and a single timestamp for the
// whole record, so claiming preflight, implement, delivery or pull-request states would be exactly
// the false green the rebuild exists to remove (K-4). This entry claims one thing and states it as
// its own name: the outcome the source recorded. The surface renders the server's stage name
// verbatim (B-35), the way it already renders the archive's historical stage names, so the entry
// labels itself as an archive state and needs no case of its own.
const stageArchivedOutcome = model.Stage("archived-outcome")

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
		rp := model.RepoPipeline{Repo: name, Stages: []model.StageView{archivedOutcomeStage(lr)}}
		// Derived, never invented: the same one derivation the store applies on every read, so
		// the written document and the read document say the same thing.
		rp.Done, rp.Succeeded = model.PipelineSucceeded(rp.Stages)
		repos = append(repos, rp)
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

// archivedOutcomeStage carries the recorded outcome as a terminal state, so the entry is DONE and
// its success is the one the source recorded — an entry left without stages is neither done nor
// successful and the surface would have to call a recorded success "incomplete". The provenance
// note sits in the field the contract designates for the state at hand: the reason is mandatory
// for a failure, the log carries the note otherwise.
func archivedOutcomeStage(lr *exportResultSummary) model.StageView {
	at := lr.At
	sv := model.StageView{Stage: stageArchivedOutcome, State: model.StepExecuted, EndedAt: &at}
	note := "outcome recorded by the pre-rebuild export for the record as a whole — one flag, one " +
		"timestamp, no per-stage detail"
	if !lr.OK {
		sv.State, sv.Reason = model.StepFailed, note
		return sv
	}
	sv.Log = note
	return sv
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
	b.WriteString("\nThe export carried no per-stage detail. This entry therefore records the outcome " +
		"above as its one stage (`" + string(stageArchivedOutcome) + "`) and claims no stage of the " +
		"delivery chain.\n")
	b.WriteString("\nNo run definition stands behind this entry: the record was completed before the " +
		"rebuild and exists as history only, so it cannot be started again from here.\n")
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

// planNotices prepares the migration protocol, skipping the items already recorded.
func (m *migrator) planNotices(p *plan) error {
	have, err := m.notices.List()
	if err != nil {
		return fmt.Errorf("notice pool unreadable: %w", err)
	}
	known := map[string]bool{}
	for _, n := range have {
		known[n.ID] = true
	}
	for _, o := range protocolItems() {
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
	// The pre-rebuild stock is set aside BEFORE anything is rewritten, so no step can lose it:
	// the snapshots and the orphaned pools move out of the way first, and each pool that is
	// rewritten gets its verbatim copy beside it before the rewrite.
	if err := moveFilesAside(p.snaps.dir, p.snaps.to, p.snaps.moved); err != nil {
		return fmt.Errorf("setting the pre-rebuild config snapshots aside: %w", err)
	}
	for _, o := range p.orphans {
		if err := os.Rename(o.from, o.to); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("setting %s aside: %w", o.from, err)
		}
	}
	if err := m.applyRuns(p); err != nil {
		return err
	}
	if err := m.applyLedger(p); err != nil {
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

// applyRuns writes the run pool the takeover decided: the records already in the rebuilt form plus
// the imported ones, in ONE atomic replacement — no snapshot per record, because the import is a
// single bulk event, not a series of user edits.
//
// It REPLACES rather than folds in, and that is the point: folding in would have to read the pool
// back through the typed decode, which is what turns a pre-rebuild record into a blank run and
// writes it straight back (measured: a no-op fold over the real pool rewrote 63 records into 63
// nameless ones and lost 171 of 309 KB). The verbatim copy is written first, so the old stock
// exists under its own name before a byte of the pool changes.
//
// The one snapshot ReplaceAll takes is deliberate: the pre-rebuild snapshots were just set aside,
// so this is the operator's first — and, right after the import, only — restore point.
func (m *migrator) applyRuns(p *plan) error {
	if !p.pool.takenOver() && len(p.autoRuns)+len(p.openTodos) == 0 {
		return nil
	}
	if p.pool.takenOver() {
		if err := copyAside(p.pool.path, p.pool.aside); err != nil {
			return fmt.Errorf("setting the pre-rebuild run pool aside: %w", err)
		}
	}
	if err := m.runs.ReplaceAll(p.poolAfter(), "migrate", model.Actor{Autonomous: true}); err != nil {
		return fmt.Errorf("run pool: %w", err)
	}
	return nil
}

// applyLedger writes the converted deliveries back through the ledger's OWN access point, one
// record at a time — there is exactly one writer for a delivery and the import does not open a
// second path to the same entity. The verbatim copy is written first: the store's write path reads
// the whole ledger and writes it back in the rebuilt shape, so from the first record on the status
// words are gone from the file and only the copy still holds them.
func (m *migrator) applyLedger(p *plan) error {
	if p.ledger.count() == 0 {
		return nil
	}
	if err := copyAside(p.ledger.path, p.ledger.aside); err != nil {
		return fmt.Errorf("setting the pre-rebuild delivery ledger aside: %w", err)
	}
	for _, d := range p.ledger.converted {
		if err := m.deliveries.Put(d); err != nil {
			return fmt.Errorf("delivery %s: %w", d.ID, err)
		}
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
