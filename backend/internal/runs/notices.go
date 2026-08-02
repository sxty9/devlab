package runs

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"

	"devlab/backend/internal/fsatomic"
	"devlab/backend/internal/statepath"
)

// A Notice is ONE persistent hint the service raises about itself (S14): a delivery alarm, a
// blockade, a rescued orphan, an automatic axiom assignment, a protection deviation, an
// emergency-route override, a deferred restart. It stays until it is read (REQ-031.2), and an
// unchanged repetition is bundled into the existing record with a count and a period
// (REQ-032.5) instead of drowning the feed in identical rows.
//
// The store below is a pure, passive pool: it holds these records, bundles unchanged repeats
// and hands them out — it never decides what a finding means, who should see it, or whether it
// is serious. Every evaluation (the notice panel, the daily report's rubrics, the self-check)
// happens outside the pool.
type Notice struct {
	ID string `json:"id"`
	// At is the first occurrence — kept alongside FirstAt because it is the field the wire
	// shape has always carried.
	At   time.Time `json:"at"`
	Kind string    `json:"kind"`

	// The finding itself (model.Notice): which repository it concerns, what happened, and the
	// next step that resolves it.
	Repo     string `json:"repo,omitempty"`
	Text     string `json:"text,omitempty"`
	NextStep string `json:"nextStep,omitempty"`

	// Bundling (REQ-032.5): how often the unchanged message occurred and over which period.
	Count   int       `json:"count"`
	FirstAt time.Time `json:"firstAt"`
	LastAt  time.Time `json:"lastAt"`
	// Read is the acknowledgment. A notice remains unread — and thus keeps nagging — until the
	// user dismisses it; a fresh occurrence re-opens it.
	Read bool `json:"read"`

	// The automatic axiom→run assignment records its own facts (REQ-004.5): which run took the
	// axioms, whether it was created for them, and the axioms themselves (ids plus a title
	// snapshot, so the feed reads without a live lookup).
	RunID    string   `json:"runId,omitempty"`
	RunName  string   `json:"runName,omitempty"`
	NewRun   bool     `json:"newRun,omitempty"`
	AxiomIDs []string `json:"axiomIds"`
	Axioms   []string `json:"axioms"`

	// Reason is the short, human reason (never a raw log). It is the older name for Text and
	// stays because callers write into it; Message reads whichever is set, so a consumer never
	// has to know which.
	Reason string `json:"reason,omitempty"`
}

// The notice kinds. A kind is wire vocabulary: it labels the finding in the pool, in the notice
// panel and in the daily report's rubrics, so it belongs in ONE place. Every raising package
// names one of these.
const (
	// The automatic axiom→run assignment (REQ-004.5).
	NoticeAssigned = "assigned"
	NoticeFailed   = "failed"
	// Delivery findings: work that did not reach the user (REQ-031/REQ-032, REQ-028.4).
	NoticeDeliveryAlarm      = "delivery-alarm"
	NoticeDeliveryBlocked    = "delivery-blocked"
	NoticeDeliverySelfCheck  = "delivery-selfcheck"
	NoticeStructureViolation = "code-structure-violation"
	// The standstill of the delivery maintenance: its writing half is not armed, so nothing is
	// merged, pruned or stamped. A STATE the operator ends, and the reason nothing moves.
	NoticeDeliveryHeld = "delivery-held"
	// The shared, self-ending standstill of an exhausted GitHub request budget: a property of the
	// whole request window, not of any single pull request, so nothing is blocked and it ends by
	// itself once the window refills.
	NoticeGitHubQuota = "github-quota"
	// Delivery-origin findings (REQ-033): a protection that drifted, and a deliberate override.
	NoticeProtectionDeviation = "protection-deviation"
	NoticeAdminOverride       = "admin-override"
)

// Message is the notice's display text: Text when set, else Reason. Repo, when present, is a
// separate field — callers that fold it into the text keep doing so and are not double-prefixed
// here.
func (n Notice) Message() string {
	if t := strings.TrimSpace(n.Text); t != "" {
		return t
	}
	return strings.TrimSpace(n.Reason)
}

// CoalesceKey identifies "the same message again": same kind, same repository, same wording,
// same next step. Anything that differs — a new reason, another repository, a changed next
// step — is a DIFFERENT finding and gets its own record. The assignment facts take part too, so
// two assignments of different axioms never collapse into one.
func (n Notice) CoalesceKey() string {
	return strings.Join([]string{
		n.Kind, n.Repo, n.Message(), n.NextStep, n.RunID, strings.Join(n.AxiomIDs, ","),
	}, "\x00")
}

// noticeCap bounds the pool so the feed stays portioned: only the newest noticeCap notices are
// kept. Bundling is what keeps a recurring problem inside that bound — a repeat moves its
// record back to the front instead of consuming another slot.
const noticeCap = 50

// NoticeStore persists the notice pool (a small JSON file, same discipline as the runs and PR
// stores: atomic tmp+rename, 0600, missing → empty).
type NoticeStore struct {
	path string
	mu   sync.Mutex
	// onNew is fired ONCE per genuinely new record — after it is persisted, never for a coalesced
	// repeat — so a consumer can react to a finding the pool has not held before (the notice
	// dispatcher delivers it outward). It reports a STRUCTURAL fact of the write (append vs
	// coalesce), not an interpretation of what the notice means, so the pool stays passive: every
	// evaluation of the record happens inside the hook, outside the pool. Optional; nil = nobody
	// listens.
	onNew func(Notice)
}

// SetOnNew installs the hook fired once per genuinely new record (see the field). Wired at boot; a
// second call replaces the first. Concurrency-safe.
func (s *NoticeStore) SetOnNew(f func(Notice)) {
	s.mu.Lock()
	s.onNew = f
	s.mu.Unlock()
}

func NewNoticeStore(p *statepath.Paths) *NoticeStore { return &NoticeStore{path: noticesPath(p)} }

func noticesPath(p *statepath.Paths) string {
	if v := os.Getenv("DEVLAB_MERCURY_RUNS_NOTICES"); v != "" {
		return v
	}
	if p != nil {
		return p.NoticesFile()
	}
	return ""
}

// NewNoticeID mints an unguessable notice id (front-matter of the feed file, never a path segment).
func NewNoticeID() string {
	var b [10]byte
	_, _ = rand.Read(b[:])
	return "ntc_" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:]))
}

type noticeFile struct {
	Notices []Notice `json:"notices"`
}

func (s *NoticeStore) load() ([]Notice, error) {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var f noticeFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	out := f.Notices
	for i := range out {
		normalizeNotice(&out[i], out[i].At) // legacy rows predate count/period
	}
	return out, nil
}

// List returns all notices, newest first (missing store → empty, never nil).
func (s *NoticeStore) List() ([]Notice, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.load()
	if err != nil {
		return []Notice{}, err
	}
	if cur == nil {
		return []Notice{}, nil
	}
	return cur, nil
}

// Coalesce records ONE occurrence of a notice and returns the stored record.
//
// It is the pool's single write path: an unchanged repetition (same CoalesceKey) bundles into
// the existing record — the count grows, the period stretches to the new occurrence, the record
// moves back to the front and counts as unread again, because a problem that is still happening
// must still be seen. Anything else is appended as a new record, and the pool is trimmed to its
// cap so the feed stays portioned. The store owns identity (id) and, when the caller supplies
// none, the timestamps; the caller supplies only the meaning.
func (s *NoticeStore) Coalesce(n Notice) (Notice, error) {
	stored, created, err := s.write(n)
	// The hook fires OUTSIDE the store lock and ONLY for a genuinely new record: a coalesced repeat
	// (same finding again) updates the existing record and delivers nothing further, so a fault
	// recurring every 20s reaches the user once and is only bundled afterwards.
	if err == nil && created {
		s.fireNew(stored)
	}
	return stored, err
}

// write is the locked half of Coalesce: it records one occurrence and reports whether it appended a
// NEW record (created=true) or bundled an unchanged repeat onto an existing one (created=false).
func (s *NoticeStore) write(n Notice) (Notice, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.load()
	if err != nil {
		return Notice{}, false, err
	}
	normalizeNotice(&n, time.Now().UTC())

	key := n.CoalesceKey()
	for i := range cur {
		if cur[i].CoalesceKey() != key {
			continue
		}
		merged := cur[i]
		merged.Count += n.Count
		if n.FirstAt.Before(merged.FirstAt) {
			merged.FirstAt = n.FirstAt
		}
		if n.LastAt.After(merged.LastAt) {
			merged.LastAt = n.LastAt
		}
		merged.At = merged.FirstAt
		merged.Read = false
		next := make([]Notice, 0, len(cur))
		next = append(next, merged)
		next = append(next, cur[:i]...)
		next = append(next, cur[i+1:]...)
		return merged, false, s.save(next)
	}

	next := append([]Notice{n}, cur...)
	if len(next) > noticeCap {
		next = next[:noticeCap]
	}
	return n, true, s.save(next)
}

// fireNew invokes the on-new hook, if one is installed, reading it under the lock but calling it
// released so a slow consumer never holds up the pool.
func (s *NoticeStore) fireNew(n Notice) {
	s.mu.Lock()
	f := s.onNew
	s.mu.Unlock()
	if f != nil {
		f(n)
	}
}

// Add records one occurrence. It is the name the raising packages call and does exactly what
// Coalesce does — one write path, two spellings of it, never two behaviors.
func (s *NoticeStore) Add(n Notice) error {
	_, err := s.Coalesce(n)
	return err
}

// normalizeNotice fills what the store owns: identity, a count of at least one, and a period.
// A caller may pin the times (tests, imports); otherwise the occurrence is now. Nil slices
// become empty so a reader never faces a JSON null.
func normalizeNotice(n *Notice, now time.Time) {
	if n.ID == "" {
		n.ID = NewNoticeID()
	}
	if n.Count <= 0 {
		n.Count = 1
	}
	ts := now
	switch {
	case !n.LastAt.IsZero():
		ts = n.LastAt
	case !n.At.IsZero():
		ts = n.At
	case !n.FirstAt.IsZero():
		ts = n.FirstAt
	}
	if n.FirstAt.IsZero() {
		n.FirstAt = ts
	}
	if n.LastAt.IsZero() {
		n.LastAt = ts
	}
	if n.At.IsZero() {
		n.At = n.FirstAt
	}
	if n.AxiomIDs == nil {
		n.AxiomIDs = []string{}
	}
	if n.Axioms == nil {
		n.Axioms = []string{}
	}
}

// Dismiss marks one notice read — the acknowledgment that ends the nagging (idempotent; an
// unknown id is a no-op). The record itself stays: a later occurrence of the same finding
// bundles onto it and re-opens it, so its count and period are never silently restarted.
func (s *NoticeStore) Dismiss(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.load()
	if err != nil {
		return err
	}
	for i := range cur {
		if cur[i].ID == id {
			cur[i].Read = true
		}
	}
	return s.save(cur)
}

// Clear empties the feed — the explicit "I have seen all of this" that also drops the records.
func (s *NoticeStore) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.save(nil)
}

func (s *NoticeStore) save(notices []Notice) error {
	if notices == nil {
		notices = []Notice{}
	}
	return fsatomic.WriteJSON(s.path, noticeFile{Notices: notices})
}
