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
	// QuestionProdReceiver is the THIRD guarded handle, the same Blocked/approval shape as the two above:
	// a devlab (self) delivery whose merged content changes the ROOT RECEIVER SCRIPTS that live on the
	// production host (devlab-deploy-recv and the library it sources, devlab-setup-lib.sh) cannot install
	// them over the chain — the deploy key is a forced command that can only rsync into staging and
	// trigger an install, never overwrite its own gatekeeper. That part of the delivery therefore does
	// NOT reach the host, so the delivery must not settle live; the outstanding step is surfaced here as a
	// concrete approval carrying the exact command an operator with root runs on the target and the
	// checksums the merged scripts must reach (Wrappers). Like the host-key hold it is production-only
	// (OpenForRepo skips it) and, unlike the wrapper renewal, the chain never installs it itself — it
	// deliberately lacks the right — so an approval only records that the operator ran the command; the
	// production pass re-MEASURES the host's receiver checksums and settles the delivery live only once
	// they match (approval is never a bypass of the measurement).
	QuestionProdReceiver = "prod-receiver"
)

// Wrapper renewal SOURCE — where the approved content is re-read from at install time. There is now
// exactly ONE: the branch this run delivers, which the self-delivery gate proves the install against.
// An approval that installs that content therefore always resolves the gate, so no second source can
// contradict it (task point 2 — an earlier design also offered the stack tip and drove a pendulum).
const (
	// WrapperSourceWorking renews from THIS run's own delivering branch — the sole source. Its content is
	// what deliver-dev is measured against (a change the run itself made, or a stacked change it inherited
	// from the branch's base); pinned to its sha256, with the human as the gate instead of the merge, and
	// the four bindings (sha, single-use, run-unwritable approval, re-read-after-approval) keeping the
	// root boundary exactly where it was.
	WrapperSourceWorking = "working"
	// WrapperSourceStackTip is the RETIRED stack-tip source. No question is raised with it any more; it
	// remains named only so the write half recognises an in-flight question persisted before the change —
	// which it renews from the delivering branch regardless, refusing on a checksum mismatch (so a stale
	// stack-tip approval installs nothing and the delivery re-asks with the delivering-branch content).
	WrapperSourceStackTip = "stacktip"
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

	// Wrappers pins EXACTLY WHICH root scripts (by name) and WHICH content (by sha256) the user's
	// approval covers — one entry per file. On a wrapper-renewal question it names the root wrappers under
	// /usr/local/sbin, derived from committed history (the delivering branch, per WrapperSource) and
	// re-read and re-checked at install time, so a stale approval installs nothing (single-use,
	// content-pinned). On a QuestionProdReceiver it names the root receiver scripts on the production host
	// and the sha256 each must reach — the "checksums that bind it"; there the chain never installs them
	// (it lacks the right), so the pins are what the operator's command brings the host to and what the
	// production pass re-measures against before it settles the delivery live.
	Wrappers []WrapperGrant `json:"wrappers,omitempty"`

	// HostKeyTarget and HostKeyFingerprint pin a QuestionProdHostKey to ONE production host and ONE
	// key: Target is the host whose key changed; Fingerprint is the SHA256 of the key now presented,
	// the exact key the approval covers. The accept path re-reads the host's current key and installs it
	// ONLY when it still hashes to this fingerprint (content-pin), so an approval never carries over to a
	// key that changed again after the human looked. Set only on a host-key question.
	HostKeyTarget      string `json:"hostKeyTarget,omitempty"`
	HostKeyFingerprint string `json:"hostKeyFingerprint,omitempty"`

	// ProdReceiverTarget and ProdReceiverCommand belong to a QuestionProdReceiver: the production host
	// whose root receiver scripts are older than the merged delivery ships, and the exact one-line command
	// an operator with root runs ON THAT HOST to bring them current. The scripts and the exact sha256 each
	// must reach are pinned in Wrappers. The chain never installs these itself (it deliberately lacks the
	// right), so the command is Handarbeit for a human — the chain re-measures the host afterwards.
	ProdReceiverTarget  string `json:"prodReceiverTarget,omitempty"`
	ProdReceiverCommand string `json:"prodReceiverCommand,omitempty"`

	// WrapperSource names where the approved wrapper content is re-read from at install time. A question
	// raised today always sets WrapperSourceWorking — this run's own delivering branch, the ONE stand the
	// self-delivery gate proves against. The retired WrapperSourceStackTip (and an empty value) may still
	// appear on a question persisted before the change; the write half re-reads from the delivering branch
	// regardless and re-checks the sha256, so a stale stack-tip approval installs nothing and re-asks.
	WrapperSource string `json:"wrapperSource,omitempty"`

	// ApprovalStatement is the EXACT sentence a user affirms to approve a guarded question, derived
	// wholly from that question's own subject (GuardedApprovalStatement). It is NOT persisted with the
	// question — it is filled in on read so the surface always shows the current derivation, and it is
	// the SAME sentence recorded as the answer when the approval is given, so what a human reads while
	// consenting and what stands in the ledger afterwards are one and the same wording. Empty for a plain
	// decision (answered with free text, no fixed consent).
	ApprovalStatement string `json:"approvalStatement,omitempty"`

	// Resolved marks a question whose answer the resumed run has already consumed, so a later resume
	// does not feed the same answer to the agent twice.
	Resolved   bool       `json:"resolved,omitempty"`
	ResolvedAt *time.Time `json:"resolvedAt,omitempty"`

	// Declined marks the question the user REJECTED — the co-equal "no". A rejection is a full,
	// always-available answer, not a missing one: it resolves the question (Open()/Answered() both
	// false, so it feeds no resume and holds no repository) and NAMES the rejection as the reason its
	// run ended. It never approves, never installs and never continues — the safe exit that leaves the
	// state exactly as it was before the question. DeclinedBy records who rejected it.
	Declined   bool        `json:"declined,omitempty"`
	DeclinedBy model.Actor `json:"declinedBy,omitempty"`

	// Moot marks a question closed because its RUN no longer exists — an order that was removed while
	// its question still stood open. Such a question is neither sensibly approvable (no branch is
	// carried forward) nor rejectable-with-effect (there is no run to end); it is gegenstandslos. It is
	// resolved so it holds nothing and drops off the surface. The set of runs that still exist is
	// decided by the OWNER and handed to the pool; the pool never queries other stores itself.
	Moot bool `json:"moot,omitempty"`

	// Ended marks a question closed because the ORDER it belongs to has FINISHED — the run still exists
	// but reached its goal or was abandoned, so no execution will act on the question again and its
	// answer can no longer take effect ("Endet der Vorgang, zu dem eine Frage gehört, kann ihre
	// Beantwortung keine Wirkung mehr entfalten"). Unlike Moot the run is not gone; unlike Declined the
	// user did not reject it — it simply outlived its order. It is resolved so it holds nothing and drops
	// off the surface. The OWNER decides which orders have ended and hands the set to the pool.
	Ended bool `json:"ended,omitempty"`

	// CloseNote is the human-readable reason a question closed WITHOUT an effective answer — the
	// rejection, or the fact that its run is gone. Display-only; the pool never acts on it.
	CloseNote string `json:"closeNote,omitempty"`
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

// GuardedApprovalStatement is the ONE source of the consent wording for a guarded question (task
// points 1 & 3): the exact sentence the user affirms when approving, and the exact sentence recorded
// as the answer afterwards. It is derived wholly from the question's OWN subject — never formulated a
// second time beside it — so the consent can never describe something other than what the approval
// actually frees. A wrapper renewal names WHICH version is installed and binds each file to its exact
// checksum; a host-key names the target host and the exact key fingerprint. A plain decision has no
// fixed consent, so this returns "" and the question is answered with free text.
func (q Question) GuardedApprovalStatement() string {
	switch q.QKind {
	case QuestionWrapperRenewal:
		return wrapperApprovalStatement(q.Wrappers)
	case QuestionProdHostKey:
		return hostKeyApprovalStatement(q.HostKeyTarget, q.HostKeyFingerprint)
	case QuestionProdReceiver:
		return prodReceiverApprovalStatement(q.ProdReceiverTarget, q.ProdReceiverCommand, q.Wrappers)
	default:
		return ""
	}
}

// prodReceiverApprovalStatement spells out, for the human and for the ledger, WHICH production host and
// WHICH exact command an approval confirms — and the checksums the receiver scripts must reach. Unlike
// the wrapper renewal and the host-key accept, the chain performs nothing on this approval: it cannot
// install the receiver on the production host (the deploy key is a forced command that cannot overwrite
// its own gatekeeper), so the consent is honest about that — it confirms an operator ran the command,
// and states plainly that the chain re-measures the host before it settles the delivery live, so the
// confirmation is never a bypass of the measurement.
func prodReceiverApprovalStatement(target, command string, grants []WrapperGrant) string {
	var b strings.Builder
	b.WriteString("I confirm that on the production host " + target + " I have run the following as root, from a " +
		"checkout of the standard branch, to bring the root receiver scripts current:\n  " + command +
		"\nThe approval covers exactly these scripts, each pinned to the checksum the merged delivery ships:")
	for _, g := range grants {
		b.WriteString("\n  - ")
		b.WriteString(g.Name)
		b.WriteString(" → sha256 ")
		b.WriteString(g.SHA)
	}
	b.WriteString("\nThe chain re-reads the production host's receiver checksums before it settles the delivery live, " +
		"so this confirmation settles nothing on its own — the delivery stays NOT live until the host actually carries these scripts.")
	return b.String()
}

// wrapperApprovalStatement spells out, for the human and for the ledger, WHICH version of the root
// wrapper scripts an approval installs and EXACTLY which files at which checksums it covers (task point
// 2). It names one version — this run's own delivering branch, not yet in the standard branch — because
// that is the sole stand the write half ever installs from: RenewApprovedWrappers re-reads the
// delivering branch regardless of the recorded source and refuses any byte that no longer hashes to the
// approved checksum. So the earlier "standard-branch (merged) version" wording named a version that is
// never the one installed; this names the one that is.
func wrapperApprovalStatement(grants []WrapperGrant) string {
	var b strings.Builder
	b.WriteString("I approve installing the version of these root wrapper scripts that THIS run delivers — " +
		"the scripts on this run's own delivery branch, which is NOT yet merged into the standard branch. " +
		"The approval covers exactly these files, each pinned to this checksum, and nothing else:")
	for _, g := range grants {
		b.WriteString("\n  - ")
		b.WriteString(g.Name)
		b.WriteString(" → sha256 ")
		b.WriteString(g.SHA)
	}
	b.WriteString("\nIt is single-use: the run re-reads the content at install time and installs nothing if " +
		"it no longer matches these checksums.")
	return b.String()
}

// hostKeyApprovalStatement spells out, for the human and for the ledger, WHICH host and WHICH exact key
// an approval trusts — the same single-source consent as the wrapper renewal, so a host-key approval
// too reads identically on the surface and in the ledger.
func hostKeyApprovalStatement(target, fingerprint string) string {
	return "I approve trusting the NEW ssh host key of the production host " + target + ", whose SHA256 " +
		"fingerprint is " + fingerprint + ". I have verified out-of-band that this fingerprint belongs to " +
		"that host. The approval covers exactly this one key and nothing else: it is single-use, and the " +
		"chain re-reads and re-verifies the host's key before trusting it."
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
		// A host-key or receiver question holds the PRODUCTION send, not a dev branch: a production failure
		// never blocks the stack (WHAT-3), so it must not halt new orders branching on this repository.
		if q.QKind == QuestionProdHostKey || q.QKind == QuestionProdReceiver {
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

// OpenProdReceiverQuestion returns the oldest still-open receiver question for a repository, or nil.
// The production pass calls it to avoid raising a duplicate while one already waits for the same repo:
// the receiver drift is a property of the production host and this repo's merged content, so it is
// asked once — and re-raised only when the required checksums change (a newer delivery changed the
// receiver again), which the caller detects by comparing the pinned Wrappers.
func (s *QuestionStore) OpenProdReceiverQuestion(repo string) (*Question, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.load()
	if err != nil {
		return nil, err
	}
	for i := range cur {
		if cur[i].Open() && cur[i].QKind == QuestionProdReceiver && sameRepoID(cur[i].Repo, repo) {
			q := cur[i]
			return &q, nil
		}
	}
	return nil, nil
}

// RetireProdReceiverQuestions resolves every not-yet-consumed receiver question for a repository —
// OPEN or ANSWERED — once the production pass has MEASURED that the host carries the receiver scripts
// the delivery ships (the drift cleared). It is the counterpart of the host-key accept's Resolve: the
// chain performs no install here, so the question is retired by the measurement rather than by an
// applied approval. Returns how many it retired (0 → nothing written). The note records why it closed.
func (s *QuestionStore) RetireProdReceiverQuestions(repo, note string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.load()
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	n := 0
	for i := range cur {
		if cur[i].Resolved || cur[i].QKind != QuestionProdReceiver || !sameRepoID(cur[i].Repo, repo) {
			continue
		}
		cur[i].Resolved = true
		cur[i].ResolvedAt = &now
		if cur[i].CloseNote == "" {
			cur[i].CloseNote = note
		}
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

// Decline records the user's REJECTION of one open question — the co-equal "no". It resolves the
// question (so it holds no repository and drops off the surface), marks it declined, and records the
// note as the reason. It writes NOTHING else: no answer feeds a resume, no approval frees an action.
// The caller ends the question's run and frees its slot; the pool only records the rejection. Unknown
// id → ErrNotFound; an already-closed question → returned unchanged (idempotent, never overwritten).
func (s *QuestionStore) Decline(id, note string, by model.Actor) (Question, error) {
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
		cur[i].Declined = true
		cur[i].DeclinedBy = by
		cur[i].CloseNote = note
		cur[i].Resolved = true
		cur[i].ResolvedAt = &now
		if err := s.save(cur); err != nil {
			return Question{}, err
		}
		return cur[i], nil
	}
	return Question{}, ErrNotFound
}

// CloseMoot closes every OPEN question whose run no longer exists — a question left standing by an
// order that was removed. It is neither approvable nor rejectable-with-effect, so it is resolved
// (holds nothing, off the surface) and marked moot with the note as its reason. The CALLER supplies
// liveRunIDs — the set of orders that still exist — because the pool is passive and never reads other
// stores; the pool only closes the ids that fall outside the set it is told. Returns the questions it
// closed, so the caller can announce the change. Best-effort correctness rests on the caller: it must
// pass the AUTHORITATIVE set (a successful read of the run store), never a partial one — an empty set
// legitimately means "no orders exist", so every open question is then moot.
func (s *QuestionStore) CloseMoot(liveRunIDs map[string]bool, note string) ([]Question, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.load()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	var closed []Question
	for i := range cur {
		if !cur[i].Open() || liveRunIDs[cur[i].RunID] {
			continue
		}
		cur[i].Moot = true
		cur[i].CloseNote = note
		cur[i].Resolved = true
		cur[i].ResolvedAt = &now
		closed = append(closed, cur[i])
	}
	if len(closed) == 0 {
		return nil, nil
	}
	if err := s.save(cur); err != nil {
		return nil, err
	}
	return closed, nil
}

// CloseEnded closes every OPEN question whose ORDER has ENDED — a run that still exists but is
// finished (it reached its goal or was abandoned), so its execution will not act on the question again
// and an answer can no longer resume anything (Selbst prüfen statt fragen: "Endet der Vorgang, zu dem
// eine Frage gehört, kann ihre Beantwortung keine Wirkung mehr entfalten. Sie bleibt nicht offen,
// sondern wird geschlossen und als unwirksam gekennzeichnet."). Such a question is resolved so it holds
// nothing and drops off the surface, and marked Ended with the note as its reason. The CALLER supplies
// endedRunIDs — the orders it has determined are finished — because the pool is passive and never reads
// the execution store itself. An ANSWERED, not-yet-consumed question is left untouched: its answer is
// still redeemed by a resuming or restarting execution, so ending is only ever applied to a question
// nobody has answered. Returns the questions it closed, so the caller can announce the change.
func (s *QuestionStore) CloseEnded(endedRunIDs map[string]bool, note string) ([]Question, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.load()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	var closed []Question
	for i := range cur {
		if !cur[i].Open() || !endedRunIDs[cur[i].RunID] {
			continue
		}
		cur[i].Ended = true
		cur[i].CloseNote = note
		cur[i].Resolved = true
		cur[i].ResolvedAt = &now
		closed = append(closed, cur[i])
	}
	if len(closed) == 0 {
		return nil, nil
	}
	if err := s.save(cur); err != nil {
		return nil, err
	}
	return closed, nil
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
