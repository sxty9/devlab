// Read-only projections (REQ-015.5): the slot overview and the execution views — portioned,
// assembled OUTSIDE the passive pools.
package sched

import (
	"sort"

	"devlab/backend/internal/execstate"
	"devlab/backend/internal/model"
)

// SlotOverview is the read-only slot projection: capacity and occupancy, the active list, the
// deferred executions with their continuation points, the single overload, the restart state
// and the starts queued behind a pending restart.
func (s *Scheduler) SlotOverview() (model.SlotOverview, error) {
	s.mu.Lock()
	restart := s.restartStateLocked()
	s.mu.Unlock()

	docs, err := s.docs.Live()
	if err != nil {
		return model.SlotOverview{}, err
	}
	ov := model.SlotOverview{
		Capacity:       s.settingsEffective().MaxConcurrency,
		RestartPending: restart.Pending,
		Active:         []model.ExecutionView{},
		Deferred:       []model.ExecutionView{},
		QueuedStarts:   restart.QueuedStarts,
	}
	if ov.QueuedStarts == nil {
		ov.QueuedStarts = []model.QueuedStart{}
	}
	for _, d := range docs {
		if d.Phase == model.PhaseRunning && !d.Overload {
			ov.Occupied++
		}
		if d.Overload {
			ov.OverloadActive = true
		}
		v := s.executionView(d)
		if d.Phase == model.PhasePaused {
			ov.Deferred = append(ov.Deferred, v)
		} else {
			ov.Active = append(ov.Active, v)
		}
	}
	sortViews(ov.Active)
	sortViews(ov.Deferred)
	return ov, nil
}

// ActiveList is the ACTIVE executions as a LIST (REQ-013.1) — every live document, paused ones
// included; the answer about "the active run" is never a singular.
func (s *Scheduler) ActiveList() ([]model.ExecutionView, error) {
	docs, err := s.docs.Live()
	if err != nil {
		return nil, err
	}
	out := make([]model.ExecutionView, 0, len(docs))
	for _, d := range docs {
		out = append(out, s.executionView(d))
	}
	sortViews(out)
	return out, nil
}

func sortViews(vs []model.ExecutionView) {
	sort.SliceStable(vs, func(i, j int) bool {
		if vs[i].CreatedAt.Equal(vs[j].CreatedAt) {
			return vs[i].ID < vs[j].ID
		}
		return vs[i].CreatedAt.Before(vs[j].CreatedAt)
	})
}

// executionView projects one document (plus its run title and live usage) into the wire shape.
func (s *Scheduler) executionView(d execstate.Doc) model.ExecutionView {
	v := model.ExecutionView{
		ID:           d.ID,
		RunID:        d.RunID,
		Kind:         d.Kind,
		Phase:        d.Phase,
		Reason:       d.Reason,
		Pause:        d.Pause,
		Continuation: d.Continuation,
		Overload:     d.Overload,
		Requested:    d.Requested,
		CreatedAt:    d.CreatedAt,
		Repos:        []model.RepoPipeline{},
	}
	if d.StartedAt != nil {
		v.StartedAt = *d.StartedAt
	}
	if d.UpdatedAt != nil {
		v.UpdatedAt = *d.UpdatedAt
	}
	if s.runs != nil {
		if r, ok, err := s.runs.Get(d.RunID); err == nil && ok {
			v.RunTitle = r.Title
		}
	}
	// The stage detail and the usage live on the result document (the recorder writes it
	// live); the doc carries the coarse repo progress as a fallback when no result exists yet.
	haveResult := false
	if s.results != nil {
		if res, ok, err := s.results.Get(d.ID); err == nil && ok {
			v.Repos = res.Repos
			v.Usage = res.Usage
			if v.RunTitle == "" {
				v.RunTitle = res.RunTitle
			}
			haveResult = true
		}
	}
	if !haveResult {
		for _, r := range d.Repos {
			rp := model.RepoPipeline{Repo: r.Repo, Block: r.Block}
			v.Repos = append(v.Repos, rp)
		}
	}
	if v.RunTitle == "" {
		v.RunTitle = d.RunID
	}
	return v
}
