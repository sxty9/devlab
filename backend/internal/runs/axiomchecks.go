package runs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	"devlab/backend/internal/fsatomic"
	"devlab/backend/internal/statepath"
)

// AxiomChecks is the passive record of WHICH COMMIT a repository was last examined against, per axiom.
// It exists so a recurring run can work incrementally: an axiom that was already checked up to commit X
// only needs the commits since X, while an axiom never checked here needs the whole repository.
//
// Before this store, the run prompt asserted that such a record lived in the repo's CLAUDE.md — nothing
// ever wrote one, so every run fell through to "never checked ⇒ scan everything" and re-read entire
// repositories every night at maximum reasoning effort.
//
// It is a pure store (a passive pool): it holds commits and timestamps and interprets nothing. Which
// axioms belong to a run, what counts as checked, and how the prompt phrases it all live outside.
type AxiomChecks struct {
	path string
	mu   sync.Mutex
}

// ErrAxiomChecksUnreadable names a pool file that exists but cannot be read as the record: a damaged
// or foreign file, or one the process may not read. It is deliberately distinct from "nothing checked
// yet" — the two must never be answered the same way, because one means "examine in full" and the
// other means "I cannot tell you anything, and there is a file here I must not overwrite blindly".
var ErrAxiomChecksUnreadable = errors.New("axiom-check pool unreadable")

// AxiomCheck is one recorded examination: the commit the repository stood at, and when.
type AxiomCheck struct {
	Commit string    `json:"commit"`
	At     time.Time `json:"at"`
}

// checksFile is the on-disk shape: repo id → axiom id → the last examination.
type checksFile struct {
	Repos map[string]map[string]AxiomCheck `json:"repos"`
}

// NewAxiomChecks builds the store from the environment. Constructing it never fails; reading and
// writing report what went wrong (a missing file is an empty record, a damaged one an error).
func NewAxiomChecks(sp *statepath.Paths) *AxiomChecks {
	p := os.Getenv("DEVLAB_MERCURY_AXIOM_CHECKS")
	if p == "" && sp != nil {
		p = sp.AxiomChecks()
	}
	return &AxiomChecks{path: p}
}

// load reads the pool. A missing file is an empty record — nothing has been checked yet. Every other
// failure is returned NAMED, never swallowed: a damaged file read as "no axiom was ever checked"
// makes the pool lie in the one direction that also destroys it, because the next write would then
// persist that emptiness over the damaged content.
func (a *AxiomChecks) load() (checksFile, error) {
	empty := checksFile{Repos: map[string]map[string]AxiomCheck{}}
	b, err := os.ReadFile(a.path)
	if err != nil {
		if os.IsNotExist(err) {
			return empty, nil
		}
		return empty, fmt.Errorf("%w: %s: %w", ErrAxiomChecksUnreadable, a.path, err)
	}
	var f checksFile
	if err := json.Unmarshal(b, &f); err != nil {
		return empty, fmt.Errorf("%w: %s: %w", ErrAxiomChecksUnreadable, a.path, err)
	}
	if f.Repos == nil {
		f.Repos = map[string]map[string]AxiomCheck{}
	}
	return f, nil
}

// ForRepo returns the recorded examinations for one repository (an empty map when there are none) and
// reports an unreadable pool. The map is a copy, and it is empty on error: the caller then examines in
// full — it loses its incrementality, never its correctness — but it learns that it did so blindly.
func (a *AxiomChecks) ForRepo(repo string) (map[string]AxiomCheck, error) {
	if a == nil {
		return nil, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	f, err := a.load()
	out := map[string]AxiomCheck{}
	for id, c := range f.Repos[repo] {
		out[id] = c
	}
	return out, err
}

// Record notes that repo was examined against every axiom in ids, at commit.
//
// Damaged existing content is never silently replaced. Overwriting it would turn one unreadable file
// into permanent data loss for every OTHER repository and axiom in it, so the file is first set aside
// under a dated name (a rename — the bytes are not rewritten and stay recoverable) and only then does
// a fresh record start. The returned error names both the damage and where the evidence went; if the
// file cannot even be set aside, nothing is written at all.
//
// A failed write costs the next run its incrementality, never its correctness (it simply re-examines
// in full), so the caller may treat the error as a report rather than a fatality — but it is reported.
func (a *AxiomChecks) Record(repo string, ids []string, commit string, at time.Time) error {
	if a == nil || a.path == "" || repo == "" || commit == "" || len(ids) == 0 {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	f, loadErr := a.load()
	var damage error
	if loadErr != nil {
		kept, err := a.setAside(at)
		if err != nil {
			return fmt.Errorf("%w; it could not be set aside either, so nothing was written: %w", loadErr, err)
		}
		f = checksFile{Repos: map[string]map[string]AxiomCheck{}}
		damage = fmt.Errorf("%w; set aside as %s, a fresh record was started", loadErr, kept)
	}
	if f.Repos[repo] == nil {
		f.Repos[repo] = map[string]AxiomCheck{}
	}
	for _, id := range ids {
		if id != "" {
			f.Repos[repo][id] = AxiomCheck{Commit: commit, At: at.UTC()}
		}
	}
	// Persisted through the ONE atomic write path (fsatomic) — no pool re-derives tmp+rename (B-11).
	if err := fsatomic.WriteJSON(a.path, f); err != nil {
		return errors.Join(damage, fmt.Errorf("axiom-check pool: write %s: %w", a.path, err))
	}
	return damage
}

// setAside moves an unreadable pool file out of the way and returns where it went. The name carries
// the moment; a name already taken gets a counter, so a second damage never overwrites the evidence
// of the first.
func (a *AxiomChecks) setAside(at time.Time) (string, error) {
	base := a.path + ".damaged-" + at.UTC().Format("20060102T150405.000000000Z")
	for i := 0; ; i++ {
		kept := base
		if i > 0 {
			kept += "-" + strconv.Itoa(i)
		}
		if _, err := os.Lstat(kept); err == nil {
			if i < 100 {
				continue
			}
			return "", fmt.Errorf("no free name beside %s", a.path)
		}
		if err := os.Rename(a.path, kept); err != nil {
			return "", err
		}
		return kept, nil
	}
}
