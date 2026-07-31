// The document-progress sink — sched's half of the executor's Sink seam (§3.8): it mirrors the
// motor's per-repo progress into the persisted state document (coarse repo states + the
// continuation point), so admission, the slot overview and resume always read the truth. It
// implements the executor's Sink interface STRUCTURALLY — sched never imports the executor
// (B-3); cmd/devlabd composes this with the result recorder.
package sched

import (
	"log"

	"devlab/backend/internal/execstate"
	"devlab/backend/internal/live"
	"devlab/backend/internal/model"
)

// DocSink mirrors one execution's progress into its state document.
type DocSink struct {
	s      *Scheduler
	execID string
}

// DocSink returns the document-progress sink for one execution.
func (s *Scheduler) DocSink(execID string) *DocSink {
	return &DocSink{s: s, execID: execID}
}

func (k *DocSink) update(mutate func(*execstate.Doc)) {
	if _, err := k.s.docs.Update(k.execID, func(d *execstate.Doc) error {
		mutate(d)
		return nil
	}); err != nil {
		log.Printf("devlabd: sched doc sink %s: %v", k.execID, err)
	}
}

func ensureRepo(d *execstate.Doc, repo string) *execstate.RepoProgress {
	if r := d.Repo(repo); r != nil {
		return r
	}
	// An all-repos run discovers its repositories during execution — append them as they
	// appear (busy ones were already ordered to the back at admission).
	d.Repos = append(d.Repos, execstate.RepoProgress{Repo: repo, State: execstate.RepoPending})
	return &d.Repos[len(d.Repos)-1]
}

// StageUpdate marks the repo active the moment any of its stages moves — the claim the
// admission rules read for repo exclusivity (REQ-014.2: an all-repos run occupies only the
// repository it is actually working).
func (k *DocSink) StageUpdate(repo string, _ model.StageView) {
	k.update(func(d *execstate.Doc) {
		r := ensureRepo(d, repo)
		if r.State == execstate.RepoPending || r.State == execstate.RepoBlocked {
			r.State = execstate.RepoActive
			r.Block = nil
		}
	})
	k.s.publish(live.TopicActive)
}

// Transcript is the recorder's concern — the document holds no transcript.
func (k *DocSink) Transcript(string, []byte) {}

// Usage is the recorder's concern — the document holds no counters.
func (k *DocSink) Usage(model.UsageView) {}

// RepoDone records one repo's outcome: done — or blocked, with its persisted backoff
// (REQ-032: a blocked repository holds its block visibly and does not stop the others).
func (k *DocSink) RepoDone(rp model.RepoPipeline) {
	k.update(func(d *execstate.Doc) {
		r := ensureRepo(d, rp.Repo)
		if rp.Block != nil {
			r.State = execstate.RepoBlocked
			r.Block = rp.Block
			return
		}
		r.State = execstate.RepoDone
		r.Block = nil
	})
	k.s.publish(live.TopicActive)
	k.s.publish(live.TopicSlots)
}

// Continuation persists the exact continuation point (resume at the same spot, REQ-018.6).
func (k *DocSink) Continuation(c model.ContinuationView) {
	k.update(func(d *execstate.Doc) {
		cc := c
		d.Continuation = &cc
	})
}
