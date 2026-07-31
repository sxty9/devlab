package deploy

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"devlab/backend/internal/workspace"
)

// The prod send runs ONLY against a fixture target in this phase: the artifact lands in the
// staging directory, and with a nil Trigger nothing is ever installed — the path is implemented
// but NOT armed.
func TestSendProdAgainstFixtureTarget(t *testing.T) {
	art := t.TempDir()
	if err := os.WriteFile(filepath.Join(art, "svc-ad"), []byte("prebuilt-bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	staging := t.TempDir() // the fixture rsync target — a local directory, never a live host

	if err := SendProd(context.Background(), ProdConfig{RsyncTarget: staging}, "svc-a", art); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(staging, "svc-a", "svc-ad"))
	if err != nil || string(got) != "prebuilt-bytes" {
		t.Fatalf("artifact not staged under <target>/<repo>/: %v %q", err, got)
	}
}

func TestSendProdTriggerSeam(t *testing.T) {
	art := t.TempDir()
	if err := os.WriteFile(filepath.Join(art, "x"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	fired := ""
	cfg := ProdConfig{
		RsyncTarget: t.TempDir(),
		Trigger:     func(_ context.Context, repo string) error { fired = repo; return nil },
	}
	if err := SendProd(context.Background(), cfg, "svc-a", art); err != nil {
		t.Fatal(err)
	}
	if fired != "svc-a" {
		t.Fatalf("trigger fired for %q, want svc-a", fired)
	}
}

func TestSendProdValidation(t *testing.T) {
	art := t.TempDir()
	// Repo grammar guards the remote path composition.
	if err := SendProd(context.Background(), ProdConfig{RsyncTarget: t.TempDir()}, "../evil", art); err == nil {
		t.Fatal("a traversal repo name must be rejected")
	}
	if err := SendProd(context.Background(), ProdConfig{RsyncTarget: t.TempDir()}, "UPPER", art); err == nil {
		t.Fatal("the name grammar is lowercase")
	}
	// The target comes from server-side config; an empty one is an honest refusal.
	if err := SendProd(context.Background(), ProdConfig{}, "svc-a", art); err == nil {
		t.Fatal("a missing prod target must refuse")
	}
	// No prebuilt artifact ⇒ nothing to ship.
	if err := SendProd(context.Background(), ProdConfig{RsyncTarget: t.TempDir()}, "svc-a",
		filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("a missing artifact must refuse")
	}
}

// Build is a thin, honest delegation: it refuses outside per-user execution (root or the
// service user must never build agent code) and surfaces the toolchain's own words.
func TestBuildRequiresPerUserExecution(t *testing.T) {
	_, err := Build(context.Background(), workspace.Executor{User: "someone", PerUser: false}, t.TempDir())
	if err == nil {
		t.Fatal("a non-per-user build must be refused (root never builds)")
	}
}
