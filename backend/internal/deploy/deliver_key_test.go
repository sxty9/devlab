package deploy

// ONE key per setup call. The install wrapper builds the unit, the service account, the route and
// the binary name from the repository name it is handed; the port ledger and the running gate key on
// the DETECTED service id. A repository whose rights manifest names a different id (a very ordinary
// shape: repository `holistic-scrapr` carrying `permissions/scrapr.json`) used to split the call
// silently, which is what these tests forbid.

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// recordingPorts remembers which key the port ledger was asked about — and whether it was asked at
// all, because a refused delivery must not reserve a port either.
type recordingPorts struct {
	asked []string
	port  int
}

func (p *recordingPorts) PortFor(_ context.Context, service string, _ int) (int, bool, error) {
	p.asked = append(p.asked, service)
	return p.port, false, nil
}

// A repository name that differs from the detected service id is refused BEFORE any effect: no port
// reservation, no wrapper call, and an error that names both keys so the operator can fix the repo.
func TestDeliverDevRefusesDivergingKeys(t *testing.T) {
	ri := &fakeInstaller{}
	ports := &recordingPorts{port: 8772}

	out, err := DeliverDev(context.Background(), ri, ports, greenGate(),
		Detection{Kind: KindService, ID: "scrapr", Evidence: "./service CLI (template convention)"},
		"holistic-scrapr", "/work/holistic-scrapr/.mercury-artifact")

	if !errors.Is(err, ErrIDMismatch) {
		t.Fatalf("err = %v, want ErrIDMismatch", err)
	}
	if len(ri.calls) != 0 {
		t.Errorf("a diverging key must never reach the installer: %v", ri.calls)
	}
	if len(ports.asked) != 0 {
		t.Errorf("a diverging key must not reserve a port either: %v", ports.asked)
	}
	if out.Installed || out.Running || out.Port != 0 {
		t.Errorf("outcome must stay empty and honest: %+v", out)
	}
	for _, want := range []string{"holistic-scrapr", "scrapr"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must name both keys, missing %q: %v", want, err)
		}
	}
}

// A service detected without an id is the same defect seen from the other side: the wrapper would be
// handed the repository name while the gate probed an empty unit name (which systemd resolves to
// nothing at all, so the gate could not fail honestly).
func TestDeliverDevRefusesEmptyServiceID(t *testing.T) {
	ri := &fakeInstaller{}
	ports := &recordingPorts{port: 8773}
	_, err := DeliverDev(context.Background(), ri, ports, greenGate(),
		Detection{Kind: KindService, Evidence: "cmd layout"}, "svc-a", "/work/svc-a/.mercury-artifact")
	if !errors.Is(err, ErrIDMismatch) {
		t.Fatalf("err = %v, want ErrIDMismatch", err)
	}
	if len(ri.calls) != 0 || len(ports.asked) != 0 {
		t.Errorf("nothing may happen: installer=%v ports=%v", ri.calls, ports.asked)
	}
}

// The agreeing case proves the enforcement, not just the refusal: ONE key reaches the port ledger,
// the wrapper and the gate.
func TestDeliverDevUsesTheSameKeyEverywhere(t *testing.T) {
	ri := &fakeInstaller{}
	ports := &recordingPorts{port: 8774}
	var gated []string
	gate := Gate{
		UnitActive: func(_ context.Context, unit string) error { gated = append(gated, unit); return nil },
		PortHeld:   func(context.Context, int) error { return nil },
	}

	out, err := DeliverDev(context.Background(), ri, ports, gate,
		Detection{Kind: KindService, ID: "svc-a", Evidence: "./service"}, "svc-a", "/work/svc-a/.mercury-artifact")
	if err != nil {
		t.Fatal(err)
	}
	if !out.Installed || !out.Running {
		t.Fatalf("outcome = %+v, want installed and running", out)
	}
	if len(ports.asked) != 1 || ports.asked[0] != "svc-a" {
		t.Errorf("port ledger asked about %v, want [svc-a]", ports.asked)
	}
	if len(gated) != 1 || gated[0] != "svc-a" {
		t.Errorf("gate probed %v, want [svc-a]", gated)
	}
	if len(ri.calls) != 1 || !strings.HasPrefix(ri.calls[0], "svc-a ") {
		t.Errorf("installer called with %v, want the same key first", ri.calls)
	}
}
