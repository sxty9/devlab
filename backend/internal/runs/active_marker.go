package runs

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// The busy marker is how a PRIVILEGED outsider — the root deploy wrapper — learns that devlabd is
// mid-run, WITHOUT a session or an API call. It exists exactly while a run occupies the scheduler's
// single slot, and it carries the devlabd PID so a stale marker (crash, kill -9) is recognisable
// rather than blocking forever.
//
// Why it exists: deploying the DevLab service itself restarts devlabd, which would kill the run in
// flight. Historically that was "solved" by never dev-deploying the own repo at all — so the one
// service the owner watches never updated from its own runs. With this marker the deploy INSTALLS
// immediately (harmless) and defers only the RESTART until the slot is free.

// BusyMarkerPath is the file the scheduler holds while a run executes. It sits beside runs.json, in a
// directory the root deploy helper can read.
func BusyMarkerPath() string {
	if p := os.Getenv("DEVLAB_MERCURY_BUSY"); p != "" {
		return p
	}
	return filepath.Join(filepath.Dir(runsPath()), "run-active")
}

// markBusy records that a run occupies the slot: "<pid> <runID> <RFC3339 start>". Best-effort — an
// unwritable marker must never stop a run from executing; the deploy helper then simply falls back to
// its bounded wait.
func markBusy(runID string) {
	p := BusyMarkerPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	line := fmt.Sprintf("%d %s %s\n", os.Getpid(), runID, time.Now().UTC().Format(time.RFC3339))
	tmp := p + ".tmp"
	if os.WriteFile(tmp, []byte(line), 0o644) == nil {
		_ = os.Rename(tmp, p)
	}
}

// clearBusy releases the marker. Safe to call when it does not exist.
func clearBusy() {
	_ = os.Remove(BusyMarkerPath())
}
