package runs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// A Snapshot is the full run configuration at one instant, written on every Store mutation so any
// past constellation can be restored. Full snapshots (not diffs) are the simplest correct match to
// "restore a past version" and are robust to schema drift.
type Snapshot struct {
	TS     string `json:"ts"`     // RFC3339Nano — also the (colon-escaped) filename stem
	Action string `json:"action"` // create | update | delete | apply-fill | apply-replace | restore | fire
	Actor  string `json:"actor"`
	Runs   []Run  `json:"runs"`
}

// SnapshotMeta is the listing view of a snapshot (without the payload).
type SnapshotMeta struct {
	TS       string `json:"ts"`
	Action   string `json:"action"`
	Actor    string `json:"actor"`
	RunCount int    `json:"runCount"`
}

// History is an append-only directory of config snapshots.
type History struct {
	dir string
}

// NewHistory builds the history store from the environment.
func NewHistory() *History {
	return &History{dir: historyDir()}
}

func historyDir() string {
	if p := os.Getenv("DEVLAB_MERCURY_RUNS_HISTORY"); p != "" {
		return p
	}
	return filepath.Join("/var/lib/devlab/mercury", "runs-history")
}

// snapshot writes the resulting config; best-effort (a history write must never fail a mutation).
// The write is atomic (tmp + rename, 0600) like every sibling store: List/Get read the history
// directory without holding the runs-store mutex, so a plain truncating write could let a concurrent
// reader observe a half-written snapshot, and a crash mid-write could leave a torn file at the final
// path. The tmp sits in the same directory (rename stays on one filesystem) and carries a ".json.tmp"
// suffix, so List — which matches only ".json" — never picks it up.
func (h *History) snapshot(action, actor string, runs []Run) {
	if h == nil {
		return
	}
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	snap := Snapshot{TS: ts, Action: action, Actor: actor, Runs: runs}
	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return
	}
	if os.MkdirAll(h.dir, 0o700) != nil {
		return
	}
	final := filepath.Join(h.dir, stem(ts)+".json")
	tmp := final + ".tmp"
	if os.WriteFile(tmp, b, 0o600) != nil {
		return
	}
	if os.Rename(tmp, final) != nil {
		_ = os.Remove(tmp)
	}
}

// List returns snapshot metadata, newest first.
func (h *History) List() ([]SnapshotMeta, error) {
	out := []SnapshotMeta{} // never nil: a nil slice marshals to JSON null and hangs the UI
	entries, err := os.ReadDir(h.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return out, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		snap, err := h.readFile(filepath.Join(h.dir, e.Name()))
		if err != nil {
			continue
		}
		out = append(out, SnapshotMeta{TS: snap.TS, Action: snap.Action, Actor: snap.Actor, RunCount: len(snap.Runs)})
	}
	// Newest first, by the PARSED time — a lexicographic string sort mis-orders RFC3339Nano values
	// whose trailing-zero fractions were trimmed (".1Z" would sort after ".11Z").
	sort.Slice(out, func(i, j int) bool {
		ti, _ := time.Parse(time.RFC3339Nano, out[i].TS)
		tj, _ := time.Parse(time.RFC3339Nano, out[j].TS)
		return ti.After(tj)
	})
	return out, nil
}

// Get returns the full snapshot for a timestamp (as reported by List). It confines reads to the
// history directory: a legit stem has no path separators, so a `ts` carrying "/" or ".." (which
// stem() does not escape) would land elsewhere — reject it defensively even though the handler also
// validates the format.
func (h *History) Get(ts string) (Snapshot, bool, error) {
	full := filepath.Join(h.dir, stem(ts)+".json")
	if filepath.Dir(full) != filepath.Clean(h.dir) {
		return Snapshot{}, false, nil
	}
	snap, err := h.readFile(full)
	if err != nil {
		if os.IsNotExist(err) {
			return Snapshot{}, false, nil
		}
		return Snapshot{}, false, err
	}
	return snap, true, nil
}

func (h *History) readFile(path string) (Snapshot, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, err
	}
	var s Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return Snapshot{}, err
	}
	return s, nil
}

// stem makes a timestamp filesystem-safe (colons → dashes) without losing lexicographic ordering.
func stem(ts string) string {
	return strings.ReplaceAll(ts, ":", "-")
}
