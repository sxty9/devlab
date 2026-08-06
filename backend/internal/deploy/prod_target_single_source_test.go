package deploy

// The production target has exactly ONE source: the runtime environment the daemon reads
// (DEVLAB_RUNS_PROD_TARGET / _RECV / _KEY, delivered as the devlabd systemd drop-in). The file
// /etc/devlab/prod-target that a reinstall left behind steered nothing — a card-index corpse that can
// lie: someone edits it and changes nothing. This guard fails if any daemon source starts reading it,
// so a second data path to the same entity can never quietly reappear (Zugangspunkt wiederverwenden).

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNoDaemonCodeReadsDeadProdTarget(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("no caller info")
	}
	backendRoot := filepath.Dir(filepath.Dir(filepath.Dir(file))) // internal/deploy → internal → backend

	var offenders []string
	err := filepath.Walk(backendRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		// Only Go sources; the guard's own test names the string legitimately.
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(string(b), "prod-target") {
			offenders = append(offenders, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Errorf("no daemon code may read the dead /etc/devlab/prod-target — the ONE source is the "+
			"runtime environment (DEVLAB_RUNS_PROD_TARGET / _RECV / _KEY). Offending files: %v", offenders)
	}
}
