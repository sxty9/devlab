package deploy

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stageWrappers writes the four self wrappers into a checkout's deploy/ dir and an equal set into a
// fake sbin dir, then points the drift probe at that sbin dir. It returns the checkout root.
func stageWrappers(t *testing.T, repoContent, sbinContent map[string]string) string {
	t.Helper()
	wt := t.TempDir()
	sbin := t.TempDir()
	for _, name := range selfWrappers {
		if c, ok := repoContent[name]; ok {
			writeExec(t, filepath.Join(wt, "deploy", name), c)
		}
		if c, ok := sbinContent[name]; ok {
			writeExec(t, filepath.Join(sbin, name), c)
		}
	}
	// wrapperInstallDir is read once at package load from DEVLAB_SBIN_DIR; point it at this test's
	// fake sbin dir directly (the env var is the production/read seam, exercised by callers).
	old := wrapperInstallDir
	wrapperInstallDir = sbin
	t.Cleanup(func() { wrapperInstallDir = old })
	return wt
}

func writeExec(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

// identical returns a repo/sbin content pair where every wrapper matches byte-for-byte.
func identical() map[string]string {
	m := map[string]string{}
	for _, name := range selfWrappers {
		m[name] = "#!/usr/bin/env bash\n# " + name + " v1\n"
	}
	return m
}

// TestGuardWrappersCurrentPassesWhenInstalledMatches: no drift ⇒ no refusal, delivery may proceed.
func TestGuardWrappersCurrentPassesWhenInstalledMatches(t *testing.T) {
	wt := stageWrappers(t, identical(), identical())
	if err := GuardWrappersCurrent(wt, ""); err != nil {
		t.Fatalf("matching wrappers must not refuse the delivery, got: %v", err)
	}
}

// TestGuardWrappersCurrentRefusesStaleRootScript is THE proof (point 6): a stale installed
// devlab-install must produce the NAMED refusal — not "should now be noticed", an actual failure
// that names the wrapper and the fix, and is NOT satisfied.
func TestGuardWrappersCurrentRefusesStaleRootScript(t *testing.T) {
	repo := identical()
	repo["devlab-install"] = "#!/usr/bin/env bash\n# devlab-install v2 — the fixed copy step\n"
	sbin := identical() // sbin still carries devlab-install v1

	wt := stageWrappers(t, repo, sbin)

	err := GuardWrappersCurrent(wt, "")
	if err == nil {
		t.Fatal("a stale installed root wrapper must fail the delivery, got nil")
	}
	if !errors.Is(err, ErrWrappersStale) {
		t.Fatalf("the refusal must match ErrWrappersStale, got: %v", err)
	}
	msg := err.Error()
	// It must NAME the stale wrapper …
	if !strings.Contains(msg, "devlab-install") {
		t.Errorf("the refusal must name the stale wrapper devlab-install: %q", msg)
	}
	// … and say WHAT TO DO, on the first line (the motor records only the first line as the reason).
	first := strings.SplitN(msg, "\n", 2)[0]
	if !strings.Contains(first, "sudo") || !strings.Contains(first, "service") {
		t.Errorf("the first line must carry the one-line fix (sudo … service setup): %q", first)
	}
	// A wrapper that DID match must not be reported as stale.
	if strings.Contains(msg, "devlab-exec") {
		t.Errorf("a matching wrapper must not appear in the refusal: %q", msg)
	}
}

// TestGuardWrappersCurrentRefusesMissingInstall: a wrapper the repo ships but that is not installed
// at all is stale too (the running daemon calls a wrapper the delivered code expects to exist).
func TestGuardWrappersCurrentRefusesMissingInstall(t *testing.T) {
	repo := identical()
	sbin := identical()
	delete(sbin, "devlab-restart-when-free") // never installed

	wt := stageWrappers(t, repo, sbin)

	err := GuardWrappersCurrent(wt, "")
	if !errors.Is(err, ErrWrappersStale) {
		t.Fatalf("a not-installed wrapper must fail the delivery, got: %v", err)
	}
	if !strings.Contains(err.Error(), "not installed") {
		t.Errorf("the refusal must state the wrapper is not installed: %q", err.Error())
	}
}

// TestCheckWrapperDriftReportsAllStale: several stale wrappers are all named, sorted, and a wrapper
// the repository does not ship is silently skipped (not this repo's to keep current).
func TestCheckWrapperDriftReportsAllStale(t *testing.T) {
	repo := identical()
	repo["devlab-exec"] = "changed-a\n"
	repo["devlab-install"] = "changed-b\n"
	delete(repo, "devlab-mkworkspace") // repo does not ship it here
	sbin := identical()

	wt := stageWrappers(t, repo, sbin)

	drifts, err := CheckWrapperDrift(wt, "")
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(drifts))
	for i, d := range drifts {
		got[i] = d.Name
	}
	want := []string{"devlab-exec", "devlab-install"} // sorted; mkworkspace skipped, restart matches
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("drift set = %v, want %v", got, want)
	}
}
