// Package telemetry reports — and never judges (cross-cutting 5): the ONE AI-usage pool
// (assistant, agent and runs all record here), the portioned storage report, and the process's
// own load. Evaluation happens outside the service.
package telemetry

import (
	"time"

	"devlab/backend/internal/model"
	"devlab/backend/internal/statepath"
)

// UsageSample is one recorded AI consumption sample.
type UsageSample struct {
	Source string    `json:"source"` // "assistant" | "agent" | "run"
	User   string    `json:"user,omitempty"`
	Repo   string    `json:"repo,omitempty"`
	Model  string    `json:"model,omitempty"`
	In     int64     `json:"in"`
	Out    int64     `json:"out"`
	At     time.Time `json:"at"`
}

// UsageLedger is the ONE AI-usage pool (mercury/ai-usage.json), capped as a rolling window.
type UsageLedger struct {
	path string
}

// OpenUsage opens the ledger below the state root.
func OpenUsage(p *statepath.Paths) *UsageLedger {
	var path string
	if p != nil {
		path = p.AiUsage()
	}
	return &UsageLedger{path: path}
}

// Record appends one sample (atomic rewrite of the capped window).
func (u *UsageLedger) Record(s UsageSample) error {
	panic("TODO(B10)")
}

// Aggregate reports usage over a window, per source — reported, never judged.
func (u *UsageLedger) Aggregate(window time.Duration) (model.AiUsageView, error) {
	panic("TODO(B10)")
}

// Storage reports the portioned occupancy of every pool below the state root.
func Storage(p *statepath.Paths) (model.StorageView, error) {
	panic("TODO(B10)")
}

// Load reports the process's own CPU/RSS/goroutine claim.
func Load() model.LoadView {
	panic("TODO(B10)")
}
