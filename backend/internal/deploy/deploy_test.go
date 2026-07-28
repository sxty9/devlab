package deploy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/model"
)

// ─── fixture repos ───────────────────────────────────────────────────────────

func mkRepo(t *testing.T, files map[string]string, execs ...string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, rel := range execs {
		if err := os.Chmod(filepath.Join(dir, rel), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// templateServiceRepo is a freshly generated template-conforming service: the ./service CLI,
// the daemon cmd, and the rights manifest carrying its id.
func templateServiceRepo(t *testing.T, id string) string {
	return mkRepo(t, map[string]string{
		"service":                       "#!/usr/bin/env bash\n# uniform holistic service CLI\n",
		"permissions/" + id + ".json":   `{"service":"` + id + `","categories":[]}`,
		"backend/cmd/" + id + "d/main.go": "package main\nfunc main() {}\n",
		"backend/go.mod":                "module example.invalid/" + id + "\n",
	}, "service")
}

// ─── Detect: every kind with evidence (REQ-029, B-04) ────────────────────────

func TestDetectServiceWithCLI(t *testing.T) {
	d, err := Detect(templateServiceRepo(t, "svc-a"))
	if err != nil {
		t.Fatal(err)
	}
	if d.Kind != KindService || d.ID != "svc-a" {
		t.Fatalf("got %+v, want service svc-a", d)
	}
	if !strings.Contains(d.Evidence, "./service") {
		t.Errorf("evidence should name the CLI marker: %q", d.Evidence)
	}
}

func TestDetectServiceByDaemonLayout(t *testing.T) {
	dir := mkRepo(t, map[string]string{
		"backend/cmd/food/main.go": "package main\n",
		"backend/go.mod":           "module example.invalid/foo\n",
	})
	d, err := Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if d.Kind != KindService || d.ID != "foo" || !strings.Contains(d.Evidence, "cmd/food") {
		t.Fatalf("got %+v, want service foo evidenced by cmd/food", d)
	}
}

func TestDetectLibrary(t *testing.T) {
	dir := mkRepo(t, map[string]string{"go.mod": "module example.invalid/lib\n", "lib.go": "package lib\n"})
	d, err := Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if d.Kind != KindLibrary || d.Evidence == "" {
		t.Fatalf("got %+v, want an evidenced library", d)
	}
}

func TestDetectExcluded(t *testing.T) {
	dir := mkRepo(t, map[string]string{
		"holistic-service.json":    `{"deliver": false}`,
		"backend/cmd/bard/main.go": "package main\n",
	})
	d, err := Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if d.Kind != KindExcluded || !strings.Contains(d.Evidence, "deliver:false") {
		t.Fatalf("got %+v, want excluded evidenced by the declaration", d)
	}
}

func TestDetectNonconforming(t *testing.T) {
	// Declares delivery values but conforms to nothing.
	dir := mkRepo(t, map[string]string{"holistic-service.json": `{"port": 8790}`, "main.go": "package main\n"})
	d, err := Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if d.Kind != KindNonconforming || !strings.Contains(d.Evidence, DeclarationFileName) {
		t.Fatalf("got %+v, want nonconforming evidenced by the declaration", d)
	}

	// A malformed declaration is a violation too, never a guess.
	bad := mkRepo(t, map[string]string{"holistic-service.json": `{not json`})
	d, err = Detect(bad)
	if err != nil {
		t.Fatal(err)
	}
	if d.Kind != KindNonconforming || !strings.Contains(d.Evidence, "unreadable") {
		t.Fatalf("got %+v, want nonconforming for a malformed declaration", d)
	}
}

func TestDetectTemplate(t *testing.T) {
	dir := mkRepo(t, map[string]string{
		"service":                        "#!/usr/bin/env bash\n",
		"permissions/myservice.json":     `{"service":"myservice"}`,
		"backend/cmd/myserviced/main.go": "package main\n",
	}, "service")
	d, err := Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if d.Kind != KindTemplate || !strings.Contains(d.Evidence, "myservice") {
		t.Fatalf("got %+v, want the pristine template", d)
	}
}

func TestDetectMissingDirErrors(t *testing.T) {
	if _, err := Detect(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("expected an error for a missing repo dir")
	}
}

// ─── FindGaps: "delivery not yet set up" ≠ "no service", never green (REQ-029) ─

func TestFindGapsDistinguishesStates(t *testing.T) {
	dets := map[string]Detection{
		"svc-new":   {Kind: KindService, ID: "svc-new", Evidence: "./service CLI (template convention)"},
		"svc-live":  {Kind: KindService, ID: "svc-live", Evidence: "./service CLI (template convention)"},
		"lib":       {Kind: KindLibrary, Evidence: "no ./service CLI and no cmd/<id>d daemon — nothing to deliver"},
		"template":  {Kind: KindTemplate, Evidence: "pristine service template"},
		"opted-out": {Kind: KindExcluded, Evidence: "declares deliver:false"},
		"weird":     {Kind: KindNonconforming, Evidence: "declaration present, but nothing conforms"},
	}
	allocs := []model.PortAllocation{{Port: 8771, Service: "svc-live", Routed: true, Bound: true}}

	gaps := FindGaps(dets, allocs)
	byRepo := map[string]Gap{}
	for _, g := range gaps {
		byRepo[g.Repo] = g
	}

	g, ok := byRepo["svc-new"]
	if !ok {
		t.Fatalf("the un-provisioned service must be a gap: %+v", gaps)
	}
	if !strings.Contains(g.Detail, "delivery not yet set up") {
		t.Errorf("the gap must say 'delivery not yet set up', never 'no service': %q", g.Detail)
	}
	if g.Kind != KindService {
		t.Errorf("the gap keeps the SERVICE classification (it is a service, only undelivered): %+v", g)
	}
	if _, ok := byRepo["weird"]; !ok {
		t.Errorf("a nonconforming repo is reported as a violation: %+v", gaps)
	}
	for _, repo := range []string{"svc-live", "lib", "template", "opted-out"} {
		if _, ok := byRepo[repo]; ok {
			t.Errorf("%s must not be a gap: %+v", repo, byRepo[repo])
		}
	}
}

// ─── DeliverDev ──────────────────────────────────────────────────────────────

type fakeInstaller struct {
	calls []string
	err   error
}

func (f *fakeInstaller) Install(_ context.Context, repo, artifactDir, env string, port int, handover bool) error {
	f.calls = append(f.calls, fmt.Sprintf("%s %s %s port=%d handover=%v", repo, artifactDir, env, port, handover))
	return f.err
}

type fakePorts struct {
	port      int
	firstTime bool
	err       error
}

func (f fakePorts) PortFor(context.Context, string, int) (int, bool, error) {
	return f.port, f.firstTime, f.err
}

func greenGate() Gate {
	return Gate{
		UnitActive: func(context.Context, string) error { return nil },
		PortHeld:   func(context.Context, int) error { return nil },
		Wait:       10 * time.Millisecond, Poll: time.Millisecond,
	}
}

func redGate(reason string) Gate {
	return Gate{
		UnitActive: func(context.Context, string) error { return errors.New(reason) },
		PortHeld:   func(context.Context, int) error { return nil },
		Wait:       5 * time.Millisecond, Poll: time.Millisecond,
	}
}

// Excluded never triggers an attempt (REQ-029.5): the installer is not called.
func TestDeliverDevExcludedNeverAttempts(t *testing.T) {
	ri := &fakeInstaller{}
	out, err := DeliverDev(context.Background(), ri, fakePorts{}, greenGate(),
		Detection{Kind: KindExcluded, Evidence: "declares deliver:false"}, "opted-out", "/tmp/x")
	if !errors.Is(err, ErrExcluded) {
		t.Fatalf("err = %v, want ErrExcluded", err)
	}
	if len(ri.calls) != 0 {
		t.Fatalf("excluded must never reach the installer: %v", ri.calls)
	}
	if out.Installed || out.Running {
		t.Errorf("outcome must stay honest: %+v", out)
	}
}

// Libraries and the template are refused with their evidence — no attempt either.
func TestDeliverDevNonServicesRefused(t *testing.T) {
	for kind, want := range map[Kind]error{
		KindLibrary:       ErrNotAService,
		KindTemplate:      ErrTemplateRepo,
		KindNonconforming: ErrNonconforming,
	} {
		ri := &fakeInstaller{}
		_, err := DeliverDev(context.Background(), ri, fakePorts{}, greenGate(),
			Detection{Kind: kind, Evidence: "e"}, "r", "/tmp/x")
		if !errors.Is(err, want) {
			t.Errorf("%s: err = %v, want %v", kind, err, want)
		}
		if len(ri.calls) != 0 {
			t.Errorf("%s must never reach the installer: %v", kind, ri.calls)
		}
	}
}

// A freshly generated template-conforming service is deliverable WITHOUT prior setup
// (REQ-028.3): detection succeeds, a free port is proposed, the one generic wrapper install runs.
func TestDeliverDevFreshTemplateServiceWithoutSetup(t *testing.T) {
	repoDir := templateServiceRepo(t, "svc-new")
	d, err := Detect(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	if d.Kind != KindService {
		t.Fatalf("fresh template service must detect as service: %+v", d)
	}

	ri := &fakeInstaller{}
	out, err := DeliverDev(context.Background(), ri, fakePorts{port: 8772, firstTime: true}, greenGate(),
		d, "svc-new", "/work/svc-new/.mercury-artifact")
	if err != nil {
		t.Fatal(err)
	}
	if len(ri.calls) != 1 {
		t.Fatalf("want exactly one generic install, got %v", ri.calls)
	}
	if !strings.Contains(ri.calls[0], "svc-new /work/svc-new/.mercury-artifact dev") ||
		!strings.Contains(ri.calls[0], "handover=false") {
		t.Errorf("install call = %q", ri.calls[0])
	}
	if !out.Installed || !out.Running || out.Port != 8772 {
		t.Errorf("outcome = %+v, want installed+running on 8772", out)
	}
	if !strings.Contains(out.Detail, "installed and started") {
		t.Errorf("detail = %q", out.Detail)
	}
}

// The honest gate (F10/REQ-044.3): install succeeded, but the unit never came up ⇒ the delivery
// FAILS with the reason — never green by default.
func TestDeliverDevHonestGateFailure(t *testing.T) {
	ri := &fakeInstaller{}
	out, err := DeliverDev(context.Background(), ri, fakePorts{port: 8773}, redGate("unit svc is not active"),
		Detection{Kind: KindService, ID: "svc", Evidence: "./service"}, "svc", "/tmp/a")
	if err == nil {
		t.Fatal("a not-running service must fail the delivery")
	}
	if !out.Installed || out.Running {
		t.Errorf("outcome = %+v, want installed but NOT running", out)
	}
	if !strings.Contains(out.Detail, "not running") {
		t.Errorf("detail must say the truth: %q", out.Detail)
	}
}

// An occupied desired port is rejected BEFORE any install; the error names holder + proposal.
func TestDeliverDevOccupiedPortRejectedBeforeInstall(t *testing.T) {
	ri := &fakeInstaller{}
	deliverErr := errors.New("port 8780 is held by aigentic; 8770 is free")
	out, err := DeliverDev(context.Background(), ri, fakePorts{err: deliverErr}, greenGate(),
		Detection{Kind: KindService, ID: "prizm", Evidence: "./service", Decl: &DeclarationFile{Port: 8780}},
		"prizm", "/tmp/a")
	if !errors.Is(err, deliverErr) {
		t.Fatalf("err = %v, want the port rejection", err)
	}
	if len(ri.calls) != 0 {
		t.Fatalf("a rejected port must never reach the installer: %v", ri.calls)
	}
	if !strings.Contains(out.Detail, "8770 is free") {
		t.Errorf("detail should carry the proposal: %q", out.Detail)
	}
}

// A failed wrapper install is an honest failure.
func TestDeliverDevInstallFailure(t *testing.T) {
	ri := &fakeInstaller{err: errors.New("exit status 2: rejected: artifact dir escapes the workspace root")}
	out, err := DeliverDev(context.Background(), ri, fakePorts{port: 8774}, greenGate(),
		Detection{Kind: KindService, ID: "svc", Evidence: "./service"}, "svc", "/tmp/a")
	if err == nil || out.Installed {
		t.Fatalf("want a failed install, got out=%+v err=%v", out, err)
	}
}

// Self repo: install + handover in ONE wrapper call; a failed handover FAILS the stage (K-2).
func TestSelfInstallAndHandover(t *testing.T) {
	ri := &fakeInstaller{}
	if err := SelfInstallAndHandover(context.Background(), ri, "devlab", "/work/self/.mercury-artifact"); err != nil {
		t.Fatal(err)
	}
	if len(ri.calls) != 1 || !strings.Contains(ri.calls[0], "handover=true") {
		t.Fatalf("want one install WITH handover, got %v", ri.calls)
	}

	ri = &fakeInstaller{err: errors.New("systemd-run failed")}
	if err := SelfInstallAndHandover(context.Background(), ri, "devlab", "/x"); err == nil ||
		!strings.Contains(err.Error(), "no inline restart") {
		t.Fatalf("a failed handover must fail the stage with the no-inline-restart doctrine, got %v", err)
	}
}

// The gate never reports running without probes, and it polls until the deadline.
func TestGateHonesty(t *testing.T) {
	if err := (Gate{Wait: time.Millisecond, Poll: time.Millisecond}).VerifyRunning(context.Background(), "u", 1); err == nil {
		t.Fatal("a probe-less gate must refuse to report running")
	}

	// Comes up on the third probe within the wait window.
	n := 0
	g := Gate{
		UnitActive: func(context.Context, string) error {
			n++
			if n < 3 {
				return errors.New("starting")
			}
			return nil
		},
		PortHeld: func(context.Context, int) error { return nil },
		Wait:     200 * time.Millisecond, Poll: time.Millisecond,
	}
	if err := g.VerifyRunning(context.Background(), "u", 8770); err != nil {
		t.Fatalf("late-arriving service within the window should pass: %v", err)
	}
}
