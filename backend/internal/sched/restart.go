// The restart coordinator (REQ-018, K-2): exactly ONE marker file under exactly one name —
// statepath.RestartMarker() = mercury/restart.json (B-13; the old system's twin-marker trap of
// two near-identical names does not exist). "Restart requested" and "execution starts" exclude
// each other atomically under the ONE admission mutex; running executions drain out (never
// killed); starts requested meanwhile are recorded on the marker and fire after the restart.
package sched

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"devlab/backend/internal/fsatomic"
	"devlab/backend/internal/live"
	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
)

func (s *Scheduler) markerPath() string {
	return s.docs.Paths().RestartMarker()
}

// restartStateLocked reads the marker under the admission mutex. A missing file is "no restart
// pending"; an unreadable file counts as pending (fail closed — a requested restart must never
// be lost to a torn read).
func (s *Scheduler) restartStateLocked() model.RestartState {
	b, err := os.ReadFile(s.markerPath())
	if err != nil {
		if !os.IsNotExist(err) {
			return model.RestartState{Pending: true}
		}
		return model.RestartState{}
	}
	var st model.RestartState
	if err := json.Unmarshal(b, &st); err != nil {
		return model.RestartState{Pending: true}
	}
	st.Pending = true
	return st
}

func (s *Scheduler) restartPendingLocked() bool {
	return s.restartStateLocked().Pending
}

// RequestRestart writes restart.json — atomic versus Submit (one mutex). Implemented here,
// injected into the executor seam (B-3). The FIRST request sets the marker; further requests
// keep it (idempotent); the marker resolves when the last running execution drains out and the
// restart wrapper finds the ready socket free.
func (s *Scheduler) RequestRestart(by model.Actor) (model.RestartState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st := s.restartStateLocked(); st.Pending {
		return st, nil // the first requester set it; a second request never resets the clock
	}
	now := s.now()
	drainCap := s.cfg.MaxDuration
	if drainCap <= 0 {
		drainCap = defaultRestartDrainCap
	}
	st := model.RestartState{
		Pending:     true,
		RequestedBy: by,
		RequestedAt: now,
		// The drain-out is time-bounded (REQ-018.5): past the deadline the restart wrapper
		// restarts anyway and the event is on the record here.
		Deadline: now.Add(drainCap),
	}
	if err := fsatomic.WriteJSON(s.markerPath(), st); err != nil {
		return model.RestartState{}, err
	}
	s.notice("restart-requested", fmt.Sprintf("restart requested — running executions drain out (deadline %s)", st.Deadline.Format(time.RFC3339)))
	s.publish(live.TopicRestart)
	s.publish(live.TopicSlots)
	return st, nil
}

// RestartState reports the restart marker's state.
func (s *Scheduler) RestartState() model.RestartState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.restartStateLocked()
}

// RestartReady reports free ⇔ no document is "running" (the ready socket's truth). Errors read
// as busy — a restart never fires over an unknown state; a DEAD daemon reads as free on the
// wrapper's side.
func (s *Scheduler) RestartReady() bool {
	docs, err := s.docs.Live()
	if err != nil {
		return false
	}
	for _, d := range docs {
		if d.Phase == model.PhaseRunning {
			return false
		}
	}
	return true
}

// appendQueuedStartLocked records a start requested while a restart is pending; the fresh
// process releases it through the normal admission (REQ-018.3/4).
func (s *Scheduler) appendQueuedStartLocked(req StartRequest, run runs.Run) error {
	st := s.restartStateLocked()
	if !st.Pending {
		return errors.New("no restart pending")
	}
	for _, q := range st.QueuedStarts {
		if q.RunID == req.RunID {
			return nil // already queued — one start per run suffices
		}
	}
	st.QueuedStarts = append(st.QueuedStarts, model.QueuedStart{
		RunID: req.RunID,
		Title: run.Title,
		By:    req.By,
		At:    s.now(),
	})
	return fsatomic.WriteJSON(s.markerPath(), st)
}

// CompleteBootRestart finishes a restart at boot (reconcile step 4): a present restart.json
// means THIS boot is the restart — notify the requester, delete the marker, and release the
// recorded queued starts through the normal admission. Idempotent; returns how many queued
// starts it released.
func (s *Scheduler) CompleteBootRestart(ctx context.Context) (int, error) {
	s.mu.Lock()
	st := s.restartStateLocked()
	if !st.Pending {
		s.mu.Unlock()
		return 0, nil
	}
	if err := os.Remove(s.markerPath()); err != nil && !os.IsNotExist(err) {
		s.mu.Unlock()
		return 0, err
	}
	s.mu.Unlock()

	who := st.RequestedBy.User
	if who == "" {
		who = "the delivery chain"
	}
	s.notice("restart-completed", fmt.Sprintf("restart completed (requested by %s at %s)", who, st.RequestedAt.Format(time.RFC3339)))
	s.publish(live.TopicRestart)

	released := 0
	for _, q := range st.QueuedStarts {
		out, err := s.Submit(ctx, StartRequest{RunID: q.RunID, By: q.By, Placement: &Placement{Kind: PlacementQueue}})
		if err != nil {
			s.notice("restart-queued-start-failed", fmt.Sprintf("queued start of run %s could not be released: %v", q.RunID, err))
			continue
		}
		if out.Started || out.Queued {
			released++
		}
	}
	return released, nil
}
