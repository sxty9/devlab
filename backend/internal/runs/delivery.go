package runs

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// A Delivery is the UNIT of work a run produced at one repository (req 8): the exact commit range it
// added to the growing dev branch, the per-delivery snapshot branch that carries it, its stacked PR, and
// when it was made. Recording it is what makes a single piece of work addressable afterwards — to name
// what dev serves, to stack the next PR on it, and to counter-book (roll back) precisely this delivery.
//
// A delivery is NEVER destroyed. Rolling one back adds a REVERSING delivery (RevertOf set) and flips this
// one's Status to reverted; history is only ever appended to.
type Delivery struct {
	ID       string `json:"id"`
	RunID    string `json:"runId"`
	ResultID string `json:"resultId"`
	RunName  string `json:"runName,omitempty"` // snapshot so a delivery is legible after the run is deleted
	Repo     string `json:"repo"`              // owner/name

	// Branch is the immutable per-delivery snapshot branch (mercury-run/<runId>/<deliveryId>) whose tip is
	// ToCommit; its PR is stacked on BaseBranch so the PR shows only THIS delivery's changes. DevBranch is
	// the persistent integration branch (mercury-dev) this delivery grew — the state dev actually serves.
	Branch     string `json:"branch"`
	DevBranch  string `json:"devBranch"`
	BaseBranch string `json:"baseBranch"` // the stacked PR base: the previous open delivery's branch, else the default branch

	// FromCommit..ToCommit is exactly this delivery's own work on the dev branch (captured AFTER the
	// default-branch fold, so the range is linear and contains no merge) — the range a counter-booking
	// reverses.
	FromCommit string `json:"fromCommit"`
	ToCommit   string `json:"toCommit"`

	PRNumber  int            `json:"prNumber,omitempty"`
	PRUrl     string         `json:"prUrl,omitempty"`
	CreatedAt time.Time      `json:"createdAt"`
	Status    DeliveryStatus `json:"status"`

	// RevertedAt/RevertedBy are set when this delivery has been counter-booked. RevertOf, on a REVERSING
	// delivery, points at the delivery it undoes (so a reversal is itself addressable and traceable).
	RevertedAt *time.Time `json:"revertedAt,omitempty"`
	RevertedBy string     `json:"revertedBy,omitempty"`
	RevertOf   string     `json:"revertOf,omitempty"`
}

// DeliveryStatus tracks a delivery's lifecycle. It mirrors the PR outcome for the common path and adds
// "reverted" for a counter-booked delivery. Only an "open" delivery is eligible as a stacked PR base
// (req 9 — the base is the previous NOT-YET-MERGED delivery).
type DeliveryStatus string

const (
	DeliveryOpen     DeliveryStatus = "open"     // PR still open (not yet merged) — the growing dev tip lineage
	DeliveryMerged   DeliveryStatus = "merged"   // PR merged into the default branch
	DeliveryClosed   DeliveryStatus = "closed"   // PR closed without merging (withdrawn)
	DeliveryReverted DeliveryStatus = "reverted" // counter-booked by a reversing delivery
)

// NewDeliveryID mints an unguessable delivery id (a record key, never a path segment).
func NewDeliveryID() string {
	var b [10]byte
	_, _ = rand.Read(b[:])
	return "dlv_" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:]))
}

// DeliveryStore persists the delivery ledger (a small JSON file, same discipline as PRStore/runs.json:
// atomic tmp+rename, 0600, missing → empty). It is a passive pool — it records deliveries and answers
// queries; every decision (which base to stack on, what to roll back) is made by the caller.
type DeliveryStore struct {
	path string
	mu   sync.Mutex
}

func NewDeliveryStore() *DeliveryStore { return &DeliveryStore{path: deliveriesPath()} }

func deliveriesPath() string {
	if p := os.Getenv("DEVLAB_MERCURY_RUNS_DELIVERIES"); p != "" {
		return p
	}
	return filepath.Join("/var/lib/devlab/mercury", "runs-deliveries.json")
}

type deliveryFile struct {
	Deliveries []Delivery `json:"deliveries"`
}

func (s *DeliveryStore) load() ([]Delivery, error) {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var f deliveryFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	return f.Deliveries, nil
}

// List returns every delivery, oldest first (missing store → empty).
func (s *DeliveryStore) List() ([]Delivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sorted()
}

// sorted loads and returns deliveries oldest-first (stable ordering by CreatedAt). Caller holds s.mu.
func (s *DeliveryStore) sorted() ([]Delivery, error) {
	cur, err := s.load()
	if err != nil {
		return nil, err
	}
	sort.SliceStable(cur, func(i, j int) bool { return cur[i].CreatedAt.Before(cur[j].CreatedAt) })
	return cur, nil
}

// ListForRepo returns a repo's deliveries, oldest first.
func (s *DeliveryStore) ListForRepo(repo string) ([]Delivery, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	out := make([]Delivery, 0, len(all))
	for _, d := range all {
		if d.Repo == repo {
			out = append(out, d)
		}
	}
	return out, nil
}

// Get returns one delivery by id.
func (s *DeliveryStore) Get(id string) (Delivery, bool, error) {
	all, err := s.List()
	if err != nil {
		return Delivery{}, false, err
	}
	for _, d := range all {
		if d.ID == id {
			return d, true, nil
		}
	}
	return Delivery{}, false, nil
}

// LatestOpen returns the most recent OPEN delivery for a repo — the stacked-PR base of the next delivery
// (req 9). ok=false when none is open, in which case the caller stacks on the default branch instead.
func (s *DeliveryStore) LatestOpen(repo string) (Delivery, bool, error) {
	list, err := s.ListForRepo(repo)
	if err != nil {
		return Delivery{}, false, err
	}
	for i := len(list) - 1; i >= 0; i-- {
		if list[i].Status == DeliveryOpen {
			return list[i], true, nil
		}
	}
	return Delivery{}, false, nil
}

// LaterOpenDeliveries returns the OPEN deliveries for a repo created after the given delivery — the work
// that is stacked ON it. A rollback consults this to name what a counter-booking would sit under.
func (s *DeliveryStore) LaterOpenDeliveries(repo, afterID string) ([]Delivery, error) {
	list, err := s.ListForRepo(repo)
	if err != nil {
		return nil, err
	}
	seen := false
	var out []Delivery
	for _, d := range list {
		if d.ID == afterID {
			seen = true
			continue
		}
		if seen && d.Status == DeliveryOpen {
			out = append(out, d)
		}
	}
	return out, nil
}

// Add records a new delivery (deduped by id).
func (s *DeliveryStore) Add(d Delivery) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.load()
	if err != nil {
		return err
	}
	for _, e := range cur {
		if e.ID == d.ID {
			return nil // already recorded
		}
	}
	if d.Status == "" {
		d.Status = DeliveryOpen
	}
	return s.save(append(cur, d))
}

// Update applies fn to the delivery with the given id and saves. A no-op (delivery absent) returns nil.
func (s *DeliveryStore) Update(id string, fn func(*Delivery)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.load()
	if err != nil {
		return err
	}
	changed := false
	for i := range cur {
		if cur[i].ID == id {
			fn(&cur[i])
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return s.save(cur)
}

// SetStatusByPR flips the status of the delivery matching (repo, prNumber) — used by Maintain to mirror a
// PR merge/close onto the ledger. A reverted delivery is left as-is (its withdrawal is authoritative). It
// returns the affected delivery (ok=false when none matches, e.g. a PR predating the ledger).
func (s *DeliveryStore) SetStatusByPR(repo string, prNumber int, status DeliveryStatus) (Delivery, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.load()
	if err != nil {
		return Delivery{}, false, err
	}
	var hit Delivery
	found := false
	for i := range cur {
		if cur[i].Repo == repo && cur[i].PRNumber == prNumber && prNumber != 0 {
			if cur[i].Status != DeliveryReverted {
				cur[i].Status = status
			}
			hit, found = cur[i], true
		}
	}
	if !found {
		return Delivery{}, false, nil
	}
	if err := s.save(cur); err != nil {
		return Delivery{}, false, err
	}
	return hit, true, nil
}

func (s *DeliveryStore) save(ds []Delivery) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(deliveryFile{Deliveries: ds}, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
