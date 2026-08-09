// The OPEN side of an execution's agent session: who can be spoken to right now.
//
// The chain's implement stage runs the agent as ONE conversation whose input stays open while it
// works. This file is the register of those open conversations inside the running daemon — one
// entry per execution, one desk per repository — plus the single act that uses it: a person writes
// a message, it reaches the running agent, and the same words are recorded in the session journal
// the rest of the conversation is recorded in.
//
// Two rules make the register safe to hold:
//
//   - THE TURN COUNT DECIDES THE END. The agent's input must be closed for the invocation to end at
//     all; closing it too early would cut a person's message off, closing it never would stall the
//     stage. So the desk counts what it handed over and what came back: once every handed-over
//     message has been answered, the input is closed and the agent finishes. A message that arrives
//     in that same instant either still gets in (and is awaited) or is refused as "ended" — never
//     silently dropped.
//
//   - THE REGISTER IS A WINDOW, NOT A VALVE. Nothing the chain does depends on it. A missing entry,
//     a closed desk or a failing journal write makes speaking impossible and says so; the execution
//     itself runs on untouched.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	"devlab/backend/internal/executor"
	"devlab/backend/internal/live"
	"devlab/backend/internal/model"
)

// The named refusals a speaker can meet. They are errors, not silences: a message that did not
// reach the agent never looks as though it had.
var (
	// errSessionClosed means the conversation has ended — no answer can reach it any more.
	errSessionClosed = errors.New("this session has ended — nothing can be written into it any more")
	// errNoSuchSession means no conversation of that execution (or that repository) is open here.
	errNoSuchSession = errors.New("no open session for this execution")
	// errEmptyMessage refuses a blank intervention rather than recording one.
	errEmptyMessage = errors.New("an empty message is not an intervention")
	// errMessageTooLong refuses an oversized one. It is REFUSED, not shortened: a message cut in
	// half would reach the agent as something its writer never said, and be recorded as such.
	errMessageTooLong = fmt.Errorf("a message into a session is at most %d characters — put what does not fit into the repository", maxSpokenBytes)
)

// maxSpokenBytes caps one intervention. A person types; a paste of a whole file belongs in the
// repository, not in the conversation's input.
const maxSpokenBytes = 8 << 10

// openSessions is the daemon's register of open agent conversations, keyed by execution id.
type openSessions struct {
	mu sync.Mutex
	by map[string]*openSession
}

func newOpenSessions() *openSessions { return &openSessions{by: map[string]*openSession{}} }

// openSession is ONE execution's open conversation: its recorder (the single writer of the
// execution's result document) and one desk per repository the agent is working in.
type openSession struct {
	rec   *ResultRecorder
	mu    sync.Mutex
	desks map[string]*agentDesk
}

// agentDesk is the input of one running agent invocation.
type agentDesk struct {
	mu sync.Mutex
	w  io.WriteCloser
	// handed counts the messages given to the agent (the chain's own opening message is the
	// first); answered counts the turn-ending results that came back.
	handed, answered int
	closed           bool
}

// open registers an execution's conversation. Called when its recorder is opened, so the register
// and the result document have exactly the same lifetime.
func (o *openSessions) open(execID string, rec *ResultRecorder) {
	if execID == "" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.by[execID] = &openSession{rec: rec, desks: map[string]*agentDesk{}}
}

// close forgets an execution's conversation and shuts every desk still standing, so no writer is
// left pointing at a finished process.
func (o *openSessions) close(execID string) {
	o.mu.Lock()
	s := o.by[execID]
	delete(o.by, execID)
	o.mu.Unlock()
	if s == nil {
		return
	}
	s.mu.Lock()
	desks := s.desks
	s.desks = map[string]*agentDesk{}
	s.mu.Unlock()
	for _, d := range desks {
		d.shut()
	}
}

func (o *openSessions) get(execID string) *openSession {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.by[execID]
}

// desk opens the input of one repository's agent invocation. A second invocation for the same
// repository (the fresh conversation after a lost one) replaces the first, whose process is gone.
func (o *openSessions) desk(execID, repo string, w io.WriteCloser) *agentDesk {
	d := &agentDesk{w: w}
	s := o.get(execID)
	if s == nil {
		return d // no register entry (an observation-only composition): the desk still works locally
	}
	s.mu.Lock()
	prev := s.desks[repo]
	s.desks[repo] = d
	s.mu.Unlock()
	if prev != nil {
		prev.shut()
	}
	return d
}

// drop removes one repository's desk once its invocation has ended.
func (o *openSessions) drop(execID, repo string, d *agentDesk) {
	d.shut()
	s := o.get(execID)
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.desks[repo] == d {
		delete(s.desks, repo)
	}
	s.mu.Unlock()
}

// openDesk / dropDesk are the chain's two touches on the register: an invocation hands over its
// input when it starts and gives it back when it ends. Both tolerate a server without a register
// (an observation-only composition), because the chain must not depend on being watchable.
func (s *Server) openDesk(execID, repo string, w io.WriteCloser) *agentDesk {
	if s.sessions == nil {
		return &agentDesk{w: w}
	}
	return s.sessions.desk(execID, repo, w)
}

func (s *Server) dropDesk(execID, repo string, d *agentDesk) {
	if d == nil {
		return
	}
	if s.sessions == nil {
		d.shut()
		return
	}
	s.sessions.drop(execID, repo, d)
}

// pick resolves which desk a message goes to. Naming the repository is optional while exactly one
// conversation is open — the common case — and required as soon as there are several, because
// guessing which agent a person meant is worse than asking.
func (s *openSession) pick(repo string) (*agentDesk, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if repo != "" {
		if d := s.desks[repo]; d != nil {
			return d, nil
		}
		return nil, errNoSuchSession
	}
	switch len(s.desks) {
	case 0:
		return nil, errNoSuchSession
	case 1:
		for _, d := range s.desks {
			return d, nil
		}
	}
	return nil, errors.New("several repositories are working — name the one to write to")
}

// speak hands one message to the agent. It returns once the message is IN the conversation's
// input; the answer is what the session journal shows next.
func (d *agentDesk) speak(text string) error {
	msg, err := json.Marshal(struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	}{Type: "user", Message: struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}{Role: "user", Content: text}})
	if err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || d.w == nil {
		return errSessionClosed
	}
	if _, err := d.w.Write(append(msg, '\n')); err != nil {
		// The process is gone (a broken pipe): the conversation ended between the check and the
		// write. Say that, do not report a stranger's error.
		d.closeLocked()
		return errSessionClosed
	}
	d.handed++
	return nil
}

// answered records that one turn came back. Once every handed-over message has been answered the
// input is closed — which is what lets the invocation end at all.
func (d *agentDesk) answer() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.answered++
	if d.answered >= d.handed {
		d.closeLocked()
	}
}

// shut closes the input unconditionally — the end of the invocation, whatever brought it about.
func (d *agentDesk) shut() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closeLocked()
}

func (d *agentDesk) closeLocked() {
	if d.closed {
		return
	}
	d.closed = true
	if d.w != nil {
		_ = d.w.Close()
	}
}

// ── the one act that uses the register ───────────────────────────────────────────────────

// SpeakIntoSession carries one person's message into a running execution's agent conversation and
// records it: the words go into the SAME session journal the agent's own output goes into, and the
// execution's document notes that a person intervened, who and when. A run that was written into
// is no longer purely self-acting and says so afterwards.
//
// The order is deliberate. The message reaches the agent FIRST; only what actually arrived is
// recorded. A refused message leaves no trace claiming otherwise.
func (s *Server) SpeakIntoSession(execID, repo, text string, by model.Actor, at time.Time) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return errEmptyMessage
	}
	if len(text) > maxSpokenBytes {
		return errMessageTooLong
	}
	if s.sessions == nil {
		return errNoSuchSession
	}
	sess := s.sessions.get(execID)
	if sess == nil {
		return errNoSuchSession
	}
	desk, err := sess.pick(repo)
	if err != nil {
		return err
	}
	if err := desk.speak(text); err != nil {
		return err
	}

	// From here on the message IS in the conversation. Recording it must not undo that, so a
	// failing record is logged and named — never turned into "the message did not arrive".
	if s.results != nil {
		if err := s.results.AppendTranscript(execID, executor.SessionLine(repo, by.User, text)); err != nil {
			log.Printf("devlabd: session of execution %s: intervention not journalled: %v", execID, err)
		}
	}
	if sess.rec != nil {
		sess.rec.Intervened(model.Intervention{By: by, At: at.UTC(), Repo: repo})
	}
	s.publish(live.TopicSession)
	return nil
}
