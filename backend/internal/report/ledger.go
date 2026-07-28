// Package report delivers Mercury's daily run report: at the close of a day on which at least one
// execution ran, it mails the run owner a summary of what the runs did. The Ledger below is the
// passive record of which day-reports have been delivered; the Reporter (reporter.go) holds all the
// deciding logic. Keeping the store passive — it holds records, it does not decide when to send or
// what a report contains — is what lets the once-per-day, retry-until-delivered guarantees be
// reasoned about (and tested) in one place.
package report

import (
	"encoding/json"
	"os"
	"sync"
	"time"

	"devlab/backend/internal/fsatomic"

	"devlab/backend/internal/statepath"
)

// Status is the delivery state of one day's report to one recipient.
type Status string

const (
	// StatusSent means the report for this (recipient, day) was accepted by the mail service. A day
	// in this state is sealed: it is never composed or sent again, so a restart, a second trigger, or
	// a later-finishing run cannot produce a second email for the same day.
	StatusSent Status = "sent"
	// StatusFailed means a send was attempted and rejected/errored. It is deliberately visible (it
	// carries the error and the attempt count) and is retried on the next pass — without duplicating,
	// because success flips it to StatusSent and a sealed day is never retried.
	StatusFailed Status = "failed"
)

// Record is the ledger entry for one (Recipient, Day). Day is a calendar date "YYYY-MM-DD" in the
// server's report timezone — the unit the "at most one email per day and recipient" rule counts in.
type Record struct {
	Recipient   string     `json:"recipient"`
	Day         string     `json:"day"`
	Status      Status     `json:"status"`
	Executions  int        `json:"executions"`            // how many executions the report covered
	Attempts    int        `json:"attempts"`              // total send attempts (successful or not)
	SentAt      *time.Time `json:"sentAt,omitempty"`      // set once StatusSent
	LastAttempt *time.Time `json:"lastAttempt,omitempty"` // when the most recent attempt ran
	LastError   string     `json:"lastError,omitempty"`   // the most recent failure reason (cleared on success)
}

// key is the ledger's primary key. A recipient username never contains NUL, so it is a safe joiner.
func key(recipient, day string) string { return recipient + "\x00" + day }

type file struct {
	Records []Record `json:"records"`
}

// Ledger is the single source of truth for delivered/failed day-reports: one JSON file, mutated under
// a mutex with an atomic tmp+rename write (0600, missing file → empty), mirroring runs.Store.
type Ledger struct {
	path string
	mu   sync.Mutex
}

// NewLedger builds the ledger below the state root (DEVLAB_MERCURY_REPORTS overrides — a
// ported test seam).
func NewLedger(p *statepath.Paths) *Ledger { return &Ledger{path: ledgerPath(p)} }

// NewLedgerAt builds a ledger backed by an explicit path (tests).
func NewLedgerAt(path string) *Ledger { return &Ledger{path: path} }

func ledgerPath(p *statepath.Paths) string {
	if v := os.Getenv("DEVLAB_MERCURY_REPORTS"); v != "" {
		return v
	}
	if p != nil {
		return p.DailyReports()
	}
	return ""
}

func (l *Ledger) load() ([]Record, error) {
	b, err := os.ReadFile(l.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	return f.Records, nil
}

// List returns every record (missing store → empty). Copied out, safe for the caller to read.
func (l *Ledger) List() ([]Record, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.load()
}

// Get returns the record for one (recipient, day), and whether it exists.
func (l *Ledger) Get(recipient, day string) (Record, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	recs, err := l.load()
	if err != nil {
		return Record{}, false, err
	}
	want := key(recipient, day)
	for _, r := range recs {
		if key(r.Recipient, r.Day) == want {
			return r, true, nil
		}
	}
	return Record{}, false, nil
}

// Put upserts a record by (Recipient, Day), atomically. It is the only write path — every state
// transition (a fresh failure, a retry, a success) goes through here so the file never holds two
// entries for the same day.
func (l *Ledger) Put(rec Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	recs, err := l.load()
	if err != nil {
		return err
	}
	want := key(rec.Recipient, rec.Day)
	replaced := false
	for i := range recs {
		if key(recs[i].Recipient, recs[i].Day) == want {
			recs[i] = rec
			replaced = true
			break
		}
	}
	if !replaced {
		recs = append(recs, rec)
	}
	return l.save(recs)
}

func (l *Ledger) save(recs []Record) error {
	return fsatomic.WriteJSON(l.path, file{Records: recs})
}
