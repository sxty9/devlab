// The production-state pool — a passive, per-repository record of the standard-branch commit that
// production is known to carry (the commit the last successful production send shipped). It exists
// so the chain can answer ONE question it otherwise cannot: has the standard branch advanced past
// what production runs, without a delivery to account for it? A stale production host is invisible
// as long as nothing but the merge of a booked delivery ever triggers a production send — the
// standard branch can move on another path (a hand-merged pull request) and production quietly keeps
// an older state while the ledger still reads "delivered". This pool is the reference the production
// reconciliation (deliver.MaintainProd) measures the standard branch against.
//
// Like every Data Pool it is PASSIVE (Passive Speicher): it holds the fact, it does not judge it.
// The judgement — is this drift, should production be re-sent — is made by the reconciliation that
// reads it, never here.
package runs

import (
	"encoding/json"
	"os"
	"sort"
	"sync"
	"time"

	"devlab/backend/internal/fsatomic"
	"devlab/backend/internal/statepath"
)

// ProdRecord is the standard-branch commit production carries for ONE repository, and when the send
// that put it there completed. Repo is the ledger's full "owner/repo" name (the same value a
// Delivery carries), Commit is the full standard-branch SHA that was built and shipped.
//
// HostKey is the identity of the production host this record DESCRIBES: the ssh host-key fingerprint
// the target presented when the send that wrote this record proved it running. A production-state
// record is a CLAIM about one specific machine, and a machine's ssh host key is its identity — a
// rebuilt host presents a new key. So a record whose HostKey no longer matches the host currently
// there is not stale but VOID: a claim about a machine that has been replaced. The pool only HOLDS
// this identity; whether a record is void is judged on the reading side (the reconciliation that
// compares it against the host currently there), never here — the pool stays a passive store
// (Passive Speicher). It is omitempty so a record written before the binding existed carries an empty
// identity, which the reading side treats as "cannot vouch for this host — measure afresh".
type ProdRecord struct {
	Repo       string    `json:"repo"`
	Commit     string    `json:"commit"`
	DeployedAt time.Time `json:"deployedAt"`
	HostKey    string    `json:"hostKey,omitempty"`
}

// ProdStateStore persists the production-state pool — one record per repository, matched by Repo.
type ProdStateStore struct {
	path string
	mu   sync.Mutex
}

// NewProdStateStore builds the store below the state root (env override DEVLAB_MERCURY_RUNS_PRODSTATE
// first — a test seam, mirroring the other run pools).
func NewProdStateStore(p *statepath.Paths) *ProdStateStore {
	path := os.Getenv("DEVLAB_MERCURY_RUNS_PRODSTATE")
	if path == "" && p != nil {
		path = p.ProdState()
	}
	return &ProdStateStore{path: path}
}

type prodStateFile struct {
	Records []ProdRecord `json:"records"`
}

func (s *ProdStateStore) load() ([]ProdRecord, error) {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var f prodStateFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	return f.Records, nil
}

// All returns every production-state record, repository order (missing store → empty).
func (s *ProdStateStore) All() ([]ProdRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.load()
	if err != nil {
		return nil, err
	}
	sort.SliceStable(cur, func(i, j int) bool { return cur[i].Repo < cur[j].Repo })
	return cur, nil
}

// Get returns the record for one repository (ok false when none is on file yet — production's state
// for that repository is unknown, which the reconciliation treats as "measure, do not assume").
func (s *ProdStateStore) Get(repo string) (ProdRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.load()
	if err != nil {
		return ProdRecord{}, false, err
	}
	for _, r := range cur {
		if r.Repo == repo {
			return r, true, nil
		}
	}
	return ProdRecord{}, false, nil
}

// Put inserts or replaces the record for its repository (matched by Repo) atomically.
func (s *ProdStateStore) Put(rec ProdRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.load()
	if err != nil {
		return err
	}
	replaced := false
	for i := range cur {
		if cur[i].Repo == rec.Repo {
			cur[i] = rec
			replaced = true
			break
		}
	}
	if !replaced {
		cur = append(cur, rec)
	}
	return fsatomic.WriteJSON(s.path, prodStateFile{Records: cur})
}
