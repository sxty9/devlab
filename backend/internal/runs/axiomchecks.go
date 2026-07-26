package runs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
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

// AxiomCheck is one recorded examination: the commit the repository stood at, and when.
type AxiomCheck struct {
	Commit string    `json:"commit"`
	At     time.Time `json:"at"`
}

// checksFile is the on-disk shape: repo id → axiom id → the last examination.
type checksFile struct {
	Repos map[string]map[string]AxiomCheck `json:"repos"`
}

// NewAxiomChecks builds the store from the environment. Like the other Mercury stores it never errors:
// a missing file is an empty record.
func NewAxiomChecks() *AxiomChecks {
	p := os.Getenv("DEVLAB_MERCURY_AXIOM_CHECKS")
	if p == "" {
		p = filepath.Join(filepath.Dir(runsPath()), "axiom-checks.json")
	}
	return &AxiomChecks{path: p}
}

func (a *AxiomChecks) load() checksFile {
	var f checksFile
	b, err := os.ReadFile(a.path)
	if err == nil {
		_ = json.Unmarshal(b, &f)
	}
	if f.Repos == nil {
		f.Repos = map[string]map[string]AxiomCheck{}
	}
	return f
}

// ForRepo returns the recorded examinations for one repository (empty map when there are none).
func (a *AxiomChecks) ForRepo(repo string) map[string]AxiomCheck {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	out := map[string]AxiomCheck{}
	for id, c := range a.load().Repos[repo] {
		out[id] = c
	}
	return out
}

// Record notes that repo was examined against every axiom in ids, at commit. Best-effort: a failed
// write costs the next run its incrementality, never its correctness (it simply re-examines in full).
func (a *AxiomChecks) Record(repo string, ids []string, commit string, at time.Time) {
	if a == nil || repo == "" || commit == "" || len(ids) == 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	f := a.load()
	if f.Repos[repo] == nil {
		f.Repos[repo] = map[string]AxiomCheck{}
	}
	for _, id := range ids {
		if id != "" {
			f.Repos[repo][id] = AxiomCheck{Commit: commit, At: at.UTC()}
		}
	}
	if err := os.MkdirAll(filepath.Dir(a.path), 0o700); err != nil {
		return
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return
	}
	tmp := a.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, a.path)
	}
}
