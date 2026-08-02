// Package statepath is the ONE place for state-path literals (REQ-040.5, W-F).
//
// Root resolution: $DEVLAB_STATE_DIR, default $STATE_DIRECTORY (systemd), else error —
// there is no baked-in instance path (universality: instance specifics live only in the
// runtime configuration, never in the repository).
//
// Every store derives its location from a *Paths. Individual stores may additionally keep
// their historical per-store env override (a test seam, part of the ported behavior); the
// FALLBACK literal, however, lives exactly here.
package statepath

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Paths resolves every persisted location below one state root.
type Paths struct{ Root string }

// FromEnv resolves the state root from $DEVLAB_STATE_DIR, falling back to $STATE_DIRECTORY
// (set by systemd's StateDirectory=). It errors when neither is set: DevLab never bakes in
// an instance path.
func FromEnv() (*Paths, error) {
	if v := os.Getenv("DEVLAB_STATE_DIR"); v != "" {
		return &Paths{Root: v}, nil
	}
	if v := os.Getenv("STATE_DIRECTORY"); v != "" {
		// systemd may pass a colon-separated list; the first entry is ours.
		if i := indexByte(v, ':'); i >= 0 {
			v = v[:i]
		}
		return &Paths{Root: v}, nil
	}
	return nil, errors.New("state root not configured: set DEVLAB_STATE_DIR (or run under systemd StateDirectory)")
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// Mercury is the root of the Mercury domain: <root>/mercury.
func (p *Paths) Mercury() string { return filepath.Join(p.Root, "mercury") }

// Runs is the run-definition pool: <root>/mercury/runs.json.
func (p *Paths) Runs() string { return filepath.Join(p.Mercury(), "runs.json") }

// Executions is the per-execution document tree: <root>/mercury/executions/.
func (p *Paths) Executions() string { return filepath.Join(p.Mercury(), "executions") }

// Execution is one execution's directory: <root>/mercury/executions/<id>/.
func (p *Paths) Execution(id string) string { return filepath.Join(p.Executions(), id) }

// Deliveries is the delivery ledger: <root>/mercury/runs-deliveries.json.
func (p *Paths) Deliveries() string { return filepath.Join(p.Mercury(), "runs-deliveries.json") }

// PRs is the PR maintenance pool: <root>/mercury/runs-prs.json.
func (p *Paths) PRs() string { return filepath.Join(p.Mercury(), "runs-prs.json") }

// NoticesFile is the notice pool: <root>/mercury/runs-notices.json.
func (p *Paths) NoticesFile() string { return filepath.Join(p.Mercury(), "runs-notices.json") }

// QuestionsFile is the blocking-question pool: <root>/mercury/runs-questions.json. It holds every
// open question a run raised (the Blocked surface) plus its answer once given.
func (p *Paths) QuestionsFile() string { return filepath.Join(p.Mercury(), "runs-questions.json") }

// Settings is the service-settings pool (slot capacity, default time budget, automerge
// window): <root>/mercury/settings.json.
func (p *Paths) Settings() string { return filepath.Join(p.Mercury(), "settings.json") }

// HistoryDir is the run-config snapshot history: <root>/mercury/runs-history/.
func (p *Paths) HistoryDir() string { return filepath.Join(p.Mercury(), "runs-history") }

// Attachments is the media pool: <root>/mercury/attachments/.
func (p *Paths) Attachments() string { return filepath.Join(p.Mercury(), "attachments") }

// AxiomAuthors is the per-axiom authorship pool: <root>/mercury/axiom-authors.json.
func (p *Paths) AxiomAuthors() string { return filepath.Join(p.Mercury(), "axiom-authors.json") }

// AxiomChecks is the per repo+axiom examination pool: <root>/mercury/axiom-checks.json.
func (p *Paths) AxiomChecks() string { return filepath.Join(p.Mercury(), "axiom-checks.json") }

// DailyReports is the report-delivery ledger: <root>/mercury/daily-reports.json.
func (p *Paths) DailyReports() string { return filepath.Join(p.Mercury(), "daily-reports.json") }

// AiUsage is the ONE AI-usage pool: <root>/mercury/ai-usage.json.
func (p *Paths) AiUsage() string { return filepath.Join(p.Mercury(), "ai-usage.json") }

// RestartMarker is the exactly-one restart marker file (B-13): <root>/mercury/restart.json.
func (p *Paths) RestartMarker() string { return filepath.Join(p.Mercury(), "restart.json") }

// UsageLimit is the usage-limit window cache: <root>/mercury/usage-limit.json.
func (p *Paths) UsageLimit() string { return filepath.Join(p.Mercury(), "usage-limit.json") }

// MercuryOrder is the manual tree-order pool: <root>/mercury/order.json.
func (p *Paths) MercuryOrder() string { return filepath.Join(p.Mercury(), "order.json") }

// ReadySocket is the restart-readiness Unix socket (A2-7): <root>/restart-ready.sock.
// It answers 204 (free) / 423 (busy); the tunnel never routes it.
func (p *Paths) ReadySocket() string { return filepath.Join(p.Root, "restart-ready.sock") }

// LegacyResults is the read-only archive of pre-rebuild execution results:
// <root>/mercury/runs-results. Nothing is ever written here again (REQ-027.3).
func (p *Paths) LegacyResults() string { return filepath.Join(p.Mercury(), "runs-results") }

// Links is the GitHub-link pool directory: <root>/links.
func (p *Paths) Links() string { return filepath.Join(p.Root, "links") }

// Chats is the per-user AI transcript pool: <root>/chats.
func (p *Paths) Chats() string { return filepath.Join(p.Root, "chats") }

// Comments is the per-repo comment pool: <root>/comments.
func (p *Paths) Comments() string { return filepath.Join(p.Root, "comments") }

// Workspaces is the per-user working-tree root: <root>/workspaces.
func (p *Paths) Workspaces() string { return filepath.Join(p.Root, "workspaces") }

// Www is the built SPA the daemon serves: <root>/www.
func (p *Paths) Www() string { return filepath.Join(p.Root, "www") }

// AxiomsClone is the working clone of the constitution repository: <root>/axioms.
func (p *Paths) AxiomsClone() string { return filepath.Join(p.Root, "axioms") }

// CheckWritable proves the state root is writable by writing and removing <root>/probe, and
// sweeps stale *.tmp files older than one hour (leftovers of interrupted atomic writes).
// Admission gates on it so a full disk surfaces as a named condition, not silent loss.
func (p *Paths) CheckWritable() error {
	if p == nil || p.Root == "" {
		return errors.New("state root not configured")
	}
	if err := os.MkdirAll(p.Root, 0o700); err != nil {
		return fmt.Errorf("state root not creatable: %w", err)
	}
	probe := filepath.Join(p.Root, "probe")
	if err := os.WriteFile(probe, []byte("probe"), 0o600); err != nil {
		return fmt.Errorf("state root not writable: %w", err)
	}
	if err := os.Remove(probe); err != nil {
		return fmt.Errorf("state root probe not removable: %w", err)
	}
	sweepStaleTmp(p.Root, time.Now().Add(-time.Hour))
	return nil
}

// sweepStaleTmp removes *.tmp files under root older than cutoff — best effort, never fatal.
func sweepStaleTmp(root string, cutoff time.Time) {
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // hygiene is best-effort
		}
		if filepath.Ext(path) != ".tmp" {
			return nil
		}
		if fi, err := d.Info(); err == nil && fi.ModTime().Before(cutoff) {
			_ = os.Remove(path)
		}
		return nil
	})
}
