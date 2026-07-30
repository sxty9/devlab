// The TAKEOVER of the pre-rebuild stock. The import does not sit beside the old data — it carries
// the state over: what the rebuilt code can read stays, what it cannot read is either converted
// faithfully or set aside verbatim, and nothing in a shape the daemon misreads is left in a pool.
//
// The one idea this file exists for is FORM. Before the rebuild, a run record was
// {type,name,enabled,prompt,promptAt,lastFiredAt,lastResult,done,…}; afterwards it is
// {kind,title,active,promptSnapshot,authorship,…}. Both shapes carry the same `id`. An idempotence
// check that asks "do I already know this id?" therefore answers YES for every pre-rebuild record
// and imports nothing, while the pool keeps 63 records the daemon decodes into blank runs. So the
// question asked here is never "which id is it?" but "which SHAPE is it in?" — a record is counted
// as already imported only when it is in the rebuilt form.
//
// Three outcomes, and each one is named in the protocol:
//
//   - the rebuilt form → kept, and it is what makes a second run a no-op;
//   - the pre-rebuild form → set aside verbatim, then gone from the pool;
//   - neither, or both at once → UNDECIDABLE, set aside with its find location rather than
//     interpreted by guess or dropped in silence.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"devlab/backend/internal/fsatomic"
	"devlab/backend/internal/runs"
)

// asideSuffix names every artifact this file sets aside. It is a suffix, not a directory, so the
// copy lands next to the original where the operator is already looking, and nothing the rebuilt
// code reads matches it.
const asideSuffix = ".pre-migration"

// recordForm is the shape one persisted record is in.
type recordForm int

const (
	// formNew is the rebuilt shape — readable by the daemon, and the only form that counts as
	// already imported.
	formNew recordForm = iota
	// formLegacy is the pre-rebuild shape.
	formLegacy
	// formUndecidable is everything else: a record carrying markers of BOTH shapes, or none at
	// all. It is never interpreted.
	formUndecidable
)

// Markers per pool: field names that exist in exactly ONE of the two shapes. A field both shapes
// carry (id, schedule, task, targets, repo/branch/commits) says nothing about the form and is
// deliberately absent from these lists.
var (
	// The pre-rebuild run record. `prompt`/`promptHash` became promptSnapshot/promptInputHash,
	// `createdAt`/`updatedAt` moved into the nested authorship, and every execution fact
	// (done, lastFiredAt, lastResult, nextFireAt) left the definition entirely (B-20).
	runLegacyMarkers = []string{
		"type", "name", "enabled", "done", "prompt", "promptAt", "promptHash",
		"lastFiredAt", "lastResult", "nextFireAt", "createdAt", "updatedAt",
	}
	// The rebuilt run record. kind, title and authorship carry no omitempty, so a record the
	// rebuilt store wrote always shows at least these three.
	runNewMarkers = []string{"kind", "title", "authorship", "active", "promptSnapshot", "promptInputHash"}

	// The pre-rebuild delivery record. It carried a STATUS WORD plus the run's identity and the
	// two branch names the rebuild derives instead of storing.
	deliveryLegacyMarkers = []string{"status", "resultId", "runId", "runName", "devBranch", "baseBranch"}
	// The rebuilt delivery record. Every one of these is omitempty, so their ABSENCE proves
	// nothing — see formOf's blankIsNew.
	deliveryNewMarkers = []string{"mergedAt", "closedAt", "closedReason", "executionId", "reversalOf"}
)

// formOf classifies one raw record by the markers it carries.
//
// blankIsNew settles the case of a record with no marker at all, and it differs per pool for a
// concrete reason: the rebuilt RUN record always writes kind, title and authorship, so a run
// record without any marker is not one the rebuilt store wrote and must not be assumed readable.
// Every field of the rebuilt DELIVERY record beyond the shared ones is omitempty, so an ordinary
// open delivery legitimately carries no marker — there, "no legacy marker" is the whole test.
func formOf(obj map[string]json.RawMessage, legacyMarkers, newMarkers []string, blankIsNew bool) recordForm {
	legacy, fresh := countMarkers(obj, legacyMarkers), countMarkers(obj, newMarkers)
	switch {
	case legacy > 0 && fresh == 0:
		return formLegacy
	case fresh > 0 && legacy == 0:
		return formNew
	case legacy == 0 && fresh == 0 && blankIsNew:
		return formNew
	default:
		return formUndecidable
	}
}

func countMarkers(obj map[string]json.RawMessage, markers []string) int {
	n := 0
	for _, m := range markers {
		if _, ok := obj[m]; ok {
			n++
		}
	}
	return n
}

// poolRecords splits one pool document {"<key>":[…]} into its records, still raw. A document that
// is not of that shape is an ERROR, never a silent empty pool: the caller aborts the whole import
// on it, so an unreadable pool file is left exactly as it was for a human to look at.
func poolRecords(b []byte, key string) ([]json.RawMessage, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	body, ok := doc[key]
	if !ok {
		return nil, nil // a document without the array is an empty pool
	}
	var recs []json.RawMessage
	if err := json.Unmarshal(body, &recs); err != nil {
		return nil, err
	}
	return recs, nil
}

// recordFields parses one raw record into its field names. A record that is not a JSON object is
// undecidable by construction, which is what the nil return says.
func recordFields(raw json.RawMessage) map[string]json.RawMessage {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	return obj
}

// recordLabel names a record for the protocol: its id when it has one, its position otherwise.
func recordLabel(obj map[string]json.RawMessage, i int) string {
	var id string
	if raw, ok := obj["id"]; ok {
		_ = json.Unmarshal(raw, &id)
	}
	if strings.TrimSpace(id) == "" {
		return fmt.Sprintf("#%d", i+1)
	}
	return id
}

// ── the run pool ─────────────────────────────────────────────────────────────────────────────

// runPool is the run pool as it lies on disk, split by form. newForm is the ONLY part the import
// treats as already present; legacy and undecidable are what the takeover removes from the pool.
type runPool struct {
	path        string
	newForm     []runs.Run
	legacy      []string
	undecidable []string
	// aside is where the verbatim copy goes; "" while there is nothing to set aside.
	aside string
}

// takenOver reports whether the pool holds records the daemon would misread.
func (rp *runPool) takenOver() bool { return len(rp.legacy)+len(rp.undecidable) > 0 }

// readRunPool reads and classifies the pool. A missing pool is an empty one; an unreadable pool is
// an error (nothing is written, nothing is moved).
func readRunPool(path string) (*runPool, error) {
	rp := &runPool{path: path}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return rp, nil
		}
		return nil, err
	}
	recs, err := poolRecords(b, "runs")
	if err != nil {
		return nil, fmt.Errorf("%s is not a readable run pool: %w", path, err)
	}
	for i, raw := range recs {
		obj := recordFields(raw)
		if obj == nil {
			rp.undecidable = append(rp.undecidable, fmt.Sprintf("#%d", i+1))
			continue
		}
		label := recordLabel(obj, i)
		switch formOf(obj, runLegacyMarkers, runNewMarkers, false) {
		case formLegacy:
			rp.legacy = append(rp.legacy, label)
		case formUndecidable:
			rp.undecidable = append(rp.undecidable, label)
		default:
			var r runs.Run
			if err := json.Unmarshal(raw, &r); err != nil {
				// It looks rebuilt but does not decode: that is exactly the case that must
				// not be guessed at.
				rp.undecidable = append(rp.undecidable, label)
				continue
			}
			rp.newForm = append(rp.newForm, r)
		}
	}
	if rp.takenOver() {
		rp.aside, err = freeAsidePath(path)
		if err != nil {
			return nil, err
		}
	}
	return rp, nil
}

// ── the delivery ledger ──────────────────────────────────────────────────────────────────────

// legacyDelivery is the part of the pre-rebuild delivery record the rebuilt shape has no field
// for: a status WORD instead of the two timestamps, and the execution under its old name.
type legacyDelivery struct {
	Status   string `json:"status"`
	ResultID string `json:"resultId"`
}

// The three status words the pre-rebuild ledger used. Anything else is refused by name.
const (
	legacyDeliveryOpen   = "open"
	legacyDeliveryMerged = "merged"
	legacyDeliveryClosed = "closed"
)

// legacyClosedReason states WHY a converted delivery carries no closing reason of its own. The
// field is the one place the contract designates for the closed state, so the provenance goes
// there rather than into a comment nobody reads at runtime.
const legacyClosedReason = "closed before the rebuild — the pre-rebuild ledger recorded a status " +
	"word without a time and without a reason"

// ledgerTakeover is the delivery ledger's conversion: the pre-rebuild records mapped onto the
// rebuilt shape, ready to be written back through the ledger's own access point.
type ledgerTakeover struct {
	path      string
	aside     string
	converted []runs.Delivery
	// counts per source status, for the protocol.
	merged, closed, open int
	undecidable          []string
	refusals             []string
}

// count is how many records the conversion writes.
func (lt *ledgerTakeover) count() int { return len(lt.converted) }

// readLedgerTakeover classifies the ledger and maps every pre-rebuild record.
//
// Why a conversion is needed at all: the rebuilt record expresses "merged" as a MergedAt time and
// "closed" as a ClosedAt time, and both are omitempty. A pre-rebuild record therefore decodes into
// a delivery that is neither merged nor closed — that is, OPEN — whatever its status word said.
// Open deliveries are not cosmetic: the next pull request stacks on the newest open delivery of
// the repository and the preflight reports them as outstanding arrivals, so a merged delivery read
// as open makes the chain stack onto a branch that is already gone.
//
// What the source cannot give is the WHEN: it recorded one status word and no second timestamp.
// The conversion therefore dates the outcome at the delivery's own creation time and says so — in
// the protocol for every record, and in the closing reason for a closed one. Claiming the state is
// faithful (the source recorded it); claiming a precise outcome time would not be.
func readLedgerTakeover(path string) (*ledgerTakeover, error) {
	lt := &ledgerTakeover{path: path}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return lt, nil
		}
		return nil, err
	}
	recs, err := poolRecords(b, "deliveries")
	if err != nil {
		return nil, fmt.Errorf("%s is not a readable delivery ledger: %w", path, err)
	}
	for i, raw := range recs {
		obj := recordFields(raw)
		if obj == nil {
			lt.undecidable = append(lt.undecidable, fmt.Sprintf("#%d", i+1))
			continue
		}
		label := recordLabel(obj, i)
		switch formOf(obj, deliveryLegacyMarkers, deliveryNewMarkers, true) {
		case formNew:
			continue // already in the rebuilt form — nothing to convert
		case formUndecidable:
			lt.undecidable = append(lt.undecidable, label)
			continue
		}
		var d runs.Delivery
		var l legacyDelivery
		if err := json.Unmarshal(raw, &d); err != nil {
			lt.undecidable = append(lt.undecidable, label)
			continue
		}
		if err := json.Unmarshal(raw, &l); err != nil {
			lt.undecidable = append(lt.undecidable, label)
			continue
		}
		if d.ExecutionID == "" {
			d.ExecutionID = l.ResultID
		}
		at := d.CreatedAt
		switch strings.TrimSpace(l.Status) {
		case legacyDeliveryOpen, "":
			lt.open++
		case legacyDeliveryMerged:
			d.MergedAt = &at
			lt.merged++
		case legacyDeliveryClosed:
			d.ClosedAt = &at
			if d.ClosedReason == "" {
				d.ClosedReason = legacyClosedReason
			}
			lt.closed++
		default:
			lt.refusals = append(lt.refusals, fmt.Sprintf(
				"delivery %s carries the unknown status %q — the rebuilt ledger knows merged, "+
					"closed and open, and a delivery must not be guessed into one of them", label, l.Status))
			continue
		}
		lt.converted = append(lt.converted, d)
	}
	if lt.count() > 0 || len(lt.undecidable) > 0 {
		lt.aside, err = freeAsidePath(path)
		if err != nil {
			return nil, err
		}
	}
	return lt, nil
}

// ── the run-config snapshot history ──────────────────────────────────────────────────────────

// snapshotTakeover is the config-snapshot history's takeover. A snapshot is a FULL run
// configuration and "restore" writes it back into the pool verbatim, so a snapshot holding
// pre-rebuild records is a loaded gun: one restore re-injects exactly the state this import
// removes. Those snapshots are moved aside — kept on disk under their own names, no longer offered
// as a restore point.
type snapshotTakeover struct {
	dir   string
	to    string
	moved []string
	kept  int
}

// readSnapshotTakeover lists the snapshots whose run set is not in the rebuilt form. A file that
// does not parse at all is moved too: an unreadable restore point is not a restore point.
func readSnapshotTakeover(dir string) (*snapshotTakeover, error) {
	st := &snapshotTakeover{dir: dir}
	if dir == "" {
		return st, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return st, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		legacy, err := snapshotIsLegacy(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		if legacy {
			st.moved = append(st.moved, e.Name())
			continue
		}
		st.kept++
	}
	sort.Strings(st.moved)
	if len(st.moved) > 0 {
		st.to, err = freeAsidePath(dir)
		if err != nil {
			return nil, err
		}
	}
	return st, nil
}

// snapshotIsLegacy reports whether one snapshot file holds any run record the daemon would
// misread. A snapshot with no runs at all is left where it is: restoring it yields an empty pool,
// which is an honest historical state and can inject nothing.
func snapshotIsLegacy(path string) (bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	recs, err := poolRecords(b, "runs")
	if err != nil {
		return true, nil // unreadable: not interpretable, so not a restore point
	}
	for _, raw := range recs {
		obj := recordFields(raw)
		if obj == nil {
			return true, nil
		}
		if formOf(obj, runLegacyMarkers, runNewMarkers, false) != formNew {
			return true, nil
		}
	}
	return false, nil
}

// ── pools the rebuild has no reader for ──────────────────────────────────────────────────────

// orphan is a pre-rebuild pool file the rebuilt code never opens. Leaving it in the state
// directory would leave the operator with data that looks live and is not, so it is moved next to
// the other set-aside artifacts and named with the reason.
type orphan struct {
	from string
	to   string
	why  string
}

// orphanPools names every pre-rebuild pool below mercury/ that the rebuilt code has no reader for,
// with the reason it has none. The list is explicit rather than "everything unknown": a file the
// migration does not recognise must stay untouched, not be swept up by a pattern.
//
// The pre-rebuild single-execution MARKER is deliberately not in this list. It is not stock to
// preserve but a trap to remove, and the cutover removes it by name in its own step
// (deploy/migration/00-cutover.md, step 3) — a second mechanism here would be the same job done
// twice, and naming the marker in the shipped source is exactly what the REQ-039b tripwire forbids.
func orphanPools() []struct{ name, why string } {
	return []struct{ name, why string }{
		{"runs-settings.json", "the rebuilt settings pool is settings.json and names the field " +
			"maxConcurrency; its first-start value comes from DEVLAB_RUNS_MAX_CONCURRENCY, and from " +
			"the first change on the stored value wins (REQ-013.2)"},
		{"runs-incidents.json", "the rebuild raises every finding in the notice pool " +
			"(runs-notices.json); there is no second pool beside it"},
	}
}

// ── moving and copying ───────────────────────────────────────────────────────────────────────

// freeAsidePath returns a set-aside path that does not exist yet, so a repeated or interrupted
// takeover can never overwrite an earlier copy of the stock.
func freeAsidePath(base string) (string, error) {
	cand := base + asideSuffix
	for n := 1; n < 100; n++ {
		if n > 1 {
			cand = fmt.Sprintf("%s%s.%d", base, asideSuffix, n)
		}
		if _, err := os.Stat(cand); os.IsNotExist(err) {
			return cand, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("no free set-aside name beside %s — %d already exist", base, 99)
}

// copyAside puts a verbatim copy of the stock beside the original BEFORE the original is
// rewritten. It copies rather than renames because the pool file must never be absent: a missing
// pool reads as an empty one, and an interrupted takeover must leave the old stock in place, not
// an empty service.
func copyAside(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return fsatomic.WriteFile(dst, b, 0o600)
}

// moveFilesAside renames the named files from src into dst, creating dst. Nothing is deleted and
// nothing is overwritten: a destination that already holds the name is left alone, so repeating
// the move is safe.
func moveFilesAside(src, dst string, names []string) error {
	if len(names) == 0 {
		return nil
	}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	for _, n := range names {
		to := filepath.Join(dst, n)
		if _, err := os.Stat(to); err == nil {
			continue
		}
		if err := os.Rename(filepath.Join(src, n), to); err != nil {
			return err
		}
	}
	return nil
}
