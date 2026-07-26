package api

import (
	"sync"
	"time"
)

// runWave is the process-wide state shared by every concurrently executing run so that the two global
// limits apply IN AGGREGATE, not per run (task point 5):
//
//   - the spend ceiling bounds the SUM of what all active runs cost this wave, not each run separately —
//     otherwise N concurrent runs could each spend up to the ceiling and blow the budget N-fold;
//   - the subscription usage limit, once hit by ANY run, pauses ALL of them at the same reset instant and
//     lets them resume together, instead of each independently hammering the exhausted account into
//     repeated failed attempts.
//
// A "wave" is one contiguous span during which at least one run is active. When the last run leaves and a
// new one later enters (active goes 0→1), the wave's aggregate spend and limit gate reset: a fresh wave
// starts with a fresh budget and is not held down by a limit an earlier wave already waited out. This is
// the concurrency-generalisation of the old per-attempt budget, which reset every time a single run began.
type runWave struct {
	mu       sync.Mutex
	active   int
	spend    float64
	limited  bool
	resumeAt time.Time
}

// enter marks one execution as starting and returns after bookkeeping. When it is the FIRST of a wave the
// aggregate spend and the limit gate are reset.
func (w *runWave) enter() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.active == 0 {
		w.spend = 0
		w.limited = false
		w.resumeAt = time.Time{}
	}
	w.active++
}

// leave marks one execution as finished.
func (w *runWave) leave() {
	w.mu.Lock()
	if w.active > 0 {
		w.active--
	}
	w.mu.Unlock()
}

// addSpend folds one repo's cost into the wave's running aggregate.
func (w *runWave) addSpend(usd float64) {
	w.mu.Lock()
	w.spend += usd
	w.mu.Unlock()
}

// spendSnapshot reads the current aggregate spend (for logging).
func (w *runWave) spendSnapshot() float64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.spend
}

// overBudget reports whether the aggregate spend across all runs in this wave has reached the ceiling
// (ceiling <= 0 disables it). Checked before starting another repo, so the sum is a soft cap: a repo
// already in flight may overshoot by its own cost.
func (w *runWave) overBudget(ceiling float64) bool {
	if ceiling <= 0 {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.spend >= ceiling
}

// tripLimit records that a run hit the subscription usage limit and when the window resets, so every run
// in the wave pauses together. The LATEST reset wins — resuming earlier than the true reset would just
// re-hit the limit — so all runs wait until at least that instant.
func (w *runWave) tripLimit(resumeAt time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.limited = true
	if resumeAt.After(w.resumeAt) {
		w.resumeAt = resumeAt
	}
}

// limitTripped reports whether the wave is paused on the subscription limit and, if so, when to resume.
func (w *runWave) limitTripped() (bool, time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.limited, w.resumeAt
}
