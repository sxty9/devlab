package runs

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// The busy marker is how a PRIVILEGED outsider — the root deploy wrapper — learns that devlabd is
// mid-run, WITHOUT a session or an API call. It exists exactly while AT LEAST ONE run is executing, and
// it carries the devlabd PID so a stale marker (crash, kill -9) is recognisable rather than blocking
// forever.
//
// It is REFCOUNT-aware: several runs now execute concurrently, so the marker holds the active-run COUNT
// (not a single run id). It is written when the first run begins and removed only when the LAST run ends —
// a boolean would clear the instant the first of several runs finished and let a deploy restart devlabd
// while others were still live. The COUNT keeps the restart deferred until the floor is truly empty.
//
// Why it exists: deploying the DevLab service itself restarts devlabd, which would kill every run in
// flight. Historically that was "solved" by never dev-deploying the own repo at all — so the one service
// the owner watches never updated from its own runs. With this marker the deploy INSTALLS immediately
// (harmless) and defers only the RESTART until the floor is free.

// BusyMarkerPath is the file the scheduler holds while any run executes. It sits beside runs.json, in a
// directory the root deploy helper can read.
func BusyMarkerPath() string {
	if p := os.Getenv("DEVLAB_MERCURY_BUSY"); p != "" {
		return p
	}
	return filepath.Join(filepath.Dir(runsPath()), "run-active")
}

// markActive records how many runs occupy the floor: "<pid> <count> <RFC3339>". The PID stays the FIRST
// field so the deploy helper's `kill -0 pid` staleness check is unchanged. n<=0 removes the marker (the
// floor is empty). Best-effort — an unwritable marker must never stop a run from executing; the deploy
// helper then simply falls back to its bounded wait.
func markActive(n int) {
	p := BusyMarkerPath()
	if n <= 0 {
		_ = os.Remove(p)
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	line := fmt.Sprintf("%d %d %s\n", os.Getpid(), n, time.Now().UTC().Format(time.RFC3339))
	tmp := p + ".tmp"
	if os.WriteFile(tmp, []byte(line), 0o644) == nil {
		_ = os.Rename(tmp, p)
	}
}

// clearBusy removes the marker unconditionally (startup: no run can be in flight yet). Safe when absent.
func clearBusy() {
	_ = os.Remove(BusyMarkerPath())
}
