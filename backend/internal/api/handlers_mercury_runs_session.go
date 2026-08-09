// The session of one execution, read and written.
//
// Two requests on ONE path, because there is one thing here — the conversation a run works in:
//
//	GET  …/results/{rid}/session   read a PORTION of it (open on the newest, follow forward,
//	                               fetch an older portion on demand)
//	POST …/results/{rid}/session   write into it, while it is still running
//
// They are guarded by two DIFFERENT rights. Reading needs hp_devlab_session_watch, writing needs
// hp_devlab_session_speak — whoever may watch a run think may not thereby steer it.
//
// The read works the same for a running and for a finished execution: the journal is the record
// either way, so a session that has ended stays readable exactly where it was watched. Only `open`
// differs, and that is the one thing the view needs in order to know whether an input belongs on
// the screen.
package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
)

// sessionView is one portion of a session together with what a viewer must know about it: whether
// it can still be spoken to, which repositories are working, and who has already intervened.
type sessionView struct {
	runs.SessionSlice
	// Open reports that this conversation is running HERE and now and can take a message. It is
	// measured against the register of open conversations, never guessed from a stored state.
	Open bool `json:"open"`
	// Repos names the repositories whose conversation is open — what a writer must name when
	// several are working at once.
	Repos []string `json:"repos,omitempty"`
	// Interventions is who wrote into this execution, and when. The words themselves stand in the
	// lines above; this is what makes "a person was involved here" visible at a glance.
	Interventions []model.Intervention `json:"interventions,omitempty"`
}

// runSessionRead answers one portion of an execution's session journal. Which portion follows
// from the query: nothing named opens on the NEWEST one (what a viewer wants to see first),
// `at=<next>` follows forward, `at=<from>&back=1` fetches the portion before one already held.
func (s *Server) runSessionRead(w http.ResponseWriter, r *http.Request) {
	rid := r.PathValue("rid")
	if !resultIDRe.MatchString(rid) {
		writeErr(w, http.StatusBadRequest, "Invalid result id")
		return
	}
	q := r.URL.Query()
	at, _ := strconv.ParseInt(q.Get("at"), 10, 64)
	max, _ := strconv.Atoi(q.Get("max"))
	win := runs.SessionWindow{At: at, Back: q.Get("back") == "1", Max: max}
	if q.Get("at") == "" && q.Get("back") == "" {
		win.Back = true // the opening read: where the session stands now
	}
	s.writeSession(w, rid, win)
}

// writeSession is the ONE answer both requests give: a portion of the record plus what a viewer
// must know about it. Writing into a session answers with it too, so the writer sees its own words
// land instead of a bare acknowledgement.
func (s *Server) writeSession(w http.ResponseWriter, rid string, win runs.SessionWindow) {
	if s.results == nil {
		writeErr(w, http.StatusNotFound, "No result")
		return
	}
	slice, err := s.results.ReadSession(rid, win)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not read the session")
		return
	}

	out := sessionView{SessionSlice: slice}
	if s.sessions != nil {
		out.Repos, out.Open = s.sessions.speakable(rid)
	}
	// The intervention list rides with the portion: a follower learns that somebody stepped in at
	// the same moment it sees what they wrote, without a second request.
	if res, ok, rerr := s.results.Get(rid); rerr == nil && ok {
		out.Interventions = res.Interventions
	}
	writeJSON(w, http.StatusOK, out)
}

// sessionMessage is one person's words for a running conversation.
type sessionMessage struct {
	// Repo names the repository whose conversation is meant. It may be omitted while exactly one
	// is working.
	Repo string `json:"repo,omitempty"`
	Text string `json:"text"`
}

// runSessionSpeak carries one message into a running conversation. It answers with the same shape
// the read answers with, so the writer immediately sees its own words in the record — no separate
// success form, and no need to guess whether it arrived.
func (s *Server) runSessionSpeak(w http.ResponseWriter, r *http.Request) {
	rid := r.PathValue("rid")
	if !resultIDRe.MatchString(rid) {
		writeErr(w, http.StatusBadRequest, "Invalid result id")
		return
	}
	var msg sessionMessage
	if !decodeJSONLimit(w, r, &msg, maxSpokenBytes+1024, "Invalid message") {
		return
	}

	err := s.SpeakIntoSession(rid, msg.Repo, msg.Text, actorOf(r), time.Now())
	switch {
	case err == nil:
		s.writeSession(w, rid, runs.SessionWindow{Back: true})
	case errors.Is(err, errEmptyMessage):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, errNoSuchSession), errors.Is(err, errSessionClosed):
		// GONE, not NOT-FOUND: the conversation existed, it just cannot be reached any more. The
		// difference is what the view needs to say "this session has ended" rather than "unknown".
		writeErr(w, http.StatusGone, err.Error())
	default:
		writeErr(w, http.StatusConflict, err.Error())
	}
}

// speakable reports which repositories of an execution have an open conversation right now.
func (o *openSessions) speakable(execID string) ([]string, bool) {
	s := o.get(execID)
	if s == nil {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	repos := make([]string, 0, len(s.desks))
	for repo, d := range s.desks {
		if d.speakable() {
			repos = append(repos, repo)
		}
	}
	return repos, len(repos) > 0
}

// speakable reports whether this desk would still take a message.
func (d *agentDesk) speakable() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return !d.closed && d.w != nil
}
