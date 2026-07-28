package runs

import (
	"encoding/json"
	"os"
	"sync"
	"time"

	"devlab/backend/internal/fsatomic"

	"devlab/backend/internal/statepath"
)

// A run in pr/full mode opens a PR per repo. A human may merge it anytime; if none does within the
// auto-merge window the scheduler merges it. PendingPR tracks the ones still awaiting that outcome.
type PendingPR struct {
	Repo      string    `json:"repo"` // owner/name
	Number    int       `json:"number"`
	URL       string    `json:"url"`
	RunID     string    `json:"runId"`
	CreatedAt time.Time `json:"createdAt"`
	MergeBy   time.Time `json:"mergeBy"` // auto-merge on/after this time
	// LastChecked is when Maintain last spent a GitHub read on this PR. It exists ONLY for full mode's
	// merge-detection throttle: an in-window PR is re-fetched at most once per recheck interval, so the
	// sweep never GETs every tracked PR every tick and exhausts the rate budget. report/pr mode never
	// reads or writes it (those modes only ever touch OVERDUE PRs), so their behavior is unchanged and
	// zero-value here. Older stored files simply carry the zero time.
	LastChecked time.Time `json:"lastChecked,omitempty"`
}

// PRStore persists the pending-PR set (a small JSON file, same discipline as the runs store).
type PRStore struct {
	path string
	mu   sync.Mutex
}

func NewPRStore(p *statepath.Paths) *PRStore { return &PRStore{path: prsPath(p)} }

func prsPath(p *statepath.Paths) string {
	if v := os.Getenv("DEVLAB_MERCURY_RUNS_PRS"); v != "" {
		return v
	}
	if p != nil {
		return p.PRs()
	}
	return ""
}

type prFile struct {
	PRs []PendingPR `json:"prs"`
}

func (s *PRStore) load() ([]PendingPR, error) {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var f prFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	return f.PRs, nil
}

// List returns all pending PRs (missing store → empty).
func (s *PRStore) List() ([]PendingPR, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

// Add records a new pending PR (deduped by repo+number).
func (s *PRStore) Add(pr PendingPR) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.load()
	if err != nil {
		return err
	}
	for _, p := range cur {
		if p.Repo == pr.Repo && p.Number == pr.Number {
			return nil // already tracked
		}
	}
	return s.save(append(cur, pr))
}

// Remove drops a pending PR once it is merged or closed.
func (s *PRStore) Remove(repo string, number int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.load()
	if err != nil {
		return err
	}
	out := make([]PendingPR, 0, len(cur))
	for _, p := range cur {
		if p.Repo == repo && p.Number == number {
			continue
		}
		out = append(out, p)
	}
	return s.save(out)
}

// Touch records that a tracked PR was just checked (full-mode throttle). A no-op if it isn't tracked
// (already merged/untracked between the List and here). Never creates a PR.
func (s *PRStore) Touch(repo string, number int, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.load()
	if err != nil {
		return err
	}
	changed := false
	for i := range cur {
		if cur[i].Repo == repo && cur[i].Number == number {
			cur[i].LastChecked = at
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return s.save(cur)
}

func (s *PRStore) save(prs []PendingPR) error {
	return fsatomic.WriteJSON(s.path, prFile{PRs: prs})
}
