// Tests of the TAKEOVER: the import against a state directory that still holds the pre-rebuild
// stock — which is the only situation a cutover ever runs in.
//
// The fixtures below are DERIVED from the real pre-rebuild pools of a running instance: every
// record carries the same field set, the same value shapes and the same historical oddities as the
// source (measured field-by-field), with instance values replaced. `testdata/export.json` doubles
// as the pre-rebuild run pool because that is literally what it is on the instance — the export is
// a copy of `mercury/runs.json`, so pointing --input at it while the pool still holds the same
// records IS the cutover situation.
//
// What each test pins down was measured against the real state directory first, and every
// assertion below failed before the takeover existed:
//
//	run pool     63 records decoded into 63 runs with an EMPTY kind and an EMPTY title, every id
//	             read as "already imported" (protocol: "already present — runs 8"), nothing
//	             imported, and a single no-op fold rewrote the pool as 63 nameless records.
//	deliveries   15 records read as OPEN although the source recorded 1 merged and 1 closed.
//	snapshots    158 config snapshots each restoring 63 nameless runs into the pool.
//	orphans      3 pool files no rebuilt store opens, left in place looking live.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
	"devlab/backend/internal/statepath"
)

// legacyStateFixture holds the derived copies of the other pre-rebuild pools.
const legacyStateFixture = "testdata/legacy-state"

// installLegacyRunPool puts the pre-rebuild run pool in place — the export's own bytes, because on
// the instance the export IS a copy of the pool.
func installLegacyRunPool(t *testing.T, p *statepath.Paths) []byte {
	t.Helper()
	b, err := os.ReadFile(exportFixture)
	if err != nil {
		t.Fatalf("read export fixture: %v", err)
	}
	if err := os.MkdirAll(p.Mercury(), 0o700); err != nil {
		t.Fatalf("state dir: %v", err)
	}
	if err := os.WriteFile(p.Runs(), b, 0o600); err != nil {
		t.Fatalf("install legacy run pool: %v", err)
	}
	return b
}

// installLegacyState copies every other pre-rebuild pool of the fixture into the state root.
func installLegacyState(t *testing.T, p *statepath.Paths) {
	t.Helper()
	err := filepath.WalkDir(legacyStateFixture, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(legacyStateFixture, path)
		if rerr != nil {
			return rerr
		}
		dst := filepath.Join(p.Mercury(), rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o700)
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		return os.WriteFile(dst, b, 0o600)
	})
	if err != nil {
		t.Fatalf("install legacy state: %v", err)
	}
}

// fingerprint is a content hash over the whole state tree — the honest way to say "a second run
// changed nothing" instead of only checking the records the test happens to look at.
func fingerprint(t *testing.T, root string) string {
	t.Helper()
	h := sha256.New()
	var names []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		names = append(names, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Strings(names)
	for _, n := range names {
		b, err := os.ReadFile(n)
		if err != nil {
			t.Fatalf("read %s: %v", n, err)
		}
		rel, _ := filepath.Rel(root, n)
		fmt.Fprintf(h, "%s\x00%x\x00", rel, sha256.Sum256(b))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// runPoolRaw reads the pool as raw records, so a test can ask which FIELD NAMES it holds.
func runPoolRaw(t *testing.T, path string) []map[string]json.RawMessage {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	recs, err := poolRecords(b, "runs")
	if err != nil {
		t.Fatalf("%s is not a pool document: %v", path, err)
	}
	out := make([]map[string]json.RawMessage, 0, len(recs))
	for _, raw := range recs {
		out = append(out, recordFields(raw))
	}
	return out
}

// ── the measured blocker: the import against the REAL pre-rebuild pool ───────────────────────

// The situation of the cutover: the state directory still holds the pre-rebuild run pool, and the
// export names the same records. Before the takeover the import wrote NOTHING here — it recognised
// its own eight records by their ids in the old pool and reported them as already present — so the
// service came up showing the old records with no name at all.
func TestImportOverPreRebuildRunPoolCarriesTheStateOver(t *testing.T) {
	p := stateRoot(t)
	before := installLegacyRunPool(t, p)

	pl := migrate(t, p)

	// 1. The pre-rebuild records are NOT mistaken for already imported ones.
	if len(pl.presentRuns) != 0 {
		t.Errorf("pre-rebuild records counted as already imported: %v", pl.presentRuns)
	}
	if len(pl.autoRuns) != 2 || len(pl.openTodos) != 1 {
		t.Fatalf("nothing was imported: %d recurring runs, %d open tasks", len(pl.autoRuns), len(pl.openTodos))
	}

	// 2. The pool afterwards holds exactly the target state, and no record in the old form.
	got, err := runs.NewStore(p).List()
	if err != nil {
		t.Fatalf("run pool: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected the 2 recurring runs and the 1 open task, got %d", len(got))
	}
	for _, r := range got {
		if r.Kind == "" || strings.TrimSpace(r.Title) == "" {
			t.Errorf("record survived without kind or title: %+v", r)
		}
	}
	for i, obj := range runPoolRaw(t, p.Runs()) {
		for _, marker := range runLegacyMarkers {
			if _, bad := obj[marker]; bad {
				t.Errorf("record %d still carries the pre-rebuild field %q", i, marker)
			}
		}
	}

	// 3. The old stock is preserved verbatim, and the protocol says where.
	aside, err := os.ReadFile(p.Runs() + asideSuffix)
	if err != nil {
		t.Fatalf("the pre-rebuild pool was not set aside: %v", err)
	}
	if !bytes.Equal(aside, before) {
		t.Error("the set-aside pool is not a verbatim copy of what was there")
	}
	var buf bytes.Buffer
	writeProtocol(&buf, pl, false)
	for _, want := range []string{
		"pre-rebuild stock taken over",
		"8 records in the PRE-REBUILD form",
		p.Runs() + asideSuffix,
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("the protocol does not state %q:\n%s", want, buf.String())
		}
	}
}

// A second run over the taken-over directory changes NOTHING — not one byte anywhere in the state
// tree — and says so.
func TestSecondRunOverTheTakenOverDirectoryWritesNothing(t *testing.T) {
	p := stateRoot(t)
	installLegacyRunPool(t, p)
	installLegacyState(t, p)
	installArchive(t, p)
	migrate(t, p)

	was := fingerprint(t, p.Root)
	m := newMigrator(p, ownRepo, migrateNow)
	pl, err := m.plan(exportFixture)
	if err != nil {
		t.Fatalf("second plan: %v", err)
	}
	if !pl.empty() {
		t.Errorf("the second run still plans work: %+v", pl)
	}
	if err := m.apply(pl); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if now := fingerprint(t, p.Root); now != was {
		t.Error("the second run changed the state tree")
	}
	var buf bytes.Buffer
	writeProtocol(&buf, pl, false)
	if !strings.Contains(buf.String(), "nothing to do.") {
		t.Errorf("the second run does not report itself as a no-op:\n%s", buf.String())
	}
}

// The idempotence check tells the two SHAPES apart, not the ids: a record that really is in the
// rebuilt form counts as present and is left exactly as it is, while a pre-rebuild record with the
// very same id does not.
func TestIdempotenceDistinguishesTheFormsNotTheIDs(t *testing.T) {
	p := stateRoot(t)

	// One record already in the rebuilt form, carrying an id the export also names — plus the
	// whole pre-rebuild pool.
	kept := runs.Run{
		ID: "run_auto1", Kind: model.KindAuto, Title: "Reuse and shared building blocks",
		Active: false, PromptSnapshot: "already composed", AxiomIDs: []string{"ax_reuse"},
		Schedule:   &runs.ScheduleSpec{Kind: runs.Weekly, TimeOfDay: "07:00"},
		Authorship: model.Authorship{Created: model.Actor{User: "operator"}, CreatedAt: migrateNow},
	}
	mixed := mixedPool(t, kept)
	if err := os.MkdirAll(p.Mercury(), 0o700); err != nil {
		t.Fatalf("state dir: %v", err)
	}
	if err := os.WriteFile(p.Runs(), mixed, 0o600); err != nil {
		t.Fatalf("write mixed pool: %v", err)
	}

	pl := migrate(t, p)

	if len(pl.presentRuns) != 1 || !strings.Contains(pl.presentRuns[0], "run_auto1") {
		t.Errorf("the record in the rebuilt form must be the ONE counted as present, got %v", pl.presentRuns)
	}
	got, err := runs.NewStore(p).List()
	if err != nil {
		t.Fatalf("run pool: %v", err)
	}
	byID := map[string][]runs.Run{}
	for _, r := range got {
		byID[r.ID] = append(byID[r.ID], r)
	}
	if len(byID["run_auto1"]) != 1 {
		t.Fatalf("run_auto1 must appear exactly once, got %d", len(byID["run_auto1"]))
	}
	if got := byID["run_auto1"][0]; got.PromptSnapshot != "already composed" || got.Authorship.Created.User != "operator" {
		t.Errorf("the existing record was overwritten instead of kept: %+v", got)
	}
	if len(byID["run_auto2"]) != 1 || byID["run_auto2"][0].Title == "" {
		t.Errorf("the pre-rebuild record with the same id shape was not imported: %+v", byID["run_auto2"])
	}
	if len(got) != 3 {
		t.Errorf("expected the kept record plus the 2 imported ones, got %d", len(got))
	}
}

// mixedPool builds a run pool holding one record in the rebuilt form plus every record of the
// pre-rebuild fixture, in the fixture's own shape.
func mixedPool(t *testing.T, keep runs.Run) []byte {
	t.Helper()
	b, err := os.ReadFile(exportFixture)
	if err != nil {
		t.Fatalf("read export fixture: %v", err)
	}
	legacy, err := poolRecords(b, "runs")
	if err != nil {
		t.Fatalf("export fixture: %v", err)
	}
	fresh, err := json.Marshal(keep)
	if err != nil {
		t.Fatalf("marshal kept run: %v", err)
	}
	out := append([]json.RawMessage{fresh}, legacy...)
	doc, err := json.Marshal(map[string]any{"runs": out})
	if err != nil {
		t.Fatalf("marshal mixed pool: %v", err)
	}
	return doc
}

// A record in NEITHER shape is never interpreted: it is set aside with the rest of the stock, named
// with its find location, and is gone from the pool afterwards — not dropped in silence.
func TestUndecidableRecordsAreSetAsideNamedNotInterpreted(t *testing.T) {
	p := stateRoot(t)
	if err := os.MkdirAll(p.Mercury(), 0o700); err != nil {
		t.Fatalf("state dir: %v", err)
	}
	// One record carrying markers of BOTH shapes, one carrying markers of neither.
	pool := []byte(`{"runs":[
	  {"id":"run_mixed","name":"old name","title":"new title","kind":"auto"},
	  {"id":"run_bare","schedule":{"kind":"daily","timeOfDay":"06:00"}},
	  {"id":"run_auto1","name":"Reuse and shared building blocks","type":"","enabled":false,
	   "schedule":{"kind":"weekly","timeOfDay":"07:00","weekdays":[1]},"prompt":"old",
	   "createdAt":"2026-06-01T05:00:00Z","updatedAt":"2026-06-01T05:00:00Z"}
	]}`)
	if err := os.WriteFile(p.Runs(), pool, 0o600); err != nil {
		t.Fatalf("write pool: %v", err)
	}

	pl := migrate(t, p)

	if want := []string{"run_bare", "run_mixed"}; !equalSorted(pl.pool.undecidable, want) {
		t.Errorf("undecidable records not named: got %v, want %v", pl.pool.undecidable, want)
	}
	if len(pl.pool.legacy) != 1 || pl.pool.legacy[0] != "run_auto1" {
		t.Errorf("the pre-rebuild record was not recognised: %v", pl.pool.legacy)
	}
	if len(pl.pool.newForm) != 0 {
		t.Errorf("an undecidable record must never be read as rebuilt: %+v", pl.pool.newForm)
	}

	aside, err := os.ReadFile(p.Runs() + asideSuffix)
	if err != nil {
		t.Fatalf("the stock was not set aside: %v", err)
	}
	if !bytes.Equal(aside, pool) {
		t.Error("the set-aside stock is not verbatim")
	}
	for _, obj := range runPoolRaw(t, p.Runs()) {
		var id string
		_ = json.Unmarshal(obj["id"], &id)
		if id == "run_mixed" || id == "run_bare" {
			t.Errorf("the undecidable record %s stayed in the pool", id)
		}
	}
	var buf bytes.Buffer
	writeProtocol(&buf, pl, false)
	for _, want := range []string{"? run_bare", "? run_mixed", "never interpreted", p.Runs() + asideSuffix} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("the protocol does not name %q:\n%s", want, buf.String())
		}
	}
}

func equalSorted(got, want []string) bool {
	g := append([]string{}, got...)
	sort.Strings(g)
	if len(g) != len(want) {
		return false
	}
	for i := range g {
		if g[i] != want[i] {
			return false
		}
	}
	return true
}

// A run pool that is not readable AT ALL aborts the whole import: nothing is written, nothing is
// moved, and the operator's file is left exactly as it was for a human to look at.
func TestUnreadableRunPoolAborts(t *testing.T) {
	p := stateRoot(t)
	if err := os.MkdirAll(p.Mercury(), 0o700); err != nil {
		t.Fatalf("state dir: %v", err)
	}
	broken := []byte(`{"runs": [ this was never JSON`)
	if err := os.WriteFile(p.Runs(), broken, 0o600); err != nil {
		t.Fatalf("write pool: %v", err)
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"--input", exportFixture}, &out, &errOut); code != exitConfigState {
		t.Fatalf("exit code = %d, want %d (config state)", code, exitConfigState)
	}
	if !strings.Contains(errOut.String(), "run pool unreadable") {
		t.Errorf("the abort does not name the pool:\n%s", errOut.String())
	}
	after, err := os.ReadFile(p.Runs())
	if err != nil || !bytes.Equal(after, broken) {
		t.Errorf("the unreadable pool was touched: %v", err)
	}
	if _, err := os.Stat(p.Runs() + asideSuffix); err == nil {
		t.Error("an unreadable pool must not be set aside behind the operator's back")
	}
}

// A repeated takeover never overwrites an earlier copy of the stock.
func TestSetAsideNeverOverwritesAnEarlierCopy(t *testing.T) {
	p := stateRoot(t)
	before := installLegacyRunPool(t, p)
	earlier := []byte(`{"runs":[{"id":"run_from_an_earlier_attempt","name":"x","type":"","enabled":false}]}`)
	if err := os.WriteFile(p.Runs()+asideSuffix, earlier, 0o600); err != nil {
		t.Fatalf("write earlier copy: %v", err)
	}

	migrate(t, p)

	kept, err := os.ReadFile(p.Runs() + asideSuffix)
	if err != nil || !bytes.Equal(kept, earlier) {
		t.Errorf("the earlier copy was overwritten: %v", err)
	}
	second, err := os.ReadFile(p.Runs() + asideSuffix + ".2")
	if err != nil {
		t.Fatalf("no second set-aside name was used: %v", err)
	}
	if !bytes.Equal(second, before) {
		t.Error("the stock was not set aside verbatim under the free name")
	}
}

// ── the delivery ledger ──────────────────────────────────────────────────────────────────────

// The pre-rebuild ledger recorded a status WORD; the rebuilt record expresses merged and closed as
// times. Read unconverted, EVERY record therefore reads as open — and an open delivery is not
// cosmetic: the next pull request stacks on the newest open one of the repository and the preflight
// reports it as an outstanding arrival.
func TestLegacyDeliveryLedgerReadsEveryRecordAsOpenUntilItIsConverted(t *testing.T) {
	p := stateRoot(t)
	installLegacyState(t, p)

	before, err := runs.NewDeliveryStore(p).All()
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if len(before) != 3 {
		t.Fatalf("fixture holds %d deliveries, want 3", len(before))
	}
	open := 0
	for _, d := range before {
		if d.OpenState() {
			open++
		}
	}
	if open != 3 {
		t.Fatalf("the defect this converts is that all %d read as open; got %d", len(before), open)
	}

	installLegacyRunPool(t, p)
	pl := migrate(t, p)

	after, err := runs.NewDeliveryStore(p).All()
	if err != nil {
		t.Fatalf("ledger after: %v", err)
	}
	byID := map[string]runs.Delivery{}
	for _, d := range after {
		byID[d.ID] = d
	}
	if len(after) != 3 {
		t.Fatalf("the conversion changed the record count: %d", len(after))
	}
	merged, ok := byID["dlv_merged1"]
	if !ok || merged.MergedAt == nil || merged.ClosedAt != nil {
		t.Errorf("the merged delivery was not carried over: %+v", merged)
	}
	closed, ok := byID["dlv_closed1"]
	if !ok || closed.ClosedAt == nil || closed.MergedAt != nil {
		t.Errorf("the closed delivery was not carried over: %+v", closed)
	}
	if closed.ClosedReason == "" {
		t.Error("a closed delivery must state why it carries no reason of its own")
	}
	if o := byID["dlv_open1"]; !o.OpenState() {
		t.Errorf("an open delivery must stay open: %+v", o)
	}
	// The outcome TIME is the creation time — the source carried no second timestamp — and that
	// is stated, not smuggled in.
	if !merged.MergedAt.Equal(merged.CreatedAt) {
		t.Errorf("the merge time must be the recorded creation time, got %v", merged.MergedAt)
	}
	// The execution is carried under its rebuilt name.
	for _, d := range after {
		if d.ExecutionID == "" {
			t.Errorf("delivery %s lost its execution reference", d.ID)
		}
		if !d.OpenState() && d.ExecutionID == "" {
			t.Errorf("delivery %s: no execution behind a closed outcome", d.ID)
		}
	}
	// The verbatim copy exists, and the protocol names it.
	if _, err := os.Stat(p.Deliveries() + asideSuffix); err != nil {
		t.Errorf("the pre-rebuild ledger was not set aside: %v", err)
	}
	var buf bytes.Buffer
	writeProtocol(&buf, pl, false)
	for _, want := range []string{"delivery ledger", "1 merged · 1 closed · 1 open", p.Deliveries() + asideSuffix} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("the protocol does not state %q:\n%s", want, buf.String())
		}
	}
}

// An unknown status word is refused BY NAME: the rebuilt ledger knows three outcomes and a
// delivery must not be guessed into one of them. Nothing is written at all.
func TestUnknownDeliveryStatusIsRefusedByName(t *testing.T) {
	p := stateRoot(t)
	installLegacyRunPool(t, p)
	if err := os.WriteFile(p.Deliveries(), []byte(`{"deliveries":[
	  {"id":"dlv_x","repo":"acme/alpha","branch":"b","fromCommit":"a","toCommit":"b",
	   "createdAt":"2026-07-27T11:30:17Z","status":"halfway","resultId":"r1","runId":"run_auto1",
	   "runName":"n","devBranch":"d","baseBranch":"main","prNumber":1,"prUrl":"u"}
	]}`), 0o600); err != nil {
		t.Fatalf("write ledger: %v", err)
	}
	poolWas, err := os.ReadFile(p.Runs())
	if err != nil {
		t.Fatalf("read pool: %v", err)
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"--input", exportFixture}, &out, &errOut); code != exitConfigState {
		t.Fatalf("exit code = %d, want %d", code, exitConfigState)
	}
	if !strings.Contains(out.String(), `unknown status "halfway"`) {
		t.Errorf("the refusal does not name the status:\n%s", out.String())
	}
	// A refusal aborts BEFORE the first write: the pool is untouched and nothing was set aside.
	if now, err := os.ReadFile(p.Runs()); err != nil || !bytes.Equal(now, poolWas) {
		t.Errorf("a refusal must abort before the first write, the pool changed: %v", err)
	}
	for _, path := range []string{p.Runs() + asideSuffix, p.Deliveries() + asideSuffix} {
		if _, err := os.Stat(path); err == nil {
			t.Errorf("a refusal must set nothing aside, found %s", path)
		}
	}
}

// ── the run-config snapshot history ──────────────────────────────────────────────────────────

// A snapshot is a FULL run configuration and "restore" writes it back verbatim, so a snapshot
// holding pre-rebuild records undoes the whole import with one click. Those are set aside; a
// snapshot in the rebuilt form stays restorable, and so does one that holds no runs at all.
func TestPreRebuildConfigSnapshotsAreSetAsideAndRebuiltOnesStay(t *testing.T) {
	p := stateRoot(t)
	installLegacyRunPool(t, p)
	installLegacyState(t, p) // brings the derived pre-rebuild snapshot
	fresh := runs.Snapshot{TS: "2026-07-29T10:00:00Z", Action: "update", Actor: "operator", Runs: []runs.Run{{
		ID: "run_kept", Kind: model.KindTodo, Title: "kept", Task: "t",
	}}}
	writeSnapshot(t, p, "2026-07-29T10-00-00Z.json", fresh)
	writeSnapshot(t, p, "2026-07-29T11-00-00Z.json", runs.Snapshot{
		TS: "2026-07-29T11:00:00Z", Action: "delete", Actor: "operator", Runs: []runs.Run{},
	})
	if err := os.WriteFile(filepath.Join(p.HistoryDir(), "torn.json"), []byte(`{"runs":[`), 0o600); err != nil {
		t.Fatalf("write torn snapshot: %v", err)
	}

	pl := migrate(t, p)

	if want := []string{"2026-07-28T11-18-34.723569338Z.json", "torn.json"}; !equalSorted(pl.snaps.moved, want) {
		t.Errorf("moved snapshots = %v, want %v", pl.snaps.moved, want)
	}
	for _, name := range pl.snaps.moved {
		if _, err := os.Stat(filepath.Join(p.HistoryDir()+asideSuffix, name)); err != nil {
			t.Errorf("snapshot %s was not kept at its new place: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(p.HistoryDir(), name)); err == nil {
			t.Errorf("snapshot %s is still offered as a restore point", name)
		}
	}
	// What stays: the rebuilt snapshot, the empty one, and the import's own.
	metas, err := runs.NewStore(p).History().List()
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	actions := map[string]int{}
	for _, m := range metas {
		actions[m.Action]++
	}
	if actions["update"] != 1 || actions["delete"] != 1 || actions["migrate"] != 1 || len(metas) != 3 {
		t.Errorf("history after the takeover = %v (%d entries), want update+delete+migrate", actions, len(metas))
	}
	// The restore point the import leaves behind restores the target state, not a nameless one.
	var mig runs.SnapshotMeta
	for _, m := range metas {
		if m.Action == "migrate" {
			mig = m
		}
	}
	snap, ok, err := runs.NewStore(p).History().Get(mig.TS)
	if err != nil || !ok {
		t.Fatalf("the import's own snapshot is not readable: %v", err)
	}
	for _, r := range snap.Runs {
		if r.Kind == "" || r.Title == "" {
			t.Errorf("the import's restore point holds a nameless run: %+v", r)
		}
	}
	var buf bytes.Buffer
	writeProtocol(&buf, pl, false)
	for _, want := range []string{"config snapshots", p.HistoryDir() + asideSuffix, "2 of 4"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("the protocol does not state %q:\n%s", want, buf.String())
		}
	}
}

func writeSnapshot(t *testing.T, p *statepath.Paths, name string, s runs.Snapshot) {
	t.Helper()
	if err := os.MkdirAll(p.HistoryDir(), 0o700); err != nil {
		t.Fatalf("history dir: %v", err)
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(p.HistoryDir(), name), b, 0o600); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
}

// ── the pools that need NO conversion, proven against the derived fixtures ───────────────────

// The pending-PR pool is read as it lies: every field of the rebuilt record is filled from the
// pre-rebuild file, including the blockade. The import therefore does not touch it — and this test
// is what says so, instead of the silence of a pool nobody checked.
func TestLegacyPendingPRPoolNeedsNoConversion(t *testing.T) {
	p := stateRoot(t)
	installLegacyState(t, p)
	installLegacyRunPool(t, p)
	was, err := os.ReadFile(p.PRs())
	if err != nil {
		t.Fatalf("read pr pool: %v", err)
	}

	got, err := runs.NewPRStore(p).List()
	if err != nil {
		t.Fatalf("pr pool: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected the 2 fixture records, got %d", len(got))
	}
	byRepo := map[string]runs.PendingPR{}
	for _, pr := range got {
		byRepo[pr.Repo] = pr
	}
	plain := byRepo["acme/alpha"]
	if plain.Number == 0 || plain.URL == "" || plain.RunID == "" ||
		plain.CreatedAt.IsZero() || plain.MergeBy.IsZero() || plain.LastChecked.IsZero() {
		t.Errorf("a tracked PR lost fields the source carried: %+v", plain)
	}
	blocked := byRepo["acme/beta"]
	if !blocked.Blocked || blocked.BlockedReason == "" || blocked.BlockedAt.IsZero() {
		t.Errorf("the blockade was not carried: %+v", blocked)
	}
	// The ONE field with no counterpart: the pre-rebuild deploy-attempt counter. The rebuilt
	// record keeps retry state in `backoff`, and the counter only ever accompanied a record that
	// is ALREADY blocked — its attempts are spent, so nothing is lost by not carrying it. Pinned
	// here so the drop stays a decision.
	if blocked.Backoff != nil {
		t.Errorf("no backoff may be invented for a blocked record: %+v", blocked.Backoff)
	}
	if !strings.Contains(string(was), `"deployAttempts"`) {
		t.Fatal("the fixture no longer carries the field this decision is about")
	}

	migrate(t, p)
	now, err := os.ReadFile(p.PRs())
	if err != nil || !bytes.Equal(now, was) {
		t.Errorf("the import must not touch a pool it can read: %v", err)
	}
}

// The notice pool is read as it lies, and the import's own protocol items are ADDED beside the
// pre-rebuild rows instead of replacing them.
func TestLegacyNoticePoolNeedsNoConversion(t *testing.T) {
	p := stateRoot(t)
	installLegacyState(t, p)

	before, err := runs.NewNoticeStore(p).List()
	if err != nil {
		t.Fatalf("notice pool: %v", err)
	}
	if len(before) != 2 {
		t.Fatalf("expected the 2 fixture rows, got %d", len(before))
	}
	for _, n := range before {
		if n.Kind != runs.NoticeAssigned || n.RunID == "" || n.RunName == "" ||
			len(n.AxiomIDs) != 1 || len(n.Axioms) != 1 {
			t.Errorf("an assignment row lost its facts: %+v", n)
		}
		// The bundling fields postdate these rows and are filled on read, never left at zero.
		if n.Count < 1 || n.FirstAt.IsZero() || n.LastAt.IsZero() {
			t.Errorf("a pre-rebuild row was not normalised: %+v", n)
		}
	}

	installLegacyRunPool(t, p)
	migrate(t, p)

	after, err := runs.NewNoticeStore(p).List()
	if err != nil {
		t.Fatalf("notice pool after: %v", err)
	}
	if len(after) != len(before)+len(protocolItems()) {
		t.Fatalf("notices after = %d, want %d pre-rebuild + %d protocol items",
			len(after), len(before), len(protocolItems()))
	}
	kept := 0
	for _, n := range after {
		if n.ID == "ntc_legacy1" || n.ID == "ntc_legacy2" {
			kept++
		}
	}
	if kept != 2 {
		t.Errorf("the pre-rebuild notices were not kept, found %d", kept)
	}
}

// The legacy execution archive is read as it lies, with every trait the real files carry: the
// auxiliary fields the rebuilt document has no place for, a step recorded as `ok` before statuses
// existed, a historical stage name in the instance's own language, the separate live block, and a
// zero finishing time.
func TestLegacyArchiveRecordWithTheRealWorldTraitsIsRead(t *testing.T) {
	p := stateRoot(t)
	installLegacyRunPool(t, p)
	installArchive(t, p)
	migrate(t, p)

	res, ok, err := runs.NewResultStore(p).Get("2026-07-28T03-44-37.698325606Z")
	if err != nil || !ok {
		t.Fatalf("the archived execution was not imported: ok=%v err=%v", ok, err)
	}
	if res.RunID != "run_legacy_aux" || res.RunTitle != "Interrupted nightly sweep" {
		t.Errorf("identity not carried: %+v", res)
	}
	if res.Kind != model.KindTodo {
		t.Errorf("kind = %q, want todo (the source's type)", res.Kind)
	}
	if !res.Legacy {
		t.Error("an archived execution must be marked as archive provenance")
	}
	// A zero finishing time means "never finished" — and that is what it says, rather than a
	// fabricated end.
	if res.EndedAt != nil {
		t.Errorf("a zero finishing time must not become an end: %v", res.EndedAt)
	}
	// repos + live, each with its stages VERBATIM.
	stages := map[string]model.StepState{}
	repos := []string{}
	for _, rp := range res.Repos {
		repos = append(repos, rp.Repo)
		for _, st := range rp.Stages {
			stages[string(st.Stage)] = st.State
		}
	}
	if !equalSorted(repos, []string{"alpha", "beta", "gamma"}) {
		t.Errorf("repos = %v, want the two recorded ones plus the live one", repos)
	}
	if got := stages["übersprungen"]; got != model.StepExecuted {
		t.Errorf("the historical stage name must be carried verbatim as executed, got %q", got)
	}
	if got := stages["implement"]; got == "" {
		t.Error("a step recorded as ok before statuses existed was dropped")
	}
	if got := stages["fold"]; got != model.StepNotApplicable {
		t.Errorf("fold = %q, want not-applicable", got)
	}
}

// ── the pools the rebuild has no reader for ──────────────────────────────────────────────────

// A pre-rebuild pool no rebuilt store ever opens is moved out of the way with its reason, so the
// operator is not left with data that looks live and is not. Nothing is deleted and nothing is
// changed.
func TestPoolsWithoutAReaderAreSetAsideWithTheirReason(t *testing.T) {
	p := stateRoot(t)
	installLegacyRunPool(t, p)
	installLegacyState(t, p)

	// The names are spelled out here and NOT read out of orphanPools(): a test that iterates the
	// very list it is meant to pin passes just as happily when the list is emptied.
	want := map[string][]byte{}
	for _, name := range []string{"runs-settings.json", "runs-incidents.json"} {
		b, err := os.ReadFile(filepath.Join(p.Mercury(), name))
		if err != nil {
			t.Fatalf("fixture %s: %v", name, err)
		}
		want[name] = b
	}

	pl := migrate(t, p)

	if len(pl.orphans) != len(want) {
		t.Fatalf("orphans planned = %d, want %d", len(pl.orphans), len(want))
	}
	var buf bytes.Buffer
	writeProtocol(&buf, pl, false)
	for name, content := range want {
		from := filepath.Join(p.Mercury(), name)
		if _, err := os.Stat(from); err == nil {
			t.Errorf("%s is still in place, looking live", name)
		}
		got, err := os.ReadFile(from + asideSuffix)
		if err != nil {
			t.Fatalf("%s was not kept: %v", name, err)
		}
		if !bytes.Equal(got, content) {
			t.Errorf("%s was changed while being set aside", name)
		}
		if !strings.Contains(buf.String(), from+asideSuffix) {
			t.Errorf("the protocol does not say where %s went:\n%s", name, buf.String())
		}
	}
	// Each one must state WHY it has no reader, in the terms the rebuild replaced it with.
	for name, why := range map[string]string{
		"runs-settings.json":  "settings.json",
		"runs-incidents.json": "runs-notices.json",
	} {
		if !strings.Contains(buf.String(), why) {
			t.Errorf("the protocol does not say WHY %s has no reader (expected %q):\n%s", name, why, buf.String())
		}
	}
}

// ── the classifier itself ────────────────────────────────────────────────────────────────────

// The form is decided by markers only one shape has, and "both" or "neither" is never resolved by
// preference. This is the rule the whole takeover rests on, so it is stated on its own.
func TestFormOfDecidesByMarkersAndRefusesToGuess(t *testing.T) {
	cases := []struct {
		name       string
		obj        string
		blankIsNew bool
		want       recordForm
	}{
		{"pre-rebuild run", `{"id":"r","name":"n","type":"todo","enabled":true}`, false, formLegacy},
		{"rebuilt run", `{"id":"r","kind":"todo","title":"t","authorship":{}}`, false, formNew},
		{"rebuilt run switched off", `{"id":"r","kind":"auto","title":"t","tuning":{},"authorship":{}}`, false, formNew},
		{"both shapes at once", `{"id":"r","name":"n","title":"t"}`, false, formUndecidable},
		{"no marker, run pool", `{"id":"r","schedule":{}}`, false, formUndecidable},
		{"no marker, delivery ledger", `{"id":"d","repo":"acme/alpha","branch":"b"}`, true, formNew},
		{"pre-rebuild delivery", `{"id":"d","status":"merged","resultId":"r"}`, true, formLegacy},
		{"rebuilt delivery", `{"id":"d","mergedAt":"2026-07-27T11:30:17Z"}`, true, formNew},
		{"delivery in both shapes", `{"id":"d","status":"open","mergedAt":"2026-07-27T11:30:17Z"}`, true, formUndecidable},
	}
	for _, c := range cases {
		legacyMarkers, newMarkers := runLegacyMarkers, runNewMarkers
		if c.blankIsNew {
			legacyMarkers, newMarkers = deliveryLegacyMarkers, deliveryNewMarkers
		}
		obj := recordFields(json.RawMessage(c.obj))
		if obj == nil {
			t.Fatalf("%s: fixture is not an object", c.name)
		}
		if got := formOf(obj, legacyMarkers, newMarkers, c.blankIsNew); got != c.want {
			t.Errorf("%s: form = %v, want %v", c.name, got, c.want)
		}
	}
}
