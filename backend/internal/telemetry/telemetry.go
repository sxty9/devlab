// Package telemetry reports — and never judges (cross-cutting 5): the ONE AI-usage pool
// (assistant, agent and runs all record here), the portioned storage report, and the process's
// own load. Evaluation happens outside the service.
package telemetry

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"devlab/backend/internal/fsatomic"
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

// ErrNoPool means no state root was configured, so there is nowhere to record. It is returned
// rather than silently swallowed: a consumption sample that is dropped must be visible as such.
var ErrNoPool = errors.New("telemetry: no AI-usage pool configured")

// usageCap bounds the rolling window by count and usageMaxAge by age — the pool is a report, not
// an archive, so it stays portioned in both directions.
const (
	usageCap    = 5000
	usageMaxAge = 30 * 24 * time.Hour
)

// UsageLedger is the ONE AI-usage pool (mercury/ai-usage.json), capped as a rolling window.
type UsageLedger struct {
	path string
	mu   sync.Mutex
}

type usageFile struct {
	Samples []UsageSample `json:"samples"`
}

// OpenUsage opens the ledger below the state root.
func OpenUsage(p *statepath.Paths) *UsageLedger {
	var path string
	if p != nil {
		path = p.AiUsage()
	}
	return &UsageLedger{path: path}
}

// OpenUsageAt opens a ledger backed by an explicit path (tests).
func OpenUsageAt(path string) *UsageLedger { return &UsageLedger{path: path} }

func (u *UsageLedger) load() ([]UsageSample, error) {
	b, err := os.ReadFile(u.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var f usageFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	return f.Samples, nil
}

// Record appends one sample (atomic rewrite of the capped window). Samples are kept oldest
// first; the window drops what is older than usageMaxAge and, beyond usageCap entries, the
// oldest surviving ones.
func (u *UsageLedger) Record(s UsageSample) error {
	if u == nil || u.path == "" {
		return ErrNoPool
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	cur, err := u.load()
	if err != nil {
		return err
	}
	if s.At.IsZero() {
		s.At = time.Now().UTC()
	} else {
		s.At = s.At.UTC()
	}
	next := append(cur, s)
	cutoff := s.At.Add(-usageMaxAge)
	kept := make([]UsageSample, 0, len(next))
	for _, smp := range next {
		if smp.At.Before(cutoff) {
			continue
		}
		kept = append(kept, smp)
	}
	if len(kept) > usageCap {
		kept = kept[len(kept)-usageCap:]
	}
	return fsatomic.WriteJSON(u.path, usageFile{Samples: kept})
}

// Aggregate reports usage over a window, per source — reported, never judged. A window of zero
// or less reports the whole pool. CostUSD stays zero here on purpose: a sample carries tokens
// only, and pricing tokens is a valuation that happens outside the service (an execution's own
// UsageView carries the figure its engine reported).
func (u *UsageLedger) Aggregate(window time.Duration) (model.AiUsageView, error) {
	view := model.AiUsageView{BySource: map[string]model.UsageView{}}
	if u == nil || u.path == "" {
		return view, nil
	}
	u.mu.Lock()
	samples, err := u.load()
	u.mu.Unlock()
	if err != nil {
		return view, err
	}
	cutoff := time.Time{}
	if window > 0 {
		view.WindowHours = int(window.Hours())
		cutoff = time.Now().UTC().Add(-window)
	}
	for _, s := range samples {
		if !cutoff.IsZero() && s.At.Before(cutoff) {
			continue
		}
		view.Samples++
		view.Totals.InputTokens += s.In
		view.Totals.OutputTokens += s.Out
		src := s.Source
		if src == "" {
			src = "unknown"
		}
		agg := view.BySource[src]
		agg.InputTokens += s.In
		agg.OutputTokens += s.Out
		view.BySource[src] = agg
	}
	return view, nil
}

// pool names one reported storage pool and where it lives. The names are the pools' own names
// from the persistence inventory — the same vocabulary the stores use (REQ-040.4).
type pool struct {
	name string
	path string
}

// pools lists the pools a storage report covers: one row per pool, not per file — the report is
// portioned by construction.
func pools(p *statepath.Paths) []pool {
	return []pool{
		{"runs", p.Runs()},
		{"executions", p.Executions()},
		{"deliveries", p.Deliveries()},
		{"pull-requests", p.PRs()},
		{"notices", p.NoticesFile()},
		{"settings", p.Settings()},
		{"run-history", p.HistoryDir()},
		{"attachments", p.Attachments()},
		{"axiom-authors", p.AxiomAuthors()},
		{"axiom-checks", p.AxiomChecks()},
		{"daily-reports", p.DailyReports()},
		{"ai-usage", p.AiUsage()},
		{"legacy-results", p.LegacyResults()},
		{"links", p.Links()},
		{"chats", p.Chats()},
		{"comments", p.Comments()},
		{"workspaces", p.Workspaces()},
		{"axioms-clone", p.AxiomsClone()},
		{"www", p.Www()},
	}
}

// Storage reports the portioned occupancy of every pool below the state root. A pool that does
// not exist yet is left out rather than reported as an empty one — absence is not occupancy. The
// service reports; whether a size is too large is judged elsewhere.
func Storage(p *statepath.Paths) (model.StorageView, error) {
	view := model.StorageView{Pools: []model.PoolUsage{}}
	if p == nil || p.Root == "" {
		return view, errors.New("telemetry: no state root configured")
	}
	for _, pl := range pools(p) {
		bytes, files, ok := measure(pl.path)
		if !ok {
			continue
		}
		view.Pools = append(view.Pools, model.PoolUsage{Name: pl.name, Bytes: bytes, Files: files})
		view.TotalBytes += bytes
	}
	sort.Slice(view.Pools, func(i, j int) bool { return view.Pools[i].Bytes > view.Pools[j].Bytes })
	return view, nil
}

// measure sizes one pool: a file by its own size, a directory by walking it. The walk is complete
// on purpose — a capped one would report a figure that looks exact and is not, and the reported
// shape has no way to say "partial"; the report is asked for rarely, so its cost is paid honestly.
// An unreadable entry contributes nothing and never fails the report.
func measure(path string) (bytes int64, files int, ok bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, 0, false
	}
	if !fi.IsDir() {
		return fi.Size(), 1, true
	}
	_ = filepath.WalkDir(path, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // an unreadable subtree is skipped, never fatal
		}
		if info, ierr := d.Info(); ierr == nil {
			bytes += info.Size()
			files++
		}
		return nil
	})
	return bytes, files, true
}

// processStart is when this process began, the baseline of the reported CPU share.
var processStart = time.Now()

// Load reports the process's own CPU/RSS/goroutine claim. CPUPercent is the average share of one
// core since start (cumulative CPU time ÷ uptime) — a figure, not a verdict. Where the kernel's
// process interface is unavailable, CPU and RSS are reported as zero rather than guessed.
func Load() model.LoadView {
	view := model.LoadView{Goroutines: runtime.NumGoroutine()}
	cpu, rss, ok := procSelf()
	if !ok {
		return view
	}
	view.RSSBytes = rss
	if up := time.Since(processStart).Seconds(); up > 0 {
		view.CPUPercent = cpu / up * 100
	}
	return view
}

// procSelf reads cumulative CPU seconds and resident bytes from the kernel's own process
// interface (/proc/self). It is the operating system's report about us — not a stored figure.
func procSelf() (cpuSeconds float64, rssBytes int64, ok bool) {
	b, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0, 0, false
	}
	// The comm field may contain spaces inside parentheses; fields are counted after it.
	line := string(b)
	end := strings.LastIndex(line, ")")
	if end < 0 || end+2 >= len(line) {
		return 0, 0, false
	}
	fields := strings.Fields(line[end+2:])
	// After comm and state, utime is field 11 and stime field 12 of the original numbering,
	// i.e. index 11 and 12 here (state is index 0).
	if len(fields) < 22 {
		return 0, 0, false
	}
	utime, err1 := strconv.ParseInt(fields[11], 10, 64)
	stime, err2 := strconv.ParseInt(fields[12], 10, 64)
	rss, err3 := strconv.ParseInt(fields[21], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, 0, false
	}
	const ticksPerSecond = 100 // the kernel's USER_HZ on every platform DevLab is deployed on
	return float64(utime+stime) / ticksPerSecond, rss * int64(os.Getpagesize()), true
}
