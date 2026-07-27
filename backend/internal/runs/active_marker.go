package runs

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// The active-run marker is how a PRIVILEGED outsider — the root deploy helper (devlab-restart-idle) —
// learns that devlabd is mid-run, WITHOUT a session or an API call. It exists exactly while AT LEAST ONE
// run occupies a scheduler slot, and it carries the devlabd PID as its FIRST field so a stale marker
// (crash, kill -9) is recognisable (the helper does `kill -0 <pid>`) rather than blocking forever.
//
// Why it exists: deploying the DevLab service itself restarts devlabd, which would kill any run in
// flight. Historically that was "solved" by never dev-deploying the own repo at all — so the one service
// the owner watches never updated from its own runs. With this marker the deploy INSTALLS immediately
// (harmless) and defers only the RESTART until the last slot is free.
//
// It is a REFCOUNT, not a toggle: written when the first run starts and removed only when the LAST run
// ends. A marker that merely toggled would clear the instant the first of several CONCURRENT runs
// finished — and the deploy would then restart devlabd out from under the ones still working.

// BusyMarkerPath is the file the scheduler holds while any run executes. It sits beside runs.json, in a
// directory the root deploy helper can read. DEVLAB_MERCURY_BUSY overrides (the helper reads the same
// env), so the two always agree on the path.
func BusyMarkerPath() string {
	if p := os.Getenv("DEVLAB_MERCURY_BUSY"); p != "" {
		return p
	}
	return filepath.Join(filepath.Dir(runsPath()), "run-active")
}

// markActive keeps the marker in step with the live run count n. n>0 writes "<pid> <n> run(s) since
// <RFC3339>" — field 1 is the devlabd PID (the helper's staleness probe), the rest is human context.
// n==0 removes it. Best-effort: an unwritable marker must never stop a run from executing; the deploy
// helper then simply falls back to its bounded wait. The caller serialises calls (the scheduler holds
// its admission lock), so no concurrent writer races here.
func markActive(n int) {
	p := BusyMarkerPath()
	if n <= 0 {
		_ = os.Remove(p)
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	line := fmt.Sprintf("%d %d run(s) since %s\n", os.Getpid(), n, time.Now().UTC().Format(time.RFC3339))
	tmp := p + ".tmp"
	if os.WriteFile(tmp, []byte(line), 0o644) == nil {
		_ = os.Rename(tmp, p)
	}
}

// clearBusy removes the marker unconditionally — used at scheduler start-up, where no run can yet be in
// flight so any marker left by a killed predecessor is provably stale. Safe when it does not exist.
func clearBusy() { markActive(0) }
