// The explicit resume of a BLOCKED pull request (K-5/REQ-032.4).
//
// A pull request whose read fails often enough is blocked honestly and waits for a person to say
// "try again" — that is the design, and it is what replaces endless retrying. The state existed
// from the start; the way OUT of it did not, so a pull request blocked by a passing outage stayed
// blocked for ever and the whole delivery stood still (measured 2026-07-31: 63 of 64 entries
// blocked, every one of them by a read that never reached GitHub while the service was restarting).
package api

import (
	"net/http"

	"devlab/backend/internal/live"
)

// runPRResume clears the blockade so the maintenance evaluates the pull request again. With no body
// it releases EVERY blocked entry; with {"repo": "...", "number": n} exactly the named one.
//
// It merges nothing and decides nothing about the pull request: the pass re-evaluates and blocks
// again, with a fresh reason, whatever genuinely fails. That is why this is safe to press twice.
func (s *Server) runPRResume(w http.ResponseWriter, r *http.Request) {
	if s.runPRs == nil {
		writeErr(w, http.StatusServiceUnavailable, "The pull request pool is not available")
		return
	}
	var body struct {
		Repo   string `json:"repo"`
		Number int    `json:"number"`
	}
	// An empty body is the "release everything" case and must not read as a malformed request.
	if r.ContentLength > 0 && !decodeJSON(w, r, &body) {
		return
	}
	if body.Repo != "" && body.Number <= 0 {
		writeErr(w, http.StatusBadRequest, "A named repository needs its pull request number")
		return
	}

	freed, err := s.runPRs.ResumeBlocked(body.Repo, body.Number)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "The blockade could not be released: "+err.Error())
		return
	}
	if len(freed) > 0 {
		s.publish(live.TopicDeliveries)
	}

	out := make([]map[string]any, 0, len(freed))
	for _, p := range freed {
		out = append(out, map[string]any{"repo": p.Repo, "number": p.Number, "url": p.URL})
	}
	writeJSON(w, http.StatusOK, map[string]any{"released": len(freed), "pullRequests": out})
}
