package runs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
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

	// Deploy-blocking (full mode). A merged PR whose prod-deploy fails for a PERMANENT reason is retried a
	// few times, then BLOCKED: it waits for an explicit resume instead of retrying forever, and while
	// blocked Maintain skips it entirely so one broken repo can't hold up the others. Only permanent
	// failures count; a transient one (network) is retried and never increments DeployAttempts. All of
	// this is policy owned by the caller — the store just persists the fields it sets via Update.
	DeployAttempts int       `json:"deployAttempts,omitempty"` // consecutive permanent-failure attempts
	Blocked        bool      `json:"blocked,omitempty"`        // stop auto-retrying until an explicit resume
	BlockedReason  string    `json:"blockedReason,omitempty"`  // human cause, naming the service and target
	BlockedAt      time.Time `json:"blockedAt,omitempty"`      // when the block was recorded
}

// PRStore persists the pending-PR set (a small JSON file, same discipline as the runs store).
type PRStore struct {
	path string
	mu   sync.Mutex
}

func NewPRStore() *PRStore { return &PRStore{path: prsPath()} }

func prsPath() string {
	if p := os.Getenv("DEVLAB_MERCURY_RUNS_PRS"); p != "" {
		return p
	}
	return filepath.Join("/var/lib/devlab/mercury", "runs-prs.json")
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

// Update applies mutate to the tracked PR matching (repo, number) and saves atomically. found=false (and
// no save) when the PR is untracked; it never creates one. All decisions about WHAT to change live in the
// caller's closure — the store stays a passive pool, the mutation is unteilbar under the same lock.
func (s *PRStore) Update(repo string, number int, mutate func(*PendingPR)) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.load()
	if err != nil {
		return false, err
	}
	for i := range cur {
		if cur[i].Repo == repo && cur[i].Number == number {
			mutate(&cur[i])
			return true, s.save(cur)
		}
	}
	return false, nil
}

func (s *PRStore) save(prs []PendingPR) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(prFile{PRs: prs}, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
