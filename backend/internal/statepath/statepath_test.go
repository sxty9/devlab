package statepath

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFromEnvPrefersDevlabStateDir(t *testing.T) {
	t.Setenv("DEVLAB_STATE_DIR", "/tmp/a")
	t.Setenv("STATE_DIRECTORY", "/tmp/b")
	p, err := FromEnv()
	if err != nil || p.Root != "/tmp/a" {
		t.Fatalf("FromEnv = %v, %v; want root /tmp/a", p, err)
	}
}

func TestFromEnvSystemdFallbackAndError(t *testing.T) {
	t.Setenv("DEVLAB_STATE_DIR", "")
	t.Setenv("STATE_DIRECTORY", "/tmp/sd:/tmp/other")
	p, err := FromEnv()
	if err != nil || p.Root != "/tmp/sd" {
		t.Fatalf("FromEnv = %v, %v; want root /tmp/sd", p, err)
	}
	t.Setenv("STATE_DIRECTORY", "")
	if _, err := FromEnv(); err == nil {
		t.Fatal("FromEnv without any env must error (no baked-in instance path)")
	}
}

func TestPathLiterals(t *testing.T) {
	p := &Paths{Root: "/r"}
	got := map[string]string{
		p.Mercury():       "/r/mercury",
		p.Runs():          "/r/mercury/runs.json",
		p.Executions():    "/r/mercury/executions",
		p.Execution("e1"): "/r/mercury/executions/e1",
		p.Deliveries():    "/r/mercury/runs-deliveries.json",
		p.PRs():           "/r/mercury/runs-prs.json",
		p.NoticesFile():   "/r/mercury/runs-notices.json",
		p.Settings():      "/r/mercury/settings.json",
		p.HistoryDir():    "/r/mercury/runs-history",
		p.Attachments():   "/r/mercury/attachments",
		p.AxiomAuthors():  "/r/mercury/axiom-authors.json",
		p.AxiomChecks():   "/r/mercury/axiom-checks.json",
		p.DailyReports():  "/r/mercury/daily-reports.json",
		p.AiUsage():       "/r/mercury/ai-usage.json",
		p.RestartMarker(): "/r/mercury/restart.json",
		p.UsageLimit():    "/r/mercury/usage-limit.json",
		p.ReadySocket():   "/r/restart-ready.sock",
		p.LegacyResults(): "/r/mercury/runs-results",
		p.Links():         "/r/links",
		p.Chats():         "/r/chats",
		p.Comments():      "/r/comments",
		p.Workspaces():    "/r/workspaces",
		p.Www():           "/r/www",
		p.AxiomsClone():   "/r/axioms",
		p.MercuryOrder():  "/r/mercury/order.json",
	}
	for have, want := range got {
		if have != want {
			t.Errorf("path = %q, want %q", have, want)
		}
	}
}

func TestCheckWritableProbesAndSweeps(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "old.json.tmp")
	fresh := filepath.Join(dir, "new.json.tmp")
	if err := os.WriteFile(stale, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fresh, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	p := &Paths{Root: dir}
	if err := p.CheckWritable(); err != nil {
		t.Fatalf("CheckWritable: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "probe")); !os.IsNotExist(err) {
		t.Fatal("probe file must be removed again")
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("stale .tmp (>1h) must be swept")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("fresh .tmp must survive the sweep")
	}
}
