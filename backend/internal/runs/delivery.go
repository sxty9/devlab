// The delivery ledger — a passive pool of every delivery (intent-before-PR): the addressable
// unit of work an execution produced at one repository. A delivery is NEVER destroyed: rolling
// one back appends a REVERSING delivery (ReversalOf set) and the original is closed with a
// reason; history is only appended to. The origin status (REQ-033) is derived solely from this
// ledger.
package runs

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"devlab/backend/internal/fsatomic"
	"devlab/backend/internal/statepath"
)

// Delivery is one recorded delivery (Welle-0 contract §3.6). Branch is the delivery branch the
// PR rides on; FromCommit..ToCommit is exactly this delivery's work. MergedAt set = merged;
// ClosedAt set = closed without merge, with its reason (the B-8 completion rule needs
// merged | rolled back | closed-with-reason to be distinguishable). ReversalOf, on a REVERSING
// delivery, points at the delivery it counter-books. ExecutionID ties the delivery to its
// execution ("" on none, e.g. a reversal issued by hand).
//
// FailedAt marks a delivery whose work was committed to its branch but whose delivery did NOT
// complete — a failed dev delivery, a stale root script, a GitHub rejection. It is the difference
// between "keine Lieferung" (no record at all) and "Lieferung gescheitert" (recorded, and visibly
// at the tip): a run that produced commits but could not ship them leaves this mark instead of
// vanishing, so the next run cannot silently branch past its work. A failed delivery stays
// UNSETTLED (OpenState true) — its execution is not history-ready and its ToDo stays open — until
// it is either resolved (re-delivered, which clears the mark) or rolled back (which closes it).
type Delivery struct {
	ID           string     `json:"id"`
	Repo         string     `json:"repo"`
	Branch       string     `json:"branch"`
	FromCommit   string     `json:"fromCommit"`
	ToCommit     string     `json:"toCommit"`
	PRNumber     int        `json:"prNumber,omitempty"`
	PRURL        string     `json:"prUrl,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	MergedAt     *time.Time `json:"mergedAt,omitempty"`
	ClosedAt     *time.Time `json:"closedAt,omitempty"`
	ClosedReason string     `json:"closedReason,omitempty"`
	FailedAt     *time.Time `json:"failedAt,omitempty"`
	FailedReason string     `json:"failedReason,omitempty"`
	ReversalOf   string     `json:"reversalOf,omitempty"`
	ExecutionID  string     `json:"executionId,omitempty"`
}

// OpenState reports whether the delivery is still UNSETTLED: neither merged nor closed. A failed
// delivery is unsettled too — its work is committed but not shipped, so the B-8 completion rule
// keeps its execution open until the failure is resolved or rolled back. Whether a delivery is a
// valid base to STACK on is a different question (a failed one is not); NextPRBase answers that.
func (d Delivery) OpenState() bool { return d.MergedAt == nil && d.ClosedAt == nil }

// Failed reports the "Lieferung gescheitert" state: the work is on the branch, the delivery did
// not complete, and it has neither been merged nor rolled back. Such a delivery is the tip of the
// stack — nothing is branched past it until it is resolved (which clears FailedAt) or rolled back
// (which sets ClosedAt).
func (d Delivery) Failed() bool { return d.FailedAt != nil && d.MergedAt == nil && d.ClosedAt == nil }

// NewDeliveryID mints an unguessable delivery id (a record key, never a path segment).
func NewDeliveryID() string {
	var b [10]byte
	_, _ = rand.Read(b[:])
	return "dlv_" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:]))
}

// DeliveryStore persists the delivery ledger — a passive pool; every decision (which base to
// stack on, what to roll back) is made by the caller.
type DeliveryStore struct {
	path string
	mu   sync.Mutex
}

// NewDeliveryStore builds the store below the state root (env override
// DEVLAB_MERCURY_RUNS_DELIVERIES first — a ported test seam).
func NewDeliveryStore(p *statepath.Paths) *DeliveryStore {
	path := os.Getenv("DEVLAB_MERCURY_RUNS_DELIVERIES")
	if path == "" && p != nil {
		path = p.Deliveries()
	}
	return &DeliveryStore{path: path}
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

// All returns every delivery, oldest first (missing store → empty).
func (s *DeliveryStore) All() ([]Delivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.load()
	if err != nil {
		return nil, err
	}
	sort.SliceStable(cur, func(i, j int) bool { return cur[i].CreatedAt.Before(cur[j].CreatedAt) })
	return cur, nil
}

// Open returns a repo's OPEN deliveries (not merged, not closed) in stack order (oldest first)
// — the next PR stacks on the last of them (REQ-024).
func (s *DeliveryStore) Open(repo string) ([]Delivery, error) {
	all, err := s.All()
	if err != nil {
		return nil, err
	}
	out := []Delivery{}
	for _, d := range all {
		if d.Repo == repo && d.OpenState() {
			out = append(out, d)
		}
	}
	return out, nil
}

// OpenForExecution returns the newest open delivery at repo that belongs to an execution
// (ExecutionID set) — the resume path's probe "did my interrupted execution already deliver
// here?" (C F1). nil when none.
func (s *DeliveryStore) OpenForExecution(repo string) (*Delivery, error) {
	open, err := s.Open(repo)
	if err != nil {
		return nil, err
	}
	for i := len(open) - 1; i >= 0; i-- {
		if open[i].ExecutionID != "" {
			d := open[i]
			return &d, nil
		}
	}
	return nil, nil
}

// ByID returns one delivery by id.
func (s *DeliveryStore) ByID(id string) (Delivery, bool, error) {
	all, err := s.All()
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

// Put inserts or replaces one delivery (matched by ID) atomically — the intent write BEFORE the
// PR exists, and every later status mirror.
func (s *DeliveryStore) Put(d Delivery) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.load()
	if err != nil {
		return err
	}
	replaced := false
	for i := range cur {
		if cur[i].ID == d.ID {
			cur[i] = d
			replaced = true
			break
		}
	}
	if !replaced {
		cur = append(cur, d)
	}
	return fsatomic.WriteJSON(s.path, deliveryFile{Deliveries: cur})
}
