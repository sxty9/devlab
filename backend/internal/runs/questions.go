// The blocking-question pool (the Blocked surface): the passive record of every question a run
// raised because it would not decide for itself and would not guess. A question holds its
// repository exactly like a failed delivery tip — no new order branches past an open question — and
// once the user answers, the answer is fed back into the SAME agent conversation so the run
// continues where it stopped instead of starting over.
//
// The pool is passive (Passive Speicher): it holds and hands back questions, it never decides what a
// question means. It owns identity and, unless a caller pins them, the timestamps; a structural
// on-new hook lets the owner deliver a new question outward without the pool interpreting anything.
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
	"devlab/backend/internal/model"
	"devlab/backend/internal/statepath"
)

// Question kinds — WHAT a run stopped to ask about. The kind is descriptive, never a switch the
// pool acts on; it lets the surface tell a routine design decision apart from the one privileged
// case that moves a security boundary (the root-wrapper renewal).
const (
	// QuestionDecision is an architecture or design fork the run would not settle on its own.
	QuestionDecision = "decision"
	// QuestionWrapperRenewal is the ONE guarded handle: the run proposes renewing the root wrapper
	// scripts under /usr/local/sbin and waits for the user's explicit, single-use approval. Detail
	// carries the exact difference to the installed scripts.
	QuestionWrapperRenewal = "wrapper-renewal"
	// QuestionProdHostKey is the SECOND guarded handle, the same shape as the wrapper renewal: the
	// production target presented a CHANGED ssh host key (a reinstall, or an interception), so every
	// production send is held until the user deliberately approves the new key. Approved frees the
	// acceptance of exactly the fingerprint pinned in HostKeyFingerprint and nothing else — the accept
	// path re-reads the key at apply time and refuses if it changed again (content-pin, single-use).
	// It is a production-only hold: unlike a wrapper-renewal or decision question it does NOT hold a
	// repository's dev branch (a production failure never blocks the stack), so OpenForRepo skips it.
	QuestionProdHostKey = "prod-host-key"
)

// Wrapper renewal SOURCES — where the approved content is re-read from at install time. Both flow
// through the SAME question kind, the SAME approval and the SAME root write path; only the source of
// the offered bytes differs, so a run that changes a root script asks the very question a drifted
// install already did, never a second kind of question (task point 2).
const (
	// WrapperSourceStackTip renews from the STACK TIP (deliver.NextPRBase) — another open delivery's
	// branch when one is open, else the standard branch. The installed scripts drifted from that stand
	// (a wrapper change that reached the stack but not yet /usr/local/sbin). This is the default when the
	// field is empty, and it also covers the retired "merged" value persisted by an in-flight question.
	WrapperSourceStackTip = "stacktip"
	// WrapperSourceWorking renews from THIS run's own delivering branch — the run itself changed a root
	// script that is not yet merged. The offered content is the run's, pinned to its sha256; the human
	// is the gate instead of the merge, and the four bindings (sha, single-use, run-unwritable
	// approval, re-read-after-approval) keep the root boundary exactly where it was.
	WrapperSourceWorking = "working"
)

// Question is one open (or answered) question of one run at one repository.
type Question struct {
	ID          string        `json:"id"`
	RunID       string        `json:"runId"`
	RunTitle    string        `json:"runTitle,omitempty"`
	Kind        model.RunKind `json:"kind,omitempty"`
	ExecutionID string        `json:"executionId"`
	Repo        string        `json:"repo"`

	// QKind names WHAT was asked (QuestionDecision | QuestionWrapperRenewal).
	QKind string `json:"qKind"`
	// Autonomy is the level the raising run worked at — the level whose rule let it stop and ask.
	Autonomy model.AutonomyLevel `json:"autonomy,omitempty"`

	// Question is the wording the user reads and answers. Recommendation is the run's OWN proposed
	// answer (never absent for a well-formed question); Progress is what the run did before it
	// stopped; Detail carries extra material a specific kind needs (the wrapper difference).
	Question       string `json:"question"`
	Recommendation string `json:"recommendation,omitempty"`
	Progress       string `json:"progress,omitempty"`
	Detail         string `json:"detail,omitempty"`

	// AskedAt is when the question was raised; the surface shows "waiting since" from it (REQ:
	// an unanswered question names since when it waits).
	AskedAt time.Time   `json:"askedAt"`
	AskedBy model.Actor `json:"askedBy"`

	// Answer is the user's reply once given. Approved is the explicit green light for a guarded
	// handle (wrapper renewal): a question of that kind only frees its action when Approved is true.
	Answer     string      `json:"answer,omitempty"`
	Approved   bool        `json:"approved,omitempty"`
	AnsweredAt *time.Time  `json:"answeredAt,omitempty"`
	AnsweredBy model.Actor `json:"answeredBy,omitempty"`

	// Wrappers pins EXACTLY WHICH root wrapper scripts (by name) and WHICH content (by sha256) the
	// user's approval covers — one entry per file. It is set only on a wrapper-renewal question and is
	// derived from committed history (the stack tip or the delivering branch, per WrapperSource), never
	// from an unpinned working tree. The approval frees the renewal of these named files with these
	// exact checksums and nothing else: if the source content changes after the approval, the recorded
	// sha no longer matches and the renewal is refused (single-use and content-pinned, never blanket).
	Wrappers []WrapperGrant `json:"wrappers,omitempty"`

	// HostKeyTarget and HostKeyFingerprint pin a QuestionProdHostKey to ONE production host and ONE
	// key: Target is the host whose key changed; Fingerprint is the SHA256 of the key now presented,
	// the exact key the approval covers. The accept path re-reads the host's current key and installs it
	// ONLY when it still hashes to this fingerprint (content-pin), so an approval never carries over to a
	// key that changed again after the human looked. Set only on a host-key question.
	HostKeyTarget      string `json:"hostKeyTarget,omitempty"`
	HostKeyFingerprint string `json:"hostKeyFingerprint,omitempty"`

	// WrapperSource names WHERE the approved wrapper content is re-read from at install time — the stack
	// tip (WrapperSourceStackTip) or this run's own delivering branch (WrapperSourceWorking). It is set
	// only on a wrapper-renewal question; an empty value (or the retired "merged") means the stack tip.
	// The write half re-reads the bytes from exactly this source and re-checks their sha256, so a change
	// to the source after the approval installs nothing.
	WrapperSource string `json:"wrapperSource,omitempty"`

	// Resolved marks a question whose answer the resumed run has already consumed, so a later resume
	// does not feed the same answer to the agent twice.
	Resolved   bool       `json:"resolved,omitempty"`
	ResolvedAt *time.Time `json:"resolvedAt,omitempty"`
}

// WrapperGrant names ONE root wrapper the user approved for renewal and the exact content (sha256)
// the approval covers. The bytes are not stored here — they are re-read from the standard branch at
// install time and must still hash to SHA, so a stale approval installs nothing.
type WrapperGrant struct {
	Name string `json:"name"` // one of the four renewable wrappers (the authoritative list lives in the root tool)
	SHA  string `json:"sha"`  // sha256 (hex) of the content the user approved (merged or working-branch)
	// Summary is a SHORT, human-readable description of what the renewal changes in this file (e.g.
	// "+12/-3 lines vs the installed script"). It is display-only — the approval is bound solely by
	// Name and SHA — and lets a human decide without opening the branch.
	Summary string `json:"summary,omitempty"`
}

// Open reports whether the question still waits for an answer — it carries no answer AND has not
// been withdrawn. A withdrawn question (Resolved with no answer, superseded by a fresh one) no
// longer holds its repository, so the surface and the branch-halt both stop counting it.
func (q Question) Open() bool { return q.AnsweredAt == nil && !q.Resolved }

// Answered reports whether the question carries the user's answer but the run has not yet consumed
// it — the state that feeds the resumed agent session.
func (q Question) Answered() bool { return q.AnsweredAt != nil && !q.Resolved }

// QuestionStore is the passive pool of blocking questions — one JSON file under a mutex.
type QuestionStore struct {
	path  string
	mu    sync.Mutex
	onNew func(Question)
}

// NewQuestionStore builds the pool below the given state root.
func NewQuestionStore(p *statepath.Paths) *QuestionStore {
	return &QuestionStore{path: questionsPath(p)}
}

func questionsPath(p *statepath.Paths) string {
	if v := os.Getenv("DEVLAB_MERCURY_RUNS_QUESTIONS"); v != "" {
		return v
	}
	if p != nil {
		return p.QuestionsFile()
	}
	return ""
}

// SetOnNew installs the hook fired once per genuinely new question, released of the lock so a slow
// consumer never holds up the pool. Wired at boot; a second call replaces the first.
func (s *QuestionStore) SetOnNew(f func(Question)) {
	s.mu.Lock()
	s.onNew = f
	s.mu.Unlock()
}

// NewQuestionID mints an unguessable question id.
func NewQuestionID() string {
	var b [10]byte
	_, _ = rand.Read(b[:])
	return "qst_" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:]))
}

type questionFile struct {
	Questions []Question `json:"questions"`
}

func (s *QuestionStore) load() ([]Question, error) {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var f questionFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	return f.Questions, nil
}

// List returns all questions, newest first (missing store → empty, never nil).
func (s *QuestionStore) List() ([]Question, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.load()
	if err != nil {
		return []Question{}, err
	}
	out := make([]Question, 0, len(cur))
	// Newest first: the pool appends, so reverse for the surface.
	for i := len(cur) - 1; i >= 0; i-- {
		out = append(out, cur[i])
	}
	return out, nil
}

// Get returns one question by id.
func (s *QuestionStore) Get(id string) (Question, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.load()
	if err != nil {
		return Question{}, false, err
	}
	for _, q := range cur {
		if q.ID == id {
			return q, true, nil
		}
	}
	return Question{}, false, nil
}

// OpenForRepo returns the oldest still-open question that holds this repository, raised by an ORDER
// OTHER than exceptRunID — the halt the implement stage runs before it branches (the question mirror
// of a failed delivery tip). The exclusion is by RUN, not by execution: a question belongs to its
// ORDER, so a later execution of the same order (after a restart) is never held by a question one of
// the order's earlier executions raised — it resumes and reads the answer instead of deadlocking
// behind it. nil when nothing holds the repo.
func (s *QuestionStore) OpenForRepo(repo, exceptRunID string) (*Question, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.load()
	if err != nil {
		return nil, err
	}
	for i := range cur {
		q := cur[i]
		// A host-key question holds the PRODUCTION send, not a dev branch: a production failure never
		// blocks the stack (WHAT-3), so it must not halt new orders branching on this repository.
		if q.QKind == QuestionProdHostKey {
			continue
		}
		if q.Open() && sameRepoID(q.Repo, repo) && q.RunID != exceptRunID {
			return &q, nil
		}
	}
	return nil, nil
}

// OpenHostKeyQuestion returns the oldest still-open host-key question for a production target, or nil.
// The production pass calls it to avoid raising a second question while one already waits for the same
// host: the changed key is a property of the HOST, not of any one delivery, so it is asked once.
func (s *QuestionStore) OpenHostKeyQuestion(target string) (*Question, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.load()
	if err != nil {
		return nil, err
	}
	for i := range cur {
		if cur[i].Open() && cur[i].QKind == QuestionProdHostKey && cur[i].HostKeyTarget == target {
			q := cur[i]
			return &q, nil
		}
	}
	return nil, nil
}

// ApprovedHostKeyQuestion returns the newest ANSWERED-and-APPROVED, not-yet-consumed host-key question
// for a production target — the approval the production pass redeems to accept the new key. An answered
// question that was NOT approved (the user declined) is not returned: nothing is accepted without an
// explicit green light. nil when none waits.
func (s *QuestionStore) ApprovedHostKeyQuestion(target string) (*Question, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.load()
	if err != nil {
		return nil, err
	}
	var found *Question
	for i := range cur {
		if cur[i].Answered() && cur[i].Approved && cur[i].QKind == QuestionProdHostKey && cur[i].HostKeyTarget == target {
			q := cur[i]
			found = &q
		}
	}
	return found, nil
}

// AnsweredForRun returns the ORDER's own answered, not-yet-consumed question for the repo — the
// answer a resuming execution redeems. The answer belongs to the order and its subject, not to the
// single execution that first asked: a later execution (after a restart that minted a NEW execution
// id) looks the answer up by run id and redeems it, as long as its bindings still hold. For a
// wrapper-renewal approval those bindings are re-checked at install time (the source content must
// still hash to the approved checksum), so a stale approval installs nothing. The newest matching
// answer wins. nil when none waits.
func (s *QuestionStore) AnsweredForRun(runID, repo string) (*Question, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.load()
	if err != nil {
		return nil, err
	}
	var found *Question
	for i := range cur {
		if cur[i].Answered() && cur[i].RunID == runID && sameRepoID(cur[i].Repo, repo) {
			q := cur[i]
			found = &q
		}
	}
	return found, nil
}

// WithdrawForRun marks every not-yet-consumed question this ORDER raised for the repo as resolved —
// OPEN or ANSWERED, optionally narrowed to one kind (qKind == "" means any kind). Raising a fresh
// question calls it first, so an order never accumulates two live questions for the same repository:
// a question left open by a since-ended execution, or an answered one whose action did not take, is
// retired here rather than lingering as a second, un-redeemable blocker. Returns how many it
// withdrew (0 → nothing written).
func (s *QuestionStore) WithdrawForRun(runID, repo, qKind string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.load()
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	n := 0
	for i := range cur {
		if cur[i].Resolved || cur[i].RunID != runID || !sameRepoID(cur[i].Repo, repo) {
			continue
		}
		if qKind != "" && cur[i].QKind != qKind {
			continue
		}
		cur[i].Resolved = true
		cur[i].ResolvedAt = &now
		n++
	}
	if n == 0 {
		return 0, nil
	}
	if err := s.save(cur); err != nil {
		return 0, err
	}
	return n, nil
}

// Raise records a new open question and fires the on-new hook once (outside the lock). The store
// owns identity and the ask-time; the caller supplies the meaning.
func (s *QuestionStore) Raise(q Question) (Question, error) {
	s.mu.Lock()
	if q.ID == "" {
		q.ID = NewQuestionID()
	}
	if q.AskedAt.IsZero() {
		q.AskedAt = time.Now().UTC()
	}
	cur, err := s.load()
	if err != nil {
		s.mu.Unlock()
		return Question{}, err
	}
	if err := s.save(append(cur, q)); err != nil {
		s.mu.Unlock()
		return Question{}, err
	}
	f := s.onNew
	s.mu.Unlock()
	if f != nil {
		f(q)
	}
	return q, nil
}

// Answer records the user's reply on one open question (ErrNotFound if unknown, a no-op if already
// answered — an answer is not overwritten). approved is the single-use green light a guarded handle
// needs; it is meaningless for a plain decision and simply stored.
func (s *QuestionStore) Answer(id, answer string, approved bool, by model.Actor) (Question, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.load()
	if err != nil {
		return Question{}, err
	}
	for i := range cur {
		if cur[i].ID != id {
			continue
		}
		if !cur[i].Open() {
			return cur[i], nil
		}
		now := time.Now().UTC()
		cur[i].Answer = answer
		cur[i].Approved = approved
		cur[i].AnsweredAt = &now
		cur[i].AnsweredBy = by
		if err := s.save(cur); err != nil {
			return Question{}, err
		}
		return cur[i], nil
	}
	return Question{}, ErrNotFound
}

// Resolve marks an answered question consumed by the resumed run (idempotent; unknown id is a
// no-op), so a later resume does not re-inject the same answer.
func (s *QuestionStore) Resolve(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.load()
	if err != nil {
		return err
	}
	for i := range cur {
		if cur[i].ID == id && !cur[i].Resolved {
			now := time.Now().UTC()
			cur[i].Resolved = true
			cur[i].ResolvedAt = &now
			return s.save(cur)
		}
	}
	return nil
}

func (s *QuestionStore) save(qs []Question) error {
	if qs == nil {
		qs = []Question{}
	}
	return fsatomic.WriteJSON(s.path, questionFile{Questions: qs})
}

// sameRepoID compares two repo references, tolerating the GitHub full name and the bare id — the
// same two forms the delivery ledger carries.
func sameRepoID(a, b string) bool {
	if a == b {
		return true
	}
	return repoShortID(a) == repoShortID(b)
}

func repoShortID(full string) string {
	if i := strings.LastIndexByte(full, '/'); i >= 0 {
		return full[i+1:]
	}
	return full
}
